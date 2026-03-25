package engine

import (
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
