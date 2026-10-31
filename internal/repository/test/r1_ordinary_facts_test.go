package test

import (
	"context"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"testing"
	"time"
)

func TestR1OrdinaryAggregateUnknownFeesAcrossPages(t *testing.T) {
	for _, unknownFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "unknown-first", false: "known-first"}[unknownFirst], func(t *testing.T) {
			db := openTestDatabase(t)
			now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
			cost, available := 2.5, true
			for i := 0; i < 2; i++ {
				event := entities.UsageEvent{EventKey: "r1", APIGroupKey: "key", Model: "model", Timestamp: now, InputTokens: 12, TotalTokens: 12, OutputTokens: 100, LatencyMS: 1000}
				if (i == 0) != unknownFirst {
					event.CostUSD = &cost
					event.CostAvailable = &available
				}
				if _, _, err := repository.InsertUsageEvents(db, []entities.UsageEvent{event}); err != nil {
					t.Fatal(err)
				}
				if err := repository.AggregateUsageOverviewStats(context.Background(), db, now); err != nil {
					t.Fatalf("ordinary facts blocked: %v", err)
				}
			}
			for _, model := range []any{&entities.UsageOverviewHourlyStat{}, &entities.UsageOverviewDailyStat{}} {
				var row struct {
					CostUSD                                     *float64
					UnavailableCostCount                        *int64
					RequestCount, TotalTokens, SpeedSampleCount int64
					SpeedTPSSum                                 float64
				}
				if err := db.Model(model).Take(&row).Error; err != nil {
					t.Fatal(err)
				}
				if row.CostUSD != nil || row.UnavailableCostCount != nil || row.RequestCount != 2 || row.TotalTokens != 24 || row.SpeedSampleCount != 2 || row.SpeedTPSSum != 200 {
					t.Fatalf("ordinary row=%+v", row)
				}
			}
			var events []entities.UsageEvent
			if err := db.Order("id").Find(&events).Error; err != nil {
				t.Fatal(err)
			}
			unknownIndex := 1
			if unknownFirst {
				unknownIndex = 0
			}
			if events[unknownIndex].CostUSD != nil || events[unknownIndex].CostAvailable != nil {
				t.Fatalf("ordinary aggregation repriced unknown fact: %+v", events[unknownIndex])
			}
			cursor, err := repository.UsageOverviewAggregationCursor(context.Background(), db)
			if err != nil || cursor != 2 {
				t.Fatalf("cursor=%d err=%v", cursor, err)
			}
			if err := repository.AggregateUsageOverviewStats(context.Background(), db, now); err != nil {
				t.Fatal(err)
			}
			var row entities.UsageOverviewHourlyStat
			if err := db.Take(&row).Error; err != nil || row.RequestCount != 2 {
				t.Fatalf("retry double-counted: %+v err=%v", row, err)
			}
		})
	}
}
