package test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
)

func TestCurrentRepeatedHourRetainedEvidenceKeepsReferenceAndOverrides(t *testing.T) {
	for _, zone := range []string{"Europe/Berlin", "UTC"} {
		t.Run(zone, func(t *testing.T) {
			for _, mode := range []string{"baseline", "credential-default", "fixed-override"} {
				t.Run(mode, func(t *testing.T) {
					location := setAnalysisLatencyTestTimezone(t, zone)
					parse := func(s string) time.Time {
						at, err := time.Parse(time.RFC3339Nano, s)
						if err != nil {
							t.Fatal(err)
						}
						return at.In(location)
					}
					start := parse("2026-10-25T02:00:00+02:00")
					if zone == "UTC" {
						start = parse("2026-10-25T10:00:00Z")
					}
					firstEvent := start.Add(10 * time.Minute)
					if zone == "UTC" {
						firstEvent = start.Add(100 * time.Millisecond)
					}
					now := start.Add(48 * time.Hour)
					db := openTestDatabase(t)
					if err := db.Create(&entities.CPAAPIKey{APIKey: "selected"}).Error; err != nil {
						t.Fatal(err)
					}
					fee, available := 0.5, true
					events := []entities.UsageEvent{
						{EventKey: "first-repeated-evidence", Timestamp: firstEvent, APIGroupKey: "selected", AuthType: "oauth", AuthIndex: "evidence-auth", Model: "model-a", InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: &fee, CostAvailable: &available},
						{EventKey: "second-repeated-evidence", Timestamp: firstEvent.Add(time.Hour), APIGroupKey: "selected", AuthType: "oauth", AuthIndex: "evidence-auth", Model: "model-a", InputTokens: 2_000_000, TotalTokens: 2_000_000, CostUSD: &fee, CostAvailable: &available},
						{EventKey: "other-key-evidence", Timestamp: firstEvent, APIGroupKey: "other", AuthType: "oauth", AuthIndex: "evidence-auth", Model: "model-a", InputTokens: 9_000_000, TotalTokens: 9_000_000, CostUSD: &fee, CostAvailable: &available},
					}
					if err := db.Create(&events).Error; err != nil {
						t.Fatal(err)
					}
					if err := repository.AggregateUsageOverviewStats(context.Background(), db, now); err != nil {
						t.Fatal(err)
					}
					checkpoint, err := repository.LoadUsageAggregationCheckpointSnapshot(context.Background(), db)
					if err != nil || checkpoint.OverviewCursor != events[2].ID {
						t.Fatalf("fixture lacks complete committed prefix: %+v %v", checkpoint, err)
					}
					one, zero, five := 1.0, 0.0, 5.0
					models := []pricing.ModelConfig{{Pricing: entities.ModelPriceSetting{Model: "model-a", PricingStyle: entities.ModelPricingStyleOpenAI, PromptPricePer1M: 9, PriceMultiplier: &one}}}
					bindings := []pricing.CredentialBinding{{SubjectID: "evidence-subject", AuthType: "oauth", AuthIndex: "evidence-auth", Status: "unique"}}
					var defaults []pricing.CredentialConfig
					var overrides pricing.OverrideConfig
					configuredPerMillion := 9.0
					if mode == "credential-default" {
						defaults = []pricing.CredentialConfig{{SubjectID: "evidence-subject", Multiplier: 2}}
						configuredPerMillion = 18
					}
					if mode == "fixed-override" {
						overrides.CredentialModels = []pricing.CredentialModelConfig{{SubjectID: "evidence-subject", Model: "model-a", Mode: pricing.ModeFixed, Fixed: &pricing.FixedTariff{PricingStyle: entities.ModelPricingStyleOpenAI, PromptPricePer1M: &five, CompletionPricePer1M: &zero, CacheReadPricePer1M: &zero, CacheWritePricePer1M: &zero}}}
						configuredPerMillion = 5
					}
					snapshot, err := pricing.CompileSnapshotWithCredentials(models, bindings, defaults, overrides)
					if err != nil {
						t.Fatal(err)
					}
					resolver := pricing.NewCatalog(snapshot).NewResolver()
					for index := 0; index < 2; index++ {
						t.Run(fmt.Sprintf("hour-%d", index+1), func(t *testing.T) {
							lo := start.Add(time.Duration(index) * time.Hour)
							hi := lo.Add(time.Hour)
							filter := dto.UsageQueryFilter{Range: "custom", CustomUnit: "hour", StartTime: &lo, EndTime: &hi, QueryNow: &now, EndExclusive: true, APIGroupKey: "selected"}
							wantTokens := int64(index+1) * 1_000_000
							wantConfigured := configuredPerMillion * float64(index+1)
							wantReference := 9.0 * float64(index+1)
							analysis, err := repository.BuildAnalysisWithFilter(db, filter, resolver)
							if err != nil || len(analysis.TokenUsage) != 1 || analysis.TokenUsage[0].Requests != 1 || analysis.TokenUsage[0].TotalTokens != wantTokens || analysis.TokenUsage[0].Bucket.Unix() != lo.Unix() {
								t.Fatalf("current analysis lost hour facts/scope: %+v %v", analysis, err)
							}
							if !analysis.CostBreakdown.CostAvailable {
								t.Errorf("exact retained configured evidence missing: %+v", analysis.CostBreakdown)
							} else {
								assertFloatClose(t, analysis.CostBreakdown.TotalCostUSD, wantConfigured)
							}
							assertWindowEstimate(t, analysis.CostBreakdown.DualCosts.Reference, "complete", &wantReference)
							comparison, err := repository.BuildUsageOverviewComparisonsWithFilterAndRecentCache(db, filter, nil, resolver)
							if err != nil {
								t.Fatal(err)
							}
							item := comparison.Comparisons.Models["model-a"]
							if len(comparison.Comparisons.Models) != 1 || len(comparison.Comparisons.APIKeys) != 1 || item == nil || item.Requests != 1 || item.TotalTokens != wantTokens || !item.CostAvailable || item.TokenBuckets[lo.Format(time.RFC3339Nano)] != wantTokens {
								t.Fatalf("current comparison lost exact hour facts/selection: %+v", comparison.Comparisons)
							}
							assertFloatClose(t, item.CostUSD, wantConfigured)
							assertWindowEstimate(t, item.DualCosts.Reference, "complete", &wantReference)
							stored, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, filter, nil)
							if err != nil || stored.Summary.RequestCount != 1 || stored.Summary.TokenCount != wantTokens || stored.Summary.StoredCostUSD != fee || !stored.Summary.StoredCostAvailable {
								t.Fatalf("current evidence read changed stored amounts/facts: %+v %v", stored, err)
							}
						})
					}
				})
			}
		})
	}
}

