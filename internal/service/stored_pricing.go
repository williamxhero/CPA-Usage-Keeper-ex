package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/pricingmetadata"
	"cpa-usage-keeper/internal/repository/storedpricing"
	servicedto "cpa-usage-keeper/internal/service/dto"
	"gorm.io/gorm"
)

// StoredPricingProvider is the explicitly namespaced complete-configuration
// surface. It never receives the fork's current-money pricing catalog.
type StoredPricingProvider interface {
	ListPricingModels(context.Context) (servicedto.PricingModelsResponse, error)
	SavePricingModel(context.Context, pricing.ModelPricingConfig) (servicedto.SavePricingModelResponse, error)
	DeletePricingModel(context.Context, string) (servicedto.DeletePricingModelResponse, error)
	ListUsedModels(context.Context) ([]string, error)
	FetchPricingSync(context.Context, string) (servicedto.PricingSyncFetchResponse, error)
	ApplyPricingSync(context.Context, servicedto.PricingSyncApplyRequest) (servicedto.PricingSyncApplyResponse, error)
}

type storedPricingService struct {
	store  *storedpricing.Store
	models *pricingService
}

func NewStoredPricingService(db *gorm.DB, store *storedpricing.Store, fetcher ModelsFetcher) StoredPricingProvider {
	if store == nil {
		panic("stored pricing store is required")
	}
	return &storedPricingService{store: store, models: &pricingService{db: db, modelsFetcher: fetcher, metadataClient: pricingmetadata.NewClient(nil)}}
}

func (s *storedPricingService) ListPricingModels(_ context.Context) (servicedto.PricingModelsResponse, error) {
	models, revision := s.store.List()
	return servicedto.PricingModelsResponse{Models: models, ConfigRevision: revision}, nil
}

func (s *storedPricingService) SavePricingModel(ctx context.Context, input pricing.ModelPricingConfig) (servicedto.SavePricingModelResponse, error) {
	revision, err := s.store.Save(ctx, input)
	if err != nil {
		var field *pricing.ValidationError
		var conflict *pricing.BranchConflictError
		if errors.As(err, &field) || errors.As(err, &conflict) || errors.Is(err, storedpricing.ErrInvalidPricingSnapshot) {
			return servicedto.SavePricingModelResponse{}, fmt.Errorf("%w: %w", ErrInvalidPricingInput, err)
		}
		return servicedto.SavePricingModelResponse{}, err
	}
	return servicedto.SavePricingModelResponse{Model: strings.TrimSpace(input.Model), ConfigRevision: revision}, nil
}

func (s *storedPricingService) DeletePricingModel(ctx context.Context, model string) (servicedto.DeletePricingModelResponse, error) {
	revision, err := s.store.Delete(ctx, model)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return servicedto.DeletePricingModelResponse{}, fmt.Errorf("%w: %s", ErrPricingModelNotFound, model)
	}
	if err != nil {
		return servicedto.DeletePricingModelResponse{}, err
	}
	return servicedto.DeletePricingModelResponse{ConfigRevision: revision}, nil
}

func (s *storedPricingService) ListUsedModels(ctx context.Context) ([]string, error) {
	return s.models.effectiveModels(ctx)
}

func (s *storedPricingService) FetchPricingSync(ctx context.Context, source string) (servicedto.PricingSyncFetchResponse, error) {
	return s.models.FetchPricingSync(ctx, source)
}

func (s *storedPricingService) ApplyPricingSync(ctx context.Context, request servicedto.PricingSyncApplyRequest) (servicedto.PricingSyncApplyResponse, error) {
	if !validPricingSyncSource(request.Source) {
		return servicedto.PricingSyncApplyResponse{}, ErrInvalidPricingSyncSource
	}
	if len(request.Items) == 0 {
		return servicedto.PricingSyncApplyResponse{}, fmt.Errorf("%w: %w", ErrInvalidPricingInput, &pricing.ValidationError{Path: "items", Code: "invalid", Reason: "items must not be empty"})
	}
	updates := make([]storedpricing.BasePriceUpdate, len(request.Items))
	seen := make(map[string]struct{}, len(request.Items))
	for index, item := range request.Items {
		model := strings.TrimSpace(item.Model)
		if model == "" {
			return servicedto.PricingSyncApplyResponse{}, pricingSyncItemInputError(index,
				&pricing.ValidationError{Path: "model", Code: "required", Reason: "model is required"})
		}
		if _, exists := seen[model]; exists {
			return servicedto.PricingSyncApplyResponse{}, pricingSyncItemInputError(index,
				&pricing.ValidationError{Path: "model", Code: "invalid", Reason: "duplicate model"})
		}
		seen[model] = struct{}{}
		updates[index] = storedpricing.BasePriceUpdate{Model: model, PricingStyle: item.PricingStyle, Prices: item.BasePrices}
	}
	models, revision, err := s.store.ApplyBasePrices(ctx, updates)
	if err != nil {
		var field *pricing.ValidationError
		var conflict *pricing.BranchConflictError
		if errors.As(err, &field) || errors.As(err, &conflict) || errors.Is(err, storedpricing.ErrInvalidPricingSnapshot) {
			return servicedto.PricingSyncApplyResponse{}, fmt.Errorf("%w: %w", ErrInvalidPricingInput, err)
		}
		return servicedto.PricingSyncApplyResponse{}, err
	}
	return servicedto.PricingSyncApplyResponse{Models: models, ConfigRevision: revision}, nil
}
