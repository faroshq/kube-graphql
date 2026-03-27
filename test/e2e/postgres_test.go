//go:build e2e

package e2e_test

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

// queryPostgres is like queryKuery but hits the PostgreSQL-backed server.
func queryPostgres(t *testing.T, spec v1alpha1.QuerySpec) v1alpha1.QueryStatus {
	t.Helper()
	query := map[string]any{
		"apiVersion": "kuery.io/v1alpha1",
		"kind":       "Query",
		"spec":       spec,
	}
	body, err := json.Marshal(query)
	if err != nil {
		t.Fatalf("marshal query: %v", err)
	}

	resp, err := httpClient.Post(pgServerURL+"/apis/kuery.io/v1alpha1/queries", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST query to postgres server: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(respBody, &raw); err != nil {
		t.Fatalf("unmarshal response: %v\nbody: %s", err, string(respBody))
	}
	if kind, _ := raw["kind"].(string); kind == "Status" {
		msg, _ := raw["message"].(string)
		t.Fatalf("postgres query failed: %s", msg)
	}

	statusRaw, _ := json.Marshal(raw["status"])
	var status v1alpha1.QueryStatus
	if err := json.Unmarshal(statusRaw, &status); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	return status
}

// pgProj is a shorthand for projection specs in Postgres tests.
func pgProj(spec map[string]any) *runtime.RawExtension {
	raw, _ := json.Marshal(spec)
	return &runtime.RawExtension{Raw: raw}
}

// --- PostgreSQL E2E Tests (full pipeline: K3s -> kuery sync -> PostgreSQL -> HTTP query) ---

func TestPostgres_BasicQuery(t *testing.T) {
	t.Parallel()
	status := queryPostgres(t, v1alpha1.QuerySpec{
		Count: true,
		Limit: 5,
		Objects: &v1alpha1.ObjectsSpec{
			Cluster: true,
			Object:  pgProj(map[string]any{"kind": true, "metadata": map[string]any{"name": true}}),
		},
	})
	if status.Count == nil || *status.Count == 0 {
		t.Fatal("expected objects synced into PostgreSQL")
	}
	if len(status.Objects) == 0 {
		t.Fatal("expected results")
	}
	// Verify objects have cluster and projected fields.
	for _, obj := range status.Objects {
		if obj.Cluster == "" {
			t.Fatal("expected cluster to be populated")
		}
	}
}

func TestPostgres_GroupKindFilter(t *testing.T) {
	t.Parallel()
	status := queryPostgres(t, v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{GroupKind: &v1alpha1.GroupKindFilter{APIGroup: "apps", Kind: "Deployment"}, Namespace: "demo"},
			},
		},
		Count:   true,
		Objects: &v1alpha1.ObjectsSpec{Object: pgProj(map[string]any{"kind": true, "metadata": map[string]any{"name": true}})},
	})
	if status.Count == nil || *status.Count != 2 {
		t.Fatalf("expected 2 Deployments in demo (nginx+redis), got count=%v", status.Count)
	}
	names := objectNames(t, status.Objects)
	if !containsString(names, "nginx") || !containsString(names, "redis") {
		t.Fatalf("expected nginx and redis, got %v", names)
	}
}

func TestPostgres_LabelsContainment(t *testing.T) {
	t.Parallel()
	// Tests PostgreSQL @> JSONB containment operator.
	status := queryPostgres(t, v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Labels: map[string]string{"app": "nginx"}, Namespace: "demo"},
			},
		},
		Count: true,
	})
	if status.Count == nil || *status.Count == 0 {
		t.Fatal("expected objects with app=nginx label via PostgreSQL @> operator")
	}
}

