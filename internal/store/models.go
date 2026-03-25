package store

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// ObjectModel stores all synced Kubernetes objects.
type ObjectModel struct {
	ID              uuid.UUID      `gorm:"type:uuid;primaryKey"`
	UID             string         `gorm:"type:varchar(256);not null;index:idx_obj_uid"`
	Cluster         string         `gorm:"type:varchar(256);not null;uniqueIndex:idx_obj_unique;index:idx_obj_cluster_gvk"`
	APIGroup        string         `gorm:"column:api_group;type:varchar(256);not null;default:'';uniqueIndex:idx_obj_unique;index:idx_obj_cluster_gvk"`
	APIVersion      string         `gorm:"column:api_version;type:varchar(64);not null"`
	Kind            string         `gorm:"type:varchar(256);not null;uniqueIndex:idx_obj_unique;index:idx_obj_cluster_gvk;index:idx_obj_cluster_ns_name"`
	Resource        string         `gorm:"type:varchar(256);not null"`
	Namespace       string         `gorm:"type:varchar(256);not null;default:'';uniqueIndex:idx_obj_unique;index:idx_obj_cluster_ns_name"`
	Name            string         `gorm:"type:varchar(256);not null;uniqueIndex:idx_obj_unique;index:idx_obj_cluster_ns_name;index:idx_obj_name"`
	Labels          datatypes.JSON `gorm:"type:jsonb"`
	Annotations     datatypes.JSON `gorm:"type:jsonb"`
	OwnerRefs       datatypes.JSON `gorm:"column:owner_refs;type:jsonb"`
	Conditions      datatypes.JSON `gorm:"type:jsonb"`
	CreationTS      *time.Time     `gorm:"column:creation_ts;index:idx_obj_creation_ts"`
	ResourceVersion string         `gorm:"column:resource_version;type:varchar(64)"`
	Object          datatypes.JSON `gorm:"type:jsonb;not null"`
}

func (ObjectModel) TableName() string { return "objects" }

// ResourceTypeModel is a flattened REST mapper populated from cluster discovery.
type ResourceTypeModel struct {
	Cluster      string         `gorm:"type:varchar(256);not null;primaryKey;index:idx_rt_kind;index:idx_rt_resource"`
	APIGroup     string         `gorm:"column:api_group;type:varchar(256);not null;default:'';primaryKey"`
	APIVersion   string         `gorm:"column:api_version;type:varchar(64);not null"`
	Kind         string         `gorm:"type:varchar(256);not null;index:idx_rt_kind"`
	Singular     string         `gorm:"type:varchar(256)"`
	Resource     string         `gorm:"type:varchar(256);not null;primaryKey;index:idx_rt_resource"`
	ShortNames   datatypes.JSON `gorm:"type:jsonb"`
	Categories   datatypes.JSON `gorm:"type:jsonb"`
	Namespaced   bool           `gorm:"not null"`
	Subresources datatypes.JSON `gorm:"type:jsonb"`
	Identity     string         `gorm:"type:varchar(256);not null;default:'';primaryKey"`
}

func (ResourceTypeModel) TableName() string { return "resource_types" }

// ClusterModel tracks cluster health and lifecycle.
type ClusterModel struct {
	Name      string         `gorm:"type:varchar(256);primaryKey"`
	Status    string         `gorm:"type:varchar(64);not null"`
	LastSeen  time.Time      `gorm:"column:last_seen;not null"`
	EngagedAt *time.Time     `gorm:"column:engaged_at"`
	Labels    datatypes.JSON `gorm:"type:jsonb"`
	TTL       int64          `gorm:"column:ttl;default:3600"` // seconds, default 1 hour
}

func (ClusterModel) TableName() string { return "clusters" }

// ObjectLabelModel is a supplementary table for SQLite label queries (no GIN support).
type ObjectLabelModel struct {
	ObjectID uuid.UUID `gorm:"type:uuid;primaryKey"`
	Key      string    `gorm:"type:varchar(256);not null;primaryKey;index:idx_labels_kv"`
	Value    string    `gorm:"type:varchar(256);not null;index:idx_labels_kv"`
}

func (ObjectLabelModel) TableName() string { return "object_labels" }
