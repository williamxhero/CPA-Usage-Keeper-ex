package poller_test

import (
	"context"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/poller"
	"cpa-usage-keeper/internal/repository"
)

func TestStoredMaintenanceDoesNotChangeOrdinaryMissingFeeProgress(t *testing.T) {
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.Local)
	if _, _, err := repository.InsertUsageEvents(db, []entities.UsageEvent{{EventKey: "ordinary-unknown", APIGroupKey: "key-a", Model: "model-a", Timestamp: now.Add(-time.Minute), TotalTokens: 10}}); err != nil {
		t.Fatal(err)
	}
	result, err := newUsageAggregationRunnerAt(db, now, 0).RunOnce(context.Background())
	if err != nil || !result.Processed {
		t.Fatalf("ordinary facts must progress independently of fee readiness: result=%+v err=%v", result, err)
	}
	snapshot, err := repository.LoadUsageAggregationCheckpointSnapshot(context.Background(), db)
	if err != nil || snapshot.OverviewCursor != 1 || snapshot.ActivityCursor != 1 || snapshot.LatencyCursor != 1 {
		t.Fatalf("ordinary cursors did not progress: snapshot=%+v err=%v", snapshot, err)
	}
	var event entities.UsageEvent
	if err := db.First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.CostUSD != nil || event.CostAvailable != nil {
		t.Fatalf("ordinary runner rewrote unknown historical fee: %+v", event)
	}
}

func TestStoredMaintenanceRunnerRejectsUnknownExistingBucketsAtomically(t *testing.T) {
	db := openUsageAggregationRunnerDatabase(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.Local)
	when := now.Add(-time.Minute)
	unknown := entities.UsageOverviewHourlyStat{BucketStart: when.Truncate(time.Hour), APIGroupKey: "key-a", Model: "model-a", RequestCount: 7, TotalTokens: 70}
	if err := db.Create(&unknown).Error; err != nil {
		t.Fatal(err)
	}
	fee, available := 0.5, true
	if _, _, err := repository.InsertUsageEvents(db, []entities.UsageEvent{{EventKey: "stored-ready", APIGroupKey: "key-a", Model: "model-a", Timestamp: when, TotalTokens: 10, CostUSD: &fee, CostAvailable: &available}}); err != nil {
		t.Fatal(err)
	}
	runner := poller.NewStoredUsageAggregationMaintenanceRunner(db, poller.UsageAggregationRunnerOptions{NowFunc: func() time.Time { return now }})
	result, err := runner.RunOnce(context.Background())
	if err == nil || !result.Processed {
		t.Fatalf("stored bucket error must not prevent independent rollups: result=%+v err=%v", result, err)
	}
	snapshot, err := repository.LoadUsageAggregationCheckpointSnapshot(context.Background(), db)
	if err != nil || snapshot.OverviewCursor != 0 || snapshot.ActivityCursor != 1 || snapshot.LatencyCursor != 1 {
		t.Fatalf("stored bucket failure advanced overview or blocked independent cursors: snapshot=%+v err=%v", snapshot, err)
	}
	var after entities.UsageOverviewHourlyStat
	if err := db.First(&after, unknown.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.CostUSD != nil || after.UnavailableCostCount != nil || after.RequestCount != 7 || after.TotalTokens != 70 {
		t.Fatalf("strict stored apply changed an unknown bucket: %+v", after)
	}
	var dailyCount int64
	if err := db.Model(&entities.UsageOverviewDailyStat{}).Count(&dailyCount).Error; err != nil || dailyCount != 0 {
		t.Fatalf("strict stored apply left partial daily rows: count=%d err=%v", dailyCount, err)
	}
}
