package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	keeperapi "cpa-usage-keeper/internal/api"
)

func TestStoredConfigHTTPIntentAndRetiredModelRoute(t *testing.T) {
	db := openAPITestDatabase(t)
	stored := newAPIStoredConfigProvider(t, db)
	router := keeperapi.NewRouter(nil, nil, nil, nil, keeperapi.AuthConfig{}, nil, "", keeperapi.OptionalProviders{StoredPricing: stored})
	for _, route := range []struct{ method, path, body string }{
		{http.MethodPut, "/api/v1/pricing/stored/models", completePricingHTTPBody},
		{http.MethodDelete, "/api/v1/pricing/stored/models?model=unused", ""},
		{http.MethodPost, "/api/v1/pricing/stored/sync/apply", `{"source":"models-dev","items":[]}`},
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("intentless %s %s: %d %s", route.method, route.path, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, newPricingRequest(http.MethodPut, "/api/v1/pricing/models", completePricingHTTPBody))
	assertPricingRemoved(t, response)
	list, err := stored.ListPricingModels(context.Background())
	if err != nil || list.ConfigRevision != 0 || len(list.Models) != 0 {
		t.Fatalf("blocked routes mutated stored config: %+v %v", list, err)
	}
}
