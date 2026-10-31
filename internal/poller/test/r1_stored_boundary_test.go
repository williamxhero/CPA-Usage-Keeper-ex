package poller_test

import (
	"context"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/poller"
	"cpa-usage-keeper/internal/repository"
	"math"
	"testing"
	"time"
)

func TestR1ControlledStoredOverviewRejectsInvalidFeesBeforeWatermark(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cost      *float64
		available *bool
	}{
		{name: "uninitialized"},
		{name: "missing availability", cost: new(1.0)},
		{name: "missing amount", available: new(true)},
		{name: "NaN", cost: new(math.NaN()), available: new(true)},
		{name: "positive infinity", cost: new(math.Inf(1)), available: new(true)},
		{name: "negative infinity", cost: new(math.Inf(-1)), available: new(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openUsageAggregationRunnerDatabase(t)
			now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
			if _, _, err := repository.InsertUsageEvents(db, []entities.UsageEvent{{ID: 1, EventKey: "r1-invalid", Model: "model", Timestamp: now, CostUSD: tc.cost, CostAvailable: tc.available}}); err != nil {
				t.Fatal(err)
			}
			runner := poller.NewUsageAggregationRunner(db)
			resume, err := runner.Pause(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer resume()
			if err := runner.CatchUpOverviewTo(context.Background(), 1); err == nil {
				t.Fatal("strict stored catch-up accepted invalid valuation")
			}
			snap, err := repository.LoadUsageAggregationCheckpointSnapshot(context.Background(), db)
			if err != nil || snap.OverviewCursor != 0 {
				t.Fatalf("strict rejection advanced watermark: %+v %v", snap, err)
			}
			for _, model := range []any{&entities.UsageOverviewHourlyStat{}, &entities.UsageOverviewDailyStat{}} {
				var count int64
				if err := db.Model(model).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("rejected stored row committed: count=%d err=%v", count, err)
				}
			}
		})
	}
}
