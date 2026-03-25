package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
	"github.com/faroshq/kuery/internal/store"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"gorm.io/datatypes"
)

func setupTestStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewStore(store.Config{
		Driver: "sqlite",
		DSN:    ":memory:",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AutoMigrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedObjects(t *testing.T, s store.Store, objs ...*store.ObjectModel) {
	t.Helper()
	for _, obj := range objs {
		if err := s.UpsertObject(context.Background(), obj); err != nil {
			t.Fatal(err)
		}
	}
}

func seedResourceTypes(t *testing.T, s store.Store, rts ...*store.ResourceTypeModel) {
	t.Helper()
	for _, rt := range rts {
		if err := s.UpsertResourceType(context.Background(), rt); err != nil {
			t.Fatal(err)
		}
	}
}

func mustJSON(v any) datatypes.JSON {
	b, _ := json.Marshal(v)
	return datatypes.JSON(b)
}

func ts(s string) *time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return &t
}

func makeDeployment(cluster, namespace, name string, labels map[string]string, createdAt *time.Time) *store.ObjectModel {
	obj := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
			"labels":    labels,
		},
		"spec": map[string]any{
			"replicas": 3,
		},
	}
	return &store.ObjectModel{
		ID:         uuid.New(),
		UID:        string(uuid.New().String()),
		Cluster:    cluster,
		APIGroup:   "apps",
		APIVersion: "v1",
		Kind:       "Deployment",
		Resource:   "deployments",
		Namespace:  namespace,
		Name:       name,
		Labels:     mustJSON(labels),
		CreationTS: createdAt,
		Object:     mustJSON(obj),
	}
}

func makePod(cluster, namespace, name string, labels map[string]string) *store.ObjectModel {
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
	}
	return &store.ObjectModel{
		ID:         uuid.New(),
		UID:        string(uuid.New().String()),
		Cluster:    cluster,
		APIGroup:   "",
		APIVersion: "v1",
		Kind:       "Pod",
		Resource:   "pods",
		Namespace:  namespace,
		Name:       name,
		Labels:     mustJSON(labels),
		CreationTS: ts("2025-06-01T00:00:00Z"),
		Object:     mustJSON(obj),
	}
}

func makeConditionPod(cluster, namespace, name string, conditions []map[string]string) *store.ObjectModel {
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
	}
	return &store.ObjectModel{
		ID:         uuid.New(),
		UID:        string(uuid.New().String()),
		Cluster:    cluster,
		APIGroup:   "",
		APIVersion: "v1",
		Kind:       "Pod",
		Resource:   "pods",
		Namespace:  namespace,
		Name:       name,
		Conditions: mustJSON(conditions),
		CreationTS: ts("2025-06-01T00:00:00Z"),
		Object:     mustJSON(obj),
	}
}

func deploymentRT(cluster string) *store.ResourceTypeModel {
	return &store.ResourceTypeModel{
		Cluster:    cluster,
		APIGroup:   "apps",
		APIVersion: "v1",
		Kind:       "Deployment",
		Singular:   "deployment",
		Resource:   "deployments",
		ShortNames: mustJSON([]string{"deploy"}),
		Categories: mustJSON([]string{"all"}),
		Namespaced: true,
	}
}

func podRT(cluster string) *store.ResourceTypeModel {
	return &store.ResourceTypeModel{
		Cluster:    cluster,
		APIGroup:   "",
		APIVersion: "v1",
		Kind:       "Pod",
		Singular:   "pod",
		Resource:   "pods",
		ShortNames: mustJSON([]string{"po"}),
		Categories: mustJSON([]string{"all"}),
		Namespaced: true,
	}
}

// --- Tests ---

func TestEngine_BasicQuery_NoFilters(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z")),
		makeDeployment("cluster-a", "default", "redis", nil, ts("2025-06-02T00:00:00Z")),
	)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(status.Objects))
	}
}

func TestEngine_FilterByName(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z")),
		makeDeployment("cluster-a", "default", "redis", nil, ts("2025-06-02T00:00:00Z")),
	)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "nginx"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(status.Objects))
	}
}

func TestEngine_FilterByNamespace(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z")),
		makeDeployment("cluster-a", "kube-system", "coredns", nil, ts("2025-06-01T00:00:00Z")),
	)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Namespace: "kube-system"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(status.Objects))
	}
}

func TestEngine_FilterByGroupKind(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z")),
		makePod("cluster-a", "default", "nginx-pod", nil),
	)
	seedResourceTypes(t, s,
		deploymentRT("cluster-a"),
		podRT("cluster-a"),
	)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{GroupKind: &v1alpha1.GroupKindFilter{APIGroup: "apps", Kind: "Deployment"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(status.Objects))
	}
}

