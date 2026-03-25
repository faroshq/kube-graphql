package gc

import (
	"context"
	"log/slog"
	"time"

	"github.com/faroshq/kuery/internal/metrics"
	"github.com/faroshq/kuery/internal/store"
)

// GarbageCollector cleans up stale clusters and their objects.
type GarbageCollector struct {
	store    store.Store
	interval time.Duration
	logger   *slog.Logger
}

// NewGarbageCollector creates a new GC instance.
func NewGarbageCollector(s store.Store, interval time.Duration) *GarbageCollector {
	return &GarbageCollector{
		store:    s,
		interval: interval,
		logger:   slog.Default().With("component", "gc"),
	}
}

// Run starts the GC loop. It blocks until ctx is cancelled.
func (gc *GarbageCollector) Run(ctx context.Context) {
	gc.logger.Info("starting garbage collector", "interval", gc.interval)
	ticker := time.NewTicker(gc.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			gc.logger.Info("garbage collector stopped")
			return
		case <-ticker.C:
			gc.collect(ctx)
		}
	}
}

// RunOnce performs a single GC pass. Useful for testing.
func (gc *GarbageCollector) RunOnce(ctx context.Context) {
	gc.collect(ctx)
}

func (gc *GarbageCollector) collect(ctx context.Context) {
	now := time.Now()

	clusters, err := gc.store.ListStaleClusters(ctx, now)
	if err != nil {
		gc.logger.Error("failed to list stale clusters", "error", err)
		return
	}

	for _, cluster := range clusters {
		// Check if the cluster's TTL has expired.
		expiry := cluster.LastSeen.Add(time.Duration(cluster.TTL) * time.Second)
		if now.Before(expiry) {
			continue
		}

		gc.logger.Info("cleaning up expired cluster",
			"cluster", cluster.Name,
			"lastSeen", cluster.LastSeen,
			"ttl", cluster.TTL)

		// Delete objects first, then resource types, then the cluster record.
		if err := gc.store.DeleteObjectsForCluster(ctx, cluster.Name); err != nil {
			gc.logger.Error("failed to delete objects for cluster", "cluster", cluster.Name, "error", err)
			continue
		}
		if err := gc.store.DeleteResourceTypesForCluster(ctx, cluster.Name); err != nil {
			gc.logger.Error("failed to delete resource types for cluster", "cluster", cluster.Name, "error", err)
			continue
		}
		if err := gc.store.DeleteCluster(ctx, cluster.Name); err != nil {
			gc.logger.Error("failed to delete cluster", "cluster", cluster.Name, "error", err)
			continue
		}

		gc.logger.Info("cleaned up cluster", "cluster", cluster.Name)
		metrics.ClustersTotal.WithLabelValues("stale").Dec()
	}
}
