package test

import (
	"testing"
	"time"

	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository/storedpricing"
	"cpa-usage-keeper/internal/service"
	"gorm.io/gorm"
)

// Complete configuration tests bind explicitly to the independent stored domain.
// Legacy pricing fixtures and current-money catalog tests remain unchanged.
func newStoredConfigService(t *testing.T, db *gorm.DB, fetchers ...service.ModelsFetcher) (service.StoredPricingProvider, *storedpricing.Store) {
	t.Helper()
	if err := storedpricing.CreateSchema(db); err != nil {
		t.Fatal(err)
	}
	store, err := storedpricing.Open(db, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	var fetcher service.ModelsFetcher
	if len(fetchers) > 0 {
		fetcher = fetchers[0]
	}
	return service.NewStoredPricingService(db, store, fetcher), store
}

// Typed physical projections retain all upstream persistence assertions. The
// pointer also checks SQL NULL versus explicit zero despite the NOT NULL schema.
type storedConfigSetting struct {
	ID               int64
	Model            string
	PricingStyle     string
	PromptPricePer1M float64
	PriceMultiplier  *float64
	BranchesJSON     string
}

func (storedConfigSetting) TableName() string { return "stored_model_price_settings" }

type storedConfigRule struct {
	ID                  int64
	ModelPriceSettingID int64 `gorm:"column:model_setting_id"`
	Key                 string
	Value               string
	Multiplier          float64
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

func (storedConfigRule) TableName() string { return "stored_model_price_rules" }

type storedConfigState struct {
	ID             int64
	ConfigRevision int64
}

func (storedConfigState) TableName() string { return "stored_pricing_state" }

func assertStoredConfigDatabasePrompt(t *testing.T, db *gorm.DB, model string, want float64) {
	t.Helper()
	var row storedConfigSetting
	if err := db.Where("model = ?", model).Take(&row).Error; err != nil {
		t.Fatalf("load stored DB price %q: %v", model, err)
	}
	if row.PromptPricePer1M != want {
		t.Fatalf("DB prompt price = %v, want %v", row.PromptPricePer1M, want)
	}
}

// Used by existing complete-config helpers without exposing the fork catalog.
type storedConfigResolverSource interface{ NewResolver() pricing.Resolver }
