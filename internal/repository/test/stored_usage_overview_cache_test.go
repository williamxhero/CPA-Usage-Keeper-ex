package test

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
)

func TestStoredOverviewCacheReadsScopedHalfOpenFeesWithoutRawTable(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db := openTestDatabase(t)
	now := time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC)
	start, end := now.Add(-55*time.Minute), now.Add(-5*time.Minute)
	for _, row := range []struct {
		key, group string
		at         time.Time
		fee        float64
		available  bool
	}{
		{"before", "selected", start.Add(-time.Nanosecond), 100, true},
		{"first", "selected", start, 1.25, true},
		{"partial", "selected", end.Add(-time.Nanosecond), 0.75, false},
		{"end", "selected", end, 100, true},
		{"other", "other", start.Add(time.Minute), 100, true},
	} {
		fee, available := row.fee, row.available
		if err := db.Create(&entities.UsageEvent{EventKey: row.key, APIGroupKey: row.group, Model: "model-a", Timestamp: row.at,
			InputTokens: 8, CacheReadTokens: 2, TotalTokens: 10, CostUSD: &fee, CostAvailable: &available}).Error; err != nil {
			t.Fatal(err)
		}
	}
	cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Close)
	// A database fallback cannot satisfy this test: the actual raw table is gone.
	if err := db.Exec("DROP TABLE usage_events").Error; err != nil {
		t.Fatal(err)
	}
	queries := storedOverviewRejectSideEffects(t, db)
	filter := dto.UsageQueryFilter{Range: "custom", StartTime: &start, EndTime: &end, QueryNow: &now, APIGroupKey: "selected", EndExclusive: true}
	out, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, filter, cache)
	if err != nil {
		t.Fatal(err)
	}
	if !out.CacheUsed || out.Summary.RequestCount != 2 || out.Summary.TokenCount != 20 || out.Summary.StoredCostUSD != 2 || out.Summary.StoredCostAvailable {
		t.Fatalf("cached scope/half-open amount/availability changed: %+v", out)
	}
	if len(*queries) != 0 {
		t.Fatalf("cache-only boundary read queried the database: %+v", *queries)
	}
	filter.APIGroupKey = "empty-key"
	empty, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, filter, cache)
	if err != nil || empty.Summary.RequestCount != 0 || empty.Summary.StoredCostUSD != 0 || !empty.Summary.StoredCostAvailable {
		t.Fatalf("covered empty key is not a known empty result: result=%+v err=%v", empty, err)
	}
	if _, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, filter, nil); err == nil {
		t.Fatal("missing raw table should fail when no cache was explicitly supplied")
	}
}

func TestStoredOverviewCacheFallbackRequiresActualCoverageAndAvailability(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	for _, name := range []string{"nil", "closed", "uncovered", "pruned clock", "reload unavailable", "pending"} {
		t.Run(name, func(t *testing.T) {
			db := openTestDatabase(t)
			now := time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC)
			start, end := now.Add(-55*time.Minute), now.Add(-5*time.Minute)
			fee, available := 0.625, true
			if err := db.Create(&entities.UsageEvent{EventKey: "fallback", APIGroupKey: "selected", Timestamp: start.Add(time.Minute),
				TotalTokens: 100, CostUSD: &fee, CostAvailable: &available}).Error; err != nil {
				t.Fatal(err)
			}
			cacheNow, window := now, 70*time.Minute
			if name == "uncovered" {
				window = 10 * time.Minute
			}
			if name == "pruned clock" {
				cacheNow = now.Add(20 * time.Minute)
			}
			cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return cacheNow }, Window: window, QueueSize: 1})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cache.Close)
			switch name {
			case "closed":
				cache.Close()
			case "reload unavailable":
				if err := cache.ReloadStoredCostEvents(context.Background(), nil); err == nil {
					t.Fatal("invalid reload unexpectedly succeeded")
				}
			case "pending":
				slots := *recentCacheField[chan struct{}](cache, "appendSlots")
				<-slots
				defer func() { slots <- struct{}{} }()
			}
			if name == "nil" {
				cache = nil
			}
			queries := storedOverviewRejectSideEffects(t, db)
			out, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, dto.UsageQueryFilter{
				Range: "custom", StartTime: &start, EndTime: &end, QueryNow: &now, APIGroupKey: "selected", EndExclusive: true,
			}, cache)
			if err != nil || out.CacheUsed || out.Summary.RequestCount != 1 || out.Summary.TokenCount != 100 || out.Summary.StoredCostUSD != 0.625 || !out.Summary.StoredCostAvailable || len(*queries) != 1 {
				t.Fatalf("uncovered cache did not use bounded persisted facts: result=%+v queries=%+v err=%v", out, *queries, err)
			}
		})
	}
}

