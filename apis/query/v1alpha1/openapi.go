package v1alpha1

import (
	"k8s.io/kube-openapi/pkg/common"
	"k8s.io/kube-openapi/pkg/validation/spec"
)

// GetOpenAPIDefinitions provides minimal OpenAPI definitions for kuery types.
// This avoids running full code-gen while satisfying the apiserver's SSA requirement.
func GetOpenAPIDefinitions(ref common.ReferenceCallback) map[string]common.OpenAPIDefinition {
	return map[string]common.OpenAPIDefinition{
		"github.com/faroshq/kuery/apis/query/v1alpha1.Query": {
			Schema: spec.Schema{
				SchemaProps: spec.SchemaProps{
					Description: "Query is a POST-only virtual resource for executing queries.",
					Type:        []string{"object"},
					Properties: map[string]spec.Schema{
						"apiVersion": {SchemaProps: spec.SchemaProps{Type: []string{"string"}}},
						"kind":       {SchemaProps: spec.SchemaProps{Type: []string{"string"}}},
						"metadata":   {SchemaProps: spec.SchemaProps{Type: []string{"object"}}},
						"spec":       {SchemaProps: spec.SchemaProps{Type: []string{"object"}}},
						"status":     {SchemaProps: spec.SchemaProps{Type: []string{"object"}}},
					},
				},
			},
		},
	}
}
