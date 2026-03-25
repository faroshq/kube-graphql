package store

import (
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newSQLiteDB(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}

	// Enable WAL mode for better concurrency.
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA foreign_keys=ON")

	return db, nil
}

// migrateSQLite runs SQLite-specific migrations (supplementary labels table).
func (s *store) migrateSQLite() error {
	return s.db.AutoMigrate(&ObjectLabelModel{})
}
