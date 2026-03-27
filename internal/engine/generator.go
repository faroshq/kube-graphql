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
	SQL       string
	Args      []any
	CountSQL  string
	CountArgs []any
	HasRelations bool
}

// Generator builds SQL from a QuerySpec.
type Generator struct {
	dialect     string // "sqlite" or "postgres"
	refRegistry *RefPathRegistry
}

// NewGenerator creates a new SQL generator.
func NewGenerator(dialect string) *Generator {
	return &Generator{
		dialect:     dialect,
		refRegistry: NewRefPathRegistry(),
	}
}

// Generate produces SQL for a query, including relation subqueries if present.
func (g *Generator) Generate(spec *v1alpha1.QuerySpec) (*GeneratedQuery, error) {
	hasRelations := spec.Objects != nil && len(spec.Objects.Relations) > 0
	if hasRelations {
		return g.generateWithRelations(spec)
	}
	return g.generateRootOnly(spec)
}

// generateRootOnly produces SQL for a single-level (root objects) query.
func (g *Generator) generateRootOnly(spec *v1alpha1.QuerySpec) (*GeneratedQuery, error) {
	alias := "obj"
	rootWhere, rootArgs, rootJoins, err := g.buildRootClauses(spec, alias)
	if err != nil {
		return nil, err
	}

	// Projection.
	projectionExpr, err := g.buildProjection(spec.Objects, alias)
	if err != nil {
		return nil, err
	}

	pathExpr := g.buildPathExpr(alias)

	// Build SELECT with level and relation_name columns for compatibility.
	selectCols := g.buildSelectCols(alias, projectionExpr, pathExpr, true)

	var sb strings.Builder
	sb.WriteString("SELECT ")
	sb.WriteString(selectCols)
	sb.WriteString(" FROM objects ")
	sb.WriteString(alias)
	for _, j := range rootJoins {
		sb.WriteString(" ")
		sb.WriteString(j)
	}

	args := append([]any{}, rootArgs...)

	if len(rootWhere) > 0 {
		sb.WriteString(" WHERE ")
		sb.WriteString(strings.Join(rootWhere, " AND "))
	}

	// Cursor.
	cursorWhere, cursorArgs := g.buildCursorFilter(spec)
	if cursorWhere != "" {
		if len(rootWhere) > 0 {
			sb.WriteString(" AND ")
		} else {
			sb.WriteString(" WHERE ")
		}
		sb.WriteString(cursorWhere)
		args = append(args, cursorArgs...)
	}

	sb.WriteString(" ORDER BY ")
	sb.WriteString(g.buildOrderBy(spec))
	fmt.Fprintf(&sb, " LIMIT %d", spec.Limit)
	if spec.Page != nil && spec.Page.First > 0 && spec.Page.Cursor == "" {
		fmt.Fprintf(&sb, " OFFSET %d", spec.Page.First)
	}

	// Count query.
	countArgs := make([]any, len(rootArgs))
	copy(countArgs, rootArgs)
	var countSB strings.Builder
	countSB.WriteString("SELECT COUNT(*) FROM objects ")
	countSB.WriteString(alias)
	for _, j := range rootJoins {
		countSB.WriteString(" ")
		countSB.WriteString(j)
	}
	if len(rootWhere) > 0 {
		countSB.WriteString(" WHERE ")
		countSB.WriteString(strings.Join(rootWhere, " AND "))
	}

	return &GeneratedQuery{
		SQL:       sb.String(),
		Args:      args,
		CountSQL:  countSB.String(),
		CountArgs: countArgs,
	}, nil
}

