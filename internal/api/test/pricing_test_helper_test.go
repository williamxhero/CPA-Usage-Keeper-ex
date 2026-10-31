package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	keeperapi "cpa-usage-keeper/internal/api"
	"cpa-usage-keeper/internal/poller"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/quota"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/service"
	"gorm.io/gorm"
)

func emptyPricingCatalogForTest() *pricing.Catalog {
	return pricing.NewCatalog(pricing.EmptySnapshot())
}

func newAPIPricingProvider(t *testing.T, db *gorm.DB, catalog *pricing.Catalog, fetchers ...service.ModelsFetcher) service.PricingProvider {
	t.Helper()
	recent, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	quotas := quota.NewServiceWithRegistry(db, quota.NewProviderRegistry(nil), pricing.NewCatalog(pricing.EmptySnapshot()))
	lifecycle, cancel := context.WithCancel(context.Background())
	provider := service.NewPricingServiceWithRecalculation(db, catalog, service.PricingRecalculationDependencies{
		LifecycleCtx: lifecycle,
		Sync:         service.NewSyncServiceWithOptions(db, service.SyncServiceOptions{PricingCatalog: catalog, RecentUsageEvents: recent}),
		Aggregation:  poller.NewUsageAggregationRunner(db),
		CostReadGate: service.NewCostReadGate(), Recent: recent, Quota: quotas, Now: time.Now,
	}, fetchers...)
	t.Cleanup(func() {
		cancel()
		provider.WaitPricingRecalculation()
		quotas.StopRefreshTasks()
		quotas.WaitRefreshTasks()
		recent.Close()
	})
	return provider
}

func newPricingRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	if method != http.MethodGet {
		request.Header.Set(requestIntentHeaderName, requestIntentHeaderValueFetch)
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

const removedPricingResponse = `{"code":"pricing_feature_removed","error":"pricing_feature_removed"}`

type removedPricingRoute struct{ method, path, body string }

// Each registered legacy method is explicit: neighboring matrix, official and
// directory routes must not be mistaken for retired routes by a wildcard.
func removedPricingRoutes() []removedPricingRoute {
	return []removedPricingRoute{
		{"GET", "/pricing/sync/preview", ""},
		{"PUT", "/pricing", `{"model":"base","prompt_price_per_1m":10}`},
		{"PUT", "/pricing/batch", `{"pricing":[{"model":"base","prompt_price_per_1m":10}]}`},
		{"PUT", "/pricing/base", `{"prompt_price_per_1m":10}`},
		{"DELETE", "/pricing?model=base", ""},
		{"GET", "/pricing/rules?model=base", ""},
		{"PUT", "/pricing/rules", `{"model":"base","rules":[]}`},
		{"POST", "/pricing/credentials", `{"directory_id":1}`},
		{"GET", "/pricing/credentials/cred_retained/default", ""},
		{"PUT", "/pricing/credentials/cred_retained/default", `{"multiplier":0.2}`},
		{"DELETE", "/pricing/credentials/cred_retained/default", ""},
		{"GET", "/pricing/credentials/cred_retained/models", ""},
		{"GET", "/pricing/credentials/cred_retained/model?model=base", ""},
		{"PUT", "/pricing/credentials/cred_retained/model?model=base", `{"multiplier":0.2}`},
		{"DELETE", "/pricing/credentials/cred_retained/model?model=base", ""},
		{"GET", "/pricing/channels", ""},
		{"POST", "/pricing/channels", `{"name":"New channel","member_subject_ids":[]}`},
		{"GET", "/pricing/channels/chan_retained", ""},
		{"PUT", "/pricing/channels/chan_retained", `{"name":"Changed channel","member_subject_ids":["cred_retained"]}`},
		{"DELETE", "/pricing/channels/chan_retained?confirm_dependencies=true", ""},
		{"GET", "/pricing/channels/chan_retained/default", ""},
		{"PUT", "/pricing/channels/chan_retained/default", `{"multiplier":0.2}`},
		{"DELETE", "/pricing/channels/chan_retained/default", ""},
		{"GET", "/pricing/channels/chan_retained/models", ""},
		{"GET", "/pricing/channels/chan_retained/model?model=base", ""},
		{"PUT", "/pricing/channels/chan_retained/model?model=base", `{"multiplier":0.2}`},
		{"DELETE", "/pricing/channels/chan_retained/model?model=base", ""},
		{"GET", "/pricing/identity-bindings", ""},
		{"POST", "/pricing/identity-bindings/migrate", `{"subject_id":"cred_retained","directory_ref":"selection_retained","snapshot_id":"retained","confirmed":true}`},
		{"PUT", "/pricing/identity-bindings/binding_cred_retained/correction", `{"expected_subject_id":"cred_retained","action":"unbind","snapshot_id":"retained","confirmed":true}`},
	}
}

func assertPricingRemoved(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusGone || strings.TrimSpace(response.Body.String()) != removedPricingResponse {
		t.Fatalf("retired route: status=%d body=%s; want 410 %s", response.Code, response.Body.String(), removedPricingResponse)
	}
	if response.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("retired response content type=%q", response.Header().Get("Content-Type"))
	}
}

func TestRemovedPricingRoutesAlwaysReturnFixedGoneWithoutProvider(t *testing.T) {
	router := keeperapi.NewRouter(nil, nil, nil, nil, keeperapi.AuthConfig{}, nil, "")
	for _, route := range removedPricingRoutes() {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, newPricingRequest(route.method, "/api/v1"+route.path, route.body))
			assertPricingRemoved(t, response)
		})
	}
}
