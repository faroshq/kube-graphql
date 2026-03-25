package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestTransitive_DescendantsPlus(t *testing.T) {
	s := setupTestStore(t)
	// Deployment → ReplicaSet → Pod (3-level chain)
	deploy := makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z"))
	rs := makeReplicaSet("cluster-a", "default", "nginx-rs", deploy.UID)
	pod := makeOwnedPod("cluster-a", "default", "nginx-pod", rs.UID)

	seedObjects(t, s, deploy, rs, pod)

	e := NewEngine(s)
	projJSON, _ := json.Marshal(map[string]any{"metadata": map[string]any{"name": true}})

	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "nginx"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Object: &runtime.RawExtension{Raw: projJSON},
			Relations: map[string]v1alpha1.RelationSpec{
				"descendants+": {
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
	descendants := root.Relations["descendants+"]
	// Should find RS as direct descendant.
	if len(descendants) != 1 {
		t.Fatalf("expected 1 direct descendant (RS), got %d", len(descendants))
	}

	// Pod is nested under RS as a deeper descendant.
	rsResult := descendants[0]
	nestedDescendants := rsResult.Relations["descendants+"]
	if len(nestedDescendants) != 1 {
		t.Fatalf("expected 1 nested descendant (Pod) under RS, got %d", len(nestedDescendants))
	}
}

func TestTransitive_OwnersPlus(t *testing.T) {
	s := setupTestStore(t)
	// Pod → ReplicaSet → Deployment (3-level chain via ownerRefs)
	deploy := makeDeployment("cluster-a", "default", "nginx", nil, ts("2025-06-01T00:00:00Z"))
	rs := makeReplicaSet("cluster-a", "default", "nginx-rs", deploy.UID)
	pod := makeOwnedPod("cluster-a", "default", "nginx-pod", rs.UID)

	seedObjects(t, s, deploy, rs, pod)

	e := NewEngine(s)
	projJSON, _ := json.Marshal(map[string]any{"metadata": map[string]any{"name": true}})

	// Query the Pod, ask for transitive owners.
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "nginx-pod"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Object: &runtime.RawExtension{Raw: projJSON},
			Relations: map[string]v1alpha1.RelationSpec{
				"owners+": {
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

	owners := status.Objects[0].Relations["owners+"]
	// Should find RS as direct owner.
	if len(owners) != 1 {
		t.Fatalf("expected 1 direct owner (RS), got %d", len(owners))
	}
	// Deployment is nested under RS.
	nestedOwners := owners[0].Relations["owners+"]
	if len(nestedOwners) != 1 {
		t.Fatalf("expected 1 nested owner (Deploy) under RS, got %d", len(nestedOwners))
	}
}

func TestTransitive_CycleDetection(t *testing.T) {
	s := setupTestStore(t)
	// Create a cycle: A owns B, B owns A.
	objA := makeDeployment("cluster-a", "default", "obj-a", nil, ts("2025-06-01T00:00:00Z"))
	objB := makeOwnedPod("cluster-a", "default", "obj-b", objA.UID)
	// Make A also owned by B to create a cycle.
	objA.OwnerRefs = mustJSON([]map[string]string{{"uid": objB.UID}})

	seedObjects(t, s, objA, objB)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "obj-a"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Relations: map[string]v1alpha1.RelationSpec{
				"descendants+": {},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Should not infinite loop. B is a descendant but should not re-find A.
	descendants := status.Objects[0].Relations["descendants+"]
	if len(descendants) != 1 {
		t.Fatalf("expected 1 descendant (cycle broken), got %d", len(descendants))
	}
}

func TestTransitive_DepthLimit(t *testing.T) {
	s := setupTestStore(t)
	// Create a 5-level chain: root → a → b → c → d → e
	root := makeDeployment("cluster-a", "default", "root", nil, ts("2025-06-01T00:00:00Z"))
	a := makeOwnedPod("cluster-a", "default", "child-a", root.UID)
	b := makeOwnedPod("cluster-a", "default", "child-b", a.UID)
	c := makeOwnedPod("cluster-a", "default", "child-c", b.UID)
	d := makeOwnedPod("cluster-a", "default", "child-d", c.UID)
	e := makeOwnedPod("cluster-a", "default", "child-e", d.UID)

	seedObjects(t, s, root, a, b, c, d, e)

	e2 := NewEngine(s)
	status, err := e2.Execute(context.Background(), &v1alpha1.QuerySpec{
		MaxDepth: 3, // Limit to 3 levels deep.
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "root"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Relations: map[string]v1alpha1.RelationSpec{
				"descendants+": {},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	descendants := status.Objects[0].Relations["descendants+"]
	// With maxDepth=3, should find chain: a → b → c (depth 1, 2, 3).
	// d and e are beyond depth 3.
	if len(descendants) != 1 {
		t.Fatalf("expected 1 direct descendant (a), got %d", len(descendants))
	}
	// Walk the chain.
	current := descendants[0]
	for depth := 2; depth <= 3; depth++ {
		nested := current.Relations["descendants+"]
		if len(nested) != 1 {
			t.Fatalf("expected 1 nested descendant at depth %d, got %d", depth, len(nested))
		}
		current = nested[0]
	}
	// At depth 3, there should be no more children (d is at depth 4, beyond limit).
	if len(current.Relations["descendants+"]) != 0 {
		t.Fatalf("expected 0 descendants beyond depth limit, got %d", len(current.Relations["descendants+"]))
	}
}

func TestTransitive_DescendantsPlus_WithLimit(t *testing.T) {
	s := setupTestStore(t)
	root := makeDeployment("cluster-a", "default", "root", nil, ts("2025-06-01T00:00:00Z"))
	for i := range 5 {
		pod := makeOwnedPod("cluster-a", "default", "pod-"+string(rune('a'+i)), root.UID)
		seedObjects(t, s, pod)
	}
	seedObjects(t, s, root)

	e := NewEngine(s)
	status, err := e.Execute(context.Background(), &v1alpha1.QuerySpec{
		Filter: &v1alpha1.QueryFilter{
			Objects: []v1alpha1.ObjectFilter{
				{Name: "root"},
			},
		},
		Objects: &v1alpha1.ObjectsSpec{
			Relations: map[string]v1alpha1.RelationSpec{
				"descendants+": {Limit: 2},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	descendants := status.Objects[0].Relations["descendants+"]
	if len(descendants) != 2 {
		t.Fatalf("expected 2 descendants (limited), got %d", len(descendants))
	}
}
