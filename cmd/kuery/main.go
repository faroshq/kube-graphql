package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/faroshq/kuery/internal/server"
	"github.com/faroshq/kuery/internal/store"
	kuerysync "github.com/faroshq/kuery/internal/sync"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/apiserver/pkg/server/options"
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
}

// NewOptions creates default options.
func NewOptions() *Options {
	o := &Options{
		SecureServing: options.NewSecureServingOptions().WithLoopback(),
		StoreDriver:   "sqlite",
		StoreDSN:      "kuery.db",
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
	recommendedConfig := genericapiserver.NewRecommendedConfig(server.Codecs)

	if err := o.SecureServing.ApplyTo(&recommendedConfig.SecureServing, &recommendedConfig.LoopbackClientConfig); err != nil {
		return fmt.Errorf("failed to apply secure serving: %w", err)
	}

	// Create sync controller.
	syncController := kuerysync.NewSyncController(kuerysync.Config{
		Store:     s,
		Blacklist: kuerysync.NewBlacklist(kuerysync.DefaultBlacklist),
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
