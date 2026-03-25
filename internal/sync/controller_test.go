package sync

import (
	"context"
	"testing"
	"time"

	"github.com/faroshq/kuery/internal/store"
)

func TestSyncController_EngageDisengage(t *testing.T) {
	s := newTestStore(t)

	sc := NewSyncController(Config{
		Store:     s,
		Blacklist: NewBlacklist(DefaultBlacklist),
	})

	ctx := context.Background()

	// We can't fully test Engage without a real cluster, but we can test
	// that Disengage marks the cluster as stale.
	now := time.Now()
	s.UpsertCluster(ctx, &store.ClusterModel{
		Name:     "test-cluster",
		Status:   "active",
		LastSeen: now,
	})

	if err := sc.Disengage(ctx, "test-cluster"); err != nil {
		t.Fatalf("Disengage failed: %v", err)
	}

	c, err := s.GetCluster(ctx, "test-cluster")
	if err != nil {
		t.Fatalf("GetCluster failed: %v", err)
	}
	if c.Status != "stale" {
		t.Errorf("Status = %q, want %q", c.Status, "stale")
	}
}

func TestSyncController_DefaultConfig(t *testing.T) {
	s := newTestStore(t)

	sc := NewSyncController(Config{
		Store: s,
	})

	if sc.config.ResyncPeriod != DefaultResyncPeriod {
		t.Errorf("ResyncPeriod = %v, want %v", sc.config.ResyncPeriod, DefaultResyncPeriod)
	}
	if sc.config.Blacklist == nil {
		t.Error("Blacklist should default to DefaultBlacklist")
	}
}
