package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
	"github.com/faroshq/kuery/internal/store"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"k8s.io/apimachinery/pkg/runtime"
)

// makeOwnedPod creates a Pod owned by the given parent UID.
func makeOwnedPod(cluster, namespace, name, parentUID string) *store.ObjectModel {
	ownerRefs := []map[string]string{{"uid": parentUID, "kind": "ReplicaSet", "name": "parent"}}
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
	}
	return &store.ObjectModel{
		ID:         uuid.New(),
		UID:        uuid.New().String(),
		Cluster:    cluster,
		APIGroup:   "",
		APIVersion: "v1",
		Kind:       "Pod",
		Resource:   "pods",
		Namespace:  namespace,
		Name:       name,
		OwnerRefs:  mustJSON(ownerRefs),
		CreationTS: ts("2025-06-01T00:00:00Z"),
		Object:     mustJSON(obj),
	}
}

func makeReplicaSet(cluster, namespace, name, parentUID string) *store.ObjectModel {
	uid := uuid.New().String()
	ownerRefs := []map[string]string{{"uid": parentUID, "kind": "Deployment", "name": "parent"}}
	obj := map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "ReplicaSet",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
	}
	return &store.ObjectModel{
		ID:         uuid.New(),
		UID:        uid,
		Cluster:    cluster,
		APIGroup:   "apps",
		APIVersion: "v1",
		Kind:       "ReplicaSet",
		Resource:   "replicasets",
		Namespace:  namespace,
		Name:       name,
		OwnerRefs:  mustJSON(ownerRefs),
		CreationTS: ts("2025-06-01T00:00:00Z"),
		Object:     mustJSON(obj),
	}
}

func makeEvent(cluster, namespace, name, involvedUID string) *store.ObjectModel {
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "Event",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"involvedObject": map[string]any{
			"uid": involvedUID,
		},
	}
	return &store.ObjectModel{
		ID:         uuid.New(),
		UID:        uuid.New().String(),
		Cluster:    cluster,
		APIGroup:   "",
		APIVersion: "v1",
		Kind:       "Event",
		Resource:   "events",
		Namespace:  namespace,
		Name:       name,
		CreationTS: ts("2025-06-01T00:00:00Z"),
		Object:     mustJSON(obj),
	}
}

func makeService(cluster, namespace, name string, selector map[string]string) *store.ObjectModel {
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"selector": map[string]any{
				"matchLabels": selector,
			},
		},
	}
	return &store.ObjectModel{
		ID:         uuid.New(),
		UID:        uuid.New().String(),
		Cluster:    cluster,
		APIGroup:   "",
		APIVersion: "v1",
		Kind:       "Service",
		Resource:   "services",
		Namespace:  namespace,
		Name:       name,
		CreationTS: ts("2025-06-01T00:00:00Z"),
		Object:     mustJSON(obj),
	}
}

func makeSecret(cluster, namespace, name string) *store.ObjectModel {
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"data":       map[string]any{"key": "value"},
	}
	return &store.ObjectModel{
		ID:         uuid.New(),
		UID:        uuid.New().String(),
		Cluster:    cluster,
		APIGroup:   "",
		APIVersion: "v1",
		Kind:       "Secret",
		Resource:   "secrets",
		Namespace:  namespace,
		Name:       name,
		CreationTS: ts("2025-06-01T00:00:00Z"),
		Object:     mustJSON(obj),
	}
}

func makePodWithVolumes(cluster, namespace, name string, secretNames []string, parentUID string) *store.ObjectModel {
	volumes := make([]map[string]any, 0, len(secretNames))
	for _, sn := range secretNames {
		volumes = append(volumes, map[string]any{
			"name":   sn + "-vol",
			"secret": map[string]any{"secretName": sn},
		})
	}
	obj := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec":       map[string]any{"volumes": volumes},
	}
	var ownerRefs datatypes.JSON
	if parentUID != "" {
		ownerRefs = mustJSON([]map[string]string{{"uid": parentUID}})
	}
	return &store.ObjectModel{
		ID:         uuid.New(),
		UID:        uuid.New().String(),
		Cluster:    cluster,
		APIGroup:   "",
		APIVersion: "v1",
		Kind:       "Pod",
		Resource:   "pods",
		Namespace:  namespace,
		Name:       name,
		OwnerRefs:  ownerRefs,
		CreationTS: ts("2025-06-01T00:00:00Z"),
		Object:     mustJSON(obj),
	}
}

// --- Relation Tests ---

