package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// These tests verify the SQL structure generated for each query pattern.
// They use the SQLite dialect and check for key SQL fragments rather than
// exact string matches (which would be brittle).

func gen(t *testing.T, spec *v1alpha1.QuerySpec) *GeneratedQuery {
	t.Helper()
	Validate(spec)
	g := NewGenerator("sqlite")
	q, err := g.Generate(spec)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func assertContains(t *testing.T, sql, fragment string) {
	t.Helper()
	if !strings.Contains(sql, fragment) {
		t.Errorf("SQL missing fragment %q\nSQL: %s", fragment, sql)
	}
}

func assertNotContains(t *testing.T, sql, fragment string) {
	t.Helper()
	if strings.Contains(sql, fragment) {
		t.Errorf("SQL should not contain %q\nSQL: %s", fragment, sql)
	}
}

// --- Query 1: No filter ---

func TestSQL_NoFilter(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{Limit: 3})

	assertContains(t, q.SQL, "FROM objects obj")
	assertContains(t, q.SQL, "ORDER BY obj.name ASC")
	assertContains(t, q.SQL, "LIMIT 3")
	assertNotContains(t, q.SQL, "WHERE")
	assertNotContains(t, q.SQL, "JOIN")

	if len(q.Args) != 0 {
		t.Errorf("expected 0 args, got %d: %v", len(q.Args), q.Args)
	}
	if q.HasRelations {
		t.Error("expected hasRelations=false")
	}

	// Count SQL.
	assertContains(t, q.CountSQL, "SELECT COUNT(*) FROM objects obj")
}

// --- Query 2: GroupKind filter ---

func TestSQL_GroupKindFilter(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{GroupKind: &v1alpha1.GroupKindFilter{APIGroup: "apps", Kind: "Deployment"}},
			},
		},
	})

	// Uses EXISTS subquery instead of JOIN to avoid row duplication.
	assertContains(t, q.SQL, "obj.api_group = ?")
	assertContains(t, q.SQL, "EXISTS (SELECT 1 FROM resource_types rt")
	assertContains(t, q.SQL, "lower(rt.kind) = lower(?)")
	assertContains(t, q.SQL, "lower(rt.resource) = lower(?)")
	assertContains(t, q.SQL, "lower(rt.singular) = lower(?)")
	assertContains(t, q.SQL, "json_each(rt.short_names)")
	assertNotContains(t, q.SQL, "JOIN resource_types") // No JOIN, uses EXISTS.

	if len(q.Args) != 5 {
		t.Errorf("expected 5 args, got %d: %v", len(q.Args), q.Args)
	}
	// Args: apiGroup, kind (x4 for kind/resource/singular/short_names).
	if q.Args[0] != "apps" {
		t.Errorf("expected arg[0]=apps, got %v", q.Args[0])
	}
}

// --- Query 3: Cluster + GroupKind ---

func TestSQL_ClusterFilter(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Cluster: &v1alpha1.ClusterFilter{Name: "my-cluster"},
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{GroupKind: &v1alpha1.GroupKindFilter{APIGroup: "apps", Kind: "Deployment"}},
			},
		},
	})

	assertContains(t, q.SQL, "obj.cluster = ?")
	assertContains(t, q.SQL, "obj.api_group = ?")
	if q.Args[0] != "my-cluster" {
		t.Errorf("expected arg[0]=my-cluster, got %v", q.Args[0])
	}
}

// --- Query 4: Non-transitive descendants ---

