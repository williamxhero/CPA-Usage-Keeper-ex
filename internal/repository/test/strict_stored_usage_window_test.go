package test

import (
	"context"
	"math"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
)

func TestStrictStoredWindowKeepsKnownPartialAmountButRejectsUnknownGroups(t *testing.T) {
	db := openTestDatabase(t)
	start := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(7 * 24 * time.Hour)
	row := entities.UsageOverviewHourlyStat{BucketStart: start.Add(24 * time.Hour), AuthIndex: "auth-a", Model: "model-a",
		TotalTokens: 10, CostUSD: windowCostPtr(2), UnavailableCostCount: windowUnavailablePtr(1)}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	b2RejectStoredRepositoryPriceReads(t, db)
	calculator, err := repository.NewStrictStoredUsageWindowStatsCalculator(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := calculator.SumByAuthIndex(context.Background(), "auth-a", start, &end)
	if err != nil || stats.Tokens != 10 || stats.Cost != 2 || stats.CostAvailable {
		t.Fatalf("known partial fee changed: stats=%+v err=%v", stats, err)
	}
	grouper := func(string) (string, bool) { return "group", true }
	groups, err := calculator.SumGroupsByAuthIndex(context.Background(), "auth-a", start, &end, grouper)
	if err != nil || !groups.Complete || groups.Groups["group"].Tokens != 10 || groups.Groups["group"].Cost != 2 || groups.Groups["group"].CostAvailable {
		t.Fatalf("known partial group changed: groups=%+v err=%v", groups, err)
	}
	if err := db.Model(&entities.UsageOverviewHourlyStat{}).Where("id = ?", row.ID).Update("cost_usd", nil).Error; err != nil {
		t.Fatal(err)
	}
	if groups, err := calculator.SumGroupsByAuthIndex(context.Background(), "auth-a", start, &end, grouper); err == nil || len(groups.Groups) != 0 {
		t.Fatalf("strict grouped read accepted an unknown amount: groups=%+v err=%v", groups, err)
	}
	partial, err := repository.NewStoredUsageWindowStatsCalculator(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	stats, err = partial.SumByAuthIndex(context.Background(), "auth-a", start, &end)
	if err != nil || stats.Tokens != 10 || stats.CostAvailable || stats.UnavailableReason != "stored_cost_unknown" {
		t.Fatalf("ordinary partial contract was changed by strict reader: stats=%+v err=%v", stats, err)
	}
}

func TestStrictStoredWindowRejectsOverflowWithoutPartialMoney(t *testing.T) {
	db := openTestDatabase(t)
	start := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	for i, model := range []string{"a", "b"} {
		if err := db.Create(&entities.UsageEvent{EventKey: model, AuthIndex: "auth-a", Model: model,
			Timestamp: start.Add(time.Duration(i+1) * time.Minute), TotalTokens: 1,
			CostUSD: windowCostPtr(math.MaxFloat64 * 0.75), CostAvailable: windowAvailablePtr(true)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	calculator, err := repository.NewStrictStoredUsageWindowStatsCalculator(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if stats, err := calculator.SumByAuthIndex(context.Background(), "auth-a", start, &end); err == nil || stats.Cost != 0 || stats.Tokens != 0 {
		t.Fatalf("strict sum returned partial overflow: stats=%+v err=%v", stats, err)
	}
	if groups, err := calculator.SumGroupsByAuthIndex(context.Background(), "auth-a", start, &end, func(string) (string, bool) { return "same", true }); err == nil || len(groups.Groups) != 0 {
		t.Fatalf("strict groups returned partial overflow: groups=%+v err=%v", groups, err)
	}
}
