package sync

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/faroshq/kuery/internal/store"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

// DefaultResyncPeriod is the default informer resync interval.
const DefaultResyncPeriod = 10 * time.Minute

// Config holds configuration for the SyncController.
type Config struct {
	Store         store.Store
	Blacklist     *Blacklist
	ResyncPeriod  time.Duration
}

// SyncController manages per-cluster informers that sync Kubernetes objects into the store.
// It implements the multicluster-runtime Aware interface via Engage/Disengage.
type SyncController struct {
	config Config

	mu       sync.Mutex
	clusters map[string]*clusterState
}

// clusterState tracks the running informers and cancel function for a single cluster.
type clusterState struct {
	cancel context.CancelFunc
}

// NewSyncController creates a new SyncController.
func NewSyncController(cfg Config) *SyncController {
	if cfg.ResyncPeriod == 0 {
		cfg.ResyncPeriod = DefaultResyncPeriod
	}
	if cfg.Blacklist == nil {
		cfg.Blacklist = NewBlacklist(DefaultBlacklist)
	}
	return &SyncController{
		config:   cfg,
		clusters: make(map[string]*clusterState),
	}
}

// Engage is called when a cluster becomes available. It runs discovery,
// populates resource_types, starts informers for all watchable resources,
// and marks the cluster as active.
func (sc *SyncController) Engage(ctx context.Context, clusterName string, cl cluster.Cluster) error {
	logger := klog.FromContext(ctx).WithValues("cluster", clusterName)
	logger.Info("engaging cluster")

	sc.mu.Lock()
	// If already engaged, disengage first.
	if existing, ok := sc.clusters[clusterName]; ok {
		existing.cancel()
		delete(sc.clusters, clusterName)
	}
	sc.mu.Unlock()

	// Mark cluster as active.
	now := time.Now()
	if err := sc.config.Store.UpsertCluster(ctx, &store.ClusterModel{
		Name:      clusterName,
		Status:    "active",
		LastSeen:  now,
		EngagedAt: &now,
		TTL:       3600,
	}); err != nil {
		return fmt.Errorf("failed to upsert cluster %s: %w", clusterName, err)
	}

	// Create discovery client.
	restConfig := cl.GetConfig()
	dc, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create discovery client for %s: %w", clusterName, err)
	}

	// Run discovery to populate resource_types and get watchable resources.
	watchable, err := RunDiscovery(ctx, clusterName, dc, sc.config.Store, sc.config.Blacklist)
	if err != nil {
		return fmt.Errorf("discovery failed for cluster %s: %w", clusterName, err)
	}

	// Create dynamic client for informers.
	dynClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client for %s: %w", clusterName, err)
	}

	// Create a child context for this cluster's informers.
	clusterCtx, clusterCancel := context.WithCancel(ctx)

	sc.mu.Lock()
	sc.clusters[clusterName] = &clusterState{cancel: clusterCancel}
	sc.mu.Unlock()

	// Start informers for all watchable resources.
	go sc.runInformers(clusterCtx, clusterName, dynClient, watchable)

	// Watch CRDs for discovery refresh.
	go sc.watchCRDs(clusterCtx, clusterName, cl, dc)

	logger.Info("cluster engaged", "watchable", len(watchable))
	return nil
}

// Disengage is called when a cluster is removed. It stops all informers
// and marks the cluster as stale.
func (sc *SyncController) Disengage(ctx context.Context, clusterName string) error {
	logger := klog.FromContext(ctx).WithValues("cluster", clusterName)
	logger.Info("disengaging cluster")

	sc.mu.Lock()
	if state, ok := sc.clusters[clusterName]; ok {
		state.cancel()
		delete(sc.clusters, clusterName)
	}
	sc.mu.Unlock()

	// Mark cluster as stale.
	now := time.Now()
	if err := sc.config.Store.UpsertCluster(ctx, &store.ClusterModel{
		Name:     clusterName,
		Status:   "stale",
		LastSeen: now,
	}); err != nil {
		logger.Error(err, "failed to mark cluster as stale")
		return err
	}

	logger.Info("cluster disengaged")
	return nil
}

// runInformers starts a dynamic shared informer factory and adds event handlers
// for all watchable resources.
func (sc *SyncController) runInformers(ctx context.Context, clusterName string, dynClient dynamic.Interface, watchable []DiscoveredResource) {
	logger := klog.FromContext(ctx).WithValues("cluster", clusterName)

	factory := dynamicinformer.NewDynamicSharedInformerFactory(dynClient, sc.config.ResyncPeriod)

	for _, res := range watchable {
		informer := factory.ForResource(res.GVR).Informer()

		handler := &EventHandler{
			Store:       sc.config.Store,
			ClusterName: clusterName,
			GVR:         res.GVR,
			Kind:        res.Kind,
		}

		if _, err := informer.AddEventHandler(handler); err != nil {
			logger.Error(err, "failed to add event handler", "resource", res.GVR.String())
			continue
		}
	}

	factory.Start(ctx.Done())
	factory.WaitForCacheSync(ctx.Done())

	logger.Info("all informers synced", "count", len(watchable))

	// Block until context is cancelled.
	<-ctx.Done()
	logger.Info("informers stopped")
}

// watchCRDs watches CustomResourceDefinition changes and triggers discovery refresh.
func (sc *SyncController) watchCRDs(ctx context.Context, clusterName string, cl cluster.Cluster, dc discovery.DiscoveryInterface) {
	logger := klog.FromContext(ctx).WithValues("cluster", clusterName)

	// Watch CRDs using a dynamic informer.
	dynClient, err := dynamic.NewForConfig(cl.GetConfig())
	if err != nil {
		logger.Error(err, "failed to create dynamic client for CRD watch")
		return
	}

	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}

	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(dynClient, 5*time.Minute, "", nil)
	informer := factory.ForResource(crdGVR).Informer()

	// On CRD changes, re-run discovery. Debounce by only refreshing every 30s.
	var lastRefresh time.Time
	refreshHandler := &crdRefreshHandler{
		refresh: func() {
			now := time.Now()
			if now.Sub(lastRefresh) < 30*time.Second {
				return
			}
			lastRefresh = now
			logger.Info("CRD change detected, refreshing discovery")
			if _, err := RunDiscovery(ctx, clusterName, dc, sc.config.Store, sc.config.Blacklist); err != nil {
				logger.Error(err, "discovery refresh failed")
			}
		},
	}

	if _, err := informer.AddEventHandler(refreshHandler); err != nil {
		logger.Error(err, "failed to add CRD watch handler")
		return
	}

	factory.Start(ctx.Done())
	<-ctx.Done()
}

// crdRefreshHandler triggers a callback on any CRD change.
type crdRefreshHandler struct {
	refresh func()
}

func (h *crdRefreshHandler) OnAdd(_ interface{}, _ bool)        { h.refresh() }
func (h *crdRefreshHandler) OnUpdate(_, _ interface{})          { h.refresh() }
func (h *crdRefreshHandler) OnDelete(_ interface{})             { h.refresh() }
