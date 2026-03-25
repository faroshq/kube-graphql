package engine

import (
	"fmt"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
)

const (
	DefaultLimit         = 100
	MaxLimit             = 10000
	DefaultMaxDepth      = 10
	HardMaxDepth         = 20
	MaxTotalRows         = 10000
	MaxRelationBlocks    = 10
	DefaultQueryTimeout  = 30 // seconds
)

// ValidSortFields are the fields allowed in order specs.
var ValidSortFields = map[string]string{
	"name":              "obj.name",
	"namespace":         "obj.namespace",
	"kind":              "obj.kind",
	"apiGroup":          "obj.api_group",
	"cluster":           "obj.cluster",
	"creationTimestamp":  "obj.creation_ts",
}

// Validate checks a QuerySpec for correctness and applies defaults.
func Validate(spec *v1alpha1.QuerySpec) error {
	// Limit.
	if spec.Limit <= 0 {
		spec.Limit = DefaultLimit
	}
	if spec.Limit > MaxLimit {
		return fmt.Errorf("limit %d exceeds maximum %d", spec.Limit, MaxLimit)
	}

	// MaxDepth.
	if spec.MaxDepth <= 0 {
		spec.MaxDepth = DefaultMaxDepth
	}
	if spec.MaxDepth > HardMaxDepth {
		return fmt.Errorf("maxDepth %d exceeds hard cap %d", spec.MaxDepth, HardMaxDepth)
	}

	// Order fields.
	for _, o := range spec.Order {
		if _, ok := ValidSortFields[o.Field]; !ok {
			return fmt.Errorf("unsupported sort field: %q", o.Field)
		}
		if o.Direction != "" && o.Direction != v1alpha1.SortAsc && o.Direction != v1alpha1.SortDesc {
			return fmt.Errorf("invalid sort direction: %q", o.Direction)
		}
	}

	// Page validation.
	if spec.Page != nil {
		if spec.Page.First < 0 {
			return fmt.Errorf("page.first must be >= 0")
		}
	}

	// Relation blocks count.
	if spec.Objects != nil {
		count := countRelationBlocks(spec.Objects)
		if count > MaxRelationBlocks {
			return fmt.Errorf("query has %d relation blocks, exceeding maximum %d", count, MaxRelationBlocks)
		}
	}

	return nil
}

// countRelationBlocks recursively counts the total number of relation blocks in the spec.
func countRelationBlocks(objSpec *v1alpha1.ObjectsSpec) int {
	if objSpec == nil || len(objSpec.Relations) == 0 {
		return 0
	}
	count := len(objSpec.Relations)
	for _, rel := range objSpec.Relations {
		count += countRelationBlocks(rel.Objects)
	}
	return count
}