func TestSQL_Descendants_UNION_ALL(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{GroupKind: &v1alpha1.GroupKindFilter{APIGroup: "apps", Kind: "Deployment"}, Namespace: "demo"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Relations: map[string]v1alpha1.RelationSpec{
				"descendants": {
					Objects: &v1alpha1.ObjectsSpec{
						Relations: map[string]v1alpha1.RelationSpec{
							"descendants": {},
						},
					},
				},
			},
		},
	})

	if !q.HasRelations {
		t.Error("expected hasRelations=true")
	}

	// Should have root_objects CTE.
	assertContains(t, q.SQL, "WITH root_objects AS")
	// Should have UNION ALL for 3 levels: root + 2 descendants.
	if strings.Count(q.SQL, "UNION ALL") != 2 {
		t.Errorf("expected 2 UNION ALL, got %d", strings.Count(q.SQL, "UNION ALL"))
	}
	// Level 0: root.
	assertContains(t, q.SQL, "0 AS level, '' AS relation_name")
	// Level 1: descendants.
	assertContains(t, q.SQL, "1 AS level, 'descendants' AS relation_name")
	// Level 2: descendants of descendants.
	assertContains(t, q.SQL, "2 AS level, 'descendants' AS relation_name")
	// ownerRef JOIN.
	assertContains(t, q.SQL, "json_each(l1.owner_refs)")
	assertContains(t, q.SQL, "json_each(l2.owner_refs)")
	// Path column grows with each level.
	assertContains(t, q.SQL, "lower(l0.kind)")
	assertContains(t, q.SQL, "lower(l1.kind)")
	assertContains(t, q.SQL, "lower(l2.kind)")
	// ORDER BY path.
	assertContains(t, q.SQL, "ORDER BY path")
}

// --- Query 5: Recursive CTE (descendants+) ---

func TestSQL_TransitiveDescendants_RecursiveCTE(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{GroupKind: &v1alpha1.GroupKindFilter{APIGroup: "apps", Kind: "Deployment"},
					Name: "nginx", Namespace: "demo"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Relations: map[string]v1alpha1.RelationSpec{
				"descendants+": {},
			},
		},
	})

	if !q.HasRelations {
		t.Error("expected hasRelations=true")
	}

	// Should be a recursive CTE.
	assertContains(t, q.SQL, "WITH RECURSIVE")
	assertContains(t, q.SQL, "trans_descendants_1 AS")
	// Base case.
	assertContains(t, q.SQL, "1 AS depth")
	assertContains(t, q.SQL, "',' || curr.uid || ','")
	// Recursive step.
	assertContains(t, q.SQL, "trans_descendants_1.depth + 1")
	assertContains(t, q.SQL, "trans_descendants_1.visited || next.uid")
	// Cycle detection.
	assertContains(t, q.SQL, "NOT LIKE '%,' || next.uid || ',%'")
	// Depth limit.
	assertContains(t, q.SQL, "trans_descendants_1.depth < 10")
	// Final select from CTE.
	assertContains(t, q.SQL, "FROM trans_descendants_1")
	// Relation name.
	assertContains(t, q.SQL, "'descendants+' AS relation_name")
}

// --- Query 5b: descendants+ with references sub-relation ---

func TestSQL_TransitiveDescendants_WithReferences(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{GroupKind: &v1alpha1.GroupKindFilter{APIGroup: "apps", Kind: "Deployment"},
					Name: "nginx", Namespace: "demo"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Relations: map[string]v1alpha1.RelationSpec{
				"descendants+": {
					Objects: &v1alpha1.ObjectsSpec{
						Relations: map[string]v1alpha1.RelationSpec{
							"references": {},
						},
					},
				},
			},
		},
	})

	// Should have at least 2 top-level UNION ALL parts: root + transitive descendants + references.
	// (The references IN (...) subquery also contains UNION ALL for ref-path extraction,
	// so the total count is higher.)
	if strings.Count(q.SQL, "UNION ALL") < 2 {
		t.Errorf("expected at least 2 UNION ALL, got %d", strings.Count(q.SQL, "UNION ALL"))
	}

	// CTE carries raw object column for ref extraction.
	assertContains(t, q.SQL, "curr.object,")
	assertContains(t, q.SQL, "next.object,")

	// References sub-relation joins from CTE.
	assertContains(t, q.SQL, "'references' AS relation_name")
	// Ref-path extraction: volumes -> secret.secretName.
	assertContains(t, q.SQL, "json_extract(trans_descendants_1.object, '$.spec.volumes')")
	assertContains(t, q.SQL, "$.secret.secretName")
	// Ref-path extraction: volumes -> configMap.name.
	assertContains(t, q.SQL, "$.configMap.name")
	// Ref-path extraction: volumes -> persistentVolumeClaim.claimName.
	assertContains(t, q.SQL, "$.persistentVolumeClaim.claimName")
	// Ref-path extraction: serviceAccountName.
	assertContains(t, q.SQL, "$.spec.serviceAccountName")
}

// --- Query 6: Labels + ordering ---

