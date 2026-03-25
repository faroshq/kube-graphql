package engine

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
)

// flatRow represents a single row from the UNION ALL query with relation metadata.
type flatRow struct {
	ID              string
	UID             string
	Cluster         string
	APIGroup        string
	APIVersion      string
	Kind            string
	Resource        string
	Namespace       string
	Name            string
	Labels          sql.NullString
	Annotations     sql.NullString
	OwnerRefs       sql.NullString
	Conditions      sql.NullString
	CreationTS      sql.NullTime
	ResourceVersion string
	ProjectedObject sql.NullString
	Path            string
	Level           int
	RelationName    string
}

// scanFlatRows scans rows from a UNION ALL query into flatRow slices.
// If maxRows > 0, stops scanning after maxRows and returns truncated=true.
func scanFlatRows(rows *sql.Rows, maxRows int) ([]flatRow, bool, error) {
	var result []flatRow
	truncated := false
	for rows.Next() {
		if maxRows > 0 && len(result) >= maxRows {
			truncated = true
			break
		}
		var r flatRow
		if err := rows.Scan(
			&r.ID, &r.UID, &r.Cluster, &r.APIGroup, &r.APIVersion,
			&r.Kind, &r.Resource, &r.Namespace, &r.Name,
			&r.Labels, &r.Annotations, &r.OwnerRefs, &r.Conditions,
			&r.CreationTS, &r.ResourceVersion,
			&r.ProjectedObject, &r.Path,
			&r.Level, &r.RelationName,
		); err != nil {
			return nil, false, fmt.Errorf("scanning flat row: %w", err)
		}
		result = append(result, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return result, truncated, nil
}

// treeNode is an internal node used during tree construction.
type treeNode struct {
	row      flatRow
	children map[string][]*treeNode // keyed by relation name
}

// AssembleTree reconstructs a nested ObjectResult tree from flat rows.
// The rows must be sorted by path. The path encodes the hierarchical position:
//
//	Level 0: .deployment.default/nginx
//	Level 1: .deployment.default/nginx.replicaset.default/nginx-abc
//	Level 2: .deployment.default/nginx.replicaset.default/nginx-abc.pod.default/nginx-pod
//
// Each level adds 2 path segments (kind + ns/name).
func AssembleTree(flatRows []flatRow, spec *v1alpha1.QuerySpec) []v1alpha1.ObjectResult {
	if len(flatRows) == 0 {
		return nil
	}

	// Build lookup: path → treeNode.
	nodesByPath := make(map[string]*treeNode)
	var rootNodes []*treeNode

	for i := range flatRows {
		row := flatRows[i]
		node := &treeNode{
			row:      row,
			children: make(map[string][]*treeNode),
		}
		nodesByPath[row.Path] = node

		if row.Level == 0 {
			rootNodes = append(rootNodes, node)
		} else {
			// Find parent path by stripping the last 2 segments.
			parentPath := parentPathOf(row.Path)
			if parent, ok := nodesByPath[parentPath]; ok {
				parent.children[row.RelationName] = append(parent.children[row.RelationName], node)
			}
			// If parent not found, row is orphaned (shouldn't happen with proper SQL).
		}
	}

	// Convert treeNodes to ObjectResult.
	results := make([]v1alpha1.ObjectResult, 0, len(rootNodes))
	for _, node := range rootNodes {
		results = append(results, nodeToResult(node, spec, objectsSpecForLevel(spec, 0, "")))
	}
	return results
}

// parentPathOf strips the last 2 segments (.<kind>.<ns/name>) from a path.
func parentPathOf(path string) string {
	// Path format: .kind1.ns1/name1.kind2.ns2/name2...
	// Split by '.', remove last 2 non-empty segments.
	parts := strings.Split(path, ".")
	// First element is empty (leading dot), so parts = ["", "kind1", "ns1/name1", "kind2", "ns2/name2", ...]
	if len(parts) <= 3 {
		return "" // already at root or invalid
	}
	// Remove last 2.
	parentParts := parts[:len(parts)-2]
	return strings.Join(parentParts, ".")
}

// objectsSpecForLevel finds the ObjectsSpec for a given level and relation name
// by walking the spec tree.
func objectsSpecForLevel(spec *v1alpha1.QuerySpec, level int, relationName string) *v1alpha1.ObjectsSpec {
	if level == 0 {
		return spec.Objects
	}
	// For nested levels, the caller should pass the correct spec.
	// This is a simplified version; the full version would walk the spec tree.
	return spec.Objects
}

// nodeToResult converts a treeNode into an ObjectResult.
func nodeToResult(node *treeNode, spec *v1alpha1.QuerySpec, objSpec *v1alpha1.ObjectsSpec) v1alpha1.ObjectResult {
	result := v1alpha1.ObjectResult{}
	row := node.row

	if objSpec != nil {
		if objSpec.ID {
			result.ID = row.ID
		}
		if objSpec.Cluster {
			result.Cluster = row.Cluster
		}
		if objSpec.MutablePath {
			result.MutablePath = MutablePath(row.APIGroup, row.APIVersion, row.Resource, row.Namespace, row.Name)
		}
	}

	// Projected object.
	if row.ProjectedObject.Valid && row.ProjectedObject.String != "" && row.ProjectedObject.String != "null" {
		raw := json.RawMessage(row.ProjectedObject.String)
		result.Object = &runtime.RawExtension{Raw: raw}
	}

	// Recursively process children.
	if len(node.children) > 0 {
		result.Relations = make(map[string][]v1alpha1.ObjectResult)
		for relName, children := range node.children {
			childObjSpec := findRelationObjectsSpec(objSpec, relName)
			childResults := make([]v1alpha1.ObjectResult, 0, len(children))
			for _, child := range children {
				childResults = append(childResults, nodeToResult(child, spec, childObjSpec))
			}
			result.Relations[relName] = childResults
		}
	}

	return result
}

// findRelationObjectsSpec finds the ObjectsSpec for a named relation.
func findRelationObjectsSpec(parentSpec *v1alpha1.ObjectsSpec, relationName string) *v1alpha1.ObjectsSpec {
	if parentSpec == nil || parentSpec.Relations == nil {
		return nil
	}
	if rel, ok := parentSpec.Relations[relationName]; ok {
		return rel.Objects
	}
	return nil
}

// ExtractLastRowForCursor extracts cursor data from root-level rows.
func ExtractLastRowForCursor(flatRows []flatRow) map[string]string {
	for i := len(flatRows) - 1; i >= 0; i-- {
		if flatRows[i].Level == 0 {
			r := flatRows[i]
			m := map[string]string{
				"name":      r.Name,
				"namespace": r.Namespace,
				"kind":      r.Kind,
				"apiGroup":  r.APIGroup,
				"cluster":   r.Cluster,
			}
			if r.CreationTS.Valid {
				m["creationTimestamp"] = r.CreationTS.Time.Format(time.RFC3339)
			}
			return m
		}
	}
	return nil
}