func TestCurrentTodayComparisonLateCachePreservesFullCalendarAxis(t *testing.T) {
	for _, zone := range []string{"UTC", "Europe/Berlin"} {
		t.Run(zone, func(t *testing.T) {
			location := setAnalysisLatencyTestTimezone(t, zone)
			start := time.Date(2026, 10, 25, 0, 0, 0, 0, location)
			end := start.AddDate(0, 0, 1)
			now := time.Date(2026, 10, 25, 12, 59, 59, 0, location)
			latest := now.Add(2 * time.Second)
			db := openTestDatabase(t)
			fee, available := 0.25, true
			events := []entities.UsageEvent{
				{EventKey: "before-query-now", Timestamp: now.Add(-time.Second), APIGroupKey: "selected", Model: "model-a", InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: &fee, CostAvailable: &available},
				{EventKey: "after-query-now", Timestamp: latest, APIGroupKey: "selected", Model: "model-a", InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: &fee, CostAvailable: &available},
				{EventKey: "other-key", Timestamp: latest, APIGroupKey: "other", Model: "other-model", InputTokens: 9_000_000, TotalTokens: 9_000_000, CostUSD: &fee, CostAvailable: &available},
			}
			if err := db.Create(&events).Error; err != nil {
				t.Fatal(err)
			}
			cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return latest }})
			if err != nil {
				t.Fatal(err)
			}
			defer cache.Close()
			multiplier := 1.0
			snapshot, err := pricing.CompileSnapshot([]pricing.ModelConfig{{Pricing: entities.ModelPriceSetting{Model: "model-a", PricingStyle: entities.ModelPricingStyleOpenAI, PromptPricePer1M: 9, PriceMultiplier: &multiplier}}})
			if err != nil {
				t.Fatal(err)
			}
			resolver := pricing.NewCatalog(snapshot).NewResolver()
			filter := dto.UsageQueryFilter{Range: "today", StartTime: &start, EndTime: &end, QueryNow: &now, APIGroupKey: "selected"}
			got, err := repository.BuildUsageOverviewComparisonsWithFilterAndRecentCache(db, filter, cache, resolver)
			if err != nil {
				t.Fatal(err)
			}
			comparison := got.Comparisons
			wantBuckets := 25
			if zone == "Europe/Berlin" {
				wantBuckets = 26 // repeated DST hour plus the empty next midnight
			}
			if len(comparison.Buckets) != wantBuckets || comparison.Buckets[0] != start.Format(time.RFC3339Nano) || comparison.Buckets[len(comparison.Buckets)-1] != end.Format(time.RFC3339Nano) {
				t.Fatalf("late cache event truncated full %s calendar axis: got %v, want %d buckets through %s", zone, comparison.Buckets, wantBuckets, end.Format(time.RFC3339Nano))
			}
			model := comparison.Models["model-a"]
			if len(comparison.Models) != 1 || len(comparison.APIKeys) != 1 || model == nil || model.Requests != 2 || model.TotalTokens != 2_000_000 || !model.CostAvailable {
				t.Fatalf("calendar axis repair lost latest facts or key scope: %+v", comparison)
			}
			assertFloatClose(t, model.CostUSD, 18)
			if model.TokenBuckets[now.Truncate(time.Hour).Format(time.RFC3339Nano)] != 1_000_000 || model.TokenBuckets[latest.Truncate(time.Hour).Format(time.RFC3339Nano)] != 1_000_000 {
				t.Fatalf("late cache event lost its own hour: %+v", model.TokenBuckets)
			}
			var tokens int64
			for _, bucket := range comparison.Buckets {
				tokens += model.TokenBuckets[bucket]
			}
			if tokens != 2_000_000 {
				t.Fatal("calendar axis does not cover all counted tokens", tokens)
			}
			main, err := repository.BuildUsageOverviewWithFilterAndRecentCache(db, filter, cache, resolver)
			if err != nil || main.Usage.TotalRequests != 2 || main.Usage.TotalTokens != 2_000_000 || !main.Summary.CostAvailable {
				t.Fatalf("calendar comparison diverged from ordinary current facts: %+v %v", main, err)
			}
			assertFloatClose(t, main.Summary.TotalCost, 18)
			var persisted []entities.UsageEvent
			if err := db.Find(&persisted).Error; err != nil || len(persisted) != 3 {
				t.Fatalf("calendar read changed event facts: %+v %v", persisted, err)
			}
			for _, event := range persisted {
				if event.CostUSD == nil || *event.CostUSD != 0.25 || event.CostAvailable == nil || !*event.CostAvailable {
					t.Fatal("current calendar read rewrote stored fee", event)
				}
			}
		})
	}
}

