package dto

// StoredUsageComparisonsFilter cannot express current pricing or event filters.
type StoredUsageComparisonsFilter StoredUsageOverviewFilter

// StoredUsageComparisons is an explicit public allowlist, never a repository map.
type StoredUsageComparisons struct {
	Range       StoredUsageOverviewRange      `json:"range"`
	Coverage    StoredUsageComparisonCoverage `json:"coverage"`
	Buckets     []string                      `json:"buckets"`
	Granularity string                        `json:"granularity"`
	Models      []StoredUsageComparisonItem   `json:"models"`
	APIKeys     []StoredUsageComparisonItem   `json:"api_keys"`
	AuthFiles   []StoredUsageComparisonItem   `json:"auth_files"`
	Credentials []StoredUsageComparisonItem   `json:"credentials"`
	AIProviders []StoredUsageComparisonItem   `json:"ai_providers"`
}
type StoredUsageComparisonCoverage struct {
	StoredUsageOverviewCoverage
	IdentityAttributionComplete bool  `json:"identity_attribution_complete"`
	UnattributedRequests        int64 `json:"unattributed_requests"`
}

// ID is the existing model name, API key ID or credential directory ID. A null
// ID explicitly means no safe public selection exists. Each array entry remains
// distinct even when ID is unavailable or labels duplicate; labels are not keys.
type StoredUsageComparisonItem struct {
	ID                  *string          `json:"id"`
	IDAvailable         bool             `json:"id_available"`
	Label               string           `json:"label"`
	TokenBuckets        map[string]int64 `json:"token_buckets"`
	Requests            int64            `json:"requests"`
	Failures            int64            `json:"failures"`
	InputTokens         int64            `json:"input_tokens"`
	OutputTokens        int64            `json:"output_tokens"`
	CacheReadTokens     int64            `json:"cache_read_tokens"`
	CacheCreationTokens int64            `json:"cache_creation_tokens"`
	ReasoningTokens     int64            `json:"reasoning_tokens"`
	TotalTokens         int64            `json:"total_tokens"`
	StoredCostUSD       float64          `json:"stored_cost_usd"`
	StoredCostAvailable bool             `json:"stored_cost_available"`
}
