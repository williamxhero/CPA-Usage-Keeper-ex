package storedpricing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"cpa-usage-keeper/internal/pricing"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

var ErrInvalidPricingSnapshot = errors.New("invalid stored pricing snapshot")

// The stored valuation tables never alias the fork's model_price_settings,
// model_price_rules, pricing migration evidence, official prices or matrix.
type modelSetting struct {
	ID                   int64   `gorm:"primaryKey"`
	Model                string  `gorm:"not null;uniqueIndex:uniq_stored_model_name"`
	PricingStyle         string  `gorm:"not null"`
	PromptPricePer1M     float64 `gorm:"not null"`
	CompletionPricePer1M float64 `gorm:"not null"`
	CacheReadPricePer1M  float64 `gorm:"not null"`
	CacheWritePricePer1M float64 `gorm:"not null"`
	PriceMultiplier      float64 `gorm:"not null"`
	BranchesJSON         string  `gorm:"type:text;not null"`
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

func (modelSetting) TableName() string { return "stored_model_price_settings" }

type modelRule struct {
	ID             int64   `gorm:"primaryKey"`
	ModelSettingID int64   `gorm:"not null;uniqueIndex:uniq_stored_model_rule,priority:1"`
	Key            string  `gorm:"not null;uniqueIndex:uniq_stored_model_rule,priority:2"`
	Value          string  `gorm:"not null;uniqueIndex:uniq_stored_model_rule,priority:3"`
	Multiplier     float64 `gorm:"not null"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (modelRule) TableName() string { return "stored_model_price_rules" }

type configState struct {
	ID             int64 `gorm:"primaryKey;autoIncrement:false;check:chk_stored_pricing_singleton,id = 1"`
	ConfigRevision int64 `gorm:"not null;check:chk_stored_pricing_revision,config_revision >= 0"`
}

func (configState) TableName() string { return "stored_pricing_state" }

// CreateSchema only creates missing stored-domain tables. Registering this in
// the normal migration sequence and initializing historical fees are separate slices.
func CreateSchema(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("database is nil")
	}
	return db.Clauses(dbresolver.Write).Transaction(func(tx *gorm.DB) error {
		for _, table := range []any{&modelSetting{}, &modelRule{}, &configState{}} {
			if !tx.Migrator().HasTable(table) {
				if err := tx.Migrator().CreateTable(table); err != nil {
					return fmt.Errorf("create stored pricing table: %w", err)
				}
			}
		}
		return nil
	})
}

// Store owns a separate immutable catalog and serializes its own config writes.
// A caller must not pass the fork catalog to this adapter.
type Store struct {
	db       *gorm.DB
	location *time.Location
	catalog  *pricing.Catalog
	mu       sync.Mutex
	revision int64
}

// Open requires an existing stored schema; it never copies legacy prices or
// starts backfill. An empty stored configuration is an empty fee catalog.
func Open(db *gorm.DB, location *time.Location) (*Store, error) {
	if db == nil || location == nil {
		return nil, fmt.Errorf("stored pricing database and timezone are required")
	}
	var snapshot *pricing.Snapshot
	var revision int64
	err := db.Clauses(dbresolver.Write).Transaction(func(tx *gorm.DB) error {
		var err error
		snapshot, revision, err = loadSnapshot(tx, location)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &Store{db: db, location: location, catalog: pricing.NewCatalog(snapshot), revision: revision}, nil
}

// List returns the revision paired with the published deep-copy configuration.
func (s *Store) List() ([]pricing.ModelPricingConfig, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalog.Snapshot().PricingModelConfigs(), s.revision
}

// NewResolver pins only the stored catalog. Its CalculateFee uses complete
// branches and multipliers; the fork's current-snapshot catalog is untouched.
func (s *Store) NewResolver() pricing.Resolver { return s.catalog.NewResolver() }

// Snapshot pins the independent immutable stored configuration, never current pricing.
func (s *Store) Snapshot() *pricing.Snapshot { return s.catalog.Snapshot() }

// SnapshotWithRevision pins one committed stored configuration and its matching
// revision. It never reads or publishes the fork's current pricing catalog.
func (s *Store) SnapshotWithRevision(ctx context.Context) (*pricing.Snapshot, int64, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalog.Snapshot(), s.revision, nil
}

// Save validates a complete model and commits model, rules and revision in one
// transaction. Publication happens only after commit succeeds.
func (s *Store) Save(ctx context.Context, input pricing.ModelPricingConfig) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("stored pricing store is nil")
	}
	if err := validateDefaultBranches(input); err != nil {
		return 0, err
	}
	validated, err := pricing.CompilePricingSnapshot([]pricing.ModelPricingConfig{input}, s.location)
	if err != nil {
		return 0, err
	}
	config, _ := validated.PricingModelConfig(strings.TrimSpace(input.Model))
	s.mu.Lock()
	defer s.mu.Unlock()
	var next int64
	var candidate *pricing.Snapshot
	err = s.db.Clauses(dbresolver.Write).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		configs, revision, err := loadConfigs(tx)
		if err != nil {
			return err
		}
		replaced := false
		for index := range configs {
			if configs[index].Model == config.Model {
				configs[index] = config
				replaced = true
				break
			}
		}
		if !replaced {
			configs = append(configs, config)
		}
		candidate, err = pricing.CompilePricingSnapshot(configs, s.location)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidPricingSnapshot, err)
		}
		if err := saveModel(tx, config); err != nil {
			return err
		}
		next, err = advanceRevision(tx, revision)
		return err
	})
	if err != nil {
		return 0, err
	}
	s.catalog.Replace(candidate)
	s.revision = next
	return next, nil
}

// BasePriceUpdate applies a reviewed source price without replacing existing
// stored branches or multipliers. A batch produces one committed revision.
type BasePriceUpdate struct {
	Model        string
	PricingStyle string
	Prices       pricing.BasePrices
}

func (s *Store) ApplyBasePrices(ctx context.Context, updates []BasePriceUpdate) ([]pricing.ModelPricingConfig, int64, error) {
	if s == nil || len(updates) == 0 {
		return nil, 0, fmt.Errorf("stored pricing updates are required")
	}
	seen := make(map[string]struct{}, len(updates))
	for index := range updates {
		updates[index].Model = strings.TrimSpace(updates[index].Model)
		if updates[index].Model == "" {
			return nil, 0, fmt.Errorf("model is required")
		}
		if _, exists := seen[updates[index].Model]; exists {
			return nil, 0, fmt.Errorf("duplicate model %q", updates[index].Model)
		}
		seen[updates[index].Model] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var next int64
	var candidate *pricing.Snapshot
	err := s.db.Clauses(dbresolver.Write).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		configs, revision, err := loadConfigs(tx)
		if err != nil {
			return err
		}
		indices := make(map[string]int, len(configs))
		for index := range configs {
			indices[configs[index].Model] = index
		}
		for itemIndex, update := range updates {
			index, exists := indices[update.Model]
			if !exists {
				index = len(configs)
				indices[update.Model] = index
				configs = append(configs, pricing.ModelPricingConfig{Model: update.Model, ModelMultiplier: 1,
					ConditionalMultipliers: []pricing.RuleConfig{}, Branches: []pricing.PriceBranch{}})
			}
			// Validate the reviewed style and prices against the current multiplier,
			// but preserve every existing model field other than the base prices.
			validated := configs[index]
			validated.PricingStyle = update.PricingStyle
			validated.BasePrices = update.Prices
			if _, err := pricing.CompilePricingSnapshot([]pricing.ModelPricingConfig{validated}, s.location); err != nil {
				var field *pricing.ValidationError
				if errors.As(err, &field) {
					return &pricing.ValidationError{Path: fmt.Sprintf("items[%d].%s", itemIndex, field.Path), Code: field.Code, Reason: field.Reason}
				}
				return err
			}
			if !exists {
				configs[index].PricingStyle = update.PricingStyle
			}
			configs[index].BasePrices = update.Prices
		}
		candidate, err = pricing.CompilePricingSnapshot(configs, s.location)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidPricingSnapshot, err)
		}
		for _, update := range updates {
			config, _ := candidate.PricingModelConfig(update.Model)
			if err := saveModel(tx, config); err != nil {
				return err
			}
		}
		next, err = advanceRevision(tx, revision)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	s.catalog.Replace(candidate)
	s.revision = next
	applied := make([]pricing.ModelPricingConfig, len(updates))
	for index, update := range updates {
		applied[index], _ = candidate.PricingModelConfig(update.Model)
	}
	return applied, next, nil
}

func (s *Store) Delete(ctx context.Context, model string) (int64, error) {
	if s == nil {
		return 0, fmt.Errorf("stored pricing store is nil")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return 0, fmt.Errorf("model is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var next int64
	var candidate *pricing.Snapshot
	err := s.db.Clauses(dbresolver.Write).WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		configs, revision, err := loadConfigs(tx)
		if err != nil {
			return err
		}
		index := -1
		for i := range configs {
			if configs[i].Model == model {
				index = i
				break
			}
		}
		if index < 0 {
			return gorm.ErrRecordNotFound
		}
		configs = append(configs[:index], configs[index+1:]...)
		candidate, err = pricing.CompilePricingSnapshot(configs, s.location)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidPricingSnapshot, err)
		}
		var setting modelSetting
		if err := tx.Where("model = ?", model).Take(&setting).Error; err != nil {
			return err
		}
		if err := tx.Where("model_setting_id = ?", setting.ID).Delete(&modelRule{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&setting).Error; err != nil {
			return err
		}
		next, err = advanceRevision(tx, revision)
		return err
	})
	if err != nil {
		return 0, err
	}
	s.catalog.Replace(candidate)
	s.revision = next
	return next, nil
}

func validateDefaultBranches(config pricing.ModelPricingConfig) error {
	for index, branch := range config.Branches {
		if (branch.Days == "" || branch.Days == pricing.DaysAll) && branch.Context.Type == pricing.ContextAll && branch.Period.Type == pricing.PeriodAll {
			return &pricing.ValidationError{Path: fmt.Sprintf("branches[%d].context", index), Code: "default_conflict", Reason: "an unconditional branch replaces the default price"}
		}
	}
	return nil
}

func loadSnapshot(tx *gorm.DB, location *time.Location) (*pricing.Snapshot, int64, error) {
	configs, revision, err := loadConfigs(tx)
	if err != nil {
		return nil, 0, err
	}
	snapshot, err := pricing.CompilePricingSnapshot(configs, location)
	if err != nil {
		return nil, 0, fmt.Errorf("compile stored pricing: %w", err)
	}
	return snapshot, revision, nil
}

func loadConfigs(tx *gorm.DB) ([]pricing.ModelPricingConfig, int64, error) {
	var state configState
	err := tx.Clauses(dbresolver.Write).Where("id = ?", 1).Take(&state).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, 0, fmt.Errorf("load stored pricing revision: %w", err)
	}
	var settings []modelSetting
	if err := tx.Order("model asc").Find(&settings).Error; err != nil {
		return nil, 0, fmt.Errorf("load stored pricing models: %w", err)
	}
	var rules []modelRule
	if err := tx.Order("model_setting_id asc").Order("id asc").Find(&rules).Error; err != nil {
		return nil, 0, fmt.Errorf("load stored pricing rules: %w", err)
	}
	configs := make([]pricing.ModelPricingConfig, len(settings))
	byID := make(map[int64]int, len(settings))
	for index, setting := range settings {
		var branches []pricing.PriceBranch
		if err := json.Unmarshal([]byte(setting.BranchesJSON), &branches); err != nil || branches == nil {
			return nil, 0, fmt.Errorf("decode stored pricing branches for %q: %v", setting.Model, err)
		}
		configs[index] = pricing.ModelPricingConfig{
			Model: setting.Model, PricingStyle: setting.PricingStyle,
			BasePrices:      pricing.BasePrices{Input: setting.PromptPricePer1M, Output: setting.CompletionPricePer1M, CacheRead: setting.CacheReadPricePer1M, CacheWrite: setting.CacheWritePricePer1M},
			ModelMultiplier: setting.PriceMultiplier, Branches: branches, ConditionalMultipliers: []pricing.RuleConfig{},
		}
		byID[setting.ID] = index
	}
	for _, rule := range rules {
		index, ok := byID[rule.ModelSettingID]
		if !ok {
			return nil, 0, fmt.Errorf("stored pricing rule %d has no model %d", rule.ID, rule.ModelSettingID)
		}
		configs[index].ConditionalMultipliers = append(configs[index].ConditionalMultipliers, pricing.RuleConfig{Key: rule.Key, Value: rule.Value, Multiplier: rule.Multiplier})
	}
	return configs, state.ConfigRevision, nil
}

func saveModel(tx *gorm.DB, config pricing.ModelPricingConfig) error {
	branches, err := json.Marshal(config.Branches)
	if err != nil {
		return err
	}
	var existing modelSetting
	err = tx.Clauses(dbresolver.Write).Where("model = ?", config.Model).Take(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	setting := modelSetting{
		ID: existing.ID, Model: config.Model, PricingStyle: config.PricingStyle,
		PromptPricePer1M: config.BasePrices.Input, CompletionPricePer1M: config.BasePrices.Output,
		CacheReadPricePer1M: config.BasePrices.CacheRead, CacheWritePricePer1M: config.BasePrices.CacheWrite,
		PriceMultiplier: config.ModelMultiplier, BranchesJSON: string(branches),
	}
	if err := tx.Save(&setting).Error; err != nil {
		return fmt.Errorf("save stored pricing model: %w", err)
	}
	if err := tx.Where("model_setting_id = ?", setting.ID).Delete(&modelRule{}).Error; err != nil {
		return fmt.Errorf("replace stored pricing rules: %w", err)
	}
	for _, rule := range config.ConditionalMultipliers {
		if err := tx.Create(&modelRule{ModelSettingID: setting.ID, Key: rule.Key, Value: rule.Value, Multiplier: rule.Multiplier}).Error; err != nil {
			return fmt.Errorf("save stored pricing rule: %w", err)
		}
	}
	return nil
}

func advanceRevision(tx *gorm.DB, previous int64) (int64, error) {
	if previous == math.MaxInt64 {
		return 0, fmt.Errorf("stored pricing revision exhausted")
	}
	if previous == 0 {
		result := tx.Exec("INSERT OR IGNORE INTO stored_pricing_state (id, config_revision) VALUES (1, 0)")
		if result.Error != nil {
			return 0, result.Error
		}
	}
	next := previous + 1
	result := tx.Model(&configState{}).Where("id = ? AND config_revision = ?", 1, previous).Update("config_revision", next)
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected != 1 {
		return 0, fmt.Errorf("stored pricing revision changed concurrently")
	}
	return next, nil
}
