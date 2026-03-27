package engine

import (
	"fmt"
	"strings"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
)

// transitiveResult holds the generated CTE and SELECT for a transitive relation.
type transitiveResult struct {
	cteName string // CTE name, e.g. "trans_descendants_1"
	cteSQL  string // The recursive CTE body (without "WITH RECURSIVE")
	cteArgs []any  // Args for the CTE
	// The SELECT to include in UNION ALL that reads from the CTE.
	selectSQL  string
	selectArgs []any
}

// buildTransitiveCTE generates a recursive CTE for a transitive relation.
// It handles descendants+, owners+.
func buildTransitiveCTE(
	relationType string, // base type: "descendants" or "owners"
	cteName string,
	rootAlias string,
	rootPath string, // path expression for root
	parentAlias string, // alias of the direct parent (l0, l1, etc.)
	ancestors []struct{ alias, join string },
	relationSpec v1alpha1.RelationSpec,
	objSpec *v1alpha1.ObjectsSpec,
	maxDepth int32,
	dialect string,
	levelNum int,
	relationName string,
) (*transitiveResult, error) {
	childAlias := "curr" // alias within the recursive CTE

	// Build the JOIN condition for the base relation type.
	var baseJoinCond string
	var recursiveJoinCond string

	switch relationType {
	case RelDescendants:
		baseJoinCond = descendantJoinCond(childAlias, parentAlias, dialect)
		recursiveJoinCond = descendantJoinCond("next", cteName, dialect)
	case RelOwners:
		baseJoinCond = ownerJoinCond(childAlias, parentAlias, dialect)
		recursiveJoinCond = ownerJoinCond("next", cteName, dialect)
	case RelLinked:
		baseJoinCond = linkedJoinCond(childAlias, parentAlias, dialect)
		recursiveJoinCond = linkedJoinCond("next", cteName, dialect)
	default:
		return nil, fmt.Errorf("unsupported transitive relation: %s", relationType)
	}

	// Build the parent path expression (for the base case, the path of the parent).
	parentPathExpr := rootPath
	for _, a := range ancestors {
		parentPathExpr += fmt.Sprintf(" || '.' || lower(%s.kind) || '.' || %s.namespace || '/' || %s.name",
			a.alias, a.alias, a.alias)
	}

	// Projection.
	projExpr := childAlias + ".object"
	if objSpec != nil && objSpec.Object != nil && len(objSpec.Object.Raw) > 0 {
		var err error
		projExpr, err = BuildProjectionSQL(objSpec.Object.Raw, dialect, childAlias)
		if err != nil {
			return nil, err
		}
	}

	nextProjExpr := "next.object"
	if objSpec != nil && objSpec.Object != nil && len(objSpec.Object.Raw) > 0 {
		var err error
		nextProjExpr, err = BuildProjectionSQL(objSpec.Object.Raw, dialect, "next")
		if err != nil {
			return nil, err
		}
	}

	// Build recursive CTE.
	var cteSB strings.Builder

	// Base case columns: all object columns + projected_object, path, depth, visited, level, relation_name.
	basePathExpr := fmt.Sprintf("%s || '.' || lower(%s.kind) || '.' || %s.namespace || '/' || %s.name",
		parentPathExpr, childAlias, childAlias, childAlias)

	// Visited: for cycle detection.
	var baseVisited, recursiveVisited, cycleCheck string
	switch dialect {
	case "postgres":
		baseVisited = fmt.Sprintf("ARRAY[%s.uid::text]", childAlias)
		recursiveVisited = fmt.Sprintf("%s.visited || next.uid::text", cteName)
		cycleCheck = fmt.Sprintf("next.uid::text != ALL(%s.visited)", cteName)
	default: // sqlite — use comma-separated string
		baseVisited = fmt.Sprintf("',' || %s.uid || ','", childAlias)
		recursiveVisited = fmt.Sprintf("%s.visited || next.uid || ','", cteName)
		cycleCheck = fmt.Sprintf("%s.visited NOT LIKE '%%,' || next.uid || ',%%'", cteName)
	}

	// Base case SELECT — includes raw `object` column for sub-relation JOINs.
	baseCols := fmt.Sprintf(
		"%s.id, %s.uid, %s.cluster, %s.api_group, %s.api_version, %s.kind, %s.resource, "+
			"%s.namespace, %s.name, %s.labels, %s.annotations, %s.owner_refs, %s.conditions, "+
			"%s.creation_ts, %s.resource_version, %s.object, "+
			"%s AS projected_object, "+
			"%s AS path, "+
			"1 AS depth, "+
			"%s AS visited, "+
			"%d AS level, "+
			"'%s' AS relation_name",
		childAlias, childAlias, childAlias, childAlias, childAlias, childAlias, childAlias,
		childAlias, childAlias, childAlias, childAlias, childAlias, childAlias,
		childAlias, childAlias, childAlias,
		projExpr,
		basePathExpr,
		baseVisited,
		levelNum, relationName,
	)

	// Recursive case SELECT.
	recursivePathExpr := fmt.Sprintf("%s.path || '.' || lower(next.kind) || '.' || next.namespace || '/' || next.name", cteName)

	recursiveCols := fmt.Sprintf(
		"next.id, next.uid, next.cluster, next.api_group, next.api_version, next.kind, next.resource, "+
			"next.namespace, next.name, next.labels, next.annotations, next.owner_refs, next.conditions, "+
			"next.creation_ts, next.resource_version, next.object, "+
			"%s AS projected_object, "+
			"%s AS path, "+
			"%s.depth + 1, "+
			"%s AS visited, "+
			"%d AS level, "+
			"'%s' AS relation_name",
		nextProjExpr,
		recursivePathExpr,
		cteName,
		recursiveVisited,
		levelNum, relationName,
	)

	// Build the CTE body.
	fmt.Fprintf(&cteSB, "%s AS (\n", cteName)

	// Base case: join from root_objects through ancestors to get direct children.
	cteSB.WriteString("  SELECT ")
	cteSB.WriteString(baseCols)
	cteSB.WriteString("\n  FROM root_objects ")
	cteSB.WriteString(rootAlias)
	for _, a := range ancestors {
		cteSB.WriteString(" ")
		cteSB.WriteString(a.join)
	}
	fmt.Fprintf(&cteSB, "\n  JOIN objects %s ON %s AND %s.cluster = %s.cluster",
		childAlias, baseJoinCond, childAlias, parentAlias)

	// Relation filters on base case.
	var filterWhere []string
	var filterArgs []any
	if len(relationSpec.Filters) > 0 {
		for _, f := range relationSpec.Filters {
			if f.GroupKind != nil && f.GroupKind.Kind != "" {
				filterWhere = append(filterWhere, fmt.Sprintf("%s.kind = ?", childAlias))
				filterArgs = append(filterArgs, f.GroupKind.Kind)
			}
			if f.Name != "" {
				filterWhere = append(filterWhere, fmt.Sprintf("%s.name = ?", childAlias))
				filterArgs = append(filterArgs, f.Name)
			}
			if f.Namespace != "" {
				filterWhere = append(filterWhere, fmt.Sprintf("%s.namespace = ?", childAlias))
				filterArgs = append(filterArgs, f.Namespace)
			}
		}
	}

	// Note: for transitive descendants+, we typically don't filter the base case
	// (we want ALL descendants), but filters can narrow results.
	// For simplicity, apply filters only if explicitly provided.

	cteSB.WriteString("\n  UNION ALL\n")

	// Recursive case.
	cteSB.WriteString("  SELECT ")
	cteSB.WriteString(recursiveCols)
	fmt.Fprintf(&cteSB, "\n  FROM objects next\n  JOIN %s ON %s AND next.cluster = %s.cluster",
		cteName, recursiveJoinCond, cteName)
	fmt.Fprintf(&cteSB, "\n  WHERE %s AND %s.depth < %d",
		cycleCheck, cteName, maxDepth)

	cteSB.WriteString("\n)")

	// The SELECT from the CTE for the UNION ALL.
	// Only include object columns + projected_object + path + level + relation_name (no depth/visited).
	selectCols := fmt.Sprintf(
		"%s.id, %s.uid, %s.cluster, %s.api_group, %s.api_version, %s.kind, %s.resource, "+
			"%s.namespace, %s.name, %s.labels, %s.annotations, %s.owner_refs, %s.conditions, "+
			"%s.creation_ts, %s.resource_version, "+
			"%s.projected_object, "+
			"%s.path, "+
			"%s.level, "+
			"%s.relation_name",
		cteName, cteName, cteName, cteName, cteName, cteName, cteName,
		cteName, cteName, cteName, cteName, cteName, cteName,
		cteName, cteName,
		cteName,
		cteName,
		cteName,
		cteName,
	)

	var selectSB strings.Builder
	fmt.Fprintf(&selectSB, "SELECT %s FROM %s", selectCols, cteName)

	// Apply relation-level filters and limit on the final select.
	if len(filterWhere) > 0 {
		selectSB.WriteString(" WHERE ")
		selectSB.WriteString(strings.Join(filterWhere, " AND "))
	}

	if relationSpec.Limit > 0 {
		selectSQL := fmt.Sprintf("SELECT * FROM (%s LIMIT %d)", selectSB.String(), relationSpec.Limit)
		return &transitiveResult{
			cteName:    cteName,
			cteSQL:     cteSB.String(),
			cteArgs:    nil, // args handled via filterArgs on select
			selectSQL:  selectSQL,
			selectArgs: filterArgs,
		}, nil
	}

	return &transitiveResult{
		cteName:    cteName,
		cteSQL:     cteSB.String(),
		cteArgs:    nil,
		selectSQL:  selectSB.String(),
		selectArgs: filterArgs,
	}, nil
}