// generateWithRelations produces a UNION ALL query for root + all relation levels.
func (g *Generator) generateWithRelations(spec *v1alpha1.QuerySpec) (*GeneratedQuery, error) {
	rootAlias := "l0"

	rootWhere, rootArgs, rootJoins, err := g.buildRootClauses(spec, rootAlias)
	if err != nil {
		return nil, err
	}

	rootProj, err := g.buildProjection(spec.Objects, rootAlias)
	if err != nil {
		return nil, err
	}
	rootPath := g.buildPathExpr(rootAlias)

	// Build the root inner query (with ORDER BY/LIMIT) as a CTE.
	// This avoids ORDER BY inside UNION ALL which SQLite doesn't allow.
	var rootInnerSB strings.Builder
	rootInnerSB.WriteString("SELECT * FROM objects ")
	rootInnerSB.WriteString(rootAlias)
	for _, j := range rootJoins {
		rootInnerSB.WriteString(" ")
		rootInnerSB.WriteString(j)
	}

	allArgs := append([]any{}, rootArgs...)

	cursorWhere, cursorArgs := g.buildCursorFilter(spec)
	rootWhereAll := append([]string{}, rootWhere...)
	if cursorWhere != "" {
		rootWhereAll = append(rootWhereAll, cursorWhere)
		allArgs = append(allArgs, cursorArgs...)
	}

	if len(rootWhereAll) > 0 {
		rootInnerSB.WriteString(" WHERE ")
		rootInnerSB.WriteString(strings.Join(rootWhereAll, " AND "))
	}

	rootInnerSB.WriteString(" ORDER BY ")
	rootInnerSB.WriteString(strings.ReplaceAll(g.buildOrderBy(spec), "obj.", rootAlias+"."))
	fmt.Fprintf(&rootInnerSB, " LIMIT %d", spec.Limit)
	if spec.Page != nil && spec.Page.First > 0 && spec.Page.Cursor == "" {
		fmt.Fprintf(&rootInnerSB, " OFFSET %d", spec.Page.First)
	}

	// The UNION ALL uses root_objects CTE as the source for l0.
	// Root SELECT wraps inner query via CTE.
	rootSelect := g.buildSelectCols(rootAlias, rootProj, rootPath, false)

	var rootSB strings.Builder
	fmt.Fprintf(&rootSB, "WITH root_objects AS (%s) ", rootInnerSB.String())
	rootSB.WriteString("SELECT ")
	rootSB.WriteString(rootSelect)
	rootSB.WriteString(", 0 AS level, '' AS relation_name")
	rootSB.WriteString(" FROM root_objects ")
	rootSB.WriteString(rootAlias)

	// BFS over relation tree to generate subqueries.
	type ancestorLevel struct {
		alias string
		join  string
	}

	type bfsNode struct {
		level        int
		relationName string
		relationSpec v1alpha1.RelationSpec
		// Accumulated join chain from root.
		ancestors    []ancestorLevel
		parentObjSpec *v1alpha1.ObjectsSpec
	}

	var subqueries []string
	var extraCTEs []string // Additional CTEs for transitive relations.
	var queue []bfsNode

	// Seed BFS with root's relations.
	if spec.Objects != nil && spec.Objects.Relations != nil {
		for relName, relSpec := range spec.Objects.Relations {
			queue = append(queue, bfsNode{
				level:        1,
				relationName: relName,
				relationSpec: relSpec,
				ancestors:    nil,
				parentObjSpec: spec.Objects,
			})
		}
	}

	// Sort queue for deterministic SQL output.
	sort.Slice(queue, func(i, j int) bool { return queue[i].relationName < queue[j].relationName })

	levelCounter := 1
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]

		if node.level > int(spec.MaxDepth) {
			continue
		}

		parentAlias := rootAlias
		if len(node.ancestors) > 0 {
			parentAlias = node.ancestors[len(node.ancestors)-1].alias
		}

		// Check if this is a transitive relation (+ suffix).
		if IsTransitive(node.relationName) {
			cteName := fmt.Sprintf("trans_%s_%d", strings.ReplaceAll(BaseRelation(node.relationName), "-", "_"), levelCounter)
			ancestorsForCTE := make([]struct{ alias, join string }, len(node.ancestors))
			for i, a := range node.ancestors {
				ancestorsForCTE[i] = struct{ alias, join string }{a.alias, a.join}
			}

			result, err := buildTransitiveCTE(
				BaseRelation(node.relationName),
				cteName,
				rootAlias,
				rootPath,
				parentAlias,
				ancestorsForCTE,
				node.relationSpec,
				node.relationSpec.Objects,
				spec.MaxDepth,
				g.dialect,
				node.level,
				node.relationName,
			)
			if err != nil {
				return nil, err
			}

			extraCTEs = append(extraCTEs, result.cteSQL)
			subqueries = append(subqueries, result.selectSQL)
			allArgs = append(allArgs, result.selectArgs...)

			// Enqueue non-transitive sub-relations of the transitive result.
			// These join from the CTE table (e.g., "references" under "descendants+").
			if node.relationSpec.Objects != nil && node.relationSpec.Objects.Relations != nil {
				for relName, relSpec := range node.relationSpec.Objects.Relations {
					if IsTransitive(relName) {
						continue // Skip nested transitive (not supported yet)
					}
					subChildAlias := fmt.Sprintf("l%d", levelCounter+1)
					subRelType := BaseRelation(relName)
					joinClause, extraWhere, extraArgs := buildRelationJoin(subRelType, relationContext{
						parentAlias: cteName,
						childAlias:  subChildAlias,
						dialect:     g.dialect,
						filters:     relSpec.Filters,
						refRegistry: g.refRegistry,
					})

					// Relation-level filters.
					var subRelFilterWhere []string
					var subRelFilterArgs []any
					for _, f := range relSpec.Filters {
						subRTAlias := fmt.Sprintf("rt%d", levelCounter+1)
						andClauses, filterArgs, _ := g.buildObjectFilterWithRT(f, subChildAlias, subRTAlias)
						subRelFilterWhere = append(subRelFilterWhere, andClauses...)
						subRelFilterArgs = append(subRelFilterArgs, filterArgs...)
					}

					subProj, err := g.buildProjection(relSpec.Objects, subChildAlias)
					if err != nil {
						return nil, err
					}

					// Path: CTE row's path + child segment.
					subPathExpr := fmt.Sprintf("%s.path || '.' || lower(%s.kind) || '.' || %s.namespace || '/' || %s.name",
						cteName, subChildAlias, subChildAlias, subChildAlias)
					selectCols := g.buildSelectCols(subChildAlias, subProj, subPathExpr, false)

					var subSB strings.Builder
					subSB.WriteString("SELECT ")
					subSB.WriteString(selectCols)
					fmt.Fprintf(&subSB, ", %d AS level, '%s' AS relation_name", node.level+1, relName)
					fmt.Fprintf(&subSB, " FROM %s", cteName)
					subSB.WriteString(" ")
					subSB.WriteString(joinClause)

					var subWhere []string
					subWhere = append(subWhere, extraWhere...)
					subWhere = append(subWhere, subRelFilterWhere...)
					if len(subWhere) > 0 {
						subSB.WriteString(" WHERE ")
						subSB.WriteString(strings.Join(subWhere, " AND "))
					}

					subqueries = append(subqueries, subSB.String())
					var subArgs []any
					subArgs = append(subArgs, extraArgs...)
					subArgs = append(subArgs, subRelFilterArgs...)
					allArgs = append(allArgs, subArgs...)
				}
			}

			levelCounter++
			continue
		}

		// Non-transitive relation handling (existing logic).
		childAlias := fmt.Sprintf("l%d", levelCounter)

		relType := BaseRelation(node.relationName)
		joinClause, extraWhere, extraArgs := buildRelationJoin(relType, relationContext{
			parentAlias: parentAlias,
			childAlias:  childAlias,
			dialect:     g.dialect,
			filters:     node.relationSpec.Filters,
			refRegistry: g.refRegistry,
		})

		var relFilterWhere []string
		var relFilterArgs []any
		if len(node.relationSpec.Filters) > 0 {
			for _, f := range node.relationSpec.Filters {
				rtAlias := fmt.Sprintf("rt%d", levelCounter)
				andClauses, filterArgs, _ := g.buildObjectFilterWithRT(f, childAlias, rtAlias)
				relFilterWhere = append(relFilterWhere, andClauses...)
				relFilterArgs = append(relFilterArgs, filterArgs...)
			}
		}

		childProj, err := g.buildProjection(node.relationSpec.Objects, childAlias)
		if err != nil {
			return nil, err
		}

		childPathSegment := fmt.Sprintf("'.' || lower(%s.kind) || '.' || %s.namespace || '/' || %s.name",
			childAlias, childAlias, childAlias)
		var pathExpr string
		if len(node.ancestors) == 0 {
			pathExpr = fmt.Sprintf("%s || %s", rootPath, childPathSegment)
		} else {
			pathExpr = rootPath
			for _, a := range node.ancestors {
				aAlias := a.alias
				pathExpr += fmt.Sprintf(" || '.' || lower(%s.kind) || '.' || %s.namespace || '/' || %s.name",
					aAlias, aAlias, aAlias)
			}
			pathExpr += fmt.Sprintf(" || %s", childPathSegment)
		}

		selectCols := g.buildSelectCols(childAlias, childProj, pathExpr, false)

		var subSB strings.Builder
		subSB.WriteString("SELECT ")
		subSB.WriteString(selectCols)
		fmt.Fprintf(&subSB, ", %d AS level, '%s' AS relation_name", node.level, node.relationName)

		subSB.WriteString(" FROM root_objects ")
		subSB.WriteString(rootAlias)
		for _, a := range node.ancestors {
			subSB.WriteString(" ")
			subSB.WriteString(a.join)
		}
		subSB.WriteString(" ")
		subSB.WriteString(joinClause)

		var subWhere []string
		subWhere = append(subWhere, extraWhere...)
		subWhere = append(subWhere, relFilterWhere...)

		var subArgs []any
		subArgs = append(subArgs, extraArgs...)
		subArgs = append(subArgs, relFilterArgs...)

		if len(subWhere) > 0 {
			subSB.WriteString(" WHERE ")
			subSB.WriteString(strings.Join(subWhere, " AND "))
		}

		if node.relationSpec.Limit > 0 {
			wrapped := fmt.Sprintf("SELECT * FROM (%s LIMIT %d)", subSB.String(), node.relationSpec.Limit)
			subqueries = append(subqueries, wrapped)
		} else {
			subqueries = append(subqueries, subSB.String())
		}
		allArgs = append(allArgs, subArgs...)

		currentAncestors := append([]ancestorLevel{}, node.ancestors...)
		currentAncestors = append(currentAncestors, ancestorLevel{
			alias: childAlias,
			join:  joinClause,
		})

		if node.relationSpec.Objects != nil && node.relationSpec.Objects.Relations != nil {
			var childRelNames []string
			for relName := range node.relationSpec.Objects.Relations {
				childRelNames = append(childRelNames, relName)
			}
			sort.Strings(childRelNames)
			for _, relName := range childRelNames {
				relSpec := node.relationSpec.Objects.Relations[relName]
				queue = append(queue, bfsNode{
					level:        node.level + 1,
					relationName: relName,
					relationSpec: relSpec,
					ancestors:    currentAncestors,
					parentObjSpec: node.relationSpec.Objects,
				})
			}
		}

		levelCounter++
	}

	// Assemble UNION ALL.
	// If there are transitive CTEs, inject them into the WITH clause.
	var finalSB strings.Builder
	if len(extraCTEs) > 0 {
		// rootSB starts with "WITH root_objects AS (...) SELECT ..."
		// We need to insert additional CTEs after root_objects.
		rootSQL := rootSB.String()
		// Find the end of the WITH clause: after "WITH root_objects AS (...) "
		// Replace "WITH root_objects AS (...) SELECT" with
		// "WITH RECURSIVE root_objects AS (...), trans_xxx AS (...) SELECT"
		withPrefix := fmt.Sprintf("WITH root_objects AS (%s) ", rootInnerSB.String())
		rest := strings.TrimPrefix(rootSQL, withPrefix)
		finalSB.WriteString("WITH RECURSIVE root_objects AS (")
		finalSB.WriteString(rootInnerSB.String())
		finalSB.WriteString("), ")
		finalSB.WriteString(strings.Join(extraCTEs, ", "))
		finalSB.WriteString(" ")
		finalSB.WriteString(rest)
	} else {
		finalSB.WriteString(rootSB.String())
	}
	for _, sq := range subqueries {
		finalSB.WriteString(" UNION ALL ")
		finalSB.WriteString(sq)
	}
	finalSB.WriteString(" ORDER BY path")

	// Count query (root only).
	countArgs := make([]any, len(rootArgs))
	copy(countArgs, rootArgs)
	var countSB strings.Builder
	countSB.WriteString("SELECT COUNT(*) FROM objects ")
	countSB.WriteString(rootAlias)
	for _, j := range rootJoins {
		countSB.WriteString(" ")
		countSB.WriteString(j)
	}
	if len(rootWhere) > 0 {
		countSB.WriteString(" WHERE ")
		countSB.WriteString(strings.Join(rootWhere, " AND "))
	}

	return &GeneratedQuery{
		SQL:          finalSB.String(),
		Args:         allArgs,
		CountSQL:     countSB.String(),
		CountArgs:    countArgs,
		HasRelations: true,
	}, nil
}

