package server

import (
	"context"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
	"github.com/faroshq/kuery/internal/engine"
	"github.com/faroshq/kuery/internal/store"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/rest"
)

// QueryREST implements the REST storage for the Query resource.
// It only supports Create (POST) — no GET, LIST, UPDATE, DELETE.
type QueryREST struct {
	store  store.Store
	engine *engine.Engine
}

// NewQueryREST creates a new QueryREST handler.
func NewQueryREST(s store.Store) *QueryREST {
	return &QueryREST{
		store:  s,
		engine: engine.NewEngine(s),
	}
}

var _ rest.Storage = &QueryREST{}
var _ rest.Creater = &QueryREST{}
var _ rest.Scoper = &QueryREST{}
var _ rest.SingularNameProvider = &QueryREST{}

// New returns a new instance of the Query object.
func (r *QueryREST) New() runtime.Object {
	return &v1alpha1.Query{}
}

// Destroy cleans up resources on shutdown.
func (r *QueryREST) Destroy() {}

// NamespaceScoped returns false — Query is cluster-scoped.
func (r *QueryREST) NamespaceScoped() bool { return false }

// GetSingularName returns the singular name of the resource.
func (r *QueryREST) GetSingularName() string { return "query" }

// Create handles POST requests. It executes the query and returns the result inline.
func (r *QueryREST) Create(ctx context.Context, obj runtime.Object, createValidation rest.ValidateObjectFunc, options *metav1.CreateOptions) (runtime.Object, error) {
	query := obj.(*v1alpha1.Query)

	if createValidation != nil {
		if err := createValidation(ctx, obj); err != nil {
			return nil, err
		}
	}

	status, err := r.engine.Execute(ctx, &query.Spec)
	if err != nil {
		return nil, err
	}

	query.Status = *status
	return query, nil
}
