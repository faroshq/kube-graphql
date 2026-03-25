package sync

import (
	"encoding/json"
	"testing"

	"github.com/faroshq/kuery/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

func newTestStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewStore(store.Config{Driver: "sqlite", DSN: ":memory:"})
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	if err := s.AutoMigrate(); err != nil {
		t.Fatalf("failed to auto-migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func newTestHandler(t *testing.T, s store.Store) *EventHandler {
	t.Helper()
	return &EventHandler{
		Store:       s,
		ClusterName: "test-cluster",
		GVR:         schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		Kind:        "Deployment",
	}
}

func newDeploymentObj(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":              name,
				"namespace":         namespace,
				"uid":               "uid-" + name,
				"resourceVersion":   "100",
				"creationTimestamp": "2025-06-01T00:00:00Z",
				"labels": map[string]interface{}{
					"app": name,
				},
			},
			"spec": map[string]interface{}{
				"replicas": int64(3),
			},
			"status": map[string]interface{}{
				"conditions": []interface{}{
					map[string]interface{}{
						"type":   "Available",
						"status": "True",
					},
				},
			},
		},
	}
}

func TestEventHandler_OnAdd(t *testing.T) {
	s := newTestStore(t)
	h := newTestHandler(t, s)

	obj := newDeploymentObj("nginx", "default")
	h.OnAdd(obj, true)

	// Verify the object was stored.
	var count int64
	s.RawDB().Model(&store.ObjectModel{}).Where("name = ? AND cluster = ?", "nginx", "test-cluster").Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 object, got %d", count)
	}

	// Verify fields.
	var stored store.ObjectModel
	s.RawDB().First(&stored, "name = ? AND cluster = ?", "nginx", "test-cluster")

	if stored.UID != "uid-nginx" {
		t.Errorf("UID = %q, want %q", stored.UID, "uid-nginx")
	}
	if stored.Kind != "Deployment" {
		t.Errorf("Kind = %q, want %q", stored.Kind, "Deployment")
	}
	if stored.Namespace != "default" {
		t.Errorf("Namespace = %q, want %q", stored.Namespace, "default")
	}
	if stored.APIGroup != "apps" {
		t.Errorf("APIGroup = %q, want %q", stored.APIGroup, "apps")
	}

	// Verify labels were extracted.
	var labels map[string]string
	json.Unmarshal(stored.Labels, &labels)
	if labels["app"] != "nginx" {
		t.Errorf("Labels[app] = %q, want %q", labels["app"], "nginx")
	}

	// Verify conditions were extracted.
	var conditions []map[string]interface{}
	json.Unmarshal(stored.Conditions, &conditions)
	if len(conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(conditions))
	}
	if conditions[0]["type"] != "Available" {
		t.Errorf("condition type = %q, want %q", conditions[0]["type"], "Available")
	}
}

func TestEventHandler_OnUpdate(t *testing.T) {
	s := newTestStore(t)
	h := newTestHandler(t, s)

	obj := newDeploymentObj("nginx", "default")
	h.OnAdd(obj, true)

	// Update the object.
	updated := newDeploymentObj("nginx", "default")
	updated.Object["metadata"].(map[string]interface{})["resourceVersion"] = "200"
	updated.Object["spec"].(map[string]interface{})["replicas"] = int64(5)
	h.OnUpdate(obj, updated)

	// Verify the object was updated.
	var stored store.ObjectModel
	s.RawDB().First(&stored, "name = ? AND cluster = ?", "nginx", "test-cluster")
	if stored.ResourceVersion != "200" {
		t.Errorf("ResourceVersion = %q, want %q", stored.ResourceVersion, "200")
	}
}

func TestEventHandler_OnDelete(t *testing.T) {
	s := newTestStore(t)
	h := newTestHandler(t, s)

	obj := newDeploymentObj("nginx", "default")
	h.OnAdd(obj, true)

	h.OnDelete(obj)

	var count int64
	s.RawDB().Model(&store.ObjectModel{}).Where("name = ? AND cluster = ?", "nginx", "test-cluster").Count(&count)
	if count != 0 {
		t.Errorf("expected 0 objects after delete, got %d", count)
	}
}

func TestEventHandler_OnDeleteFinalStateUnknown(t *testing.T) {
	s := newTestStore(t)
	h := newTestHandler(t, s)

	obj := newDeploymentObj("nginx", "default")
	h.OnAdd(obj, true)

	// Simulate DeletedFinalStateUnknown (tombstone).
	tombstone := cache.DeletedFinalStateUnknown{
		Key: "default/nginx",
		Obj: obj,
	}
	h.OnDelete(tombstone)

	var count int64
	s.RawDB().Model(&store.ObjectModel{}).Where("name = ? AND cluster = ?", "nginx", "test-cluster").Count(&count)
	if count != 0 {
		t.Errorf("expected 0 objects after tombstone delete, got %d", count)
	}
}

func TestEventHandler_DeterministicID(t *testing.T) {
	s := newTestStore(t)
	h := newTestHandler(t, s)

	obj := newDeploymentObj("nginx", "default")
	h.OnAdd(obj, true)

	var stored1 store.ObjectModel
	s.RawDB().First(&stored1, "name = ? AND cluster = ?", "nginx", "test-cluster")

	// Delete and re-add — should get the same ID.
	h.OnDelete(obj)
	h.OnAdd(obj, false)

	var stored2 store.ObjectModel
	s.RawDB().First(&stored2, "name = ? AND cluster = ?", "nginx", "test-cluster")

	if stored1.ID != stored2.ID {
		t.Errorf("IDs should be deterministic: %v != %v", stored1.ID, stored2.ID)
	}
}

func TestEventHandler_OwnerRefs(t *testing.T) {
	s := newTestStore(t)
	h := &EventHandler{
		Store:       s,
		ClusterName: "test-cluster",
		GVR:         schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"},
		Kind:        "ReplicaSet",
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "ReplicaSet",
			"metadata": map[string]interface{}{
				"name":            "nginx-abc123",
				"namespace":       "default",
				"uid":             "rs-uid",
				"resourceVersion": "50",
				"ownerReferences": []interface{}{
					map[string]interface{}{
						"apiVersion": "apps/v1",
						"kind":       "Deployment",
						"name":       "nginx",
						"uid":        "deploy-uid",
					},
				},
			},
		},
	}

	h.OnAdd(obj, true)

	var stored store.ObjectModel
	s.RawDB().First(&stored, "name = ? AND cluster = ?", "nginx-abc123", "test-cluster")

	var ownerRefs []metav1.OwnerReference
	if err := json.Unmarshal(stored.OwnerRefs, &ownerRefs); err != nil {
		t.Fatalf("failed to unmarshal owner refs: %v", err)
	}
	if len(ownerRefs) != 1 {
		t.Fatalf("expected 1 owner ref, got %d", len(ownerRefs))
	}
	if string(ownerRefs[0].UID) != "deploy-uid" {
		t.Errorf("ownerRef UID = %q, want %q", ownerRefs[0].UID, "deploy-uid")
	}
}

