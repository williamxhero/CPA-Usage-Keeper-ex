package service

import (
	"fmt"
	"math"
	"time"

	repodto "cpa-usage-keeper/internal/repository/dto"
	servicedto "cpa-usage-keeper/internal/service/dto"
	"cpa-usage-keeper/internal/timeutil"
)

// mapStoredUsageOverviewSeries preserves sparse instant order and the upstream
// bounded calendar-day chart contract without consulting any current valuation.
// Map fields remain keyed by the explicit ordered Buckets, including merged days.
func mapStoredUsageOverviewSeries(series repodto.StoredUsageOverviewSeriesRecord, filter servicedto.StoredUsageOverviewFilter) (servicedto.StoredUsageOverviewSeries, error) {
	result := servicedto.StoredUsageOverviewSeries{Buckets: []string{}, Requests: series.Requests, Tokens: series.Tokens,
		RPM: series.RPM, TPM: series.TPM, StoredCostUSD: series.StoredCostUSD,
		StoredCostAvailable: series.StoredCostAvailable, CacheReadRate: series.CacheReadRate}
	if len(series.Requests) == 0 {
		return result, nil
	}
	rangeCount := timeutil.DateOf(*filter.StartTime).DaysUntil(timeutil.DateOf(*filter.EndTime))
	labels := usageOverviewBucketLabels(series.Requests, servicedto.UsageFilter{
		Range: filter.Range, CustomUnit: filter.CustomUnit, RangeCount: rangeCount,
		StartTime: filter.StartTime, EndTime: filter.EndTime, EndExclusive: filter.EndExclusive,
	})
	if len(labels) <= usageOverviewSeriesMaxPoints {
		result.Buckets = labels
		for _, label := range labels {
			if _, exists := result.Requests[label]; !exists {
				result.Requests[label] = 0
				result.Tokens[label] = 0
				result.RPM[label] = 0
				result.TPM[label] = 0
				result.StoredCostUSD[label] = 0
				result.StoredCostAvailable[label] = true
				result.CacheReadRate[label] = nil
			}
		}
		return result, nil
	}
	result = servicedto.StoredUsageOverviewSeries{Buckets: make([]string, 0, usageOverviewSeriesMaxPoints),
		Requests: map[string]int64{}, Tokens: map[string]int64{}, RPM: map[string]float64{}, TPM: map[string]float64{},
		StoredCostUSD: map[string]float64{}, StoredCostAvailable: map[string]bool{}, CacheReadRate: map[string]*float64{}}
	for i := 0; i < usageOverviewSeriesMaxPoints; i++ {
		group := labels[i*len(labels)/usageOverviewSeriesMaxPoints : (i+1)*len(labels)/usageOverviewSeriesMaxPoints]
		bucket := group[0]
		var requests, tokens, input, cached int64
		var cost, minutes float64
		available := true
		for _, label := range group {
			requests += series.Requests[label]
			tokens += series.Tokens[label]
			cost += series.StoredCostUSD[label]
			input += series.CacheReadRateInputTokens[label]
			cached += series.CacheReadRateReadTokens[label]
			if known, exists := series.StoredCostAvailable[label]; exists && !known {
				available = false
			}
			if date, err := timeutil.ParseCivilDate(label); err == nil {
				if bounds, exists := timeutil.DayBounds(date, time.Local); exists {
					minutes += bounds.Minutes()
					continue
				}
			}
			minutes += 24 * 60
		}
		if math.IsNaN(cost) || math.IsInf(cost, 0) {
			return servicedto.StoredUsageOverviewSeries{}, fmt.Errorf("stored overview chart sum is non-finite")
		}
		result.Buckets = append(result.Buckets, bucket)
		result.Requests[bucket] = requests
		result.Tokens[bucket] = tokens
		result.RPM[bucket] = float64(requests) / minutes
		result.TPM[bucket] = float64(tokens) / minutes
		result.StoredCostUSD[bucket] = cost
		result.StoredCostAvailable[bucket] = available
		result.CacheReadRate[bucket] = usageOverviewSeriesCacheRate(input, cached)
	}
	return result, nil
}
