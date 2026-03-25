package engine

import (
	"fmt"
	"strings"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
)

// RelationType identifies the supported relation types (non-transitive).
// Transitive variants (+ suffix) are handled in Phase 5.
const (
	RelOwners     = "owners"
	RelDescendants = "descendants"
	RelReferences  = "references"
	RelSelects     = "selects"
	RelSelectedBy  = "selected-by"
	RelEvents      = "events"
)

// IsTransitive returns true if the relation name ends with "+".
func IsTransitive(name string) bool {
	return strings.HasSuffix(name, "+")
}

// BaseRelation strips the "+" suffix from a relation name.
func BaseRelation(name string) string {
	return strings.TrimSuffix(name, "+")
}

// relationContext carries information needed to generate a relation JOIN.
type relationContext struct {
	parentAlias string // alias of the parent object (e.g., "l0")
	childAlias  string // alias of the child/target object (e.g., "l1")
	dialect     string
	filters     []v1alpha1.ObjectFilter // relation-level filters
	refRegistry *RefPathRegistry
}

// buildRelationJoin generates the JOIN clause and additional WHERE conditions
// for a given relation type. Returns (joinClause, extraWhere, args).
func buildRelationJoin(relationType string, ctx relationContext) (string, []string, []any) {
	switch relationType {
	case RelDescendants:
		return buildDescendantJoin(ctx)
	case RelOwners:
		return buildOwnerJoin(ctx)
	case RelReferences:
		return buildReferenceJoin(ctx)
	case RelSelects:
		return buildSelectsJoin(ctx)
	case RelSelectedBy:
		return buildSelectedByJoin(ctx)
	case RelEvents:
		return buildEventsJoin(ctx)
	default:
		// Unknown relation type — return empty (will produce no results).
		return fmt.Sprintf("JOIN objects %s ON 1=0", ctx.childAlias), nil, nil
	}
}

// buildDescendantJoin: parent → children via ownerRefs.
// Children whose ownerRefs contain the parent's UID.
func buildDescendantJoin(ctx relationContext) (string, []string, []any) {
	var join string
	switch ctx.dialect {
	case "postgres":
		join = fmt.Sprintf(
			"JOIN objects %s ON %s.cluster = %s.cluster "+
				"AND %s.owner_refs @> jsonb_build_array(jsonb_build_object('uid', %s.uid))",
			ctx.childAlias, ctx.childAlias, ctx.parentAlias,
			ctx.childAlias, ctx.parentAlias)
	default: // sqlite
		join = fmt.Sprintf(
			"JOIN objects %s ON %s.cluster = %s.cluster "+
				"AND EXISTS (SELECT 1 FROM json_each(%s.owner_refs) oref WHERE json_extract(oref.value, '$.uid') = %s.uid)",
			ctx.childAlias, ctx.childAlias, ctx.parentAlias,
			ctx.childAlias, ctx.parentAlias)
	}
	return join, nil, nil
}

// buildOwnerJoin: child → parent via ownerRefs.
// Parents whose UID appears in the child's ownerRefs.
func buildOwnerJoin(ctx relationContext) (string, []string, []any) {
	var join string
	switch ctx.dialect {
	case "postgres":
		join = fmt.Sprintf(
			"JOIN objects %s ON %s.cluster = %s.cluster "+
				"AND %s.uid IN (SELECT ref->>'uid' FROM jsonb_array_elements(%s.owner_refs) ref)",
			ctx.childAlias, ctx.childAlias, ctx.parentAlias,
			ctx.childAlias, ctx.parentAlias)
	default: // sqlite
		join = fmt.Sprintf(
			"JOIN objects %s ON %s.cluster = %s.cluster "+
				"AND %s.uid IN (SELECT json_extract(oref.value, '$.uid') FROM json_each(%s.owner_refs) oref)",
			ctx.childAlias, ctx.childAlias, ctx.parentAlias,
			ctx.childAlias, ctx.parentAlias)
	}
	return join, nil, nil
}

