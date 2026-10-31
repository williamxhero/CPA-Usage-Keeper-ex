package test

import (
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/helper"
	"cpa-usage-keeper/internal/pricing"
	"testing"
)

func TestLegacyConfigReadProjectionPreservesPricingAndDeepCopy(t *testing.T) {
	zero := 0.0
	snapshot, err := pricing.CompileSnapshot([]pricing.ModelConfig{{
		Pricing: entities.ModelPriceSetting{Model: "legacy", PricingStyle: "claude", PromptPricePer1M: 2, PriceMultiplier: &zero},
		Rules:   []pricing.RuleConfig{{Key: "service_tier", Value: "priority", Multiplier: 3}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	resolver := pricing.NewCatalog(snapshot).NewResolver()
	subject := pricing.NewCostSubject(pricing.UsageDimensions{Model: "legacy", ServiceTier: "priority"}, helper.UsageTokenCostInput{InputTokens: 1000000})
	before := resolver.CalculateFee(subject)
	if !before.Available || before.TotalCostUSD != 0 {
		t.Fatalf("zero multiplier lost: %+v", before)
	}
	configs := snapshot.PricingModelConfigs()
	if len(configs) != 1 || configs[0].Model != "legacy" || configs[0].PricingStyle != "claude" || configs[0].BasePrices.Input != 2 || configs[0].ModelMultiplier != 0 || len(configs[0].ConditionalMultipliers) != 1 || configs[0].ConditionalMultipliers[0].Multiplier != 3 || configs[0].Branches == nil || len(configs[0].Branches) != 0 {
		t.Fatalf("legacy read projection lost data: %+v", configs)
	}
	configs[0].ConditionalMultipliers[0].Multiplier = 99
	configs[0].BasePrices.Input = 77
	again, ok := snapshot.PricingModelConfig("legacy")
	if !ok || again.BasePrices.Input != 2 || again.ConditionalMultipliers[0].Multiplier != 3 {
		t.Fatalf("read projection mutated immutable snapshot: %+v", again)
	}
	if after := resolver.CalculateFee(subject); after != before {
		t.Fatalf("configuration read changed valuation: before=%+v after=%+v", before, after)
	}
}
