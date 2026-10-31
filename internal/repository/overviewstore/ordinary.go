package overviewstore

import (
	"fmt"
	"math"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/timeutil"
	"gorm.io/gorm"
)

// ApplyOrdinaryRows writes facts independently of stored valuation readiness.
// NULL is absorbing across pages as well as within BuildOrdinaryRows buckets.
// The caller owns the same atomic hourly/daily/facts-checkpoint transaction.
func ApplyOrdinaryRows(tx *gorm.DB, hourly []entities.UsageOverviewHourlyStat, daily []entities.UsageOverviewDailyStat, now time.Time) error {
	for _, row := range hourly {
		updates := tokenStatUpdates(row.RequestCount, row.SuccessCount, row.FailureCount, row.InputTokens, row.OutputTokens, row.ReasoningTokens, row.CachedTokens, row.CacheReadTokens, row.CacheCreationTokens, row.TotalTokens, 0, 0, now)
		// Fee expressions are replaced below; no initialized fee is invented or written.
		addSpeedStatUpdates(updates, row.SpeedTPSSum, row.SpeedSampleCount, row.DecodeSpeedTPSSum, row.DecodeSpeedSampleCount)
		row.CreatedAt, row.UpdatedAt = timeutil.NormalizeStorageTime(now), timeutil.NormalizeStorageTime(now)
		if err := applyOrdinaryRow(tx, &entities.UsageOverviewHourlyStat{}, &row, hourlyDimensionArgs(row), updates, row.CostUSD, row.UnavailableCostCount); err != nil {
			return err
		}
	}
	for _, row := range daily {
		updates := tokenStatUpdates(row.RequestCount, row.SuccessCount, row.FailureCount, row.InputTokens, row.OutputTokens, row.ReasoningTokens, row.CachedTokens, row.CacheReadTokens, row.CacheCreationTokens, row.TotalTokens, 0, 0, now)
		// Fee expressions are replaced below; no initialized fee is invented or written.
		addSpeedStatUpdates(updates, row.SpeedTPSSum, row.SpeedSampleCount, row.DecodeSpeedTPSSum, row.DecodeSpeedSampleCount)
		row.CreatedAt, row.UpdatedAt = timeutil.NormalizeStorageTime(now), timeutil.NormalizeStorageTime(now)
		if err := applyOrdinaryRow(tx, &entities.UsageOverviewDailyStat{}, &row, dailyDimensionArgs(row), updates, row.CostUSD, row.UnavailableCostCount); err != nil {
			return err
		}
	}
	return nil
}

func applyOrdinaryRow(tx *gorm.DB, model, row any, dimensions []any, updates map[string]any, cost *float64, unavailable *int64) error {
	query := tx.Model(model).Where(overviewDimensionsPredicate, dimensions...)
	if cost == nil || unavailable == nil {
		updates["cost_usd"], updates["unavailable_cost_count"] = nil, nil
	} else {
		if err := validateOverviewFeeDelta(cost, unavailable); err != nil {
			return err
		}
		// A partial existing bucket must never become a seemingly complete known sum.
		updates["cost_usd"] = gorm.Expr("CASE WHEN cost_usd IS NULL OR unavailable_cost_count IS NULL THEN NULL ELSE cost_usd + ? END", *cost)
		updates["unavailable_cost_count"] = gorm.Expr("CASE WHEN cost_usd IS NULL OR unavailable_cost_count IS NULL THEN NULL ELSE unavailable_cost_count + ? END", *unavailable)
		query = query.Where("(cost_usd IS NULL OR unavailable_cost_count IS NULL OR cost_usd + ? BETWEEN ? AND ?)", *cost, -math.MaxFloat64, math.MaxFloat64)
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("update ordinary overview: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		return nil
	}
	if err := tx.Create(row).Error; err != nil {
		retry := query.Updates(updates)
		if retry.Error != nil {
			return fmt.Errorf("retry ordinary overview: %w", retry.Error)
		}
		if retry.RowsAffected == 0 {
			return fmt.Errorf("insert ordinary overview: %w", err)
		}
	}
	return nil
}
