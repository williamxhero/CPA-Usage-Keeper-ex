package test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	. "cpa-usage-keeper/internal/api"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

type pricingStub struct {
	service.PricingProvider
	usedModels    []string
	pricing       []entities.ModelPriceSetting
	preview       servicedto.PricingSyncPreview
	previewSource *string
	updated       *entities.ModelPriceSetting
	lastUpdate    *servicedto.UpdatePricingInput
	batch         []entities.ModelPriceSetting
	lastBatch     []servicedto.UpdatePricingInput
	rules         []servicedto.PricingRule
	lastRules     *servicedto.ReplacePricingRulesInput
	deleted       string
	err           error
	recalcOptions servicedto.RecalculationOptions
	recalcStart   *servicedto.StartRecalculationRequest
	recalcReply   servicedto.StartRecalculationResponse
	recalcCurrent *servicedto.RecalculationTask
	recalcErr     error
}

func (s *pricingStub) ListPricingModels(context.Context) (servicedto.PricingModelsResponse, error) {
	return servicedto.PricingModelsResponse{}, s.err
}

func (s *pricingStub) SavePricingModel(context.Context, pricing.ModelPricingConfig) (servicedto.SavePricingModelResponse, error) {
	return servicedto.SavePricingModelResponse{}, s.err
}

func (s *pricingStub) DeletePricingModel(context.Context, string) (servicedto.DeletePricingModelResponse, error) {
	return servicedto.DeletePricingModelResponse{}, s.err
}

func (s *pricingStub) FetchPricingSync(context.Context, string) (servicedto.PricingSyncFetchResponse, error) {
	return servicedto.PricingSyncFetchResponse{}, s.err
}

func (s *pricingStub) ApplyPricingSync(context.Context, servicedto.PricingSyncApplyRequest) (servicedto.PricingSyncApplyResponse, error) {
	return servicedto.PricingSyncApplyResponse{}, s.err
}

func (s *pricingStub) GetPricingRecalculationOptions(context.Context) (servicedto.RecalculationOptions, error) {
	return s.recalcOptions, s.recalcErr
}

func (s *pricingStub) StartPricingRecalculation(_ context.Context, input servicedto.StartRecalculationRequest) (servicedto.StartRecalculationResponse, error) {
	s.recalcStart = &input
	return s.recalcReply, s.recalcErr
}

func (s *pricingStub) CurrentPricingRecalculation(context.Context) (*servicedto.RecalculationTask, error) {
	return s.recalcCurrent, s.recalcErr
}

func (s *pricingStub) WaitPricingRecalculation() {}

func (s *pricingStub) ListUsedModels(context.Context) ([]string, error) {
	return s.usedModels, s.err
}

func TestPricingRouterRegistersOnlyCompleteContract(t *testing.T) {
	router := NewRouter(nil, nil, nil, &pricingStub{}, AuthConfig{}, nil, "", OptionalProviders{StoredPricing: &pricingStub{}})
	registered := make(map[string]bool)
	for _, route := range router.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{"GET /api/v1/pricing", "GET /api/v1/models/used", "GET /api/v1/pricing/stored/models", "PUT /api/v1/pricing/stored/models"} {
		if !registered[route] {
			t.Errorf("pricing route %s is missing", route)
		}
	}
}

type pricingTimeoutError struct{}

func (pricingTimeoutError) Error() string   { return "net/http: TLS handshake timeout" }
func (pricingTimeoutError) Timeout() bool   { return true }
func (pricingTimeoutError) Temporary() bool { return true }

func (s *pricingStub) ListPricing(context.Context) ([]entities.ModelPriceSetting, error) {
	return s.pricing, s.err
}

func (s *pricingStub) PreviewPricingSync(_ context.Context, source string) (servicedto.PricingSyncPreview, error) {
	if s.previewSource != nil {
		*s.previewSource = source
	}
	return s.preview, s.err
}

func TestPricingSyncPreviewSelectsSourceRemoved(t *testing.T) {
	// This legacy request now bypasses parsing and service execution.
	assertRetiredPricingRequest(t, "GET", "/api/v1/pricing/sync/preview", ``)
	assertRetiredPricingRequest(t, "GET", "/api/v1/pricing/sync/preview?source=litellm", ``)
	assertRetiredPricingRequest(t, "GET", "/api/v1/pricing/sync/preview?source=other", ``)
}

