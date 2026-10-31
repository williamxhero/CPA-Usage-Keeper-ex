package test

import (
	"math"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	repodto "cpa-usage-keeper/internal/repository/dto"
)

func TestRealtimeModelSpeedCacheOnlyHalfOpenBoundsAndTokenRanking(t *testing.T) {
	for _, window := range []string{"15m", "30m", "60m"} {
		t.Run(window, func(t *testing.T) {
			db := openTestDatabase(t)
			end := time.Date(2026, 10, 8, 12, 0, 0, 0, time.Local)
			duration := 15 * time.Minute
			if window == "30m" {
				duration = 30 * time.Minute
			}
			if window == "60m" {
				duration = 60 * time.Minute
			}
			start := end.Add(-duration)
			mid := start.Add(duration / 2)
			events := []entities.UsageEvent{
				{EventKey: "first-fast", APIGroupKey: "own", Model: "speed", Timestamp: start, OutputTokens: 100, LatencyMS: 1000, TotalTokens: 100},
				{EventKey: "first-slow", APIGroupKey: "own", Model: "speed", Timestamp: mid.Add(-time.Nanosecond), OutputTokens: 100, LatencyMS: 10000, TotalTokens: 100},
				{EventKey: "middle-failed", APIGroupKey: "own", Model: "speed", Timestamp: mid, OutputTokens: 100, LatencyMS: 2000, TotalTokens: 500, Failed: true},
				{EventKey: "no-total", APIGroupKey: "own", Model: "no-total", Timestamp: mid, OutputTokens: 100, LatencyMS: 1000, Generate: new(false)},
				{EventKey: "invalid-latency", APIGroupKey: "own", Model: "no-speed", Timestamp: mid, OutputTokens: 1000, TotalTokens: 1000},
				{EventKey: "invalid-output", APIGroupKey: "own", Model: "no-speed", Timestamp: mid, LatencyMS: 1000, TotalTokens: 1000},
				{EventKey: "warmup", APIGroupKey: "own", Model: "warmup", Timestamp: start.Add(-time.Minute), OutputTokens: 100, LatencyMS: 1, TotalTokens: 9000},
				{EventKey: "at-end", APIGroupKey: "own", Model: "at-end", Timestamp: end, OutputTokens: 100, LatencyMS: 1, TotalTokens: 9000},
				{EventKey: "other-key", APIGroupKey: "other", Model: "other-key", Timestamp: mid, OutputTokens: 100, LatencyMS: 1, TotalTokens: 9000},
			}
			for i := range events {
				events[i].CostUSD = new(float64(0))
				events[i].CostAvailable = new(true)
			}
			if err := db.Create(&events).Error; err != nil {
				t.Fatal(err)
			}
			cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return end }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cache.Close)
			// 缓存独有和 DB 独有事件证明速度来自既有一次缓存读取。
			if err := db.Where("event_key = ?", "first-fast").Delete(&entities.UsageEvent{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Create(&entities.UsageEvent{EventKey: "db-only", APIGroupKey: "own", Model: "db-only", Timestamp: mid, OutputTokens: 100, LatencyMS: 1, TotalTokens: 9999, CostUSD: new(float64(0)), CostAvailable: new(true)}).Error; err != nil {
				t.Fatal(err)
			}
			queries := captureAllAnalysisQueries(t, db)
			result, err := repository.BuildUsageOverviewRealtimeWithFilterAndRecentCache(db, repodto.UsageQueryFilter{RealtimeWindow: window, RealtimeEndTime: &end, APIGroupKey: "own"}, cache)
			if err != nil {
				t.Fatal(err)
			}
			speed := result.ModelSpeed
			if !speed.Available || !speed.Start.Equal(start) || !speed.Mid.Equal(mid) || !speed.End.Equal(end) || len(speed.Series) != 3 {
				t.Fatalf("bounds/models: %+v", speed)
			}
			if speed.Series[0].Model != "no-speed" || speed.Series[0].TotalTokens != 2000 || speed.Series[0].FirstHalf.MeanTPS != nil || speed.Series[0].SecondHalf.MeanTPS != nil {
				t.Fatalf("Token ranking/null: %+v", speed.Series)
			}
			model := speed.Series[1]
			if model.Model != "speed" || model.TotalTokens != 200 || model.SpeedSampleCount != 3 || model.FirstHalf.SpeedSampleCount != 2 || model.SecondHalf.SpeedSampleCount != 1 {
				t.Fatalf("samples: %+v", model)
			}
			assertFloatClose(t, model.FirstHalf.SpeedTPSSum, 110)
			assertFloatClose(t, *model.FirstHalf.MeanTPS, 55)
			assertFloatClose(t, *model.SecondHalf.MeanTPS, 50)
			if speed.Series[2].TotalTokens != 0 || speed.Series[2].SecondHalf.MeanTPS == nil || math.Abs(*speed.Series[2].SecondHalf.MeanTPS-100) > 1e-12 {
				t.Fatalf("independent speed validity: %+v", speed.Series[2])
			}
			for _, query := range *queries {
				if strings.Contains(query, "usage_events") {
					t.Fatalf("cache-hit raw query: %s", query)
				}
			}
			fallback, err := repository.BuildUsageOverviewRealtimeWithFilterAndRecentCache(db, repodto.UsageQueryFilter{RealtimeWindow: window, RealtimeEndTime: &end, APIGroupKey: "own"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if fallback.ModelSpeed.Available || len(fallback.ModelSpeed.Series) != 0 || len(fallback.CurrentUsage.Models) == 0 {
				t.Fatalf("cache unavailable must keep other fallback charts: %+v", fallback)
			}
		})
	}
}

func TestRealtimeModelSpeedKeepsAllModelsBeforeTopOtherTruncation(t *testing.T) {
	db := openTestDatabase(t)
	end := time.Date(2026, 10, 8, 12, 0, 0, 0, time.Local)
	events := make([]entities.UsageEvent, 8)
	for i := range events {
		model := string(rune('a' + i))
		events[i] = entities.UsageEvent{EventKey: model, Model: model, Timestamp: end.Add(-time.Minute), TotalTokens: int64((i + 1) * 100), CostUSD: new(float64(0)), CostAvailable: new(true)}
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatal(err)
	}
	cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return end }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Close)
	result, err := repository.BuildUsageOverviewRealtimeWithFilterAndRecentCache(db, repodto.UsageQueryFilter{RealtimeWindow: "15m", RealtimeEndTime: &end}, cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.CurrentUsage.Models) != 6 || len(result.ModelSpeed.Series) != 8 || result.ModelSpeed.Series[0].Model != "h" || result.ModelSpeed.Series[7].Model != "a" {
		t.Fatalf("full model Token ranking: %+v", result.ModelSpeed)
	}
	for _, row := range result.ModelSpeed.Series {
		if row.FirstHalf.MeanTPS != nil || row.SecondHalf.MeanTPS != nil {
			t.Fatal("no speed samples must remain null")
		}
	}
}
