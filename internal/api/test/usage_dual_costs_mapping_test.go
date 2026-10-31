package test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	. "cpa-usage-keeper/internal/api"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	repodto "cpa-usage-keeper/internal/repository/dto"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

const mappingSnapshotID = "snapshot-current-without-overrides"

type dualCostsMappingStub struct {
	*usageFilterStub
	analysis *servicedto.AnalysisSnapshot
	page     *servicedto.UsageEventsPage
}

func (s *dualCostsMappingStub) GetUsageOverviewComparisons(context.Context, servicedto.UsageFilter) (*servicedto.UsageOverviewSnapshot, error) {
	return s.overview, nil
}

func (s *dualCostsMappingStub) GetAnalysis(context.Context, servicedto.UsageFilter) (*servicedto.AnalysisSnapshot, error) {
	return s.analysis, nil
}

func (s *dualCostsMappingStub) ListUsageEvents(context.Context, servicedto.UsageFilter) (*servicedto.UsageEventsPage, error) {
	return s.page, nil
}

func (s *dualCostsMappingStub) StreamUsageEvents(_ context.Context, _ servicedto.UsageFilter, emit func(servicedto.UsageEventRecord) error) error {
	for _, event := range s.page.Events {
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}

func mappingDualCosts() pricing.DualCosts {
	configured, reference := 0.123456789012345, 0.987654321098765
	return pricing.DualCosts{
		Configured: pricing.PriceEstimate{TotalCostUSD: &configured, UncachedInputCostUSD: configured, Complete: true, HasKnown: true, Status: "complete"},
		Reference:  pricing.PriceEstimate{TotalCostUSD: &reference, OutputCostUSD: reference, HasKnown: true, Status: "partial", UnavailableReason: "missing_price"},
	}
}

func mappingUnavailableCosts() pricing.DualCosts {
	estimate := pricing.PriceEstimate{Status: "unavailable", UnavailableReason: "missing_price"}
	return pricing.DualCosts{Configured: estimate, Reference: estimate}
}

func newDualCostsMappingStub() *dualCostsMappingStub {
	d := mappingDualCosts()
	comparison := map[string]*repodto.UsageComparisonItemRecord{"item": {Key: "item", DualCosts: d, CostUSD: 91, CostAvailable: true}}
	composition := []servicedto.AnalysisCompositionItem{{Key: "item", DualCosts: d, CostUSD: 91, CostAvailable: true}}
	topItems := []servicedto.RealtimeUsageTopItem{{Key: "item", DualCosts: d, CostUSD: new(float64(91))}}
	return &dualCostsMappingStub{
		usageFilterStub: &usageFilterStub{
			overview: &servicedto.UsageOverviewSnapshot{
				PricingSnapshotID: mappingSnapshotID,
				Summary:           servicedto.UsageOverviewSummary{PricingSnapshotID: mappingSnapshotID, DualCosts: d, DailyAverageDualCosts: &d, TotalCost: 91, CostAvailable: true},
				Series:            servicedto.UsageOverviewSeries{Buckets: []string{"one", "empty"}, Cost: []float64{91, 0}, DualCosts: []pricing.DualCosts{d, {}}},
				Comparisons:       &repodto.UsageOverviewComparisonsRecord{PricingSnapshotID: mappingSnapshotID, Models: comparison, APIKeys: comparison, AuthFiles: comparison, AIProviders: comparison, Channels: comparison},
			},
			realtime: &servicedto.UsageOverviewRealtime{
				PricingSnapshotID: mappingSnapshotID,
				Insights:          &repodto.RealtimeInsightsRecord{Summary: repodto.RealtimeWindowSummaryRecord{DualCosts: d, CostUSD: 91, CostAvailable: true}},
				TokenVelocity:     []servicedto.RealtimeTokenVelocityPoint{{DualCosts: d, CostUSD: new(float64(91))}},
				CurrentUsage:      servicedto.RealtimeCurrentUsage{Models: topItems, APIKeys: topItems, AuthFiles: topItems, AIProviders: topItems},
			},
		},
		analysis: &servicedto.AnalysisSnapshot{
			PricingSnapshotID: mappingSnapshotID,
			Granularity:       servicedto.AnalysisGranularityHourly,
			TokenUsage:        []servicedto.AnalysisTokenUsageBucket{{DualCosts: d, CostUSD: 91, CostAvailable: true}},
			APIKeyComposition: composition, ModelComposition: composition, AuthFilesComposition: composition, AIProviderComposition: composition,
			Heatmap:         []servicedto.AnalysisHeatmapCell{{APIKey: "item", Model: "model", DualCosts: d, CostUSD: 91, CostAvailable: true}},
			CostBreakdown:   servicedto.AnalysisCostBreakdown{PricingSnapshotID: mappingSnapshotID, DualCosts: d, TotalCostUSD: 91, CostAvailable: true},
			ModelEfficiency: []servicedto.AnalysisModelEfficiencyItem{{DualCosts: d, CostUSD: 91, CostAvailable: true}},
		},
		page: &servicedto.UsageEventsPage{PricingSnapshotID: mappingSnapshotID, Events: []servicedto.UsageEventRecord{{
			ID: 1, Timestamp: time.Now(), PricingSnapshotID: mappingSnapshotID, DualCosts: d, CostUSD: 91, CostAvailable: true,
			PricingSelection: &pricing.CostSelection{Scope: "legacy", Mode: "legacy", Legacy: true, SnapshotID: mappingSnapshotID, DualCosts: d, MatchedRules: []pricing.MatchedRule{}},
		}}},
	}
}

func mappingJSONValue(t *testing.T, value any, path ...any) any {
	t.Helper()
	for _, part := range path {
		switch key := part.(type) {
		case string:
			object, ok := value.(map[string]any)
			if !ok {
				t.Fatalf("expected object at %v, got %T", path, value)
			}
			value, ok = object[key]
			if !ok {
				t.Fatalf("missing %s at %v", key, path)
			}
		case int:
			array, ok := value.([]any)
			if !ok || key >= len(array) {
				t.Fatalf("missing array element %d at %v", key, path)
			}
			value = array[key]
		}
	}
	return value
}

func assertMappingDualCosts(t *testing.T, value any, want pricing.DualCosts) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got pricing.DualCosts
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want.Normalized()) {
		t.Fatalf("dual costs changed: got %s, want %+v", data, want.Normalized())
	}
	for _, side := range []string{"configured", "reference"} {
		object := mappingJSONValue(t, value, side).(map[string]any)
		assertAllowedJSONKeys(t, object, "price estimate", string(data), "total_cost_usd", "uncached_input_cost_usd", "output_cost_usd", "cache_read_cost_usd", "cache_write_cost_usd", "complete", "has_known", "status", "unavailable_reason")
		for _, key := range []string{"total_cost_usd", "uncached_input_cost_usd", "output_cost_usd", "cache_read_cost_usd", "cache_write_cost_usd", "complete", "has_known", "status"} {
			mappingJSONValue(t, object, key)
		}
	}
}