// buildRootClauses generates WHERE, args, and JOINs for the root query level.
func (g *Generator) buildRootClauses(spec *v1alpha1.QuerySpec, alias string) ([]string, []any, []string, error) {
	var whereClauses []string
	var args []any
	var joins []string

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
				default:
					whereClauses = append(whereClauses, fmt.Sprintf("json_extract(cl.labels, '$.%s') = ?", k))
					args = append(args, v)
				}
			}
		}
	}

	// Object filters.
	if spec.Filter != nil && len(spec.Filter.Objects) > 0 {
		var orGroups []string
		for _, f := range spec.Filter.Objects {
			andClauses, filterArgs, _ := g.buildObjectFilter(f, alias)
			if len(andClauses) > 0 {
				orGroups = append(orGroups, "("+strings.Join(andClauses, " AND ")+")")
				args = append(args, filterArgs...)
			}
		}
		if len(orGroups) > 0 {
			whereClauses = append(whereClauses, "("+strings.Join(orGroups, " OR ")+")")
		}
	}

	return whereClauses, args, joins, nil
}

// buildProjection generates the projection SQL expression.
func (g *Generator) buildProjection(objSpec *v1alpha1.ObjectsSpec, alias string) (string, error) {
	if objSpec != nil && objSpec.Object != nil && len(objSpec.Object.Raw) > 0 {
		return BuildProjectionSQL(objSpec.Object.Raw, g.dialect, alias)
	}
	return alias + ".object", nil
}

