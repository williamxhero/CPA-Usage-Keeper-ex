package repository

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/timeutil"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

// StoredUsageWindowStatsCalculator reads committed fee facts, never a price
// catalog. It neither initializes historical fees nor advances any checkpoint.
// Keep it distinct from UsageWindowStatsCalculator (current-snapshot valuation).
type StoredUsageWindowStatsCalculator struct{ db *gorm.DB }

func NewStoredUsageWindowStatsCalculator(_ context.Context, db *gorm.DB) (*StoredUsageWindowStatsCalculator, error) {
	if db == nil {
		return nil, fmt.Errorf("stored usage window database is nil")
	}
	return &StoredUsageWindowStatsCalculator{db: db}, nil
}

// StrictStoredUsageWindowStatsCalculator is the explicit completeness boundary
// for maintenance/history callers. Ordinary stored windows retain their partial
// amount contract; a known numeric amount with available=false remains valid in
// both contracts. Neither constructor reads prices or repairs historical facts.
type StrictStoredUsageWindowStatsCalculator struct {
	partial *StoredUsageWindowStatsCalculator
}

func NewStrictStoredUsageWindowStatsCalculator(ctx context.Context, db *gorm.DB) (*StrictStoredUsageWindowStatsCalculator, error) {
	partial, err := NewStoredUsageWindowStatsCalculator(ctx, db)
	if err != nil {
		return nil, err
	}
	return &StrictStoredUsageWindowStatsCalculator{partial: partial}, nil
}

func (*StrictStoredUsageWindowStatsCalculator) StoredWindowCosts() {}

func (c *StrictStoredUsageWindowStatsCalculator) rows(ctx context.Context, auth string, start time.Time, end *time.Time) ([]storedWindowRow, error) {
	if c == nil || c.partial == nil {
		return nil, fmt.Errorf("strict stored usage window calculator is nil")
	}
	rows, err := c.partial.rows(ctx, auth, start, end)
	if err != nil {
		return nil, err
	}
	var total float64
	for _, row := range rows {
		if row.UnknownCount > 0 {
			return nil, fmt.Errorf("strict stored usage window contains unbackfilled cost")
		}
		total += row.Cost
		if !finiteStoredFee(row.Cost) || !finiteStoredFee(total) {
			return nil, fmt.Errorf("strict stored usage window contains non-finite cost")
		}
	}
	return rows, nil
}

func (c *StrictStoredUsageWindowStatsCalculator) SumByAuthIndex(ctx context.Context, auth string, start time.Time, end *time.Time) (UsageWindowStats, error) {
	rows, err := c.rows(ctx, auth, start, end)
	if err != nil {
		return UsageWindowStats{}, err
	}
	return storedWindowStatsFromRows(rows), nil
}

func (c *StrictStoredUsageWindowStatsCalculator) SumGroupsByAuthIndex(ctx context.Context, auth string, start time.Time, end *time.Time, grouper UsageWindowStatsGrouper) (UsageWindowGroupedStats, error) {
	if grouper == nil {
		return UsageWindowGroupedStats{}, fmt.Errorf("stored usage window grouper is required")
	}
	rows, err := c.rows(ctx, auth, start, end)
	if err != nil {
		return UsageWindowGroupedStats{}, err
	}
	return storedWindowGroupsFromRows(rows, grouper), nil
}

// StoredWindowCosts marks the legacy ordinary-window partial-amount contract.
func (*StoredUsageWindowStatsCalculator) StoredWindowCosts() {}

type storedWindowRow struct {
	Model            string
	TotalTokens      int64
	Cost             float64
	UnknownCount     int64
	UnavailableCount int64
}

