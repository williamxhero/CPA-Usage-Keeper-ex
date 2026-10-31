package test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func TestStoredOverviewServiceCacheKeepsCurrentPricingAndExactScopedAmountsSeparate(t *testing.T) {
	withUsageServiceLocation(t, "UTC")
	db := openUsageServiceTestDatabase(t)
	key := entities.CPAAPIKey{APIKey: "selected"}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC)
	start, end := now.Add(-55*time.Minute), now.Add(-5*time.Minute)
	cache := newServiceRecentCacheFromEvents(t, db, now, []entities.UsageEvent{
		storedUsageEventFee(entities.UsageEvent{EventKey: "selected", APIGroupKey: key.APIKey, Model: "model-a", Timestamp: start.Add(time.Minute), InputTokens: 1_000_000, TotalTokens: 1_000_000}, 1.25, true),
		storedUsageEventFee(entities.UsageEvent{EventKey: "other", APIGroupKey: "other", Model: "model-a", Timestamp: start.Add(time.Minute), InputTokens: 1_000_000, TotalTokens: 1_000_000}, 99, true),
	})
	if err := db.Exec("DROP TABLE usage_events").Error; err != nil {
		t.Fatal(err)
	}
	provider := service.NewStoredUsageOverviewServiceWithRecentCache(db, cache)
	filter := servicedto.StoredUsageOverviewFilter{Range: "custom", StartTime: &start, EndTime: &end, EndExclusive: true, QueryNow: &now, APIKeyID: strconv.FormatInt(key.ID, 10)}
	for _, price := range []float64{9, 90} {
		multiplier := 1.0
		snapshot, err := pricing.CompileSnapshot([]pricing.ModelConfig{{Pricing: entities.ModelPriceSetting{Model: "model-a", PricingStyle: entities.ModelPricingStyleOpenAI, PromptPricePer1M: price, PriceMultiplier: &multiplier}}})
		if err != nil {
			t.Fatal(err)
		}
		current, err := service.NewUsageServiceWithRecentCache(db, cache, pricing.NewCatalog(snapshot)).GetUsageOverview(context.Background(), servicedto.UsageFilter{
			Range: "custom", StartTime: &start, EndTime: &end, EndExclusive: true, QueryNow: &now, APIKeyID: filter.APIKeyID,
		})
		if err != nil || current.Usage.TotalRequests != 1 || current.Usage.TotalTokens != 1_000_000 || !current.Summary.CostAvailable || !usageFilterCostClose(current.Summary.TotalCost, price) {
			t.Fatalf("current cache path stopped valuing at current price %g: result=%+v err=%v", price, current, err)
		}
		stored, err := provider.GetStoredUsageOverview(context.Background(), filter)
		if err != nil || !stored.Coverage.CacheUsed || stored.Coverage.ArchiveCoverageAsserted || stored.Summary.RequestCount != 1 || stored.Summary.TokenCount != 1_000_000 || !stored.Summary.StoredCostAvailable || stored.Summary.StoredCostUSD != 1.25 {
			t.Fatalf("explicit stored cache path repriced or escaped key scope: result=%+v err=%v", stored, err)
		}
	}
	if _, err := service.NewStoredUsageOverviewService(db).GetStoredUsageOverview(context.Background(), filter); err == nil {
		t.Fatal("default stored service unexpectedly acquired the optional cache")
	}
	filter.APIKeyID = "invalid"
	if _, err := provider.GetStoredUsageOverview(context.Background(), filter); !errors.Is(err, service.ErrInvalidID) {
		t.Fatalf("cached stored scope bypassed API key validation: %v", err)
	}
	filter.APIKeyID = ""
	longEnd := start.Add(25 * time.Hour)
	filter.EndTime = &longEnd
	if _, err := provider.GetStoredUsageOverview(context.Background(), filter); err == nil {
		t.Fatal("internal exact stored range accepted an unbounded interval")
	}
}
