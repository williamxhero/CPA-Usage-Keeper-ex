package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/helper"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openMigrationDatabase(ctx context.Context, filename string, readOnly bool) (*gorm.DB, func(), error) {
	// Accept a local filesystem path only. Never accept caller-supplied SQLite
	// URI options, which could create a database or redirect to a memory store.
	if filename == "" || strings.TrimSpace(filename) != filename || strings.ContainsAny(filename, "?\x00\r\n") || strings.EqualFold(filename, ":memory:") || strings.HasPrefix(strings.ToLower(filename), "file:") || strings.Contains(filename, "://") || strings.HasPrefix(filename, `\\`) || strings.HasPrefix(filename, "//") {
		return nil, nil, errors.New("-db must name an existing local SQLite file without URI or query parameters")
	}
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return nil, nil, errors.New("cannot resolve database file")
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, errors.New("database file must exist and be a regular file")
	}
	mode := "rw"
	if readOnly {
		mode = "ro"
	}
	// mode=rw prevents a missing file from being created even if it disappears
	// after Stat. URI construction escapes filename characters such as # and %.
	dsn := helper.BuildSQLiteFileURI(absolute) + "?mode=" + mode + "&cache=private&_busy_timeout=15000&_foreign_keys=on"
	if readOnly {
		dsn += "&_query_only=on"
	}
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, nil, errors.New("cannot open SQLite database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, errors.New("cannot configure SQLite database")
	}
	closeDB := func() { _ = sqlDB.Close() }
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if err := sqlDB.PingContext(ctx); err != nil {
		closeDB()
		return nil, nil, errors.New("cannot read SQLite database")
	}
	return db.WithContext(ctx), closeDB, nil
}

func migrationReportModels() []any {
	return []any{&entities.PricingMigrationBatch{}, &entities.PricingMigrationBatchCount{}, &entities.PricingMigrationItem{}, &entities.PricingMigrationLegacyState{}}
}

func migrationRequiredModels() []any {
	return []any{
		&entities.ModelPriceSetting{}, &entities.ModelPriceRule{},
		&entities.OfficialModelPrice{}, &entities.OfficialModelPriceAlias{},
		&entities.CredentialPricingSubject{}, &entities.CredentialPricingAssociation{},
		&entities.CredentialPriceDefault{}, &entities.CredentialModelMultiplier{},
		&entities.CredentialModelPriceOverride{}, &entities.PricingChannel{},
		&entities.PricingChannelMember{}, &entities.ChannelPriceDefault{},
		&entities.ChannelModelPrice{}, &entities.UsageIdentity{}, &entities.AppSetting{},
	}
}

// Only run may create missing report tables. Existing business/report tables
// are checked, never AutoMigrated: SQLite AutoMigrate can rebuild a table when
// columns differ. Normal application upgrades own business schema migrations.
func prepareMigrationSchema(db *gorm.DB, action string) error {
	if action == "run" || action == "rollback" || action == "restore" {
		if err := checkModelSchema(db, migrationRequiredModels()); err != nil {
			return errors.New("required pricing schema is unavailable; complete the Keeper schema upgrade first")
		}
	}
	models := migrationReportModels()
	if action == "run" {
		if err := db.Transaction(func(tx *gorm.DB) error {
			for _, model := range models {
				if !tx.Migrator().HasTable(model) {
					if err := tx.Migrator().CreateTable(model); err != nil {
						return err
					}
				}
			}
			return checkModelSchema(tx, models)
		}); err != nil {
			return errors.New("cannot prepare pricing migration report schema")
		}
	} else if err := checkModelSchema(db, models); err != nil {
		return errors.New("pricing migration report schema is unavailable; complete the Keeper schema upgrade first")
	}
	return nil
}

func checkModelSchema(db *gorm.DB, models []any) error {
	for _, model := range models {
		statement := &gorm.Statement{DB: db}
		if err := statement.Parse(model); err != nil {
			return err
		}
		columns, err := db.Migrator().ColumnTypes(model)
		if err != nil {
			return err
		}
		present := make(map[string]bool, len(columns))
		for _, column := range columns {
			present[column.Name()] = true
		}
		for _, name := range statement.Schema.DBNames {
			if !present[name] {
				return errors.New("required schema column missing")
			}
		}
	}
	return nil
}
