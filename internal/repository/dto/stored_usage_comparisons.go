package dto

import "time"

// StoredUsageComparisonsRecord is a repository-only persisted valuation result.
// Keys are internal identities, not an HTTP serialization allowlist. There are
// deliberately no channels, current prices, dual estimates, or pricing snapshots.
type StoredUsageComparisonsRecord struct {
	RangeStart, RangeEnd                time.Time
	BoundarySource                      string
	CacheUsed                           bool
	Buckets                             []string
	Granularity                         string
	Models, APIKeys                     map[string]*StoredUsageComparisonItemRecord
	AuthFiles, Credentials, AIProviders map[string]*StoredUsageComparisonItemRecord
	IdentityAttributionComplete         bool
	UnattributedRequests                int64
}

type StoredUsageComparisonItemRecord struct {
	Key, Label                                                                                    string
	TokenBuckets                                                                                  map[string]int64
	Requests, Failures                                                                            int64
	InputTokens, OutputTokens, CacheReadTokens, CacheCreationTokens, ReasoningTokens, TotalTokens int64
	StoredCostUSD                                                                                 float64
	StoredCostAvailable                                                                           bool
}
