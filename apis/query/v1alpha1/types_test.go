package v1alpha1

import (
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestQueryJSONRoundTrip(t *testing.T) {
	q := &Query{
		Spec: QuerySpec{
			Cluster: &ClusterFilter{
				Name: "cluster-a",
				Labels: map[string]string{
					"env": "production",
				},
			},
			Filter: &QueryFilter{
				Objects: []ObjectFilter{
					{
						GroupKind: &GroupKindFilter{
							APIGroup: "apps",
							Kind:     "Deployment",
						},
						Name:      "nginx",
						Namespace: "default",
						Labels: map[string]string{
							"app": "nginx",
						},
					},
				},
			},
			Limit:    50,
			MaxDepth: 10,
			Count:    true,
			Cursor:   true,
			Order: []OrderSpec{
				{Field: "name", Direction: SortAsc},
			},
			Objects: &ObjectsSpec{
				ID:          true,
				Cluster:     true,
				MutablePath: true,
				Object: &runtime.RawExtension{
					Raw: json.RawMessage(`{"metadata":{"name":true}}`),
				},
			},
		},
	}

	data, err := json.Marshal(q)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var got Query
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if got.Spec.Cluster.Name != "cluster-a" {
		t.Errorf("Cluster.Name = %q, want %q", got.Spec.Cluster.Name, "cluster-a")
	}
	if got.Spec.Limit != 50 {
		t.Errorf("Limit = %d, want %d", got.Spec.Limit, 50)
	}
	if len(got.Spec.Filter.Objects) != 1 {
		t.Fatalf("expected 1 filter object, got %d", len(got.Spec.Filter.Objects))
	}
	if got.Spec.Filter.Objects[0].Name != "nginx" {
		t.Errorf("Filter.Objects[0].Name = %q, want %q", got.Spec.Filter.Objects[0].Name, "nginx")
	}
	if got.Spec.Objects.ID != true {
		t.Error("Objects.ID should be true")
	}
}

func TestQuerySchemeRegistration(t *testing.T) {
	s := runtime.NewScheme()
	if err := AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme failed: %v", err)
	}

	gvk := schema.GroupVersionKind{Group: "kuery.io", Version: "v1alpha1", Kind: "Query"}
	obj, err := s.New(gvk)
	if err != nil {
		t.Fatalf("scheme.New(%v) failed: %v", gvk, err)
	}
	if _, ok := obj.(*Query); !ok {
		t.Errorf("expected *Query, got %T", obj)
	}
}

func TestQueryDeepCopy(t *testing.T) {
	q := &Query{
		Spec: QuerySpec{
			Cluster: &ClusterFilter{
				Name:   "cluster-a",
				Labels: map[string]string{"env": "prod"},
			},
			Filter: &QueryFilter{
				Objects: []ObjectFilter{
					{
						GroupKind: &GroupKindFilter{Kind: "Deployment"},
						Labels:    map[string]string{"app": "nginx"},
					},
				},
			},
			Limit: 100,
		},
	}

	copied := q.DeepCopy()

	// Mutate original.
	q.Spec.Cluster.Name = "cluster-b"
	q.Spec.Filter.Objects[0].Labels["app"] = "changed"
	q.Spec.Limit = 200

	// Verify copy is unaffected.
	if copied.Spec.Cluster.Name != "cluster-a" {
		t.Errorf("DeepCopy: Cluster.Name = %q, want %q", copied.Spec.Cluster.Name, "cluster-a")
	}
	if copied.Spec.Filter.Objects[0].Labels["app"] != "nginx" {
		t.Errorf("DeepCopy: Labels[app] = %q, want %q", copied.Spec.Filter.Objects[0].Labels["app"], "nginx")
	}
	if copied.Spec.Limit != 100 {
		t.Errorf("DeepCopy: Limit = %d, want %d", copied.Spec.Limit, 100)
	}
}

func TestQueryStatusRoundTrip(t *testing.T) {
	status := QueryStatus{
		Objects: []ObjectResult{
			{
				ID:          "abc-123",
				Cluster:     "cluster-a",
				MutablePath: "/apis/apps/v1/namespaces/default/deployments/nginx",
				Object: &runtime.RawExtension{
					Raw: json.RawMessage(`{"metadata":{"name":"nginx"}}`),
				},
				Relations: map[string][]ObjectResult{
					"descendants": {
						{
							Object: &runtime.RawExtension{
								Raw: json.RawMessage(`{"metadata":{"name":"nginx-rs"}}`),
							},
						},
					},
				},
			},
		},
		Count:      ptrInt64(47),
		Incomplete: false,
		Warnings:   []string{},
	}

	data, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var got QueryStatus
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(got.Objects) != 1 {
		t.Fatalf("expected 1 object, got %d", len(got.Objects))
	}
	if got.Objects[0].ID != "abc-123" {
		t.Errorf("Objects[0].ID = %q, want %q", got.Objects[0].ID, "abc-123")
	}
	if got.Objects[0].Relations == nil {
		t.Fatal("expected Relations to be non-nil")
	}
	if len(got.Objects[0].Relations["descendants"]) != 1 {
		t.Errorf("expected 1 descendant, got %d", len(got.Objects[0].Relations["descendants"]))
	}
	if *got.Count != 47 {
		t.Errorf("Count = %d, want %d", *got.Count, 47)
	}
}

func ptrInt64(v int64) *int64 { return &v }