func TestPricingSyncLiteLLMTimeoutNamesSelectedSourceRemoved(t *testing.T) {
	// This legacy request now bypasses parsing and service execution.
	assertRetiredPricingRequest(t, "GET", "/api/v1/pricing/sync/preview?source=litellm", "", context.DeadlineExceeded)
}

func (s *pricingStub) UpdatePricing(_ context.Context, input servicedto.UpdatePricingInput) (*entities.ModelPriceSetting, error) {
	s.lastUpdate = &input
	return s.updated, s.err
}

func (s *pricingStub) UpdatePricingBatch(_ context.Context, input []servicedto.UpdatePricingInput) ([]entities.ModelPriceSetting, error) {
	s.lastBatch = input
	return s.batch, s.err
}

func (s *pricingStub) DeletePricing(_ context.Context, model string) error {
	s.deleted = model
	return s.err
}

func (s *pricingStub) ListPricingRules(context.Context, string) ([]servicedto.PricingRule, error) {
	return s.rules, s.err
}

func (s *pricingStub) ReplacePricingRules(_ context.Context, input servicedto.ReplacePricingRulesInput) ([]servicedto.PricingRule, error) {
	s.lastRules = &input
	return s.rules, s.err
}

func TestPricingRoutesReturnEmptyResponsesWithoutProvider(t *testing.T) {
	router := NewRouter(nil, nil, nil, nil, AuthConfig{}, nil, "")
	for path, want := range map[string]string{
		"/api/v1/models/used": `"models":[]`,
		"/api/v1/pricing":     `"pricing":[]`,
	} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, newPricingRequest(http.MethodGet, path, ""))
			if response.Code != http.StatusOK || !contains(response.Body.String(), want) {
				t.Fatalf("unexpected response: %d %s; want %s", response.Code, response.Body.String(), want)
			}
		})
	}
}

func TestPricingRoutesReturnConfiguredData(t *testing.T) {
	multiplier := 0.5
	router := NewRouter(nil, nil, nil, &pricingStub{
		usedModels: []string{"claude-sonnet"},
		pricing: []entities.ModelPriceSetting{{
			Model:                "claude-sonnet",
			PricingStyle:         "claude",
			PromptPricePer1M:     3,
			CompletionPricePer1M: 15,
			CacheReadPricePer1M:  0.3,
			CacheWritePricePer1M: 3.75,
			PriceMultiplier:      &multiplier,
		}},
	}, AuthConfig{}, nil, "")

	usedReq := newPricingRequest(http.MethodGet, "/api/v1/models/used", "")
	usedResp := httptest.NewRecorder()
	router.ServeHTTP(usedResp, usedReq)
	if usedResp.Code != http.StatusOK || !contains(usedResp.Body.String(), `claude-sonnet`) {
		t.Fatalf("unexpected used models response: %d %s", usedResp.Code, usedResp.Body.String())
	}

	pricingReq := newPricingRequest(http.MethodGet, "/api/v1/pricing", "")
	pricingResp := httptest.NewRecorder()
	router.ServeHTTP(pricingResp, pricingReq)
	if pricingResp.Code != http.StatusOK || !contains(pricingResp.Body.String(), `"prompt_price_per_1m":3`) || !contains(pricingResp.Body.String(), `"pricing_style":"claude"`) || !contains(pricingResp.Body.String(), `"cache_write_price_per_1m":3.75`) || !contains(pricingResp.Body.String(), `"price_multiplier":0.5`) {
		t.Fatalf("unexpected pricing response: %d %s", pricingResp.Code, pricingResp.Body.String())
	}
}

func TestPricingSyncPreviewRouteRemoved(t *testing.T) {
	// This legacy request now bypasses parsing and service execution.
	assertRetiredPricingRequest(t, "GET", "/api/v1/pricing/sync/preview", ``)
}

func TestPricingSyncPreviewRouteMapsErrorsRemoved(t *testing.T) {
	for name, err := range map[string]error{
		"deadline":        fmt.Errorf("fetch pricing catalog: %w", context.DeadlineExceeded),
		"network_timeout": fmt.Errorf("fetch pricing catalog: %w", pricingTimeoutError{}),
		"internal":        errors.New("decode pricing catalog: synthetic-private-source-key"),
	} {
		t.Run(name, func(t *testing.T) { assertRetiredPricingRequest(t, "GET", "/api/v1/pricing/sync/preview", "", err) })
	}
}

