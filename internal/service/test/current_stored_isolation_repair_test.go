package test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/storedpricing"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func TestCurrentReadRepairKeepsIndependentStored100AndCurrent3344(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	// Explicitly initialized synthetic persisted valuations are deliberately
	// different from both current configured and reference amounts.
	if err := f.db.Model(&entities.UsageEvent{}).Where("1=1").Updates(map[string]any{"cost_usd": 25.0, "cost_available": true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := storedpricing.CreateSchema(f.db); err != nil {
		t.Fatal(err)
	}
	store, err := storedpricing.Open(f.db, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	stored := service.NewStoredPricingService(f.db, store, nil)
	config := pricing.ModelPricingConfig{Model: "observed-model", PricingStyle: entities.ModelPricingStyleOpenAI, BasePrices: pricing.BasePrices{Input: 100}, ModelMultiplier: 1, Branches: []pricing.PriceBranch{}, ConditionalMultipliers: []pricing.RuleConfig{}}
	if _, err := stored.SavePricingModel(ctx, config); err != nil {
		t.Fatal(err)
	}
	before, err := stored.ListPricingModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	checkStored := func() {
		t.Helper()
		var rows []entities.UsageEvent
		if err := f.db.Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		total := 0.0
		for _, row := range rows {
			if row.CostUSD == nil || row.CostAvailable == nil || !*row.CostAvailable {
				t.Fatal("stored valuation pair changed")
			}
			total += *row.CostUSD
		}
		closeCost(t, total, 100)
		after, err := stored.ListPricingModels(ctx)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("current edit changed explicit stored config: %+v %v", after, err)
		}
	}
	f.assertCostFamilies(t, 33.44, true)
	checkStored()
	pinned := f.catalog.NewResolver()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".3"); err != nil {
		t.Fatal(err)
	}
	filter := servicedto.UsageFilter{Range: "custom", CustomUnit: "hour", StartTime: &f.start, EndTime: &f.end, EndExclusive: true}
	response, err := service.NewUsageService(f.db, f.catalog).GetUsageOverview(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if response.Summary.TotalCost == 33.44 || response.PricingSnapshotID == pinned.SnapshotID() {
		t.Fatal("current mutation did not affect current amount/snapshot")
	}
	checkStored()
	// Strict matrix publication changes only current reads, not the explicit
	// stored model/revision or already persisted valuations.
	matrix := f.prices.(service.CredentialModelMatrixProvider)
	if _, err := matrix.UpdateCredentialModelMatrix(ctx, []service.CredentialMatrixCellInput{matrixTariff(f.subjectID, "observed-model", "openai", [4]float64{1, 2, 3, 4})}); err != nil {
		t.Fatal(err)
	}
	page, err := service.NewUsageService(f.db, f.catalog).ListUsageEvents(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range page.Events {
		if event.AuthIndex == "synthetic-a" {
			found = true
			if event.PricingSelection == nil || event.PricingSelection.PricingSource != pricing.CredentialMatrixSource {
				t.Fatalf("matrix did not own current selection: %+v", event.PricingSelection)
			}
		}
	}
	if !found {
		t.Fatal("missing current fixture events")
	}
	checkStored()
	reloaded, err := repository.LoadPricingSnapshot(ctx, f.db)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.ID() == "" {
		t.Fatal("restart lost current snapshot")
	}
}
