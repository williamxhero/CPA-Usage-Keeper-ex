package test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
	"gorm.io/gorm"
)

func TestStoredComparisonCurrentNineToNinetyReadOnly(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db := openTestDatabase(t)
	start := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	fee, zero := 1.25, int64(0)
	if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: start, Model: "priced", RequestCount: 1, InputTokens: 1000000, TotalTokens: 1000000, CostUSD: &fee, UnavailableCostCount: &zero}).Error; err != nil {
		t.Fatal(err)
	}
	f := dto.UsageQueryFilter{Range: "custom", CustomUnit: "hour", StartTime: &start, EndTime: &end, EndExclusive: true}
	for _, price := range []float64{9, 90} {
		upsertUsageCostResolverPrice(t, db, "priced", price)
		current, err := repository.BuildUsageOverviewComparisonsWithFilterAndRecentCache(db, f, nil, newUsageCostResolverForTest(t, db))
		if err != nil || current.Comparisons.Models["priced"].CostUSD != price {
			t.Fatalf("current %+v %v", current, err)
		}
		r, _ := repository.NewStoredUsageComparisonReader(db)
		stored, err := r.Read(f)
		if err != nil || stored.Models["priced"].StoredCostUSD != 1.25 || !stored.Models["priced"].StoredCostAvailable {
			t.Fatalf("stored %+v %v", stored, err)
		}
	}
	before := storedOverviewFactsHash(t, db)
	queries := storedOverviewRejectSideEffects(t, db)
	r, _ := repository.NewStoredUsageComparisonReader(db)
	_, err := r.Read(f)
	if err != nil {
		t.Fatal(err)
	}
	if before != storedOverviewFactsHash(t, db) {
		t.Fatal("reader wrote facts")
	}
	if len(*queries) != 4 {
		t.Fatalf("expected 4 projections: %v", *queries)
	}
	for _, q := range *queries {
		if strings.Contains(q, "usage_events") || strings.Contains(q, "model_alias") || strings.Contains(q, "service_tier") {
			t.Fatalf("unneeded history/rule dimensions %s", q)
		}
	}
	typ := reflect.TypeFor[dto.StoredUsageComparisonItemRecord]()
	for _, name := range []string{"CostUSD", "CostAvailable", "DualCosts", "PricingSnapshotID"} {
		if _, ok := typ.FieldByName(name); ok {
			t.Fatal(name)
		}
	}
}

