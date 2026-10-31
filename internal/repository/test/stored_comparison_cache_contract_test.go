package test

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
)

func TestStoredComparisonCacheKeepsScopedHalfOpenFeesAndExactIdentities(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db := openTestDatabase(t)
	now := time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC)
	start, end := now.Add(-55*time.Minute), now.Add(-5*time.Minute)
	for _, identity := range []entities.UsageIdentity{
		{AuthType: entities.UsageIdentityAuthTypeCredential, Identity: "auth", Name: "same"},
		{AuthType: entities.UsageIdentityAuthTypeCredential, Identity: " auth ", Name: "exact"},
		{AuthType: entities.UsageIdentityAuthTypeAIProvider, Identity: "auth", Name: "same"},
	} {
		if err := db.Create(&identity).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		name, key, kind, index string
		at                     time.Time
		fee                    float64
		available              bool
	}{
		{"before", "selected", "oauth", "auth", start.Add(-time.Nanosecond), 99, true},
		{"credential", "selected", "oauth", "auth", start, 1.25, true},
		{"provider", "selected", "apikey", "auth", start.Add(time.Minute), 0.75, false},
		{"exact", "selected", "oauth", " auth ", start.Add(2 * time.Minute), 0.5, true},
		{"untyped", "selected", "", "auth", end.Add(-time.Nanosecond), 0, true},
		{"end", "selected", "oauth", "auth", end, 99, true},
		{"other", "other", "oauth", "auth", start, 99, true},
	} {
		fee, available := row.fee, row.available
		if err := db.Create(&entities.UsageEvent{EventKey: row.name, APIGroupKey: row.key, Model: "model-a", Timestamp: row.at,
			AuthType: row.kind, AuthIndex: row.index, InputTokens: 8, OutputTokens: 2, TotalTokens: 10, CostUSD: &fee, CostAvailable: &available}).Error; err != nil {
			t.Fatal(err)
		}
	}
	cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Close)
	if err := db.Exec("DROP TABLE usage_events").Error; err != nil {
		t.Fatal(err)
	}
	queries := storedOverviewRejectSideEffects(t, db)
	filter := dto.UsageQueryFilter{Range: "custom", StartTime: &start, EndTime: &end, QueryNow: &now, EndExclusive: true, APIGroupKey: "selected"}
	reader, err := repository.NewStoredUsageComparisonReaderWithRecentCache(db, cache)
	if err != nil {
		t.Fatal(err)
	}
	out, err := reader.Read(filter)
	if err != nil {
		t.Fatal(err)
	}
	model := out.Models["model-a"]
	if !out.CacheUsed || model == nil || model.Requests != 4 || model.TotalTokens != 40 || model.StoredCostUSD != 2.5 || model.StoredCostAvailable || len(out.APIKeys) != 1 || out.APIKeys["selected"].Requests != 4 {
		t.Fatalf("scoped half-open cached totals changed: %+v", out)
	}
	if len(out.Credentials) != 2 || len(out.AuthFiles) != 2 || len(out.AIProviders) != 1 || out.Credentials["auth"].StoredCostUSD != 1.25 || out.Credentials[" auth "].StoredCostUSD != 0.5 || out.AIProviders["auth"].StoredCostUSD != 0.75 || out.AIProviders["auth"].StoredCostAvailable || out.IdentityAttributionComplete || out.UnattributedRequests != 1 {
		t.Fatalf("cache normalized or guessed an exact typed identity: %+v", out)
	}
	if len(*queries) != 1 || !strings.Contains((*queries)[0], "usage_identities") {
		t.Fatalf("cached edge queried raw events or prices: %+v", *queries)
	}
	filter.APIGroupKey = "empty"
	empty, err := reader.Read(filter)
	if err != nil || !empty.CacheUsed || len(empty.Models) != 0 || len(empty.APIKeys) != 0 {
		t.Fatalf("covered empty scope: %+v %v", empty, err)
	}
	uncached, err := repository.NewStoredUsageComparisonReader(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uncached.Read(filter); err == nil {
		t.Fatal("default reader silently enabled cache")
	}
}

