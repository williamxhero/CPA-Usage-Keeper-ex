package service

import (
	"context"
	"sync"

	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository/storedpricing"
	servicedto "cpa-usage-keeper/internal/service/dto"
	"gorm.io/gorm"
)

// StoredPricingRecalculationProvider keeps the complete upstream management and
// bounded explicit replay workflow inside the stored domain.
type StoredPricingRecalculationProvider interface {
	StoredPricingProvider
	PricingRecalculationProvider
}

type storedPricingRecalculationService struct {
	StoredPricingProvider
	PricingRecalculationProvider
	coordinator *pricingService
}

// NewStoredPricingServiceWithRecalculation reuses the upstream task/permit/page
// lifecycle with an explicitly independent snapshot source, never current pricing.
func NewStoredPricingServiceWithRecalculation(db *gorm.DB, store *storedpricing.Store, opts PricingRecalculationDependencies, fetcher ModelsFetcher) StoredPricingRecalculationProvider {
	if store == nil {
		panic("stored pricing store is required")
	}
	coordinator := newPricingRecalculationCoordinator(db, store, &sync.Mutex{}, opts)
	return &storedPricingRecalculationService{
		StoredPricingProvider:        NewStoredPricingService(db, store, fetcher),
		PricingRecalculationProvider: coordinator,
		coordinator:                  coordinator,
	}
}

func (s *storedPricingRecalculationService) SavePricingModel(ctx context.Context, input pricing.ModelPricingConfig) (servicedto.SavePricingModelResponse, error) {
	s.coordinator.mutationMu.Lock()
	defer s.coordinator.mutationMu.Unlock()
	if s.coordinator.recalculationRunning {
		return servicedto.SavePricingModelResponse{}, ErrPricingBusy
	}
	return s.StoredPricingProvider.SavePricingModel(ctx, input)
}

func (s *storedPricingRecalculationService) DeletePricingModel(ctx context.Context, model string) (servicedto.DeletePricingModelResponse, error) {
	s.coordinator.mutationMu.Lock()
	defer s.coordinator.mutationMu.Unlock()
	if s.coordinator.recalculationRunning {
		return servicedto.DeletePricingModelResponse{}, ErrPricingBusy
	}
	return s.StoredPricingProvider.DeletePricingModel(ctx, model)
}

func (s *storedPricingRecalculationService) ApplyPricingSync(ctx context.Context, request servicedto.PricingSyncApplyRequest) (servicedto.PricingSyncApplyResponse, error) {
	s.coordinator.mutationMu.Lock()
	defer s.coordinator.mutationMu.Unlock()
	if s.coordinator.recalculationRunning {
		return servicedto.PricingSyncApplyResponse{}, ErrPricingBusy
	}
	return s.StoredPricingProvider.ApplyPricingSync(ctx, request)
}
