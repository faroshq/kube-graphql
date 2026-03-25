package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildProjectionSQL_Empty(t *testing.T) {
	result, err := BuildProjectionSQL(nil, "sqlite", "obj")
	if err != nil {
		t.Fatal(err)
	}
	if result != "obj.object" {
		t.Fatalf("expected obj.object for nil projection, got %s", result)
	}
}

func TestBuildProjectionSQL_SQLite(t *testing.T) {
	spec := map[string]any{
		"metadata": map[string]any{
			"name":   true,
			"labels": true,
		},
		"spec": map[string]any{
			"replicas": true,
		},
	}
	raw, _ := json.Marshal(spec)

	result, err := BuildProjectionSQL(raw, "sqlite", "obj")
	if err != nil {
		t.Fatal(err)
	}

	// Should contain json_object and json_extract calls.
	if !strings.Contains(result, "json_object(") {
		t.Fatalf("expected json_object in SQLite projection, got %s", result)
	}
	if !strings.Contains(result, "json_extract(obj.object, '$.metadata.name')") {
		t.Fatalf("expected json_extract for metadata.name, got %s", result)
	}
	if !strings.Contains(result, "json_extract(obj.object, '$.spec.replicas')") {
		t.Fatalf("expected json_extract for spec.replicas, got %s", result)
	}
}

func TestBuildProjectionSQL_Postgres(t *testing.T) {
	spec := map[string]any{
		"metadata": map[string]any{
			"name": true,
		},
	}
	raw, _ := json.Marshal(spec)

	result, err := BuildProjectionSQL(raw, "postgres", "obj")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(result, "jsonb_build_object(") {
		t.Fatalf("expected jsonb_build_object in Postgres projection, got %s", result)
	}
	if !strings.Contains(result, "obj.object->'metadata'->'name'") {
		t.Fatalf("expected -> path for metadata.name, got %s", result)
	}
}

func TestBuildProjectionSQL_InvalidJSON(t *testing.T) {
	_, err := BuildProjectionSQL([]byte("not json"), "sqlite", "obj")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
