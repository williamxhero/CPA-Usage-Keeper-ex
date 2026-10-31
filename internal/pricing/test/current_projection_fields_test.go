package test

import (
	"cpa-usage-keeper/internal/pricing"
	"testing"
)

// Direct resolver arithmetic can pass even when read projections silently drop
// its rule inputs. Verify the compilation contract, including zero/identity rules.
func TestCurrentSnapshotRetainsEffectiveProjectionRuleFields(t *testing.T) {
	snapshot := compileSnapshot(t, pricing.ModelConfig{Pricing: testPricingWithPrompt("current", 1), Rules: []pricing.RuleConfig{
		{Key: "service_tier", Value: "priority", Multiplier: 2},
		{Key: "reasoning_effort", Value: "high", Multiplier: 3},
		{Key: "endpoint", Value: "/free", Multiplier: 0},
		{Key: "executor_type", Value: "noop", Multiplier: 1},
	}})
	resolver := pricing.NewCatalog(snapshot).NewResolver()
	for _, field := range []pricing.RuleField{pricing.RuleFieldServiceTier, pricing.RuleFieldReasoningEffort, pricing.RuleFieldEndpoint} {
		if !resolver.ActiveFields().Has(field) {
			t.Errorf("effective rule field %s missing from current projection", field)
		}
	}
	if resolver.ActiveFields().Has(pricing.RuleFieldExecutorType) {
		t.Fatal("multiplier-one rule activated unnecessary projection")
	}
}
