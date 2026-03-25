package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/faroshq/kuery/apis/query/v1alpha1"
	"github.com/faroshq/kuery/internal/server"
	"github.com/faroshq/kuery/internal/store"
	kuerysync "github.com/faroshq/kuery/internal/sync"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/server/options"
	"k8s.io/apiserver/pkg/util/compatibility"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	basecompatibility "k8s.io/component-base/compatibility"
	openapicommon "k8s.io/kube-openapi/pkg/common"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/cli"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/cluster"

	genericapiserver "k8s.io/apiserver/pkg/server"
)

// Options holds the configuration for the kuery server.
type Options struct {
	SecureServing *options.SecureServingOptionsWithLoopback

	StoreDriver string
	StoreDSN    string

	// SyncEnabled enables the sync controller to watch clusters.
	SyncEnabled bool

	// Kubeconfigs is a list of name=path pairs for clusters to sync.
	// Example: "cluster-a=/path/to/a.kubeconfig,cluster-b=/path/to/b.kubeconfig"
	Kubeconfigs string

	// SyncBlacklist overrides the default blacklist. Comma-separated resource names.
	// Default: "secrets,events,events.events.k8s.io"
	// Set to empty string to sync everything.
	SyncBlacklist string
}

// NewOptions creates default options.
func NewOptions() *Options {
	o := &Options{
		SecureServing: options.NewSecureServingOptions().WithLoopback(),
		StoreDriver:   "sqlite",
		StoreDSN:      "kuery.db",
		SyncBlacklist: "secrets,events,events.events.k8s.io",
	}
	o.SecureServing.BindPort = 6443
	return o
}

// AddFlags adds flags to the flagset.
func (o *Options) AddFlags(fs *pflag.FlagSet) {
	o.SecureServing.AddFlags(fs)
	fs.StringVar(&o.StoreDriver, "store-driver", o.StoreDriver, "Database driver: sqlite or postgres")
	fs.StringVar(&o.StoreDSN, "store-dsn", o.StoreDSN, "Database connection string")
	fs.BoolVar(&o.SyncEnabled, "sync-enabled", o.SyncEnabled, "Enable sync controller to watch clusters")
	fs.StringVar(&o.Kubeconfigs, "kubeconfigs", o.Kubeconfigs, "Comma-separated list of name=path pairs for clusters to sync (e.g. cluster-a=/path/a.kubeconfig,cluster-b=/path/b.kubeconfig)")
	fs.StringVar(&o.SyncBlacklist, "sync-blacklist", o.SyncBlacklist, "Comma-separated resources to skip syncing (default: secrets,events,events.events.k8s.io). Empty string syncs everything.")
}

// Complete fills in fields required to have valid data.
func (o *Options) Complete() error {
	return nil
}

// Validate checks option values for validity.
func (o *Options) Validate() error {
	switch o.StoreDriver {
	case "sqlite", "postgres":
	default:
		return fmt.Errorf("unsupported store driver: %s", o.StoreDriver)
	}
	return nil
}

// parseKubeconfigs parses the --kubeconfigs flag into a map of name -> path.
func (o *Options) parseKubeconfigs() (map[string]string, error) {
	if o.Kubeconfigs == "" {
		return nil, nil
	}
	result := make(map[string]string)
	for _, entry := range strings.Split(o.Kubeconfigs, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid kubeconfig entry %q: expected name=path", entry)
		}
		name := strings.TrimSpace(parts[0])
		path := strings.TrimSpace(parts[1])
		if name == "" || path == "" {
			return nil, fmt.Errorf("invalid kubeconfig entry %q: name and path must be non-empty", entry)
		}
		result[name] = path
	}
	return result, nil
}

// buildBlacklist creates a Blacklist from the --sync-blacklist flag.
func (o *Options) buildBlacklist() *kuerysync.Blacklist {
	if o.SyncBlacklist == "" {
		return kuerysync.NewBlacklist(nil)
	}
	var gvrs []schema.GroupVersionResource
	for _, entry := range strings.Split(o.SyncBlacklist, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// Format: "resource" or "resource.group"
		parts := strings.SplitN(entry, ".", 2)
		resource := parts[0]
		group := ""
		if len(parts) == 2 {
			group = parts[1]
		}
		gvrs = append(gvrs, schema.GroupVersionResource{Group: group, Resource: resource})
	}
	return kuerysync.NewBlacklist(gvrs)
}

