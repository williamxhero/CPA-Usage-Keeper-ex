package test

import (
	"context"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	. "cpa-usage-keeper/internal/quota"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/timeutil"
	"gorm.io/gorm"
	"strings"
	"testing"
	"time"
)

func b2Catalog(t *testing.T, price float64) *pricing.Catalog {
	t.Helper()
	snapshot, err := pricing.CompilePricingSnapshot([]pricing.ModelPricingConfig{{Model: "b2-model", PricingStyle: entities.ModelPricingStyleOpenAI, ModelMultiplier: 1, ConditionalMultipliers: []pricing.RuleConfig{}, Branches: []pricing.PriceBranch{}, BasePrices: pricing.BasePrices{Input: price}}}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return pricing.NewCatalog(snapshot)
}

func TestB2StoredWindowPriceBoundary(t *testing.T) {
	db := openQuotaTestDB(t)
	start := time.Date(2026, 6, 2, 1, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Hour)
	seedUsageEvent(t, db, entities.UsageEvent{AuthIndex: "b2", Model: "b2-model", Timestamp: start, InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: floatPtr(1.25), CostAvailable: boolPtr(true)})
	calc, err := repository.NewStoredUsageWindowStatsCalculator(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	got, err := calc.SumByAuthIndex(context.Background(), "b2", start, &end)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cost != 1.25 || !got.CostAvailable {
		t.Fatalf("stored fee must be 1.25, not current 9: %+v", got)
	}
	count := b2RejectPricingReads(t, db)
	catalog := b2Catalog(t, 9)
	stored := NewServiceWithStoredWindowStats(db, NewProviderRegistry(nil), ServiceOptions{PricingCatalog: catalog})
	current := NewServiceWithRegistry(db, NewProviderRegistry(nil), catalog)
	t.Cleanup(stored.StopRefreshTasks)
	t.Cleanup(current.StopRefreshTasks)
	seconds := int64(5 * 3600)
	response := func() CheckResponse {
		return CheckResponse{Quota: []QuotaRow{{Key: "rate_limit.primary_window", Scope: "window", Window: &QuotaWindow{Seconds: &seconds}, ResetAt: timeutil.FormatStorageTime(end)}}}
	}
	for _, price := range []float64{9, 90} {
		catalog.Replace(b2Catalog(t, price).Snapshot())
		s := attachWindowUsageStats(stored, context.Background(), "b2", response(), end).Quota[0]
		c := attachWindowUsageStats(current, context.Background(), "b2", response(), end).Quota[0]
		assertWindowUsage(t, s, 1_000_000, 1.25)
		assertWindowUsage(t, c, 1_000_000, price)
		if s.WindowCostSource != "stored" || s.StoredCostAvailable == nil || !*s.StoredCostAvailable || s.DualCosts != nil || s.PricingSnapshotID != "" {
			t.Fatalf("stored relabeled current: %+v", s)
		}
		if c.PricingSnapshotID == "" || c.DualCosts == nil {
			t.Fatalf("current snapshot lost: %+v", c)
		}
	}
	if *count != 0 {
		t.Fatalf("price reads: %d", *count)
	}
}

func b2RejectPricingReads(t *testing.T, db *gorm.DB) *int {
	t.Helper()
	count := new(int)
	reject := func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.Table, "price") || strings.Contains(tx.Statement.SQL.String(), "price") {
			*count++
			tx.AddError(fmtB2PriceRead{})
		}
	}
	db.Callback().Query().Before("gorm:query").Register("b2_no_price", reject)
	db.Callback().Row().Before("gorm:row").Register("b2_no_price", reject)
	t.Cleanup(func() { db.Callback().Query().Remove("b2_no_price"); db.Callback().Row().Remove("b2_no_price") })
	return count
}

type fmtB2PriceRead struct{}

func (fmtB2PriceRead) Error() string { return "stored-window must never read a pricing table" }

func TestB2StoredWindowUnknownZeroAndUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		cost                    *float64
		available               *bool
		wantPair, wantAvailable bool
		want                    float64
	}{
		{"zero", floatPtr(0), boolPtr(true), true, true, 0}, {"partial", floatPtr(.5), boolPtr(false), true, false, .5}, {"null_amount", nil, boolPtr(true), false, false, 0}, {"null_flag", floatPtr(7), nil, false, false, 0}, {"null_both", nil, nil, false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openQuotaTestDB(t)
			start := time.Date(2026, 6, 2, 1, 0, 0, 0, time.UTC)
			end := start.Add(5 * time.Hour)
			seedUsageEvent(t, db, entities.UsageEvent{AuthIndex: "b2", Model: "b2-model", Timestamp: start, TotalTokens: 42, CostUSD: tc.cost, CostAvailable: tc.available})
			count := b2RejectPricingReads(t, db)
			calc, err := repository.NewStoredUsageWindowStatsCalculator(context.Background(), db)
			if err != nil {
				t.Fatal(err)
			}
			stats, err := calc.SumByAuthIndex(context.Background(), "b2", start, &end)
			if err != nil {
				t.Fatal(err)
			}
			if stats.CostAvailable != tc.wantAvailable || stats.Cost != tc.want || stats.Tokens != 42 {
				t.Fatalf("stored availability: %+v", stats)
			}
			service := NewServiceWithStoredWindowStats(db, NewProviderRegistry(nil), ServiceOptions{PricingCatalog: b2Catalog(t, 9)})
			t.Cleanup(service.StopRefreshTasks)
			seconds := int64(5 * 3600)
			row := attachWindowUsageStats(service, context.Background(), "b2", CheckResponse{Quota: []QuotaRow{{Key: "rate_limit.primary_window", Scope: "window", Window: &QuotaWindow{Seconds: &seconds}, ResetAt: timeutil.FormatStorageTime(end)}}}, end).Quota[0]
			if (row.WindowUsageCost != nil) != tc.wantPair || (row.WindowUsageTokens != nil) != tc.wantPair || row.StoredCostAvailable == nil || *row.StoredCostAvailable != tc.wantAvailable {
				t.Fatalf("unknown became free/partial contract lost: %+v", row)
			}
			if *count != 0 {
				t.Fatal("pricing table read")
			}
		})
	}
}
func TestB2StoredWindowInitializationFailureNeverFallsBack(t *testing.T) {
	service := NewServiceWithStoredWindowStats(nil, NewProviderRegistry(nil), ServiceOptions{PricingCatalog: b2Catalog(t, 9)})
	t.Cleanup(service.StopRefreshTasks)
	now := time.Now()
	seconds := int64(5 * 3600)
	used := 37.0
	response := attachWindowUsageStats(service, context.Background(), "b2", CheckResponse{Quota: []QuotaRow{{Key: "rate_limit.primary_window", Scope: "window", UsedPercent: &used, Window: &QuotaWindow{Seconds: &seconds}, ResetAt: timeutil.FormatStorageTime(now)}}}, now)
	row := response.Quota[0]
	if row.DualCosts != nil || row.PricingSnapshotID != "" || row.WindowUsageCost != nil || row.WindowUsageTokens != nil || row.UsedPercent == nil || *row.UsedPercent != 37 {
		t.Fatalf("stored init failed with current fallback/erased facts: %+v", row)
	}
}
func TestB2StoredReadFailurePreservesHeaderProgress(t *testing.T) {
	db := openQuotaTestDatabase(t)
	seedUsageIdentity(t, db, entities.UsageIdentity{Identity: "codex-auth", Provider: "codex", Type: "codex", AuthType: entities.UsageIdentityAuthTypeCredential})
	count := b2RejectPricingReads(t, db)
	if err := db.Migrator().DropTable(&entities.UsageEvent{}); err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithStoredWindowStats(db, NewProviderRegistry(nil), ServiceOptions{PricingCatalog: b2Catalog(t, 9)})
	t.Cleanup(service.StopRefreshTasks)
	now := time.Now()
	if !applyUsageHeaderSnapshot(service, context.Background(), codexUsageHeaderSnapshot("codex-auth", now, "37")) {
		t.Fatal("local failure erased header")
	}
	task := waitForRefreshTask(t, service, "codex-auth", RefreshTaskStatusCompleted)
	row := task.Quota.Quota[0]
	if row.UsedPercent == nil || *row.UsedPercent != 37 || row.ResetAt == "" || row.WindowUsageCost != nil || row.DualCosts != nil || row.PricingSnapshotID != "" {
		t.Fatalf("header/reset lost or current fallback: %+v", row)
	}
	if *count != 0 {
		t.Fatal("price query")
	}
}