func TestSQL_LabelsFilter(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Namespace: "demo", Labels: map[string]string{"app": "nginx"}},
			},
		},
		Order: []v1alpha1.OrderSpec{
			{Field: "kind", Direction: v1alpha1.SortAsc},
		},
	})

	// Label filter: json_extract for SQLite.
	assertContains(t, q.SQL, "json_extract(obj.labels, '$.app') = ?")
	assertContains(t, q.SQL, "obj.namespace = ?")
	// Custom ordering with tiebreaker.
	assertContains(t, q.SQL, "ORDER BY obj.kind ASC")
	assertContains(t, q.SQL, "obj.namespace ASC")
	assertContains(t, q.SQL, "obj.name ASC")
}

// --- Query 7: OR filter ---

func TestSQL_ORFilter(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{GroupKind: &v1alpha1.GroupKindFilter{Kind: "Pod"}, Namespace: "demo"},
				{GroupKind: &v1alpha1.GroupKindFilter{Kind: "Service"}, Namespace: "demo"},
			},
		},
	})

	// Two filter entries should be OR-ed.
	assertContains(t, q.SQL, " OR (")
	// Each entry's criteria are AND-ed.
	if strings.Count(q.SQL, "obj.namespace = ?") != 2 {
		t.Errorf("expected 2 namespace filters (one per OR branch), got %d",
			strings.Count(q.SQL, "obj.namespace = ?"))
	}
	// 10 args: 4 per kind resolution + 1 namespace, times 2.
	if len(q.Args) != 10 {
		t.Errorf("expected 10 args for OR filter, got %d: %v", len(q.Args), q.Args)
	}
}

// --- Projection ---

func TestSQL_SparseProjection(t *testing.T) {
	projJSON := []byte(`{"metadata":{"name":true},"spec":{"replicas":true}}`)
	q := gen(t, &v1alpha1.QuerySpec{
		Objects: &v1alpha1.ObjectsSpec{
			Object: &runtime.RawExtension{Raw: projJSON},
		},
	})

	// SQLite projection uses json_object + json_extract.
	assertContains(t, q.SQL, "json_object(")
	assertContains(t, q.SQL, "json_extract(obj.object, '$.metadata.name')")
	assertContains(t, q.SQL, "json_extract(obj.object, '$.spec.replicas')")
	assertNotContains(t, q.SQL, "obj.object AS projected_object") // Should use json_object, not raw.
}

// --- Cursor pagination ---

func TestSQL_CursorPagination(t *testing.T) {
	cursor := BuildCursorToken(map[string]string{
		"name":      "nginx",
		"namespace": "default",
	})
	q := gen(t, &v1alpha1.QuerySpec{
		Page: &v1alpha1.PageSpec{Cursor: cursor},
	})

	// Keyset pagination: tuple comparison.
	assertContains(t, q.SQL, ">")
	assertContains(t, q.SQL, "obj.name")
}

// --- Offset pagination ---

func TestSQL_OffsetPagination(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Limit: 10,
		Page:  &v1alpha1.PageSpec{First: 20},
	})

	assertContains(t, q.SQL, "LIMIT 10")
	assertContains(t, q.SQL, "OFFSET 20")
}

// --- Owners relation ---

func TestSQL_OwnersRelation(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{{Name: "my-pod"}},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Relations: map[string]v1alpha1.RelationSpec{
				"owners": {},
			},
		},
	})

	assertContains(t, q.SQL, "'owners' AS relation_name")
	// Owner JOIN: parent.uid IN (SELECT uid FROM child.owner_refs).
	assertContains(t, q.SQL, "l1.uid IN (SELECT json_extract(oref.value, '$.uid') FROM json_each(l0.owner_refs)")
}

// --- Events relation ---

func TestSQL_EventsRelation(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{{Name: "my-pod"}},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Relations: map[string]v1alpha1.RelationSpec{
				"events": {},
			},
		},
	})

	assertContains(t, q.SQL, "'events' AS relation_name")
	assertContains(t, q.SQL, "l1.kind = 'Event'")
	assertContains(t, q.SQL, "json_extract(l1.object, '$.involvedObject.uid') = l0.uid")
}

// --- Selects relation ---

