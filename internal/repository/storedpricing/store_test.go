package storedpricing

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/helper"
	"cpa-usage-keeper/internal/pricing"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openTestStore(t *testing.T) (*gorm.DB, *Store) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "stored-pricing.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := CreateSchema(db); err != nil {
		t.Fatal(err)
	}
	if err := CreateSchema(db); err != nil {
		t.Fatalf("idempotent schema: %v", err)
	}
	store, err := Open(db, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	return db, store
}

func testConfig() pricing.ModelPricingConfig {
	threshold := int64(100)
	return pricing.ModelPricingConfig{
		Model: "example", PricingStyle: entities.ModelPricingStyleOpenAI,
		BasePrices:             pricing.BasePrices{Input: 2, Output: 4, CacheRead: 0.5, CacheWrite: 1},
		ModelMultiplier:        2,
		ConditionalMultipliers: []pricing.RuleConfig{{Key: "api_group_key", Value: "group", Multiplier: 3}},
		Branches: []pricing.PriceBranch{{ID: "large", Name: "Large context", Days: pricing.DaysAll,
			Context: pricing.ContextCondition{Type: pricing.ContextGT, Threshold: &threshold},
			Period:  pricing.PeriodCondition{Type: pricing.PeriodAll},
			Prices:  pricing.BasePrices{Input: 8, Output: 6, CacheRead: 2, CacheWrite: 3},
		}},
	}
}

func subject(tokens int64, group string) pricing.CostSubject {
	return pricing.NewCostSubject(pricing.UsageDimensions{Model: "example", APIGroupKey: group}, helper.UsageTokenCostInput{InputTokens: tokens})
}

