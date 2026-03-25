package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// +genclient
// +genclient:nonNamespaced
// +genclient:onlyVerbs=create
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// Query is a POST-only virtual resource for executing rich, nested queries
// across multiple Kubernetes clusters. The response is returned inline in Status.
type Query struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   QuerySpec   `json:"spec,omitempty"`
	Status QueryStatus `json:"status,omitempty"`
}

// QuerySpec defines the desired query parameters.
type QuerySpec struct {
	// Cluster filters which clusters to query.
	// +optional
	Cluster *ClusterFilter `json:"cluster,omitempty"`

	// Filter defines object-level filters.
	// +optional
	Filter *QueryFilter `json:"filter,omitempty"`

	// Limit is the maximum number of root objects to return. Default: 100.
	// +optional
	Limit int32 `json:"limit,omitempty"`

	// Page specifies pagination parameters.
	// +optional
	Page *PageSpec `json:"page,omitempty"`

	// Order specifies the sort order for root objects.
	// Default: name ASC. Tiebreaker: namespace ASC, name ASC.
	// +optional
	Order []OrderSpec `json:"order,omitempty"`

	// Count requests total count of matching objects (expensive).
	// +optional
	Count bool `json:"count,omitempty"`

	// Cursor requests a cursor in the response for pagination.
	// +optional
	Cursor bool `json:"cursor,omitempty"`

	// MaxDepth limits the depth of transitive relation traversal. Default: 10, hard cap: 20.
	// +optional
	MaxDepth int32 `json:"maxDepth,omitempty"`

	// Objects defines the response shape (projection, relations).
	// +optional
	Objects *ObjectsSpec `json:"objects,omitempty"`
}

// ClusterFilter selects clusters to include in the query.
type ClusterFilter struct {
	// Name selects a specific cluster. Empty means all clusters.
	// +optional
	Name string `json:"name,omitempty"`

	// Labels filters clusters by label selector.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
}

// QueryFilter defines the filter criteria for objects.
type QueryFilter struct {
	// Objects is a list of object filters. Entries are OR-ed;
	// criteria within a single entry are AND-ed.
	Objects []ObjectFilter `json:"objects,omitempty"`
}

// ObjectFilter defines criteria for selecting objects.
type ObjectFilter struct {
	// GroupKind filters by API group and kind.
	// +optional
	GroupKind *GroupKindFilter `json:"groupKind,omitempty"`

	// Name filters by exact object name.
	// +optional
	Name string `json:"name,omitempty"`

	// Namespace filters by namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Labels filters by label selector (matchLabels style).
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// Conditions filters by status conditions.
	// +optional
	Conditions []ConditionFilter `json:"conditions,omitempty"`

	// CreationTimestamp filters by creation time range.
	// +optional
	CreationTimestamp *TimestampFilter `json:"creationTimestamp,omitempty"`

	// ID filters by opaque object ID from a previous query.
	// +optional
	ID string `json:"id,omitempty"`

	// JSONPath is a JSONPath boolean filter expression (last resort).
	// +optional
	JSONPath string `json:"jsonpath,omitempty"`

	// Categories filters by resource categories (resolved via resource_types).
	// +optional
	Categories []string `json:"categories,omitempty"`
}

// GroupKindFilter selects objects by API group and kind.
type GroupKindFilter struct {
	// APIGroup is the API group (empty string for core group).
	// +optional
	APIGroup string `json:"apiGroup,omitempty"`

	// Kind is the resource kind. Also resolves resource names and short names.
	Kind string `json:"kind"`
}

// ConditionFilter matches status conditions on objects.
type ConditionFilter struct {
	// Type is the condition type (e.g., "Available", "Ready").
	Type string `json:"type"`

	// Status is the condition status (e.g., "True", "False", "Unknown").
	// +optional
	Status string `json:"status,omitempty"`

	// Reason filters by the condition reason.
	// +optional
	Reason string `json:"reason,omitempty"`
}

// TimestampFilter filters by creation timestamp range.
type TimestampFilter struct {
	// After selects objects created after this time.
	// +optional
	After *metav1.Time `json:"after,omitempty"`

	// Before selects objects created before this time.
	// +optional
	Before *metav1.Time `json:"before,omitempty"`
}

