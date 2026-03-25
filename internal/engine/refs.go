package engine

import (
	"encoding/json"
	"fmt"
)

// RefPath defines a reference from a source kind's spec field to a target kind.
type RefPath struct {
	SourceKind  string // e.g. "Pod"
	SourceGroup string // e.g. "" (core)
	TargetKind  string // e.g. "Secret"
	TargetGroup string // e.g. "" (core)

	// PostgreSQL jsonb_path_query path.
	PGPath string
	// SQLite extraction: array path for json_each, then nested field extraction.
	// If empty, falls back to a simple json_extract.
	SQLiteArrayPath string // e.g. "$.spec.volumes" for json_each
	SQLiteFieldPath string // e.g. "$.secret.secretName" extracted from each element
}

// BuiltinRefPaths contains the built-in reference path registry for core Kubernetes types.
var BuiltinRefPaths = []RefPath{
	// Pod → Secret (volumes)
	{SourceKind: "Pod", TargetKind: "Secret",
		PGPath: "$.spec.volumes[*].secret.secretName",
		SQLiteArrayPath: "$.spec.volumes", SQLiteFieldPath: "$.secret.secretName"},
	// Pod → ConfigMap (volumes)
	{SourceKind: "Pod", TargetKind: "ConfigMap",
		PGPath: "$.spec.volumes[*].configMap.name",
		SQLiteArrayPath: "$.spec.volumes", SQLiteFieldPath: "$.configMap.name"},
	// Pod → PersistentVolumeClaim (volumes)
	{SourceKind: "Pod", TargetKind: "PersistentVolumeClaim",
		PGPath: "$.spec.volumes[*].persistentVolumeClaim.claimName",
		SQLiteArrayPath: "$.spec.volumes", SQLiteFieldPath: "$.persistentVolumeClaim.claimName"},
	// Pod → ServiceAccount
	{SourceKind: "Pod", TargetKind: "ServiceAccount",
		PGPath: "$.spec.serviceAccountName",
		SQLiteArrayPath: "", SQLiteFieldPath: "$.spec.serviceAccountName"},
	// Pod → Secret (imagePullSecrets)
	{SourceKind: "Pod", TargetKind: "Secret",
		PGPath: "$.spec.imagePullSecrets[*].name",
		SQLiteArrayPath: "$.spec.imagePullSecrets", SQLiteFieldPath: "$.name"},

	// Pod → Secret (env secretKeyRef) — nested arrays
	{SourceKind: "Pod", TargetKind: "Secret",
		PGPath:          "$.spec.containers[*].env[*].valueFrom.secretKeyRef.name",
		SQLiteArrayPath: "$.spec.containers", SQLiteFieldPath: "NESTED_ENV_SECRET"},
	// Pod → ConfigMap (env configMapKeyRef) — nested arrays
	{SourceKind: "Pod", TargetKind: "ConfigMap",
		PGPath:          "$.spec.containers[*].env[*].valueFrom.configMapKeyRef.name",
		SQLiteArrayPath: "$.spec.containers", SQLiteFieldPath: "NESTED_ENV_CONFIGMAP"},
	// Pod → Secret (envFrom secretRef)
	{SourceKind: "Pod", TargetKind: "Secret",
		PGPath:          "$.spec.containers[*].envFrom[*].secretRef.name",
		SQLiteArrayPath: "$.spec.containers", SQLiteFieldPath: "NESTED_ENVFROM_SECRET"},
	// Pod → ConfigMap (envFrom configMapRef)
	{SourceKind: "Pod", TargetKind: "ConfigMap",
		PGPath:          "$.spec.containers[*].envFrom[*].configMapRef.name",
		SQLiteArrayPath: "$.spec.containers", SQLiteFieldPath: "NESTED_ENVFROM_CONFIGMAP"},

	// Ingress → Service
	{SourceKind: "Ingress", SourceGroup: "networking.k8s.io", TargetKind: "Service",
		PGPath:          "$.spec.rules[*].http.paths[*].backend.service.name",
		SQLiteArrayPath: "$.spec.rules", SQLiteFieldPath: "NESTED_INGRESS_SERVICE"},
	// Ingress → Secret (TLS)
	{SourceKind: "Ingress", SourceGroup: "networking.k8s.io", TargetKind: "Secret",
		PGPath: "$.spec.tls[*].secretName",
		SQLiteArrayPath: "$.spec.tls", SQLiteFieldPath: "$.secretName"},

	// PersistentVolumeClaim → StorageClass
	{SourceKind: "PersistentVolumeClaim", TargetKind: "StorageClass", TargetGroup: "storage.k8s.io",
		PGPath: "$.spec.storageClassName",
		SQLiteArrayPath: "", SQLiteFieldPath: "$.spec.storageClassName"},
	// PersistentVolumeClaim → PersistentVolume
	{SourceKind: "PersistentVolumeClaim", TargetKind: "PersistentVolume",
		PGPath: "$.spec.volumeName",
		SQLiteArrayPath: "", SQLiteFieldPath: "$.spec.volumeName"},

	// RoleBinding → ClusterRole/Role
	{SourceKind: "RoleBinding", SourceGroup: "rbac.authorization.k8s.io",
		TargetKind: "ClusterRole", TargetGroup: "rbac.authorization.k8s.io",
		PGPath: "$.roleRef.name",
		SQLiteArrayPath: "", SQLiteFieldPath: "$.roleRef.name"},
	{SourceKind: "ClusterRoleBinding", SourceGroup: "rbac.authorization.k8s.io",
		TargetKind: "ClusterRole", TargetGroup: "rbac.authorization.k8s.io",
		PGPath: "$.roleRef.name",
		SQLiteArrayPath: "", SQLiteFieldPath: "$.roleRef.name"},
}