// descendantJoinCond returns the JOIN condition for descendants (child ownerRefs contains parent UID).
func descendantJoinCond(childAlias, parentAlias, dialect string) string {
	switch dialect {
	case "postgres":
		return fmt.Sprintf("%s.owner_refs @> jsonb_build_array(jsonb_build_object('uid', %s.uid))",
			childAlias, parentAlias)
	default: // sqlite
		return fmt.Sprintf("EXISTS (SELECT 1 FROM json_each(%s.owner_refs) oref WHERE json_extract(oref.value, '$.uid') = %s.uid)",
			childAlias, parentAlias)
	}
}

// ownerJoinCond returns the JOIN condition for owners (parent UID in child's ownerRefs).
func ownerJoinCond(childAlias, parentAlias, dialect string) string {
	switch dialect {
	case "postgres":
		return fmt.Sprintf("%s.uid IN (SELECT ref->>'uid' FROM jsonb_array_elements(%s.owner_refs) ref)",
			childAlias, parentAlias)
	default: // sqlite
		return fmt.Sprintf("%s.uid IN (SELECT json_extract(oref.value, '$.uid') FROM json_each(%s.owner_refs) oref)",
			childAlias, parentAlias)
	}
}

// linkedJoinCond returns the JOIN condition for annotation-based linked relations.
// The parent has a kuery.io/relates-to annotation pointing to the child.
func linkedJoinCond(childAlias, parentAlias, dialect string) string {
	switch dialect {
	case "postgres":
		return fmt.Sprintf(
			"EXISTS (SELECT 1 FROM jsonb_array_elements((%s.object->'metadata'->'annotations'->>'kuery.io/relates-to')::jsonb) ref "+
				"WHERE %s.cluster = COALESCE(ref->>'cluster', %s.cluster) "+
				"AND %s.api_group = COALESCE(ref->>'group', '') "+
				"AND %s.kind = ref->>'kind' "+
				"AND %s.namespace = COALESCE(ref->>'namespace', '') "+
				"AND %s.name = ref->>'name')",
			parentAlias,
			childAlias, parentAlias,
			childAlias,
			childAlias,
			childAlias,
			childAlias)
	default: // sqlite
		return fmt.Sprintf(
			"EXISTS (SELECT 1 FROM json_each(json_extract(%s.annotations, '$.\"kuery.io/relates-to\"')) ref "+
				"WHERE %s.cluster = COALESCE(json_extract(ref.value, '$.cluster'), %s.cluster) "+
				"AND %s.api_group = COALESCE(json_extract(ref.value, '$.group'), '') "+
				"AND %s.kind = json_extract(ref.value, '$.kind') "+
				"AND %s.namespace = COALESCE(json_extract(ref.value, '$.namespace'), '') "+
				"AND %s.name = json_extract(ref.value, '$.name'))",
			parentAlias,
			childAlias, parentAlias,
			childAlias,
			childAlias,
			childAlias,
			childAlias)
	}
}
