package test

import (
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	repodto "cpa-usage-keeper/internal/repository/dto"
	"testing"
	"time"
)

func TestStoredOverviewBoundaryDistinguishNullAndUnavailableCost(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	for _, test := range []struct {
		name      string
		cost      *float64
		available *bool
		wantError bool
	}{
		{name: "missing amount", available: overviewAvailabilityPtr(true), wantError: true},
		{name: "missing availability", cost: overviewCostPtr(0), wantError: true},
		{name: "known unavailable", cost: overviewCostPtr(0), available: overviewAvailabilityPtr(false)},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openTestDatabase(t)
			end := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
			event := entities.UsageEvent{EventKey: test.name, APIGroupKey: "selected", Model: "unpriced", Timestamp: end.Add(-time.Minute), InputTokens: 10, TotalTokens: 10, CostUSD: test.cost, CostAvailable: test.available}
			if _, _, err := repository.InsertUsageEvents(db, []entities.UsageEvent{event}); err != nil {
				t.Fatalf("seed unbackfilled event: %v", err)
			}
			start := end.Add(-15 * time.Minute)
			overview, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, repodto.UsageQueryFilter{Range: "custom", StartTime: &start, EndTime: &end, APIGroupKey: "selected"}, nil)
			if test.wantError {
				if err == nil {
					t.Fatal("boundary accepted unbackfilled fee")
				}
			} else if err != nil || !finiteStoredOverviewTestCost(overview.Summary.StoredCostUSD, 0) || overview.Summary.StoredCostAvailable {
				t.Fatalf("boundary changed explicit unavailable zero: overview=%+v err=%v", overview, err)
			}
		})
	}
}
