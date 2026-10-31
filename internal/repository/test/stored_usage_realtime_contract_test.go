package test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	repodto "cpa-usage-keeper/internal/repository/dto"
	"gorm.io/gorm"
)

func TestStoredRealtimeRejectsCancelledCacheRead(t *testing.T) {
	db := openTestDatabase(t)
	end := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return end }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = repository.BuildStoredUsageRealtimeWithFilterAndRecentCache(db.WithContext(ctx), repodto.UsageQueryFilter{RealtimeWindow: "15m", RealtimeEndTime: &end}, cache)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled cache-only read became successful: %v", err)
	}
}

func TestStoredRealtimeIsolatesPricingAndPreservesPartialFees(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db := openTestDatabase(t)
	end := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	known, partial, free := 1.25, 2.5, 0.0
	yes, no := true, false
	if err := db.Create(&[]entities.UsageEvent{
		{EventKey: "known", APIGroupKey: "selected", Model: "model-a", Timestamp: end.Add(-time.Minute), InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: &known, CostAvailable: &yes},
		{EventKey: "partial", APIGroupKey: "selected", Model: "model-a", Timestamp: end.Add(-time.Minute), TotalTokens: 100, CostUSD: &partial, CostAvailable: &no},
		{EventKey: "free", APIGroupKey: "selected", Model: "free", Timestamp: end.Add(-time.Minute), TotalTokens: 50, CostUSD: &free, CostAvailable: &yes},
		// Unready rows outside the selected half-open scope must not poison it.
		{EventKey: "other", APIGroupKey: "other", Model: "model-a", Timestamp: end.Add(-time.Minute), TotalTokens: 900},
		{EventKey: "end", APIGroupKey: "selected", Model: "model-a", Timestamp: end, TotalTokens: 900},
	}).Error; err != nil {
		t.Fatal(err)
	}
	cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return end }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Close)
	var queries []string
	if err := db.Callback().Query().After("gorm:query").Register("stored_realtime_contract", func(tx *gorm.DB) {
		queries = append(queries, tx.Statement.SQL.String())
	}); err != nil {
		t.Fatal(err)
	}
	for _, price := range []float64{9, 90} {
		upsertUsageCostResolverPrice(t, db, "model-a", price)
		for _, source := range []struct {
			name  string
			cache *repository.UsageRecentEventCache
		}{{"cache", cache}, {"database", nil}} {
			t.Run(fmt.Sprintf("%s/price=%g", source.name, price), func(t *testing.T) {
				queries = nil
				got, err := repository.BuildStoredUsageRealtimeWithFilterAndRecentCache(db, repodto.UsageQueryFilter{RealtimeWindow: "15m", RealtimeEndTime: &end, APIGroupKey: "selected"}, source.cache)
				if err != nil {
					t.Fatal(err)
				}
				s := got.Insights.Summary
				if s.Requests != 3 || s.TotalTokens != 1_000_150 || s.StoredCostUSD != 3.75 || s.StoredCostAvailable {
					t.Fatalf("stored summary changed: %+v", s)
				}
				if len(got.CurrentUsage.Models) != 2 || got.CurrentUsage.Models[0].StoredCostUSD != 3.75 || got.CurrentUsage.Models[0].StoredCostAvailable || got.CurrentUsage.Models[1].StoredCostUSD != 0 || !got.CurrentUsage.Models[1].StoredCostAvailable {
					t.Fatalf("known false/free amounts lost: %+v", got.CurrentUsage.Models)
				}
				seen := false
				for _, p := range got.TokenVelocity {
					seen = seen || p.StoredCostUSD == 3.75 && !p.StoredCostAvailable
				}
				if !seen || len(got.Insights.Outcomes) != 30 || got.CacheUsed != (source.cache != nil) {
					t.Fatalf("stored trend/coverage mismatch: %+v", got)
				}
				for _, q := range queries {
					lower := strings.ToLower(q)
					if strings.Contains(lower, "pric") || source.cache != nil && strings.Contains(lower, "usage_events") {
						t.Fatalf("unexpected pricing/raw cache query: %s", q)
					}
				}
			})
		}
	}
}

