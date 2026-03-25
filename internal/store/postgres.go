package store

import (
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newPostgresDB(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}
	return db, nil
}

// migratePostgres creates PostgreSQL-specific GIN indexes.
func (s *store) migratePostgres() error {
	ginIndexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_obj_labels_gin ON objects USING GIN(labels)",
		"CREATE INDEX IF NOT EXISTS idx_obj_owner_refs_gin ON objects USING GIN(owner_refs)",
		"CREATE INDEX IF NOT EXISTS idx_obj_conditions_gin ON objects USING GIN(conditions)",
		"CREATE INDEX IF NOT EXISTS idx_rt_categories_gin ON resource_types USING GIN(categories)",
		"CREATE INDEX IF NOT EXISTS idx_rt_short_names_gin ON resource_types USING GIN(short_names)",
	}
	for _, sql := range ginIndexes {
		if err := s.db.Exec(sql).Error; err != nil {
			return err
		}
	}
	return nil
}
