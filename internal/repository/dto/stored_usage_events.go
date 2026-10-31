package dto

import "time"

// StoredUsageEventRecord is an explicit persisted-fee result, not a current-price
// UsageEventRecord. The two stored fields come only from cost_usd/cost_available;
// an incomplete or non-finite persisted pair is an error, never a free request.
type StoredUsageEventRecord struct {
	Metadata            StoredUsageEventMetadata
	StoredCostUSD       float64 `json:"stored_cost_usd"`
	StoredCostAvailable bool    `json:"stored_cost_available"`
}

// StoredUsageEventMetadata contains no current cost, resolver, or snapshot fields.
type StoredUsageEventMetadata struct {
	ID                   int64
	Timestamp            time.Time
	APIGroupKey          string
	Model                string
	ModelAlias           string
	ResponseModel        string
	ReasoningEffort      string
	ServiceTier          string
	EffectiveServiceTier string
	ResponseServiceTier  string
	ClientIP             *string
	XForwardedFor        *string
	UserAgent            *string
	ExecutorType         string
	Endpoint             string
	AuthType             string
	// Preserve exact observed identity separately from normalized display values.
	ObservedAuthType    string
	ObservedAuthIndex   string
	RequestID           string
	Provider            string
	Source              string
	AuthIndex           string
	Failed              bool
	StatusCode          *int
	Stream              *bool
	LatencyMS           int64
	TTFTMS              *int64
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
}

// StoredUsageEventsPageRecord has the ordinary Events pagination contract.
// DataSource explicitly identifies hot-table-only coverage: archive-only history
// is not included, even if a matching archive table exists.
type StoredUsageEventsPageRecord struct {
	Events     []StoredUsageEventRecord
	TotalCount int64
	Page       int
	PageSize   int
	TotalPages int
	HasMore    bool
	DataSource string
}
