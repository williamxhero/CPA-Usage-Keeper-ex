package dto

import "time"

// StoredUsageOverviewRecord is a persisted valuation, not a current price snapshot.
// BoundarySource describes the hot-only coverage; archives are not read implicitly.
type StoredUsageOverviewRecord struct {
	RangeStart     time.Time
	RangeEnd       time.Time
	BoundarySource string
	CacheUsed      bool
	Usage          *StatisticsSnapshot
	Summary        StoredUsageOverviewSummaryRecord
	Series         StoredUsageOverviewSeriesRecord
}

type StoredUsageOverviewSummaryRecord struct {
	RequestCount        int64
	TokenCount          int64
	WindowMinutes       int64
	RPM                 float64
	TPM                 float64
	StoredCostUSD       float64 `json:"stored_cost_usd"`
	StoredCostAvailable bool    `json:"stored_cost_available"`

	InputTokens               int64
	CacheReadTokens           int64
	CacheCreationTokens       int64
	ReasoningTokens           int64
	DailyAverageRequests      *float64
	DailyAverageTokens        *float64
	DailyAverageStoredCostUSD *float64 `json:"daily_average_stored_cost_usd"`
	DailyAverageRangeDays     *float64
}

type StoredUsageOverviewSeriesRecord struct {
	Requests            map[string]int64
	Tokens              map[string]int64
	RPM                 map[string]float64
	TPM                 map[string]float64
	StoredCostUSD       map[string]float64 `json:"stored_cost_usd"`
	StoredCostAvailable map[string]bool    `json:"stored_cost_available"`

	CacheReadRateInputTokens map[string]int64 `json:"-"`
	CacheReadRateReadTokens  map[string]int64 `json:"-"`
	CacheReadRate            map[string]*float64
}
