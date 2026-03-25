package server

import (
	"github.com/faroshq/kuery/apis/query/v1alpha1"
	"github.com/faroshq/kuery/internal/store"
	kuerysync "github.com/faroshq/kuery/internal/sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
)

var (
	// Scheme is the runtime scheme for the kuery API server.
	Scheme = runtime.NewScheme()

	// Codecs provides access to encoding and decoding for the scheme.
	Codecs = serializer.NewCodecFactory(Scheme)

	// ParameterCodec handles query parameter conversion.
	ParameterCodec = runtime.NewParameterCodec(Scheme)
)

func init() {
	_ = v1alpha1.AddToScheme(Scheme)
	_ = metav1.AddMetaToScheme(Scheme)
}

// KueryServerConfig holds configuration for the kuery API server.
type KueryServerConfig struct {
	GenericConfig  *genericapiserver.RecommendedConfig
	Store          store.Store
	SyncController *kuerysync.SyncController
}

// KueryServer contains the state for the kuery API server.
type KueryServer struct {
	GenericAPIServer *genericapiserver.GenericAPIServer
}

// Complete fills in any fields not set that are required to have valid data.
func (c *KueryServerConfig) Complete() *CompletedConfig {
	return &CompletedConfig{
		GenericConfig:  c.GenericConfig.Complete(),
		Store:          c.Store,
		SyncController: c.SyncController,
	}
}

// CompletedConfig holds completed configuration for the kuery API server.
type CompletedConfig struct {
	GenericConfig  genericapiserver.CompletedConfig
	Store          store.Store
	SyncController *kuerysync.SyncController
}

// New creates a new kuery API server.
func (c *CompletedConfig) New() (*KueryServer, error) {
	genericServer, err := c.GenericConfig.New("kuery-apiserver", genericapiserver.NewEmptyDelegate())
	if err != nil {
		return nil, err
	}

	// Build API group info for kuery.io/v1alpha1.
	apiGroupInfo := genericapiserver.NewDefaultAPIGroupInfo("kuery.io", Scheme, ParameterCodec, Codecs)

	queryStorage := NewQueryREST(c.Store)
	apiGroupInfo.VersionedResourcesStorageMap["v1alpha1"] = map[string]rest.Storage{
		"queries": queryStorage,
	}

	if err := genericServer.InstallAPIGroup(&apiGroupInfo); err != nil {
		return nil, err
	}

	return &KueryServer{GenericAPIServer: genericServer}, nil
}
