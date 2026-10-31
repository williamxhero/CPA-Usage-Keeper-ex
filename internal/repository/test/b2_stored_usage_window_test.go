package test

import (
	"context"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"fmt"
	"gorm.io/gorm"
	"strings"
	"testing"
	"time"
)

func TestB2StoredLongWindowUsesDisjointSourcesAndHalfOpenBounds(t *testing.T) {
	db := openTestDatabase(t)
	start := time.Date(2026, 7, 20, 10, 30, 0, 0, time.UTC)
	end := start.Add(8 * time.Hour)
	events := []entities.UsageEvent{
		{EventKey: "left", AuthIndex: "b2", Model: "known", Timestamp: start, TotalTokens: 10, CostUSD: windowCostPtr(1), CostAvailable: windowAvailablePtr(true)},
		{EventKey: "middle-not-raw", AuthIndex: "b2", Model: "known", Timestamp: start.Add(2 * time.Hour), TotalTokens: 999, CostUSD: windowCostPtr(999), CostAvailable: windowAvailablePtr(true)},
		{EventKey: "right", AuthIndex: "b2", Model: "known", Timestamp: end.Add(-time.Hour), TotalTokens: 30, CostUSD: windowCostPtr(3), CostAvailable: windowAvailablePtr(true)},
		{EventKey: "end-excluded", AuthIndex: "b2", Model: "known", Timestamp: end, TotalTokens: 999, CostUSD: windowCostPtr(999), CostAvailable: windowAvailablePtr(true)},
		{EventKey: "before-excluded", AuthIndex: "b2", Model: "known", Timestamp: start.Add(-time.Second), TotalTokens: 999, CostUSD: windowCostPtr(999), CostAvailable: windowAvailablePtr(true)},
		{EventKey: "identity-excluded", AuthIndex: "other", Model: "known", Timestamp: start, TotalTokens: 999, CostUSD: windowCostPtr(999), CostAvailable: windowAvailablePtr(true)},
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range []entities.UsageOverviewHourlyStat{
		{BucketStart: start.Truncate(time.Hour), AuthIndex: "b2", Model: "known", TotalTokens: 999, CostUSD: windowCostPtr(999), UnavailableCostCount: windowUnavailablePtr(0), CreatedAt: start, UpdatedAt: start},
		{BucketStart: start.Add(2 * time.Hour).Truncate(time.Hour), AuthIndex: "b2", Model: "known", TotalTokens: 20, CostUSD: windowCostPtr(2), UnavailableCostCount: windowUnavailablePtr(0), CreatedAt: start, UpdatedAt: start},
		{BucketStart: end.Add(-time.Hour).Truncate(time.Hour), AuthIndex: "b2", Model: "known", TotalTokens: 999, CostUSD: windowCostPtr(999), UnavailableCostCount: windowUnavailablePtr(0), CreatedAt: start, UpdatedAt: start},
	} {
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	b2RejectStoredRepositoryPriceReads(t, db)
	calc, err := repository.NewStoredUsageWindowStatsCalculator(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		stats, err := calc.SumByAuthIndex(context.Background(), " b2 ", start, &end)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Tokens != 60 || stats.Cost != 6 || !stats.CostAvailable || stats.PricingSnapshotID != "" {
			t.Fatalf("raw/rollup overlap, boundary, or identity leak: %+v", stats)
		}
		grouped, err := calc.SumGroupsByAuthIndex(context.Background(), "b2", start, &end, func(m string) (string, bool) { return "group", m == "known" })
		if err != nil {
			t.Fatal(err)
		}
		if !grouped.Complete || grouped.Groups["group"].Tokens != 60 || grouped.Groups["group"].Cost != 6 {
			t.Fatalf("group semantics: %+v", grouped)
		}
	}
}
func TestB2StoredHourlyUnknownAndPartialAvailability(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cost      *float64
		missing   *int64
		available bool
		want      float64
	}{
		{"known_zero", windowCostPtr(0), windowUnavailablePtr(0), true, 0},
		{"known_partial", windowCostPtr(2), windowUnavailablePtr(1), false, 2},
		{"null_amount", nil, windowUnavailablePtr(0), false, 0},
		{"null_counter", windowCostPtr(8), nil, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDatabase(t)
			start := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
			end := start.Add(8 * time.Hour)
			row := entities.UsageOverviewHourlyStat{BucketStart: start.Add(time.Hour), AuthIndex: "b2", Model: "known", TotalTokens: 20, CostUSD: tc.cost, UnavailableCostCount: tc.missing, CreatedAt: start, UpdatedAt: start}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			b2RejectStoredRepositoryPriceReads(t, db)
			calc, err := repository.NewStoredUsageWindowStatsCalculator(context.Background(), db)
			if err != nil {
				t.Fatal(err)
			}
			stats, err := calc.SumByAuthIndex(context.Background(), "b2", start, &end)
			if err != nil {
				t.Fatal(err)
			}
			if stats.Tokens != 20 || stats.Cost != tc.want || stats.CostAvailable != tc.available {
				t.Fatalf("hourly NULL/free/partial: %+v", stats)
			}
			grouped, err := calc.SumGroupsByAuthIndex(context.Background(), "b2", start, &end, func(string) (string, bool) { return "", false })
			if err != nil {
				t.Fatal(err)
			}
			if grouped.Complete {
				t.Fatal("unknown model silently omitted")
			}
		})
	}
}
func TestB2StoredReaderDoesNotWaitForWriter(t *testing.T) {
	db, writer, _ := openTestDatabasePools(t, "b2-stored-reader.db")
	start := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	if err := db.Create(&entities.UsageEvent{EventKey: "reader", AuthIndex: "b2", Model: "known", Timestamp: start, TotalTokens: 20, CostUSD: windowCostPtr(2), CostAvailable: windowAvailablePtr(true)}).Error; err != nil {
		t.Fatal(err)
	}
	calc, err := repository.NewStoredUsageWindowStatsCalculator(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	held, err := writer.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stats, err := calc.SumByAuthIndex(ctx, "b2", start, &end)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Tokens != 20 || stats.Cost != 2 {
		t.Fatalf("reader facts: %+v", stats)
	}
}
func b2RejectStoredRepositoryPriceReads(t *testing.T, db *gorm.DB) {
	t.Helper()
	reject := func(tx *gorm.DB) {
		sql := strings.ToLower(tx.Statement.Table + " " + tx.Statement.SQL.String())
		if strings.Contains(sql, "price") || strings.Contains(sql, "pricing") {
			tx.AddError(fmt.Errorf("stored calculator read price table: %s", sql))
			t.Errorf("forbidden pricing read: %s", sql)
		}
	}
	db.Callback().Query().Before("gorm:query").Register("b2_no_pricing", reject)
	db.Callback().Row().Before("gorm:row").Register("b2_no_pricing", reject)
	t.Cleanup(func() { db.Callback().Query().Remove("b2_no_pricing"); db.Callback().Row().Remove("b2_no_pricing") })
}
