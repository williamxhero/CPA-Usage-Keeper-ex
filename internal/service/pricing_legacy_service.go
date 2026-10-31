package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	repodto "cpa-usage-keeper/internal/repository/dto"
	servicedto "cpa-usage-keeper/internal/service/dto"
	"gorm.io/gorm"
)

// Legacy management compatibility always projects the fork catalog, never the
// independent stored valuation catalog.
func (s *pricingService) ListPricing(context.Context) ([]entities.ModelPriceSetting, error) {
	configs := s.catalog.Snapshot().ModelConfigs()
	settings := make([]entities.ModelPriceSetting, len(configs))
	for index := range configs {
		settings[index] = configs[index].Pricing
	}
	return settings, nil
}

func (s *pricingService) UpdatePricing(ctx context.Context, input servicedto.UpdatePricingInput) (*entities.ModelPriceSetting, error) {
	settings, err := s.UpdatePricingBatch(ctx, []servicedto.UpdatePricingInput{input})
	if err != nil {
		return nil, err
	}
	return &settings[0], nil
}

func (s *pricingService) UpdatePricingBatch(ctx context.Context, inputs []servicedto.UpdatePricingInput) ([]entities.ModelPriceSetting, error) {
	if len(inputs) == 0 {
		return []entities.ModelPriceSetting{}, nil
	}
	normalized := make([]repodto.ModelPriceSettingInput, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for index, input := range inputs {
		value, err := normalizeLegacyPricingInput(input)
		if err != nil {
			return nil, fmt.Errorf("pricing at index %d: %w", index, err)
		}
		if _, exists := seen[value.Model]; exists {
			return nil, fmt.Errorf("pricing at index %d: %w: duplicate model %q", index, ErrInvalidPricingInput, value.Model)
		}
		seen[value.Model] = struct{}{}
		normalized[index] = value
	}
	settings := make([]entities.ModelPriceSetting, len(normalized))
	_, err := s.mutatePricing(ctx, func(tx *gorm.DB) error {
		for index, input := range normalized {
			setting, err := repository.UpsertModelPriceSetting(tx, input)
			if err != nil {
				return err
			}
			settings[index] = *setting
		}
		return nil
	})
	if errors.Is(err, repository.ErrInvalidPricingSnapshot) {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPricingInput, err)
	}
	return settings, err
}

func normalizeLegacyPricingInput(input servicedto.UpdatePricingInput) (repodto.ModelPriceSettingInput, error) {
	model := strings.TrimSpace(input.Model)
	if model == "" {
		return repodto.ModelPriceSettingInput{}, fmt.Errorf("%w: model is required", ErrInvalidPricingInput)
	}
	style := strings.ToLower(strings.TrimSpace(input.PricingStyle))
	if style == "" {
		style = entities.ModelPricingStyleOpenAI
	}
	if style != entities.ModelPricingStyleOpenAI && style != entities.ModelPricingStyleClaude {
		return repodto.ModelPriceSettingInput{}, fmt.Errorf("%w: pricing_style must be openai or claude", ErrInvalidPricingInput)
	}
	if input.PromptPricePer1M < 0 || input.CompletionPricePer1M < 0 || input.CacheReadPricePer1M < 0 || input.CacheWritePricePer1M < 0 {
		return repodto.ModelPriceSettingInput{}, fmt.Errorf("%w: prices must be non-negative", ErrInvalidPricingInput)
	}
	if input.PriceMultiplier != nil && (*input.PriceMultiplier < 0 || math.IsNaN(*input.PriceMultiplier) || math.IsInf(*input.PriceMultiplier, 0)) {
		return repodto.ModelPriceSettingInput{}, fmt.Errorf("%w: price_multiplier must be non-negative", ErrInvalidPricingInput)
	}
	return repodto.ModelPriceSettingInput{
		Model: model, PricingStyle: style,
		PromptPricePer1M: input.PromptPricePer1M, CompletionPricePer1M: input.CompletionPricePer1M,
		CacheReadPricePer1M: input.CacheReadPricePer1M, CacheWritePricePer1M: input.CacheWritePricePer1M,
		PriceMultiplier: input.PriceMultiplier,
	}, nil
}

func (s *pricingService) DeletePricing(ctx context.Context, model string) error {
	_, err := s.mutatePricing(ctx, func(tx *gorm.DB) error {
		return repository.DeleteModelPriceSetting(tx, model)
	})
	return err
}
