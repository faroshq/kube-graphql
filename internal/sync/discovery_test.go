package sync

import (
	"context"
	"testing"

	"github.com/faroshq/kuery/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
)

func TestRunDiscovery(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Set up fake discovery with some API resources.
	fakeClient := fakeclientset.NewSimpleClientset()
	fakeDiscovery := fakeClient.Discovery().(*fakediscovery.FakeDiscovery)
	fakeDiscovery.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{
					Name:       "pods",
					Kind:       "Pod",
					Namespaced: true,
					Verbs:      metav1.Verbs{"get", "list", "watch", "create", "delete"},
					ShortNames: []string{"po"},
					Categories: []string{"all"},
				},
				{
					Name:       "pods/status",
					Kind:       "Pod",
					Namespaced: true,
					Verbs:      metav1.Verbs{"get", "patch", "update"},
				},
				{
					Name:       "secrets",
					Kind:       "Secret",
					Namespaced: true,
					Verbs:      metav1.Verbs{"get", "list", "watch", "create", "delete"},
				},
				{
					Name:       "configmaps",
					Kind:       "ConfigMap",
					Namespaced: true,
					Verbs:      metav1.Verbs{"get", "list", "watch", "create", "delete"},
				},
				{
					Name:       "namespaces",
					Kind:       "Namespace",
					Namespaced: false,
					Verbs:      metav1.Verbs{"get", "list", "watch", "create", "delete"},
				},
			},
		},
		{
			GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{
				{
					Name:         "deployments",
					SingularName: "deployment",
					Kind:         "Deployment",
					Namespaced:   true,
					Verbs:        metav1.Verbs{"get", "list", "watch", "create", "delete", "update", "patch"},
					ShortNames:   []string{"deploy"},
					Categories:   []string{"all"},
				},
			},
		},
	}

	bl := NewBlacklist(DefaultBlacklist) // secrets and events blacklisted
	watchable, err := RunDiscovery(ctx, "test-cluster", fakeDiscovery, s, bl)
	if err != nil {
		t.Fatalf("RunDiscovery failed: %v", err)
	}

	// Secrets should be blacklisted, pods/status should be skipped (subresource).
	// Watchable: pods, configmaps, namespaces, deployments = 4
	if len(watchable) != 4 {
		t.Errorf("expected 4 watchable resources, got %d", len(watchable))
		for _, w := range watchable {
			t.Logf("  watchable: %s", w.GVR.String())
		}
	}

	// Verify secrets are NOT in watchable.
	for _, w := range watchable {
		if w.GVR.Resource == "secrets" {
			t.Error("secrets should be blacklisted")
		}
	}

	// Verify resource_types table was populated.
	var count int64
	s.RawDB().Model(&store.ResourceTypeModel{}).Where("cluster = ?", "test-cluster").Count(&count)
	// pods, configmaps, namespaces, deployments = 4 (secrets blacklisted, pods/status skipped)
	if count != 4 {
		t.Errorf("expected 4 resource types in DB, got %d", count)
	}

	// Verify a specific resource type.
	var rt store.ResourceTypeModel
	s.RawDB().First(&rt, "cluster = ? AND resource = ?", "test-cluster", "deployments")
	if rt.Kind != "Deployment" {
		t.Errorf("Kind = %q, want %q", rt.Kind, "Deployment")
	}
	if !rt.Namespaced {
		t.Error("deployments should be namespaced")
	}
}

func TestRunDiscovery_ClearsOldEntries(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Seed an old resource type.
	s.UpsertResourceType(ctx, &store.ResourceTypeModel{
		Cluster:    "test-cluster",
		APIGroup:   "old.group",
		APIVersion: "v1",
		Kind:       "OldResource",
		Resource:   "oldresources",
	})

	fakeClient := fakeclientset.NewSimpleClientset()
	fakeDiscovery := fakeClient.Discovery().(*fakediscovery.FakeDiscovery)
	fakeDiscovery.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{
					Name:       "configmaps",
					Kind:       "ConfigMap",
					Namespaced: true,
					Verbs:      metav1.Verbs{"get", "list", "watch"},
				},
			},
		},
	}

	bl := NewBlacklist(nil)
	_, err := RunDiscovery(ctx, "test-cluster", fakeDiscovery, s, bl)
	if err != nil {
		t.Fatalf("RunDiscovery failed: %v", err)
	}

	// Old resource type should be gone.
	var count int64
	s.RawDB().Model(&store.ResourceTypeModel{}).Where("cluster = ? AND resource = ?", "test-cluster", "oldresources").Count(&count)
	if count != 0 {
		t.Error("old resource type should have been cleared")
	}
}

func TestBlacklist(t *testing.T) {
	bl := NewBlacklist(DefaultBlacklist)

	if !bl.IsBlacklisted(schema.GroupResource{Group: "", Resource: "secrets"}) {
		t.Error("secrets should be blacklisted")
	}
	if !bl.IsBlacklisted(schema.GroupResource{Group: "", Resource: "events"}) {
		t.Error("core events should be blacklisted")
	}
	if !bl.IsBlacklisted(schema.GroupResource{Group: "events.k8s.io", Resource: "events"}) {
		t.Error("events.k8s.io events should be blacklisted")
	}
	if bl.IsBlacklisted(schema.GroupResource{Group: "apps", Resource: "deployments"}) {
		t.Error("deployments should not be blacklisted")
	}
}

func TestBlacklist_Empty(t *testing.T) {
	bl := NewBlacklist(nil)
	if bl.IsBlacklisted(schema.GroupResource{Group: "", Resource: "secrets"}) {
		t.Error("empty blacklist should not blacklist anything")
	}
}