func TestStoredComparisonCacheFallbackRejectsGapsAndUnavailableCoverage(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	for _, name := range []string{"nil", "closed", "uncovered", "pruned clock", "reload unavailable", "pending", "rejected append"} {
		t.Run(name, func(t *testing.T) {
			db := openTestDatabase(t)
			now := time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC)
			start, end := now.Add(-55*time.Minute), now.Add(-5*time.Minute)
			fee, available := 0.625, true
			if err := db.Create(&entities.UsageEvent{EventKey: "cached", Model: "model-a", APIGroupKey: "selected", Timestamp: start.Add(time.Minute), TotalTokens: 100, CostUSD: &fee, CostAvailable: &available}).Error; err != nil {
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
			wantRequests, wantTokens, wantFee := int64(1), int64(100), 0.625
			switch name {
			case "closed":
				cache.Close()
			case "reload unavailable":
				if err := cache.ReloadStoredCostEvents(context.Background(), nil); err == nil {
					t.Fatal("invalid reload succeeded")
				}
			case "pending":
				slots := *recentCacheField[chan struct{}](cache, "appendSlots")
				<-slots
				defer func() { slots <- struct{}{} }()
			case "rejected append":
				missedFee := 0.375
				missed := entities.UsageEvent{EventKey: "committed-missed", Model: "model-a", APIGroupKey: "selected", Timestamp: end.Add(-time.Minute), TotalTokens: 10, CostUSD: &missedFee, CostAvailable: &available}
				if err := db.Create(&missed).Error; err != nil {
					t.Fatal(err)
				}
				slots := *recentCacheField[chan struct{}](cache, "appendSlots")
				<-slots
				accepted := cache.TryAppend([]entities.UsageEvent{missed})
				slots <- struct{}{}
				if accepted {
					t.Fatal("saturated queue accepted committed batch")
				}
				wantRequests, wantTokens, wantFee = 2, 110, 1
				ordinary, ok := cache.Events(start, end, false, "selected")
				if !ok || len(ordinary) != 1 {
					t.Fatalf("strict policy changed ordinary cache: %+v %v", ordinary, ok)
				}
			}
			if name == "nil" {
				cache = nil
			}
			queries := storedOverviewRejectSideEffects(t, db)
			reader, err := repository.NewStoredUsageComparisonReaderWithRecentCache(db, cache)
			if err != nil {
				t.Fatal(err)
			}
			out, err := reader.Read(dto.UsageQueryFilter{Range: "custom", StartTime: &start, EndTime: &end, QueryNow: &now, APIGroupKey: "selected", EndExclusive: true})
			if err != nil {
				t.Fatal(err)
			}
			model := out.Models["model-a"]
			if out.CacheUsed || model == nil || model.Requests != wantRequests || model.TotalTokens != wantTokens || model.StoredCostUSD != wantFee || !model.StoredCostAvailable || len(*queries) != 1 || !strings.Contains((*queries)[0], "usage_events") {
				t.Fatalf("untrusted cache yielded partial success: result=%+v model=%+v queries=%+v", out, model, *queries)
			}
		})
	}
}

func TestStoredComparisonCacheStrictFeesNeverReadReplacementPrices(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	known, unknown := true, false
	zero, nonzero, inf, nan := 0.0, 0.75, math.Inf(1), math.NaN()
	for _, tc := range []struct {
		name              string
		fee               *float64
		available         *bool
		cancel, wantError bool
	}{
		{"NULL amount", nil, &known, false, true},
		{"NULL availability", &zero, nil, false, true},
		{"infinite", &inf, &known, false, true},
		{"NaN", &nan, &known, false, true},
		{"known free", &zero, &known, false, false},
		{"known unavailable zero", &zero, &unknown, false, false},
		{"known unavailable numeric", &nonzero, &unknown, false, false},
		{"cancelled", &zero, &known, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDatabase(t)
			now := time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC)
			start, end := now.Add(-55*time.Minute), now.Add(-5*time.Minute)
			if err := db.Create(&entities.UsageEvent{EventKey: "strict", Model: "model-a", APIGroupKey: "selected", Timestamp: start.Add(time.Minute), TotalTokens: 10, CostUSD: tc.fee, CostAvailable: tc.available}).Error; err != nil {
				t.Fatal(err)
			}
			cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cache.Close)
			// A DB replacement is valid but wrong: invalid cache rows must fail,
			// not silently become this newly written numeric amount.
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
			reader, err := repository.NewStoredUsageComparisonReaderWithRecentCache(db.WithContext(ctx), cache)
			if err != nil {
				t.Fatal(err)
			}
			out, err := reader.Read(dto.UsageQueryFilter{Range: "custom", StartTime: &start, EndTime: &end, QueryNow: &now, APIGroupKey: "selected", EndExclusive: true})
			if tc.wantError {
				if err == nil || out != nil {
					t.Fatalf("invalid cached cost became a partial success: %+v %v", out, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				model := out.Models["model-a"]
				if !out.CacheUsed || model == nil || model.Requests != 1 || model.StoredCostUSD != *tc.fee || model.StoredCostAvailable != *tc.available {
					t.Fatalf("known cached fee changed: %+v", out)
				}
			}
			if len(*queries) != 0 {
				t.Fatalf("cached validation queried replacement facts/prices: %+v", *queries)
			}
		})
	}
}
