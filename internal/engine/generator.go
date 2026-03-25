package engine

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
)

// GeneratedQuery holds the generated SQL and its parameters.
type GeneratedQuery struct {
	SQL    string
	Args   []any
	CountSQL string
	CountArgs []any
}

// Generator builds SQL from a QuerySpec.
type Generator struct {
	dialect string // "sqlite" or "postgres"
}

// NewGenerator creates a new SQL generator.
func NewGenerator(dialect string) *Generator {
	return &Generator{dialect: dialect}
}

// Generate produces SQL for a single-level (root objects) query.
// Relations are handled in Phase 4.
func (g *Generator) Generate(spec *v1alpha1.QuerySpec) (*GeneratedQuery, error) {
	var (
		whereClauses []string
		args         []any
		joins        []string
	)

	alias := "obj"

	// Cluster filter.
	if spec.Cluster != nil {
		if spec.Cluster.Name != "" {
			whereClauses = append(whereClauses, alias+".cluster = ?")
			args = append(args, spec.Cluster.Name)
		}
		if len(spec.Cluster.Labels) > 0 {
			joins = append(joins, "JOIN clusters cl ON cl.name = "+alias+".cluster")
			for k, v := range spec.Cluster.Labels {
				switch g.dialect {
				case "postgres":
					whereClauses = append(whereClauses, "cl.labels @> ?::jsonb")
					labelsJSON, _ := json.Marshal(map[string]string{k: v})
					args = append(args, string(labelsJSON))
				default: // sqlite
					whereClauses = append(whereClauses, fmt.Sprintf("json_extract(cl.labels, '$.%s') = ?", k))
					args = append(args, v)
				}
			}
		}
	}

	// Object filters (OR-ed, AND within each entry).
	needsRT := false
	if spec.Filter != nil && len(spec.Filter.Objects) > 0 {
		var orGroups []string
		for _, f := range spec.Filter.Objects {
			andClauses, filterArgs, rtNeeded := g.buildObjectFilter(f, alias)
			if rtNeeded {
				needsRT = true
			}
			if len(andClauses) > 0 {
				orGroups = append(orGroups, "("+strings.Join(andClauses, " AND ")+")")
				args = append(args, filterArgs...)
			}
		}
		if len(orGroups) > 0 {
			whereClauses = append(whereClauses, "("+strings.Join(orGroups, " OR ")+")")
		}
	}

	// Resource types JOIN for kind/category resolution.
	if needsRT {
		joins = append(joins, fmt.Sprintf(
			"JOIN resource_types rt ON rt.cluster = %s.cluster AND rt.api_group = %s.api_group AND rt.kind = %s.kind",
			alias, alias, alias))
	}

	// Projection.
	projectionExpr := alias + ".object"
	if spec.Objects != nil && spec.Objects.Object != nil && len(spec.Objects.Object.Raw) > 0 {
		var err error
		projectionExpr, err = BuildProjectionSQL(spec.Objects.Object.Raw, g.dialect, alias)
		if err != nil {
			return nil, fmt.Errorf("building projection: %w", err)
		}
	}

	// Path column for tree assembly (used in later phases, but set up now).
	var pathExpr string
	switch g.dialect {
	case "postgres":
		pathExpr = fmt.Sprintf("'.' || lower(%s.kind) || '.' || %s.namespace || '/' || %s.name", alias, alias, alias)
	default: // sqlite
		pathExpr = fmt.Sprintf("'.' || lower(%s.kind) || '.' || %s.namespace || '/' || %s.name", alias, alias, alias)
	}

	// Build SELECT.
	selectCols := fmt.Sprintf(
		"%s.id, %s.uid, %s.cluster, %s.api_group, %s.api_version, %s.kind, %s.resource, "+
			"%s.namespace, %s.name, %s.labels, %s.annotations, %s.owner_refs, %s.conditions, "+
			"%s.creation_ts, %s.resource_version, "+
			"%s AS projected_object, "+
			"%s AS path",
		alias, alias, alias, alias, alias, alias, alias,
		alias, alias, alias, alias, alias, alias,
		alias, alias,
		projectionExpr,
		pathExpr,
	)

	// Assemble main query.
	var sb strings.Builder
	sb.WriteString("SELECT ")
	sb.WriteString(selectCols)
	sb.WriteString(" FROM objects ")
	sb.WriteString(alias)
	for _, j := range joins {
		sb.WriteString(" ")
		sb.WriteString(j)
	}

	if len(whereClauses) > 0 {
		sb.WriteString(" WHERE ")
		sb.WriteString(strings.Join(whereClauses, " AND "))
	}

	// Cursor-based pagination (keyset).
	cursorWhere, cursorArgs := g.buildCursorFilter(spec)
	if cursorWhere != "" {
		if len(whereClauses) > 0 {
			sb.WriteString(" AND ")
		} else {
			sb.WriteString(" WHERE ")
		}
		sb.WriteString(cursorWhere)
		args = append(args, cursorArgs...)
	}

	// ORDER BY.
	sb.WriteString(" ORDER BY ")
	sb.WriteString(g.buildOrderBy(spec))

	// LIMIT + OFFSET.
	fmt.Fprintf(&sb, " LIMIT %d", spec.Limit)
	if spec.Page != nil && spec.Page.First > 0 && spec.Page.Cursor == "" {
		fmt.Fprintf(&sb, " OFFSET %d", spec.Page.First)
	}

	// Count query.
	var countSB strings.Builder
	countSB.WriteString("SELECT COUNT(*) FROM objects ")
	countSB.WriteString(alias)
	for _, j := range joins {
		countSB.WriteString(" ")
		countSB.WriteString(j)
	}
	// Count uses the same WHERE but without cursor filter.
	countArgs := make([]any, len(args)-len(cursorArgs))
	copy(countArgs, args[:len(args)-len(cursorArgs)])
	if len(whereClauses) > 0 {
		countSB.WriteString(" WHERE ")
		countSB.WriteString(strings.Join(whereClauses, " AND "))
	}

	return &GeneratedQuery{
		SQL:       sb.String(),
		Args:      args,
		CountSQL:  countSB.String(),
		CountArgs: countArgs,
	}, nil
}

