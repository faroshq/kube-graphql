package sync

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/faroshq/kuery/internal/store"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"
)

// kcpAPIExportGVR is the GVR for kcp APIExport resources.
var kcpAPIExportGVR = schema.GroupVersionResource{
	Group:    "apis.kcp.io",
	Version:  "v1alpha1",
	Resource: "apiexports",
}

// RunKCPDiscovery performs kcp-specific discovery to populate the identity column
// in resource_types for resources provided via kcp APIExports.
// This is called after standard discovery to enrich the data.
func RunKCPDiscovery(ctx context.Context, clusterName string, dynClient dynamic.Interface, s store.Store) error {
	logger := klog.FromContext(ctx).WithValues("cluster", clusterName)

	// List APIExports to find identity information.
	exports, err := dynClient.Resource(kcpAPIExportGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		logger.V(2).Info("kcp APIExports not available, skipping kcp discovery", "error", err)
		return nil // Not a kcp cluster, skip silently.
	}

	for _, export := range exports.Items {
		identity, found, err := unstructured.NestedString(export.Object, "status", "identityHash")
		if err != nil || !found || identity == "" {
			continue
		}

		// Get the schemas provided by this APIExport.
		schemas, found, err := unstructured.NestedSlice(export.Object, "spec", "latestResourceSchemas")
		if err != nil || !found {
			continue
		}

		for _, s := range schemas {
			schemaName, ok := s.(string)
			if !ok {
				continue
			}
			// Schema names follow the pattern: <version>.<resource>.<group>
			// e.g., "v1.widgets.example.io"
			parts := strings.SplitN(schemaName, ".", 3)
			if len(parts) < 2 {
				continue
			}
			resource := parts[1]
			group := ""
			if len(parts) >= 3 {
				group = parts[2]
			}

			// Update the resource_types entry with the identity.
			if err := updateResourceTypeIdentity(ctx, s, clusterName, group, resource, identity); err != nil {
				logger.V(2).Info("failed to update identity", "resource", resource, "group", group, "error", err)
			}
		}

		logger.V(2).Info("processed APIExport", "export", export.GetName(), "identity", identity)
	}

	return nil
}

// updateResourceTypeIdentity updates the identity field for a resource type.
func updateResourceTypeIdentity(ctx context.Context, s interface{}, clusterName, apiGroup, resource, identity string) error {
	st, ok := s.(store.Store)
	if !ok {
		return nil
	}

	// Read existing resource type.
	db := st.RawDB()
	return db.WithContext(ctx).
		Model(&store.ResourceTypeModel{}).
		Where("cluster = ? AND api_group = ? AND resource = ?", clusterName, apiGroup, resource).
		Update("identity", identity).Error
}

// DiscoveredResourceWithIdentity extends DiscoveredResource with kcp identity.
type DiscoveredResourceWithIdentity struct {
	DiscoveredResource
	Identity string
}

// EnrichWithKCPIdentity takes standard discovered resources and adds identity
// information from the resource_types table (populated by RunKCPDiscovery).
func EnrichWithKCPIdentity(ctx context.Context, s store.Store, clusterName string, resources []DiscoveredResource) []DiscoveredResourceWithIdentity {
	result := make([]DiscoveredResourceWithIdentity, len(resources))
	for i, res := range resources {
		result[i] = DiscoveredResourceWithIdentity{
			DiscoveredResource: res,
		}

		// Look up identity from store.
		var rt store.ResourceTypeModel
		err := s.RawDB().WithContext(ctx).
			Where("cluster = ? AND api_group = ? AND resource = ? AND identity != ''",
				clusterName, res.GVR.Group, res.GVR.Resource).
			First(&rt).Error
		if err == nil {
			result[i].Identity = rt.Identity
		}
	}
	return result
}

// GetIdentityForResource returns the kcp identity for a specific resource, or empty.
func GetIdentityForResource(ctx context.Context, s store.Store, clusterName, apiGroup, resource string) string {
	var rt store.ResourceTypeModel
	err := s.RawDB().WithContext(ctx).
		Where("cluster = ? AND api_group = ? AND resource = ? AND identity != ''",
			clusterName, apiGroup, resource).
		First(&rt).Error
	if err != nil {
		return ""
	}
	return rt.Identity
}

// ParseKCPRefsAnnotation parses kuery.io/refs from CRD annotations.
// This is used during CRD discovery to find custom reference paths.
func ParseKCPRefsAnnotation(annotations map[string]string) ([]byte, bool) {
	val, ok := annotations["kuery.io/refs"]
	if !ok || val == "" {
		return nil, false
	}
	// Validate it's valid JSON.
	var parsed json.RawMessage
	if err := json.Unmarshal([]byte(val), &parsed); err != nil {
		return nil, false
	}
	return parsed, true
}