func TestEngine_FilterByGroupKind_ShortName(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z")),
		makePod("cluster-a", "default", "nginx-pod", nil),
	)
	seedResourceTypes(t, s,
		deploymentRT("cluster-a"),
		podRT("cluster-a"),
	)

	e := NewEngine(s)
	// Use short name "deploy" to find Deployments.
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{GroupKind: &v1alpha1.GroupKindFilter{Kind: "deploy"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 object via short name, got %d", len(status.Objects))
	}
}

func TestEngine_FilterByLabels(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "nginx", map[string]string{"app": "nginx", "env": "prod"}, ts("2025-06-01T00:00:00Z")),
		makeDeployment("cluster-a", "default", "redis", map[string]string{"app": "redis"}, ts("2025-06-01T00:00:00Z")),
	)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Labels: map[string]string{"app": "nginx"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(status.Objects))
	}
}

func TestEngine_FilterByConditions(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeConditionPod("cluster-a", "default", "ready-pod", []map[string]string{
			{"type": "Ready", "status": "True"},
		}),
		makeConditionPod("cluster-a", "default", "not-ready-pod", []map[string]string{
			{"type": "Ready", "status": "False"},
		}),
	)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Conditions: []v1alpha1.ConditionFilter{
					{Type: "Ready", Status: "True"},
				}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(status.Objects))
	}
}

func TestEngine_FilterByCreationTimestamp(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "old", nil, ts("2024-01-01T00:00:00Z")),
		makeDeployment("cluster-a", "default", "new", nil, ts("2025-06-01T00:00:00Z")),
	)

	after := metav1.NewTime(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC))
	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{CreationTimestamp: &v1alpha1.TimestampFilter{After: &after}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(status.Objects))
	}
}

func TestEngine_FilterByCluster(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z")),
		makeDeployment("cluster-b", "default", "nginx", nil, ts("2025-06-01T00:00:00Z")),
	)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Cluster: &v1alpha1.ClusterFilter{Name: "cluster-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(status.Objects))
	}
}

func TestEngine_FilterOR(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z")),
		makeDeployment("cluster-a", "default", "redis", nil, ts("2025-06-01T00:00:00Z")),
		makeDeployment("cluster-a", "default", "postgres", nil, ts("2025-06-01T00:00:00Z")),
	)

	e := NewEngine(s)
	// OR: name=nginx OR name=redis
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "nginx"},
				{Name: "redis"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 2 {
		t.Fatalf("expected 2 objects from OR filter, got %d", len(status.Objects))
	}
}

func TestEngine_Ordering_Default(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "zulu", nil, ts("2025-06-01T00:00:00Z")),
		makeDeployment("cluster-a", "default", "alpha", nil, ts("2025-06-01T00:00:00Z")),
		makeDeployment("cluster-a", "default", "mike", nil, ts("2025-06-01T00:00:00Z")),
	)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Objects: &v1alpha1.ObjectsSpec{ID: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 3 {
		t.Fatalf("expected 3 objects, got %d", len(status.Objects))
	}
	// Default order is name ASC — verify we get alpha, mike, zulu by checking projected object.
}

func TestEngine_Ordering_Custom(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "a-deploy", nil, ts("2025-01-01T00:00:00Z")),
		makeDeployment("cluster-a", "default", "b-deploy", nil, ts("2025-06-01T00:00:00Z")),
		makeDeployment("cluster-a", "default", "c-deploy", nil, ts("2025-03-01T00:00:00Z")),
	)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Order: []v1alpha1.OrderSpec{
			{Field: "creationTimestamp", Direction: v1alpha1.SortDesc},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 3 {
		t.Fatalf("expected 3 objects, got %d", len(status.Objects))
	}
}

func TestEngine_Limit(t *testing.T) {
	s := setupTestStore(t)
	for i := 0; i < 10; i++ {
		seedObjects(t, s, makeDeployment("cluster-a", "default", "deploy-"+string(rune('a'+i)), nil, ts("2025-06-01T00:00:00Z")))
	}

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Limit: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 3 {
		t.Fatalf("expected 3 objects, got %d", len(status.Objects))
	}
	if !status.Incomplete {
		t.Fatal("expected incomplete=true when limit is reached")
	}
}

func TestEngine_Pagination_Offset(t *testing.T) {
	s := setupTestStore(t)
	for i := 0; i < 5; i++ {
		seedObjects(t, s, makeDeployment("cluster-a", "default", "deploy-"+string(rune('a'+i)), nil, ts("2025-06-01T00:00:00Z")))
	}

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Limit: 2,
		Page:  &v1alpha1.PageSpec{First: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(status.Objects))
	}
}