// RefPathRegistry manages reference path lookups.
type RefPathRegistry struct {
	paths map[string][]RefPath // key: "SourceGroup/SourceKind"
}

// NewRefPathRegistry creates a registry from built-in paths.
func NewRefPathRegistry() *RefPathRegistry {
	r := &RefPathRegistry{paths: make(map[string][]RefPath)}
	for _, p := range BuiltinRefPaths {
		key := p.SourceGroup + "/" + p.SourceKind
		r.paths[key] = append(r.paths[key], p)
	}
	return r
}

// Register adds custom ref paths to the registry.
func (r *RefPathRegistry) Register(paths ...RefPath) {
	for _, p := range paths {
		key := p.SourceGroup + "/" + p.SourceKind
		r.paths[key] = append(r.paths[key], p)
	}
}

// ParseCRDRefAnnotation parses a kuery.io/refs annotation value into RefPath entries.
// The annotation is a JSON array: [{"path": "$.spec.secretRef.name", "targetKind": "Secret", "targetGroup": ""}]
func ParseCRDRefAnnotation(sourceKind, sourceGroup, annotationValue string) ([]RefPath, error) {
	var entries []struct {
		Path        string `json:"path"`
		TargetKind  string `json:"targetKind"`
		TargetGroup string `json:"targetGroup"`
	}
	if err := json.Unmarshal([]byte(annotationValue), &entries); err != nil {
		return nil, fmt.Errorf("parsing kuery.io/refs annotation: %w", err)
	}

	var result []RefPath
	for _, e := range entries {
		rp := RefPath{
			SourceKind:  sourceKind,
			SourceGroup: sourceGroup,
			TargetKind:  e.TargetKind,
			TargetGroup: e.TargetGroup,
			PGPath:      e.Path,
			// For custom CRD refs, assume simple scalar paths (no array traversal).
			SQLiteArrayPath: "",
			SQLiteFieldPath: e.Path,
		}
		result = append(result, rp)
	}
	return result, nil
}

// Lookup returns all ref paths for a given source kind.
func (r *RefPathRegistry) Lookup(sourceGroup, sourceKind string) []RefPath {
	return r.paths[sourceGroup+"/"+sourceKind]
}