func TestDualCostsAPIMappingPreservesAuthoritativeValuesAndLegacyFields(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		costPaths  [][]any
		legacyPath []any
	}{
		{"overview", "/usage/overview", [][]any{{"summary", "dual_costs"}, {"summary", "daily_average_dual_costs"}, {"series", "dual_costs", 0}}, []any{"summary", "total_cost"}},
		{"comparisons", "/usage/overview/comparisons", [][]any{{"models", 0, "dual_costs"}, {"api_keys", 0, "dual_costs"}, {"auth_files", 0, "dual_costs"}, {"ai_providers", 0, "dual_costs"}, {"channels", 0, "dual_costs"}}, []any{"models", 0, "cost"}},
		{"analysis", "/usage/analysis", [][]any{{"token_usage", 0, "dual_costs"}, {"api_key_composition", 0, "dual_costs"}, {"model_composition", 0, "dual_costs"}, {"auth_files_composition", 0, "dual_costs"}, {"ai_provider_composition", 0, "dual_costs"}, {"heatmap", "cells", 0, "dual_costs"}, {"cost_breakdown", "dual_costs"}, {"model_efficiency", 0, "dual_costs"}}, []any{"cost_breakdown", "total_cost_usd"}},
		{"realtime", "/usage/overview/realtime", [][]any{{"insights", "summary", "dual_costs"}, {"token_velocity", 0, "dual_costs"}, {"current_usage", "models", 0, "dual_costs"}, {"current_usage", "api_keys", 0, "dual_costs"}, {"current_usage", "auth_files", 0, "dual_costs"}, {"current_usage", "ai_providers", 0, "dual_costs"}}, []any{"insights", "summary", "cost"}},
		{"events", "/usage/events", [][]any{{"events", 0, "dual_costs"}, {"events", 0, "pricing_selection", "dual_costs"}}, []any{"events", 0, "cost_usd"}},
		{"json export", "/usage/events/export?format=json", [][]any{{"events", 0, "dual_costs"}, {"events", 0, "pricing_selection", "dual_costs"}}, []any{"events", 0, "cost_usd"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := NewRouter(nil, nil, newDualCostsMappingStub(), nil, AuthConfig{}, nil, "")
			path := "/api/v1" + tc.path
			if strings.Contains(path, "?") {
				path += "&range=24h"
			} else {
				path += "?range=24h"
			}
			response := serveAPIGet(router, path)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var payload any
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			for _, path := range tc.costPaths {
				assertMappingDualCosts(t, mappingJSONValue(t, payload, path...), mappingDualCosts())
			}
			if got := mappingJSONValue(t, payload, tc.legacyPath...); got != float64(91) {
				t.Fatalf("legacy cost changed: %v", got)
			}
			snapshotPath := []any{"pricing_snapshot_id"}
			if tc.name == "json export" {
				snapshotPath = []any{"events", 0, "pricing_snapshot_id"}
			}
			if got := mappingJSONValue(t, payload, snapshotPath...); got != mappingSnapshotID {
				t.Fatalf("snapshot id lost: %v", got)
			}
			if tc.name == "overview" {
				assertMappingDualCosts(t, mappingJSONValue(t, payload, "series", "dual_costs", 1), pricing.DualCosts{})
			}
		})
	}
}

