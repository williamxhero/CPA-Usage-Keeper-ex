package app

import (
	"context"
	"encoding/json"
	"fmt"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

// Reject already-structured files without a fixed pre-structure baseline before
// creating any M1 backup. A current-schema snapshot is not a v1 recovery image.
// The repository still performs full backup fingerprint/evidence verification;
// this is only a read-only precondition, never permission to bypass its guards.
func verifyStoredPricingUpgradePreconditions(ctx context.Context, db *gorm.DB, state entities.PricingMigrationState) error {
	read := db.WithContext(ctx).Clauses(dbresolver.Read)
	structured := false
	if read.Migrator().HasTable("schema_migrations") {
		var count int64
		if err := read.Table("schema_migrations").Where("version = ?", "20261009_pricing_storage_structure").Count(&count).Error; err != nil {
			return fmt.Errorf("read stored pricing schema identity: %w", err)
		}
		structured = count > 0
	}
	for _, target := range []struct{ table, column string }{
		{"usage_events", "cost_usd"}, {"usage_events", "cost_available"},
		{"usage_events_archive", "cost_usd"}, {"usage_events_archive", "cost_available"},
		{"usage_overview_hourly_stats", "cost_usd"}, {"usage_overview_daily_stats", "cost_usd"},
	} {
		if read.Migrator().HasColumn(target.table, target.column) {
			structured = true
		}
	}
	if !structured {
		return nil
	}
	var baseline repository.PricingLegacyBaseline
	if state.BaselineJSON == nil || state.CursorsJSON == nil || json.Unmarshal([]byte(*state.BaselineJSON), &baseline) != nil || baseline.Fixed == nil {
		return fmt.Errorf("stored pricing upgrade requires verified pre-structure protection; this structured database has no fixed legacy baseline")
	}
	return nil
}
