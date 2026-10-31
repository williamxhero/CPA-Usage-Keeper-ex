package test

import (
	"context"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/overview"
	"cpa-usage-keeper/internal/repository"
	"testing"
	"time"
)

func TestR1StrictStoredApplyRejectsUnknownBucketWithoutWatermark(t *testing.T) {
	db := openTestDatabase(t)
	now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: now, APIGroupKey: "key", Model: "model", RequestCount: 7, TotalTokens: 70, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	event := entities.UsageEvent{ID: 1, APIGroupKey: "key", Model: "model", Timestamp: now, TotalTokens: 10, CostUSD: new(2.0), CostAvailable: new(true)}
	h, d, next, err := overview.BuildStoredRows([]entities.UsageEvent{event})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ApplyStoredUsageOverviewAggregationPage(context.Background(), db, 0, next, h, d, now); err == nil {
		t.Fatal("stored apply accepted uninitialized bucket")
	}
	var row entities.UsageOverviewHourlyStat
	if err := db.Take(&row).Error; err != nil || row.RequestCount != 7 || row.TotalTokens != 70 || row.CostUSD != nil {
		t.Fatalf("strict apply changed unknown bucket %+v err=%v", row, err)
	}
	cursor, err := repository.UsageOverviewAggregationCursor(context.Background(), db)
	if err != nil || cursor != 0 {
		t.Fatalf("strict apply advanced cursor=%d err=%v", cursor, err)
	}
	var count int64
	if err := db.Model(&entities.UsageOverviewDailyStat{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("strict apply committed daily count=%d err=%v", count, err)
	}
}
