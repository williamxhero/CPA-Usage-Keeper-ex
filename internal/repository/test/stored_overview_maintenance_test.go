package test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"gorm.io/gorm"
)

func seedStoredOverviewMaintenanceEvents(t *testing.T, db *gorm.DB, now time.Time, count int) {
	t.Helper()
	events := make([]entities.UsageEvent, count)
	for i := range events {
		events[i] = entities.UsageEvent{EventKey: fmt.Sprintf("maintenance-%d", i),
			APIGroupKey: "key-a", Model: "model-a", Timestamp: now.Add(-time.Minute),
			InputTokens: 1, TotalTokens: 1,
			CostUSD: overviewCostPtr(0.25), CostAvailable: overviewAvailabilityPtr(true)}
	}
	if err := db.CreateInBatches(events, 100).Error; err != nil {
		t.Fatal(err)
	}
}

func TestStoredOverviewMaintenancePinsTargetAndDoesNotDuplicateOnResume(t *testing.T) {
	db := openTestDatabase(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.Local)
	seedStoredOverviewMaintenanceEvents(t, db, now, 1001)
	inserted := false
	const hook = "test:append_after_stored_maintenance_page"
	if err := db.Callback().Query().After("gorm:query").Register(hook, func(tx *gorm.DB) {
		if inserted || tx.Error != nil || tx.Statement.Table != "usage_events" {
			return
		}
		inserted = true
		late := entities.UsageEvent{EventKey: "later-than-fixed-target", APIGroupKey: "key-a", Model: "model-a",
			Timestamp: now.Add(-time.Minute), InputTokens: 1, TotalTokens: 1,
			CostUSD: overviewCostPtr(0.25), CostAvailable: overviewAvailabilityPtr(true)}
		if err := db.Session(&gorm.Session{NewDB: true}).Create(&late).Error; err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove(hook) })
	if err := repository.AggregateStoredUsageOverviewStats(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Fatal("late event was never inserted during the catch-up")
	}
	assertUsageOverviewCheckpoint(t, db, 1001)
	assertOverviewStoredFeeBuckets(t, db, 250.25, 0, 1001, 1001)
	if err := repository.AggregateStoredUsageOverviewStats(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	assertUsageOverviewCheckpoint(t, db, 1002)
	assertOverviewStoredFeeBuckets(t, db, 250.5, 0, 1002, 1002)
	if err := repository.AggregateStoredUsageOverviewStats(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	assertOverviewStoredFeeBuckets(t, db, 250.5, 0, 1002, 1002)
}

func TestStoredOverviewMaintenanceResumesAfterLaterPageFailure(t *testing.T) {
	db := openTestDatabase(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.Local)
	seedStoredOverviewMaintenanceEvents(t, db, now, 1001)
	if err := db.Model(&entities.UsageEvent{}).Where("id = ?", 1001).Update("cost_usd", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.AggregateStoredUsageOverviewStats(context.Background(), db, now); err == nil || !strings.Contains(err.Error(), "not backfilled") {
		t.Fatalf("expected the second page's NULL amount to be rejected, got %v", err)
	}
	assertUsageOverviewCheckpoint(t, db, 1000)
	assertOverviewStoredFeeBuckets(t, db, 250, 0, 1000, 1000)
	if err := db.Model(&entities.UsageEvent{}).Where("id = ?", 1001).Update("cost_usd", 0.25).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.AggregateStoredUsageOverviewStats(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	assertUsageOverviewCheckpoint(t, db, 1001)
	assertOverviewStoredFeeBuckets(t, db, 250.25, 0, 1001, 1001)
}

func TestStoredOverviewMaintenanceRejectsCanceledContextAndNilDatabase(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.Local)
	if err := repository.AggregateStoredUsageOverviewStats(context.Background(), nil, now); err == nil {
		t.Fatal("nil database accepted")
	}
	db := openTestDatabase(t)
	seedStoredOverviewMaintenanceEvents(t, db, now, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := repository.AggregateStoredUsageOverviewStats(ctx, db, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not preserved: %v", err)
	}
	assertOverviewNoCommittedRows(t, db)
}