func TestEngine_Count(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z")),
		makeDeployment("cluster-a", "default", "redis", nil, ts("2025-06-01T00:00:00Z")),
		makeDeployment("cluster-a", "default", "postgres", nil, ts("2025-06-01T00:00:00Z")),
	)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Count: true,
		Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.Count == nil || *status.Count != 3 {
		t.Fatalf("expected count 3, got %v", status.Count)
	}
	if len(status.Objects) != 2 {
		t.Fatalf("expected 2 objects, got %d", len(status.Objects))
	}
}

func TestEngine_Projection(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "nginx", map[string]string{"app": "nginx"}, ts("2025-06-01T00:00:00Z")),
	)

	e := NewEngine(s)
	projSpec := map[string]any{
		"metadata": map[string]any{
			"name": true,
		},
		"spec": map[string]any{
			"replicas": true,
		},
	}
	projJSON, _ := json.Marshal(projSpec)

	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Objects: &v1alpha1.ObjectsSpec{
			Object: &runtime.RawExtension{Raw: projJSON},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(status.Objects))
	}
	if status.Objects[0].Object == nil {
		t.Fatal("expected projected object, got nil")
	}

	// Verify projected object has only requested fields.
	var projected map[string]any
	if err := json.Unmarshal(status.Objects[0].Object.Raw, &projected); err != nil {
		t.Fatal(err)
	}
	meta, ok := projected["metadata"].(map[string]any)
	if !ok {
		t.Fatal("expected metadata in projected object")
	}
	if meta["name"] != "nginx" {
		t.Fatalf("expected name=nginx, got %v", meta["name"])
	}
	// labels should NOT be present since not requested.
	if _, ok := meta["labels"]; ok {
		t.Fatal("expected labels to not be in projected object")
	}
	spec, ok := projected["spec"].(map[string]any)
	if !ok {
		t.Fatal("expected spec in projected object")
	}
	if spec["replicas"] != float64(3) {
		t.Fatalf("expected replicas=3, got %v", spec["replicas"])
	}
}

func TestEngine_ObjectMetadata(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z")),
	)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Objects: &v1alpha1.ObjectsSpec{
			ID:          true,
			Cluster:     true,
			MutablePath: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(status.Objects))
	}

	result := status.Objects[0]
	if result.ID == "" {
		t.Fatal("expected ID to be set")
	}
	if result.Cluster != "cluster-a" {
		t.Fatalf("expected cluster-a, got %s", result.Cluster)
	}
	if result.MutablePath != "/apis/apps/v1/namespaces/default/deployments/nginx" {
		t.Fatalf("unexpected mutablePath: %s", result.MutablePath)
	}
}

func TestEngine_CursorPagination(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "alpha", nil, ts("2025-06-01T00:00:00Z")),
		makeDeployment("cluster-a", "default", "bravo", nil, ts("2025-06-01T00:00:00Z")),
		makeDeployment("cluster-a", "default", "charlie", nil, ts("2025-06-01T00:00:00Z")),
	)

	e := NewEngine(s)
	// First page.
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Limit:  2,
		Cursor: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 2 {
		t.Fatalf("expected 2 objects on first page, got %d", len(status.Objects))
	}
	if status.Cursor == nil || status.Cursor.Next == "" {
		t.Fatal("expected cursor in response")
	}

	// Second page using cursor.
	status2, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Limit:  2,
		Cursor: true,
		Page:   &v1alpha1.PageSpec{Cursor: status.Cursor.Next},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status2.Objects) != 1 {
		t.Fatalf("expected 1 object on second page, got %d", len(status2.Objects))
	}
}

func TestEngine_FilterByCategories(t *testing.T) {
	s := setupTestStore(t)
	seedObjects(t, s,
		makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z")),
		makePod("cluster-a", "default", "nginx-pod", nil),
	)
	seedResourceTypes(t, s,
		deploymentRT("cluster-a"),
		podRT("cluster-a"),
	)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Categories: []string{"all"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Both Deployment and Pod are in "all" category.
	if len(status.Objects) != 2 {
		t.Fatalf("expected 2 objects with category 'all', got %d", len(status.Objects))
	}
}

func TestEngine_EmptyResult(t *testing.T) {
	s := setupTestStore(t)
	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "nonexistent"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Objects) != 0 {
		t.Fatalf("expected 0 objects, got %d", len(status.Objects))
	}
	if status.Incomplete {
		t.Fatal("expected incomplete=false for empty result")
	}
}
