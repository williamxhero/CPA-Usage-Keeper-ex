package repository

import (
	"fmt"
	"math"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/helper"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository/dto"
	"cpa-usage-keeper/internal/timeutil"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

// StoredUsageComparisonReader never consults current pricing. The default reader
// does not use caches; callers must explicitly supply a committed-event cache.
// The read resolver transaction pins all source and identity queries together.
type StoredUsageComparisonReader struct {
	db          *gorm.DB
	recentCache *UsageRecentEventCache
}

func NewStoredUsageComparisonReader(db *gorm.DB) (*StoredUsageComparisonReader, error) {
	if db == nil {
		return nil, fmt.Errorf("stored comparison database is nil")
	}
	return &StoredUsageComparisonReader{db: db}, nil
}

// NewStoredUsageComparisonReaderWithRecentCache opts into proven half-open hot
// edge coverage only. It does not change current APIs or the default HTTP reader.
func NewStoredUsageComparisonReaderWithRecentCache(db *gorm.DB, cache *UsageRecentEventCache) (*StoredUsageComparisonReader, error) {
	reader, err := NewStoredUsageComparisonReader(db)
	if err != nil {
		return nil, err
	}
	reader.recentCache = cache
	return reader, nil
}
func (r *StoredUsageComparisonReader) Read(filter dto.UsageQueryFilter) (*dto.StoredUsageComparisonsRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("stored comparison reader is nil")
	}
	if filter.StartTime == nil || filter.EndTime == nil || filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, fmt.Errorf("stored comparison requires bounded start_time and end_time")
	}
	if filter.Model != "" || filter.AuthIndex != "" || filter.AuthType != "" || filter.Result != "" || filter.RealtimeWindow != "" || filter.RealtimeEndTime != nil {
		return nil, fmt.Errorf("unsupported stored comparison filter")
	}
	start, end := timeutil.NormalizeStorageTime(*filter.StartTime), timeutil.NormalizeStorageTime(*filter.EndTime)
	if !start.Before(end) {
		return nil, fmt.Errorf("stored comparison requires start_time before end_time")
	}
	if filter.Range != "custom" {
		f := usageOverviewEffectiveFilter(filter, usageOverviewQueryNow(filter))
		start, end = *f.StartTime, *f.EndTime
		if !start.Before(end) {
			return nil, fmt.Errorf("stored comparison effective range is empty")
		}
	}
	byDay := shouldBucketUsageOverviewByDay(filter, computeWindowMinutes(dto.UsageQueryFilter{StartTime: &start, EndTime: &end})) || filter.Range == "custom" && filter.CustomUnit == "day"
	axis := filter
	if filter.Range == "today" || filter.Range == "yesterday" {
		// The calendar axis displays the following midnight as an empty point,
		// while data remains half-open at the parsed day's right boundary.
		date := timeutil.DateOf(timeutil.NormalizeStorageTime(*filter.StartTime))
		if bounds, ok := timeutil.DayBounds(date, start.Location()); ok {
			axis.StartTime, axis.EndTime = &bounds.Start, &bounds.End
			axis.EndExclusive = false
		}
	}
	// Calendar ranges keep the display endpoint, not a truncated current-day axis.
	if filter.Range != "today" && filter.Range != "yesterday" {
		axis.StartTime, axis.EndTime = &start, &end
	}
	out := &dto.StoredUsageComparisonsRecord{RangeStart: start, RangeEnd: end, BoundarySource: "usage_events (hot edges only; archive coverage not asserted)", Buckets: usageOverviewComparisonBuckets(axis, byDay), Granularity: "hourly", Models: map[string]*dto.StoredUsageComparisonItemRecord{}, APIKeys: map[string]*dto.StoredUsageComparisonItemRecord{}, Credentials: map[string]*dto.StoredUsageComparisonItemRecord{}, AuthFiles: map[string]*dto.StoredUsageComparisonItemRecord{}, AIProviders: map[string]*dto.StoredUsageComparisonItemRecord{}, IdentityAttributionComplete: true}
	if byDay {
		out.Granularity = "daily"
	}
	err := r.db.Clauses(dbresolver.Read).Transaction(func(tx *gorm.DB) error {
		var rows []storedComparisonRow
		for _, source := range storedOverviewSourcePlan(filter, start, end) {
			var batch []storedComparisonRow
			var err error
			if source.grain == "raw" {
				batch, err = r.readRaw(tx, filter, source, byDay, out)
			} else {
				batch, err = readStoredComparisonRollup(tx, filter, source, byDay, out)
			}
			if err != nil {
				return err
			}
			rows = append(rows, batch...)
		}
		indexes := []string{}
		seen := map[string]bool{}
		for _, row := range rows {
			if row.AuthIndex != "" && !seen[row.AuthIndex] {
				indexes = append(indexes, row.AuthIndex)
				seen[row.AuthIndex] = true
			}
		}
		lookup, err := loadStoredComparisonIdentities(tx, indexes)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if !row.SeriesOnly {
				addStoredComparison(out.Models, normalizeUsageOverviewDimension(row.Model), row, "")
				addStoredComparison(out.APIKeys, normalizeUsageOverviewDimension(row.APIGroupKey), row, "")
			}
			identity, found := storedComparisonIdentity(lookup, row.AuthType, row.AuthIndex, row.Raw)
			if !found {
				if !row.SeriesOnly {
					out.IdentityAttributionComplete = false
					out.UnattributedRequests += row.RequestCount
				}
				continue
			}
			target := out.Credentials
			if identity.authType == entities.UsageIdentityAuthTypeAIProvider {
				target = out.AIProviders
			}
			addStoredComparison(target, identity.identity, row, identity.label)
			if identity.authType == entities.UsageIdentityAuthTypeCredential {
				addStoredComparison(out.AuthFiles, identity.identity, row, identity.label)
			}
		}
		for _, items := range []map[string]*dto.StoredUsageComparisonItemRecord{out.Models, out.APIKeys, out.Credentials, out.AuthFiles, out.AIProviders} {
			for _, item := range items {
				if !finiteStoredFee(item.StoredCostUSD) {
					return fmt.Errorf("stored comparison accumulated fee is non-finite")
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Keep only existing directory identity evidence; no pricing/catalog/channel
// tables. Projection is bounded to observed indexes, and uses the fork's shared
// redactor without evaluating or snapshotting any prices.
func loadStoredComparisonIdentities(db *gorm.DB, indexes []string) (analysisIdentityLookup, error) {
	lookup := analysisIdentityLookup{entities.UsageIdentityAuthTypeCredential: {}, entities.UsageIdentityAuthTypeAIProvider: {}}
	for start := 0; start < len(indexes); start += analysisIdentityLookupBatchSize {
		var identities []entities.UsageIdentity
		err := db.Select("identity, auth_type, name, alias, provider, type, prefix, base_url, lookup_key").Where("identity IN ? AND auth_type IN ? AND is_deleted = ?", indexes[start:min(start+analysisIdentityLookupBatchSize, len(indexes))], []entities.UsageIdentityAuthType{entities.UsageIdentityAuthTypeCredential, entities.UsageIdentityAuthTypeAIProvider}, false).Find(&identities).Error
		if err != nil {
			return nil, err
		}
		for _, identity := range identities {
			label := pricing.SafeCredentialText(helper.UsageIdentityDisplayName(identity), identity)
			lookup[identity.AuthType][identity.Identity] = analysisIdentityInfo{identity: identity.Identity, label: label, authType: identity.AuthType}
		}
	}
	return lookup, nil
}

// Raw evidence is exact (including whitespace and case). Rollups have no type:
// only a single existing exact registry class can be used; collisions stay
// unattributed, never duplicated. No historical/channel membership is invented.
func storedComparisonIdentity(lookup analysisIdentityLookup, authType, index string, raw bool) (analysisIdentityInfo, bool) {
	if raw {
		var kind entities.UsageIdentityAuthType
		switch authType {
		case "oauth":
			kind = entities.UsageIdentityAuthTypeCredential
		case "apikey":
			kind = entities.UsageIdentityAuthTypeAIProvider
		default:
			return analysisIdentityInfo{}, false
		}
		v, ok := lookup[kind][index]
		return v, ok
	}
	var found analysisIdentityInfo
	count := 0
	for _, kind := range []entities.UsageIdentityAuthType{entities.UsageIdentityAuthTypeCredential, entities.UsageIdentityAuthTypeAIProvider} {
		if v, ok := lookup[kind][index]; ok {
			found = v
			count++
		}
	}
	return found, count == 1
}

type storedComparisonRow struct {
	Model, APIGroupKey, AuthIndex, AuthType                                                       string
	BucketStart                                                                                   time.Time
	Bucket                                                                                        string `gorm:"-"`
	RequestCount, FailureCount                                                                    int64
	InputTokens, OutputTokens, CacheReadTokens, CacheCreationTokens, ReasoningTokens, TotalTokens int64
	CostUSD                                                                                       *float64
	UnavailableCostCount                                                                          *int64
	UnknownCount                                                                                  int64
	Raw, SeriesOnly                                                                               bool `gorm:"-"`
}

func storedComparisonFee(row storedComparisonRow) error {
	if row.UnknownCount > 0 || row.CostUSD == nil || row.UnavailableCostCount == nil {
		return fmt.Errorf("stored comparison has unbackfilled cost")
	}
	if !finiteStoredFee(*row.CostUSD) || *row.UnavailableCostCount < 0 {
		return fmt.Errorf("stored comparison has invalid persisted fee")
	}
	return nil
}
func readStoredComparisonRollup(db *gorm.DB, f dto.UsageQueryFilter, s storedOverviewSource, byDay bool, out *dto.StoredUsageComparisonsRecord) ([]storedComparisonRow, error) {
	table := "usage_overview_hourly_stats"
	if s.grain == "daily" {
		table = "usage_overview_daily_stats"
	}
	bounds := overviewBucketRead(s.start, s.end, s.grain)
	base := func() *gorm.DB {
		q := bounds.query(db.Table(table))
		if key := strings.TrimSpace(f.APIGroupKey); key != "" {
			q = q.Where("api_group_key = ?", key)
		}
		return q
	}
	projection := "model, api_group_key, auth_index, SUM(request_count) AS request_count, SUM(failure_count) AS failure_count, SUM(input_tokens) AS input_tokens, SUM(output_tokens) AS output_tokens, SUM(cache_read_tokens) AS cache_read_tokens, SUM(cache_creation_tokens) AS cache_creation_tokens, SUM(reasoning_tokens) AS reasoning_tokens, SUM(total_tokens) AS total_tokens, SUM(cost_usd) AS cost_usd, SUM(unavailable_cost_count) AS unavailable_cost_count, SUM(CASE WHEN cost_usd IS NULL OR cost_usd < ? OR cost_usd > ? OR unavailable_cost_count IS NULL OR unavailable_cost_count < 0 THEN 1 ELSE 0 END) AS unknown_count"
	var result []storedComparisonRow
	if err := base().Select(projection, -math.MaxFloat64, math.MaxFloat64).Group("model, api_group_key, auth_index").Find(&result).Error; err != nil {
		return nil, err
	}
	for _, row := range result {
		if err := storedComparisonFee(row); err != nil {
			return nil, err
		}
	}
	for _, dim := range []string{"model", "api_group_key", "auth_index"} {
		var series []storedComparisonRow
		if err := base().Select("bucket_start, " + dim + ", SUM(total_tokens) AS total_tokens").Group("bucket_start, " + dim).Order("bucket_start").Find(&series).Error; err != nil {
			return nil, err
		}
		for _, row := range series {
			row.Bucket, _ = usageOverviewBucket(bounds.displayStart(row.BucketStart), byDay)
			row.SeriesOnly = true
			switch dim {
			case "model":
				addStoredComparison(out.Models, normalizeUsageOverviewDimension(row.Model), row, "")
			case "api_group_key":
				addStoredComparison(out.APIKeys, normalizeUsageOverviewDimension(row.APIGroupKey), row, "")
			case "auth_index":
				result = append(result, row)
			}
		}
	}
	return result, nil
}
func (r *StoredUsageComparisonReader) readRaw(db *gorm.DB, f dto.UsageQueryFilter, s storedOverviewSource, byDay bool, out *dto.StoredUsageComparisonsRecord) ([]storedComparisonRow, error) {
	var result []storedComparisonRow
	if cached, ok := storedUsageEventsFromRecentCache(r.recentCache, s.start, s.end, usageOverviewQueryNow(f), f.APIGroupKey); ok {
		out.CacheUsed = true
		out.BoundarySource = "recent_event_cache (usage_events hot edges only; archive coverage not asserted)"
		for _, event := range cached {
			if err := db.Statement.Context.Err(); err != nil {
				return nil, err
			}
			row, err := storedComparisonRawEvent(recentUsageEventToEntity(event), byDay)
			if err != nil {
				return nil, err
			}
			result = append(result, row)
		}
		return result, nil
	}
	for _, segment := range usageEventTimeReads(s.start, s.end, false) {
		q := segment.query(db.Table("usage_events").Select("timestamp, model, api_group_key, auth_index, auth_type, failed, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, cost_usd, cost_available"))
		if key := strings.TrimSpace(f.APIGroupKey); key != "" {
			q = q.Where("api_group_key = ?", key)
		}
		var rows []entities.UsageEvent
		if err := q.Order("timestamp").Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			if !segment.contains(r.Timestamp) {
				continue
			}
			row, err := storedComparisonRawEvent(r, byDay)
			if err != nil {
				return nil, err
			}
			result = append(result, row)
		}
	}
	return result, nil
}

// Both committed cache and SQLite boundaries use exactly the same fee and raw
// identity validation; a bad cached amount must not trigger a replacement read.
func storedComparisonRawEvent(r entities.UsageEvent, byDay bool) (storedComparisonRow, error) {
	if r.CostUSD == nil || r.CostAvailable == nil {
		return storedComparisonRow{}, fmt.Errorf("stored comparison boundary has unbackfilled cost")
	}
	if !finiteStoredFee(*r.CostUSD) {
		return storedComparisonRow{}, fmt.Errorf("stored comparison boundary has non-finite cost")
	}
	unavailable := int64(0)
	if !*r.CostAvailable {
		unavailable = 1
	}
	bucket, _ := usageOverviewBucket(r.Timestamp, byDay)
	return storedComparisonRow{Model: r.Model, APIGroupKey: r.APIGroupKey, AuthIndex: r.AuthIndex, AuthType: r.AuthType, RequestCount: 1, FailureCount: boolInt(r.Failed), InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CacheReadTokens: r.CacheReadTokens, CacheCreationTokens: r.CacheCreationTokens, ReasoningTokens: r.ReasoningTokens, TotalTokens: r.TotalTokens, CostUSD: r.CostUSD, UnavailableCostCount: &unavailable, Bucket: bucket, Raw: true}, nil
}
func addStoredComparison(items map[string]*dto.StoredUsageComparisonItemRecord, key string, row storedComparisonRow, label string) {
	item := items[key]
	if item == nil {
		item = &dto.StoredUsageComparisonItemRecord{Key: key, Label: label, StoredCostAvailable: true, TokenBuckets: map[string]int64{}}
		items[key] = item
	}
	if item.Label == "" {
		item.Label = label
	}
	if row.Bucket != "" {
		item.TokenBuckets[row.Bucket] += row.TotalTokens
	}
	if row.SeriesOnly {
		return
	}
	item.Requests += row.RequestCount
	item.Failures += row.FailureCount
	item.InputTokens += row.InputTokens
	item.OutputTokens += row.OutputTokens
	item.CacheReadTokens += row.CacheReadTokens
	item.CacheCreationTokens += row.CacheCreationTokens
	item.ReasoningTokens += row.ReasoningTokens
	item.TotalTokens += row.TotalTokens
	item.StoredCostUSD += *row.CostUSD
	item.StoredCostAvailable = item.StoredCostAvailable && *row.UnavailableCostCount == 0
}
