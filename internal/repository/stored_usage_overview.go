package repository

import (
	"fmt"
	"math"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository/dto"
	"cpa-usage-keeper/internal/timeutil"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

// StoredUsageOverviewReader is the explicit read-only persisted-fee overview slice.
// It never invokes a resolver and never writes facts, checkpoints, or caches.
type StoredUsageOverviewReader struct {
	db          *gorm.DB
	recentCache *UsageRecentEventCache
}

func NewStoredUsageOverviewReader(db *gorm.DB) (*StoredUsageOverviewReader, error) {
	if db == nil {
		return nil, fmt.Errorf("stored usage overview database is nil")
	}
	return &StoredUsageOverviewReader{db: db}, nil
}

// NewStoredUsageOverviewReaderWithRecentCache explicitly opts into the existing
// committed-event cache for covered raw edges only. Rollups stay database-backed.
// The default reader and HTTP wiring remain uncached and never read prices.
func NewStoredUsageOverviewReaderWithRecentCache(db *gorm.DB, cache *UsageRecentEventCache) (*StoredUsageOverviewReader, error) {
	reader, err := NewStoredUsageOverviewReader(db)
	if err != nil {
		return nil, err
	}
	reader.recentCache = cache
	return reader, nil
}

// BuildStoredUsageOverviewWithFilterAndRecentCache reads only persisted fees for
// summary/series. A supplied cache serves only proven covered, half-open raw
// edges; unavailable/uncovered caches fall back to bounded database reads.
func BuildStoredUsageOverviewWithFilterAndRecentCache(db *gorm.DB, filter dto.UsageQueryFilter, cache *UsageRecentEventCache) (*dto.StoredUsageOverviewRecord, error) {
	reader, err := NewStoredUsageOverviewReaderWithRecentCache(db, cache)
	if err != nil {
		return nil, err
	}
	return reader.Read(filter)
}

func (r *StoredUsageOverviewReader) Read(filter dto.UsageQueryFilter) (*dto.StoredUsageOverviewRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("stored usage overview reader is nil")
	}
	if filter.Model != "" || filter.AuthIndex != "" || filter.AuthType != "" || filter.Result != "" || filter.ComparisonOnly || filter.RealtimeWindow != "" || filter.RealtimeEndTime != nil {
		return nil, fmt.Errorf("unsupported stored overview filter")
	}
	var result *dto.StoredUsageOverviewRecord
	err := r.db.Clauses(dbresolver.Read).Transaction(func(tx *gorm.DB) error {
		reader := &StoredUsageOverviewReader{db: tx, recentCache: r.recentCache}
		var err error
		result, err = reader.read(filter)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, amount := range result.Series.StoredCostUSD {
		if !finiteStoredFee(amount) {
			return nil, fmt.Errorf("stored overview series sum is non-finite")
		}
	}
	return result, nil
}

func (r *StoredUsageOverviewReader) read(filter dto.UsageQueryFilter) (*dto.StoredUsageOverviewRecord, error) {
	if filter.StartTime == nil || filter.EndTime == nil || filter.StartTime.IsZero() || filter.EndTime.IsZero() {
		return nil, fmt.Errorf("stored usage overview requires start_time and end_time")
	}
	start := timeutil.NormalizeStorageTime(*filter.StartTime)
	end := timeutil.NormalizeStorageTime(*filter.EndTime)
	if !start.Before(end) {
		return nil, fmt.Errorf("stored usage overview requires start_time before end_time")
	}
	if filter.Range != "custom" {
		effective := usageOverviewEffectiveFilter(filter, usageOverviewQueryNow(filter))
		start, end = *effective.StartTime, *effective.EndTime
		if !start.Before(end) {
			return nil, fmt.Errorf("stored overview effective range is empty")
		}
	}
	result := newStoredUsageOverview(start, end)
	for _, source := range storedOverviewSourcePlan(filter, start, end) {
		var err error
		if source.grain == "raw" {
			err = r.readRaw(result, filter, source.start, source.end)
		} else {
			err = r.readRollup(result, filter, source.start, source.end, source.grain)
		}
		if err != nil {
			return nil, err
		}
	}
	finalizeStoredUsageOverview(result)
	if !finiteStoredFee(result.Summary.StoredCostUSD) {
		return nil, fmt.Errorf("stored overview sum is non-finite")
	}
	return result, nil
}

// storedOverviewSourcePlan is shared by persisted summary and comparisons.
// Segments are disjoint half-open windows; only partial hours use hot facts.
type storedOverviewSource struct {
	start, end time.Time
	grain      string
}

func storedOverviewSourcePlan(filter dto.UsageQueryFilter, start, end time.Time) []storedOverviewSource {
	var sources []storedOverviewSource
	add := func(a, b time.Time, grain string) {
		if a.Before(b) {
			sources = append(sources, storedOverviewSource{a, b, grain})
		}
	}
	if strings.EqualFold(strings.TrimSpace(filter.Range), "custom") {
		switch strings.TrimSpace(filter.CustomUnit) {
		case "hour":
			add(start, end, "hourly")
			return sources
		case "day":
			add(start, end, "daily")
			return sources
		}
	}
	byDay := shouldBucketUsageOverviewByDay(filter, computeWindowMinutes(dto.UsageQueryFilter{StartTime: &start, EndTime: &end}))
	fullStart, fullEnd := usageOverviewFullHourWindow(start, end)
	if fullEnd.After(fullStart) {
		dayStart, dayEnd := storedOverviewFullDayWindow(fullStart, fullEnd)
		if !byDay || !dayEnd.After(dayStart) {
			add(fullStart, fullEnd, "hourly")
		} else {
			add(fullStart, dayStart, "hourly")
			add(dayStart, dayEnd, "daily")
			add(dayEnd, fullEnd, "hourly")
		}
	}
	add(start, minTime(fullStart, end), "raw")
	add(maxTime(fullEnd, start), end, "raw")
	return sources
}

type storedOverviewRollupRow struct {
	UnknownCount         int64
	BucketStart          time.Time
	RequestCount         int64
	SuccessCount         int64
	FailureCount         int64
	InputTokens          int64
	CacheReadTokens      int64
	CacheCreationTokens  int64
	ReasoningTokens      int64
	TotalTokens          int64
	CostUSD              *float64
	UnavailableCostCount *int64
}

type storedOverviewRawRow struct {
	Timestamp           time.Time
	Failed              bool
	InputTokens         int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	ReasoningTokens     int64
	TotalTokens         int64
	CostUSD             *float64 `gorm:"column:cost_usd"`
	CostAvailable       *bool    `gorm:"column:cost_available"`
}

type storedOverviewAccumulator struct {
	requests, success, failures                     int64
	input, cacheRead, cacheWrite, reasoning, tokens int64
	cost                                            float64
	available                                       bool
	seriesMinutes                                   int64
}

func newStoredUsageOverview(start, end time.Time) *dto.StoredUsageOverviewRecord {
	return &dto.StoredUsageOverviewRecord{
		RangeStart: start, RangeEnd: end, BoundarySource: "usage_events (hot edges only; archive coverage not asserted)",
		Usage:   &dto.StatisticsSnapshot{},
		Summary: dto.StoredUsageOverviewSummaryRecord{WindowMinutes: computeWindowMinutes(dto.UsageQueryFilter{StartTime: &start, EndTime: &end}), StoredCostAvailable: true},
		Series:  dto.StoredUsageOverviewSeriesRecord{Requests: map[string]int64{}, Tokens: map[string]int64{}, RPM: map[string]float64{}, TPM: map[string]float64{}, StoredCostUSD: map[string]float64{}, StoredCostAvailable: map[string]bool{}, CacheReadRate: map[string]*float64{}, CacheReadRateInputTokens: map[string]int64{}, CacheReadRateReadTokens: map[string]int64{}},
	}
}

func (r *StoredUsageOverviewReader) readRollup(out *dto.StoredUsageOverviewRecord, filter dto.UsageQueryFilter, start, end time.Time, grain string) error {
	if !start.Before(end) {
		return nil
	}
	read := overviewBucketRead(start, end, grain)
	table := "usage_overview_hourly_stats"
	if grain == "daily" {
		table = "usage_overview_daily_stats"
	}
	projection := "bucket_start, SUM(request_count) AS request_count, SUM(success_count) AS success_count, SUM(failure_count) AS failure_count, SUM(input_tokens) AS input_tokens, SUM(cache_read_tokens) AS cache_read_tokens, SUM(cache_creation_tokens) AS cache_creation_tokens, SUM(reasoning_tokens) AS reasoning_tokens, SUM(total_tokens) AS total_tokens, SUM(cost_usd) AS cost_usd, SUM(unavailable_cost_count) AS unavailable_cost_count, SUM(CASE WHEN cost_usd IS NULL OR cost_usd < ? OR cost_usd > ? OR unavailable_cost_count IS NULL OR unavailable_cost_count < 0 THEN 1 ELSE 0 END) AS unknown_count"
	q := read.query(r.db.Table(table).Select(projection, -math.MaxFloat64, math.MaxFloat64)).Group("bucket_start")
	if key := strings.TrimSpace(filter.APIGroupKey); key != "" {
		q = q.Where("api_group_key = ?", key)
	}
	var rows []storedOverviewRollupRow
	if err := q.Order("bucket_start asc").Find(&rows).Error; err != nil {
		return fmt.Errorf("read stored %s overview rollups: %w", grain, err)
	}
	for _, row := range rows {
		if row.UnknownCount > 0 || row.CostUSD == nil || row.UnavailableCostCount == nil {
			return fmt.Errorf("stored %s overview bucket %s has uninitialized persisted fee", grain, timeutil.FormatStorageTime(row.BucketStart))
		}
		if !finiteStoredFee(*row.CostUSD) || *row.UnavailableCostCount < 0 {
			return fmt.Errorf("stored %s overview bucket %s has invalid persisted fee", grain, timeutil.FormatStorageTime(row.BucketStart))
		}
		display := timeutil.NormalizeStorageTime(row.BucketStart)
		bucket := timeutil.FormatStorageTime(display)
		if filter.Range != "custom" && shouldBucketUsageOverviewByDay(filter, out.Summary.WindowMinutes) {
			bucket = display.Format(time.DateOnly)
		}
		if grain == "daily" {
			display = read.displayStart(row.BucketStart)
			bucket = display.Format(time.DateOnly)
		}
		seriesMinutes := usageOverviewSeriesBucketMinutes(bucket)
		if len(bucket) == len(time.DateOnly) {
			if bounds, ok := timeutil.DayBounds(timeutil.DateOf(display), display.Location()); ok {
				seriesMinutes = int64(bounds.End.Sub(bounds.Start) / time.Minute)
			}
		}
		applyStoredOverviewAccumulator(out, storedOverviewAccumulator{requests: row.RequestCount, success: row.SuccessCount, failures: row.FailureCount, input: row.InputTokens, cacheRead: row.CacheReadTokens, cacheWrite: row.CacheCreationTokens, reasoning: row.ReasoningTokens, tokens: row.TotalTokens, cost: *row.CostUSD, available: *row.UnavailableCostCount == 0, seriesMinutes: seriesMinutes}, bucket, seriesMinutes)
	}
	return nil
}

func (r *StoredUsageOverviewReader) readRaw(out *dto.StoredUsageOverviewRecord, filter dto.UsageQueryFilter, start, end time.Time) error {
	if !start.Before(end) {
		return nil
	}
	byDay := shouldBucketUsageOverviewByDay(filter, out.Summary.WindowMinutes)
	if cached, ok := storedUsageEventsFromRecentCache(r.recentCache, start, end, usageOverviewQueryNow(filter), filter.APIGroupKey); ok {
		out.CacheUsed = true
		out.BoundarySource = "recent_event_cache (usage_events hot edges only; archive coverage not asserted)"
		for _, row := range cached {
			if err := r.db.Statement.Context.Err(); err != nil {
				return err
			}
			if err := applyStoredOverviewRawRow(out, storedOverviewRawRow{Timestamp: row.Timestamp, Failed: row.Failed,
				InputTokens: row.InputTokens, CacheReadTokens: row.CacheReadTokens, CacheCreationTokens: row.CacheCreationTokens,
				ReasoningTokens: row.ReasoningTokens, TotalTokens: row.TotalTokens,
				CostUSD: row.StoredCostUSD, CostAvailable: row.StoredCostAvailable}, byDay); err != nil {
				return err
			}
		}
		return nil
	}
	for _, segment := range usageEventTimeReads(start, end, false) {
		q := segment.query(r.db.Model(&entities.UsageEvent{}).Select("timestamp, failed, input_tokens, cache_read_tokens, cache_creation_tokens, reasoning_tokens, total_tokens, cost_usd, cost_available"))
		if key := strings.TrimSpace(filter.APIGroupKey); key != "" {
			q = q.Where("api_group_key = ?", key)
		}
		var rows []storedOverviewRawRow
		if err := q.Order("timestamp asc").Scan(&rows).Error; err != nil {
			return fmt.Errorf("read stored overview boundary events: %w", err)
		}
		for _, row := range rows {
			if !segment.contains(row.Timestamp) {
				continue
			}
			if err := applyStoredOverviewRawRow(out, row, byDay); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyStoredOverviewRawRow(out *dto.StoredUsageOverviewRecord, row storedOverviewRawRow, byDay bool) error {
	if row.CostUSD == nil || row.CostAvailable == nil {
		return fmt.Errorf("stored overview boundary event at %s has uninitialized persisted fee", timeutil.FormatStorageTime(row.Timestamp))
	}
	if !finiteStoredFee(*row.CostUSD) {
		return fmt.Errorf("stored overview boundary event at %s has non-finite persisted fee", timeutil.FormatStorageTime(row.Timestamp))
	}
	bucket, minutes := usageOverviewBucket(row.Timestamp, byDay)
	if byDay {
		at := timeutil.NormalizeStorageTime(row.Timestamp)
		if bounds, ok := timeutil.DayBounds(timeutil.DateOf(at), at.Location()); ok {
			minutes = int64(bounds.End.Sub(bounds.Start) / time.Minute)
		}
	}
	applyStoredOverviewAccumulator(out, storedOverviewAccumulator{requests: 1, success: boolInt(!row.Failed), failures: boolInt(row.Failed), input: row.InputTokens, cacheRead: row.CacheReadTokens, cacheWrite: row.CacheCreationTokens, reasoning: row.ReasoningTokens, tokens: row.TotalTokens, cost: *row.CostUSD, available: *row.CostAvailable, seriesMinutes: minutes}, bucket, minutes)
	return nil
}

func applyStoredOverviewAccumulator(out *dto.StoredUsageOverviewRecord, a storedOverviewAccumulator, bucket string, _ int64) {
	out.Usage.TotalRequests += a.requests
	out.Usage.SuccessCount += a.success
	out.Usage.FailureCount += a.failures
	out.Usage.TotalTokens += a.tokens
	s := &out.Summary
	s.RequestCount += a.requests
	s.TokenCount += a.tokens
	s.InputTokens += a.input
	s.CacheReadTokens += a.cacheRead
	s.CacheCreationTokens += a.cacheWrite
	s.ReasoningTokens += a.reasoning
	s.StoredCostUSD += a.cost
	if !a.available {
		s.StoredCostAvailable = false
	}

	out.Series.Requests[bucket] += a.requests
	out.Series.Tokens[bucket] += a.tokens
	out.Series.StoredCostUSD[bucket] += a.cost

	if _, ok := out.Series.StoredCostAvailable[bucket]; !ok {
		out.Series.StoredCostAvailable[bucket] = true
	}
	out.Series.StoredCostAvailable[bucket] = out.Series.StoredCostAvailable[bucket] && a.available
	out.Series.CacheReadRateInputTokens[bucket] += a.input
	out.Series.CacheReadRateReadTokens[bucket] += a.cacheRead
	out.Series.CacheReadRate[bucket] = nil
	if out.Series.CacheReadRateInputTokens[bucket] > 0 {
		v := float64(out.Series.CacheReadRateReadTokens[bucket]) / float64(out.Series.CacheReadRateInputTokens[bucket]) * 100
		out.Series.CacheReadRate[bucket] = &v
	}
	minutes := a.seriesMinutes
	if minutes < 1 {
		minutes = usageOverviewSeriesBucketMinutes(bucket)
	}
	out.Series.RPM[bucket] = float64(out.Series.Requests[bucket]) / float64(minutes)
	out.Series.TPM[bucket] = float64(out.Series.Tokens[bucket]) / float64(minutes)
}

func finalizeStoredUsageOverview(out *dto.StoredUsageOverviewRecord) {
	out.Summary.RPM = float64(out.Summary.RequestCount) / float64(max64(out.Summary.WindowMinutes, 1))
	out.Summary.TPM = float64(out.Summary.TokenCount) / float64(max64(out.Summary.WindowMinutes, 1))
	if out.Summary.WindowMinutes > usageOverviewDailyAverageDayMinutes {
		days := float64(out.Summary.WindowMinutes) / float64(usageOverviewDailyAverageDayMinutes)
		out.Summary.DailyAverageRequests = usageOverviewFloat64Ptr(float64(out.Summary.RequestCount) / days)
		out.Summary.DailyAverageTokens = usageOverviewFloat64Ptr(float64(out.Summary.TokenCount) / days)
		out.Summary.DailyAverageStoredCostUSD = usageOverviewFloat64Ptr(out.Summary.StoredCostUSD / days)
		out.Summary.DailyAverageRangeDays = &days
	}
}

func finiteStoredFee(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// Preserve the existing stable-offset plan; ambiguous midnight transitions need
// real half-open day bounds. The legacy storage-key mapping stays in bucketRead.
func storedOverviewFullDayWindow(start, end time.Time) (time.Time, time.Time) {
	if usageReadHasStableOffset(start, end) {
		return usageOverviewFullDayWindow(start, end)
	}
	date := timeutil.DateOf(start)
	first, ok := timeutil.DayBounds(date, start.Location())
	for !ok || first.Start.Before(start) {
		date = date.AddDays(1)
		first, ok = timeutil.DayBounds(date, start.Location())
	}
	date = timeutil.DateOf(end)
	last, ok := timeutil.DayBounds(date, end.Location())
	for !ok {
		date = date.AddDays(-1)
		last, ok = timeutil.DayBounds(date, end.Location())
	}
	if last.Start.Before(first.Start) {
		return first.Start, first.Start
	}
	return first.Start, last.Start
}
