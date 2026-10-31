package test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"cpa-usage-keeper/internal/api"
	repodto "cpa-usage-keeper/internal/repository/dto"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func TestUsageIdentityAliasCompatibilityPreservesCanonicalScope(t *testing.T) {
	for _, endpoint := range []struct{ path, canonical, legacy, identityField string }{
		{"/usage/overview/comparisons", "credentials", "auth_files", "key"},
		{"/usage/analysis", "credentials_composition", "auth_files_composition", "label"},
	} {
		for _, mode := range []string{"legacy-only", "canonical", "explicit-empty"} {
			t.Run(endpoint.canonical+"/"+mode, func(t *testing.T) {
				stub := newDualCostsMappingStub()
				wantKey, wantCount := "item", 1
				switch mode {
				case "canonical":
					wantKey = "canonical"
					stub.overview.Comparisons.Credentials = map[string]*repodto.UsageComparisonItemRecord{"canonical": {Key: "canonical", Requests: 9}}
					stub.analysis.CredentialsComposition = []servicedto.AnalysisCompositionItem{{Key: "canonical", Requests: 9}}
				case "explicit-empty":
					wantCount = 0
					stub.overview.Comparisons.Credentials = map[string]*repodto.UsageComparisonItemRecord{}
					stub.analysis.CredentialsComposition = []servicedto.AnalysisCompositionItem{}
				}
				router := api.NewRouter(nil, nil, stub, nil, api.AuthConfig{}, nil, "")
				response := serveAPIGet(router, "/api/v1"+endpoint.path+"?range=24h")
				if response.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				var canonical, legacy []map[string]any
				if value := payload[endpoint.canonical]; len(value) > 0 {
					if err := json.Unmarshal(value, &canonical); err != nil {
						t.Fatal(err)
					}
				}
				if value := payload[endpoint.legacy]; len(value) > 0 {
					if err := json.Unmarshal(value, &legacy); err != nil {
						t.Fatal(err)
					}
				}
				if len(canonical) != wantCount || !reflect.DeepEqual(canonical, legacy) {
					t.Fatalf("alias lost data or escaped canonical scope: canonical=%s legacy=%s", payload[endpoint.canonical], payload[endpoint.legacy])
				}
				if wantCount > 0 && canonical[0][endpoint.identityField] != wantKey {
					t.Fatalf("canonical identity replaced: %+v", canonical)
				}
			})
		}
	}
}