// PageSpec defines pagination parameters.
type PageSpec struct {
	// First is the offset (number of objects to skip).
	// +optional
	First int32 `json:"first,omitempty"`

	// Cursor is an opaque cursor from a previous response.
	// +optional
	Cursor string `json:"cursor,omitempty"`
}

// OrderSpec defines a single sort criterion.
type OrderSpec struct {
	// Field is the field to sort by.
	// Allowed: name, namespace, kind, apiGroup, cluster, creationTimestamp.
	Field string `json:"field"`

	// Direction is Asc or Desc.
	// +optional
	Direction SortDirection `json:"direction,omitempty"`
}

// SortDirection is the sort direction.
// +enum
type SortDirection string

const (
	SortAsc  SortDirection = "Asc"
	SortDesc SortDirection = "Desc"
)

// ObjectsSpec defines the response shape for returned objects.
type ObjectsSpec struct {
	// ID includes the opaque object ID in the response.
	// +optional
	ID bool `json:"id,omitempty"`

	// Cluster includes the cluster name in the response.
	// +optional
	Cluster bool `json:"cluster,omitempty"`

	// MutablePath includes the REST path for direct mutation.
	// +optional
	MutablePath bool `json:"mutablePath,omitempty"`

	// Object defines sparse projection — only return specified fields.
	// Keys are field names. Value `true` includes the field and all descendants.
	// Nested maps include only specified sub-fields.
	// Implemented as jsonb_build_object in SQL.
	// +optional
	Object *runtime.RawExtension `json:"object,omitempty"`

	// Relations defines nested queries following object relationships.
	// Keys are relation type names (e.g., "descendants", "owners", "references").
	// +optional
	Relations map[string]RelationSpec `json:"relations,omitempty"`
}

// RelationSpec defines a nested relation query.
type RelationSpec struct {
	// Limit is the maximum number of related objects per parent.
	// +optional
	Limit int32 `json:"limit,omitempty"`

	// Filters restricts which related objects are returned.
	// +optional
	Filters []ObjectFilter `json:"filters,omitempty"`

	// Objects defines the response shape for related objects (recursive).
	// +optional
	Objects *ObjectsSpec `json:"objects,omitempty"`
}

// QueryStatus is the response returned inline in the Query object.
type QueryStatus struct {
	// Objects is the list of matched objects with their projected fields and relations.
	Objects []ObjectResult `json:"objects,omitempty"`

	// Cursor contains pagination cursor information.
	// +optional
	Cursor *CursorResult `json:"cursor,omitempty"`

	// Count is the total number of matching root objects (only if spec.count=true).
	// +optional
	Count *int64 `json:"count,omitempty"`

	// Incomplete indicates the response was truncated due to limits.
	Incomplete bool `json:"incomplete,omitempty"`

	// Warnings contains non-fatal issues encountered during query execution.
	// +optional
	Warnings []string `json:"warnings,omitempty"`
}

// ObjectResult is a single object in the query response.
type ObjectResult struct {
	// ID is the opaque object identifier.
	// +optional
	ID string `json:"id,omitempty"`

	// Cluster is the cluster this object belongs to.
	// +optional
	Cluster string `json:"cluster,omitempty"`

	// MutablePath is the REST path for direct mutation of this object.
	// +optional
	MutablePath string `json:"mutablePath,omitempty"`

	// Object contains the projected fields of the Kubernetes object.
	// +optional
	Object *runtime.RawExtension `json:"object,omitempty"`

	// Relations contains nested related objects, keyed by relation type.
	// +optional
	Relations map[string][]ObjectResult `json:"relations,omitempty"`
}

// CursorResult contains pagination cursor information.
type CursorResult struct {
	// Next is the opaque cursor for the next page.
	Next string `json:"next,omitempty"`

	// Page is the current page number.
	Page int32 `json:"page,omitempty"`

	// PageSize is the number of objects per page.
	PageSize int32 `json:"pageSize,omitempty"`
}
