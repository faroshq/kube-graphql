package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
	"github.com/faroshq/kuery/internal/store"

	"k8s.io/apimachinery/pkg/runtime"
)

// Engine orchestrates query execution: validate → generate SQL → execute → assemble response.
type Engine struct {
	store     store.Store
	generator *Generator
}

// NewEngine creates a new query engine.
func NewEngine(s store.Store) *Engine {
	return &Engine{
		store:     s,
		generator: NewGenerator(s.Driver()),
	}
}

// Execute runs a query and returns the populated QueryStatus.
func (e *Engine) Execute(ctx context.Context, spec *v1alpha1.QuerySpec) (*v1alpha1.QueryStatus, error) {
	// 1. Validate and apply defaults.
	if err := Validate(spec); err != nil {
		return nil, fmt.Errorf("validation: %w", err)
	}

	// 2. Apply query timeout.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(DefaultQueryTimeout)*time.Second)
	defer cancel()

	// 3. Generate SQL.
	gen, err := e.generator.Generate(spec)
	if err != nil {
		return nil, fmt.Errorf("sql generation: %w", err)
	}

	// 4. Execute main query.
	rows, err := e.store.RawDB().WithContext(ctx).Raw(gen.SQL, gen.Args...).Rows()
	if err != nil {
		return nil, fmt.Errorf("query execution: %w", err)
	}
	defer rows.Close()

	// 5. Scan rows into results.
	results, lastRow, err := e.scanRows(rows, spec)
	if err != nil {
		return nil, fmt.Errorf("scanning results: %w", err)
	}

	status := &v1alpha1.QueryStatus{
		Objects:  results,
		Warnings: []string{},
	}

	// 6. Count query if requested.
	if spec.Count {
		var count int64
		if err := e.store.RawDB().WithContext(ctx).Raw(gen.CountSQL, gen.CountArgs...).Scan(&count).Error; err != nil {
			return nil, fmt.Errorf("count query: %w", err)
		}
		status.Count = &count
	}

	// 7. Build cursor if requested.
	if spec.Cursor && lastRow != nil {
		status.Cursor = &v1alpha1.CursorResult{
			Next:     BuildCursorToken(lastRow),
			PageSize: spec.Limit,
		}
		if spec.Page != nil {
			status.Cursor.Page = spec.Page.First / spec.Limit
		}
	}

	// 8. Check if incomplete.
	if len(results) == int(spec.Limit) {
		status.Incomplete = true
	}

	return status, nil
}

// rowResult holds scanned values from a single result row.
type rowResult struct {
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
}

// scanRows reads query result rows and builds ObjectResult slice.
func (e *Engine) scanRows(rows *sql.Rows, spec *v1alpha1.QuerySpec) ([]v1alpha1.ObjectResult, map[string]string, error) {
	var results []v1alpha1.ObjectResult
	var lastRow map[string]string

	for rows.Next() {
		var r rowResult
		if err := rows.Scan(
			&r.ID, &r.UID, &r.Cluster, &r.APIGroup, &r.APIVersion,
			&r.Kind, &r.Resource, &r.Namespace, &r.Name,
			&r.Labels, &r.Annotations, &r.OwnerRefs, &r.Conditions,
			&r.CreationTS, &r.ResourceVersion,
			&r.ProjectedObject, &r.Path,
		); err != nil {
			return nil, nil, fmt.Errorf("scanning row: %w", err)
		}

		result := v1alpha1.ObjectResult{}

		// Include fields based on spec.
		if spec.Objects != nil {
			if spec.Objects.ID {
				result.ID = r.ID
			}
			if spec.Objects.Cluster {
				result.Cluster = r.Cluster
			}
			if spec.Objects.MutablePath {
				result.MutablePath = MutablePath(r.APIGroup, r.APIVersion, r.Resource, r.Namespace, r.Name)
			}
		}

		// Projected object.
		if r.ProjectedObject.Valid && r.ProjectedObject.String != "" {
			raw := json.RawMessage(r.ProjectedObject.String)
			result.Object = &runtime.RawExtension{Raw: raw}
		}

		results = append(results, result)

		// Track last row for cursor.
		lastRow = map[string]string{
			"name":              r.Name,
			"namespace":         r.Namespace,
			"kind":              r.Kind,
			"apiGroup":          r.APIGroup,
			"cluster":           r.Cluster,
		}
		if r.CreationTS.Valid {
			lastRow["creationTimestamp"] = r.CreationTS.Time.Format(time.RFC3339)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	return results, lastRow, nil
}
