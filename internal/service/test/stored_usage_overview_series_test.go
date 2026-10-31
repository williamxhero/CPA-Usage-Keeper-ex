package test

import (
	"context"
	"math"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func TestStoredOverviewMergedDayRatesUseWholeCalendarDays(t *testing.T) {
	for _, tc := range []struct {
		zone         string
		firstMinutes float64
	}{{"Asia/Shanghai", 2880}, {"America/Los_Angeles", 2820}} {
		t.Run(tc.zone, func(t *testing.T) {
			withUsageServiceLocation(t, tc.zone)
			start := time.Date(2026, 3, 7, 0, 0, 0, 0, time.Local)
			end := start.AddDate(0, 0, 180)
			db := openUsageServiceTestDatabase(t)
			cost, unavailable := 0.125, int64(0)
			rows := make([]entities.UsageOverviewDailyStat, 0, 180)
			for i := 0; i < 180; i++ {
				rows = append(rows, entities.UsageOverviewDailyStat{BucketStart: start.AddDate(0, 0, i),
					APIGroupKey: "key", Model: "model", RequestCount: 60, TotalTokens: 600,
					InputTokens: 100, CacheReadTokens: 40, CostUSD: &cost, UnavailableCostCount: &unavailable})
			}
			partial := int64(1)
			rows[1].UnavailableCostCount = &partial
			if err := db.Create(&rows).Error; err != nil {
				t.Fatal(err)
			}
			result, err := service.NewStoredUsageOverviewService(db).GetStoredUsageOverview(context.Background(), servicedto.StoredUsageOverviewFilter{Range: "custom", CustomUnit: "day", StartTime: &start, EndTime: &end, EndExclusive: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Series.Buckets) != 90 {
				t.Fatalf("expected 90 aligned stored points, got %d", len(result.Series.Buckets))
			}
			first := result.Series.Buckets[0]
			if first != "2026-03-07" || result.Series.Requests[first] != 120 || result.Series.Tokens[first] != 1200 || result.Series.StoredCostUSD[first] != 0.25 || result.Series.StoredCostAvailable[first] {
				t.Fatalf("merged stored counts/fee/availability changed: %+v", result.Series)
			}
			if math.Abs(result.Series.RPM[first]-120/tc.firstMinutes) > 1e-12 || math.Abs(result.Series.TPM[first]-1200/tc.firstMinutes) > 1e-12 {
				t.Fatalf("wrong calendar-day denominator: %+v", result.Series)
			}
			var costSum float64
			for _, label := range result.Series.Buckets {
				costSum += result.Series.StoredCostUSD[label]
				rate := result.Series.CacheReadRate[label]
				if rate == nil || *rate != 40 {
					t.Fatalf("weighted stored cache-read rate[%s]=%v", label, rate)
				}
			}
			if costSum != 22.5 || result.Summary.StoredCostUSD != 22.5 || result.Summary.StoredCostAvailable {
				t.Fatalf("merged stored series lost amount or availability: sum=%g summary=%+v", costSum, result.Summary)
			}
		})
	}
}

func TestStoredOverviewEmptyLongRangeDoesNotInventBuckets(t *testing.T) {
	db := openUsageServiceTestDatabase(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 0, 120)
	result, err := service.NewStoredUsageOverviewService(db).GetStoredUsageOverview(context.Background(), servicedto.StoredUsageOverviewFilter{Range: "custom", CustomUnit: "day", StartTime: &start, EndTime: &end, EndExclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Series.Buckets) != 0 || len(result.Series.Requests) != 0 || len(result.Series.StoredCostUSD) != 0 {
		t.Fatalf("empty stored range invented chart points: %+v", result.Series)
	}
}

func TestStoredOverviewSparseLongDaySeriesAlignsAllMapKeys(t *testing.T) {
	withUsageServiceLocation(t, "UTC")
	db := openUsageServiceTestDatabase(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 0, 60)
	fee, unavailable := 1.25, int64(0)
	if err := db.Create(&entities.UsageOverviewDailyStat{BucketStart: start, APIGroupKey: "key", Model: "model", RequestCount: 3, TotalTokens: 10, CostUSD: &fee, UnavailableCostCount: &unavailable}).Error; err != nil {
		t.Fatal(err)
	}
	result, err := service.NewStoredUsageOverviewService(db).GetStoredUsageOverview(context.Background(), servicedto.StoredUsageOverviewFilter{Range: "custom", CustomUnit: "day", StartTime: &start, EndTime: &end, EndExclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	series := result.Series
	if len(series.Buckets) != 60 || len(series.Requests) != 60 || len(series.Tokens) != 60 || len(series.RPM) != 60 || len(series.TPM) != 60 || len(series.StoredCostUSD) != 60 || len(series.StoredCostAvailable) != 60 || len(series.CacheReadRate) != 60 {
		t.Fatalf("sparse stored calendar series maps are not aligned: %+v", series)
	}
	for i, label := range series.Buckets {
		if want := start.AddDate(0, 0, i).Format(time.DateOnly); label != want || !series.StoredCostAvailable[label] {
			t.Fatalf("sparse day order/known zero availability changed: bucket=%s want=%s", label, want)
		}
		if i == 0 {
			if series.Requests[label] != 3 || series.StoredCostUSD[label] != 1.25 {
				t.Fatalf("sparse real day lost totals: %+v", series)
			}
		} else if series.Requests[label] != 0 || series.StoredCostUSD[label] != 0 {
			t.Fatalf("sparse empty day invented usage: %+v", series)
		}
	}
}
