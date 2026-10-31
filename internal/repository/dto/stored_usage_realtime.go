package dto

import "time"

// StoredUsageRealtimeRecord is an internal persisted-fee read result, not the
// current-pricing DTO or a public HTTP payload. Directory keys need a separate
// allowlisted, scoped, redacted HTTP adapter before they can leave the service.
// No resolver snapshot or dual-cost fields belong to this monetary contract.
type StoredUsageRealtimeRecord struct {
	Window         string
	BucketSeconds  int64
	WindowStart    time.Time
	WindowEnd      time.Time
	CacheUsed      bool
	Insights       StoredRealtimeInsightsRecord
	TokenVelocity  []StoredRealtimeTokenVelocityPointRecord
	ModelSpeed     RealtimeModelSpeedRecord
	LatencyScatter RealtimeLatencyScatterRecord
	CurrentUsage   StoredRealtimeCurrentUsageRecord `json:"-"`
	RequestLevel   []RealtimeRequestLevelPointRecord
	CacheLevel     []RealtimeCacheLevelPointRecord
}

type StoredRealtimeInsightsRecord struct {
	Summary  StoredRealtimeWindowSummaryRecord
	Outcomes []RealtimeOutcomePointRecord
}

type StoredRealtimeWindowSummaryRecord struct {
	Requests, Failures, TokenRequests, CachedRequests     int64
	TotalTokens, InputTokens, OutputTokens                int64
	ReasoningTokens, CacheReadTokens, CacheCreationTokens int64
	StoredCostUSD                                         float64
	StoredCostAvailable                                   bool
}

type StoredRealtimeTokenVelocityPointRecord struct {
	Bucket              string
	TokensPerMinute     float64
	Tokens              int64
	StoredCostUSD       float64
	StoredCostAvailable bool
}

type StoredRealtimeCurrentUsageRecord struct {
	Models      []StoredRealtimeUsageTopItemRecord
	APIKeys     []StoredRealtimeUsageTopItemRecord
	Credentials []StoredRealtimeUsageTopItemRecord
	AIProviders []StoredRealtimeUsageTopItemRecord
}

type StoredRealtimeUsageTopItemRecord struct {
	Key, Label          string
	Tokens, Requests    int64
	Share               float64
	StoredCostUSD       float64
	StoredCostAvailable bool
}