func TestStoredOverviewCacheValidatesFeesAndCancellationWithoutDatabaseFallback(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	known, unknown := true, false
	zero, inf, nan := 0.0, math.Inf(1), math.NaN()
	for _, tc := range []struct {
		name      string
		fee       *float64
		available *bool
		cancel    bool
		wantError bool
	}{
		{"NULL amount", nil, &known, false, true},
		{"NULL availability", &zero, nil, false, true},
		{"infinite", &inf, &known, false, true},
		{"NaN", &nan, &known, false, true},
		{"known free", &zero, &known, false, false},
		{"known unavailable zero", &zero, &unknown, false, false},
		{"cancelled", &zero, &known, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDatabase(t)
			now := time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC)
			start, end := now.Add(-55*time.Minute), now.Add(-5*time.Minute)
			if err := db.Create(&entities.UsageEvent{EventKey: "strict", APIGroupKey: "selected", Timestamp: start.Add(time.Minute),
				TotalTokens: 10, CostUSD: tc.fee, CostAvailable: tc.available}).Error; err != nil {
				t.Fatal(err)
			}
			cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cache.Close)
			// Make DB fallback observably wrong for invalid cached rows. Validation
			// must reject the selected snapshot, not silently replace it with new fees.
			if tc.wantError && !tc.cancel {
				if err := db.Model(&entities.UsageEvent{}).Where("event_key = ?", "strict").Updates(map[string]any{"cost_usd": 5.0, "cost_available": true}).Error; err != nil {
					t.Fatal(err)
				}
			}
			queries := storedOverviewRejectSideEffects(t, db)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			out, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db.WithContext(ctx), dto.UsageQueryFilter{
				Range: "custom", StartTime: &start, EndTime: &end, QueryNow: &now, APIGroupKey: "selected", EndExclusive: true,
			}, cache)
			if tc.wantError {
				if err == nil || out != nil {
					t.Fatalf("unknown/nonfinite/cancelled cache became partial success: result=%+v err=%v", out, err)
				}
			} else if err != nil || !out.CacheUsed || out.Summary.RequestCount != 1 || out.Summary.StoredCostUSD != 0 || out.Summary.StoredCostAvailable != *tc.available {
				t.Fatalf("known zero semantics changed: result=%+v err=%v", out, err)
			}
			if len(*queries) != 0 {
				t.Fatalf("cached validation silently queried replacement facts: %+v", *queries)
			}
		})
	}
}

func TestStoredOverviewCacheFallsBackAfterRejectedAppendWithoutChangingCurrentCache(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db := openTestDatabase(t)
	now := time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC)
	start, end := now.Add(-50*time.Minute), now
	fee, available := 1.0, true
	if err := db.Create(&entities.UsageEvent{EventKey: "cached", APIGroupKey: "selected", Timestamp: now.Add(-30 * time.Minute),
		TotalTokens: 10, CostUSD: &fee, CostAvailable: &available}).Error; err != nil {
		t.Fatal(err)
	}
	cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return now }, QueueSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Close)
	fee = 2
	missed := entities.UsageEvent{EventKey: "missed", APIGroupKey: "selected", Timestamp: now.Add(-10 * time.Minute),
		TotalTokens: 10, CostUSD: &fee, CostAvailable: &available}
	if err := db.Create(&missed).Error; err != nil {
		t.Fatal(err)
	}
	// Reuse the existing bounded-queue fixture control, but observe behavior only
	// through TryAppend, the real stored reader, and the unchanged current Events.
	slots := *recentCacheField[chan struct{}](cache, "appendSlots")
	<-slots
	accepted := cache.TryAppend([]entities.UsageEvent{missed})
	slots <- struct{}{}
	if accepted {
		t.Fatal("saturated queue accepted the committed batch")
	}
	queries := storedOverviewRejectSideEffects(t, db)
	out, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, dto.UsageQueryFilter{
		Range: "custom", StartTime: &start, EndTime: &end, QueryNow: &now, APIGroupKey: "selected", EndExclusive: true,
	}, cache)
	if err != nil || out.CacheUsed || out.Summary.RequestCount != 2 || out.Summary.TokenCount != 20 || out.Summary.StoredCostUSD != 3 || !out.Summary.StoredCostAvailable {
		t.Fatalf("rejected append produced a partial cached success: result=%+v err=%v", out, err)
	}
	if len(*queries) != 1 {
		t.Fatalf("untrusted cache must use exactly the bounded raw query: %+v", *queries)
	}
	current, ok := cache.Events(start, end, false, "selected")
	if !ok || len(current) != 1 || current[0].StoredCostUSD == nil || *current[0].StoredCostUSD != 1 {
		t.Fatalf("stored safety policy invalidated or rewrote ordinary cache reads: %+v %v", current, ok)
	}
}

