package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func TestStoredComparisonHTTPPublicIDsRedactionAndDistinctUnavailable(t *testing.T) {
	db := openAPITestDatabase(t)
	secrets := []string{"sk-private-key-one", "sk-private-key-two", "unregistered-secret-one", "unregistered-secret-two", "observed-index-one", "observed-index-two", "lookup-secret-value"}
	keys := []entities.CPAAPIKey{{ID: 42, APIKey: secrets[0], KeyAlias: secrets[1], DisplayKey: secrets[0]}, {ID: 43, APIKey: secrets[1], KeyAlias: secrets[0]}}
	if err := db.Create(&keys).Error; err != nil {
		t.Fatal(err)
	}
	identities := []entities.UsageIdentity{
		{ID: 51, AuthType: entities.UsageIdentityAuthTypeCredential, Identity: secrets[4], Name: secrets[0], LookupKey: secrets[6]},
		{ID: 52, AuthType: entities.UsageIdentityAuthTypeCredential, Identity: secrets[5], Name: secrets[6], LookupKey: secrets[6]},
		{ID: 53, AuthType: entities.UsageIdentityAuthTypeAIProvider, Identity: "provider-index-one", Name: "Duplicate label"},
		{ID: 54, AuthType: entities.UsageIdentityAuthTypeAIProvider, Identity: "provider-index-two", Name: "Duplicate label"},
	}
	if err := db.Create(&identities).Error; err != nil {
		t.Fatal(err)
	}
	yes := true
	fee := 1.25
	at := time.Now().Truncate(time.Hour).Add(time.Nanosecond)
	for i, key := range []string{secrets[0], secrets[1], secrets[2], secrets[3]} {
		kind := "oauth"
		if i > 1 {
			kind = "apikey"
		}
		if err := db.Create(&entities.UsageEvent{EventKey: fmt.Sprint(i), Timestamp: at.Add(time.Duration(i) * time.Nanosecond), Model: "safe-model", APIGroupKey: key, AuthIndex: identities[i].Identity, AuthType: kind, CostUSD: &fee, CostAvailable: &yes, TotalTokens: int64(i + 1)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	r, a, _ := comparisonHTTPRouter(t, db, emptyPricingCatalogForTest(), nil)
	path := "/api/v1/usage/stored/comparisons?range=24h"
	w := serveAPIGet(r, path, a)
	out := comparisonDecode(t, w)
	for _, secret := range append(secrets, "provider-index-one", "provider-index-two") {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("secret leaked", secret, w.Body.String())
		}
	}
	if len(out.APIKeys) != 4 || len(out.Credentials) != 2 || len(out.AuthFiles) != 2 || len(out.AIProviders) != 2 || out.Summary.RequestCount != 4 || out.Summary.StoredCostUSD != 5 {
		t.Fatal(out)
	}
	for _, tc := range []struct {
		items []servicedto.StoredUsageComparisonItem
		ids   []string
	}{{out.Credentials, []string{"51", "52"}}, {out.AuthFiles, []string{"51", "52"}}, {out.AIProviders, []string{"53", "54"}}} {
		for i, item := range tc.items {
			if item.ID == nil || *item.ID != tc.ids[i] || !item.IDAvailable || item.Requests != 1 {
				t.Fatal(tc)
			}
		}
	}
	if out.Credentials[0].Label != "Credential" || out.Credentials[1].Label != "Credential" || out.AIProviders[0].Label != "Duplicate label" || out.AIProviders[1].Label != "Duplicate label" {
		t.Fatal("unsafe label or label merge", out)
	}
	unavailable := 0
	known := map[string]bool{}
	for _, item := range out.APIKeys {
		if item.ID == nil {
			unavailable++
			if item.IDAvailable || item.Label != "API key" || item.Requests != 1 {
				t.Fatal(item)
			}
		} else {
			known[*item.ID] = true
		}
	}
	if unavailable != 2 || !known["42"] || !known["43"] {
		t.Fatal(out.APIKeys)
	}
	for i := 0; i < 4; i++ {
		next := comparisonDecode(t, serveAPIGet(r, path, a))
		// Each real HTTP request has its own rolling-range clock anchor. Compare
		// every payload field except those intentionally advancing range bounds.
		expected := out.StoredUsageComparisons
		expected.Range = next.Range
		if !reflect.DeepEqual(expected, next.StoredUsageComparisons) {
			t.Fatal("unstable ordering")
		}
	}
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"id": true, "id_available": true, "label": true, "token_buckets": true, "requests": true, "failures": true, "input_tokens": true, "output_tokens": true, "cache_read_tokens": true, "cache_creation_tokens": true, "reasoning_tokens": true, "total_tokens": true, "stored_cost_usd": true, "stored_cost_available": true}
	for _, dimension := range []string{"models", "api_keys", "auth_files", "credentials", "ai_providers"} {
		for _, item := range raw[dimension].([]any) {
			for field := range item.(map[string]any) {
				if !allowed[field] {
					t.Fatal("not allowlisted", field)
				}
			}
		}
	}
}

func TestStoredComparisonHTTPAmbiguousTypedIdentityAndUnattributedRollup(t *testing.T) {
	db := openAPITestDatabase(t)
	index := "shared-observed-index"
	for _, identity := range []entities.UsageIdentity{{ID: 71, AuthType: entities.UsageIdentityAuthTypeCredential, Identity: index, Name: "Same label"}, {ID: 72, AuthType: entities.UsageIdentityAuthTypeAIProvider, Identity: index, Name: "Same label"}} {
		if err := db.Create(&identity).Error; err != nil {
			t.Fatal(err)
		}
	}
	yes := true
	fee := 2.0
	zero := int64(0)
	at := time.Now().Truncate(time.Hour).Add(time.Nanosecond)
	for i, kind := range []string{"oauth", "apikey", "OAUTH", "", "oauth "} {
		if err := db.Create(&entities.UsageEvent{EventKey: fmt.Sprint(i), Timestamp: at, Model: "m", AuthIndex: index, AuthType: kind, CostUSD: &fee, CostAvailable: &yes}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: at.Truncate(time.Hour).Add(-time.Hour), Model: "m", AuthIndex: index, RequestCount: 3, CostUSD: &fee, UnavailableCostCount: &zero}).Error; err != nil {
		t.Fatal(err)
	}
	r, a, _ := comparisonHTTPRouter(t, db, emptyPricingCatalogForTest(), nil)
	w := serveAPIGet(r, "/api/v1/usage/stored/comparisons?range=24h", a)
	out := comparisonDecode(t, w)
	if out.Summary.RequestCount != 8 || out.Summary.StoredCostUSD != 12 || out.Coverage.IdentityAttributionComplete || out.Coverage.UnattributedRequests != 6 || len(out.Credentials) != 1 || len(out.AIProviders) != 1 || out.Credentials[0].Requests != 1 || out.AIProviders[0].Requests != 1 || *out.Credentials[0].ID != "71" || *out.AIProviders[0].ID != "72" {
		t.Fatal(out)
	}
	if strings.Contains(w.Body.String(), index) {
		t.Fatal("raw index leaked")
	}
}

func TestStoredComparisonHTTPQueryBoundsAndUnconfigured(t *testing.T) {
	db := openAPITestDatabase(t)
	unconfigured, a, _ := storedHTTPRouter(t, db, emptyPricingCatalogForTest(), nil)
	if w := serveAPIGet(unconfigured, "/api/v1/usage/stored/comparisons?range=24h", a); w.Code != 503 {
		t.Fatal(w.Code, w.Body.String())
	}
	r, a, _ := comparisonHTTPRouter(t, db, emptyPricingCatalogForTest(), nil)
	for _, q := range []string{
		"range=24h&range=24h", "range=24h&api_key_id=42&api_key_id=43", "range=24h&foo=", "range=24h&unit=hour", "range=custom&unit=hour&start=bad&end=bad", "range=100000d", "range=custom&unit=minute&start=2026-10-30&end=2026-10-30", "range=custom&unit=day&start=2026-10-30&end=2026-10-29", "range=custom&unit=hour&start=2026-10-28T00:00:00Z&end=2026-10-30T00:00:00Z",
	} {
		w := serveAPIGet(r, "/api/v1/usage/stored/comparisons?"+q, a)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body.String())
		}
	}
	// Repeated empty values are unsupported, not silently collapsed.
	q := url.Values{"range": {"24h"}, "start": {"", ""}}
	if w := serveAPIGet(r, "/api/v1/usage/stored/comparisons?"+q.Encode(), a); w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestStoredComparisonHTTPModelIDsPreserveExactSelectionIdentity(t *testing.T) {
	anchor := time.Now().Truncate(time.Hour)
	for _, source := range []struct {
		name string
		at   time.Time
	}{
		{"current_raw_hour", anchor.Add(time.Nanosecond)},
		{"completed_rollup_hour", anchor.Add(-time.Minute)},
	} {
		t.Run(source.name, func(t *testing.T) {
			db := openAPITestDatabase(t)
			yes, fee := true, 1.0
			for i, model := range []string{"safe-model", " safe-model", "safe-model ", "sk-synthetic-model-one", "sk-synthetic-model-two"} {
				row := entities.UsageEvent{EventKey: fmt.Sprint(i), Timestamp: source.at, Model: model, CostUSD: &fee, CostAvailable: &yes}
				if err := db.Create(&row).Error; err != nil {
					t.Fatal(err)
				}
			}
			// A complete historical hour is read from rollups, not hot raw
			// rows. Prepare that real source so identity checks do not depend
			// on whether wall time is in the first minute of an hour.
			if err := repository.AggregateUsageOverviewStats(context.Background(), db, time.Now()); err != nil {
				t.Fatal(err)
			}
			r, a, _ := comparisonHTTPRouter(t, db, emptyPricingCatalogForTest(), nil)
			out := comparisonDecode(t, serveAPIGet(r, "/api/v1/usage/stored/comparisons?range=24h", a))
			if len(out.Models) != 3 {
				t.Fatal("distinct stored model identities merged", out.Models)
			}
			selectable, unavailable := 0, 0
			for _, item := range out.Models {
				if item.Requests != 1 && item.Requests != 3 {
					t.Fatal(item)
				}
				if item.ID == nil {
					unavailable++
					if item.IDAvailable || item.Label != "Model" || item.Requests != 1 {
						t.Fatal("unsafe model advertised as selectable", item)
					}
				} else {
					selectable++
					if *item.ID != "safe-model" || !item.IDAvailable || item.Label != "safe-model" || item.Requests != 3 {
						t.Fatal(item)
					}
				}
			}
			if selectable != 1 || unavailable != 2 {
				t.Fatal("sanitized labels became colliding model selectors", out.Models)
			}
		})
	}
}
