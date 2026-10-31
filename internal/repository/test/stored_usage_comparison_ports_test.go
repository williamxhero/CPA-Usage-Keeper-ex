package test

import (
	"context"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
	"cpa-usage-keeper/internal/timeutil"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// Ported verbatim assertions from usage_overview_comparisons_stored_test.go / TestOverviewComparisonDailyStoredFeeRequiresCompleteRows
func TestStoredPortOverviewComparisonDailyStoredFeeRequiresCompleteRows(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db := openTestDatabase(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	zeroUnavailable := int64(0)
	rows := []entities.UsageOverviewDailyStat{
		{BucketStart: start, APIGroupKey: "old-key", Model: "model-a", ModelAlias: "old-a", AuthIndex: "auth-a", RequestCount: 1, InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: overviewCostPtr(2.75), UnavailableCostCount: &zeroUnavailable},
		{BucketStart: start, APIGroupKey: "old-key", Model: "model-a", ModelAlias: "old-b", AuthIndex: "auth-a", RequestCount: 1, InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: overviewCostPtr(3.5), UnavailableCostCount: &zeroUnavailable},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed daily comparison rows: %v", err)
	}
	if err := db.Migrator().DropTable(&entities.UsageEvent{}); err != nil {
		t.Fatalf("drop hot events: %v", err)
	}
	filter := dto.UsageQueryFilter{Range: "custom", CustomUnit: "day", StartTime: &start, EndTime: &end, EndExclusive: true}
	result, err := storedComparisonTestAdapter(db, filter, nil)
	if err != nil {
		t.Fatalf("read daily comparison without hot events: %v", err)
	}
	if len(result.Comparisons.Models) != 1 || !finiteStoredOverviewTestCost(result.Comparisons.Models["model-a"].CostUSD, 6.25) || result.Comparisons.Models["model-a"].Requests != 2 {
		t.Fatalf("daily stored fee or real model grouping changed: %+v", result.Comparisons.Models)
	}
	if err := db.Create(&entities.UsageOverviewDailyStat{BucketStart: start, APIGroupKey: "old-key", Model: "model-a", ModelAlias: "pending", AuthIndex: "auth-a", RequestCount: 1}).Error; err != nil {
		t.Fatalf("seed partially unbackfilled daily row: %v", err)
	}
	if _, err := storedComparisonTestAdapter(db, filter, nil); err == nil || !strings.Contains(err.Error(), "unbackfilled cost") {
		t.Fatalf("partially NULL comparison group was accepted: %v", err)
	}
}

// 比较边界的缓存和数据库来源都不能把旧事件的 NULL 金额当作已知零价。
// Ported verbatim assertions from usage_overview_comparisons_stored_test.go / TestOverviewComparisonBoundaryRejectsUnbackfilledStoredFee
func TestStoredPortOverviewComparisonBoundaryRejectsUnbackfilledStoredFee(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db := openTestDatabase(t)
	end := time.Date(2026, 9, 12, 12, 30, 0, 0, time.UTC)
	start := end.Add(-30 * time.Minute)
	if _, _, err := repository.InsertUsageEvents(db, []entities.UsageEvent{{
		EventKey: "old-null", Timestamp: start.Add(10 * time.Minute), APIGroupKey: "old-key", Model: "model-a", InputTokens: 10, TotalTokens: 10,
	}}); err != nil {
		t.Fatalf("seed unbackfilled comparison event: %v", err)
	}
	cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{Now: func() time.Time { return end.Add(time.Minute) }})
	if err != nil {
		t.Fatalf("load recent events: %v", err)
	}
	t.Cleanup(cache.Close)
	filter := dto.UsageQueryFilter{Range: "custom", StartTime: &start, EndTime: &end, EndExclusive: true, QueryNow: &end}
	for _, source := range []*repository.UsageRecentEventCache{cache, nil} {
		if _, err := storedComparisonTestAdapter(db, filter, source); err == nil || !strings.Contains(err.Error(), "unbackfilled cost") {
			t.Fatalf("comparison accepted NULL boundary fee from cache=%t: %v", source != nil, err)
		}
	}
}

// Ported verbatim assertions from usage_overview_comparison_aggregation_test.go / TestOverviewComparisonSplitAggregationMatchesEvents
func TestStoredPortOverviewComparisonSplitAggregationMatchesEvents(t *testing.T) {
	for _, unit := range []string{"hour", "day"} {
		for _, scope := range []string{"", "key-a"} {
			t.Run(unit+"/"+scope, func(t *testing.T) {
				db := openTestDatabase(t)
				start := time.Date(2026, 8, 10, 0, 0, 0, 0, time.Local)
				end := start.AddDate(0, 0, 3)
				if unit == "hour" {
					end = start.Add(3 * time.Hour)
				}
				ids := []entities.UsageIdentity{
					{AuthType: entities.UsageIdentityAuthTypeCredential, Identity: "file-a", Name: "same"},
					{AuthType: entities.UsageIdentityAuthTypeAIProvider, Identity: "provider-a", Name: "same"},
				}
				if err := db.Create(&ids).Error; err != nil {
					t.Fatal(err)
				}
				var events []entities.UsageEvent
				for bucket := 0; bucket < 3; bucket++ {
					stamp := start.Add(time.Duration(bucket) * time.Hour)
					if unit == "day" {
						stamp = start.AddDate(0, 0, bucket)
					}
					for _, key := range []string{"key-a", "key-b"} {
						for _, model := range []string{"model-a", "model-b"} {
							for _, auth := range []string{"file-a", "provider-a", "unresolved"} {
								for _, tier := range []string{"", "priority"} {
									n := int64(len(events) + 1)
									events = append(events, entities.UsageEvent{EventKey: fmt.Sprint(n), Timestamp: stamp.Add(time.Minute), APIGroupKey: key, Model: model, AuthIndex: auth, ServiceTier: tier, ModelAlias: &model, InputTokens: 100 + n, OutputTokens: 20, CacheReadTokens: n % 7, CacheCreationTokens: n % 3, ReasoningTokens: 3, TotalTokens: 120 + n, Failed: n%5 == 0})
								}
							}
						}
					}
				}
				// 非可加的历史 Token 不影响已存费用，统计不得重新按 Token 推导价格。
				events[0].InputTokens = -3
				events[1].CacheReadTokens = 999
				for i := range events {
					cost, available := float64(i+1)/100, i%7 != 0
					events[i].CostUSD, events[i].CostAvailable = &cost, &available
				}
				if err := db.Create(&events).Error; err != nil {
					t.Fatal(err)
				}
				if err := repository.AggregateUsageOverviewStats(context.Background(), db, end); err != nil {
					t.Fatal(err)
				}
				filter := dto.UsageQueryFilter{Range: "custom", CustomUnit: unit, StartTime: &start, EndTime: &end, EndExclusive: true, QueryNow: &end, ComparisonOnly: true, APIGroupKey: scope}
				got, err := storedComparisonTestAdapter(db, filter, nil)
				if err != nil {
					t.Fatal(err)
				}
				dimensions := []map[string]*dto.UsageComparisonItemRecord{got.Comparisons.Models, got.Comparisons.APIKeys, got.Comparisons.Credentials, got.Comparisons.AIProviders}
				for dim, items := range dimensions {
					expected := make(map[string]*dto.UsageComparisonItemRecord)
					for _, e := range events {
						if scope != "" && e.APIGroupKey != scope {
							continue
						}
						key := e.Model
						switch dim {
						case 1:
							key = e.APIGroupKey
						case 2:
							if e.AuthIndex != "file-a" {
								continue
							}
							key = e.AuthIndex
						case 3:
							if e.AuthIndex != "provider-a" {
								continue
							}
							key = e.AuthIndex
						}
						item := expected[key]
						if item == nil {
							item = &dto.UsageComparisonItemRecord{CostAvailable: true, TokenBuckets: map[string]int64{}}
							expected[key] = item
						}
						item.Requests++
						if e.Failed {
							item.Failures++
						}
						item.InputTokens += e.InputTokens
						item.OutputTokens += e.OutputTokens
						item.CacheReadTokens += e.CacheReadTokens
						item.CacheCreationTokens += e.CacheCreationTokens
						item.ReasoningTokens += e.ReasoningTokens
						item.TotalTokens += e.TotalTokens
						item.CostUSD += *e.CostUSD
						item.CostAvailable = item.CostAvailable && *e.CostAvailable
						bucket := e.Timestamp.Truncate(time.Hour).Format(time.RFC3339Nano)
						if unit == "day" {
							bucket = e.Timestamp.Format(time.DateOnly)
						}
						item.TokenBuckets[bucket] += e.TotalTokens
					}
					if len(items) != len(expected) {
						t.Fatalf("dimension %d items=%d want %d", dim, len(items), len(expected))
					}
					for key, want := range expected {
						item := items[key]
						if item == nil {
							t.Fatalf("missing dimension %d key %s", dim, key)
						}
						if item.Requests != want.Requests || item.Failures != want.Failures || item.TotalTokens != want.TotalTokens || item.InputTokens != want.InputTokens || item.OutputTokens != want.OutputTokens || item.CacheReadTokens != want.CacheReadTokens || item.CacheCreationTokens != want.CacheCreationTokens || item.ReasoningTokens != want.ReasoningTokens || item.CostAvailable != want.CostAvailable || math.Abs(item.CostUSD-want.CostUSD) > 1e-9 {
							t.Fatalf("dimension %d key %s totals: got %+v want %+v", dim, key, item, want)
						}
						if len(item.TokenBuckets) != len(want.TokenBuckets) {
							t.Fatalf("dimension %d key %s unexpected buckets: %v", dim, key, item.TokenBuckets)
						}
						for bucket, tokens := range want.TokenBuckets {
							if item.TokenBuckets[bucket] != tokens {
								t.Fatalf("dimension %d key %s bucket %s got %d want %d", dim, key, bucket, item.TokenBuckets[bucket], tokens)
							}
						}
					}
				}
			})
		}
	}
}

// Ported verbatim assertions from usage_overview_comparison_series_test.go / TestOverviewComparisonIdentitySeriesPreservesBoundariesAndDistinctIdentities
func TestStoredPortOverviewComparisonIdentitySeriesPreservesBoundariesAndDistinctIdentities(t *testing.T) {
	db := openTestDatabase(t)
	start := time.Date(2026, 9, 12, 8, 30, 0, 0, time.Local)
	end := start.Add(4 * time.Hour)
	identities := []entities.UsageIdentity{
		{AuthType: entities.UsageIdentityAuthTypeCredential, Identity: "file-a", Name: "same.json"},
		{AuthType: entities.UsageIdentityAuthTypeCredential, Identity: "file-b", Name: "same.json"},
		{AuthType: entities.UsageIdentityAuthTypeAIProvider, Identity: "provider-a", Name: "OpenAI"},
	}
	if err := db.Create(&identities).Error; err != nil {
		t.Fatal(err)
	}
	events := []entities.UsageEvent{
		{EventKey: "file-left", Timestamp: start, APIGroupKey: "key-a", Model: "model-a", AuthType: "oauth", AuthIndex: "file-a", TotalTokens: 10},
		{EventKey: "file-rollup", Timestamp: start.Add(time.Hour), APIGroupKey: "key-a", Model: "model-a", AuthType: "oauth", AuthIndex: "file-b", TotalTokens: 20},
		{EventKey: "provider-rollup", Timestamp: start.Add(2 * time.Hour), APIGroupKey: "key-b", Model: "model-b", AuthType: "apikey", AuthIndex: "provider-a", TotalTokens: 30},
		{EventKey: "provider-right", Timestamp: end.Add(-time.Minute), APIGroupKey: "key-b", Model: "model-b", AuthType: "apikey", AuthIndex: "provider-a", TotalTokens: 40},
	}
	events = priceOverviewFixtureEvents(t, db, events)
	if err := db.Create(&events).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.AggregateUsageOverviewStats(context.Background(), db, end); err != nil {
		t.Fatal(err)
	}
	filter := dto.UsageQueryFilter{Range: "4h", StartTime: &start, EndTime: &end, EndExclusive: true, QueryNow: &end, ComparisonOnly: true}
	overview, err := storedComparisonTestAdapter(db, filter, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := overview.Comparisons
	if len(c.Credentials) != 2 || len(c.AIProviders) != 1 {
		t.Fatalf("identity collision: %+v", c)
	}
	for _, dimension := range []map[string]*dto.UsageComparisonItemRecord{c.Models, c.APIKeys, c.Credentials, c.AIProviders} {
		for _, item := range dimension {
			var sum int64
			for _, bucket := range c.Buckets {
				sum += item.TokenBuckets[bucket]
			}
			if sum != item.TotalTokens {
				t.Fatalf("timeline total %d != %d for %s", sum, item.TotalTokens, item.Key)
			}
		}
	}
	if c.Credentials["file-a"].TokenBuckets[c.Buckets[0]] != 10 || c.Credentials["file-b"].TokenBuckets[c.Buckets[1]] != 20 || c.AIProviders["provider-a"].TokenBuckets[c.Buckets[4]] != 40 {
		t.Fatal("identity buckets lost boundary/rollup data")
	}
	filter.APIGroupKey = "key-a"
	scoped, err := storedComparisonTestAdapter(db, filter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.Comparisons.AIProviders) != 0 || len(scoped.Comparisons.APIKeys) != 1 {
		t.Fatal("scoped timeline leaked another key")
	}
}

// Ported verbatim assertions from usage_comparison_calendar_day_test.go / TestComparisonCalendarDayMatchesAnalysisEndpoint
func TestStoredPortComparisonCalendarDayMatchesAnalysisEndpoint(t *testing.T) {
	for _, tc := range []struct {
		zone, date string
		hours      int
	}{
		{"Asia/Shanghai", "2026-03-08", 24},
		{"America/Los_Angeles", "2026-03-08", 23},
		{"America/Los_Angeles", "2026-11-01", 25},
		{"Europe/Berlin", "2026-10-25", 25},
		{"America/Santiago", "2026-09-06", 23},
	} {
		t.Run(tc.zone+"/"+tc.date, func(t *testing.T) {
			location := setAnalysisLatencyTestTimezone(t, tc.zone)
			date, _ := timeutil.ParseCivilDate(tc.date)
			bounds, _ := timeutil.DayBounds(date, location)
			db := openTestDatabase(t)
			cost, unavailable := 1.0, int64(0)
			available := true
			// Include a real next-day row: the display endpoint must not read it.
			for i := 0; i <= tc.hours; i++ {
				row := entities.UsageOverviewHourlyStat{BucketStart: bounds.Start.Add(time.Duration(i) * time.Hour),
					APIGroupKey: "calendar-key", Model: "model", RequestCount: 1, TotalTokens: 100,
					CostUSD: &cost, UnavailableCostCount: &unavailable}
				if err := db.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
				// The inclusive day's final partial-hour query reads raw events.
				event := entities.UsageEvent{EventKey: fmt.Sprintf("calendar-%d", i), Timestamp: row.BucketStart.Add(time.Minute),
					APIGroupKey: row.APIGroupKey, Model: row.Model, TotalTokens: row.TotalTokens,
					CostUSD: &cost, CostAvailable: &available}
				if err := db.Create(&event).Error; err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"today", "yesterday"} {
				anchor := bounds.Start.Add(12 * time.Hour)
				if name == "yesterday" {
					anchor = bounds.End.Add(12 * time.Hour)
				}
				window, err := timeutil.ParseUsageQueryRange(name, "", "", "", anchor)
				if err != nil {
					t.Fatal(err)
				}
				now := bounds.End.Add(48 * time.Hour)
				filter := dto.UsageQueryFilter{Range: name, StartTime: &window.StartTime, EndTime: &window.EndTime, QueryNow: &now}
				result, err := storedComparisonTestAdapter(db, filter, nil)
				if err != nil {
					t.Fatal(err)
				}
				comparison := result.Comparisons
				if comparison.Granularity != "hourly" || len(comparison.Buckets) != tc.hours+1 {
					t.Fatalf("%s: expected %d hourly points, got %s/%d", name, tc.hours+1, comparison.Granularity, len(comparison.Buckets))
				}
				analysis, err := repository.BuildAnalysisWithFilter(db, filter)
				if err != nil {
					t.Fatal(err)
				}
				for i, key := range comparison.Buckets {
					if key != timeutil.FormatStorageTime(analysis.TokenUsage[i].Bucket) {
						t.Fatalf("%s: axis differs at %d: %s", name, i, key)
					}
				}
				model := comparison.Models["model"]
				if model == nil || model.TotalTokens != int64(tc.hours*100) || model.Requests != int64(tc.hours) || model.CostUSD != float64(tc.hours) {
					t.Fatalf("%s: changed totals or included next day: %+v", name, model)
				}
				if model.TokenBuckets[timeutil.FormatStorageTime(bounds.End)] != 0 {
					t.Fatal("next-day endpoint must be empty")
				}
				// A query during today still displays the full day, like Analysis.
				if name == "today" {
					filter.QueryNow = &anchor
					current, err := storedComparisonTestAdapter(db, filter, nil)
					if err != nil || len(current.Comparisons.Buckets) != tc.hours+1 {
						t.Fatalf("today axis is truncated at query time: %v", err)
					}
				}
			}
		})
	}
}

// Ported verbatim assertions from usage_overview_comparisons_test.go / TestOverviewComparisonsShareRollupsAndExactBoundaries
func TestStoredPortOverviewComparisonsShareRollupsAndExactBoundaries(t *testing.T) {
	db := openTestDatabase(t)
	end := time.Date(2026, 9, 12, 12, 30, 0, 0, time.Local)
	start := end.Add(-4 * time.Hour)
	events := []entities.UsageEvent{
		{EventKey: "outside", Timestamp: start.Add(-time.Minute), APIGroupKey: "key-a", Model: "model-a", TotalTokens: 9999},
		{EventKey: "left", Timestamp: start, APIGroupKey: "key-a", Model: "model-a", InputTokens: 100, CacheReadTokens: 40, OutputTokens: 20, TotalTokens: 120},
		{EventKey: "middle", Timestamp: start.Add(time.Hour), APIGroupKey: "key-b", Model: "model-a", InputTokens: 200, OutputTokens: 30, TotalTokens: 230},
		{EventKey: "failure", Timestamp: start.Add(2 * time.Hour), APIGroupKey: "key-b", Model: "model-b", Failed: true},
		{EventKey: "right", Timestamp: end.Add(-time.Minute), APIGroupKey: "key-a", Model: "model-b", InputTokens: 50, CacheCreationTokens: 10, OutputTokens: 40, TotalTokens: 90},
	}
	events = priceOverviewFixtureEvents(t, db, events)
	if err := db.Create(&events).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.AggregateUsageOverviewStats(context.Background(), db, end); err != nil {
		t.Fatal(err)
	}
	filter := dto.UsageQueryFilter{Range: "4h", StartTime: &start, EndTime: &end, EndExclusive: true, QueryNow: &end, ComparisonOnly: true}
	queries := captureOverviewDataQueries(t, db, "comparisons")
	overview, err := storedComparisonTestAdapter(db, filter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Comparisons == nil {
		t.Fatal("missing comparisons")
	}
	a, b := overview.Comparisons.Models["model-a"], overview.Comparisons.APIKeys["key-b"]
	if a.Requests != 2 || a.TotalTokens != 350 || a.OutputTokens != 50 || a.CacheReadTokens != 40 || a.CostAvailable {
		t.Fatalf("model totals: %+v", a)
	}
	if b.Requests != 2 || b.Failures != 1 || b.TotalTokens != 230 {
		t.Fatalf("key totals: %+v", b)
	}
	if len(overview.Comparisons.Buckets) != 5 || overview.Comparisons.Granularity != "hourly" {
		t.Fatalf("expected five hourly buckets including idle hours: %+v", overview.Comparisons)
	}
	if a.TokenBuckets[overview.Comparisons.Buckets[0]] != 120 || a.TokenBuckets[overview.Comparisons.Buckets[1]] != 230 || overview.Comparisons.Models["model-b"].TokenBuckets[overview.Comparisons.Buckets[4]] != 90 {
		t.Fatal("boundary and rollup tokens must retain their original time buckets")
	}
	if overview.Usage.TotalRequests != 0 || len(overview.Series.Requests) != 0 {
		t.Fatal("comparison-only query must not build overview totals or series")
	}
	if len(*queries) != 6 {
		t.Fatalf("expected two boundary reads, one summary and three token series reads, got %d", len(*queries))
	}
	filter.APIGroupKey = "key-a"
	filtered, err := storedComparisonTestAdapter(db, filter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered.Comparisons.APIKeys) != 1 || filtered.Comparisons.APIKeys["key-a"].TotalTokens != 210 || filtered.Usage.TotalTokens != 0 {
		t.Fatalf("key filter leaked: %+v", filtered.Comparisons)
	}
}

// Ported verbatim assertions from usage_overview_comparisons_test.go / TestOverviewComparisonsCustomDayNeverReadsRawEvents
func TestStoredPortOverviewComparisonsCustomDayNeverReadsRawEvents(t *testing.T) {
	db := openTestDatabase(t)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 0, 365)
	zeroCost, unavailable := 0.0, int64(0)
	rows := []entities.UsageOverviewDailyStat{
		{BucketStart: start, APIGroupKey: "key-a", Model: "model-a", RequestCount: 5, SuccessCount: 4, FailureCount: 1, TotalTokens: 100, CostUSD: &zeroCost, UnavailableCostCount: &unavailable},
		{BucketStart: end.AddDate(0, 0, -1), APIGroupKey: "key-b", Model: "model-a", RequestCount: 3, SuccessCount: 3, TotalTokens: 300, CostUSD: &zeroCost, UnavailableCostCount: &unavailable},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	queries := captureOverviewDataQueries(t, db, "comparisons_long")
	filter := dto.UsageQueryFilter{Range: "custom", CustomUnit: "day", StartTime: &start, EndTime: &end, EndExclusive: true, QueryNow: &end, ComparisonOnly: true}
	overview, err := storedComparisonTestAdapter(db, filter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Comparisons.Models["model-a"].Requests != 8 || len(overview.Comparisons.APIKeys) != 2 {
		t.Fatal("lost rollup dimensions")
	}
	if len(overview.Comparisons.Buckets) != 365 || overview.Comparisons.Granularity != "daily" {
		t.Fatal("custom day series must include empty days")
	}
	if got := overview.Comparisons.Models["model-a"].TokenBuckets; got[start.Format(time.DateOnly)] != 100 || got[end.AddDate(0, 0, -1).Format(time.DateOnly)] != 300 {
		t.Fatalf("daily rollup series lost tokens: %v", got)
	}
	assertOverviewQueryTables(t, *queries, false, true)
	if len(*queries) != 4 {
		t.Fatalf("expected one daily summary and three token series reads, got %d", len(*queries))
	}
	if !strings.Contains((*queries)[0], "api_group_key") {
		t.Fatal("missing comparison grouping")
	}
	if len(overview.Series.Requests) != 0 || overview.Usage.TotalRequests != 0 {
		t.Fatal("comparison-only projection must preserve comparisons without building the main series")
	}
	plain, err := repository.BuildUsageOverviewWithFilterAndRecentCache(db, dto.UsageQueryFilter{Range: "custom", CustomUnit: "day", StartTime: &start, EndTime: &end, EndExclusive: true, QueryNow: &end}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Comparisons != nil {
		t.Fatal("comparison collection must be opt-in for other overview callers")
	}
}

// 比较视图的完整桶与边界都读取落库金额，当前规则不能改写旧事件费用。
// Ported verbatim assertions from usage_overview_comparisons_test.go / TestOverviewComparisonUsesStoredCostInsteadOfCurrentRules
func TestStoredPortOverviewComparisonUsesStoredCostInsteadOfCurrentRules(t *testing.T) {
	db := openTestDatabase(t)
	end := time.Date(2026, 9, 12, 12, 30, 0, 0, time.Local)
	start := end.Add(-4 * time.Hour)
	events := []entities.UsageEvent{
		{EventKey: "priced-left", Timestamp: start, APIGroupKey: "key-a", Model: "model-a", InputTokens: 1000000, TotalTokens: 1000000, CostUSD: overviewCostPtr(2.5), CostAvailable: overviewAvailabilityPtr(true)},
		{EventKey: "priced-middle", Timestamp: start.Add(time.Hour), APIGroupKey: "key-b", Model: "model-a", InputTokens: 1000000, CacheReadTokens: 100000, CacheCreationTokens: 50000, TotalTokens: 1000000, CostUSD: overviewCostPtr(4), CostAvailable: overviewAvailabilityPtr(true)},
	}
	events = priceOverviewFixtureEvents(t, db, events)
	if err := db.Create(&events).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.AggregateUsageOverviewStats(context.Background(), db, end); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertModelPriceSetting(db, dto.ModelPriceSettingInput{Model: "model-a", PromptPricePer1M: 9}); err != nil {
		t.Fatal(err)
	}
	comparisonOnlyFilter := dto.UsageQueryFilter{Range: "4h", StartTime: &start, EndTime: &end, QueryNow: &end, ComparisonOnly: true}
	comparisonOnly, err := storedComparisonTestAdapter(db, comparisonOnlyFilter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !finiteStoredOverviewTestCost(comparisonOnly.Comparisons.APIKeys["key-a"].CostUSD, 2.5) || !finiteStoredOverviewTestCost(comparisonOnly.Comparisons.APIKeys["key-b"].CostUSD, 4) {
		t.Fatalf("comparison reread current price instead of stored event fees: key-a=%+v key-b=%+v", comparisonOnly.Comparisons.APIKeys["key-a"], comparisonOnly.Comparisons.APIKeys["key-b"])
	}
}
