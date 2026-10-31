package test

import (
	"testing"
	"time"

	"cpa-usage-keeper/internal/repository/storedpricing"
	"cpa-usage-keeper/internal/service"
	"gorm.io/gorm"
)

// Only complete stored-config callers use this factory; legacy providers and
// retired-route fixtures retain their original constructors and assertions.
func newAPIStoredConfigProvider(t *testing.T, db *gorm.DB) service.StoredPricingProvider {
	t.Helper()
	if err := storedpricing.CreateSchema(db); err != nil {
		t.Fatal(err)
	}
	store, err := storedpricing.Open(db, time.Local)
	if err != nil {
		t.Fatal(err)
	}
	return service.NewStoredPricingService(db, store, nil)
}

// Same physical-price assertions, projected from the independent stored table.
type storedHTTPConfigSetting struct {
	Model            string
	PromptPricePer1M float64
}

func (storedHTTPConfigSetting) TableName() string { return "stored_model_price_settings" }
