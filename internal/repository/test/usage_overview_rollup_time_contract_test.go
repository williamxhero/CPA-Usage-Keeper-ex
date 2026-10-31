package test

import (
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
)

// Retained rollups must keep their time facts while configured prices remain
// current. Persisted fees are deliberately different and never price the current
// reader. No hot events are available to hide a broken rollup query.
func TestCurrentOverviewRollupOffsetsKeepFactsAndPrices(t *testing.T) {
	for _, tc := range []struct{ zone, start string }{
		{"Europe/Berlin", "2026-10-25T02:00:00+02:00"},
		{"Asia/Shanghai", "2026-10-25T02:00:00+08:00"},
		{"Asia/Kathmandu", "2026-10-25T02:45:00+05:45"},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			loc := setAnalysisLatencyTestTimezone(t, tc.zone)
			start, err := time.Parse(time.RFC3339, tc.start)
			if err != nil {
				t.Fatal(err)
			}
			start = start.In(loc)
			end := start.Add(6 * time.Hour)
			db := openTestDatabase(t)
			fee, unavailable := 0.5, int64(0)
			for i := -1; i <= 6; i++ {
				if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: start.Add(time.Duration(i) * time.Hour), APIGroupKey: "selected", Model: "model-a", RequestCount: 1, SuccessCount: 1,
					InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: &fee, UnavailableCostCount: &unavailable}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: start, APIGroupKey: "other", Model: "model-a", RequestCount: 99, InputTokens: 99_000_000, TotalTokens: 99_000_000, CostUSD: &fee, UnavailableCostCount: &unavailable}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("DROP TABLE usage_events").Error; err != nil {
				t.Fatal(err)
			}
			filter := dto.UsageQueryFilter{Range: "custom", CustomUnit: "hour", APIGroupKey: "selected", StartTime: &start, EndTime: &end, EndExclusive: true, QueryNow: &end}
			for _, price := range []float64{9, 90} {
				multiplier := 1.0
				snapshot, err := pricing.CompileSnapshot([]pricing.ModelConfig{{Pricing: entities.ModelPriceSetting{Model: "model-a", PricingStyle: entities.ModelPricingStyleOpenAI, PromptPricePer1M: price, PriceMultiplier: &multiplier}}})
				if err != nil {
					t.Fatal(err)
				}
				resolver := pricing.NewCatalog(snapshot).NewResolver()
				current, err := repository.BuildUsageOverviewWithFilterAndRecentCache(db, filter, nil, resolver)
				if err != nil || current.Usage.TotalRequests != 6 || current.Usage.TotalTokens != 6_000_000 || len(current.Series.Tokens) != 6 || !current.Summary.CostAvailable {
					t.Fatalf("current retained rollup time facts changed: result=%+v err=%v", current, err)
				}
				assertFloatClose(t, current.Summary.TotalCost, 6*price)
				comparison, err := repository.BuildUsageOverviewComparisonsWithFilterAndRecentCache(db, filter, nil, resolver)
				if err != nil {
					t.Fatal(err)
				}
				model := comparison.Comparisons.Models["model-a"]
				if model == nil || model.Requests != 6 || model.TotalTokens != 6_000_000 || len(model.TokenBuckets) != 6 || !model.CostAvailable {
					t.Fatalf("current comparison retained facts changed: %+v", comparison.Comparisons)
				}
				assertFloatClose(t, model.CostUSD, 6*price)
				stored, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, filter, nil)
				if err != nil || stored.Summary.RequestCount != 6 || stored.Summary.TokenCount != 6_000_000 || !stored.Summary.StoredCostAvailable {
					t.Fatalf("stored retained rollups differ from current facts: result=%+v err=%v", stored, err)
				}
				assertFloatClose(t, stored.Summary.StoredCostUSD, 3)
			}
		})
	}
}