func TestUpdatePricingRoutePreservesOpenAICacheWritePriceRemoved(t *testing.T) {
	// This legacy request now bypasses parsing and service execution.
	assertRetiredPricingRequest(t, "PUT", "/api/v1/pricing/gpt-5.6-terra", `{"pricing_style":"openai","prompt_price_per_1m":2.5,"completion_price_per_1m":15,"cache_read_price_per_1m":0.25,"cache_write_price_per_1m":3.125}`)
}

func TestUpdatePricingRouteRejectsLegacyCachePriceFieldsRemoved(t *testing.T) {
	// This legacy request now bypasses parsing and service execution.
	assertRetiredPricingRequest(t, "PUT", "/api/v1/pricing/gpt-5.6-terra", `{"cache_price_per_1m":0.25,"cache_creation_price_per_1m":3.125}`)
}

func TestUpdatePricingRouteRemoved(t *testing.T) {
	// This legacy request now bypasses parsing and service execution.
	assertRetiredPricingRequest(t, "PUT", "/api/v1/pricing/claude-sonnet", `{"pricing_style":"claude","prompt_price_per_1m":3,"completion_price_per_1m":15,"cache_read_price_per_1m":0.3,"cache_write_price_per_1m":3.75,"price_multiplier":0.5}`)
}

func TestBatchUpdatePricingRouteUsesOneProviderCallRemoved(t *testing.T) {
	// This legacy request now bypasses parsing and service execution.
	assertRetiredPricingRequest(t, "PUT", "/api/v1/pricing/batch", `{"pricing":[{"model":"model-a","prompt_price_per_1m":2},{"model":"model-b","prompt_price_per_1m":3}]}`)
}

func TestBatchUpdatePricingRouteMapsInvalidInputToBadRequestRemoved(t *testing.T) {
	// This legacy request now bypasses parsing and service execution.
	assertRetiredPricingRequest(t, "PUT", "/api/v1/pricing/batch", `{"pricing":[{"model":"overflow-model","prompt_price_per_1m":1.7976931348623157e308}]}`, service.ErrInvalidPricingInput)
}

func TestUpdatePricingRouteAllowsZeroPriceMultiplierRemoved(t *testing.T) {
	// This legacy request now bypasses parsing and service execution.
	assertRetiredPricingRequest(t, "PUT", "/api/v1/pricing/free-model", `{"prompt_price_per_1m":3,"completion_price_per_1m":15,"cache_read_price_per_1m":0.3,"price_multiplier":0}`)
}

func TestUpdatePricingRouteMapsPriceMultiplierValidationToBadRequestRemoved(t *testing.T) {
	// This legacy request now bypasses parsing and service execution.
	assertRetiredPricingRequest(t, "PUT", "/api/v1/pricing/free-model", `{"prompt_price_per_1m":3,"completion_price_per_1m":15,"cache_read_price_per_1m":0.3,"price_multiplier":-1}`, service.ErrInvalidPricingInput)
}

func TestUpdatePricingRouteMapsInvalidSnapshotInputToBadRequestRemoved(t *testing.T) {
	// This legacy request now bypasses parsing and service execution.
	assertRetiredPricingRequest(t, "PUT", "/api/v1/pricing/overflow-model", `{"prompt_price_per_1m":1.7976931348623157e308}`, service.ErrInvalidPricingInput)
}

func TestUpdatePricingRouteAcceptsModelInBodyRemoved(t *testing.T) {
	// This legacy request now bypasses parsing and service execution.
	assertRetiredPricingRequest(t, "PUT", "/api/v1/pricing", `{"model":"openai/gpt-4.1","prompt_price_per_1m":3,"completion_price_per_1m":15,"cache_read_price_per_1m":0.3}`)
}

func TestDeletePricingRouteRemoved(t *testing.T) {
	// This legacy request now bypasses parsing and service execution.
	assertRetiredPricingRequest(t, "DELETE", "/api/v1/pricing?model=openai%2Fgpt-4.1", ``)
}