func TestDualCostsHeatmapTotalsMergeCoverageWithoutUsingCounts(t *testing.T) {
	stub := newDualCostsMappingStub()
	stub.analysis.Heatmap = append(stub.analysis.Heatmap, servicedto.AnalysisHeatmapCell{APIKey: "item", Model: "model", DualCosts: mappingUnavailableCosts()})
	key := entities.CPAAPIKey{ID: 42, APIKey: "item", KeyAlias: "Public Key"}
	router := NewRouter(nil, nil, stub, nil, AuthConfig{}, nil, "", OptionalProviders{CPAAPIKeys: usageAnalysisAPIKeyStub{rows: []entities.CPAAPIKey{key}}})
	response := serveAPIGet(router, "/api/v1/usage/analysis?range=24h")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	want := mappingDualCosts()
	want.Merge(mappingUnavailableCosts())
	assertMappingDualCosts(t, mappingJSONValue(t, payload, "heatmap", "row_dual_costs", "42"), want)
	assertMappingDualCosts(t, mappingJSONValue(t, payload, "heatmap", "column_dual_costs", "model"), want)
	assertMappingDualCosts(t, mappingJSONValue(t, payload, "heatmap", "cells", 1, "dual_costs"), mappingUnavailableCosts())
	if want.Configured.Status != "partial" || want.Reference.Status != "partial" {
		t.Fatalf("expected partial authoritative totals, got %+v", want)
	}
}

