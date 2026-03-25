package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/faroshq/kuery/internal/server"
	"github.com/faroshq/kuery/internal/store"
	kuerysync "github.com/faroshq/kuery/internal/sync"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"k8s.io/apiserver/pkg/server/options"
	"k8s.io/component-base/cli"

	genericapiserver "k8s.io/apiserver/pkg/server"
)

// Options holds the configuration for the kuery server.
type Options struct {
	SecureServing *options.SecureServingOptionsWithLoopback

	StoreDriver string
	StoreDSN    string

	// SyncEnabled enables the sync controller to watch clusters.
	SyncEnabled bool
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

// Run starts the kuery API server.
func (o *Options) Run(ctx context.Context) error {
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

	// Create sync controller (available for cluster engagement).
	syncController := kuerysync.NewSyncController(kuerysync.Config{
		Store:     s,
		Blacklist: kuerysync.NewBlacklist(kuerysync.DefaultBlacklist),
	})

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