// Run starts the kuery API server.
func (o *Options) Run(ctx context.Context) error {
	logger := klog.FromContext(ctx)

	// Initialize the store.
	s, err := store.NewStore(store.Config{
		Driver: o.StoreDriver,
		DSN:    o.StoreDSN,
	})
	if err != nil {
		return fmt.Errorf("failed to create store: %w", err)
	}
	defer s.Close()

	if err := s.AutoMigrate(); err != nil {
		return fmt.Errorf("failed to auto-migrate: %w", err)
	}

	// Build the generic API server config.
	// Register effective version for the apiserver component (required by k8s.io/apiserver).
	if compatibility.DefaultComponentGlobalsRegistry.EffectiveVersionFor(basecompatibility.DefaultKubeComponent) == nil {
		featureGate := utilfeature.DefaultMutableFeatureGate
		effectiveVersion := compatibility.DefaultBuildEffectiveVersion()
		if err := compatibility.DefaultComponentGlobalsRegistry.Register(basecompatibility.DefaultKubeComponent, effectiveVersion, featureGate); err != nil {
			return fmt.Errorf("failed to register effective version: %w", err)
		}
	}

	recommendedConfig := genericapiserver.NewRecommendedConfig(server.Codecs)
	recommendedConfig.EffectiveVersion = compatibility.DefaultComponentGlobalsRegistry.EffectiveVersionFor(basecompatibility.DefaultKubeComponent)

	// Minimal OpenAPI V3 config (required by SSA/managed fields).
	// Skip installing the OpenAPI handlers (no code-gen), but set the config
	// so the apiserver can create type converters.
	recommendedConfig.SkipOpenAPIInstallation = true
	recommendedConfig.OpenAPIV3Config = &openapicommon.OpenAPIV3Config{
		GetDefinitions: v1alpha1.GetOpenAPIDefinitions,
	}

	if err := o.SecureServing.ApplyTo(&recommendedConfig.SecureServing, &recommendedConfig.LoopbackClientConfig); err != nil {
		return fmt.Errorf("failed to apply secure serving: %w", err)
	}

	// Allow all access in standalone mode. When deployed as an aggregated API
	// server behind kube-apiserver, the parent handles auth.
	recommendedConfig.Authentication.Authenticator = authenticator.RequestFunc(
		func(req *http.Request) (*authenticator.Response, bool, error) {
			return &authenticator.Response{
				User: &user.DefaultInfo{Name: "kuery-user", Groups: []string{"system:masters"}},
			}, true, nil
		})
	recommendedConfig.Authorization.Authorizer = authorizer.AuthorizerFunc(
		func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
			return authorizer.DecisionAllow, "", nil
		})

	// Create sync controller with configurable blacklist.
	blacklist := o.buildBlacklist()
	syncController := kuerysync.NewSyncController(kuerysync.Config{
		Store:     s,
		Blacklist: blacklist,
	})

	// Engage clusters from --kubeconfigs flag.
	kubeconfigs, err := o.parseKubeconfigs()
	if err != nil {
		return fmt.Errorf("failed to parse kubeconfigs: %w", err)
	}

	if len(kubeconfigs) > 0 {
		for name, path := range kubeconfigs {
			go func(name, path string) {
				if err := engageClusterFromKubeconfig(ctx, syncController, name, path); err != nil {
					logger.Error(err, "failed to engage cluster", "cluster", name, "kubeconfig", path)
				} else {
					logger.Info("cluster engaged", "cluster", name)
				}
			}(name, path)
		}
	}

	// Build and start the server.
	serverConfig := &server.KueryServerConfig{
		GenericConfig:  recommendedConfig,
		Store:          s,
		SyncController: syncController,
	}

	kueryServer, err := serverConfig.Complete().New()
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	return kueryServer.GenericAPIServer.PrepareRun().RunWithContext(ctx)
}

// engageClusterFromKubeconfig loads a kubeconfig file, creates a controller-runtime
// cluster, and engages it with the sync controller.
func engageClusterFromKubeconfig(ctx context.Context, sc *kuerysync.SyncController, name, kubeconfigPath string) error {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return fmt.Errorf("loading kubeconfig %s: %w", kubeconfigPath, err)
	}

	// Increase QPS for sync.
	cfg.QPS = 100
	cfg.Burst = 200

	cl, err := cluster.New(cfg)
	if err != nil {
		return fmt.Errorf("creating cluster client for %s: %w", name, err)
	}

	// Start the cluster (cache + informers).
	go func() {
		if err := cl.Start(ctx); err != nil {
			klog.FromContext(ctx).Error(err, "cluster runtime stopped", "cluster", name)
		}
	}()

	// Wait for cache sync.
	if !cl.GetCache().WaitForCacheSync(ctx) {
		return fmt.Errorf("cache sync failed for cluster %s", name)
	}

	// Engage with sync controller.
	return sc.Engage(ctx, name, cl)
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	o := NewOptions()
	cmd := &cobra.Command{
		Use:   "kuery",
		Short: "Kubernetes query API server",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(); err != nil {
				return err
			}
			if err := o.Validate(); err != nil {
				return err
			}
			return o.Run(ctx)
		},
	}
	o.AddFlags(cmd.Flags())

	code := cli.Run(cmd)
	os.Exit(code)
}
