package test

import (
	"context"
	"reflect"
	"testing"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func TestStoredConfigEditsKeepCurrent3344Stored100AndEvidence(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Model(&entities.UsageEvent{}).Where("1=1").Updates(map[string]any{"cost_usd": 25.0, "cost_available": true}).Error; err != nil {
		t.Fatal(err)
	}
	stored, store := newStoredConfigService(t, f.db)
	before := f.catalog.Snapshot()
	// Compare every row of source, official, matrix, identity and adoption evidence,
	// not only the rendered amount. All are real migrated fork tables.
	tables := []string{"model_price_settings", "model_price_rules", "official_model_prices", "credential_model_price_overrides", "pricing_migration_batches", "pricing_migration_batch_counts", "pricing_migration_items", "pricing_migration_legacy_states", "usage_events"}
	read := func() map[string][]map[string]any {
		t.Helper()
		out := map[string][]map[string]any{}
		for _, name := range tables {
			var rows []map[string]any
			if err := f.db.Table(name).Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			out[name] = rows
		}
		return out
	}
	evidence := read()
	config := completeServiceConfig("observed-model", 100)
	if _, err := stored.SavePricingModel(ctx, config); err != nil {
		t.Fatal(err)
	}
	pinned := store.NewResolver()
	threshold := int64(100)
	config.Branches = []pricing.PriceBranch{{ID: "large", Name: "Large", Context: pricing.ContextCondition{Type: pricing.ContextGT, Threshold: &threshold}, Period: pricing.PeriodCondition{Type: pricing.PeriodAll}, Prices: pricing.BasePrices{Input: 7}}}
	config.ModelMultiplier = 2
	if _, err := stored.SavePricingModel(ctx, config); err != nil {
		t.Fatal(err)
	}
	if got := completeServiceFee(store, "observed-model", 1_000_000); !got.Available || got.TotalCostUSD != 14 {
		t.Fatalf("stored branch=%+v", got)
	}
	if _, err := stored.ApplyPricingSync(ctx, servicedto.PricingSyncApplyRequest{Source: "models-dev", Items: []servicedto.PricingSyncApplyItem{{Model: "observed-model", PricingStyle: "claude", BasePrices: pricing.BasePrices{Input: 9}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := stored.DeletePricingModel(ctx, "observed-model"); err != nil {
		t.Fatal(err)
	}
	if pinned.SnapshotID() == "" {
		t.Fatal("lost independent pinned snapshot")
	}
	if f.catalog.Snapshot() != before || !reflect.DeepEqual(evidence, read()) {
		t.Fatal("stored mutation changed fork source/current/evidence or persisted usage")
	}
	f.assertCostFamilies(t, 33.44, true)
	var events []entities.UsageEvent
	if err := f.db.Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	total := 0.0
	for _, e := range events {
		if e.CostUSD == nil || e.CostAvailable == nil || !*e.CostAvailable {
			t.Fatal("stored pair lost")
		}
		total += *e.CostUSD
	}
	closeCost(t, total, 100)
}
