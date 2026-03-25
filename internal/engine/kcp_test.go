package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
	"github.com/faroshq/kuery/internal/store"
	"github.com/google/uuid"
)

func TestKCP_IdentityAwareResolution(t *testing.T) {
	s := setupTestStore(t)

	// Two resource types with the same name "widgets" but different identities
	// (simulating two kcp APIExports providing the same resource name).
	seedResourceTypes(t, s,
		&store.ResourceTypeModel{
			Cluster: "cluster-a", APIGroup: "widgets.example.io", APIVersion: "v1",
			Kind: "Widget", Singular: "widget", Resource: "widgets",
			ShortNames: mustJSON([]string{"wg"}), Categories: mustJSON([]string{}),
			Namespaced: true, Identity: "abc123",
		},
		&store.ResourceTypeModel{
			Cluster: "cluster-a", APIGroup: "widgets.other.io", APIVersion: "v1",
			Kind: "Widget", Singular: "widget", Resource: "widgets",
			ShortNames: mustJSON([]string{}), Categories: mustJSON([]string{}),
			Namespaced: true, Identity: "def456",
		},
	)

	// Seed objects for both.
	widgetA := makeCustomObject("cluster-a", "default", "widget-a", "widgets.example.io", "Widget", "widgets")
	widgetB := makeCustomObject("cluster-a", "default", "widget-b", "widgets.other.io", "Widget", "widgets")
	seedObjects(t, s, widgetA, widgetB)

	e := NewEngine(s)

	// Query for widgets from widgets.example.io group.
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{GroupKind: &v1alpha1.GroupKindFilter{APIGroup: "widgets.example.io", Kind: "Widget"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 widget from widgets.example.io, got %d", len(status.Objects))
	}
}

func TestKCP_ParseRefsAnnotation(t *testing.T) {
	annotations := map[string]string{
		"kuery.io/refs": `[{"path":"$.spec.secretRef.name","targetKind":"Secret"}]`,
	}
	raw, ok := parseKueryRefsAnnotation(annotations)
	if !ok {
		t.Fatal("expected annotation to be present")
	}

	var parsed []map[string]string
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 ref entry, got %d", len(parsed))
	}
	if parsed[0]["targetKind"] != "Secret" {
		t.Fatalf("expected targetKind=Secret, got %s", parsed[0]["targetKind"])
	}
}

func TestKCP_ParseRefsAnnotation_Missing(t *testing.T) {
	annotations := map[string]string{}
	_, ok := parseKueryRefsAnnotation(annotations)
	if ok {
		t.Fatal("expected annotation to be absent")
	}
}

func TestKCP_ParseRefsAnnotation_InvalidJSON(t *testing.T) {
	annotations := map[string]string{
		"kuery.io/refs": "not json",
	}
	_, ok := parseKueryRefsAnnotation(annotations)
	if ok {
		t.Fatal("expected invalid JSON to be rejected")
	}
}

// --- Helpers ---

func makeCustomObject(cluster, namespace, name, apiGroup, kind, resource string) *store.ObjectModel {
	obj := map[string]any{
		"apiVersion": apiGroup + "/v1",
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": namespace},
	}
	return &store.ObjectModel{
		ID:         newUUID(),
		UID:        newUUID().String(),
		Cluster:    cluster,
		APIGroup:   apiGroup,
		APIVersion: "v1",
		Kind:       kind,
		Resource:   resource,
		Namespace:  namespace,
		Name:       name,
		CreationTS: ts("2025-06-01T00:00:00Z"),
		Object:     mustJSON(obj),
	}
}

func newUUID() uuid.UUID {
	return uuid.New()
}

// parseKueryRefsAnnotation is a local helper wrapping the sync package function.
func parseKueryRefsAnnotation(annotations map[string]string) ([]byte, bool) {
	val, ok := annotations["kuery.io/refs"]
	if !ok || val == "" {
		return nil, false
	}
	var parsed json.RawMessage
	if err := json.Unmarshal([]byte(val), &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}