// LookupForTarget returns ref paths for a source kind that target a specific kind.
func (r *RefPathRegistry) LookupForTarget(sourceGroup, sourceKind, targetGroup, targetKind string) []RefPath {
	all := r.Lookup(sourceGroup, sourceKind)
	var result []RefPath
	for _, p := range all {
		if p.TargetKind == targetKind && (targetGroup == "" || p.TargetGroup == targetGroup) {
			result = append(result, p)
		}
	}
	return result
}

// BuildRefNameSubquery generates a SQL subquery that extracts referenced names
// from a source object. Returns the SQL expression and whether it's valid.
func (rp *RefPath) BuildRefNameSubquery(sourceAlias, dialect string) string {
	switch dialect {
	case "postgres":
		return fmt.Sprintf(
			"(SELECT v#>>'{}' FROM jsonb_path_query(%s.object, '%s') v)",
			sourceAlias, rp.PGPath)
	default: // sqlite
		return rp.buildSQLiteRefSubquery(sourceAlias)
	}
}

func (rp *RefPath) buildSQLiteRefSubquery(sourceAlias string) string {
	// Simple scalar path (no array traversal).
	if rp.SQLiteArrayPath == "" {
		return fmt.Sprintf(
			"(SELECT json_extract(%s.object, '%s'))",
			sourceAlias, rp.SQLiteFieldPath)
	}

	// Handle nested array patterns with special markers.
	switch rp.SQLiteFieldPath {
	case "NESTED_ENV_SECRET":
		return fmt.Sprintf(
			"(SELECT json_extract(env.value, '$.valueFrom.secretKeyRef.name') "+
				"FROM json_each(json_extract(%s.object, '$.spec.containers')) cont, "+
				"json_each(json_extract(cont.value, '$.env')) env "+
				"WHERE json_extract(env.value, '$.valueFrom.secretKeyRef.name') IS NOT NULL)",
			sourceAlias)
	case "NESTED_ENV_CONFIGMAP":
		return fmt.Sprintf(
			"(SELECT json_extract(env.value, '$.valueFrom.configMapKeyRef.name') "+
				"FROM json_each(json_extract(%s.object, '$.spec.containers')) cont, "+
				"json_each(json_extract(cont.value, '$.env')) env "+
				"WHERE json_extract(env.value, '$.valueFrom.configMapKeyRef.name') IS NOT NULL)",
			sourceAlias)
	case "NESTED_ENVFROM_SECRET":
		return fmt.Sprintf(
			"(SELECT json_extract(ef.value, '$.secretRef.name') "+
				"FROM json_each(json_extract(%s.object, '$.spec.containers')) cont, "+
				"json_each(json_extract(cont.value, '$.envFrom')) ef "+
				"WHERE json_extract(ef.value, '$.secretRef.name') IS NOT NULL)",
			sourceAlias)
	case "NESTED_ENVFROM_CONFIGMAP":
		return fmt.Sprintf(
			"(SELECT json_extract(ef.value, '$.configMapRef.name') "+
				"FROM json_each(json_extract(%s.object, '$.spec.containers')) cont, "+
				"json_each(json_extract(cont.value, '$.envFrom')) ef "+
				"WHERE json_extract(ef.value, '$.configMapRef.name') IS NOT NULL)",
			sourceAlias)
	case "NESTED_INGRESS_SERVICE":
		return fmt.Sprintf(
			"(SELECT json_extract(p.value, '$.backend.service.name') "+
				"FROM json_each(json_extract(%s.object, '$.spec.rules')) r, "+
				"json_each(json_extract(r.value, '$.http.paths')) p "+
				"WHERE json_extract(p.value, '$.backend.service.name') IS NOT NULL)",
			sourceAlias)
	default:
		// Standard single-level array with field extraction.
		return fmt.Sprintf(
			"(SELECT json_extract(je.value, '%s') "+
				"FROM json_each(json_extract(%s.object, '%s')) je "+
				"WHERE json_extract(je.value, '%s') IS NOT NULL)",
			rp.SQLiteFieldPath, sourceAlias, rp.SQLiteArrayPath, rp.SQLiteFieldPath)
	}
}