func TestStoredComparisonExactRawIdentityAndAmbiguousRollup(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db := openTestDatabase(t)
	start := time.Date(2026, 7, 20, 12, 15, 0, 0, time.UTC)
	end := start.Add(2 * time.Hour)
	zero := int64(0)
	yes := true
	identities := []entities.UsageIdentity{{AuthType: entities.UsageIdentityAuthTypeCredential, Identity: "same", Name: "file"}, {AuthType: entities.UsageIdentityAuthTypeAIProvider, Identity: "same", Name: "provider"}, {AuthType: entities.UsageIdentityAuthTypeCredential, Identity: " padded ", Name: "exact"}}
	if err := db.Create(&identities).Error; err != nil {
		t.Fatal(err)
	}
	for i, e := range []struct {
		kind, index string
		cost        float64
	}{{"oauth", "same", 2}, {"apikey", "same", 3}, {"OAUTH", "same", 5}, {"", "same", 7}, {"oauth", " padded ", 11}, {"oauth", "padded", 13}, {"oauth ", "same", 17}} {
		if err := db.Create(&entities.UsageEvent{Timestamp: start.Add(time.Duration(i) * time.Minute), Model: "m", AuthType: e.kind, AuthIndex: e.index, CostUSD: &e.cost, CostAvailable: &yes, TotalTokens: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	cost := 19.0
	if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: start.Truncate(time.Hour).Add(time.Hour), Model: "m", AuthIndex: "same", RequestCount: 1, TotalTokens: 1, CostUSD: &cost, UnavailableCostCount: &zero}).Error; err != nil {
		t.Fatal(err)
	}
	storedOverviewRejectSideEffects(t, db)
	r, _ := repository.NewStoredUsageComparisonReader(db)
	out, err := r.Read(dto.UsageQueryFilter{Range: "custom", StartTime: &start, EndTime: &end, EndExclusive: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Models["m"].StoredCostUSD != 77 || out.Models["m"].Requests != 8 {
		t.Fatalf("totals %+v", out.Models)
	}
	if out.Credentials["same"].StoredCostUSD != 2 || out.AIProviders["same"].StoredCostUSD != 3 || out.Credentials[" padded "].StoredCostUSD != 11 || len(out.Credentials) != 2 {
		t.Fatalf("unsafe attribution %+v", out)
	}
	if out.IdentityAttributionComplete || out.UnattributedRequests != 5 {
		t.Fatalf("missing attribution flag %+v", out)
	}
}

func TestStoredComparisonSnapshotIdentityAndExhaustedWriter(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db, writer, reader := openTestDatabasePools(t, "stored-comparison-snapshot.db")
	start := time.Date(2026, 7, 20, 11, 45, 0, 0, time.UTC)
	end := start.Add(90 * time.Minute)
	zero := int64(0)
	yes := true
	fee := 3.0
	if err := db.Create(&entities.UsageIdentity{AuthType: entities.UsageIdentityAuthTypeCredential, Identity: "file", Name: "before"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: start.Add(15 * time.Minute), Model: "m", AuthIndex: "file", RequestCount: 1, CostUSD: &fee, UnavailableCostCount: &zero}).Error; err != nil {
		t.Fatal(err)
	}
	fee = 2
	if err := db.Create(&entities.UsageEvent{Timestamp: start, Model: "m", AuthIndex: "file", AuthType: "oauth", CostUSD: &fee, CostAvailable: &yes}).Error; err != nil {
		t.Fatal(err)
	}
	changed := false
	after := func(tx *gorm.DB) {
		if !changed && strings.Contains(tx.Statement.SQL.String(), "usage_overview_hourly_stats") {
			changed = true
			if _, err := writer.Exec("UPDATE usage_events SET cost_usd=200"); err != nil {
				tx.AddError(err)
			}
			if _, err := writer.Exec("UPDATE usage_identities SET name='after'"); err != nil {
				tx.AddError(err)
			}
		}
	}
	if err := db.Callback().Query().After("gorm:query").Register("stored_comparison_snapshot", after); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove("stored_comparison_snapshot") })
	r, _ := repository.NewStoredUsageComparisonReader(db)
	f := dto.UsageQueryFilter{Range: "custom", StartTime: &start, EndTime: &end, EndExclusive: true}
	out, err := r.Read(f)
	if err != nil || !changed || out.Models["m"].StoredCostUSD != 5 || out.Credentials["file"].Label != "before" {
		t.Fatalf("mixed snapshot %+v %v", out, err)
	}
	var value float64
	if err := reader.QueryRow("SELECT cost_usd FROM usage_events").Scan(&value); err != nil || value != 200 {
		t.Fatalf("commit %v %v", value, err)
	}
	writer.SetMaxOpenConns(1)
	held, err := writer.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	second, err := repository.NewStoredUsageComparisonReader(db.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	out, err = second.Read(f)
	cancel()
	held.Close()
	if err != nil || out.Models["m"].StoredCostUSD != 203 || out.Credentials["file"].Label != "after" {
		t.Fatalf("exhausted writer %+v %v", out, err)
	}
	if reader.Stats().InUse != 0 || writer.Stats().InUse != 0 {
		t.Fatal("connection leak")
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	r, _ = repository.NewStoredUsageComparisonReader(db.WithContext(ctx))
	out, err = r.Read(f)
	if !errors.Is(err, context.Canceled) || out != nil {
		t.Fatalf("cancel %+v %v", out, err)
	}
}
