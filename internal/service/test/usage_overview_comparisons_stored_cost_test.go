package test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository"
	repodto "cpa-usage-keeper/internal/repository/dto"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func TestUsageServiceComparisonsKeepStoredFeesAcrossPricingMutations(t *testing.T) {
	withUsageServiceLocation(t, "UTC")
	db := openUsageServiceTestDatabase(t)
	key := entities.CPAAPIKey{APIKey: "sk-comparison-current", KeyAlias: "Current Key"}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	sharedLabel := "Shared Display"
	identities := []entities.UsageIdentity{
		{Name: "Auth Source", Alias: &sharedLabel, AuthType: entities.UsageIdentityAuthTypeCredential, Identity: "auth-one", Type: "codex"},
		{Name: "Provider Source", Alias: &sharedLabel, AuthType: entities.UsageIdentityAuthTypeAIProvider, Identity: "provider-one", Type: "codex"},
	}
	if err := db.Create(&identities).Error; err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)
	feeAuth, feeProvider, feeUnknown := 2.5, 3.75, 1.25
	available, unavailable := int64(0), int64(1)
	// 两个同名来源按稳定身份分别计入；未知身份只进入模型和 Key，不伪造凭据归属。
	rows := []entities.UsageOverviewDailyStat{
		{BucketStart: day, APIGroupKey: key.APIKey, Model: "model-a", ModelAlias: "alias-a", AuthIndex: "auth-one", RequestCount: 2, SuccessCount: 2, InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: &feeAuth, UnavailableCostCount: &available},
		{BucketStart: day, APIGroupKey: key.APIKey, Model: "model-a", AuthIndex: "provider-one", RequestCount: 1, SuccessCount: 1, InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: &feeProvider, UnavailableCostCount: &available},
		{BucketStart: day, APIGroupKey: "sk-comparison-deleted", Model: "model-unknown", AuthIndex: "missing-identity", RequestCount: 1, SuccessCount: 1, InputTokens: 100, TotalTokens: 100, CostUSD: &feeUnknown, UnavailableCostCount: &unavailable},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertModelPriceSetting(db, repodto.ModelPriceSettingInput{Model: "model-a", PromptPricePer1M: 9}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.LoadPricingSnapshot(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	catalog := pricing.NewCatalog(snapshot)
	provider, ok := service.NewUsageService(db, catalog).(service.UsageComparisonProvider)
	if !ok {
		t.Fatal("usage service does not expose comparisons")
	}
	end := day.AddDate(0, 0, 1)
	filter := servicedto.UsageFilter{Range: "custom", CustomUnit: "day", StartTime: &day, EndTime: &end, EndExclusive: true}
	storedProvider := service.NewStoredUsageComparisonsService(db)
	storedFilter := servicedto.StoredUsageComparisonsFilter{Range: filter.Range, CustomUnit: filter.CustomUnit, StartTime: filter.StartTime, EndTime: filter.EndTime, EndExclusive: filter.EndExclusive}
	currentFee, currentAvailable := 18.0, true

	assertComparison := func() {
		t.Helper()
		current, err := provider.GetUsageOverviewComparisons(context.Background(), filter)
		if err != nil {
			t.Fatal(err)
		}
		if current.Comparisons == nil {
			t.Fatal("comparison result missing")
		}
		if len(current.Comparisons.Models) != 2 || current.Comparisons.Models["model-a"].Requests != 3 || current.Comparisons.Models["model-a"].TotalTokens != 2_000_000 || current.Comparisons.Models["model-a"].CostAvailable != currentAvailable || !usageFilterCostClose(current.Comparisons.Models["model-a"].CostUSD, currentFee) {
			t.Fatalf("current comparison lost facts or current-price mutations: %+v", current.Comparisons)
		}
		result, err := storedProvider.GetStoredUsageComparisons(context.Background(), storedFilter)
		if err != nil {
			t.Fatal(err)
		}
		modelA := storedComparisonPublicItem(t, result.Models, "model-a")
		unknown := storedComparisonPublicItem(t, result.Models, "model-unknown")
		if len(result.Models) != 2 || modelA.Requests != 3 || modelA.TotalTokens != 2_000_000 || !modelA.StoredCostAvailable || !usageFilterCostClose(modelA.StoredCostUSD, 6.25) || unknown.StoredCostAvailable || !usageFilterCostClose(unknown.StoredCostUSD, 1.25) {
			t.Fatalf("model comparison lost stored fee/availability or real model grouping: %+v", result.Models)
		}
		active := storedComparisonPublicItem(t, result.APIKeys, strconv.FormatInt(key.ID, 10))
		if len(result.APIKeys) != 2 || !active.StoredCostAvailable || !usageFilterCostClose(active.StoredCostUSD, 6.25) {
			t.Fatalf("Key comparison lost stored fee/deleted history: %+v", result.APIKeys)
		}
		deletedCount := 0
		for _, item := range result.APIKeys {
			if item.ID == nil && !item.IDAvailable {
				deletedCount++
				if item.Requests != 1 || item.TotalTokens != 100 || item.StoredCostAvailable || !usageFilterCostClose(item.StoredCostUSD, feeUnknown) {
					t.Fatalf("deleted key lost persisted amount or availability: %+v", item)
				}
			}
		}
		if deletedCount != 1 {
			t.Fatalf("deleted key must remain a distinct unavailable public ID: %+v", result.APIKeys)
		}
		credentials, aiProviders := result.Credentials, result.AIProviders
		auth := storedComparisonPublicItem(t, credentials, strconv.FormatInt(identities[0].ID, 10))
		ai := storedComparisonPublicItem(t, aiProviders, strconv.FormatInt(identities[1].ID, 10))
		if len(credentials) != 1 || len(aiProviders) != 1 || auth.Label != sharedLabel || ai.Label != sharedLabel || !auth.StoredCostAvailable || !ai.StoredCostAvailable || !usageFilterCostClose(auth.StoredCostUSD, feeAuth) || !usageFilterCostClose(ai.StoredCostUSD, feeProvider) {
			t.Fatalf("typed identity comparison lost stored fee/alias: auth=%+v provider=%+v", credentials, aiProviders)
		}
		filtered := filter
		filtered.APIKeyID = strconv.FormatInt(key.ID, 10)
		current, err = provider.GetUsageOverviewComparisons(context.Background(), filtered)
		if err != nil {
			t.Fatal(err)
		}
		if len(current.Comparisons.APIKeys) != 1 || current.Comparisons.APIKeys[key.APIKey] == nil || len(current.Comparisons.Models) != 1 || current.Comparisons.Models["model-a"].Requests != 3 || current.Comparisons.Models["model-a"].TotalTokens != 2_000_000 {
			t.Fatalf("current comparison escaped API Key scope: %+v", current.Comparisons)
		}
		storedScoped := storedFilter
		storedScoped.APIKeyID = filtered.APIKeyID
		result, err = storedProvider.GetStoredUsageComparisons(context.Background(), storedScoped)
		if err != nil {
			t.Fatal(err)
		}
		filteredModel := storedComparisonPublicItem(t, result.Models, "model-a")
		storedComparisonPublicItem(t, result.APIKeys, strconv.FormatInt(key.ID, 10))
		if len(result.APIKeys) != 1 || len(result.Models) != 1 || !usageFilterCostClose(filteredModel.StoredCostUSD, 6.25) {
			t.Fatalf("filtered comparison escaped API Key scope: %+v", result)
		}
	}
	assertComparison()
	if _, err := repository.UpsertModelPriceSetting(db, repodto.ModelPriceSettingInput{Model: "model-a", PromptPricePer1M: 90}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = repository.LoadPricingSnapshot(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	catalog.Replace(snapshot)
	currentFee = 180
	assertComparison()
	if err := repository.DeleteModelPriceSettingRequired(db, "model-a"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = repository.LoadPricingSnapshot(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	catalog.Replace(snapshot)
	currentFee, currentAvailable = 0, false
	assertComparison()
}

func TestUsageServiceComparisonsUseStoredFeesAtPartialHourBoundaries(t *testing.T) {
	withUsageServiceLocation(t, "UTC")
	db := openUsageServiceTestDatabase(t)
	if _, err := repository.UpsertModelPriceSetting(db, repodto.ModelPriceSettingInput{Model: "model-a", PromptPricePer1M: 9}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 6, 2, 12, 5, 0, 0, time.UTC)
	end := start.Add(50 * time.Minute)
	now := end.Add(time.Hour)
	events := []entities.UsageEvent{
		storedUsageEventFee(entities.UsageEvent{EventKey: "comparison-boundary-a", APIGroupKey: "sk-boundary", Model: "model-a", Timestamp: start.Add(5 * time.Minute), InputTokens: 1_000_000, TotalTokens: 1_000_000}, 0.4, true),
		storedUsageEventFee(entities.UsageEvent{EventKey: "comparison-boundary-b", APIGroupKey: "sk-boundary", Model: "model-b", Timestamp: end.Add(-5 * time.Minute), InputTokens: 50, TotalTokens: 50}, 0.7, false),
		storedUsageEventFee(entities.UsageEvent{EventKey: "comparison-outside", APIGroupKey: "sk-boundary", Model: "model-a", Timestamp: end.Add(time.Minute), InputTokens: 1_000_000, TotalTokens: 1_000_000}, 99, true),
	}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.LoadPricingSnapshot(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := service.NewUsageService(db, pricing.NewCatalog(snapshot)).(service.UsageComparisonProvider)
	if !ok {
		t.Fatal("usage service does not expose comparisons")
	}
	result, err := provider.GetUsageOverviewComparisons(context.Background(), servicedto.UsageFilter{
		Range: "custom", StartTime: &start, EndTime: &end, EndExclusive: true, QueryNow: &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Comparisons == nil || len(result.Comparisons.Models) != 2 || len(result.Comparisons.APIKeys) != 1 {
		t.Fatalf("partial-hour comparison dimensions: %+v", result.Comparisons)
	}
	if result.Comparisons.Models["model-a"].Requests != 1 || result.Comparisons.Models["model-a"].TotalTokens != 1_000_000 || !usageFilterCostClose(result.Comparisons.Models["model-a"].CostUSD, 9) || result.Comparisons.Models["model-b"].Requests != 1 || result.Comparisons.APIKeys["sk-boundary"].Requests != 2 {
		t.Fatalf("current partial-hour comparison changed facts/current pricing: %+v", result.Comparisons)
	}
	stored, err := service.NewStoredUsageComparisonsService(db).GetStoredUsageComparisons(context.Background(), servicedto.StoredUsageComparisonsFilter{
		Range: "custom", StartTime: &start, EndTime: &end, EndExclusive: true, QueryNow: &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Models) != 2 || len(stored.APIKeys) != 1 {
		t.Fatalf("stored partial-hour dimensions changed: %+v", stored)
	}
	modelA := storedComparisonPublicItem(t, stored.Models, "model-a")
	modelB := storedComparisonPublicItem(t, stored.Models, "model-b")
	key := stored.APIKeys[0]
	if modelA.Requests != 1 || !modelA.StoredCostAvailable || !usageFilterCostClose(modelA.StoredCostUSD, 0.4) || modelB.Requests != 1 || modelB.StoredCostAvailable || !usageFilterCostClose(modelB.StoredCostUSD, 0.7) || key.Requests != 2 || key.StoredCostAvailable || !usageFilterCostClose(key.StoredCostUSD, 1.1) || key.ID != nil || key.IDAvailable {
		t.Fatalf("partial-hour comparison lost persisted fee/availability: models=%+v keys=%+v", stored.Models, stored.APIKeys)
	}
	tooLong := start.Add(25 * time.Hour)
	for _, unit := range []string{"", "hour", "unsupported"} {
		if _, err := service.NewStoredUsageComparisonsService(db).GetStoredUsageComparisons(context.Background(), servicedto.StoredUsageComparisonsFilter{Range: "custom", CustomUnit: unit, StartTime: &start, EndTime: &tooLong}); err == nil {
			t.Fatalf("unbounded/unsupported internal custom unit %q accepted", unit)
		}
	}
}

// Select only by the real safe public ID; never reconstruct raw-key maps or use
// a display label as an identity key. Missing and duplicate IDs fail the test.
func storedComparisonPublicItem(t *testing.T, items []servicedto.StoredUsageComparisonItem, id string) servicedto.StoredUsageComparisonItem {
	t.Helper()
	var found *servicedto.StoredUsageComparisonItem
	for i := range items {
		if items[i].ID != nil && *items[i].ID == id {
			if found != nil || !items[i].IDAvailable {
				t.Fatalf("duplicate/unavailable public ID %q: %+v", id, items)
			}
			found = &items[i]
		}
	}
	if found == nil {
		t.Fatalf("public ID %q missing: %+v", id, items)
	}
	return *found
}