// buildPathExpr generates the path expression for the given alias.
func (g *Generator) buildPathExpr(alias string) string {
	return fmt.Sprintf("'.' || lower(%s.kind) || '.' || %s.namespace || '/' || %s.name", alias, alias, alias)
}

// buildSelectCols generates the SELECT column list.
func (g *Generator) buildSelectCols(alias, projectionExpr, pathExpr string, includeMetaCols bool) string {
	cols := fmt.Sprintf(
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
	if includeMetaCols {
		cols += ", 0 AS level, '' AS relation_name"
	}
	return cols
}

// buildObjectFilter converts a single ObjectFilter into AND-ed WHERE clauses.
// Uses "rt" as the resource_types alias.
func (g *Generator) buildObjectFilter(f v1alpha1.ObjectFilter, alias string) (clauses []string, args []any, needsRT bool) {
	return g.buildObjectFilterWithRT(f, alias, "rt")
}

// buildObjectFilterWithRT converts a single ObjectFilter using a custom RT alias.
func (g *Generator) buildObjectFilterWithRT(f v1alpha1.ObjectFilter, alias, rtAlias string) (clauses []string, args []any, needsRT bool) {
	if f.GroupKind != nil {
		// Use EXISTS subquery instead of JOIN to avoid row duplication when
		// multiple resource_type versions exist for the same (cluster, api_group, kind).
		if f.GroupKind.APIGroup != "" {
			clauses = append(clauses, alias+".api_group = ?")
			args = append(args, f.GroupKind.APIGroup)
		}
		if f.GroupKind.Kind != "" {
			switch g.dialect {
			case "postgres":
				clauses = append(clauses, fmt.Sprintf(
					"EXISTS (SELECT 1 FROM resource_types %s WHERE %s.cluster = %s.cluster AND %s.api_group = %s.api_group AND %s.kind = %s.kind AND "+
						"(lower(%s.kind) = lower(?) OR lower(%s.resource) = lower(?) OR lower(%s.singular) = lower(?) OR ? = ANY(CASE WHEN jsonb_typeof(%s.short_names) = 'array' THEN ARRAY(SELECT jsonb_array_elements_text(%s.short_names)) ELSE ARRAY[]::text[] END)))",
					rtAlias, rtAlias, alias, rtAlias, alias, rtAlias, alias,
					rtAlias, rtAlias, rtAlias, rtAlias, rtAlias))
			default:
				clauses = append(clauses, fmt.Sprintf(
					"EXISTS (SELECT 1 FROM resource_types %s WHERE %s.cluster = %s.cluster AND %s.api_group = %s.api_group AND %s.kind = %s.kind AND "+
						"(lower(%s.kind) = lower(?) OR lower(%s.resource) = lower(?) OR lower(%s.singular) = lower(?) OR EXISTS (SELECT 1 FROM json_each(%s.short_names) WHERE json_each.value = lower(?))))",
					rtAlias, rtAlias, alias, rtAlias, alias, rtAlias, alias,
					rtAlias, rtAlias, rtAlias, rtAlias))
			}
			args = append(args, f.GroupKind.Kind, f.GroupKind.Kind, f.GroupKind.Kind, strings.ToLower(f.GroupKind.Kind))
		}
	}

	if f.Name != "" {
		clauses = append(clauses, alias+".name = ?")
		args = append(args, f.Name)
	}
	if f.Namespace != "" {
		clauses = append(clauses, alias+".namespace = ?")
		args = append(args, f.Namespace)
	}

	if len(f.Labels) > 0 {
		switch g.dialect {
		case "postgres":
			labelsJSON, _ := json.Marshal(f.Labels)
			clauses = append(clauses, alias+".labels @> ?::jsonb")
			args = append(args, string(labelsJSON))
		default:
			for k, v := range f.Labels {
				clauses = append(clauses, fmt.Sprintf("json_extract(%s.labels, '$.%s') = ?", alias, k))
				args = append(args, v)
			}
		}
	}

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
			default:
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

	if f.ID != "" {
		clauses = append(clauses, alias+".id = ?")
		args = append(args, f.ID)
	}

	if len(f.Categories) > 0 {
		for _, cat := range f.Categories {
			switch g.dialect {
			case "postgres":
				clauses = append(clauses, fmt.Sprintf(
					"EXISTS (SELECT 1 FROM resource_types %s WHERE %s.cluster = %s.cluster AND %s.api_group = %s.api_group AND %s.kind = %s.kind AND ? = ANY(CASE WHEN jsonb_typeof(%s.categories) = 'array' THEN ARRAY(SELECT jsonb_array_elements_text(%s.categories)) ELSE ARRAY[]::text[] END))",
					rtAlias, rtAlias, alias, rtAlias, alias, rtAlias, alias, rtAlias, rtAlias))
			default:
				clauses = append(clauses, fmt.Sprintf(
					"EXISTS (SELECT 1 FROM resource_types %s WHERE %s.cluster = %s.cluster AND %s.api_group = %s.api_group AND %s.kind = %s.kind AND EXISTS (SELECT 1 FROM json_each(%s.categories) WHERE json_each.value = ?))",
					rtAlias, rtAlias, alias, rtAlias, alias, rtAlias, alias, rtAlias))
			}
			args = append(args, cat)
		}
	}

	// Label expressions (In, NotIn, Exists, DoesNotExist).
	for _, expr := range f.LabelExpressions {
		switch expr.Operator {
		case v1alpha1.LabelOpIn:
			if len(expr.Values) > 0 {
				placeholders := make([]string, len(expr.Values))
				for i := range expr.Values {
					placeholders[i] = "?"
					args = append(args, expr.Values[i])
				}
				switch g.dialect {
				case "postgres":
					clauses = append(clauses, fmt.Sprintf("%s.labels->>'%s' IN (%s)", alias, expr.Key, strings.Join(placeholders, ", ")))
				default:
					clauses = append(clauses, fmt.Sprintf("json_extract(%s.labels, '$.%s') IN (%s)", alias, expr.Key, strings.Join(placeholders, ", ")))
				}
			}
		case v1alpha1.LabelOpNotIn:
			if len(expr.Values) > 0 {
				placeholders := make([]string, len(expr.Values))
				for i := range expr.Values {
					placeholders[i] = "?"
					args = append(args, expr.Values[i])
				}
				switch g.dialect {
				case "postgres":
					clauses = append(clauses, fmt.Sprintf("(%s.labels->>'%s' IS NULL OR %s.labels->>'%s' NOT IN (%s))", alias, expr.Key, alias, expr.Key, strings.Join(placeholders, ", ")))
				default:
					clauses = append(clauses, fmt.Sprintf("(json_extract(%s.labels, '$.%s') IS NULL OR json_extract(%s.labels, '$.%s') NOT IN (%s))", alias, expr.Key, alias, expr.Key, strings.Join(placeholders, ", ")))
				}
			}
		case v1alpha1.LabelOpExists:
			switch g.dialect {
			case "postgres":
				clauses = append(clauses, fmt.Sprintf("%s.labels ? '%s'", alias, expr.Key))
			default:
				clauses = append(clauses, fmt.Sprintf("json_extract(%s.labels, '$.%s') IS NOT NULL", alias, expr.Key))
			}
		case v1alpha1.LabelOpDoesNotExist:
			switch g.dialect {
			case "postgres":
				clauses = append(clauses, fmt.Sprintf("NOT (%s.labels ? '%s')", alias, expr.Key))
			default:
				clauses = append(clauses, fmt.Sprintf("json_extract(%s.labels, '$.%s') IS NULL", alias, expr.Key))
			}
		}
	}

	// JSONPath boolean filter (last resort).
	if f.JSONPath != "" {
		switch g.dialect {
		case "postgres":
			clauses = append(clauses, fmt.Sprintf("jsonb_path_match(%s.object, ?::jsonpath)", alias))
			args = append(args, f.JSONPath)
		default: // sqlite
			// SQLite doesn't have jsonb_path_match. Use json_extract for simple paths.
			// The JSONPath should be a simple extraction path like "$.status.phase"
			// followed by a comparison. For boolean matching, we check if the
			// extracted value is truthy.
			clauses = append(clauses, fmt.Sprintf("json_extract(%s.object, ?) IS NOT NULL AND json_extract(%s.object, ?) != 'false' AND json_extract(%s.object, ?) != 0", alias, alias, alias))
			args = append(args, f.JSONPath, f.JSONPath, f.JSONPath)
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

	orderFields := spec.Order
	if len(orderFields) == 0 {
		orderFields = []v1alpha1.OrderSpec{{Field: "name", Direction: v1alpha1.SortAsc}}
	}

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

	var cols []string
	var args []any
	for _, o := range orderFields {
		col := ValidSortFields[o.Field]
		cols = append(cols, col)
		args = append(args, cursor.Values[o.Field])
	}

	op := ">"
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