// buildReferenceJoin: source → target via spec field references (ref-path registry).
// Uses the ref-path registry to find which fields in the source reference which target kinds.
func buildReferenceJoin(ctx relationContext) (string, []string, []any) {
	if ctx.refRegistry == nil {
		return fmt.Sprintf("JOIN objects %s ON 1=0", ctx.childAlias), nil, nil
	}

	// Determine target kind filter from relation filters.
	var targetKinds []string
	for _, f := range ctx.filters {
		if f.GroupKind != nil && f.GroupKind.Kind != "" {
			targetKinds = append(targetKinds, f.GroupKind.Kind)
		}
	}

	// Build a UNION of all ref-path subqueries that match the target kinds.
	// If no target kind filter, use all ref paths from all source kinds.
	// Since we don't know the source kind at SQL-generation time (it's dynamic),
	// we generate a broad match using CASE WHEN on the parent's kind.
	//
	// For simplicity, we generate the join condition as:
	// target.name IN (ref_subquery) AND target.cluster = parent.cluster AND target.namespace = parent.namespace
	// with a CASE on parent.kind to select the right ref paths.

	// Collect ref paths and build name lookup subqueries.
	allPaths := BuiltinRefPaths
	var nameSubqueries []string

	for _, rp := range allPaths {
		if len(targetKinds) > 0 {
			found := false
			for _, tk := range targetKinds {
				if strings.EqualFold(rp.TargetKind, tk) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Each ref subquery returns a set of names.
		// Strip the outer parentheses from BuildRefNameSubquery since we'll use it in UNION ALL.
		subq := rp.BuildRefNameSubquery(ctx.parentAlias, ctx.dialect)
		// Remove surrounding parens: "(SELECT ...)" → "SELECT ..."
		subq = strings.TrimPrefix(subq, "(")
		subq = strings.TrimSuffix(subq, ")")
		nameSubqueries = append(nameSubqueries, subq)
	}

	if len(nameSubqueries) == 0 {
		return fmt.Sprintf("JOIN objects %s ON 1=0", ctx.childAlias), nil, nil
	}

	combinedSubquery := strings.Join(nameSubqueries, " UNION ALL ")

	join := fmt.Sprintf(
		"JOIN objects %s ON %s.cluster = %s.cluster "+
			"AND %s.namespace = %s.namespace "+
			"AND %s.name IN (%s)",
		ctx.childAlias, ctx.childAlias, ctx.parentAlias,
		ctx.childAlias, ctx.parentAlias,
		ctx.childAlias, combinedSubquery)

	// Add target kind filter.
	var extraWhere []string
	if len(targetKinds) > 0 {
		placeholders := make([]string, len(targetKinds))
		var args []any
		for i, tk := range targetKinds {
			placeholders[i] = "?"
			args = append(args, tk)
		}
		extraWhere = append(extraWhere,
			fmt.Sprintf("%s.kind IN (%s)", ctx.childAlias, strings.Join(placeholders, ",")))
		return join, extraWhere, args
	}

	return join, nil, nil
}

// buildSelectsJoin: selector holder → matched objects.
// The parent holds a selector (spec.selector.matchLabels), find targets whose labels match.
func buildSelectsJoin(ctx relationContext) (string, []string, []any) {
	var join string
	switch ctx.dialect {
	case "postgres":
		join = fmt.Sprintf(
			"JOIN objects %s ON %s.cluster = %s.cluster "+
				"AND %s.namespace = %s.namespace "+
				"AND %s.labels @> (%s.object->'spec'->'selector'->'matchLabels') "+
				"AND %s.object->'spec'->'selector'->'matchLabels' IS NOT NULL "+
				"AND %s.id != %s.id",
			ctx.childAlias, ctx.childAlias, ctx.parentAlias,
			ctx.childAlias, ctx.parentAlias,
			ctx.childAlias, ctx.parentAlias,
			ctx.parentAlias,
			ctx.childAlias, ctx.parentAlias)
	default: // sqlite
		// For SQLite, check every key-value in parent's matchLabels exists in target's labels.
		join = fmt.Sprintf(
			"JOIN objects %s ON %s.cluster = %s.cluster "+
				"AND %s.namespace = %s.namespace "+
				"AND json_extract(%s.object, '$.spec.selector.matchLabels') IS NOT NULL "+
				"AND %s.id != %s.id "+
				"AND NOT EXISTS ("+
				"SELECT 1 FROM json_each(json_extract(%s.object, '$.spec.selector.matchLabels')) sel "+
				"WHERE json_extract(%s.labels, '$.' || sel.key) IS NULL "+
				"OR json_extract(%s.labels, '$.' || sel.key) != sel.value"+
				")",
			ctx.childAlias, ctx.childAlias, ctx.parentAlias,
			ctx.childAlias, ctx.parentAlias,
			ctx.parentAlias,
			ctx.childAlias, ctx.parentAlias,
			ctx.parentAlias,
			ctx.childAlias,
			ctx.childAlias)
	}
	return join, nil, nil
}

// buildSelectedByJoin: matched → selector holder (reverse of selects).
// The child holds a selector, parent's labels match it.
func buildSelectedByJoin(ctx relationContext) (string, []string, []any) {
	var join string
	switch ctx.dialect {
	case "postgres":
		join = fmt.Sprintf(
			"JOIN objects %s ON %s.cluster = %s.cluster "+
				"AND %s.namespace = %s.namespace "+
				"AND %s.labels @> (%s.object->'spec'->'selector'->'matchLabels') "+
				"AND %s.object->'spec'->'selector'->'matchLabels' IS NOT NULL "+
				"AND %s.id != %s.id",
			ctx.childAlias, ctx.childAlias, ctx.parentAlias,
			ctx.childAlias, ctx.parentAlias,
			ctx.parentAlias, ctx.childAlias,
			ctx.childAlias,
			ctx.childAlias, ctx.parentAlias)
	default: // sqlite
		join = fmt.Sprintf(
			"JOIN objects %s ON %s.cluster = %s.cluster "+
				"AND %s.namespace = %s.namespace "+
				"AND json_extract(%s.object, '$.spec.selector.matchLabels') IS NOT NULL "+
				"AND %s.id != %s.id "+
				"AND NOT EXISTS ("+
				"SELECT 1 FROM json_each(json_extract(%s.object, '$.spec.selector.matchLabels')) sel "+
				"WHERE json_extract(%s.labels, '$.' || sel.key) IS NULL "+
				"OR json_extract(%s.labels, '$.' || sel.key) != sel.value"+
				")",
			ctx.childAlias, ctx.childAlias, ctx.parentAlias,
			ctx.childAlias, ctx.parentAlias,
			ctx.childAlias,
			ctx.childAlias, ctx.parentAlias,
			ctx.childAlias,
			ctx.parentAlias,
			ctx.parentAlias)
	}
	return join, nil, nil
}

// buildEventsJoin: object → events matching involvedObject.uid.
func buildEventsJoin(ctx relationContext) (string, []string, []any) {
	var join string
	switch ctx.dialect {
	case "postgres":
		join = fmt.Sprintf(
			"JOIN objects %s ON %s.cluster = %s.cluster "+
				"AND %s.kind = 'Event' "+
				"AND %s.object->'involvedObject'->>'uid' = %s.uid",
			ctx.childAlias, ctx.childAlias, ctx.parentAlias,
			ctx.childAlias,
			ctx.childAlias, ctx.parentAlias)
	default: // sqlite
		join = fmt.Sprintf(
			"JOIN objects %s ON %s.cluster = %s.cluster "+
				"AND %s.kind = 'Event' "+
				"AND json_extract(%s.object, '$.involvedObject.uid') = %s.uid",
			ctx.childAlias, ctx.childAlias, ctx.parentAlias,
			ctx.childAlias,
			ctx.childAlias, ctx.parentAlias)
	}
	return join, nil, nil
}