func TestDualCostsCSVExportAppendsFieldsAndPreservesNullVersusKnownZero(t *testing.T) {
	stub := newDualCostsMappingStub()
	stub.page.Events[0].EffectiveServiceTier = "CustomTier"
	stub.page.Events = append(stub.page.Events, servicedto.UsageEventRecord{DualCosts: mappingUnavailableCosts()}, servicedto.UsageEventRecord{})
	router := NewRouter(nil, nil, stub, nil, AuthConfig{}, nil, "")
	response := serveAPIGet(router, "/api/v1/usage/events/export?format=csv&range=24h")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	records, err := csv.NewReader(strings.NewReader(response.Body.String())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Split("id,timestamp,api_key,cpa_api_key_id,source,source_type,auth_index,is_identity_deleted,model,model_alias,response_model,reasoning_effort,service_tier,response_service_tier,executor_type,result,status_code,stream,endpoint,ttft_ms,latency_ms,speed_tps,client_ip,x_forwarded_for,user_agent,input_tokens,output_tokens,reasoning_tokens,cache_read_tokens,cache_creation_tokens,cache_read_rate,total_tokens,cost_usd", ",")
	oldHeader := append(legacy, strings.Split("configured_total_cost_usd,configured_status,configured_uncached_input_cost_usd,configured_output_cost_usd,configured_cache_read_cost_usd,configured_cache_write_cost_usd,reference_total_cost_usd,reference_status,reference_uncached_input_cost_usd,reference_output_cost_usd,reference_cache_read_cost_usd,reference_cache_write_cost_usd,pricing_snapshot_id,pricing_selection,cost_available", ",")...)
	if len(records) != 4 || len(records[0]) != len(oldHeader)+1 || !reflect.DeepEqual(records[0][:len(oldHeader)], oldHeader) || records[0][len(oldHeader)] != "effective_service_tier" {
		t.Fatalf("CSV legacy prefix changed or upstream tier column was not additive: %+v", records)
	}
	indexes := map[string]int{}
	for index, column := range records[0] {
		indexes[column] = index
	}
	for column, want := range map[string]string{
		"cost_usd": "91", "configured_total_cost_usd": "0.123456789012345", "configured_status": "complete",
		"configured_uncached_input_cost_usd": "0.123456789012345", "configured_output_cost_usd": "0", "configured_cache_read_cost_usd": "0", "configured_cache_write_cost_usd": "0",
		"reference_total_cost_usd": "0.987654321098765", "reference_status": "partial", "reference_output_cost_usd": "0.987654321098765",
		"reference_uncached_input_cost_usd": "0", "reference_cache_read_cost_usd": "0", "reference_cache_write_cost_usd": "0",
		"pricing_snapshot_id": mappingSnapshotID, "cost_available": "true", "effective_service_tier": "CustomTier",
	} {
		index, ok := indexes[column]
		if !ok || records[1][index] != want {
			t.Fatalf("column %s lost authoritative value %s", column, want)
		}
	}
	var selection pricing.CostSelection
	if err := json.Unmarshal([]byte(records[1][indexes["pricing_selection"]]), &selection); err != nil || !selection.Legacy || selection.SnapshotID != mappingSnapshotID {
		t.Fatalf("legacy explanation lost: %+v, %v", selection, err)
	}
	for _, side := range []string{"configured", "reference"} {
		if records[2][indexes[side+"_total_cost_usd"]] != "" || records[2][indexes[side+"_status"]] != "unavailable" {
			t.Fatalf("unavailable total must stay blank: %v", records[2])
		}
		if records[3][indexes[side+"_total_cost_usd"]] != "0" || records[3][indexes[side+"_status"]] != "complete" {
			t.Fatalf("known zero must stay zero: %v", records[3])
		}
	}
}

func TestDualCostsEmptyResponsesRetainCurrentSnapshotID(t *testing.T) {
	stub := newDualCostsMappingStub()
	stub.overview = &servicedto.UsageOverviewSnapshot{PricingSnapshotID: mappingSnapshotID, Summary: servicedto.UsageOverviewSummary{PricingSnapshotID: mappingSnapshotID}}
	stub.analysis = &servicedto.AnalysisSnapshot{PricingSnapshotID: mappingSnapshotID, CostBreakdown: servicedto.AnalysisCostBreakdown{PricingSnapshotID: mappingSnapshotID}}
	stub.realtime = &servicedto.UsageOverviewRealtime{PricingSnapshotID: mappingSnapshotID, Insights: &repodto.RealtimeInsightsRecord{}}
	stub.page = &servicedto.UsageEventsPage{PricingSnapshotID: mappingSnapshotID}
	for _, tc := range []struct {
		path     string
		costPath []any
	}{
		{"/usage/overview?range=24h", []any{"summary", "dual_costs"}},
		{"/usage/overview/comparisons?range=24h", nil},
		{"/usage/analysis?range=24h", []any{"cost_breakdown", "dual_costs"}},
		{"/usage/overview/realtime?window=15m", []any{"insights", "summary", "dual_costs"}},
		{"/usage/events?range=24h", nil},
	} {
		t.Run(tc.path, func(t *testing.T) {
			router := NewRouter(nil, nil, stub, nil, AuthConfig{}, nil, "")
			response := serveAPIGet(router, "/api/v1"+tc.path)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var payload any
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if got := mappingJSONValue(t, payload, "pricing_snapshot_id"); got != mappingSnapshotID {
				t.Fatalf("empty response lost current snapshot id: %v", got)
			}
			if tc.costPath != nil {
				assertMappingDualCosts(t, mappingJSONValue(t, payload, tc.costPath...), pricing.DualCosts{})
			}
		})
	}
}

func TestDualCostsViewerResponsesKeepAuthoritativeCostsAndRestrictedDimensionsHidden(t *testing.T) {
	for _, tc := range []struct {
		path       string
		costPath   []any
		restricted []string
	}{
		{"/key-overview?range=24h&api_key_id=99", []any{"summary", "dual_costs"}, []string{}},
		{"/key-overview/realtime?window=15m&api_key_id=99", []any{"current_usage", "models", 0, "dual_costs"}, []string{"api_keys", "auth_files", "ai_providers"}},
		{"/key-overview/comparisons?range=24h&api_key_id=99", []any{"models", 0, "dual_costs"}, []string{"auth_files", "ai_providers"}},
		{"/key-analysis?range=24h&api_key_id=99", []any{"cost_breakdown", "dual_costs"}, []string{}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			stub := newDualCostsMappingStub()
			router, cookie := newUsageViewerRouter(t, stub)
			response := serveAPIGet(router, "/api/v1"+tc.path, cookie)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var payload any
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			assertMappingDualCosts(t, mappingJSONValue(t, payload, tc.costPath...), mappingDualCosts())
			if got := mappingJSONValue(t, payload, "pricing_snapshot_id"); got != mappingSnapshotID {
				t.Fatalf("viewer lost snapshot id: %v", got)
			}
			for _, field := range tc.restricted {
				if strings.Contains(response.Body.String(), `"`+field+`":`) {
					t.Fatalf("viewer received restricted dimension %s", field)
				}
			}
			if strings.Contains(tc.path, "/key-analysis") {
				for _, field := range []string{"auth_files_composition", "ai_provider_composition"} {
					if items := mappingJSONValue(t, payload, field).([]any); len(items) != 0 {
						t.Fatalf("viewer received restricted composition %s", field)
					}
				}
			}
		})
	}
}