func TestStoredRealtimeDoesNotChangeCurrentResolverOrWriteDatabase(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db := openTestDatabase(t)
	end := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	fee, available := 1.25, true
	if err := db.Create(&entities.UsageEvent{EventKey: "independent", Model: "model-a", Timestamp: end.Add(-time.Minute), InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: &fee, CostAvailable: &available}).Error; err != nil {
		t.Fatal(err)
	}
	cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return end }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Close)
	reading := false
	denyWrite := func(tx *gorm.DB) {
		if reading {
			t.Error("read path attempted a database write")
			tx.AddError(fmt.Errorf("write forbidden during read"))
		}
	}
	for _, register := range []func(string, func(*gorm.DB)) error{
		db.Callback().Create().Before("gorm:create").Register,
		db.Callback().Update().Before("gorm:update").Register,
		db.Callback().Delete().Before("gorm:delete").Register,
		db.Callback().Raw().Before("gorm:raw").Register,
	} {
		if err := register("stored_realtime_readonly", denyWrite); err != nil {
			t.Fatal(err)
		}
	}
	filter := repodto.UsageQueryFilter{RealtimeWindow: "15m", RealtimeEndTime: &end}
	for _, price := range []float64{9, 90} {
		upsertUsageCostResolverPrice(t, db, "model-a", price)
		resolver := newUsageCostResolverForTest(t, db)
		for _, source := range []*repository.UsageRecentEventCache{nil, cache} {
			reading = true
			stored, storedErr := repository.BuildStoredUsageRealtimeWithFilterAndRecentCache(db, filter, source)
			current, currentErr := repository.BuildUsageOverviewRealtimeWithFilterAndRecentCache(db, filter, source, resolver)
			reading = false
			if storedErr != nil || currentErr != nil {
				t.Fatalf("stored=%v current=%v", storedErr, currentErr)
			}
			if stored.Insights.Summary.StoredCostUSD != 1.25 || !stored.Insights.Summary.StoredCostAvailable || current.Insights.Summary.CostUSD != price || !current.Insights.Summary.CostAvailable {
				t.Fatalf("monetary contracts mixed: stored=%+v current=%+v", stored.Insights.Summary, current.Insights.Summary)
			}
			dual := current.Insights.Summary.DualCosts.Configured
			if dual.TotalCostUSD == nil || *dual.TotalCostUSD != price || !dual.Complete || current.PricingSnapshotID != resolver.SnapshotID() {
				t.Fatalf("current dual costs/snapshot changed: %+v", current)
			}
		}
	}
}

func TestStoredRealtimeRejectsUnknownNonfiniteAndOverflow(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cost      *float64
		available *bool
		count     int
	}{
		{"null", nil, overviewAvailabilityPtr(true), 1},
		{"null-availability", overviewCostPtr(0), nil, 1},
		{"infinite", overviewCostPtr(math.Inf(1)), overviewAvailabilityPtr(true), 1},
		{"overflow", overviewCostPtr(math.MaxFloat64), overviewAvailabilityPtr(true), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDatabase(t)
			end := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			for i := 0; i < tc.count; i++ {
				if err := db.Create(&entities.UsageEvent{Timestamp: end.Add(-time.Duration(i+1) * time.Minute), Model: "model-a", TotalTokens: 1, CostUSD: tc.cost, CostAvailable: tc.available}).Error; err != nil {
					t.Fatal(err)
				}
			}
			cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return end }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cache.Close)
			for _, source := range []*repository.UsageRecentEventCache{nil, cache} {
				if _, err := repository.BuildStoredUsageRealtimeWithFilterAndRecentCache(db, repodto.UsageQueryFilter{RealtimeWindow: "15m", RealtimeEndTime: &end}, source); err == nil {
					t.Fatal("invalid persisted fee became success")
				}
			}
		})
	}
}
