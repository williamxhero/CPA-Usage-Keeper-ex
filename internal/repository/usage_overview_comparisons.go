package repository

import (
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/helper"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository/dto"
	"cpa-usage-keeper/internal/timeutil"
	"gorm.io/gorm"
	"strings"
	"time"
)

// 时间轴和数据共用项目时区，空桶保留为零；完整天用 AddDate 避免 DST 导致日期漂移。
func newUsageOverviewComparisons(filter dto.UsageQueryFilter, byDay bool) *dto.UsageOverviewComparisonsRecord {
	result := &dto.UsageOverviewComparisonsRecord{
		Channels: map[string]*dto.UsageComparisonItemRecord{},
		Models:   map[string]*dto.UsageComparisonItemRecord{}, APIKeys: map[string]*dto.UsageComparisonItemRecord{},
		AuthFiles: map[string]*dto.UsageComparisonItemRecord{}, AIProviders: map[string]*dto.UsageComparisonItemRecord{},
		Buckets: usageOverviewComparisonBuckets(filter, byDay), Granularity: "hourly",
	}
	if byDay {
		result.Granularity = "daily"
	}
	return result
}

func usageOverviewComparisonBuckets(filter dto.UsageQueryFilter, byDay bool) []string {
	buckets := []string{}
	start, end := timeutil.NormalizeStorageTime(*filter.StartTime), timeutil.NormalizeStorageTime(*filter.EndTime)
	cursor := start.Truncate(time.Hour)
	if byDay {
		for date := timeutil.DateOf(start); ; date = date.AddDays(1) {
			bounds, exists := timeutil.DayBounds(date, start.Location())
			if !exists {
				continue
			}
			if bounds.Start.After(end) || (filter.EndExclusive && bounds.Start.Equal(end)) {
				break
			}
			buckets = append(buckets, bounds.Start.Format(time.DateOnly))
		}
		return buckets
	}
	for cursor.Before(end) || (!filter.EndExclusive && cursor.Equal(end)) {
		bucket, _ := usageOverviewBucket(cursor, byDay)
		buckets = append(buckets, bucket)
		if byDay {
			cursor = cursor.AddDate(0, 0, 1)
		} else {
			cursor = cursor.Add(time.Hour)
		}
	}
	return buckets
}

func applyUsageEventToComparisonOnly(comparisons *dto.UsageOverviewComparisonsRecord, event entities.UsageEvent, resolver pricing.Resolver, identityLookup analysisIdentityLookup) {
	failed := int64(0)
	if event.Failed {
		failed = 1
	}
	result := resolver.Calculate(UsageEventCostSubject(event))
	row := dto.UsageComparisonItemRecord{Requests: 1, Failures: failed, InputTokens: event.InputTokens, OutputTokens: event.OutputTokens, CacheReadTokens: event.CacheReadTokens, CacheCreationTokens: event.CacheCreationTokens, ReasoningTokens: event.ReasoningTokens, TotalTokens: event.TotalTokens, CostUSD: result.Cost.TotalCostUSD, CostAvailable: result.Available, DualCosts: result.DualCosts()}
	row.Bucket, _ = usageOverviewBucket(event.Timestamp, comparisons.Granularity == "daily")
	applyUsageOverviewComparison(comparisons, event.Model, event.APIGroupKey, row)
	if resolver.HasPricingOverrides() || resolver.HasChannels() {
		comparisons.PricingSnapshotID = resolver.SnapshotID()
	}
	applyPricingChannelComparison(comparisons, resolver, UsageEventCostSubject(event), row)
	if resolver.HasPricingOverrides() {
		applyPricingExactIdentityComparison(comparisons, identityLookup, usagePricingIdentity{event.AuthType, event.AuthIndex}, row)
		return
	}
	applyUsageOverviewIdentityComparison(comparisons, identityLookup, event.AuthIndex, row)
}

// 同一比较行的费用只计算一次，再累计到模型和 API Key 两个维度。
func applyUsageOverviewComparison(comparisons *dto.UsageOverviewComparisonsRecord, model, apiKey string, row dto.UsageComparisonItemRecord) {
	addUsageOverviewComparison(comparisons.Models, normalizeUsageOverviewDimension(model), row)
	addUsageOverviewComparison(comparisons.APIKeys, normalizeUsageOverviewDimension(apiKey), row)
}

func applyUsageOverviewIdentityComparison(comparisons *dto.UsageOverviewComparisonsRecord, identityLookup analysisIdentityLookup, authIndex string, row dto.UsageComparisonItemRecord) {
	if identity, ok := identityLookup.find(entities.UsageIdentityAuthTypeAuthFile, strings.TrimSpace(authIndex)); ok {
		row.Label = identity.label
		addUsageOverviewComparison(comparisons.AuthFiles, identity.identity, row)
	}
	if identity, ok := identityLookup.find(entities.UsageIdentityAuthTypeAIProvider, strings.TrimSpace(authIndex)); ok {
		row.Label = identity.label
		addUsageOverviewComparison(comparisons.AIProviders, identity.identity, row)
	}
}

