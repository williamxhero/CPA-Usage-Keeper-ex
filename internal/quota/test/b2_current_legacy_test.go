package test

import (
	"context"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/helper"
	"cpa-usage-keeper/internal/pricing"
	. "cpa-usage-keeper/internal/quota"
	"cpa-usage-keeper/internal/repository"
	repositorydto "cpa-usage-keeper/internal/repository/dto"
	"cpa-usage-keeper/internal/timeutil"
	"math"
	"testing"
	"time"
)

// Preserve the fork 4416153e assertion alongside upstream's distinct stored test.
// The injected current catalog contains the actual fixture prices, not an empty
// snapshot disguised as known zero. Stored event amounts deliberately disagree.
func TestQuotaWindowWithoutRulesMatchesLegacyTokenCostHelper(t *testing.T) {
	db := openQuotaTestDB(t)
	setting, err := repository.UpsertModelPriceSetting(db, repositorydto.ModelPriceSettingInput{Model: "priced-model", PricingStyle: entities.ModelPricingStyleOpenAI, PromptPricePer1M: 3, CompletionPricePer1M: 15, CacheReadPricePer1M: .3, CacheWritePricePer1M: 3.75})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := pricing.CompilePricingSnapshot([]pricing.ModelPricingConfig{{Model: "priced-model", PricingStyle: entities.ModelPricingStyleOpenAI, ModelMultiplier: 1, BasePrices: pricing.BasePrices{Input: 3, Output: 15, CacheRead: .3, CacheWrite: 3.75}, ConditionalMultipliers: []pricing.RuleConfig{}, Branches: []pricing.PriceBranch{}}}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithRegistry(db, NewProviderRegistry(nil), pricing.NewCatalog(snapshot))
	t.Cleanup(service.StopRefreshTasks)
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	resetAt := now.Add(2 * time.Hour)
	tokens := helper.UsageTokenCostInput{InputTokens: 1_000_000, OutputTokens: 500_000, CacheReadTokens: 200_000, CacheCreationTokens: 100_000}
	if err := db.Create(&entities.UsageEvent{EventKey: "quota-no-rules-current", AuthIndex: "auth-no-rules", Model: "priced-model", Timestamp: now.Add(-time.Hour), InputTokens: tokens.InputTokens, OutputTokens: tokens.OutputTokens, CacheReadTokens: tokens.CacheReadTokens, CacheCreationTokens: tokens.CacheCreationTokens, TotalTokens: tokens.InputTokens + tokens.OutputTokens, CostUSD: floatPtr(4.125), CostAvailable: boolPtr(true)}).Error; err != nil {
		t.Fatal(err)
	}
	seconds := int64(5 * time.Hour / time.Second)
	response := attachWindowUsageStats(service, context.Background(), "auth-no-rules", CheckResponse{ID: "auth-no-rules", Quota: []QuotaRow{{Key: "rate_limit.primary_window", Label: "5h", Scope: "window", Window: &QuotaWindow{Seconds: &seconds}, ResetAt: timeutil.FormatStorageTime(resetAt)}}}, now)
	row := findQuotaRow(t, response.Quota, "rate_limit.primary_window")
	want := helper.CalculateUsageTokenCostBreakdown(tokens, *setting).TotalCostUSD
	if row.WindowUsageCost == nil || !(math.Abs(*row.WindowUsageCost-want) <= 1e-9) {
		t.Fatalf("quota no-Rules cost = %#v, want %.12f", row.WindowUsageCost, want)
	}
}
