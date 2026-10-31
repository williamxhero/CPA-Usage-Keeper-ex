package dto

import "time"

// StoredUsageOverviewFilter intentionally cannot express event/cursor/realtime
// filters, nor a pricing mode. HTTP callers must use the public range parser.
// Internal callers may use custom with an empty CustomUnit for exact hot edges
// plus complete rollups, bounded to 24 hours. Public custom ranges still require
// an explicit hour/day unit and must not acquire this internal compatibility form.
type StoredUsageOverviewFilter struct {
	Range              string
	CustomUnit         string
	StartTime, EndTime *time.Time
	EndExclusive       bool
	QueryNow           *time.Time
	APIKeyID           string
}

// StoredUsageOverview contains only public persisted-valuation fields. Never
// embed StatisticsSnapshot: its key/credential maps are not this API's contract.
type StoredUsageOverview struct {
	Range    StoredUsageOverviewRange    `json:"range"`
	Coverage StoredUsageOverviewCoverage `json:"coverage"`
	Summary  StoredUsageOverviewSummary  `json:"summary"`
	Series   StoredUsageOverviewSeries   `json:"series"`
}
type StoredUsageOverviewRange struct {
	Start        time.Time `json:"start"`
	End          time.Time `json:"end"`
	EndExclusive bool      `json:"end_exclusive"`
	Timezone     string    `json:"timezone"`
}
type StoredUsageOverviewCoverage struct {
	BoundarySource          string   `json:"boundary_source"`
	RollupSources           []string `json:"rollup_sources"`
	ArchiveCoverageAsserted bool     `json:"archive_coverage_asserted"`
	CacheUsed               bool     `json:"cache_used"`
}
type StoredUsageOverviewSummary struct {
	RequestCount              int64    `json:"request_count"`
	TokenCount                int64    `json:"token_count"`
	WindowMinutes             int64    `json:"window_minutes"`
	RPM                       float64  `json:"rpm"`
	TPM                       float64  `json:"tpm"`
	StoredCostUSD             float64  `json:"stored_cost_usd"`
	StoredCostAvailable       bool     `json:"stored_cost_available"`
	InputTokens               int64    `json:"input_tokens"`
	CacheReadTokens           int64    `json:"cache_read_tokens"`
	CacheCreationTokens       int64    `json:"cache_creation_tokens"`
	ReasoningTokens           int64    `json:"reasoning_tokens"`
	DailyAverageRequests      *float64 `json:"daily_average_requests"`
	DailyAverageTokens        *float64 `json:"daily_average_tokens"`
	DailyAverageStoredCostUSD *float64 `json:"daily_average_stored_cost_usd"`
	DailyAverageRangeDays     *float64 `json:"daily_average_range_days"`
}
type StoredUsageOverviewSeries struct {
	Buckets             []string            `json:"buckets"`
	Requests            map[string]int64    `json:"requests"`
	Tokens              map[string]int64    `json:"tokens"`
	RPM                 map[string]float64  `json:"rpm"`
	TPM                 map[string]float64  `json:"tpm"`
	StoredCostUSD       map[string]float64  `json:"stored_cost_usd"`
	StoredCostAvailable map[string]bool     `json:"stored_cost_available"`
	CacheReadRate       map[string]*float64 `json:"cache_read_rate"`
}
