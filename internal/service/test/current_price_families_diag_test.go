package test

import (
	"context"
	"math"
	"testing"
	"time"

	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func (f credentialDefaultFixture) diagnosticCostFamilies(t *testing.T, expected float64, includeDetails bool) {
	ctx := context.Background()
	usage := service.NewUsageService(f.db, f.catalog)
	filter := servicedto.UsageFilter{Range: "custom", CustomUnit: "hour", StartTime: &f.start, EndTime: &f.end, EndExclusive: true}
	overview, err := usage.GetUsageOverview(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	diagnosticCloseCost(t, overview.Summary.TotalCost, expected)
	if !overview.Summary.CostAvailable {
		t.Fatal("overview incomplete")
	}
	sum := 0.0
	for _, cost := range overview.Series.Cost {
		sum += cost
	}
	diagnosticCloseCost(t, sum, expected)
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
	diagnosticCloseCost(t, sum, expected)
	sum = 0
	for _, item := range comparisons.Comparisons.AuthFiles {
		sum += item.CostUSD
	}
	for _, item := range comparisons.Comparisons.AIProviders {
		sum += item.CostUSD
	}
	diagnosticCloseCost(t, sum, expected)
	analysis, err := usage.GetAnalysis(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	diagnosticCloseCost(t, analysis.CostBreakdown.TotalCostUSD, expected)
	if !analysis.CostBreakdown.CostAvailable {
		t.Fatal("analysis incomplete")
	}
	diagnosticCloseCost(t, analysis.CostBreakdown.UncachedInputCostUSD+analysis.CostBreakdown.OutputCostUSD+analysis.CostBreakdown.CacheReadCostUSD+analysis.CostBreakdown.CacheWriteCostUSD, expected)
	sum = 0
	for _, item := range analysis.ModelEfficiency {
		sum += item.CostUSD
	}
	diagnosticCloseCost(t, sum, expected)
	sum = 0
	for _, item := range analysis.AIProviderComposition {
		sum += item.CostUSD
	}
	for _, item := range analysis.AuthFilesComposition {
		sum += item.CostUSD
	}
	diagnosticCloseCost(t, sum, expected)
	// Daily and hourly use identical retained pricing evidence, not token sums.
	daily := filter
	daily.CustomUnit = "day"
	dailyEnd := f.start.AddDate(0, 0, 1)
	daily.EndTime = &dailyEnd
	dailyOverview, err := usage.GetUsageOverview(ctx, daily)
	if err != nil {
		t.Fatal(err)
	}
	diagnosticCloseCost(t, dailyOverview.Summary.TotalCost, expected)
	dailyAnalysis, err := usage.GetAnalysis(ctx, daily)
	if err != nil {
		t.Fatal(err)
	}
	diagnosticCloseCost(t, dailyAnalysis.CostBreakdown.TotalCostUSD, expected)
	windowA, err := repository.SumUsageWindowStatsByAuthIndex(ctx, f.db, "synthetic-a", f.start, &f.end, f.catalog.NewResolver())
	if err != nil {
		t.Fatal(err)
	}
	windowB, err := repository.SumUsageWindowStatsByAuthIndex(ctx, f.db, "synthetic-b", f.start, &f.end, f.catalog.NewResolver())
	if err != nil {
		t.Fatal(err)
	}
	diagnosticCloseCost(t, windowA.Cost+windowB.Cost, expected)
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
	diagnosticCloseCost(t, grouped.Groups["synthetic-group"].Cost, windowA.Cost)
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
	diagnosticCloseCost(t, sum, expected)
	sum = 0
	if err := usage.StreamUsageEvents(ctx, filter, func(event servicedto.UsageEventRecord) error { sum += event.CostUSD; return nil }); err != nil {
		t.Fatal(err)
	}
	diagnosticCloseCost(t, sum, expected)
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
	diagnosticCloseCost(t, sum, expected)
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
	diagnosticCloseCost(t, sum, expected)
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
	diagnosticCloseCost(t, shortA.Cost+shortB.Cost, expected)
}

func diagnosticCloseCost(t *testing.T, actual, expected float64) {
	t.Helper()
	if math.Abs(actual-expected) > 1e-9 {
		t.Errorf("cost %.12f want %.12f", actual, expected)
	}
}
func TestDiagnosticAllCurrentFamilies(t *testing.T) {
	f := newCredentialDefaultFixture(t)
	if _, err := f.defaults.SetCredentialDefault(context.Background(), f.subjectID, ".2"); err != nil {
		t.Fatal(err)
	}
	f.diagnosticCostFamilies(t, 33.44, true)
}