func (c *StoredUsageWindowStatsCalculator) rows(ctx context.Context, auth string, start time.Time, end *time.Time) ([]storedWindowRow, error) {
	if c == nil || c.db == nil {
		return nil, fmt.Errorf("stored usage window calculator is nil")
	}
	auth = strings.TrimSpace(auth)
	if auth == "" {
		return nil, fmt.Errorf("auth_index is required")
	}
	if start.IsZero() || (end != nil && end.IsZero()) {
		// Preserve the upstream empty-window contract; do not query from a zero boundary.
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	start = timeutil.NormalizeStorageTime(start)
	var stop *time.Time
	if end != nil {
		v := timeutil.NormalizeStorageTime(*end)
		stop = &v
		if !start.Before(v) {
			return nil, nil
		}
	}
	var rows []storedWindowRow
	// One reader transaction pins the disjoint raw/hourly sources together.
	db := c.db.Clauses(dbresolver.Read).Session(&gorm.Session{Context: ctx})
	err := db.Transaction(func(tx *gorm.DB) error {
		raw := func(from time.Time, to *time.Time) error {
			finite := "cost_usd IS NOT NULL AND cost_usd >= ? AND cost_usd <= ?"
			q := tx.Model(&entities.UsageEvent{}).Select("model, COALESCE(SUM(total_tokens),0) AS total_tokens, COALESCE(SUM(CASE WHEN "+finite+" AND cost_available IS NOT NULL THEN cost_usd ELSE 0 END),0) AS cost, SUM(CASE WHEN "+finite+" AND cost_available IS NOT NULL THEN 0 ELSE 1 END) AS unknown_count, SUM(CASE WHEN cost_available = 1 THEN 0 ELSE 1 END) AS unavailable_count", -math.MaxFloat64, math.MaxFloat64, -math.MaxFloat64, math.MaxFloat64).Where("auth_index = ? AND timestamp >= ?", auth, timeutil.FormatStorageTime(from)).Group("model")
			if to != nil {
				q = q.Where("timestamp < ?", timeutil.FormatStorageTime(*to))
			}
			var batch []storedWindowRow
			if err := q.Scan(&batch).Error; err != nil {
				return fmt.Errorf("read stored raw window: %w", err)
			}
			rows = append(rows, batch...)
			return nil
		}
		if stop == nil || stop.Sub(start) <= quotaWindowRawOnlyThreshold {
			return raw(start, stop)
		}
		leftEnd := ceilUsageWindowHour(start)
		if stop.Before(leftEnd) {
			leftEnd = *stop
		}
		rightStart := stop.Truncate(time.Hour).Add(-time.Hour)
		if rightStart.Before(start) {
			rightStart = start
		}
		if rightStart.Before(leftEnd) {
			rightStart = leftEnd
		}
		if start.Before(leftEnd) {
			if err := raw(start, &leftEnd); err != nil {
				return err
			}
		}
		if leftEnd.Before(rightStart) {
			finite := "cost_usd IS NOT NULL AND cost_usd >= ? AND cost_usd <= ? AND unavailable_cost_count IS NOT NULL AND unavailable_cost_count >= 0"
			var batch []storedWindowRow
			err := tx.Model(&entities.UsageOverviewHourlyStat{}).Select("model, COALESCE(SUM(total_tokens),0) AS total_tokens, COALESCE(SUM(CASE WHEN "+finite+" THEN cost_usd ELSE 0 END),0) AS cost, SUM(CASE WHEN "+finite+" THEN 0 ELSE 1 END) AS unknown_count, COALESCE(SUM(unavailable_cost_count),0) AS unavailable_count", -math.MaxFloat64, math.MaxFloat64, -math.MaxFloat64, math.MaxFloat64).Where("auth_index = ? AND bucket_start >= ? AND bucket_start < ?", auth, timeutil.FormatStorageTime(leftEnd), timeutil.FormatStorageTime(rightStart)).Group("model").Scan(&batch).Error
			if err != nil {
				return fmt.Errorf("read stored hourly window: %w", err)
			}
			rows = append(rows, batch...)
		}
		if rightStart.Before(*stop) {
			return raw(rightStart, stop)
		}
		return nil
	})
	return rows, err
}

func addStoredWindowRow(stats UsageWindowStats, row storedWindowRow) UsageWindowStats {
	stats.Tokens += row.TotalTokens
	stats.Cost += row.Cost
	if row.UnknownCount > 0 || math.IsNaN(stats.Cost) || math.IsInf(stats.Cost, 0) {
		stats.CostAvailable = false
		stats.UnavailableReason = "stored_cost_unknown"
		if math.IsNaN(stats.Cost) || math.IsInf(stats.Cost, 0) {
			stats.Cost = 0
		}
	}
	if row.UnavailableCount > 0 {
		stats.CostAvailable = false
		if stats.UnavailableReason == "" {
			stats.UnavailableReason = "stored_cost_unavailable"
		}
	}
	return stats
}

func (c *StoredUsageWindowStatsCalculator) SumByAuthIndex(ctx context.Context, auth string, start time.Time, end *time.Time) (UsageWindowStats, error) {
	rows, err := c.rows(ctx, auth, start, end)
	if err != nil {
		return UsageWindowStats{}, err
	}
	return storedWindowStatsFromRows(rows), nil
}

func storedWindowStatsFromRows(rows []storedWindowRow) UsageWindowStats {
	stats := UsageWindowStats{CostAvailable: true}
	for _, row := range rows {
		stats = addStoredWindowRow(stats, row)
	}
	return stats
}
func (c *StoredUsageWindowStatsCalculator) SumGroupsByAuthIndex(ctx context.Context, auth string, start time.Time, end *time.Time, grouper UsageWindowStatsGrouper) (UsageWindowGroupedStats, error) {
	if grouper == nil {
		return UsageWindowGroupedStats{}, fmt.Errorf("stored usage window grouper is required")
	}
	rows, err := c.rows(ctx, auth, start, end)
	if err != nil {
		return UsageWindowGroupedStats{}, err
	}
	return storedWindowGroupsFromRows(rows, grouper), nil
}

func storedWindowGroupsFromRows(rows []storedWindowRow, grouper UsageWindowStatsGrouper) UsageWindowGroupedStats {
	result := UsageWindowGroupedStats{Groups: map[string]UsageWindowStats{}, Complete: true}
	for _, row := range rows {
		key, ok := grouper(row.Model)
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			if row.TotalTokens > 0 || row.Cost != 0 || row.UnknownCount > 0 || row.UnavailableCount > 0 {
				result.Complete = false
			}
			continue
		}
		stats, exists := result.Groups[key]
		if !exists {
			stats.CostAvailable = true
		}
		result.Groups[key] = addStoredWindowRow(stats, row)
	}
	return result
}