func applyPricingExactIdentityComparison(comparisons *dto.UsageOverviewComparisonsRecord, lookup analysisIdentityLookup, exact usagePricingIdentity, row dto.UsageComparisonItemRecord) {
	identity, found := pricingExactIdentity(lookup, exact)
	if !found {
		return
	} // Missing evidence must not borrow a normalized identity.
	row.Label = identity.label
	items := comparisons.AuthFiles
	if identity.authType == entities.UsageIdentityAuthTypeAIProvider {
		items = comparisons.AIProviders
	}
	addUsageOverviewComparison(items, identity.identity, row)
}

func addUsageOverviewComparison(items map[string]*dto.UsageComparisonItemRecord, key string, row dto.UsageComparisonItemRecord) {
	item := items[key]
	if item == nil {
		item = &dto.UsageComparisonItemRecord{Key: key, Label: row.Label, CostAvailable: true, TokenBuckets: map[string]int64{}}
		items[key] = item
	}
	if item.Label == "" && row.Label != "" {
		item.Label = row.Label
	}
	item.Requests += row.Requests
	item.Failures += row.Failures
	item.InputTokens += row.InputTokens
	item.OutputTokens += row.OutputTokens
	item.CacheReadTokens += row.CacheReadTokens
	item.CacheCreationTokens += row.CacheCreationTokens
	item.ReasoningTokens += row.ReasoningTokens
	item.TotalTokens += row.TotalTokens
	if row.Bucket != "" {
		item.TokenBuckets[row.Bucket] += row.TotalTokens
	}
	item.DualCosts.Merge(row.DualCosts)
	item.CostUSD += row.CostUSD
	item.CostAvailable = item.CostAvailable && row.CostAvailable
}

func calculateUsageOverviewComparisonProjectionCost(costResolver pricing.Resolver, row usageOverviewComparisonProjection, evidence usagePricingEvidenceMap) pricing.CostResult {
	subject := newUsagePricingCostSubject(row.APIGroupKey, row.Model, row.AuthIndex, row.ModelAlias, row.ServiceTier, row.ResponseServiceTier, row.ReasoningEffort, row.Endpoint, row.ExecutorType, row.CostUncachedInputTokens+row.CostCacheReadTokens+row.CostCacheCreationTokens, row.CostOutputTokens, row.CostCacheReadTokens, row.CostCacheCreationTokens)
	return calculateUsageRollupCost(costResolver, subject, time.Time{}, "range", row.RequestCount, row.TotalTokens, helper.UsageTokenCostInput{InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens, CacheCreationTokens: row.CacheCreationTokens}, evidence)
}

// 区间汇总保留定价维度，Token 趋势单独按时间和分类聚合，避免将费用明细按时间展开。
// 比较查询复用范围规划，边界事件由调用方读取一次并补入比较结果。
func loadAndApplyUsageOverviewStats(overview *dto.UsageOverviewRecord, db *gorm.DB, filter dto.UsageQueryFilter, start, end time.Time, grain string, bucketByDay bool, resolver pricing.Resolver) error {
	evidenceGrain := grain
	if filter.ComparisonOnly && !resolver.HasPricingOverrides() && !resolver.HasChannels() {
		evidenceGrain = "range"
	}
	fields := resolver.ActiveFields()
	if filter.ComparisonOnly && (resolver.HasPricingOverrides() || resolver.HasChannels()) {
		fields = resolver.AttributionFields()
	}
	columns := UsagePricingDimensionColumns(fields)
	if filter.ComparisonOnly && !resolver.HasPricingOverrides() && !resolver.HasChannels() {
		columns = append(columns, "api_group_key", "auth_index")
	}
	evidence, err := loadUsagePricingEvidence(db, filter, start, end, evidenceGrain, resolver, columns)
	if err != nil {
		return err
	}
	if filter.ComparisonOnly && (resolver.HasPricingOverrides() || resolver.HasChannels()) {
		return loadAndApplyPricedUsageComparisons(overview, db, filter, start, end, grain, bucketByDay, resolver, evidence)
	}
	if filter.ComparisonOnly {
		rows, err := loadUsageOverviewComparisonProjection(db, filter, start, end, grain, resolver.ActiveFields())
		if err != nil {
			return err
		}
		var identityLookup analysisIdentityLookup
		authIndexes := make([]string, 0)
		seenAuthIndexes := make(map[string]struct{})
		for _, row := range rows {
			if authIndex := strings.TrimSpace(row.AuthIndex); authIndex != "" {
				if _, seen := seenAuthIndexes[authIndex]; !seen {
					authIndexes = append(authIndexes, authIndex)
					seenAuthIndexes[authIndex] = struct{}{}
				}
			}
		}
		identityLookup, err = loadAnalysisIdentityLookup(db, authIndexes)
		if err != nil {
			return err
		}
		for _, row := range rows {
			result := calculateUsageOverviewComparisonProjectionCost(resolver, row, evidence)
			comparison := dto.UsageComparisonItemRecord{Requests: row.RequestCount, Failures: row.FailureCount, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens, CacheCreationTokens: row.CacheCreationTokens, ReasoningTokens: row.ReasoningTokens, TotalTokens: row.TotalTokens, CostUSD: result.Cost.TotalCostUSD, CostAvailable: result.Available, DualCosts: result.DualCosts()}
			applyUsageOverviewComparison(overview.Comparisons, row.Model, row.APIGroupKey, comparison)
			applyUsageOverviewIdentityComparison(overview.Comparisons, identityLookup, row.AuthIndex, comparison)
		}
		return loadUsageOverviewComparisonTokenSeries(db, filter, start, end, grain, bucketByDay, overview.Comparisons, identityLookup)
	}
	var model any = &entities.UsageOverviewHourlyStat{}
	if grain == "daily" {
		model = &entities.UsageOverviewDailyStat{}
	}
	rows, err := loadUsageOverviewStatProjection(db.Model(model), filter, start, end, grain, resolver.ActiveFields())
	if err != nil {
		return err
	}
	read := overviewBucketRead(start, end, grain)
	for _, row := range rows {
		// Price evidence is keyed by the original physical bucket; only display
		// uses the business-date start for legacy midnight-gap daily keys.
		cost := calculateUsageOverviewProjectionCost(resolver, row, grain, evidence)
		row.BucketStart = read.displayStart(row.BucketStart)
		applyUsageOverviewStatToOverviewWithCost(overview, row, bucketByDay, cost)
	}
	return nil
}

