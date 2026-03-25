package store

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

func newTestStore(t *testing.T) Store {
	t.Helper()
	s, err := NewStore(Config{Driver: "sqlite", DSN: ":memory:"})
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	if err := s.AutoMigrate(); err != nil {
		t.Fatalf("failed to auto-migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAutoMigrate(t *testing.T) {
	s := newTestStore(t)

	// Verify tables exist by running a count query on each.
	db := s.RawDB()
	for _, table := range []string{"objects", "resource_types", "clusters", "object_labels"} {
		var count int64
		if err := db.Table(table).Count(&count).Error; err != nil {
			t.Errorf("table %q should exist after auto-migrate: %v", table, err)
		}
	}
}

func TestUpsertAndGetObject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Second)
	obj := &ObjectModel{
		ID:              uuid.New(),
		UID:             "test-uid-123",
		Cluster:         "cluster-a",
		APIGroup:        "apps",
		APIVersion:      "v1",
		Kind:            "Deployment",
		Resource:        "deployments",
		Namespace:       "default",
		Name:            "nginx",
		Labels:          datatypes.JSON(`{"app":"nginx"}`),
		Annotations:     datatypes.JSON(`{}`),
		OwnerRefs:       datatypes.JSON(`[]`),
		Conditions:      datatypes.JSON(`[{"type":"Available","status":"True"}]`),
		CreationTS:      &now,
		ResourceVersion: "12345",
		Object:          datatypes.JSON(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"nginx"}}`),
	}

	if err := s.UpsertObject(ctx, obj); err != nil {
		t.Fatalf("UpsertObject failed: %v", err)
	}

	got, err := s.GetObject(ctx, obj.ID)
	if err != nil {
		t.Fatalf("GetObject failed: %v", err)
	}
	if got.UID != obj.UID {
		t.Errorf("UID = %q, want %q", got.UID, obj.UID)
	}
	if got.Name != "nginx" {
		t.Errorf("Name = %q, want %q", got.Name, "nginx")
	}
	if got.Cluster != "cluster-a" {
		t.Errorf("Cluster = %q, want %q", got.Cluster, "cluster-a")
	}
}

func TestUpsertObjectConflict(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	obj := &ObjectModel{
		ID:         uuid.New(),
		UID:        "uid-1",
		Cluster:    "cluster-a",
		APIGroup:   "apps",
		APIVersion: "v1",
		Kind:       "Deployment",
		Resource:   "deployments",
		Namespace:  "default",
		Name:       "nginx",
		Object:     datatypes.JSON(`{}`),
	}
	if err := s.UpsertObject(ctx, obj); err != nil {
		t.Fatalf("first upsert failed: %v", err)
	}

	// Upsert with same unique key but different UID (simulates resource version change).
	obj2 := &ObjectModel{
		ID:              uuid.New(),
		UID:             "uid-2",
		Cluster:         "cluster-a",
		APIGroup:        "apps",
		APIVersion:      "v1",
		Kind:            "Deployment",
		Resource:        "deployments",
		Namespace:       "default",
		Name:            "nginx",
		ResourceVersion: "99999",
		Object:          datatypes.JSON(`{"updated":true}`),
	}
	if err := s.UpsertObject(ctx, obj2); err != nil {
		t.Fatalf("conflict upsert failed: %v", err)
	}

	// Should have updated the existing row (same unique key).
	got, err := s.GetObject(ctx, obj.ID)
	if err != nil {
		t.Fatalf("GetObject failed: %v", err)
	}
	if got.UID != "uid-2" {
		t.Errorf("UID should be updated to %q, got %q", "uid-2", got.UID)
	}
	if got.ResourceVersion != "99999" {
		t.Errorf("ResourceVersion should be %q, got %q", "99999", got.ResourceVersion)
	}
}

func TestDeleteObject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	obj := &ObjectModel{
		ID:         uuid.New(),
		UID:        "uid-del",
		Cluster:    "cluster-a",
		APIGroup:   "",
		APIVersion: "v1",
		Kind:       "ConfigMap",
		Resource:   "configmaps",
		Namespace:  "default",
		Name:       "my-config",
		Object:     datatypes.JSON(`{}`),
	}
	if err := s.UpsertObject(ctx, obj); err != nil {
		t.Fatalf("UpsertObject failed: %v", err)
	}

	if err := s.DeleteObject(ctx, "cluster-a", "", "ConfigMap", "default", "my-config"); err != nil {
		t.Fatalf("DeleteObject failed: %v", err)
	}

	_, err := s.GetObject(ctx, obj.ID)
	if err == nil {
		t.Error("expected error getting deleted object, got nil")
	}
}

func TestUpsertResourceType(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rt := &ResourceTypeModel{
		Cluster:    "cluster-a",
		APIGroup:   "apps",
		APIVersion: "v1",
		Kind:       "Deployment",
		Singular:   "deployment",
		Resource:   "deployments",
		ShortNames: datatypes.JSON(`["deploy"]`),
		Categories: datatypes.JSON(`["all"]`),
		Namespaced: true,
	}
	if err := s.UpsertResourceType(ctx, rt); err != nil {
		t.Fatalf("UpsertResourceType failed: %v", err)
	}

	// Verify by querying directly.
	var got ResourceTypeModel
	if err := s.RawDB().First(&got, "cluster = ? AND resource = ?", "cluster-a", "deployments").Error; err != nil {
		t.Fatalf("query failed: %v", err)
	}
	if got.Kind != "Deployment" {
		t.Errorf("Kind = %q, want %q", got.Kind, "Deployment")
	}
	if !got.Namespaced {
		t.Error("expected Namespaced to be true")
	}
}

func TestUpsertAndGetCluster(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().Truncate(time.Second)
	c := &ClusterModel{
		Name:      "cluster-a",
		Status:    "active",
		LastSeen:  now,
		EngagedAt: &now,
		Labels:    datatypes.JSON(`{"env":"production"}`),
		TTL:       3600,
	}
	if err := s.UpsertCluster(ctx, c); err != nil {
		t.Fatalf("UpsertCluster failed: %v", err)
	}

	got, err := s.GetCluster(ctx, "cluster-a")
	if err != nil {
		t.Fatalf("GetCluster failed: %v", err)
	}
	if got.Status != "active" {
		t.Errorf("Status = %q, want %q", got.Status, "active")
	}
	if got.TTL != 3600 {
		t.Errorf("TTL = %d, want %d", got.TTL, 3600)
	}
}

func TestDeleteResourceTypesForCluster(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	rt := &ResourceTypeModel{
		Cluster:    "cluster-b",
		APIGroup:   "",
		APIVersion: "v1",
		Kind:       "Pod",
		Resource:   "pods",
		Namespaced: true,
	}
	if err := s.UpsertResourceType(ctx, rt); err != nil {
		t.Fatalf("UpsertResourceType failed: %v", err)
	}

	if err := s.DeleteResourceTypesForCluster(ctx, "cluster-b"); err != nil {
		t.Fatalf("DeleteResourceTypesForCluster failed: %v", err)
	}

	var count int64
	s.RawDB().Model(&ResourceTypeModel{}).Where("cluster = ?", "cluster-b").Count(&count)
	if count != 0 {
		t.Errorf("expected 0 resource types for cluster-b, got %d", count)
	}
}
