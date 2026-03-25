package engine

import "testing"

func TestParseCRDRefAnnotation(t *testing.T) {
	annotation := `[{"path":"$.spec.secretRef.name","targetKind":"Secret"},{"path":"$.spec.configRef.name","targetKind":"ConfigMap"}]`
	paths, err := ParseCRDRefAnnotation("MyResource", "example.com", annotation)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("expected 2 paths, got %d", len(paths))
	}
	if paths[0].SourceKind != "MyResource" {
		t.Fatalf("expected SourceKind=MyResource, got %s", paths[0].SourceKind)
	}
	if paths[0].TargetKind != "Secret" {
		t.Fatalf("expected TargetKind=Secret, got %s", paths[0].TargetKind)
	}
	if paths[1].TargetKind != "ConfigMap" {
		t.Fatalf("expected TargetKind=ConfigMap, got %s", paths[1].TargetKind)
	}
}

func TestParseCRDRefAnnotation_InvalidJSON(t *testing.T) {
	_, err := ParseCRDRefAnnotation("X", "", "not json")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestRefPathRegistry_Register(t *testing.T) {
	r := NewRefPathRegistry()
	custom := RefPath{
		SourceKind: "MyResource", SourceGroup: "example.com",
		TargetKind: "Secret",
		PGPath:     "$.spec.secretRef.name",
	}
	r.Register(custom)

	paths := r.Lookup("example.com", "MyResource")
	if len(paths) != 1 {
		t.Fatalf("expected 1 custom path, got %d", len(paths))
	}
	if paths[0].TargetKind != "Secret" {
		t.Fatalf("unexpected target kind: %s", paths[0].TargetKind)
	}
}