func loadAndApplyPricedUsageComparisons(overview *dto.UsageOverviewRecord, db *gorm.DB, filter dto.UsageQueryFilter, start, end time.Time, grain string, bucketByDay bool, resolver pricing.Resolver, evidence usagePricingEvidenceMap) error {
	var model any = &entities.UsageOverviewHourlyStat{}
	if grain == "daily" {
		model = &entities.UsageOverviewDailyStat{}
	}
	rows, err := loadUsageOverviewStatProjection(db.Model(model), filter, start, end, grain, resolver.AttributionFields())
	if err != nil {
		return err
	}
	indexes := make([]string, 0, len(rows))
	for _, row := range rows {
		indexes = append(indexes, row.AuthIndex)
	}
	lookup, err := loadAnalysisIdentityLookup(db, indexes)
	if err != nil {
		return err
	}
	lookup, err = extendPricingIdentityLookup(db, lookup, evidence)
	if err != nil {
		return err
	}
	for _, row := range rows {
		result := calculateUsageOverviewProjectionCost(resolver, row, grain, evidence)
		comparison := dto.UsageComparisonItemRecord{Requests: row.RequestCount, Failures: row.FailureCount, InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens, CacheCreationTokens: row.CacheCreationTokens, ReasoningTokens: row.ReasoningTokens, TotalTokens: row.TotalTokens, CostUSD: result.Cost.TotalCostUSD, CostAvailable: result.Available, DualCosts: result.DualCosts()}
		applyUsageOverviewComparison(overview.Comparisons, row.Model, row.APIGroupKey, comparison)
		subject := newUsagePricingCostSubject(row.APIGroupKey, row.Model, row.AuthIndex, row.ModelAlias, row.ServiceTier, row.ResponseServiceTier, row.ReasoningEffort, row.Endpoint, row.ExecutorType, 0, 0, 0, 0)
		key := usagePricingEvidenceKey{Bucket: pricingEvidenceBucket(row.BucketStart, grain), Dimensions: subject.Dimensions}
		retained := evidence[key]
		covered := retained.covers(row.RequestCount, row.TotalTokens, helper.UsageTokenCostInput{InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, CacheReadTokens: row.CacheReadTokens, CacheCreationTokens: row.CacheCreationTokens})
		applyPricingRollupChannelComparison(overview.Comparisons, resolver, retained, covered, comparison, row.BucketStart, bucketByDay)
		if result.Scope != "" && retained.Selected && covered {
			for exact, typed := range retained.Typed {
				typed.Bucket, _ = usageOverviewBucket(row.BucketStart, bucketByDay)
				applyPricingExactIdentityComparison(overview.Comparisons, lookup, exact, typed)
			}
		} else {
			comparison.Bucket, _ = usageOverviewBucket(row.BucketStart, bucketByDay)
			applyUsageOverviewIdentityComparison(overview.Comparisons, lookup, row.AuthIndex, comparison)
		}
	}
	// Identity token series were accumulated alongside the same proven typed
	// facts; a normalized auth_index series must not overwrite their attribution.
	return loadUsageOverviewComparisonTokenSeries(db, filter, start, end, grain, bucketByDay, overview.Comparisons, nil)
}
