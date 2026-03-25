package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Config holds database connection configuration.
type Config struct {
	Driver string // "sqlite" or "postgres"
	DSN    string // connection string
}

// Store is the interface for database operations.
type Store interface {
	// AutoMigrate creates/updates database tables.
	AutoMigrate() error

	// Object operations.
	UpsertObject(ctx context.Context, obj *ObjectModel) error
	DeleteObject(ctx context.Context, cluster, apiGroup, kind, namespace, name string) error
	GetObject(ctx context.Context, id uuid.UUID) (*ObjectModel, error)

	// ResourceType operations.
	UpsertResourceType(ctx context.Context, rt *ResourceTypeModel) error
	DeleteResourceTypesForCluster(ctx context.Context, cluster string) error

	// Cluster operations.
	UpsertCluster(ctx context.Context, c *ClusterModel) error
	GetCluster(ctx context.Context, name string) (*ClusterModel, error)

	// RawDB exposes the underlying *gorm.DB for the query engine.
	RawDB() *gorm.DB

	// Driver returns the database driver name ("sqlite" or "postgres").
	Driver() string

	// GC operations.
	ListStaleClusters(ctx context.Context, expiredBefore time.Time) ([]ClusterModel, error)
	DeleteCluster(ctx context.Context, name string) error
	DeleteObjectsForCluster(ctx context.Context, cluster string) error

	// Close closes the database connection.
	Close() error
}

type store struct {
	db     *gorm.DB
	driver string
}

// NewStore creates a new Store based on the given config.
func NewStore(cfg Config) (Store, error) {
	var db *gorm.DB
	var err error

	switch cfg.Driver {
	case "sqlite":
		db, err = newSQLiteDB(cfg.DSN)
	case "postgres":
		db, err = newPostgresDB(cfg.DSN)
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", cfg.Driver)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	return &store{db: db, driver: cfg.Driver}, nil
}

func (s *store) AutoMigrate() error {
	err := s.db.AutoMigrate(
		&ObjectModel{},
		&ResourceTypeModel{},
		&ClusterModel{},
	)
	if err != nil {
		return fmt.Errorf("auto-migrate core tables: %w", err)
	}

	switch s.driver {
	case "sqlite":
		if err := s.migrateSQLite(); err != nil {
			return fmt.Errorf("sqlite-specific migration: %w", err)
		}
	case "postgres":
		if err := s.migratePostgres(); err != nil {
			return fmt.Errorf("postgres-specific migration: %w", err)
		}
	}
	return nil
}

func (s *store) UpsertObject(ctx context.Context, obj *ObjectModel) error {
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "cluster"},
				{Name: "api_group"},
				{Name: "kind"},
				{Name: "namespace"},
				{Name: "name"},
			},
			DoUpdates: clause.AssignmentColumns([]string{
				"uid", "api_version", "resource", "labels", "annotations",
				"owner_refs", "conditions", "creation_ts", "resource_version", "object",
			}),
		}).
		Create(obj).Error
}

func (s *store) DeleteObject(ctx context.Context, cluster, apiGroup, kind, namespace, name string) error {
	return s.db.WithContext(ctx).
		Where("cluster = ? AND api_group = ? AND kind = ? AND namespace = ? AND name = ?",
			cluster, apiGroup, kind, namespace, name).
		Delete(&ObjectModel{}).Error
}

func (s *store) GetObject(ctx context.Context, id uuid.UUID) (*ObjectModel, error) {
	var obj ObjectModel
	if err := s.db.WithContext(ctx).First(&obj, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &obj, nil
}

func (s *store) UpsertResourceType(ctx context.Context, rt *ResourceTypeModel) error {
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{
				{Name: "cluster"},
				{Name: "api_group"},
				{Name: "resource"},
				{Name: "identity"},
			},
			DoUpdates: clause.AssignmentColumns([]string{
				"api_version", "kind", "singular", "short_names",
				"categories", "namespaced", "subresources",
			}),
		}).
		Create(rt).Error
}

func (s *store) DeleteResourceTypesForCluster(ctx context.Context, cluster string) error {
	return s.db.WithContext(ctx).
		Where("cluster = ?", cluster).
		Delete(&ResourceTypeModel{}).Error
}

func (s *store) UpsertCluster(ctx context.Context, c *ClusterModel) error {
	return s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "name"}},
			DoUpdates: clause.AssignmentColumns([]string{"status", "last_seen", "engaged_at", "labels", "ttl"}),
		}).
		Create(c).Error
}

func (s *store) GetCluster(ctx context.Context, name string) (*ClusterModel, error) {
	var c ClusterModel
	if err := s.db.WithContext(ctx).First(&c, "name = ?", name).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *store) RawDB() *gorm.DB {
	return s.db
}

func (s *store) Driver() string {
	return s.driver
}

func (s *store) ListStaleClusters(ctx context.Context, expiredBefore time.Time) ([]ClusterModel, error) {
	var clusters []ClusterModel
	err := s.db.WithContext(ctx).
		Where("status = ? AND last_seen < ?", "stale", expiredBefore).
		Find(&clusters).Error
	return clusters, err
}

func (s *store) DeleteCluster(ctx context.Context, name string) error {
	return s.db.WithContext(ctx).Where("name = ?", name).Delete(&ClusterModel{}).Error
}

func (s *store) DeleteObjectsForCluster(ctx context.Context, cluster string) error {
	return s.db.WithContext(ctx).Where("cluster = ?", cluster).Delete(&ObjectModel{}).Error
}

func (s *store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
