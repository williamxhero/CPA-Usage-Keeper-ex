package test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"cpa-usage-keeper/internal/cpa/dto/credentials"
	"cpa-usage-keeper/internal/cpa/dto/providerconfig"
	"cpa-usage-keeper/internal/cpa/dto/response"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/helper"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository"
	repositorydto "cpa-usage-keeper/internal/repository/dto"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
	"gorm.io/gorm"
)

type credentialDefaultFixture struct {
	db              *gorm.DB
	prices          service.PricingProvider
	defaults        service.PricingCredentialDefaultsProvider
	catalog         *pricing.Catalog
	subjectID       string
	events          []entities.UsageEvent
	start, end, now time.Time
}

func newCredentialDefaultFixture(t *testing.T) credentialDefaultFixture {
	t.Helper()
	db := openUsageServiceTestDatabase(t)
	identities := []entities.UsageIdentity{
		{Name: "Synthetic A", AuthType: entities.UsageIdentityAuthTypeAuthFile, AuthTypeName: "oauth", Identity: "synthetic-a", Type: "codex", BindingIdentityStatus: "unique"},
		{Name: "Synthetic B", AuthType: entities.UsageIdentityAuthTypeAuthFile, AuthTypeName: "oauth", Identity: "synthetic-b", Type: "codex", BindingIdentityStatus: "unique"},
	}
	if err := db.Create(&identities).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entities.CPAAPIKey{APIKey: "synthetic-downstream", DisplayKey: "Synthetic caller"}).Error; err != nil {
		t.Fatal(err)
	}
	prices, catalog := newCatalogPricingService(t, db)
	half := .5
	if _, err := prices.UpdatePricing(context.Background(), servicedto.UpdatePricingInput{Model: "base", PromptPricePer1M: 10, CompletionPricePer1M: 20, CacheReadPricePer1M: 2, CacheWritePricePer1M: 4, PriceMultiplier: &half}); err != nil {
		t.Fatal(err)
	}
	if _, err := prices.ReplacePricingRules(context.Background(), servicedto.ReplacePricingRulesInput{Model: "base", Rules: []servicedto.PricingRuleInput{{Key: "service_tier", Value: "priority", Multiplier: new(2.0)}, {Key: "reasoning_effort", Value: "high", Multiplier: new(3.0)}}}); err != nil {
		t.Fatal(err)
	}
	subject, err := prices.(service.PricingCredentialProvider).BindPricingCredential(context.Background(), identities[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 6, 1, 0, 0, 0, 0, time.Local)
	now := day.Add(12*time.Hour + 10*time.Minute)
	alias := "base"
	events := []entities.UsageEvent{
		{EventKey: "default-normal-a", APIGroupKey: "synthetic-downstream", AuthIndex: "synthetic-a", AuthType: "oauth", Provider: "codex", Model: "observed-model", ModelAlias: &alias, ServiceTier: "priority", ReasoningEffort: "high", Timestamp: day.Add(12*time.Hour + time.Minute), InputTokens: 1_000_000, OutputTokens: 100_000, CacheReadTokens: 200_000, CacheCreationTokens: 100_000, TotalTokens: 1_100_000},
		{EventKey: "default-normal-b", APIGroupKey: "synthetic-downstream", AuthIndex: "synthetic-b", AuthType: "oauth", Provider: "codex", Model: "observed-model", ModelAlias: &alias, ServiceTier: "priority", ReasoningEffort: "high", Timestamp: day.Add(12*time.Hour + 2*time.Minute), InputTokens: 1_000_000, OutputTokens: 100_000, CacheReadTokens: 200_000, CacheCreationTokens: 100_000, TotalTokens: 1_100_000},
		// These share EVERY rollup dimension: aggregate input-cache would lose
		// 100k ordinary input. Correct selected-override pricing is per event.
		{EventKey: "default-abnormal-a", APIGroupKey: "synthetic-downstream", AuthIndex: "synthetic-a", AuthType: "oauth", Provider: "codex", Model: "observed-model", ModelAlias: &alias, ServiceTier: "priority", ReasoningEffort: "high", Timestamp: day.Add(12*time.Hour + 3*time.Minute), InputTokens: 100_000, CacheReadTokens: 200_000, TotalTokens: 100_000},
		{EventKey: "default-ordinary-a", APIGroupKey: "synthetic-downstream", AuthIndex: "synthetic-a", AuthType: "oauth", Provider: "codex", Model: "observed-model", ModelAlias: &alias, ServiceTier: "priority", ReasoningEffort: "high", Timestamp: day.Add(12*time.Hour + 4*time.Minute), InputTokens: 1_000_000, TotalTokens: 1_000_000},
	}
	if _, _, err := repository.InsertUsageEvents(db, events); err != nil {
		t.Fatal(err)
	}
	if err := repository.AggregateUsageOverviewStats(context.Background(), db, day.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	return credentialDefaultFixture{db: db, prices: prices, defaults: prices.(service.PricingCredentialDefaultsProvider), catalog: catalog, subjectID: subject.SubjectID, events: events, start: day, end: day.Add(20 * time.Hour), now: now}
}

func closeCost(t *testing.T, actual, expected float64) {
	t.Helper()
	if math.Abs(actual-expected) > 1e-9 {
		t.Fatalf("cost %.12f, want %.12f", actual, expected)
	}
}

func assertRealtimeCredentialCosts(t *testing.T, rows []servicedto.RealtimeUsageTopItem, expected map[string]float64) {
	t.Helper()
	if len(rows) != len(expected) {
		t.Fatalf("realtime credential rows %+v, want %v", rows, expected)
	}
	for _, row := range rows {
		value, found := expected[row.Key]
		if !found || row.CostUSD == nil {
			t.Fatalf("unexpected realtime credential %+v", row)
		}
		closeCost(t, *row.CostUSD, value)
	}
}

func (f credentialDefaultFixture) assertCostFamilies(t *testing.T, expected float64, includeDetails bool) {
	t.Helper()
	ctx := context.Background()
	usage := service.NewUsageService(f.db, f.catalog)
	filter := servicedto.UsageFilter{Range: "custom", CustomUnit: "hour", StartTime: &f.start, EndTime: &f.end, EndExclusive: true}
	overview, err := usage.GetUsageOverview(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	closeCost(t, overview.Summary.TotalCost, expected)
	if !overview.Summary.CostAvailable {
		t.Fatal("overview incomplete")
	}
	sum := 0.0
	for _, cost := range overview.Series.Cost {
		sum += cost
	}
	closeCost(t, sum, expected)
	comparisons, err := usage.(service.UsageComparisonProvider).GetUsageOverviewComparisons(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	sum = 0
	for _, item := range comparisons.Comparisons.Models {
		sum += item.CostUSD
		if !item.CostAvailable {
			t.Fatal("model comparison incomplete")
		}
	}
	closeCost(t, sum, expected)
	sum = 0
	for _, item := range comparisons.Comparisons.AuthFiles {
		sum += item.CostUSD
	}
	for _, item := range comparisons.Comparisons.AIProviders {
		sum += item.CostUSD
	}
	closeCost(t, sum, expected)
	analysis, err := usage.GetAnalysis(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	closeCost(t, analysis.CostBreakdown.TotalCostUSD, expected)
	if !analysis.CostBreakdown.CostAvailable {
		t.Fatal("analysis incomplete")
	}
	closeCost(t, analysis.CostBreakdown.UncachedInputCostUSD+analysis.CostBreakdown.OutputCostUSD+analysis.CostBreakdown.CacheReadCostUSD+analysis.CostBreakdown.CacheWriteCostUSD, expected)
	sum = 0
	for _, item := range analysis.ModelEfficiency {
		sum += item.CostUSD
	}
	closeCost(t, sum, expected)
	sum = 0
	for _, item := range analysis.AIProviderComposition {
		sum += item.CostUSD
	}
	for _, item := range analysis.AuthFilesComposition {
		sum += item.CostUSD
	}
	closeCost(t, sum, expected)
	// Daily and hourly use identical retained pricing evidence, not token sums.
	daily := filter
	daily.CustomUnit = "day"
	dailyEnd := f.start.AddDate(0, 0, 1)
	daily.EndTime = &dailyEnd
	dailyOverview, err := usage.GetUsageOverview(ctx, daily)
	if err != nil {
		t.Fatal(err)
	}
	closeCost(t, dailyOverview.Summary.TotalCost, expected)
	dailyAnalysis, err := usage.GetAnalysis(ctx, daily)
	if err != nil {
		t.Fatal(err)
	}
	closeCost(t, dailyAnalysis.CostBreakdown.TotalCostUSD, expected)
	windowA, err := repository.SumUsageWindowStatsByAuthIndex(ctx, f.db, "synthetic-a", f.start, &f.end, f.catalog.NewResolver())
	if err != nil {
		t.Fatal(err)
	}
	windowB, err := repository.SumUsageWindowStatsByAuthIndex(ctx, f.db, "synthetic-b", f.start, &f.end, f.catalog.NewResolver())
	if err != nil {
		t.Fatal(err)
	}
	closeCost(t, windowA.Cost+windowB.Cost, expected)
	if !windowA.CostAvailable || !windowB.CostAvailable {
		t.Fatal("window incomplete")
	}
	calculator, err := repository.NewUsageWindowStatsCalculator(ctx, f.db, f.catalog.NewResolver())
	if err != nil {
		t.Fatal(err)
	}
	grouped, err := calculator.SumGroupsByAuthIndex(ctx, "synthetic-a", f.start, &f.end, func(model string) (string, bool) { return "synthetic-group", model == "observed-model" })
	if err != nil {
		t.Fatal(err)
	}
	if !grouped.Complete || !grouped.Groups["synthetic-group"].CostAvailable {
		t.Fatal("grouped window incomplete")
	}
	closeCost(t, grouped.Groups["synthetic-group"].Cost, windowA.Cost)
	if !includeDetails {
		return
	}
	page, err := usage.ListUsageEvents(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	sum = 0
	for _, event := range page.Events {
		sum += event.CostUSD
		if !event.CostAvailable {
			t.Fatal("detail incomplete")
		}
	}
	closeCost(t, sum, expected)
	sum = 0
	if err := usage.StreamUsageEvents(ctx, filter, func(event servicedto.UsageEventRecord) error { sum += event.CostUSD; return nil }); err != nil {
		t.Fatal(err)
	}
	closeCost(t, sum, expected)
	realtimeFilter := servicedto.UsageFilter{RealtimeWindow: "15m", RealtimeEndTime: &f.now}
	realtime, err := usage.GetUsageOverviewRealtime(ctx, realtimeFilter)
	if err != nil {
		t.Fatal(err)
	}
	sum = 0
	for _, item := range realtime.CurrentUsage.Models {
		if item.CostUSD == nil {
			t.Fatal("realtime incomplete")
		}
		sum += *item.CostUSD
	}
	closeCost(t, sum, expected)
	cache, err := repository.NewUsageRecentEventCache(f.db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	cachedUsage := service.NewUsageServiceWithRecentCache(f.db, cache, f.catalog)
	cached, err := cachedUsage.GetUsageOverviewRealtime(ctx, realtimeFilter)
	if err != nil {
		t.Fatal(err)
	}
	sum = 0
	for _, item := range cached.CurrentUsage.Models {
		if item.CostUSD == nil {
			t.Fatal("cached realtime incomplete")
		}
		sum += *item.CostUSD
	}
	closeCost(t, sum, expected)
	shortStart := f.start.Add(12 * time.Hour)
	shortEnd := shortStart.Add(time.Hour)
	shortA, err := repository.SumUsageWindowStatsByAuthIndex(ctx, f.db, "synthetic-a", shortStart, &shortEnd, f.catalog.NewResolver())
	if err != nil {
		t.Fatal(err)
	}
	shortB, err := repository.SumUsageWindowStatsByAuthIndex(ctx, f.db, "synthetic-b", shortStart, &shortEnd, f.catalog.NewResolver())
	if err != nil {
		t.Fatal(err)
	}
	closeCost(t, shortA.Cost+shortB.Cost, expected)
}

func TestCredentialDefaultRealServiceSaveQueriesClearAndRestart(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	inherited, err := f.defaults.GetCredentialDefault(ctx, f.subjectID)
	if err != nil || inherited.Multiplier != nil {
		t.Fatalf("inherit: %+v %v", inherited, err)
	}
	for _, text := range []string{"0.2", " 0.2x ", "20%", ".2X"} {
		saved, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, text)
		if err != nil {
			t.Fatal(err)
		}
		if saved.Multiplier == nil || *saved.Multiplier != .2 || saved.SnapshotID == "" {
			t.Fatalf("canonical save: %+v", saved)
		}
		f.assertCostFamilies(t, 33.44, true)
	}
	// Same DB, reconstructed real service and catalog, no test price stubs.
	restartedPrices, restartedCatalog := newCatalogPricingService(t, f.db)
	f.prices = restartedPrices
	f.defaults = restartedPrices.(service.PricingCredentialDefaultsProvider)
	f.catalog = restartedCatalog
	saved, err := f.defaults.GetCredentialDefault(ctx, f.subjectID)
	if err != nil || saved.Multiplier == nil || *saved.Multiplier != .2 {
		t.Fatalf("restart %+v %v", saved, err)
	}
	f.assertCostFamilies(t, 33.44, true)
	beforeClear := f.catalog.NewResolver().Calculate(repository.UsageEventCostSubject(f.events[0]))
	closeCost(t, beforeClear.Cost.TotalCostUSD, 1.96)
	closeCost(t, beforeClear.Cost.UncachedInputCostUSD, 1.4)
	closeCost(t, beforeClear.Cost.OutputCostUSD, .4)
	closeCost(t, beforeClear.Cost.CacheReadCostUSD, .08)
	closeCost(t, beforeClear.Cost.CacheWriteCostUSD, .08)
	if beforeClear.MatchedBy != "model_alias" {
		t.Fatalf("alias %+v", beforeClear)
	}
	cleared, err := f.defaults.ClearCredentialDefault(ctx, f.subjectID)
	if err != nil || cleared.Multiplier != nil {
		t.Fatalf("clear %+v %v", cleared, err)
	}
	legacy := f.catalog.NewResolver().Calculate(repository.UsageEventCostSubject(f.events[0]))
	closeCost(t, legacy.Cost.TotalCostUSD, 29.4)
	if legacy.RuleMultiplier != 6 || legacy.MatchedBy != "model_alias" {
		t.Fatalf("legacy metadata %+v", legacy)
	}
}

func TestCredentialDefaultExplicitZeroOneMissingBaselineAndFutureModel(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	subject := repository.UsageEventCostSubject(f.events[0])
	for _, value := range []struct {
		text string
		cost float64
	}{{"0x", 0}, {"1", 9.8}, {"120%", 11.76}, {"1.2x", 11.76}} {
		if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, value.text); err != nil {
			t.Fatal(err)
		}
		result := f.catalog.NewResolver().Calculate(subject)
		closeCost(t, result.Cost.TotalCostUSD, value.cost)
		if !result.Available || result.Scope != "credential_default" {
			t.Fatalf("active override %+v", result)
		}
	}
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, "0"); err != nil {
		t.Fatal(err)
	}
	subject.Dimensions.Model = "missing"
	subject.Dimensions.ModelAlias = ""
	if result := f.catalog.NewResolver().Calculate(subject); result.Available || result.UnavailableReason != "missing_baseline" {
		t.Fatalf("zero invented free %+v", result)
	}
	subject.Tokens = helper.UsageTokenCostInput{}
	if result := f.catalog.NewResolver().Calculate(subject); !result.Available || result.Cost.TotalCostUSD != 0 {
		t.Fatalf("empty unavailable %+v", result)
	}
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".3"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.prices.UpdatePricing(ctx, servicedto.UpdatePricingInput{Model: "future", PromptPricePer1M: 10, PriceMultiplier: new(.5)}); err != nil {
		t.Fatal(err)
	}
	subject.Dimensions.Model = "future"
	subject.Tokens.InputTokens = 1_000_000
	closeCost(t, f.catalog.NewResolver().Calculate(subject).Cost.TotalCostUSD, 3)
	// A new explicit one replaces even a legacy zero model multiplier.
	if _, err := f.prices.UpdatePricing(ctx, servicedto.UpdatePricingInput{Model: "future", PromptPricePer1M: 10, PriceMultiplier: new(0.0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, "1"); err != nil {
		t.Fatal(err)
	}
	closeCost(t, f.catalog.NewResolver().Calculate(subject).Cost.TotalCostUSD, 10)
}

func TestCredentialDefaultRejectsInvalidAndUnsafeCompleteCandidatesAtomically(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	previous := f.catalog.Snapshot()
	for _, text := range []string{"", " ", "-1", "+1", "NaN", "Infinity", "1e2", "20%x", "1 x", "1x%", "broken", "999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999999"} {
		if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, text); !errors.Is(err, service.ErrInvalidPricingInput) {
			t.Fatalf("accepted invalid %q: %v", text, err)
		}
		if f.catalog.Snapshot() != previous {
			t.Fatal("invalid changed active candidate")
		}
	}
	huge := "100000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, huge); !errors.Is(err, service.ErrInvalidPricingInput) {
		t.Fatalf("unsafe accepted: %v", err)
	}
	if _, err := f.prices.UpdatePricing(ctx, servicedto.UpdatePricingInput{Model: "unsafe-zero", PromptPricePer1M: math.MaxFloat64, PriceMultiplier: new(0.0)}); !errors.Is(err, service.ErrInvalidPricingInput) {
		t.Fatalf("legacy zero bypassed baseline safety: %v", err)
	}
	if f.catalog.Snapshot() != previous {
		t.Fatal("unsafe changed snapshot")
	}
	var persisted entities.CredentialPriceDefault
	if err := f.db.First(&persisted, "subject_id = ?", f.subjectID).Error; err != nil {
		t.Fatal(err)
	}
	closeCost(t, persisted.Multiplier, .2)
	// Also reject a future price combined with an already-saved finite default.
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, "10000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"); err != nil {
		t.Fatal(err)
	}
	previous = f.catalog.Snapshot()
	if _, err := f.prices.UpdatePricing(ctx, servicedto.UpdatePricingInput{Model: "unsafe-product", PromptPricePer1M: 1e30}); !errors.Is(err, service.ErrInvalidPricingInput) {
		t.Fatalf("unsafe product accepted: %v", err)
	}
	if f.catalog.Snapshot() != previous {
		t.Fatal("unsafe future price published")
	}
}

func TestCredentialDefaultCommitFailureAndConcurrentPinnedResponses(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	previous := f.catalog.Snapshot()
	if err := f.db.Exec(`CREATE TABLE default_commit_probe (subject_id TEXT, FOREIGN KEY(subject_id) REFERENCES credential_pricing_subjects(id) DEFERRABLE INITIALLY DEFERRED)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec(`CREATE TRIGGER fail_default_commit AFTER UPDATE ON credential_price_defaults BEGIN INSERT INTO default_commit_probe(subject_id) VALUES ('absent-subject'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".7"); err == nil {
		t.Fatal("expected failed COMMIT")
	}
	if f.catalog.Snapshot() != previous {
		t.Fatal("failed COMMIT published")
	}
	readback, err := f.defaults.GetCredentialDefault(ctx, f.subjectID)
	if err != nil || readback.Multiplier == nil || *readback.Multiplier != .2 {
		t.Fatalf("failed commit lost value %+v %v", readback, err)
	}
	if err := f.db.Exec(`DROP TRIGGER fail_default_commit`).Error; err != nil {
		t.Fatal(err)
	}
	oldResolver := f.catalog.NewResolver()
	var wg sync.WaitGroup
	for _, text := range []string{".3", ".4", ".5"} {
		wg.Go(func() {
			if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, text); err != nil {
				t.Error(err)
			}
		})
	}
	for range 4 {
		wg.Go(func() {
			for range 30 {
				resolver := f.catalog.NewResolver()
				first := resolver.Calculate(repository.UsageEventCostSubject(f.events[0]))
				second := resolver.Calculate(repository.UsageEventCostSubject(f.events[0]))
				if first.Cost != second.Cost {
					t.Error("pinned response mixed candidates")
				}
			}
		})
	}
	wg.Wait()
	closeCost(t, oldResolver.Calculate(repository.UsageEventCostSubject(f.events[0])).Cost.TotalCostUSD, 1.96)
	restarted, _ := newCatalogPricingService(t, f.db)
	current, err := f.defaults.GetCredentialDefault(ctx, f.subjectID)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := restarted.(service.PricingCredentialDefaultsProvider).GetCredentialDefault(ctx, f.subjectID)
	if err != nil || *persisted.Multiplier != *current.Multiplier {
		t.Fatalf("concurrent DB/catalog mismatch %+v %+v %v", persisted, current, err)
	}
}

func TestCredentialDefaultQuotaEfficiencyUsesExactSavedScopeAndPerEventBaseline(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	cycle := entities.QuotaCycle{Provider: "codex", AuthIndex: "synthetic-a", QuotaKey: "rate_limit.primary_window", WindowSeconds: 5 * 60 * 60, ResetAtSource: entities.QuotaResetAtSourceAbsolute, WindowStartedAt: f.start.Add(10 * time.Hour), ResetAt: f.start.Add(15 * time.Hour), FirstObservedAt: f.start.Add(10 * time.Hour), LastObservedAt: f.now, CreatedAt: f.start, UpdatedAt: f.start}
	if err := f.db.Create(&cycle).Error; err != nil {
		t.Fatal(err)
	}
	query := repositorydto.CodexQuotaEfficiencyQuery{Provider: "codex", AuthIndex: "synthetic-a", Now: f.now, RangeStart: f.start}
	readCost := func() float64 {
		t.Helper()
		history, err := repository.BuildCodexQuotaEfficiencyHistory(ctx, f.db, query, f.catalog.NewResolver())
		if err != nil {
			t.Fatal(err)
		}
		if len(history.Cycles) != 1 || !history.Cycles[0].Usage.CostAvailable || history.Cycles[0].Usage.Requests != 3 {
			t.Fatalf("efficiency %+v", history)
		}
		return history.Cycles[0].Usage.TotalCostUSD
	}
	legacy := readCost()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	closeCost(t, readCost(), 4.04)
	if _, err := f.defaults.ClearCredentialDefault(ctx, f.subjectID); err != nil {
		t.Fatal(err)
	}
	closeCost(t, readCost(), legacy)
	// A sole saved API-key claim at the same index must not be retried as
	// type-less attribution after the OAuth stream rejects it.
	identity := entities.UsageIdentity{Name: "Synthetic opposite type", AuthType: entities.UsageIdentityAuthTypeAIProvider, AuthTypeName: "apikey", Identity: "synthetic-a", Type: "openai", BindingIdentityStatus: "unique"}
	if err := f.db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	bound, err := f.prices.(service.PricingCredentialProvider).BindPricingCredential(ctx, identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.defaults.SetCredentialDefault(ctx, bound.SubjectID, "0"); err != nil {
		t.Fatal(err)
	}
	closeCost(t, readCost(), legacy)
}

func TestCredentialDefaultRollupCoverageKeepsExactUntrimmedIdentityCohorts(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	identity := entities.UsageIdentity{Name: "Synthetic spaced", AuthType: entities.UsageIdentityAuthTypeAuthFile, AuthTypeName: "oauth", Identity: " synthetic-a ", Type: "codex", BindingIdentityStatus: "unique"}
	if err := f.db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	bound, err := f.prices.(service.PricingCredentialProvider).BindPricingCredential(ctx, identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.defaults.SetCredentialDefault(ctx, bound.SubjectID, ".3"); err != nil {
		t.Fatal(err)
	}
	event := f.events[0]
	event.ID, event.EventKey, event.AuthIndex = 0, "exact-spaced-cohort", identity.Identity
	if _, _, err := repository.InsertUsageEvents(f.db, []entities.UsageEvent{event}); err != nil {
		t.Fatal(err)
	}
	if err := repository.AggregateUsageOverviewStats(ctx, f.db, f.end); err != nil {
		t.Fatal(err)
	}
	usage := service.NewUsageService(f.db, f.catalog)
	for _, unit := range []string{"hour", "day"} {
		end := f.end
		if unit == "day" {
			end = f.start.AddDate(0, 0, 1)
		}
		filter := servicedto.UsageFilter{Range: "custom", CustomUnit: unit, StartTime: &f.start, EndTime: &end, EndExclusive: true}
		overview, err := usage.GetUsageOverview(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		closeCost(t, overview.Summary.TotalCost, 36.38)
		if !overview.Summary.CostAvailable {
			t.Fatal("exact separate cohorts lost coverage")
		}
		analysis, err := usage.GetAnalysis(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		closeCost(t, analysis.CostBreakdown.TotalCostUSD, 36.38)
		if !analysis.CostBreakdown.CostAvailable {
			t.Fatal("exact separate analysis cohorts lost coverage")
		}
		expected := map[string]float64{"synthetic-a": 4.04, " synthetic-a ": 2.94, "synthetic-b": 29.4}
		if len(analysis.AuthFilesComposition) != len(expected) {
			t.Fatalf("lost exact composition: %+v", analysis.AuthFilesComposition)
		}
		for _, item := range analysis.AuthFilesComposition {
			value, ok := expected[item.Key]
			if !ok {
				t.Fatalf("unexpected exact identity %q", item.Key)
			}
			closeCost(t, item.CostUSD, value)
		}
		comparisons, err := usage.(service.UsageComparisonProvider).GetUsageOverviewComparisons(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		for index, value := range expected {
			item := comparisons.Comparisons.AuthFiles[index]
			if item == nil {
				t.Fatalf("lost exact comparison %q", index)
			}
			closeCost(t, item.CostUSD, value)
			wantTokens := map[string]int64{"synthetic-a": 2_200_000, " synthetic-a ": 1_100_000, "synthetic-b": 1_100_000}[index]
			var bucketTokens int64
			for _, tokens := range item.TokenBuckets {
				bucketTokens += tokens
			}
			if item.TotalTokens != wantTokens || bucketTokens != wantTokens {
				t.Fatalf("exact identity token attribution %q: %+v", index, item)
			}
		}
	}
	cache, err := repository.NewUsageRecentEventCache(f.db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	for _, reader := range []service.UsageProvider{usage, service.NewUsageServiceWithRecentCache(f.db, cache, f.catalog)} {
		realtime, err := reader.GetUsageOverviewRealtime(ctx, servicedto.UsageFilter{RealtimeWindow: "15m", RealtimeEndTime: &f.now})
		if err != nil {
			t.Fatal(err)
		}
		sum := 0.0
		for _, model := range realtime.CurrentUsage.Models {
			if model.CostUSD == nil {
				t.Fatal("realtime incomplete")
			}
			sum += *model.CostUSD
		}
		closeCost(t, sum, 36.38)
		assertRealtimeCredentialCosts(t, realtime.CurrentUsage.AuthFiles, map[string]float64{"synthetic-a": 4.04, " synthetic-a ": 2.94, "synthetic-b": 29.4})
		assertRealtimeCredentialCosts(t, realtime.CurrentUsage.AIProviders, nil)
	}
	if _, err := f.defaults.ClearCredentialDefault(ctx, f.subjectID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Where("event_key = ?", "exact-spaced-cohort").Delete(&entities.UsageEvent{}).Error; err != nil {
		t.Fatal(err)
	}
	overview, err := usage.GetUsageOverview(ctx, servicedto.UsageFilter{Range: "custom", CustomUnit: "hour", StartTime: &f.start, EndTime: &f.end, EndExclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	if overview.Summary.CostAvailable || overview.Summary.UnavailableReason != "retained_pricing_evidence_incomplete" {
		t.Fatal("normalized rollup guessed pruned exact identity")
	}
}

func TestCredentialDefaultSnapshotNamesAreSafeImmutableAndUnknownSubjectsRemainConfigurable(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	old := f.catalog.Snapshot()
	if old.CredentialName(f.subjectID) != "Synthetic A" {
		t.Fatal("missing safe snapshot name")
	}
	if err := f.db.Model(&entities.UsageIdentity{}).Where("identity = ?", "synthetic-a").Update("name", "synthetic-a").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.defaults.SetCredentialDefault(context.Background(), f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	if f.catalog.Snapshot().CredentialName(f.subjectID) != "Credential" || old.CredentialName(f.subjectID) != "Synthetic A" {
		t.Fatal("unsafe or mutable snapshot name")
	}
	candidate, err := pricing.CompileSnapshotWithCredentials(nil, []pricing.CredentialBinding{{SubjectID: "cred_unknown", AuthType: "unknown"}}, []pricing.CredentialConfig{{SubjectID: "cred_unknown", Multiplier: 1}})
	if err != nil || !candidate.HasCredentialSubject("cred_unknown") {
		t.Fatalf("unknown saved subject lost: %v", err)
	}
	if candidate.CredentialName("cred_unknown") != "Credential" {
		t.Fatal("missing safe fallback")
	}
}

func TestCredentialDefaultRejectedTypedCohortKeepsOriginalAbnormalLegacyFees(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	// The directory proves only an OAuth binding. Actual retained API-key
	// requests at the same index reject that attribution, despite the rollup
	// having no auth_type. Its original grouped legacy fee is not per-event.
	if err := f.db.Model(&entities.UsageEvent{}).Where("auth_index = ?", "synthetic-a").Update("auth_type", "apikey").Error; err != nil {
		t.Fatal(err)
	}
	f.assertCostFamilies(t, 87, false)
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	f.assertCostFamilies(t, 87, false)
	usage := service.NewUsageService(f.db, f.catalog)
	page, err := usage.ListUsageEvents(ctx, servicedto.UsageFilter{Range: "custom", CustomUnit: "hour", StartTime: &f.start, EndTime: &f.end, EndExclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	sum := 0.0
	for _, event := range page.Events {
		if !event.CostAvailable {
			t.Fatal("legacy detail unavailable")
		}
		sum += event.CostUSD
	}
	closeCost(t, sum, 90) // Preserve the pre-existing detail-versus-rollup normalization.
}

func TestCredentialDefaultRetainedArchiveEvidenceAndUncoveredHistoricalCohort(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	// Split a cohort between hot and archive without changing endpoint event scope.
	// Transaction models the real atomic archive move using synthetic rows only.
	var first entities.UsageEvent
	if err := f.db.First(&first, "event_key = ?", "default-abnormal-a").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Exec("INSERT INTO usage_events_archive ("+entities.UsageEventStorageColumns+") SELECT "+entities.UsageEventStorageColumns+" FROM usage_events WHERE id = ?", first.ID).Error; err != nil {
		t.Fatal(err)
	}
	// A retained ID present in both stores must count once, not make coverage
	// appear incomplete or double its fee.
	f.assertCostFamilies(t, 33.44, false)
	if err := f.db.Transaction(func(tx *gorm.DB) error {
		return tx.Delete(&entities.UsageEvent{}, first.ID).Error
	}); err != nil {
		t.Fatal(err)
	}
	f.assertCostFamilies(t, 33.44, false)
	// Removing exact retained evidence cannot be repaired from summed rollup
	// tokens. The known other credential subtotal is retained, but not complete.
	if err := f.db.Table("usage_events_archive").Where("id = ?", first.ID).Delete(&entities.UsageEventArchive{}).Error; err != nil {
		t.Fatal(err)
	}
	usage := service.NewUsageService(f.db, f.catalog)
	filter := servicedto.UsageFilter{Range: "custom", CustomUnit: "hour", StartTime: &f.start, EndTime: &f.end, EndExclusive: true}
	overview, err := usage.GetUsageOverview(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	closeCost(t, overview.Summary.TotalCost, 29.4)
	if overview.Summary.CostAvailable {
		t.Fatal("uncovered historical cohort claimed complete")
	}
	analysis, err := usage.GetAnalysis(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	closeCost(t, analysis.CostBreakdown.TotalCostUSD, 29.4)
	if analysis.CostBreakdown.CostAvailable {
		t.Fatal("uncovered analysis claimed complete")
	}
	window, err := repository.SumUsageWindowStatsByAuthIndex(ctx, f.db, "synthetic-a", f.start, &f.end, f.catalog.NewResolver())
	if err != nil {
		t.Fatal(err)
	}
	if window.CostAvailable {
		t.Fatal("uncovered window claimed complete")
	}
}

func TestCredentialDefaultMissingPriceKeepsKnownSubtotalAndEndpointAvailability(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, "0"); err != nil {
		t.Fatal(err)
	}
	missing := entities.UsageEvent{EventKey: "missing-baseline", APIGroupKey: "synthetic-downstream", Model: "missing", AuthIndex: "synthetic-a", AuthType: "oauth", Timestamp: f.start.Add(12*time.Hour + 5*time.Minute), InputTokens: 1, TotalTokens: 1}
	if _, _, err := repository.InsertUsageEvents(f.db, []entities.UsageEvent{missing}); err != nil {
		t.Fatal(err)
	}
	if err := repository.AggregateUsageOverviewStats(ctx, f.db, f.end); err != nil {
		t.Fatal(err)
	}
	usage := service.NewUsageService(f.db, f.catalog)
	filter := servicedto.UsageFilter{Range: "custom", CustomUnit: "hour", StartTime: &f.start, EndTime: &f.end, EndExclusive: true}
	overview, err := usage.GetUsageOverview(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	closeCost(t, overview.Summary.TotalCost, 29.4)
	if overview.Summary.CostAvailable {
		t.Fatal("unknown zero override marked free")
	}
	page, err := usage.ListUsageEvents(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	known, unknown := 0, 0
	for _, event := range page.Events {
		if event.CostAvailable {
			known++
		} else {
			unknown++
		}
	}
	if known != 4 || unknown != 1 {
		t.Fatalf("availability known=%d unknown=%d", known, unknown)
	}
}

// Keep one explicit domain assertion at the real-service seam for the SPEC's
// $10 * .5 * 2 * 3 example, without introducing a test-only price engine.
func TestCredentialDefaultReplacesAllLegacyAdjustments(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	subject := pricing.NewCostSubject(pricing.UsageDimensions{Model: "base", AuthIndex: "synthetic-a", ServiceTier: "priority", ReasoningEffort: "high"}, helper.UsageTokenCostInput{InputTokens: 1_000_000})
	subject.AuthType = "oauth"
	legacy := f.catalog.NewResolver().Calculate(subject)
	closeCost(t, legacy.Cost.TotalCostUSD, 30)
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".3"); err != nil {
		t.Fatal(err)
	}
	closeCost(t, f.catalog.NewResolver().Calculate(subject).Cost.TotalCostUSD, 3)
	if _, err := f.defaults.ClearCredentialDefault(ctx, f.subjectID); err != nil {
		t.Fatal(err)
	}
	restored := f.catalog.NewResolver().Calculate(subject)
	if restored != legacy {
		t.Fatalf("clear did not restore exact legacy result: %+v %+v", restored, legacy)
	}
}

func TestCredentialDefaultExactRawEvidenceDoesNotBorrowUnknownType(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, "0"); err != nil {
		t.Fatal(err)
	}
	start := f.start.Add(10 * time.Hour)
	end := start.Add(time.Hour)
	for i, identity := range []struct{ index, kind string }{{"synthetic-a", "apikey"}, {"synthetic-a", ""}, {"synthetic-a", " oauth "}, {" synthetic-a ", "oauth"}, {"synthetic-a", "oauth"}} {
		event := entities.UsageEvent{EventKey: fmt.Sprintf("exact-%d", i), APIGroupKey: "synthetic-a", Model: "base", AuthIndex: identity.index, AuthType: identity.kind, Timestamp: start.Add(time.Duration(i) * time.Minute), InputTokens: 1_000_000, TotalTokens: 1_000_000}
		if _, _, err := repository.InsertUsageEvents(f.db, []entities.UsageEvent{event}); err != nil {
			t.Fatal(err)
		}
		expected := 5.0
		if i == 4 {
			expected = 0
		}
		closeCost(t, f.catalog.NewResolver().Calculate(repository.UsageEventCostSubject(event)).Cost.TotalCostUSD, expected)
	}
	usage := service.NewUsageService(f.db, f.catalog)
	filter := servicedto.UsageFilter{Range: "custom", CustomUnit: "hour", StartTime: &start, EndTime: &end, EndExclusive: true}
	page, err := usage.ListUsageEvents(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 5 {
		t.Fatal(len(page.Events))
	}
	sum := 0.0
	for _, row := range page.Events {
		sum += row.CostUSD
	}
	closeCost(t, sum, 20)
	sum = 0
	if err := usage.StreamUsageEvents(ctx, filter, func(row servicedto.UsageEventRecord) error { sum += row.CostUSD; return nil }); err != nil {
		t.Fatal(err)
	}
	closeCost(t, sum, 20)
	window, err := repository.SumUsageWindowStatsByAuthIndex(ctx, f.db, "synthetic-a", start, &end, f.catalog.NewResolver())
	if err != nil {
		t.Fatal(err)
	}
	closeCost(t, window.Cost, 15)
	// Saved whitespace is also exact evidence, not normalized identity.
	spaced := entities.UsageIdentity{Name: "Safe spaced account", AuthType: entities.UsageIdentityAuthTypeAuthFile, AuthTypeName: "oauth", Identity: " synthetic-a ", Type: "codex", BindingIdentityStatus: "unique"}
	if err := f.db.Create(&spaced).Error; err != nil {
		t.Fatal(err)
	}
	bound, err := f.prices.(service.PricingCredentialProvider).BindPricingCredential(ctx, spaced.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.defaults.SetCredentialDefault(ctx, bound.SubjectID, ".3"); err != nil {
		t.Fatal(err)
	}
	subject := repository.UsageEventCostSubject(entities.UsageEvent{Model: "base", AuthType: "oauth", AuthIndex: " synthetic-a ", InputTokens: 1_000_000})
	closeCost(t, f.catalog.NewResolver().Calculate(subject).Cost.TotalCostUSD, 3)
}

func TestCredentialDefaultMixedTypedCohortSplitsCredentialCompositions(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	other := entities.UsageIdentity{Name: "Safe API account", AuthType: entities.UsageIdentityAuthTypeAIProvider, AuthTypeName: "apikey", Identity: "synthetic-a", Type: "openai", BindingIdentityStatus: "unique"}
	if err := f.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.prices.(service.PricingCredentialProvider).BindPricingCredential(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	event := f.events[0]
	event.EventKey = "mixed-type-api"
	event.ID = 0
	event.AuthType = "apikey"
	failed := event
	failed.EventKey = "mixed-type-api-failed"
	failed.Failed = true
	failed.ReasoningTokens = 123
	failed.InputTokens, failed.OutputTokens, failed.CacheReadTokens, failed.CacheCreationTokens, failed.TotalTokens = 0, 0, 0, 0, 0
	// Realtime intentionally omits failed requests. Keep the original billable
	// success assertions and independently preserve failed/reasoning facts.
	if _, _, err := repository.InsertUsageEvents(f.db, []entities.UsageEvent{event, failed}); err != nil {
		t.Fatal(err)
	}
	if err := repository.AggregateUsageOverviewStats(ctx, f.db, f.end); err != nil {
		t.Fatal(err)
	}
	f.assertCostFamilies(t, 62.84, true)
	usage := service.NewUsageService(f.db, f.catalog)
	filter := servicedto.UsageFilter{Range: "custom", CustomUnit: "hour", StartTime: &f.start, EndTime: &f.end, EndExclusive: true}
	analysis, err := usage.GetAnalysis(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.AIProviderComposition) != 1 {
		t.Fatalf("provider rows %+v", analysis.AIProviderComposition)
	}
	closeCost(t, analysis.AIProviderComposition[0].CostUSD, 29.4)
	if analysis.AIProviderComposition[0].ReasoningTokens != 123 {
		t.Fatal("typed analysis lost reasoning tokens")
	}
	for _, row := range analysis.AuthFilesComposition {
		if row.Key == "synthetic-a" {
			closeCost(t, row.CostUSD, 4.04)
		}
	}
	comparisons, err := usage.(service.UsageComparisonProvider).GetUsageOverviewComparisons(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	closeCost(t, comparisons.Comparisons.AuthFiles["synthetic-a"].CostUSD, 4.04)
	closeCost(t, comparisons.Comparisons.AIProviders["synthetic-a"].CostUSD, 29.4)
	typed := comparisons.Comparisons.AIProviders["synthetic-a"]
	if typed.Failures != 1 || typed.ReasoningTokens != 123 {
		t.Fatalf("typed comparison lost facts: %+v", typed)
	}
	cache, err := repository.NewUsageRecentEventCache(f.db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return f.now }})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	for _, reader := range []service.UsageProvider{usage, service.NewUsageServiceWithRecentCache(f.db, cache, f.catalog)} {
		realtime, err := reader.GetUsageOverviewRealtime(ctx, servicedto.UsageFilter{RealtimeWindow: "15m", RealtimeEndTime: &f.now})
		if err != nil {
			t.Fatal(err)
		}
		assertRealtimeCredentialCosts(t, realtime.CurrentUsage.AuthFiles, map[string]float64{"synthetic-a": 4.04, "synthetic-b": 29.4})
		assertRealtimeCredentialCosts(t, realtime.CurrentUsage.AIProviders, map[string]float64{"synthetic-a": 29.4})
	}
	if err := f.db.Where("event_key = ?", event.EventKey).Delete(&entities.UsageEvent{}).Error; err != nil {
		t.Fatal(err)
	}
	for _, grain := range []string{"hour", "day"} {
		query := filter
		query.CustomUnit = grain
		overview, err := usage.GetUsageOverview(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		if overview.Summary.CostAvailable || overview.Summary.UnavailableReason != "retained_pricing_evidence_incomplete" {
			t.Fatalf("mixed historical false completeness %+v", overview.Summary)
		}
		closeCost(t, overview.Summary.TotalCost, 29.4)
	}
}

func TestCredentialDefaultZeroBillableHistoricalCompositionKeepsRollupFacts(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	event := entities.UsageEvent{EventKey: "zero-billable-history", APIGroupKey: "synthetic-downstream", Model: "base", AuthIndex: "synthetic-a", AuthType: "oauth", Failed: true, ReasoningTokens: 123, Timestamp: f.start.Add(10 * time.Hour)}
	if _, _, err := repository.InsertUsageEvents(f.db, []entities.UsageEvent{event}); err != nil {
		t.Fatal(err)
	}
	if err := repository.AggregateUsageOverviewStats(ctx, f.db, f.end); err != nil {
		t.Fatal(err)
	}
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Where("event_key = ?", event.EventKey).Delete(&entities.UsageEvent{}).Error; err != nil {
		t.Fatal(err)
	}
	usage := service.NewUsageService(f.db, f.catalog)
	for _, grain := range []string{"hour", "day"} {
		filter := servicedto.UsageFilter{Range: "custom", CustomUnit: grain, StartTime: &f.start, EndTime: &f.end, EndExclusive: true}
		analysis, err := usage.GetAnalysis(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		if !analysis.CostBreakdown.CostAvailable {
			t.Fatal("zero billable history must remain available zero")
		}
		for _, item := range analysis.AuthFilesComposition {
			if item.Key == "synthetic-a" && (item.Requests != 4 || item.ReasoningTokens != 123) {
				t.Fatalf("%s zero billable analysis lost rollup facts: %+v", grain, item)
			}
		}
		comparisons, err := usage.(service.UsageComparisonProvider).GetUsageOverviewComparisons(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		item := comparisons.Comparisons.AuthFiles["synthetic-a"]
		if item == nil || item.Requests != 4 || item.Failures != 1 || item.ReasoningTokens != 123 || !item.CostAvailable {
			t.Fatalf("%s zero billable comparison lost rollup facts: %+v", grain, item)
		}
		closeCost(t, item.CostUSD, 4.04)
	}
}

func TestCredentialDefaultAggregationMembershipRejectsEqualTokenLateReplacement(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	other := entities.UsageIdentity{Name: "Safe API account", AuthType: entities.UsageIdentityAuthTypeAIProvider, AuthTypeName: "apikey", Identity: "synthetic-a", Type: "openai", BindingIdentityStatus: "unique"}
	if err := f.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.prices.(service.PricingCredentialProvider).BindPricingCredential(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, "0"); err != nil {
		t.Fatal(err)
	}
	start := f.start.Add(10 * time.Hour)
	end := start.Add(time.Hour)
	old := entities.UsageEvent{EventKey: "old-member", APIGroupKey: "synthetic-downstream", Model: "base", AuthIndex: "synthetic-a", AuthType: "apikey", Timestamp: start.Add(time.Minute), InputTokens: 1_000_000, TotalTokens: 1_000_000}
	if _, _, err := repository.InsertUsageEvents(f.db, []entities.UsageEvent{old}); err != nil {
		t.Fatal(err)
	}
	if err := repository.AggregateUsageOverviewStats(ctx, f.db, f.end); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Where("event_key = ?", old.EventKey).Delete(&entities.UsageEvent{}).Error; err != nil {
		t.Fatal(err)
	}
	late := old
	late.EventKey = "unaggregated-late"
	late.AuthType = "oauth"
	if _, _, err := repository.InsertUsageEvents(f.db, []entities.UsageEvent{late}); err != nil {
		t.Fatal(err)
	}
	usage := service.NewUsageService(f.db, f.catalog)
	filter := servicedto.UsageFilter{Range: "custom", CustomUnit: "hour", StartTime: &start, EndTime: &end, EndExclusive: true}
	for _, aggregate := range []bool{false, true} {
		if aggregate {
			if err := repository.AggregateUsageOverviewStats(ctx, f.db, f.end); err != nil {
				t.Fatal(err)
			}
		}
		overview, err := usage.GetUsageOverview(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		if overview.Summary.CostAvailable || overview.Summary.UnavailableReason != "retained_pricing_evidence_incomplete" {
			t.Fatalf("late event fabricated membership afterCatchup=%v %+v", aggregate, overview.Summary)
		}
		analysis, err := usage.GetAnalysis(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		if analysis.CostBreakdown.CostAvailable || analysis.CostBreakdown.UnavailableReason != "retained_pricing_evidence_incomplete" {
			t.Fatalf("analysis fabricated membership %+v", analysis.CostBreakdown)
		}
	}
	// An unrelated ingested event above the cursor cannot invalidate a complete
	// retained historic cohort for another credential.
	if _, _, err := repository.InsertUsageEvents(f.db, []entities.UsageEvent{{EventKey: "unrelated-lag", Model: "missing", AuthIndex: "unrelated", Timestamp: f.end.Add(time.Minute), InputTokens: 1}}); err != nil {
		t.Fatal(err)
	}
	start = f.start.Add(12 * time.Hour)
	end = start.Add(time.Hour)
	overview, err := usage.GetUsageOverview(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if !overview.Summary.CostAvailable {
		t.Fatalf("unrelated lag invalidated history %+v", overview.Summary)
	}
	closeCost(t, overview.Summary.TotalCost, 29.4)
}

func TestCredentialDefaultMetadataRefreshAmbiguousStaleFailureAndRecovery(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	if _, err := f.defaults.SetCredentialDefault(ctx, f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	fetcher := newMetadataTestFetcher()
	file := credentials.Credential{AuthIndex: "synthetic-a", Email: "safe@example.invalid", Name: "synthetic.json", Type: "codex", Provider: "codex"}
	fetcher.setCredentials([]credentials.Credential{file})
	syncer := service.NewSyncServiceWithOptions(f.db, service.SyncServiceOptions{BaseURL: "https://cpa.example.invalid", MetadataFetcher: fetcher, Now: func() time.Time { return f.now }, PricingCatalog: f.catalog})
	assertFresh := func(expected float64) {
		t.Helper()
		usage := service.NewUsageService(f.db, f.catalog)
		page, err := usage.ListUsageEvents(ctx, servicedto.UsageFilter{Range: "custom", CustomUnit: "hour", StartTime: &f.start, EndTime: &f.end, EndExclusive: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range page.Events {
			if event.InputTokens == 1_000_000 && event.OutputTokens == 100_000 && event.AuthIndex == "synthetic-a" {
				closeCost(t, event.CostUSD, expected)
				warning := ""
				if expected == 29.4 {
					warning = "unresolved_identity"
				}
				selection := event.PricingSelection
				if selection == nil || selection.AttributionWarning != warning || selection.ChannelID != "" || selection.ChannelName != "" || selection.SnapshotID != page.PricingSnapshotID || event.PricingSnapshotID != page.PricingSnapshotID || page.PricingSnapshotID != f.catalog.Snapshot().ID() {
					t.Fatalf("credential-only metadata explanation %+v page=%s", selection, page.PricingSnapshotID)
				}
				if warning != "" && (selection.Scope != "legacy" || selection.SubjectID != "") {
					t.Fatalf("ambiguous metadata guessed attribution %+v", selection)
				}
				return
			}
		}
		t.Fatal("fixture request not found")
	}
	if err := syncer.SyncMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	assertFresh(1.96)
	old := f.catalog.NewResolver()
	// A successful empty list marks stale, not an erased historical link.
	fetcher.setCredentials(nil)
	if err := syncer.SyncMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	assertFresh(1.96)
	fetcher.setCredentials([]credentials.Credential{file})
	fetcher.credentialsErr = errors.New("synthetic transient source failure")
	if err := syncer.SyncMetadata(ctx); err == nil {
		t.Fatal("expected warning")
	}
	assertFresh(1.96)
	second := file
	second.Name = "distinct.json"
	second.Email = "other@example.invalid"
	fetcher.setCredentials([]credentials.Credential{file, second})
	if err := syncer.SyncMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	assertFresh(29.4)
	closeCost(t, old.Calculate(repository.UsageEventCostSubject(f.events[0])).Cost.TotalCostUSD, 1.96)
	fetcher.setCredentials([]credentials.Credential{file})
	if err := syncer.SyncMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	assertFresh(1.96)
	// The directory commit succeeds, then context cancellation interrupts the
	// pricing reload. No subsequent price mutation is allowed to repair it.
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	callbackName := "synthetic_cancel_pricing_refresh"
	if err := f.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == "credential_price_defaults" {
			cancel()
			tx.AddError(cancelCtx.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	fetcher.setCredentials([]credentials.Credential{file, second})
	if err := syncer.SyncMetadata(cancelCtx); err == nil {
		t.Fatal("expected reload failure")
	}
	if err := f.db.Callback().Query().Remove(callbackName); err != nil {
		t.Fatal(err)
	}
	var directory entities.UsageIdentity
	if err := f.db.First(&directory, "identity = ? AND auth_type = ?", "synthetic-a", entities.UsageIdentityAuthTypeAuthFile).Error; err != nil {
		t.Fatal(err)
	}
	if directory.BindingIdentityStatus != "ambiguous" {
		t.Fatalf("ambiguity was not committed: %+v", directory)
	}
	assertFresh(29.4)
	readback, err := f.defaults.GetCredentialDefault(ctx, f.subjectID)
	if err != nil || readback.Multiplier == nil || *readback.Multiplier != .2 {
		t.Fatalf("invalidation lost config %+v %v", readback, err)
	}
	fetcher.setCredentials([]credentials.Credential{file})
	if err := syncer.SyncMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	assertFresh(1.96)
}

func TestCredentialDefaultPartialProviderFailurePreservesPreviouslySavedExactEvidence(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	ctx := context.Background()
	identity := entities.UsageIdentity{Name: "Safe provider account", AuthType: entities.UsageIdentityAuthTypeAIProvider, AuthTypeName: "apikey", Identity: "synthetic-provider", Type: "codex", BindingIdentityStatus: "unique"}
	if err := f.db.Create(&identity).Error; err != nil {
		t.Fatal(err)
	}
	bound, err := f.prices.(service.PricingCredentialProvider).BindPricingCredential(ctx, identity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.defaults.SetCredentialDefault(ctx, bound.SubjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	fetcher := newMetadataTestFetcher()
	fetcher.standardResults["codex"] = &response.ProviderKeyConfigResult{StatusCode: 200, Payload: []providerconfig.ProviderKeyConfig{{Name: "Safe account renamed", AuthIndex: identity.Identity, APIKey: "synthetic-source-key"}}}
	fetcher.standardErrors["xai"] = errors.New("synthetic source unavailable")
	syncer := service.NewSyncServiceWithOptions(f.db, service.SyncServiceOptions{BaseURL: "https://cpa.example.invalid", MetadataFetcher: fetcher, Now: func() time.Time { return f.now }, PricingCatalog: f.catalog})
	if err := syncer.SyncMetadata(ctx); err == nil {
		t.Fatal("expected partial warning")
	}
	subject := repository.UsageEventCostSubject(entities.UsageEvent{Model: "base", AuthType: "apikey", AuthIndex: identity.Identity, InputTokens: 1_000_000})
	closeCost(t, f.catalog.NewResolver().Calculate(subject).Cost.TotalCostUSD, 2)
	var saved entities.UsageIdentity
	if err := f.db.First(&saved, identity.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.BindingIdentityStatus != "unique" {
		t.Fatalf("transient warning invalidated prior evidence %+v", saved)
	}
}
