package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/faroshq/kuery/internal/store"
	"github.com/google/uuid"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

// objectIDNamespace is a fixed UUID namespace for generating deterministic object IDs.
// ID = UUID v5(namespace, "cluster/group/kind/namespace/name").
var objectIDNamespace = uuid.MustParse("a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d")

// EventHandler implements cache.ResourceEventHandler to sync Kubernetes objects
// into the store.
type EventHandler struct {
	Store       store.Store
	ClusterName string
	GVR         schema.GroupVersionResource
	Kind        string
}

var _ cache.ResourceEventHandler = &EventHandler{}

func (h *EventHandler) OnAdd(obj interface{}, isInInitialList bool) {
	h.upsert(obj)
}

func (h *EventHandler) OnUpdate(oldObj, newObj interface{}) {
	h.upsert(newObj)
}

func (h *EventHandler) OnDelete(obj interface{}) {
	u, ok := toUnstructured(obj)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := h.Store.DeleteObject(ctx, h.ClusterName, h.GVR.Group, h.Kind, u.GetNamespace(), u.GetName()); err != nil {
		klog.ErrorS(err, "failed to delete object",
			"cluster", h.ClusterName, "kind", h.Kind,
			"namespace", u.GetNamespace(), "name", u.GetName())
	}
}

func (h *EventHandler) upsert(obj interface{}) {
	u, ok := toUnstructured(obj)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	model, err := h.toObjectModel(u)
	if err != nil {
		klog.ErrorS(err, "failed to convert object to model",
			"cluster", h.ClusterName, "kind", h.Kind,
			"namespace", u.GetNamespace(), "name", u.GetName())
		return
	}

	if err := h.Store.UpsertObject(ctx, model); err != nil {
		klog.ErrorS(err, "failed to upsert object",
			"cluster", h.ClusterName, "kind", h.Kind,
			"namespace", u.GetNamespace(), "name", u.GetName())
	}
}

func (h *EventHandler) toObjectModel(u *unstructured.Unstructured) (*store.ObjectModel, error) {
	// Deterministic UUID from cluster + GVK + namespace + name.
	idKey := fmt.Sprintf("%s/%s/%s/%s/%s", h.ClusterName, h.GVR.Group, h.Kind, u.GetNamespace(), u.GetName())
	id := uuid.NewSHA1(objectIDNamespace, []byte(idKey))

	// Marshal full object.
	objJSON, err := json.Marshal(u.Object)
	if err != nil {
		return nil, fmt.Errorf("marshal object: %w", err)
	}

	// Extract labels.
	labelsJSON, _ := json.Marshal(u.GetLabels())

	// Extract annotations.
	annotationsJSON, _ := json.Marshal(u.GetAnnotations())

	// Extract ownerReferences.
	ownerRefs := u.GetOwnerReferences()
	ownerRefsJSON, _ := json.Marshal(ownerRefs)

	// Extract conditions from status.conditions if present.
	conditionsJSON := extractConditions(u)

	// Parse creation timestamp.
	var creationTS *time.Time
	ct := u.GetCreationTimestamp()
	if !ct.IsZero() {
		t := ct.Time
		creationTS = &t
	}

	return &store.ObjectModel{
		ID:              id,
		UID:             string(u.GetUID()),
		Cluster:         h.ClusterName,
		APIGroup:        h.GVR.Group,
		APIVersion:      h.GVR.Version,
		Kind:            h.Kind,
		Resource:        h.GVR.Resource,
		Namespace:       u.GetNamespace(),
		Name:            u.GetName(),
		Labels:          labelsJSON,
		Annotations:     annotationsJSON,
		OwnerRefs:       ownerRefsJSON,
		Conditions:      conditionsJSON,
		CreationTS:      creationTS,
		ResourceVersion: u.GetResourceVersion(),
		Object:          objJSON,
	}, nil
}

// extractConditions extracts status.conditions from the unstructured object.
func extractConditions(u *unstructured.Unstructured) []byte {
	status, ok := u.Object["status"].(map[string]interface{})
	if !ok {
		return []byte("[]")
	}
	conditions, ok := status["conditions"]
	if !ok {
		return []byte("[]")
	}
	data, err := json.Marshal(conditions)
	if err != nil {
		return []byte("[]")
	}
	return data
}

func toUnstructured(obj interface{}) (*unstructured.Unstructured, bool) {
	if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
		obj = d.Obj
	}
	u, ok := obj.(*unstructured.Unstructured)
	return u, ok
}