func TestStoredOverviewCacheFallsBackWhileDequeuedBatchIsApplying(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db := openTestDatabase(t)
	now := time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC)
	start, end := now.Add(-50*time.Minute), now
	fee, available := 1.0, true
	if err := db.Create(&entities.UsageEvent{EventKey: "cached", Timestamp: start.Add(time.Minute), TotalTokens: 10, CostUSD: &fee, CostAvailable: &available}).Error; err != nil {
		t.Fatal(err)
	}
	cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return now }, QueueSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Close)
	fee = 2
	committed := entities.UsageEvent{EventKey: "applying", Timestamp: end.Add(-time.Minute), TotalTokens: 10, CostUSD: &fee, CostAvailable: &available}
	if err := db.Create(&committed).Error; err != nil {
		t.Fatal(err)
	}
	// Pause the real worker at its apply lock. It already returns the queue slot
	// before applying a batch, so an empty queue alone is not coverage evidence.
	mu := recentCacheField[sync.RWMutex](cache, "mu")
	mu.Lock()
	locked := true
	defer func() {
		if locked {
			mu.Unlock()
		}
	}()
	if !cache.TryAppend([]entities.UsageEvent{committed}) {
		t.Fatal("batch was not accepted")
	}
	slots := *recentCacheField[chan struct{}](cache, "appendSlots")
	deadline := time.Now().Add(2 * time.Second)
	for len(slots) != cap(slots) {
		if time.Now().After(deadline) {
			t.Fatal("worker did not dequeue the accepted batch")
		}
		time.Sleep(time.Millisecond)
	}
	type readResult struct {
		out *dto.StoredUsageOverviewRecord
		err error
	}
	done := make(chan readResult, 1)
	go func() {
		out, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, dto.UsageQueryFilter{
			Range: "custom", StartTime: &start, EndTime: &end, QueryNow: &now, EndExclusive: true,
		}, cache)
		done <- readResult{out, err}
	}()
	var result readResult
	select {
	case result = <-done:
	case <-time.After(2 * time.Second):
		t.Error("stored read waited on a dequeued cache batch instead of using committed DB facts")
		mu.Unlock()
		locked = false
		result = <-done
	}
	if result.err != nil || result.out.CacheUsed || result.out.Summary.RequestCount != 2 || result.out.Summary.TokenCount != 20 || result.out.Summary.StoredCostUSD != 3 || !result.out.Summary.StoredCostAvailable {
		t.Fatalf("applying cache batch yielded incomplete coverage: result=%+v err=%v", result.out, result.err)
	}
	mu.Unlock()
	locked = false
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := cache.DrainAcceptedAppends(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DROP TABLE usage_events").Error; err != nil {
		t.Fatal(err)
	}
	out, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, dto.UsageQueryFilter{
		Range: "custom", StartTime: &start, EndTime: &end, QueryNow: &now, EndExclusive: true,
	}, cache)
	if err != nil || !out.CacheUsed || out.Summary.RequestCount != 2 || out.Summary.TokenCount != 20 || out.Summary.StoredCostUSD != 3 || !out.Summary.StoredCostAvailable {
		t.Fatalf("fully applied cache did not regain complete coverage: result=%+v err=%v", out, err)
	}
}
