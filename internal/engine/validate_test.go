package engine

import (
	"fmt"
	"testing"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
)

func TestValidate_Defaults(t *testing.T) {
	spec := &v1alpha1.QuerySpec{}
	if err := Validate(spec); err != nil {
		t.Fatal(err)
	}
	if spec.Limit != DefaultLimit {
		t.Fatalf("expected default limit %d, got %d", DefaultLimit, spec.Limit)
	}
	if spec.MaxDepth != DefaultMaxDepth {
		t.Fatalf("expected default maxDepth %d, got %d", DefaultMaxDepth, spec.MaxDepth)
	}
}

func TestValidate_LimitExceedsMax(t *testing.T) {
	spec := &v1alpha1.QuerySpec{Limit: MaxLimit + 1}
	if err := Validate(spec); err == nil {
		t.Fatal("expected error for limit exceeding max")
	}
}

func TestValidate_MaxDepthExceedsHardCap(t *testing.T) {
	spec := &v1alpha1.QuerySpec{MaxDepth: HardMaxDepth + 1}
	if err := Validate(spec); err == nil {
		t.Fatal("expected error for maxDepth exceeding hard cap")
	}
}

func TestValidate_InvalidSortField(t *testing.T) {
	spec := &v1alpha1.QuerySpec{
		Order: []v1alpha1.OrderSpec{
			{Field: "invalid"},
		},
	}
	if err := Validate(spec); err == nil {
		t.Fatal("expected error for invalid sort field")
	}
}

func TestValidate_InvalidSortDirection(t *testing.T) {
	spec := &v1alpha1.QuerySpec{
		Order: []v1alpha1.OrderSpec{
			{Field: "name", Direction: "BadDirection"},
		},
	}
	if err := Validate(spec); err == nil {
		t.Fatal("expected error for invalid sort direction")
	}
}

func TestValidate_ValidOrder(t *testing.T) {
	spec := &v1alpha1.QuerySpec{
		Order: []v1alpha1.OrderSpec{
			{Field: "name", Direction: v1alpha1.SortAsc},
			{Field: "creationTimestamp", Direction: v1alpha1.SortDesc},
		},
	}
	if err := Validate(spec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_NegativePageFirst(t *testing.T) {
	spec := &v1alpha1.QuerySpec{
		Page: &v1alpha1.PageSpec{First: -1},
	}
	if err := Validate(spec); err == nil {
		t.Fatal("expected error for negative page.first")
	}
}

func TestValidate_MaxRelationBlocksExceeded(t *testing.T) {
	// Build a spec with 11 relation blocks.
	relations := make(map[string]v1alpha1.RelationSpec)
	for i := range 11 {
		relations[fmt.Sprintf("rel%d", i)] = v1alpha1.RelationSpec{}
	}
	spec := &v1alpha1.QuerySpec{
		Objects: &v1alpha1.ObjectsSpec{
			Relations: relations,
		},
	}
	if err := Validate(spec); err == nil {
		t.Fatal("expected error for exceeding max relation blocks")
	}
}

func TestValidate_MaxRelationBlocksNested(t *testing.T) {
	// 3 top-level + 3 nested = 6, within limit.
	inner := make(map[string]v1alpha1.RelationSpec)
	for i := range 3 {
		inner[fmt.Sprintf("inner%d", i)] = v1alpha1.RelationSpec{}
	}
	outer := make(map[string]v1alpha1.RelationSpec)
	for i := range 3 {
		outer[fmt.Sprintf("outer%d", i)] = v1alpha1.RelationSpec{
			Objects: &v1alpha1.ObjectsSpec{Relations: inner},
		}
	}
	spec := &v1alpha1.QuerySpec{
		Objects: &v1alpha1.ObjectsSpec{Relations: outer},
	}
	// 3 outer + 3*3 inner = 12 > 10. Should fail.
	if err := Validate(spec); err == nil {
		t.Fatal("expected error for nested relation blocks exceeding max")
	}
}

func TestValidate_RelationBlocksWithinLimit(t *testing.T) {
	relations := make(map[string]v1alpha1.RelationSpec)
	for i := range 5 {
		relations[fmt.Sprintf("rel%d", i)] = v1alpha1.RelationSpec{}
	}
	spec := &v1alpha1.QuerySpec{
		Objects: &v1alpha1.ObjectsSpec{Relations: relations},
	}
	if err := Validate(spec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