func TestSQL_SelectsRelation(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{{Name: "my-svc"}},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Relations: map[string]v1alpha1.RelationSpec{
				"selects": {},
			},
		},
	})

	assertContains(t, q.SQL, "'selects' AS relation_name")
	assertContains(t, q.SQL, "json_extract(l0.object, '$.spec.selector.matchLabels')")
	assertContains(t, q.SQL, "NOT EXISTS")
}

// --- Linked relation ---

func TestSQL_LinkedRelation(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{{Name: "my-obj"}},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Relations: map[string]v1alpha1.RelationSpec{
				"linked": {},
			},
		},
	})

	assertContains(t, q.SQL, "'linked' AS relation_name")
	assertContains(t, q.SQL, `json_extract(l0.annotations, '$."kuery.io/relates-to"')`)
	assertContains(t, q.SQL, "json_extract(ref.value, '$.kind')")
}

// --- Grouped relation ---

func TestSQL_GroupedRelation(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{{Name: "my-obj"}},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Relations: map[string]v1alpha1.RelationSpec{
				"grouped": {},
			},
		},
	})

	assertContains(t, q.SQL, "'grouped' AS relation_name")
	assertContains(t, q.SQL, `json_extract(l1.labels, '$."kuery.io/group"')`)
	assertContains(t, q.SQL, "l1.id != l0.id")
}

// --- Label expressions ---

func TestSQL_LabelExpression_In(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{LabelExpressions: []v1alpha1.LabelExpression{
					{Key: "env", Operator: v1alpha1.LabelOpIn, Values: []string{"prod", "staging"}},
				}},
			},
		},
	})
	assertContains(t, q.SQL, "json_extract(obj.labels, '$.env') IN (?, ?)")
}

func TestSQL_LabelExpression_Exists(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{LabelExpressions: []v1alpha1.LabelExpression{
					{Key: "env", Operator: v1alpha1.LabelOpExists},
				}},
			},
		},
	})
	assertContains(t, q.SQL, "json_extract(obj.labels, '$.env') IS NOT NULL")
}

func TestSQL_LabelExpression_DoesNotExist(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{LabelExpressions: []v1alpha1.LabelExpression{
					{Key: "deprecated", Operator: v1alpha1.LabelOpDoesNotExist},
				}},
			},
		},
	})
	assertContains(t, q.SQL, "json_extract(obj.labels, '$.deprecated') IS NULL")
}

// --- JSONPath filter ---

func TestSQL_JSONPathFilter(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{JSONPath: "$.status.phase"},
			},
		},
	})
	assertContains(t, q.SQL, "json_extract(obj.object, ?) IS NOT NULL")
	if q.Args[0] != "$.status.phase" {
		t.Errorf("expected arg=$.status.phase, got %v", q.Args[0])
	}
}

// --- Timestamp filter ---

func TestSQL_TimestampFilter(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{CreationTimestamp: &v1alpha1.TimestampFilter{
					After:  timePtr("2025-01-01T00:00:00Z"),
					Before: timePtr("2025-12-31T00:00:00Z"),
				}},
			},
		},
	})
	assertContains(t, q.SQL, "obj.creation_ts > ?")
	assertContains(t, q.SQL, "obj.creation_ts < ?")
}

func timePtr(s string) *metav1.Time {
	t, _ := time.Parse(time.RFC3339, s)
	mt := metav1.NewTime(t)
	return &mt
}

// --- Condition filter ---

func TestSQL_ConditionFilter(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Conditions: []v1alpha1.ConditionFilter{
					{Type: "Ready", Status: "True"},
				}},
			},
		},
	})
	assertContains(t, q.SQL, "json_each(obj.conditions)")
	assertContains(t, q.SQL, "json_extract(je.value, '$.type') = ?")
	assertContains(t, q.SQL, "json_extract(je.value, '$.status') = ?")
}

// --- ID filter ---

func TestSQL_IDFilter(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{ID: "abc-123"},
			},
		},
	})
	assertContains(t, q.SQL, "obj.id = ?")
	if q.Args[0] != "abc-123" {
		t.Errorf("expected arg=abc-123, got %v", q.Args[0])
	}
}

// --- Categories filter ---

func TestSQL_CategoriesFilter(t *testing.T) {
	q := gen(t, &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Categories: []string{"all"}},
			},
		},
	})
	assertContains(t, q.SQL, "EXISTS (SELECT 1 FROM resource_types")
	assertContains(t, q.SQL, "json_each(rt.categories)")
}