// buildObjectFilter converts a single ObjectFilter into AND-ed WHERE clauses.
func (g *Generator) buildObjectFilter(f v1alpha1.ObjectFilter, alias string) (clauses []string, args []any, needsRT bool) {
	// GroupKind filter.
	if f.GroupKind != nil {
		needsRT = true
		if f.GroupKind.APIGroup != "" {
			clauses = append(clauses, alias+".api_group = ?")
			args = append(args, f.GroupKind.APIGroup)
		}
		if f.GroupKind.Kind != "" {
			// Resolve via resource_types: kind, resource, singular, or short_names.
			switch g.dialect {
			case "postgres":
				clauses = append(clauses, "(lower(rt.kind) = lower(?) OR lower(rt.resource) = lower(?) OR lower(rt.singular) = lower(?) OR ? = ANY(ARRAY(SELECT jsonb_array_elements_text(rt.short_names))))")
				args = append(args, f.GroupKind.Kind, f.GroupKind.Kind, f.GroupKind.Kind, strings.ToLower(f.GroupKind.Kind))
			default: // sqlite
				// For SQLite we use a simpler approach: match kind or resource directly,
				// plus check short_names JSON array.
				clauses = append(clauses, "(lower(rt.kind) = lower(?) OR lower(rt.resource) = lower(?) OR lower(rt.singular) = lower(?) OR EXISTS (SELECT 1 FROM json_each(rt.short_names) WHERE json_each.value = lower(?)))")
				args = append(args, f.GroupKind.Kind, f.GroupKind.Kind, f.GroupKind.Kind, strings.ToLower(f.GroupKind.Kind))
			}
		}
	}

	// Name.
	if f.Name != "" {
		clauses = append(clauses, alias+".name = ?")
		args = append(args, f.Name)
	}

	// Namespace.
	if f.Namespace != "" {
		clauses = append(clauses, alias+".namespace = ?")
		args = append(args, f.Namespace)
	}

	// Labels (matchLabels style).
	if len(f.Labels) > 0 {
		switch g.dialect {
		case "postgres":
			labelsJSON, _ := json.Marshal(f.Labels)
			clauses = append(clauses, alias+".labels @> ?::jsonb")
			args = append(args, string(labelsJSON))
		default: // sqlite — use json_extract per label.
			for k, v := range f.Labels {
				clauses = append(clauses, fmt.Sprintf("json_extract(%s.labels, '$.%s') = ?", alias, k))
				args = append(args, v)
			}
		}
	}

	// Conditions.
	if len(f.Conditions) > 0 {
		for _, cond := range f.Conditions {
			switch g.dialect {
			case "postgres":
				condJSON := map[string]string{"type": cond.Type}
				if cond.Status != "" {
					condJSON["status"] = cond.Status
				}
				if cond.Reason != "" {
					condJSON["reason"] = cond.Reason
				}
				b, _ := json.Marshal([]map[string]string{condJSON})
				clauses = append(clauses, alias+".conditions @> ?::jsonb")
				args = append(args, string(b))
			default: // sqlite — use json_each to search the conditions array.
				condParts := []string{"json_extract(je.value, '$.type') = ?"}
				condArgs := []any{cond.Type}
				if cond.Status != "" {
					condParts = append(condParts, "json_extract(je.value, '$.status') = ?")
					condArgs = append(condArgs, cond.Status)
				}
				if cond.Reason != "" {
					condParts = append(condParts, "json_extract(je.value, '$.reason') = ?")
					condArgs = append(condArgs, cond.Reason)
				}
				clauses = append(clauses, fmt.Sprintf(
					"EXISTS (SELECT 1 FROM json_each(%s.conditions) je WHERE %s)",
					alias, strings.Join(condParts, " AND ")))
				args = append(args, condArgs...)
			}
		}
	}

	// CreationTimestamp.
	if f.CreationTimestamp != nil {
		if f.CreationTimestamp.After != nil {
			clauses = append(clauses, alias+".creation_ts > ?")
			args = append(args, f.CreationTimestamp.After.Time)
		}
		if f.CreationTimestamp.Before != nil {
			clauses = append(clauses, alias+".creation_ts < ?")
			args = append(args, f.CreationTimestamp.Before.Time)
		}
	}

	// ID.
	if f.ID != "" {
		clauses = append(clauses, alias+".id = ?")
		args = append(args, f.ID)
	}

	// Categories.
	if len(f.Categories) > 0 {
		needsRT = true
		for _, cat := range f.Categories {
			switch g.dialect {
			case "postgres":
				clauses = append(clauses, "? = ANY(ARRAY(SELECT jsonb_array_elements_text(rt.categories)))")
				args = append(args, cat)
			default: // sqlite
				clauses = append(clauses, "EXISTS (SELECT 1 FROM json_each(rt.categories) WHERE json_each.value = ?)")
				args = append(args, cat)
			}
		}
	}

	return clauses, args, needsRT
}