// These are real current overview/comparison readers, not stored substitutes.
// The same time facts must survive offset/fractional text ordering while current
// valuation follows a changed catalog and persisted valuation remains fixed.
func TestCurrentOverviewAndComparisonTimeEdgesKeepSeparateValuations(t *testing.T) {
	for _, tc := range []struct {
		name, zone, start, end string
		events                 []string
		closed                 bool
		want                   int64
	}{
		{"UTC whole-second lower", "UTC", "2026-10-25T01:30:00Z", "2026-10-25T01:31:00Z", []string{"2026-10-25T01:29:59.9Z", "2026-10-25T01:30:00Z", "2026-10-25T01:30:00.1Z", "2026-10-25T01:31:00Z", "2026-10-25T01:31:00.1Z"}, false, 2},
		{"UTC fractional open edges", "UTC", "2026-10-25T01:30:00.5Z", "2026-10-25T01:31:00.5Z", []string{"2026-10-25T01:30:00Z", "2026-10-25T01:30:00.4Z", "2026-10-25T01:30:00.5Z", "2026-10-25T01:31:00Z", "2026-10-25T01:31:00.5Z"}, false, 2},
		{"UTC fractional closed edge", "UTC", "2026-10-25T01:30:00.5Z", "2026-10-25T01:31:00.5Z", []string{"2026-10-25T01:30:00.4Z", "2026-10-25T01:30:00.5Z", "2026-10-25T01:31:00.5Z", "2026-10-25T01:31:00.6Z"}, true, 2},
		{"Berlin first repeated hour", "Europe/Berlin", "2026-10-25T01:30:00+02:00", "2026-10-25T02:30:00+02:00", []string{"2026-10-25T02:10:00+02:00", "2026-10-25T02:10:00+01:00"}, false, 1},
		{"London backward clock fractions", "Europe/London", "2026-10-25T01:30:00+01:00", "2026-10-25T01:30:00Z", []string{"2026-10-25T01:00:00Z", "2026-10-25T01:00:00.1Z", "2026-10-25T01:00:00.999999999Z", "2026-10-25T01:30:00Z"}, false, 3},
		{"Shanghai subsecond edges", "Asia/Shanghai", "2026-10-25T02:30:00.5+08:00", "2026-10-25T03:30:00.5+08:00", []string{"2026-10-25T02:30:00.4+08:00", "2026-10-25T02:30:00.5+08:00", "2026-10-25T03:30:00.4+08:00", "2026-10-25T03:30:00.5+08:00"}, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			location := setAnalysisLatencyTestTimezone(t, tc.zone)
			parse := func(value string) time.Time {
				at, err := time.Parse(time.RFC3339Nano, value)
				if err != nil {
					t.Fatal(err)
				}
				return at.In(location)
			}
			start, end := parse(tc.start), parse(tc.end)
			now := end.Add(48 * time.Hour)
			db := openTestDatabase(t)
			fee, available := 0.5, true
			for i, value := range tc.events {
				if err := db.Create(&entities.UsageEvent{EventKey: fmt.Sprintf("time-edge-%d", i), APIGroupKey: "selected", Model: "model-a", Timestamp: parse(value), InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: &fee, CostAvailable: &available}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Create(&entities.UsageEvent{EventKey: "other-key", APIGroupKey: "other", Model: "model-a", Timestamp: start, InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: &fee, CostAvailable: &available}).Error; err != nil {
				t.Fatal(err)
			}
			filter := dto.UsageQueryFilter{Range: "custom", StartTime: &start, EndTime: &end, EndExclusive: !tc.closed, QueryNow: &now, APIGroupKey: "selected"}
			for _, price := range []float64{9, 90} {
				multiplier := 1.0
				snapshot, err := pricing.CompileSnapshot([]pricing.ModelConfig{{Pricing: entities.ModelPriceSetting{Model: "model-a", PricingStyle: entities.ModelPricingStyleOpenAI, PromptPricePer1M: price, PriceMultiplier: &multiplier}}})
				if err != nil {
					t.Fatal(err)
				}
				resolver := pricing.NewCatalog(snapshot).NewResolver()
				current, err := repository.BuildUsageOverviewWithFilterAndRecentCache(db, filter, nil, resolver)
				if err != nil {
					t.Fatal(err)
				}
				if current.Usage.TotalRequests != tc.want || current.Usage.TotalTokens != tc.want*1_000_000 || !current.Summary.CostAvailable {
					t.Fatalf("current overview time facts changed: usage=%+v summary=%+v", current.Usage, current.Summary)
				}
				assertFloatClose(t, current.Summary.TotalCost, float64(tc.want)*price)
				comparisons, err := repository.BuildUsageOverviewComparisonsWithFilterAndRecentCache(db, filter, nil, resolver)
				if err != nil {
					t.Fatal(err)
				}
				model := comparisons.Comparisons.Models["model-a"]
				if model == nil || model.Requests != tc.want || model.TotalTokens != tc.want*1_000_000 || !model.CostAvailable {
					t.Fatalf("current comparison time facts changed: %+v", comparisons.Comparisons)
				}
				assertFloatClose(t, model.CostUSD, float64(tc.want)*price)
				// Stored reads are explicitly half-open, while the legacy current
				// reader also accepts a closed upper edge. Compare the same facts
				// by representing that closed instant as a half-open nanosecond.
				storedFilter := filter
				storedFilter.EndExclusive = true
				if tc.closed {
					storedEnd := end.Add(time.Nanosecond)
					storedFilter.EndTime = &storedEnd
				}
				stored, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, storedFilter, nil)
				if err != nil {
					t.Fatal(err)
				}
				if stored.Usage.TotalRequests != tc.want || stored.Usage.TotalTokens != tc.want*1_000_000 || !stored.Summary.StoredCostAvailable {
					t.Fatalf("stored/current time facts disagree: %+v", stored)
				}
				assertFloatClose(t, stored.Summary.StoredCostUSD, float64(tc.want)*0.5)
			}
			var rows []entities.UsageEvent
			if err := db.Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.CostUSD == nil || *row.CostUSD != 0.5 || row.CostAvailable == nil || !*row.CostAvailable {
					t.Fatalf("read rewrote historical fee: %+v", row)
				}
			}
		})
	}
}
