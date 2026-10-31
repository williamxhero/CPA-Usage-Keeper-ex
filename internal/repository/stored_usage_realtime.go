package repository

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository/dto"
	"cpa-usage-keeper/internal/timeutil"
	"gorm.io/gorm"
)

// BuildStoredUsageRealtimeWithFilterAndRecentCache explicitly reads persisted
// fees. It is not a mode of the current builder, and accepts no price resolver.
// The temporal/token/latency helpers are shared; valuation and monetary DTOs are
// separate. The original current-pricing builder remains unchanged.
func BuildStoredUsageRealtimeWithFilterAndRecentCache(db *gorm.DB, filter dto.UsageQueryFilter, recentCache *UsageRecentEventCache) (dto.StoredUsageRealtimeRecord, error) {
	if db == nil {
		return dto.StoredUsageRealtimeRecord{}, fmt.Errorf("stored realtime database is nil")
	}
	ctx := db.Statement.Context
	if err := ctx.Err(); err != nil {
		return dto.StoredUsageRealtimeRecord{}, err
	}
	// window/span 由 15m/30m/60m 统一映射，前端所有 realtime 图共享同一窗口。
	window, span := usageOverviewRealtimeWindow(filter.RealtimeWindow)
	// 滑动聚合需要窗口左侧的少量预热 bucket，避免切换窗口时曲线从左边界重新爬坡。
	aggregationWindow := usageOverviewRealtimeAggregationWindow(window)
	aggregationBucketCount := usageOverviewRealtimeAggregationBucketCount(span, aggregationWindow)
	warmupBucketCount := usageOverviewRealtimeWarmupBucketCount(aggregationBucketCount)
	// 默认实时结束时间是当前项目时区时间，测试可以用 RealtimeEndTime 固定。
	end := timeutil.NormalizeStorageTime(time.Now())
	if filter.RealtimeEndTime != nil {
		end = timeutil.NormalizeStorageTime(*filter.RealtimeEndTime)
	}
	// realtime 只看最近窗口，不受 Overview 顶部 range 影响。
	start := end.Add(-window)
	// readStart 只用于图表平滑预热；右侧 current usage 仍从 start 开始统计。
	readStart := start.Add(-time.Duration(warmupBucketCount) * span)
	// 所有 realtime 图表共用一次缓存读取，缓存对象不存在时再回退到 DB 窄窗查询。
	events, cacheOK := loadUsageOverviewRealtimeEventsFromRecentCache(recentCache, filter, readStart, end)
	if !cacheOK {
		// 缓存对象不可用时，直接回退到 usage_events 的同窗口投影，不影响正常缓存命中语义。
		dbEvents, err := loadStoredRealtimeEventsFromDB(db, filter, readStart, end)
		if err != nil {
			return dto.StoredUsageRealtimeRecord{}, err
		}
		events = dbEvents
	}

	// A missing or non-finite persisted pair is not a known zero. Validate
	// every scoped row, including warmup/request-only rows, before aggregation.
	for _, row := range events {
		if err := ctx.Err(); err != nil {
			return dto.StoredUsageRealtimeRecord{}, err
		}
		e := row.event
		if e.CostUSD == nil || e.CostAvailable == nil || !finiteStoredFee(*e.CostUSD) {
			return dto.StoredUsageRealtimeRecord{}, fmt.Errorf("stored realtime fee is incomplete or non-finite")
		}
	}

	// 无论数据来源是缓存还是 DB，都先创建完整 bucket 骨架，前端渲染结构保持一致。
	buckets := newUsageOverviewRealtimeBuckets(readStart, span, usageOverviewRealtimeBucketCount+warmupBucketCount)
	// 只有 current usage 的 Auth File / AI Provider 展示名需要身份表补全，隐藏预热事件不参与当前占比。
	authIndexes := collectRealtimeAuthIndexes(events, start)
	identityLookup, err := loadAnalysisIdentityLookup(db, authIndexes)
	if err != nil {
		return dto.StoredUsageRealtimeRecord{}, err
	}
	// 四个当前用量维度共用 accumulator，按 token 占比输出 Top5+Other。
	modelUsage := map[string]*usageOverviewRealtimeTopAccumulator{}
	apiKeyUsage := map[string]*usageOverviewRealtimeTopAccumulator{}
	authFileUsage := map[string]*usageOverviewRealtimeTopAccumulator{}
	aiProviderUsage := map[string]*usageOverviewRealtimeTopAccumulator{}
	modelSpeedAccumulator := newRealtimeModelSpeed(start, end, cacheOK)

	for _, realtimeEvent := range events {
		if err := ctx.Err(); err != nil {
			return dto.StoredUsageRealtimeRecord{}, err
		}
		// 缓存事件已经是最小投影，这里转回 UsageEvent 复用现有 cost/token helper。
		event := realtimeEvent.event
		// bucket index 基于 realtime 窗口 start 和固定 span 计算。
		timestamp := timeutil.NormalizeStorageTime(event.Timestamp)
		index := usageOverviewRealtimeBucketIndex(timestamp, readStart, span, len(buckets))
		if index < 0 {
			continue
		}
		// visibleEvent 控制当前占比统计范围，避免预热事件进入当前窗口语义。
		visibleEvent := !timestamp.Before(start)
		if visibleEvent && cacheOK {
			// 速度与其它 realtime 图表复用同一份缓存事件遍历；速度资格独立于失败、Generate 和总 Token guards。
			modelSpeedAccumulator.add(event, timestamp)
		}
		// 请求水平包含成功和失败请求。
		bucket := &buckets[index]
		bucket.requests++
		if event.Failed {
			bucket.failures++
		}
		if visibleEvent {
			// current usage 的请求数同样包含成功和失败，token 后续只由成功请求累计。
			applyUsageOverviewRealtimeRequest(realtimeEvent, modelUsage, apiKeyUsage, authFileUsage, aiProviderUsage, identityLookup)
		}
		if !event.Failed && usageEventGenerateEnabled(event.Generate) && event.TTFTMS != nil && *event.TTFTMS > 0 && event.LatencyMS > 0 {
			// 散点保留同一有效请求的 TTFT 和总耗时，预热事件不计入可见窗口。
			if visibleEvent {
				bucket.latencyPairs = append(bucket.latencyPairs, usageOverviewRealtimeLatencyPair{timestamp: timestamp, ttftMS: *event.TTFTMS, latencyMS: event.LatencyMS})
			}
		}
		// 失败或无 token 的请求不参与 token velocity/cache/current token share。
		if event.Failed || event.TotalTokens <= 0 {
			continue
		}

		// Persisted fees never consult current prices or create dual costs.
		cost := *event.CostUSD
		// token velocity/cache level 都从同一个 bucket accumulator 派生。
		bucket.tokenRequests++
		if event.CacheReadTokens > 0 {
			bucket.cachedRequests++
		}
		bucket.outputTokens += event.OutputTokens
		bucket.reasoningTokens += event.ReasoningTokens
		bucket.tokens += event.TotalTokens
		bucket.inputTokens += event.InputTokens
		bucket.cacheReadTokens += event.CacheReadTokens
		bucket.cacheCreationTokens += event.CacheCreationTokens
		bucket.costUSD += cost
		if !*event.CostAvailable {
			bucket.costAvailable = false
		}
		if visibleEvent {
			// current usage 的 token 占比只统计有 token 的成功请求。
			applyUsageOverviewRealtimeTokenUsage(realtimeEvent, cost, *event.CostAvailable, modelUsage, apiKeyUsage, authFileUsage, aiProviderUsage, identityLookup)
		}
	}

	// Keep persisted monetary results separate from current-pricing DTOs.
	modelSpeed := modelSpeedAccumulator.finalize(modelUsage)
	result, err := finalizeStoredUsageRealtime(window, span, start, end, buckets, warmupBucketCount, modelSpeed, modelUsage, apiKeyUsage, authFileUsage, aiProviderUsage, cacheOK)
	if err != nil {
		return dto.StoredUsageRealtimeRecord{}, err
	}
	if err := ctx.Err(); err != nil {
		return dto.StoredUsageRealtimeRecord{}, err
	}
	return result, nil
}

