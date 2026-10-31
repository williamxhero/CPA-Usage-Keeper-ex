package test

import (
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
	"gorm.io/gorm"
	"testing"
)

// Only bridges test assertions, never a production entrypoint or pricing mode.
// New reader fields are explicitly mapped so the original numeric assertions
// can execute unchanged; current repository methods are never redirected.
func storedComparisonTestAdapter(db *gorm.DB, f dto.UsageQueryFilter, _ *repository.UsageRecentEventCache) (*dto.UsageOverviewRecord, error) {
	r, err := repository.NewStoredUsageComparisonReader(db)
	if err != nil {
		return nil, err
	}
	stored, err := r.Read(f)
	if err != nil {
		return nil, err
	}
	convert := func(items map[string]*dto.StoredUsageComparisonItemRecord) map[string]*dto.UsageComparisonItemRecord {
		out := map[string]*dto.UsageComparisonItemRecord{}
		for k, v := range items {
			out[k] = &dto.UsageComparisonItemRecord{Key: v.Key, Label: v.Label, Requests: v.Requests, Failures: v.Failures, InputTokens: v.InputTokens, OutputTokens: v.OutputTokens, CacheReadTokens: v.CacheReadTokens, CacheCreationTokens: v.CacheCreationTokens, ReasoningTokens: v.ReasoningTokens, TotalTokens: v.TotalTokens, CostUSD: v.StoredCostUSD, CostAvailable: v.StoredCostAvailable, TokenBuckets: v.TokenBuckets}
		}
		return out
	}
	return &dto.UsageOverviewRecord{Usage: &dto.StatisticsSnapshot{}, Series: dto.UsageOverviewSeriesRecord{Requests: map[string]int64{}}, Comparisons: &dto.UsageOverviewComparisonsRecord{Buckets: stored.Buckets, Granularity: stored.Granularity, Models: convert(stored.Models), APIKeys: convert(stored.APIKeys), Credentials: convert(stored.Credentials), AuthFiles: convert(stored.AuthFiles), AIProviders: convert(stored.AIProviders)}}, nil
}

func TestStoredComparisonAdapterUsesNewDTOOnly(t *testing.T) {
	if _, err := repository.NewStoredUsageComparisonReader(nil); err == nil {
		t.Fatal("nil database accepted")
	}
}