func TestRelation_Descendants(t *testing.T) {
	s := setupTestStore(t)
	deploy := makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z"))
	rs := makeReplicaSet("cluster-a", "default", "nginx-rs", deploy.UID)
	pod1 := makeOwnedPod("cluster-a", "default", "nginx-pod-1", rs.UID)
	pod2 := makeOwnedPod("cluster-a", "default", "nginx-pod-2", rs.UID)
	// Unrelated pod.
	unrelated := makePod("cluster-a", "default", "redis-pod", nil)

	seedObjects(t, s, deploy, rs, pod1, pod2, unrelated)

	e := NewEngine(s)
	projJSON, _ := json.Marshal(map[string]any{"metadata": map[string]any{"name": true}})

	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "nginx", Namespace: "default"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Object: &runtime.RawExtension{Raw: projJSON},
			Relations: map[string]v1alpha1.RelationSpec{
				"descendants": {
					Objects: &v1alpha1.ObjectsSpec{
						Object: &runtime.RawExtension{Raw: projJSON},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 root object, got %d", len(status.Objects))
	}

	root := status.Objects[0]
	if root.Relations == nil {
		t.Fatal("expected relations on root")
	}
	descendants := root.Relations["descendants"]
	if len(descendants) != 1 {
		t.Fatalf("expected 1 descendant (ReplicaSet), got %d", len(descendants))
	}
}

func TestRelation_Owners(t *testing.T) {
	s := setupTestStore(t)
	deploy := makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z"))
	rs := makeReplicaSet("cluster-a", "default", "nginx-rs", deploy.UID)

	seedObjects(t, s, deploy, rs)

	e := NewEngine(s)
	projJSON, _ := json.Marshal(map[string]any{"metadata": map[string]any{"name": true}})

	// Query the ReplicaSet and ask for its owners.
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "nginx-rs", Namespace: "default"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Object: &runtime.RawExtension{Raw: projJSON},
			Relations: map[string]v1alpha1.RelationSpec{
				"owners": {
					Objects: &v1alpha1.ObjectsSpec{
						Object: &runtime.RawExtension{Raw: projJSON},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 root object, got %d", len(status.Objects))
	}

	root := status.Objects[0]
	owners := root.Relations["owners"]
	if len(owners) != 1 {
		t.Fatalf("expected 1 owner (Deployment), got %d", len(owners))
	}
}

func TestRelation_Events(t *testing.T) {
	s := setupTestStore(t)
	deploy := makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z"))
	event1 := makeEvent("cluster-a", "default", "nginx-event-1", deploy.UID)
	event2 := makeEvent("cluster-a", "default", "nginx-event-2", deploy.UID)
	unrelatedEvent := makeEvent("cluster-a", "default", "redis-event", "other-uid")

	seedObjects(t, s, deploy, event1, event2, unrelatedEvent)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "nginx", Namespace: "default"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Relations: map[string]v1alpha1.RelationSpec{
				"events": {},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 root object, got %d", len(status.Objects))
	}

	events := status.Objects[0].Relations["events"]
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
}

func TestRelation_Selects(t *testing.T) {
	s := setupTestStore(t)
	svc := makeService("cluster-a", "default", "nginx-svc", map[string]string{"app": "nginx"})
	pod1 := makePod("cluster-a", "default", "nginx-pod-1", map[string]string{"app": "nginx"})
	pod2 := makePod("cluster-a", "default", "nginx-pod-2", map[string]string{"app": "nginx"})
	unmatched := makePod("cluster-a", "default", "redis-pod", map[string]string{"app": "redis"})

	seedObjects(t, s, svc, pod1, pod2, unmatched)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "nginx-svc"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Relations: map[string]v1alpha1.RelationSpec{
				"selects": {},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 root object, got %d", len(status.Objects))
	}

	selected := status.Objects[0].Relations["selects"]
	if len(selected) != 2 {
		t.Fatalf("expected 2 selected pods, got %d", len(selected))
	}
}

func TestRelation_References_PodToSecret(t *testing.T) {
	s := setupTestStore(t)
	pod := makePodWithVolumes("cluster-a", "default", "nginx-pod", []string{"nginx-tls", "nginx-config"}, "")
	secret1 := makeSecret("cluster-a", "default", "nginx-tls")
	secret2 := makeSecret("cluster-a", "default", "nginx-config")
	unrelated := makeSecret("cluster-a", "default", "other-secret")

	seedObjects(t, s, pod, secret1, secret2, unrelated)
	seedResourceTypes(t, s, podRT("cluster-a"), secretRT("cluster-a"))

	e := NewEngine(s)
	projJSON, _ := json.Marshal(map[string]any{"metadata": map[string]any{"name": true}})

	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "nginx-pod"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Object: &runtime.RawExtension{Raw: projJSON},
			Relations: map[string]v1alpha1.RelationSpec{
				"references": {
					Filters: []v1alpha1.ObjectFilter{
						{GroupKind: &v1alpha1.GroupKindFilter{Kind: "Secret"}},
					},
					Objects: &v1alpha1.ObjectsSpec{
						Object: &runtime.RawExtension{Raw: projJSON},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 root object, got %d", len(status.Objects))
	}

	refs := status.Objects[0].Relations["references"]
	if len(refs) != 2 {
		t.Fatalf("expected 2 referenced secrets, got %d", len(refs))
	}
}

func TestRelation_NestedDescendants(t *testing.T) {
	s := setupTestStore(t)
	deploy := makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z"))
	rs := makeReplicaSet("cluster-a", "default", "nginx-rs", deploy.UID)
	pod := makeOwnedPod("cluster-a", "default", "nginx-pod", rs.UID)

	seedObjects(t, s, deploy, rs, pod)

	e := NewEngine(s)
	projJSON, _ := json.Marshal(map[string]any{"metadata": map[string]any{"name": true}})

	// Deployment → descendants (ReplicaSet) → descendants (Pod)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "nginx"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Object: &runtime.RawExtension{Raw: projJSON},
			Relations: map[string]v1alpha1.RelationSpec{
				"descendants": {
					Objects: &v1alpha1.ObjectsSpec{
						Object: &runtime.RawExtension{Raw: projJSON},
						Relations: map[string]v1alpha1.RelationSpec{
							"descendants": {
								Objects: &v1alpha1.ObjectsSpec{
									Object: &runtime.RawExtension{Raw: projJSON},
								},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 root object, got %d", len(status.Objects))
	}

	root := status.Objects[0]
	descendants := root.Relations["descendants"]
	if len(descendants) != 1 {
		t.Fatalf("expected 1 level-1 descendant (RS), got %d", len(descendants))
	}

	rsResult := descendants[0]
	pods := rsResult.Relations["descendants"]
	if len(pods) != 1 {
		t.Fatalf("expected 1 level-2 descendant (Pod), got %d", len(pods))
	}
}

func TestRelation_DescendantsWithFilter(t *testing.T) {
	s := setupTestStore(t)
	deploy := makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z"))
	rs := makeReplicaSet("cluster-a", "default", "nginx-rs", deploy.UID)
	pod := makeOwnedPod("cluster-a", "default", "nginx-pod", deploy.UID) // Direct child of deploy

	seedObjects(t, s, deploy, rs, pod)
	seedResourceTypes(t, s,
		deploymentRT("cluster-a"),
		podRT("cluster-a"),
		&store.ResourceTypeModel{
			Cluster: "cluster-a", APIGroup: "apps", APIVersion: "v1",
			Kind: "ReplicaSet", Singular: "replicaset", Resource: "replicasets",
			ShortNames: mustJSON([]string{"rs"}), Categories: mustJSON([]string{"all"}),
			Namespaced: true,
		},
	)

	e := NewEngine(s)

	// Only get ReplicaSet descendants.
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "nginx"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Relations: map[string]v1alpha1.RelationSpec{
				"descendants": {
					Filters: []v1alpha1.ObjectFilter{
						{GroupKind: &v1alpha1.GroupKindFilter{Kind: "ReplicaSet"}},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(status.Objects) != 1 {
		t.Fatalf("expected 1 root, got %d", len(status.Objects))
	}

	descendants := status.Objects[0].Relations["descendants"]
	// Should only have the RS, not the Pod (even though Pod is also a direct descendant).
	if len(descendants) != 1 {
		t.Fatalf("expected 1 filtered descendant (RS only), got %d", len(descendants))
	}
}

func TestRelation_RelationLimit(t *testing.T) {
	s := setupTestStore(t)
	deploy := makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z"))
	// Create multiple descendants.
	for i := range 5 {
		pod := makeOwnedPod("cluster-a", "default", "pod-"+string(rune('a'+i)), deploy.UID)
		seedObjects(t, s, pod)
	}
	seedObjects(t, s, deploy)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "nginx"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Relations: map[string]v1alpha1.RelationSpec{
				"descendants": {
					Limit: 2,
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	descendants := status.Objects[0].Relations["descendants"]
	if len(descendants) != 2 {
		t.Fatalf("expected 2 descendants (limited), got %d", len(descendants))
	}
}

// --- Ref Path Registry Tests ---

func TestRefPathRegistry_Lookup(t *testing.T) {
	r := NewRefPathRegistry()
	paths := r.Lookup("", "Pod")
	if len(paths) == 0 {
		t.Fatal("expected ref paths for Pod")
	}

	// Should have entries for Secret, ConfigMap, etc.
	hasSecret := false
	for _, p := range paths {
		if p.TargetKind == "Secret" {
			hasSecret = true
		}
	}
	if !hasSecret {
		t.Fatal("expected Pod to have ref path to Secret")
	}
}

func TestRefPathRegistry_LookupForTarget(t *testing.T) {
	r := NewRefPathRegistry()
	paths := r.LookupForTarget("", "Pod", "", "Secret")
	if len(paths) == 0 {
		t.Fatal("expected ref paths from Pod to Secret")
	}

	paths = r.LookupForTarget("", "Pod", "", "NonExistent")
	if len(paths) != 0 {
		t.Fatal("expected no ref paths from Pod to NonExistent")
	}
}

// --- Tree Assembly Tests ---

func TestParentPathOf(t *testing.T) {
	tests := []struct {
		path   string
		expect string
	}{
		{".deployment.default/nginx", ""},
		{".deployment.default/nginx.replicaset.default/nginx-rs", ".deployment.default/nginx"},
		{".deployment.default/nginx.replicaset.default/rs.pod.default/pod", ".deployment.default/nginx.replicaset.default/rs"},
	}

	for _, tt := range tests {
		got := parentPathOf(tt.path)
		if got != tt.expect {
			t.Errorf("parentPathOf(%q) = %q, want %q", tt.path, got, tt.expect)
		}
	}
}