func TestStoredModelsRoundTripAndIndependentCatalog(t *testing.T) {
	db, store := openTestStore(t)
	// These sentinel rows represent distinct fork-managed tables. The adapter
	// must not consult or modify them, even for a model with the same name.
	for _, statement := range []string{
		"CREATE TABLE model_price_settings (id INTEGER PRIMARY KEY, model TEXT, prompt_price_per1_m REAL)",
		"CREATE TABLE model_price_rules (id INTEGER PRIMARY KEY, multiplier REAL)",
		"CREATE TABLE official_model_prices (id INTEGER PRIMARY KEY, prompt_price_per1_m REAL)",
		"CREATE TABLE credential_model_price_overrides (id INTEGER PRIMARY KEY, prompt_price_per1_m REAL)",
		"CREATE TABLE pricing_migration_batches (id INTEGER PRIMARY KEY, report TEXT)",
		"CREATE TABLE pricing_migration_batch_counts (id INTEGER PRIMARY KEY, category TEXT)",
		"CREATE TABLE pricing_migration_items (id INTEGER PRIMARY KEY, outcome TEXT)",
		"CREATE TABLE pricing_migration_legacy_states (id INTEGER PRIMARY KEY, reason TEXT)",
		"INSERT INTO model_price_settings VALUES (1, 'example', 17)",
		"INSERT INTO model_price_rules VALUES (1, 13)",
		"INSERT INTO official_model_prices VALUES (1, 19)",
		"INSERT INTO credential_model_price_overrides VALUES (1, 23)",
		"INSERT INTO pricing_migration_batches VALUES (1, 'audit')",
		"INSERT INTO pricing_migration_batch_counts VALUES (1, 'model_price_settings')",
		"INSERT INTO pricing_migration_items VALUES (1, 'migrated')",
		"INSERT INTO pricing_migration_legacy_states VALUES (1, 'inactive')",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	legacy, err := pricing.CompileSnapshot([]pricing.ModelConfig{{Pricing: entities.ModelPriceSetting{
		Model: "example", PricingStyle: entities.ModelPricingStyleOpenAI, PromptPricePer1M: 17,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	fork := pricing.NewCatalog(legacy)
	forkBefore := fork.NewResolver().CalculateLegacy(subject(1_000_000, "group"))
	oldStored := store.NewResolver()
	config := testConfig()
	if revision, err := store.Save(context.Background(), config); err != nil || revision != 1 {
		t.Fatalf("save revision=%d err=%v", revision, err)
	}
	models, revision := store.List()
	if revision != 1 || !reflect.DeepEqual(models, []pricing.ModelPricingConfig{config}) {
		t.Fatalf("stored model roundtrip revision=%d models=%+v", revision, models)
	}
	models[0].Branches[0].Prices.Input = 900
	models[0].ConditionalMultipliers[0].Multiplier = 900
	if cost := store.NewResolver().CalculateFee(subject(1_000_000, "group")); !cost.Available || cost.TotalCostUSD != 48 {
		t.Fatalf("branch x model x conditional multiplier: %+v", cost)
	}
	if cost := store.NewResolver().CalculateFee(subject(50, "group")); !cost.Available || math.Abs(cost.TotalCostUSD-0.0006) > 1e-12 {
		t.Fatalf("base price x multipliers: %+v", cost)
	}
	if cost := oldStored.CalculateFee(subject(1_000_000, "group")); cost.Available || cost.TotalCostUSD != 0 {
		t.Fatalf("pre-save stored snapshot changed: %+v", cost)
	}
	if after := fork.NewResolver().CalculateLegacy(subject(1_000_000, "group")); !reflect.DeepEqual(after, forkBefore) || after.Cost.TotalCostUSD != 17 {
		t.Fatalf("fork catalog changed: before=%+v after=%+v", forkBefore, after)
	}
	for _, query := range []struct {
		sql      string
		expected any
	}{
		{"SELECT prompt_price_per1_m FROM model_price_settings WHERE id = 1", float64(17)},
		{"SELECT multiplier FROM model_price_rules WHERE id = 1", float64(13)},
		{"SELECT prompt_price_per1_m FROM official_model_prices WHERE id = 1", float64(19)},
		{"SELECT prompt_price_per1_m FROM credential_model_price_overrides WHERE id = 1", float64(23)},
		{"SELECT report FROM pricing_migration_batches WHERE id = 1", "audit"},
		{"SELECT category FROM pricing_migration_batch_counts WHERE id = 1", "model_price_settings"},
		{"SELECT outcome FROM pricing_migration_items WHERE id = 1", "migrated"},
		{"SELECT reason FROM pricing_migration_legacy_states WHERE id = 1", "inactive"},
	} {
		var got any
		if err := db.Raw(query.sql).Row().Scan(&got); err != nil || !reflect.DeepEqual(got, query.expected) {
			t.Fatalf("fork sentinel %s: got=%v want=%v err=%v", query.sql, got, query.expected, err)
		}
	}
	reopened, err := Open(db, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	persisted, persistedRevision := reopened.List()
	if persistedRevision != 1 || !reflect.DeepEqual(persisted, []pricing.ModelPricingConfig{config}) {
		t.Fatalf("reopened configuration revision=%d got=%+v", persistedRevision, persisted)
	}
	if cost := reopened.NewResolver().CalculateFee(subject(1_000_000, "group")); cost.TotalCostUSD != 48 {
		t.Fatalf("reopened branch cost=%+v", cost)
	}
}

func TestStoredRevisionAndCatalogPublishOnlyAfterTransaction(t *testing.T) {
	db, store := openTestStore(t)
	config := testConfig()
	if _, err := store.Save(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	pinned := store.NewResolver()
	if err := db.Exec("CREATE TRIGGER reject_stored_rule BEFORE INSERT ON stored_model_price_rules WHEN NEW.value = 'blocked' BEGIN SELECT RAISE(ABORT, 'rule blocked'); END").Error; err != nil {
		t.Fatal(err)
	}
	changed := testConfig()
	changed.BasePrices.Input = 99
	changed.ConditionalMultipliers[0].Value = "blocked"
	if _, err := store.Save(context.Background(), changed); err == nil {
		t.Fatal("rule insertion failure committed a partial model")
	}
	models, revision := store.List()
	if revision != 1 || !reflect.DeepEqual(models, []pricing.ModelPricingConfig{config}) {
		t.Fatalf("failed transaction published revision=%d models=%+v", revision, models)
	}
	reopened, err := Open(db, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	persisted, persistedRevision := reopened.List()
	if persistedRevision != 1 || !reflect.DeepEqual(persisted, models) {
		t.Fatalf("failed transaction persisted revision=%d models=%+v", persistedRevision, persisted)
	}
	if err := db.Exec("DROP TRIGGER reject_stored_rule").Error; err != nil {
		t.Fatal(err)
	}
	if revision, err := store.Save(context.Background(), changed); err != nil || revision != 2 {
		t.Fatalf("save after rollback revision=%d err=%v", revision, err)
	}
	if cost := pinned.CalculateFee(subject(1_000_000, "group")); cost.TotalCostUSD != 48 {
		t.Fatalf("previous resolver was not pinned: %+v", cost)
	}
	if cost := store.NewResolver().CalculateFee(subject(50, "blocked")); math.Abs(cost.TotalCostUSD-0.0297) > 1e-12 {
		t.Fatalf("published updated fee: %+v", cost)
	}
	if revision, err := store.Delete(context.Background(), "example"); err != nil || revision != 3 {
		t.Fatalf("delete revision=%d err=%v", revision, err)
	}
	if _, err := store.Delete(context.Background(), "example"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("missing delete error=%v", err)
	}
	if models, revision := store.List(); revision != 3 || len(models) != 0 {
		t.Fatalf("deleted config revision=%d models=%+v", revision, models)
	}
	if cost := store.NewResolver().CalculateFee(subject(1_000_000, "group")); cost.Available {
		t.Fatalf("deleted model still available: %+v", cost)
	}
	var rules int64
	if err := db.Model(&modelRule{}).Count(&rules).Error; err != nil || rules != 0 {
		t.Fatalf("delete left rules=%d err=%v", rules, err)
	}
}

func TestStoredZeroMultipliersSurviveReopen(t *testing.T) {
	db, store := openTestStore(t)
	config := testConfig()
	config.ConditionalMultipliers[0].Multiplier = 0
	if revision, err := store.Save(context.Background(), config); err != nil || revision != 1 {
		t.Fatalf("save zero rule multiplier revision=%d err=%v", revision, err)
	}
	reopened, err := Open(db, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	models, revision := reopened.List()
	if revision != 1 || !reflect.DeepEqual(models, []pricing.ModelPricingConfig{config}) {
		t.Fatalf("zero rule multiplier lost revision=%d models=%+v", revision, models)
	}
	if cost := reopened.NewResolver().CalculateFee(subject(1_000_000, "group")); !cost.Available || cost.TotalCostUSD != 0 {
		t.Fatalf("zero rule multiplier fee=%+v", cost)
	}
	config.ConditionalMultipliers[0].Multiplier = 3
	config.ModelMultiplier = 0
	if revision, err := store.Save(context.Background(), config); err != nil || revision != 2 {
		t.Fatalf("save zero model multiplier revision=%d err=%v", revision, err)
	}
	reopened, err = Open(db, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	models, revision = reopened.List()
	if revision != 2 || !reflect.DeepEqual(models, []pricing.ModelPricingConfig{config}) {
		t.Fatalf("zero model multiplier lost revision=%d models=%+v", revision, models)
	}
	if cost := reopened.NewResolver().CalculateFee(subject(1_000_000, "group")); !cost.Available || cost.TotalCostUSD != 0 {
		t.Fatalf("zero model multiplier fee=%+v", cost)
	}
}

func TestStoredSyncBatchPreservesBranchesAndOneRevision(t *testing.T) {
	db, store := openTestStore(t)
	original := testConfig()
	if _, err := store.Save(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	updates := []BasePriceUpdate{
		{Model: "example", PricingStyle: entities.ModelPricingStyleOpenAI, Prices: pricing.BasePrices{Input: 10, Output: 4, CacheRead: 0.5, CacheWrite: 1}},
		{Model: "another", PricingStyle: entities.ModelPricingStyleClaude, Prices: pricing.BasePrices{Input: 7, Output: 8, CacheRead: 1, CacheWrite: 2}},
	}
	applied, revision, err := store.ApplyBasePrices(context.Background(), updates)
	if err != nil || revision != 2 || len(applied) != 2 {
		t.Fatalf("apply sync revision=%d applied=%+v err=%v", revision, applied, err)
	}
	original.BasePrices = updates[0].Prices
	if !reflect.DeepEqual(applied[0], original) || applied[1].ModelMultiplier != 1 || len(applied[1].Branches) != 0 {
		t.Fatalf("sync lost stored config: %+v", applied)
	}
	reopened, err := Open(db, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	models, persistedRevision := reopened.List()
	if persistedRevision != 2 || len(models) != 2 || !reflect.DeepEqual(models[1], original) {
		t.Fatalf("sync persistence revision=%d models=%+v", persistedRevision, models)
	}
	bad := []BasePriceUpdate{{Model: "example", PricingStyle: "unknown", Prices: updates[0].Prices}}
	if _, _, err := store.ApplyBasePrices(context.Background(), bad); err == nil {
		t.Fatal("invalid source style was accepted")
	}
	if models, revision := store.List(); revision != 2 || len(models) != 2 {
		t.Fatalf("failed sync advanced revision=%d models=%+v", revision, models)
	}
}