func TestPostgres_Descendants(t *testing.T) {
	t.Parallel()
	proj := pgProj(map[string]any{"kind": true, "metadata": map[string]any{"name": true}})
	status := queryPostgres(t, v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{GroupKind: &v1alpha1.GroupKindFilter{APIGroup: "apps", Kind: "Deployment"}, Name: "nginx", Namespace: "demo"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Object: proj,
			Relations: map[string]v1alpha1.RelationSpec{
				"descendants": {Objects: &v1alpha1.ObjectsSpec{Object: proj}},
			},
		},
	})
	if len(status.Objects) == 0 {
		t.Fatal("expected nginx deployment")
	}
	descendants := status.Objects[0].Relations["descendants"]
	if len(descendants) == 0 {
		t.Fatal("expected ReplicaSet descendants via PostgreSQL jsonb_build_array ownerRef JOIN")
	}
	kind := getProjectedString(t, descendants[0].Object, "kind")
	if kind != "ReplicaSet" {
		t.Fatalf("expected ReplicaSet, got %q", kind)
	}
}

func TestPostgres_TransitiveCTE(t *testing.T) {
	t.Parallel()
	proj := pgProj(map[string]any{"kind": true, "metadata": map[string]any{"name": true}})
	status := queryPostgres(t, v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{GroupKind: &v1alpha1.GroupKindFilter{APIGroup: "apps", Kind: "Deployment"}, Name: "nginx", Namespace: "demo"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Object: proj,
			Relations: map[string]v1alpha1.RelationSpec{
				"descendants+": {Objects: &v1alpha1.ObjectsSpec{Object: proj}},
			},
		},
	})
	if len(status.Objects) == 0 {
		t.Fatal("expected nginx deployment")
	}
	// Recursive CTE should find RS -> Pods.
	d1 := status.Objects[0].Relations["descendants+"]
	if len(d1) == 0 {
		t.Fatal("expected transitive descendants via PostgreSQL recursive CTE with ARRAY cycle detection")
	}
	// RS should have nested Pods.
	d2 := d1[0].Relations["descendants+"]
	if len(d2) == 0 {
		t.Fatal("expected Pods nested under RS")
	}
}

func TestPostgres_CrossClusterLinked(t *testing.T) {
	t.Parallel()
	proj := pgProj(map[string]any{"kind": true, "metadata": map[string]any{"name": true}})
	status := queryPostgres(t, v1alpha1.QuerySpec{
		Cluster: &v1alpha1.ClusterFilter{Name: "cluster-a"},
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{{Name: "app-config", Namespace: "demo"}},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Cluster: true,
			Object:  proj,
			Relations: map[string]v1alpha1.RelationSpec{
				"linked": {Objects: &v1alpha1.ObjectsSpec{Cluster: true, Object: proj}},
			},
		},
	})
	if len(status.Objects) == 0 {
		t.Fatal("expected app-config")
	}
	linked := status.Objects[0].Relations["linked"]
	if len(linked) == 0 {
		t.Fatal("expected linked Secret from cluster-b via PostgreSQL jsonb_array_elements")
	}
	if linked[0].Cluster != "cluster-b" {
		t.Fatalf("expected linked from cluster-b, got %q", linked[0].Cluster)
	}
	name := getProjectedString(t, linked[0].Object, "metadata", "name")
	if name != "shared-cert" {
		t.Fatalf("expected shared-cert, got %q", name)
	}
}

func TestPostgres_Projection(t *testing.T) {
	t.Parallel()
	status := queryPostgres(t, v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{GroupKind: &v1alpha1.GroupKindFilter{APIGroup: "apps", Kind: "Deployment"}, Name: "nginx", Namespace: "demo"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Object: pgProj(map[string]any{
				"metadata": map[string]any{"name": true, "namespace": true},
				"spec":     map[string]any{"replicas": true},
			}),
		},
	})
	if len(status.Objects) == 0 {
		t.Fatal("expected result")
	}
	name := getProjectedString(t, status.Objects[0].Object, "metadata", "name")
	if name != "nginx" {
		t.Fatalf("expected name=nginx via PostgreSQL jsonb_build_object projection, got %q", name)
	}
	replicas := getProjectedValue(t, status.Objects[0].Object, "spec", "replicas")
	if replicas == nil {
		t.Fatal("expected spec.replicas in projection")
	}
	// labels should NOT be present.
	labels := getProjectedValue(t, status.Objects[0].Object, "metadata", "labels")
	if labels != nil {
		t.Fatal("labels should not be in projection")
	}
}
