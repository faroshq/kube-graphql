package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
	"github.com/faroshq/kuery/internal/metrics"
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
	start := time.Now()

	// 1. Validate and apply defaults.
	if err := Validate(spec); err != nil {
		metrics.QueryErrors.WithLabelValues("validation").Inc()
		return nil, fmt.Errorf("validation: %w", err)
	}

	// 2. Apply query timeout.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(DefaultQueryTimeout)*time.Second)
	defer cancel()

	// 3. Generate SQL.
	gen, err := e.generator.Generate(spec)
	if err != nil {
		metrics.QueryErrors.WithLabelValues("generation").Inc()
		return nil, fmt.Errorf("sql generation: %w", err)
	}

	// 4. Execute query.
	rows, err := e.store.RawDB().WithContext(ctx).Raw(gen.SQL, gen.Args...).Rows()
	if err != nil {
		metrics.QueryErrors.WithLabelValues("execution").Inc()
		return nil, fmt.Errorf("query execution: %w", err)
	}
	defer rows.Close()

	// 5. Scan and assemble results.
	var status *v1alpha1.QueryStatus
	if gen.HasRelations {
		status, err = e.executeWithRelations(ctx, rows, spec, gen)
	} else {
		status, err = e.executeFlat(ctx, rows, spec, gen)
	}
	if err != nil {
		metrics.QueryErrors.WithLabelValues("assembly").Inc()
		return nil, err
	}

	// 6. Record metrics.
	elapsed := time.Since(start)
	metrics.QueryDuration.WithLabelValues(
		strconv.FormatBool(gen.HasRelations),
		strconv.FormatBool(status.Incomplete),
	).Observe(elapsed.Seconds())

	return status, nil
}

// executeFlat handles queries without relations (Phase 3 path).
func (e *Engine) executeFlat(ctx context.Context, rows *sql.Rows, spec *v1alpha1.QuerySpec, gen *GeneratedQuery) (*v1alpha1.QueryStatus, error) {
	flatRows, truncated, err := scanFlatRows(rows, MaxTotalRows)
	if err != nil {
		return nil, fmt.Errorf("scanning results: %w", err)
	}

	var results []v1alpha1.ObjectResult
	for _, r := range flatRows {
		results = append(results, rowToResult(r, spec.Objects))
	}

	status := &v1alpha1.QueryStatus{
		Objects:  results,
		Warnings: []string{},
	}

	if truncated {
		status.Incomplete = true
		status.Warnings = append(status.Warnings, fmt.Sprintf("response truncated at %d total rows", MaxTotalRows))
	}

	if spec.Count {
		var count int64
		if err := e.store.RawDB().WithContext(ctx).Raw(gen.CountSQL, gen.CountArgs...).Scan(&count).Error; err != nil {
			return nil, fmt.Errorf("count query: %w", err)
		}
		status.Count = &count
	}

	lastRow := ExtractLastRowForCursor(flatRows)
	if spec.Cursor && lastRow != nil {
		status.Cursor = &v1alpha1.CursorResult{
			Next:     BuildCursorToken(lastRow),
			PageSize: spec.Limit,
		}
		if spec.Page != nil {
			status.Cursor.Page = spec.Page.First / spec.Limit
		}
	}

	if !truncated && len(results) == int(spec.Limit) {
		status.Incomplete = true
	}

	return status, nil
}

// executeWithRelations handles queries with relations using tree assembly.
func (e *Engine) executeWithRelations(ctx context.Context, rows *sql.Rows, spec *v1alpha1.QuerySpec, gen *GeneratedQuery) (*v1alpha1.QueryStatus, error) {
	flatRows, truncated, err := scanFlatRows(rows, MaxTotalRows)
	if err != nil {
		return nil, fmt.Errorf("scanning results: %w", err)
	}

	results := AssembleTree(flatRows, spec)

	status := &v1alpha1.QueryStatus{
		Objects:  results,
		Warnings: []string{},
	}

	if truncated {
		status.Incomplete = true
		status.Warnings = append(status.Warnings, fmt.Sprintf("response truncated at %d total rows", MaxTotalRows))
	}

	// Count (root objects only).
	if spec.Count {
		var count int64
		if err := e.store.RawDB().WithContext(ctx).Raw(gen.CountSQL, gen.CountArgs...).Scan(&count).Error; err != nil {
			return nil, fmt.Errorf("count query: %w", err)
		}
		status.Count = &count
	}

	// Cursor.
	lastRow := ExtractLastRowForCursor(flatRows)
	if spec.Cursor && lastRow != nil {
		status.Cursor = &v1alpha1.CursorResult{
			Next:     BuildCursorToken(lastRow),
			PageSize: spec.Limit,
		}
		if spec.Page != nil {
			status.Cursor.Page = spec.Page.First / spec.Limit
		}
	}

	// Incomplete — count root objects.
	rootCount := 0
	for _, r := range flatRows {
		if r.Level == 0 {
			rootCount++
		}
	}
	if rootCount == int(spec.Limit) {
		status.Incomplete = true
	}

	return status, nil
}

// rowToResult converts a flatRow to an ObjectResult (for flat queries).
func rowToResult(r flatRow, objSpec *v1alpha1.ObjectsSpec) v1alpha1.ObjectResult {
	result := v1alpha1.ObjectResult{}
	if objSpec != nil {
		if objSpec.ID {
			result.ID = r.ID
		}
		if objSpec.Cluster {
			result.Cluster = r.Cluster
		}
		if objSpec.MutablePath {
			result.MutablePath = MutablePath(r.APIGroup, r.APIVersion, r.Resource, r.Namespace, r.Name)
		}
	}

	if r.ProjectedObject.Valid && r.ProjectedObject.String != "" && r.ProjectedObject.String != "null" {
		raw := json.RawMessage(r.ProjectedObject.String)
		result.Object = &runtime.RawExtension{Raw: raw}
	}

	return result
}