func loadStoredRealtimeEventsFromDB(db *gorm.DB, filter dto.UsageQueryFilter, start, end time.Time) ([]usageOverviewRealtimeEvent, error) {
	events := []usageOverviewRealtimeEvent{}
	for _, read := range usageEventTimeReads(start, end, false) {
		query := read.query(db.Model(&entities.UsageEvent{}).Select(usageOverviewRealtimeEventProjectionColumns))
		if key := strings.TrimSpace(filter.APIGroupKey); key != "" {
			query = query.Where("api_group_key = ?", key)
		}
		var rows []usageEventProjection
		if err := query.Order("timestamp asc").Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("read stored realtime events: %w", err)
		}
		for _, row := range rows {
			if !read.contains(row.Timestamp) {
				continue
			}
			event := usageEventProjectionToEntity(row)
			kind, label := usageRecentIdentityFallback(event.AuthType, event.Source, event.Provider)
			events = append(events, usageOverviewRealtimeEvent{event: event, identityFallbackKind: kind, identityFallbackLabel: label})
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].event.Timestamp.Before(events[j].event.Timestamp) })
	return events, nil
}

func finalizeStoredUsageRealtime(window, span time.Duration, start, end time.Time, buckets []usageOverviewRealtimeBucket, visible int, speed dto.RealtimeModelSpeedRecord, models, keys, credentials, providers map[string]*usageOverviewRealtimeTopAccumulator, cacheUsed bool) (dto.StoredUsageRealtimeRecord, error) {
	result := dto.StoredUsageRealtimeRecord{
		Window: usageOverviewRealtimeWindowLabel(window), BucketSeconds: int64(span / time.Second), WindowStart: start, WindowEnd: end, CacheUsed: cacheUsed,
		ModelSpeed: speed, LatencyScatter: buildUsageOverviewRealtimeLatencyScatter(buckets[visible:]),
		Insights: dto.StoredRealtimeInsightsRecord{
			Summary:  dto.StoredRealtimeWindowSummaryRecord{StoredCostAvailable: true},
			Outcomes: make([]dto.RealtimeOutcomePointRecord, 0, len(buckets)-visible),
		},
		TokenVelocity: make([]dto.StoredRealtimeTokenVelocityPointRecord, 0, len(buckets)-visible),
		RequestLevel:  make([]dto.RealtimeRequestLevelPointRecord, 0, len(buckets)-visible),
		CacheLevel:    make([]dto.RealtimeCacheLevelPointRecord, 0, len(buckets)-visible),
	}
	// Window totals are non-overlapping and exclude smoothing warmup. Rolling
	// points below deliberately use the shared temporal window helper instead.
	for _, b := range buckets[visible:] {
		s := &result.Insights.Summary
		s.Requests += b.requests
		s.Failures += b.failures
		s.TokenRequests += b.tokenRequests
		s.CachedRequests += b.cachedRequests
		s.TotalTokens += b.tokens
		s.InputTokens += b.inputTokens
		s.OutputTokens += b.outputTokens
		s.ReasoningTokens += b.reasoningTokens
		s.CacheReadTokens += b.cacheReadTokens
		s.CacheCreationTokens += b.cacheCreationTokens
		s.StoredCostUSD += b.costUSD
		s.StoredCostAvailable = s.StoredCostAvailable && b.costAvailable
		if !finiteStoredFee(s.StoredCostUSD) {
			return dto.StoredUsageRealtimeRecord{}, fmt.Errorf("stored realtime fee accumulation is non-finite")
		}
		result.Insights.Outcomes = append(result.Insights.Outcomes, dto.RealtimeOutcomePointRecord{Bucket: timeutil.FormatStorageTime(b.bucketStart), Requests: b.requests, Failures: b.failures})
	}
	count := usageOverviewRealtimeAggregationBucketCount(span, usageOverviewRealtimeAggregationWindow(window))
	for index := visible; index < len(buckets); index++ {
		roll := aggregateUsageOverviewRealtimeBucket(buckets, index, count)
		if !finiteStoredFee(roll.costUSD) {
			return dto.StoredUsageRealtimeRecord{}, fmt.Errorf("stored realtime rolling fee is non-finite")
		}
		result.TokenVelocity = append(result.TokenVelocity, dto.StoredRealtimeTokenVelocityPointRecord{
			Bucket: timeutil.FormatStorageTime(roll.bucketStart), Tokens: roll.tokens,
			TokensPerMinute: float64(roll.tokens) / usageOverviewRealtimeAggregationWindow(window).Minutes(),
			StoredCostUSD:   roll.costUSD, StoredCostAvailable: roll.costAvailable,
		})
		result.RequestLevel = append(result.RequestLevel, dto.RealtimeRequestLevelPointRecord{
			Bucket: timeutil.FormatStorageTime(roll.bucketStart), Requests: roll.requests,
			RequestsPerMinute: float64(roll.requests) / usageOverviewRealtimeAggregationWindow(window).Minutes(),
		})
		result.CacheLevel = append(result.CacheLevel, dto.RealtimeCacheLevelPointRecord{
			Bucket: timeutil.FormatStorageTime(roll.bucketStart), CacheReadRate: usageOverviewRealtimeCacheReadRate(roll.cacheReadTokens, roll.inputTokens),
			CacheReadTokens: roll.cacheReadTokens, CacheCreationTokens: roll.cacheCreationTokens, InputTokens: roll.inputTokens,
		})
	}
	dimensions := []struct {
		source map[string]*usageOverviewRealtimeTopAccumulator
		target *[]dto.StoredRealtimeUsageTopItemRecord
	}{
		{models, &result.CurrentUsage.Models}, {keys, &result.CurrentUsage.APIKeys},
		{credentials, &result.CurrentUsage.Credentials}, {providers, &result.CurrentUsage.AIProviders},
	}
	for _, d := range dimensions {
		items, err := finalizeStoredRealtimeTopItems(d.source)
		if err != nil {
			return dto.StoredUsageRealtimeRecord{}, err
		}
		*d.target = items
	}
	return result, nil
}
func finalizeStoredRealtimeTopItems(totals map[string]*usageOverviewRealtimeTopAccumulator) ([]dto.StoredRealtimeUsageTopItemRecord, error) {
	items := make([]*usageOverviewRealtimeTopAccumulator, 0, len(totals))
	totalTokens := int64(0)
	for _, item := range totals {
		if item.tokens <= 0 {
			continue
		}
		items = append(items, item)
		totalTokens += item.tokens
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].tokens == items[j].tokens {
			return items[i].key < items[j].key
		}
		return items[i].tokens > items[j].tokens
	})
	if len(items) > 5 {
		// 第六项保留余项的原始统计口径，费用有任一未知时仍保持未知。
		other := &usageOverviewRealtimeTopAccumulator{key: dto.RealtimeUsageOtherKey, label: "Other", costAvailable: true}
		for _, item := range items[5:] {
			other.tokens += item.tokens
			other.requests += item.requests
			other.costUSD += item.costUSD
			if !item.costAvailable {
				other.costAvailable = false
			}
		}
		items = append(items[:5], other)
	}
	result := make([]dto.StoredRealtimeUsageTopItemRecord, 0, len(items))
	for _, item := range items {
		share := 0.0
		if totalTokens > 0 {
			share = (float64(item.tokens) / float64(totalTokens)) * 100
		}
		if !finiteStoredFee(item.costUSD) {
			return nil, fmt.Errorf("stored realtime top fee is non-finite")
		}
		result = append(result, dto.StoredRealtimeUsageTopItemRecord{
			Key:                 item.key,
			Label:               item.label,
			Tokens:              item.tokens,
			Requests:            item.requests,
			StoredCostUSD:       item.costUSD,
			StoredCostAvailable: item.costAvailable,
			Share:               share,
		})
	}
	return result, nil
}
