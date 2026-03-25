package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/faroshq/kuery/internal/store"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/klog/v2"
)

// DiscoveredResource holds a discovered API resource with its GVR.
type DiscoveredResource struct {
	GVR        schema.GroupVersionResource
	Kind       string
	Singular   string
	ShortNames []string
	Categories []string
	Namespaced bool
}

// RunDiscovery discovers all API resources for a cluster and populates the
// resource_types table. Returns the list of watchable resources (those with list+watch verbs).
func RunDiscovery(ctx context.Context, clusterName string, dc discovery.DiscoveryInterface, s store.Store, bl *Blacklist) ([]DiscoveredResource, error) {
	logger := klog.FromContext(ctx)

	// Get all server resources (groups + resources).
	_, apiResourceLists, err := dc.ServerGroupsAndResources()
	if err != nil {
		// ServerGroupsAndResources may return partial results with an error.
		// Log the error but continue with what we got.
		if len(apiResourceLists) == 0 {
			return nil, fmt.Errorf("discovery failed for cluster %s: %w", clusterName, err)
		}
		logger.V(2).Info("partial discovery error", "cluster", clusterName, "error", err)
	}

	// Clear existing resource types for this cluster before repopulating.
	if err := s.DeleteResourceTypesForCluster(ctx, clusterName); err != nil {
		return nil, fmt.Errorf("failed to clear resource_types for cluster %s: %w", clusterName, err)
	}

	var watchable []DiscoveredResource

	for _, list := range apiResourceLists {
		if list == nil {
			continue
		}
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			logger.V(2).Info("skipping unparseable group version", "groupVersion", list.GroupVersion, "error", err)
			continue
		}

		for _, ar := range list.APIResources {
			// Skip subresources (e.g., pods/status, deployments/scale).
			if strings.Contains(ar.Name, "/") {
				continue
			}

			gr := schema.GroupResource{Group: gv.Group, Resource: ar.Name}
			if bl.IsBlacklisted(gr) {
				logger.V(3).Info("skipping blacklisted resource", "cluster", clusterName, "resource", ar.Name, "group", gv.Group)
				continue
			}

			// Persist to resource_types table.
			shortNamesJSON, _ := json.Marshal(ar.ShortNames)
			categoriesJSON, _ := json.Marshal(ar.Categories)

			// Collect subresource names for this resource.
			var subresources []string
			for _, sub := range list.APIResources {
				if strings.HasPrefix(sub.Name, ar.Name+"/") {
					subresources = append(subresources, strings.TrimPrefix(sub.Name, ar.Name+"/"))
				}
			}
			subresourcesJSON, _ := json.Marshal(subresources)

			rt := &store.ResourceTypeModel{
				Cluster:      clusterName,
				APIGroup:     gv.Group,
				APIVersion:   gv.Version,
				Kind:         ar.Kind,
				Singular:     ar.SingularName,
				Resource:     ar.Name,
				ShortNames:   shortNamesJSON,
				Categories:   categoriesJSON,
				Namespaced:   ar.Namespaced,
				Subresources: subresourcesJSON,
			}
			if err := s.UpsertResourceType(ctx, rt); err != nil {
				logger.Error(err, "failed to upsert resource type", "cluster", clusterName, "resource", ar.Name)
				continue
			}

			// Check if the resource supports list and watch verbs.
			if hasVerbs(ar.Verbs, "list", "watch") {
				watchable = append(watchable, DiscoveredResource{
					GVR:        schema.GroupVersionResource{Group: gv.Group, Version: gv.Version, Resource: ar.Name},
					Kind:       ar.Kind,
					Singular:   ar.SingularName,
					ShortNames: ar.ShortNames,
					Categories: ar.Categories,
					Namespaced: ar.Namespaced,
				})
			}
		}
	}

	logger.Info("discovery complete", "cluster", clusterName, "watchable", len(watchable))
	return watchable, nil
}

// hasVerbs checks that all required verbs are present in the list.
func hasVerbs(verbs metav1.Verbs, required ...string) bool {
	set := make(map[string]bool, len(verbs))
	for _, v := range verbs {
		set[v] = true
	}
	for _, r := range required {
		if !set[r] {
			return false
		}
	}
	return true
}
