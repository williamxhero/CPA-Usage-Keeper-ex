package test

import (
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	repositorydto "cpa-usage-keeper/internal/repository/dto"
	"testing"
	"time"
)

func TestStoredCustomHourOverviewReadsCompleteHourlyBucketsWithoutUsageEvents(t *testing.T) {
	db := openTestDatabase(t)
	queryNow := time.Date(2026, 7, 22, 10, 30, 0, 0, time.Local)
	start := time.Date(2026, 7, 22, 6, 0, 0, 0, time.Local)
	end := time.Date(2026, 7, 22, 11, 0, 0, 0, time.Local)
	rows := make([]entities.UsageOverviewHourlyStat, 0, 5)
	zeroCost, unavailable := 0.0, int64(1)
	for bucket := start; bucket.Before(end); bucket = bucket.Add(time.Hour) {
		rows = append(rows, entities.UsageOverviewHourlyStat{
			BucketStart: bucket, APIGroupKey: "provider-a", Model: "model-a",
			RequestCount: 1, SuccessCount: 1, InputTokens: 10, TotalTokens: 10,
			CostUSD: &zeroCost, UnavailableCostCount: &unavailable,
		})
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed hourly rollups: %v", err)
	}

	if err := db.Migrator().DropTable(&entities.UsageEvent{}); err != nil {
		t.Fatal(err)
	}
	queries := captureOverviewDataQueries(t, db)
	overview, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, repositorydto.UsageQueryFilter{
		Range: "custom", CustomUnit: "hour", StartTime: &start, EndTime: &end, EndExclusive: true, QueryNow: &queryNow,
	}, nil)

	if err != nil {
		t.Fatalf("BuildUsageOverviewWithFilter returned error: %v", err)
	}

	if overview.Usage.TotalRequests != 5 || overview.Usage.TotalTokens != 50 {
		t.Fatalf("expected all five complete hourly buckets, got %+v", overview.Usage)
	}
	assertOverviewQueryTables(t, *queries, true, false)
}

func TestStoredCustomDayOverviewReadsCompleteDailyBucketsWithoutUsageEvents(t *testing.T) {
	db := openTestDatabase(t)
	queryNow := time.Date(2026, 7, 22, 10, 30, 0, 0, time.Local)
	start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.Local)
	end := time.Date(2026, 7, 23, 0, 0, 0, 0, time.Local)
	rows := make([]entities.UsageOverviewDailyStat, 0, 3)
	zeroCost, unavailable := 0.0, int64(1)
	for bucket := start; bucket.Before(end); bucket = bucket.AddDate(0, 0, 1) {
		rows = append(rows, entities.UsageOverviewDailyStat{
			BucketStart: bucket, APIGroupKey: "provider-a", Model: "model-a",
			RequestCount: 1, SuccessCount: 1, InputTokens: 10, TotalTokens: 10,
			CostUSD: &zeroCost, UnavailableCostCount: &unavailable,
		})
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed daily rollups: %v", err)
	}

	if err := db.Migrator().DropTable(&entities.UsageEvent{}); err != nil {
		t.Fatal(err)
	}
	queries := captureOverviewDataQueries(t, db)
	overview, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, repositorydto.UsageQueryFilter{
		Range: "custom", CustomUnit: "day", StartTime: &start, EndTime: &end, EndExclusive: true, QueryNow: &queryNow,
	}, nil)

	if err != nil {
		t.Fatalf("BuildUsageOverviewWithFilter returned error: %v", err)
	}

	if overview.Usage.TotalRequests != 3 || overview.Usage.TotalTokens != 30 {
		t.Fatalf("expected all three complete daily buckets, got %+v", overview.Usage)
	}
	assertOverviewQueryTables(t, *queries, false, true)
}
