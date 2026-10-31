package quota

import (
	"context"
	"cpa-usage-keeper/internal/repository"
	"gorm.io/gorm"
)

// StoredWindowStatsFactory is an explicit persisted-fee boundary. A zero value
// still selects stored facts and can never fall back to a current resolver.
type StoredWindowStatsFactory struct{}

func NewStoredWindowStatsFactory() *StoredWindowStatsFactory { return &StoredWindowStatsFactory{} }
func (*StoredWindowStatsFactory) newProvider(ctx context.Context, db *gorm.DB) (usageWindowStatsProvider, error) {
	return repository.NewStoredUsageWindowStatsCalculator(ctx, db)
}

// NewServiceWithStoredWindowStats opts all ordinary/header refresh entries into
// stored fees, including stored cycle/transition history, with the same lifecycle.
func NewServiceWithStoredWindowStats(db *gorm.DB, registry ProviderRegistry, options ServiceOptions) *Service {
	options.StoredWindowStats = NewStoredWindowStatsFactory()
	return NewServiceWithRegistryAndOptions(db, registry, options)
}

func isStoredWindowStatsProvider(provider usageWindowStatsProvider) bool {
	_, ok := provider.(interface{ StoredWindowCosts() })
	return ok
}

func attachLocalWindowCosts(row *QuotaRow, stats repository.UsageWindowStats, provider usageWindowStatsProvider) {
	if isStoredWindowStatsProvider(provider) {
		row.WindowCostSource = "stored"
		available := stats.CostAvailable
		row.StoredCostAvailable = &available
		// NULL/invalid facts do not become a fake free numeric pair. Explicit
		// unavailable facts retain the historical ordinary-window partial amount.
		if stats.UnavailableReason == "stored_cost_unknown" {
			return
		}
		tokens, cost := stats.Tokens, stats.Cost
		row.WindowUsageTokens = &tokens
		row.WindowUsageCost = &cost
		return
	}
	dual := stats.DualCosts.Normalized()
	row.DualCosts = &dual
	row.PricingSnapshotID = stats.PricingSnapshotID
	if stats.CostAvailable {
		tokens, cost := stats.Tokens, stats.Cost
		row.WindowUsageTokens = &tokens
		row.WindowUsageCost = &cost
	}
}