// buildOrderBy generates the ORDER BY clause.
func (g *Generator) buildOrderBy(spec *v1alpha1.QuerySpec) string {
	var parts []string

	if len(spec.Order) > 0 {
		for _, o := range spec.Order {
			col := ValidSortFields[o.Field]
			dir := "ASC"
			if o.Direction == v1alpha1.SortDesc {
				dir = "DESC"
			}
			parts = append(parts, col+" "+dir)
		}
	}

	// Tiebreaker: namespace ASC, name ASC — always appended for stable pagination.
	tiebreakers := map[string]bool{}
	for _, p := range parts {
		tiebreakers[strings.Split(p, " ")[0]] = true
	}
	if !tiebreakers["obj.namespace"] {
		parts = append(parts, "obj.namespace ASC")
	}
	if !tiebreakers["obj.name"] {
		parts = append(parts, "obj.name ASC")
	}

	// Default: name ASC if no explicit order and tiebreaker already handles it.
	if len(spec.Order) == 0 {
		return "obj.name ASC, obj.namespace ASC"
	}

	return strings.Join(parts, ", ")
}

// CursorData holds the keyset values for cursor pagination.
type CursorData struct {
	Values map[string]string `json:"v"`
}

// buildCursorFilter decodes a cursor and generates the WHERE clause for keyset pagination.
func (g *Generator) buildCursorFilter(spec *v1alpha1.QuerySpec) (string, []any) {
	if spec.Page == nil || spec.Page.Cursor == "" {
		return "", nil
	}

	data, err := base64.StdEncoding.DecodeString(spec.Page.Cursor)
	if err != nil {
		return "", nil
	}

	var cursor CursorData
	if err := json.Unmarshal(data, &cursor); err != nil {
		return "", nil
	}

	// Build keyset condition using the sort fields.
	orderFields := spec.Order
	if len(orderFields) == 0 {
		orderFields = []v1alpha1.OrderSpec{{Field: "name", Direction: v1alpha1.SortAsc}}
	}

	// Add tiebreaker fields if not present.
	hasNamespace := false
	hasName := false
	for _, o := range orderFields {
		if o.Field == "namespace" {
			hasNamespace = true
		}
		if o.Field == "name" {
			hasName = true
		}
	}
	if !hasNamespace {
		orderFields = append(orderFields, v1alpha1.OrderSpec{Field: "namespace", Direction: v1alpha1.SortAsc})
	}
	if !hasName {
		orderFields = append(orderFields, v1alpha1.OrderSpec{Field: "name", Direction: v1alpha1.SortAsc})
	}

	// Build tuple comparison: (col1, col2, ...) > (val1, val2, ...)
	var cols []string
	var args []any
	for _, o := range orderFields {
		col := ValidSortFields[o.Field]
		cols = append(cols, col)
		args = append(args, cursor.Values[o.Field])
	}

	op := ">"
	// If the primary sort is DESC, we need <.
	if len(spec.Order) > 0 && spec.Order[0].Direction == v1alpha1.SortDesc {
		op = "<"
	}

	clause := fmt.Sprintf("(%s) %s (%s)",
		strings.Join(cols, ", "),
		op,
		strings.Join(strings.Split(strings.Repeat("?,", len(cols)), ",")[:len(cols)], ", "))

	return clause, args
}

// BuildCursorToken encodes sort key values from the last row into an opaque cursor.
func BuildCursorToken(values map[string]string) string {
	cursor := CursorData{Values: values}
	data, _ := json.Marshal(cursor)
	return base64.StdEncoding.EncodeToString(data)
}

// MutablePath constructs the REST path for an object.
func MutablePath(apiGroup, apiVersion, resource, namespace, name string) string {
	var prefix string
	if apiGroup == "" {
		prefix = "/api/" + apiVersion
	} else {
		prefix = "/apis/" + apiGroup + "/" + apiVersion
	}
	if namespace != "" {
		return prefix + "/namespaces/" + namespace + "/" + resource + "/" + name
	}
	return prefix + "/" + resource + "/" + name
}

// SortMapKeys returns sorted keys of a map for deterministic output.
func SortMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
