package migration

import (
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestStoredPricingConfigurationMigrationIsAdditive(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "stored-config.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	for _, statement := range []string{
		"CREATE TABLE model_price_settings (id INTEGER PRIMARY KEY, model TEXT)",
		"INSERT INTO model_price_settings VALUES (1, 'fork-legacy')",
		"CREATE TABLE usage_overview_hourly_stats (id INTEGER PRIMARY KEY, request_count INTEGER)",
		"INSERT INTO usage_overview_hourly_stats VALUES (1, 7)",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := createSchemaMigrationsTable(db); err != nil {
		t.Fatal(err)
	}
	var item databaseMigration
	for _, candidate := range orderedMigrations() {
		if candidate.version == migrationStoredPricingConfiguration {
			item = candidate
			break
		}
	}
	if item.run == nil || item.destructive || item.disableTransaction {
		t.Fatal("stored configuration version must be registered as an additive transactional migration")
	}
	if err := runSchemaMigration(db, item); err != nil {
		t.Fatal(err)
	}
	if err := runSchemaMigration(db, item); err != nil {
		t.Fatalf("repeat stored schema migration: %v", err)
	}
	for _, table := range []string{"stored_model_price_settings", "stored_model_price_rules", "stored_pricing_state"} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("missing stored configuration table %s", table)
		}
	}
	var model string
	if err := db.Raw("SELECT model FROM model_price_settings WHERE id = 1").Scan(&model).Error; err != nil || model != "fork-legacy" {
		t.Fatalf("legacy source changed: model=%q err=%v", model, err)
	}
	var requests int64
	if err := db.Raw("SELECT request_count FROM usage_overview_hourly_stats WHERE id = 1").Scan(&requests).Error; err != nil || requests != 7 {
		t.Fatalf("overview requests changed: requests=%d err=%v", requests, err)
	}
}
