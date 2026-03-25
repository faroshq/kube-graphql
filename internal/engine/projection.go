package engine

import (
	"encoding/json"
	"fmt"
	"strings"
)

// BuildProjectionSQL generates a SQL expression for sparse field projection.
// It converts a projection spec (nested map of field names to true/nested maps)
// into a json_object() call (SQLite) or jsonb_build_object() call (PostgreSQL).
//
// Example input: {"metadata": {"name": true, "labels": true}, "spec": {"replicas": true}}
// SQLite output:  json_object('metadata', json_object('name', json_extract(obj.object, '$.metadata.name'), ...), ...)
// Postgres output: jsonb_build_object('metadata', jsonb_build_object('name', obj.object->'metadata'->'name', ...), ...)
func BuildProjectionSQL(projectionRaw json.RawMessage, dialect string, alias string) (string, error) {
	if len(projectionRaw) == 0 {
		return alias + ".object", nil
	}

	var spec any
	if err := json.Unmarshal(projectionRaw, &spec); err != nil {
		return "", fmt.Errorf("invalid projection spec: %w", err)
	}

	switch dialect {
	case "sqlite":
		return buildProjectionSQLite(spec, alias, ""), nil
	case "postgres":
		return buildProjectionPostgres(spec, alias, ""), nil
	default:
		return buildProjectionSQLite(spec, alias, ""), nil
	}
}

// buildProjectionSQLite generates json_object(...) with json_extract for SQLite.
func buildProjectionSQLite(spec any, alias string, path string) string {
	switch v := spec.(type) {
	case bool:
		if v {
			return fmt.Sprintf("json_extract(%s.object, '$.%s')", alias, path)
		}
		return "NULL"
	case map[string]any:
		var parts []string
		for key, val := range v {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			expr := buildProjectionSQLite(val, alias, childPath)
			parts = append(parts, fmt.Sprintf("'%s', %s", key, expr))
		}
		if len(parts) == 0 {
			return "NULL"
		}
		return fmt.Sprintf("json_object(%s)", strings.Join(parts, ", "))
	default:
		// Treat anything truthy as "include this field".
		return fmt.Sprintf("json_extract(%s.object, '$.%s')", alias, path)
	}
}

// buildProjectionPostgres generates jsonb_build_object(...) with -> operators for PostgreSQL.
func buildProjectionPostgres(spec any, alias string, path string) string {
	switch v := spec.(type) {
	case bool:
		if v {
			return buildPostgresPath(alias, path)
		}
		return "NULL"
	case map[string]any:
		var parts []string
		for key, val := range v {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			expr := buildProjectionPostgres(val, alias, childPath)
			parts = append(parts, fmt.Sprintf("'%s', %s", key, expr))
		}
		if len(parts) == 0 {
			return "NULL"
		}
		return fmt.Sprintf("jsonb_build_object(%s)", strings.Join(parts, ", "))
	default:
		return buildPostgresPath(alias, path)
	}
}

// buildPostgresPath converts "metadata.name" to obj.object->'metadata'->'name'.
func buildPostgresPath(alias, path string) string {
	parts := strings.Split(path, ".")
	expr := alias + ".object"
	for _, p := range parts {
		expr += fmt.Sprintf("->'%s'", p)
	}
	return expr
}
