package gc

import (
	"context"
	"testing"
	"time"

	"github.com/faroshq/kuery/internal/store"

	"github.com/google/uuid"
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

func TestGC_DeletesExpiredClusters(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	// Seed a stale cluster that expired.
	longAgo := time.Now().Add(-2 * time.Hour)
	s.UpsertCluster(ctx, &store.ClusterModel{
		Name:     "expired-cluster",
		Status:   "stale",
		LastSeen: longAgo,
		TTL:      3600, // 1 hour
	})

	// Seed objects for this cluster.
	s.UpsertObject(ctx, &store.ObjectModel{
		ID: uuid.New(), UID: "uid-1", Cluster: "expired-cluster",
		APIVersion: "v1", Kind: "Pod", Resource: "pods",
		Name: "test-pod", Object: datatypes.JSON("{}"),
	})

	gc := NewGarbageCollector(s, time.Minute)
	gc.RunOnce(ctx)

	// Cluster and its objects should be deleted.
	_, err := s.GetCluster(ctx, "expired-cluster")
	if err == nil {
		t.Fatal("expected cluster to be deleted")
	}
}

func TestGC_KeepsActiveClusters(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	s.UpsertCluster(ctx, &store.ClusterModel{
		Name:     "active-cluster",
		Status:   "active",
		LastSeen: time.Now(),
		TTL:      3600,
	})

	gc := NewGarbageCollector(s, time.Minute)
	gc.RunOnce(ctx)

	c, err := s.GetCluster(ctx, "active-cluster")
	if err != nil {
		t.Fatal("expected active cluster to be kept")
	}
	if c.Status != "active" {
		t.Fatalf("expected status 'active', got %s", c.Status)
	}
}

func TestGC_KeepsRecentStaleClusters(t *testing.T) {
	s := setupTestStore(t)
	ctx := context.Background()

	// Stale but TTL not yet expired.
	s.UpsertCluster(ctx, &store.ClusterModel{
		Name:     "recent-stale",
		Status:   "stale",
		LastSeen: time.Now().Add(-30 * time.Minute),
		TTL:      3600, // 1 hour — not expired yet
	})

	gc := NewGarbageCollector(s, time.Minute)
	gc.RunOnce(ctx)

	_, err := s.GetCluster(ctx, "recent-stale")
	if err != nil {
		t.Fatal("expected recent stale cluster to be kept")
	}
}
