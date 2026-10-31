package test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	. "cpa-usage-keeper/internal/api"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/helper"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

type usageAnalysisAPIKeyStub struct {
	service.CPAAPIKeyProvider
	rows []entities.CPAAPIKey
}

func (s usageAnalysisAPIKeyStub) ListCPAAPIKeys(context.Context) ([]entities.CPAAPIKey, error) {
	return s.rows, nil
}

func TestUsageAnalysisReturnsAggregatedRows(t *testing.T) {
	bucket := time.Date(2026, 4, 22, 10, 0, 0, 0, time.Local)
	provider := &analysisSplitStub{analysis: &servicedto.AnalysisSnapshot{
		Granularity: servicedto.AnalysisGranularityHourly,
		TokenUsage: []servicedto.AnalysisTokenUsageBucket{{
			Bucket:              bucket,
			InputTokens:         30,
			OutputTokens:        9,
			CacheReadTokens:     1,
			CacheCreationTokens: 2,
			ReasoningTokens:     2,
			TotalTokens:         42,
			Requests:            2,
			CostUSD:             1.23,
			CostAvailable:       true,
		}},
		APIKeyComposition: []servicedto.AnalysisCompositionItem{{
			Key:           "sk-provider123456",
			TotalTokens:   42,
			Requests:      2,
			CostUSD:       1.23,
			CostAvailable: true,
		}},
		ModelComposition: []servicedto.AnalysisCompositionItem{{
			Key:           "claude-sonnet",
			TotalTokens:   42,
			Requests:      2,
			CostUSD:       1.23,
			CostAvailable: true,
		}},
		CredentialsComposition: []servicedto.AnalysisCompositionItem{{
			Key:           "credential-1",
			Label:         "Credential One",
			TotalTokens:   30,
			Requests:      1,
			CostUSD:       0.8,
			CostAvailable: true,
		}},
		AIProviderComposition: []servicedto.AnalysisCompositionItem{{
			Key:           "provider-1",
			Label:         "Provider One",
			TotalTokens:   12,
			Requests:      1,
			CostUSD:       0.43,
			CostAvailable: true,
		}},
		Heatmap: []servicedto.AnalysisHeatmapCell{{
			APIKey:              "sk-provider123456",
			Model:               "claude-sonnet",
			InputTokens:         30,
			OutputTokens:        9,
			CacheReadTokens:     1,
			CacheCreationTokens: 2,
			ReasoningTokens:     2,
			TotalTokens:         42,
			Requests:            2,
			CostUSD:             1.23,
			CostAvailable:       true,
		}},
		CostSummary: servicedto.AnalysisCostSummary{
			TotalCostUSD:  1.23,
			CostAvailable: true,
		},
		CostBreakdown: servicedto.AnalysisCostBreakdown{
			TotalCostUSD:  1.23,
			CostAvailable: true,
		},
		ModelEfficiency: []servicedto.AnalysisModelEfficiencyItem{{
			Model:                  "claude-sonnet",
			Requests:               2,
			InputTokens:            30,
			OutputTokens:           9,
			CacheReadTokens:        1,
			CacheCreationTokens:    2,
			ReasoningTokens:        2,
			TotalTokens:            42,
			CostUSD:                1.23,
			CostAvailable:          true,
			CostPerRequestUSD:      0.615,
			OutputTokensPerRequest: 5.5,
			CacheReadRate:          1.0 / 30.0,
		}},
	}}
	router := NewRouter(nil, nil, provider, nil, AuthConfig{}, nil, "")
	resp := serveAPIGet(router, "/api/v1/usage/analysis?range=24h")

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.Code)
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"granularity":"hourly"`) || !strings.Contains(body, `"token_usage":[`) || !strings.Contains(body, `"heatmap":`) {
		t.Fatalf("unexpected response body: %s", body)
	}
	if !strings.Contains(body, `"cost_usd":1.23`) || !strings.Contains(body, `"cost_available":true`) {
		t.Fatalf("expected token/composition cost fields in response body: %s", body)
	}
	if !strings.Contains(body, `"api_key_composition":[`) || !strings.Contains(body, `"model_composition":[`) || !strings.Contains(body, `"credentials_composition":[`) || !strings.Contains(body, `"ai_provider_composition":[`) {
		t.Fatalf("expected composition payloads in response body: %s", body)
	}
	if !strings.Contains(body, `"key":"sk-*********123456"`) || !strings.Contains(body, `"label":"sk-*********123456"`) {
		t.Fatalf("expected redacted api key composition in response body: %s", body)
	}
	if !strings.Contains(body, `"key":"cre*********tial-1"`) || !strings.Contains(body, `"label":"Credential One"`) || !strings.Contains(body, `"percent":100`) {
		t.Fatalf("expected credential composition in response body: %s", body)
	}
	if !strings.Contains(body, `"key":"pro*********ider-1"`) || !strings.Contains(body, `"label":"Provider One"`) {
		t.Fatalf("expected ai provider composition in response body: %s", body)
	}
	if !strings.Contains(body, `"model":"claude-sonnet"`) || !strings.Contains(body, `"intensity":1`) || !strings.Contains(body, `"input_tokens":30`) || !strings.Contains(body, `"reasoning_tokens":2`) {
		t.Fatalf("expected heatmap cell in response body: %s", body)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode Analysis response: %v", err)
	}
	var costSummary map[string]json.RawMessage
	if err := json.Unmarshal(payload["cost_breakdown"], &costSummary); err != nil {
		t.Fatalf("decode current cost breakdown: %v", err)
	}
	var totalCost float64
	if err := json.Unmarshal(costSummary["total_cost_usd"], &totalCost); err != nil {
		t.Fatalf("decode total cost: %v", err)
	}
	var costAvailable bool
	if err := json.Unmarshal(costSummary["cost_available"], &costAvailable); err != nil {
		t.Fatalf("decode cost availability: %v", err)
	}
	if !overviewAPICostClose(totalCost, 1.23) || !costAvailable || costSummary["dual_costs"] == nil || costSummary["pricing_snapshot_id"] == nil {
		t.Fatalf("unexpected current cost breakdown: %s", payload["cost_breakdown"])
	}
	if _, exists := payload["cost_summary"]; exists {
		t.Fatalf("stored summary leaked into current Analysis response: %s", resp.Body.String())
	}
	if !strings.Contains(body, `"model_efficiency":`) || !strings.Contains(body, `"cost_per_request_usd":0.615`) || !strings.Contains(body, `"output_tokens_per_request":5.5`) || !strings.Contains(body, `"cache_read_rate":0.03333333333333333`) {
		t.Fatalf("expected model efficiency in response body: %s", body)
	}
	if strings.Contains(body, `"latency_diagnostics":`) {
		t.Fatalf("expected latency diagnostics to use the independent endpoint, got %s", body)
	}
	if provider.analysisCalls != 1 {
		t.Fatalf("expected GetAnalysis to be called once, got %d", provider.analysisCalls)
	}
	if provider.analysisFilter.Range != "24h" {
		t.Fatalf("expected range to be passed through, got %+v", provider.analysisFilter)
	}
	if provider.analysisFilter.StartTime == nil || provider.analysisFilter.EndTime == nil {
		t.Fatalf("expected resolved time bounds in filter, got %+v", provider.analysisFilter)
	}

	// Preserve the original historical summary and amounts at the real stored
	// service seam, not by changing ordinary HTTP back to stored valuation.
	db := openAPITestDatabase(t)
	if err := db.Create(&entities.CPAAPIKey{ID: 42, APIKey: "sk-provider123456"}).Error; err != nil {
		t.Fatal(err)
	}
	identities := []entities.UsageIdentity{
		{Name: "Credential One", AuthType: entities.UsageIdentityAuthTypeCredential, Identity: "credential-1"},
		{Name: "Provider One", AuthType: entities.UsageIdentityAuthTypeAIProvider, Identity: "provider-1"},
	}
	if err := db.Create(&identities).Error; err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Hour).Add(-2 * time.Hour)
	credentialCost, providerCost, unavailable := 0.8, 0.43, int64(0)
	rows := []entities.UsageOverviewHourlyStat{
		{BucketStart: at, APIGroupKey: "sk-provider123456", Model: "claude-sonnet", AuthIndex: "credential-1", RequestCount: 1, SuccessCount: 1, InputTokens: 22, OutputTokens: 7, CacheReadTokens: 1, CacheCreationTokens: 2, ReasoningTokens: 1, TotalTokens: 30, CostUSD: &credentialCost, UnavailableCostCount: &unavailable},
		{BucketStart: at, APIGroupKey: "sk-provider123456", Model: "claude-sonnet", AuthIndex: "provider-1", RequestCount: 1, SuccessCount: 1, InputTokens: 8, OutputTokens: 2, ReasoningTokens: 1, TotalTokens: 12, CostUSD: &providerCost, UnavailableCostCount: &unavailable},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	start, end := at.Add(-time.Hour), at.Add(time.Hour)
	stored, err := service.NewStoredUsageAnalysisService(db).GetStoredAnalysis(context.Background(), servicedto.StoredAnalysisFilter{Range: "24h", StartTime: &start, EndTime: &end, EndExclusive: true, APIKeyID: "42"})
	if err != nil || stored == nil {
		t.Fatalf("real stored analysis failed: %+v %v", stored, err)
	}
	encoded, err := json.Marshal(stored.CostSummary)
	if err != nil {
		t.Fatal(err)
	}
	var summaryFields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &summaryFields); err != nil || len(summaryFields) != 2 || !overviewAPICostClose(stored.CostSummary.TotalCostUSD, 1.23) || !stored.CostSummary.CostAvailable {
		t.Fatalf("stored summary lost original two-field monetary contract: %s %v", encoded, err)
	}
	if len(stored.TokenUsage) != 1 || stored.TokenUsage[0].TotalTokens != 42 || stored.TokenUsage[0].Requests != 2 || stored.TokenUsage[0].InputTokens != 30 || stored.TokenUsage[0].OutputTokens != 9 || stored.TokenUsage[0].ReasoningTokens != 2 || stored.TokenUsage[0].CacheReadTokens != 1 || stored.TokenUsage[0].CacheCreationTokens != 2 || !overviewAPICostClose(stored.TokenUsage[0].CostUSD, 1.23) || !stored.TokenUsage[0].CostAvailable {
		t.Fatalf("stored token facts/cost changed: %+v", stored.TokenUsage)
	}
	if len(stored.CredentialsComposition) != 1 || stored.CredentialsComposition[0].TotalTokens != 30 || stored.CredentialsComposition[0].Requests != 1 || !overviewAPICostClose(stored.CredentialsComposition[0].CostUSD, 0.8) || !stored.CredentialsComposition[0].CostAvailable || len(stored.AIProviderComposition) != 1 || stored.AIProviderComposition[0].TotalTokens != 12 || stored.AIProviderComposition[0].Requests != 1 || !overviewAPICostClose(stored.AIProviderComposition[0].CostUSD, 0.43) || !stored.AIProviderComposition[0].CostAvailable {
		t.Fatalf("stored typed composition amounts changed: %+v %+v", stored.CredentialsComposition, stored.AIProviderComposition)
	}
	// The original stub's 5.5 mapping assertion remains above. Real persisted
	// output is canonical (already includes reasoning), so 9 / 2 is 4.5.
	if len(stored.ModelEfficiency) != 1 || !overviewAPICostClose(stored.ModelEfficiency[0].CostUSD, 1.23) || !stored.ModelEfficiency[0].CostAvailable || !overviewAPICostClose(stored.ModelEfficiency[0].CostPerRequestUSD, 0.615) || stored.ModelEfficiency[0].OutputTokensPerRequest != 4.5 || !overviewAPICostClose(stored.ModelEfficiency[0].CacheReadRate, 1.0/30.0) {
		t.Fatalf("stored model efficiency changed: %+v", stored.ModelEfficiency)
	}
}

func TestUsageAnalysisUsesCPAAPIKeyOptionLabels(t *testing.T) {
	bucket := time.Date(2026, 4, 22, 10, 0, 0, 0, time.Local)
	lastSyncedAt := time.Date(2026, 5, 13, 10, 0, 0, 0, time.Local)
	provider := &analysisSplitStub{analysis: &servicedto.AnalysisSnapshot{
		Granularity: servicedto.AnalysisGranularityHourly,
		TokenUsage:  []servicedto.AnalysisTokenUsageBucket{{Bucket: bucket, TotalTokens: 42, Requests: 2}},
		APIKeyComposition: []servicedto.AnalysisCompositionItem{{
			Key:         "sk-alpha123456",
			TotalTokens: 42,
			Requests:    2,
		}},
		ModelComposition: []servicedto.AnalysisCompositionItem{{Key: "claude-sonnet", TotalTokens: 42, Requests: 2}},
		Heatmap: []servicedto.AnalysisHeatmapCell{{
			APIKey:      "sk-alpha123456",
			Model:       "claude-sonnet",
			TotalTokens: 42,
			Requests:    2,
		}},
	}}
	router := NewRouter(nil, nil, provider, nil, AuthConfig{}, nil, "", OptionalProviders{CPAAPIKeys: usageAnalysisAPIKeyStub{rows: []entities.CPAAPIKey{{
		ID:           1,
		APIKey:       "sk-alpha123456",
		DisplayKey:   "sk-*********123456",
		KeyAlias:     "Primary Key",
		LastSyncedAt: &lastSyncedAt,
	}}}})
	resp := serveAPIGet(router, "/api/v1/usage/analysis?range=24h&api_key_id=1")

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.Code)
	}
	body := resp.Body.String()
	maskedKey := helper.RedactSensitiveValue("sk-alpha123456")
	if !strings.Contains(body, `"key":"1"`) || !strings.Contains(body, `"label":"Primary Key"`) || !strings.Contains(body, `"api_key":"1"`) || !strings.Contains(body, `"api_key_labels":{"1":"Primary Key"}`) {
		t.Fatalf("expected analysis payload to use CPA API key id and display label, got %s", body)
	}
	if strings.Contains(body, "sk-alpha123456") || strings.Contains(body, maskedKey) {
		t.Fatalf("expected raw key and fallback redacted label to stay hidden when a CPA key alias exists, got %s", body)
	}
	if provider.analysisFilter.APIKeyID != "1" {
		t.Fatalf("expected API key id to pass into usage filter, got %+v", provider.analysisFilter)
	}
}

func TestUsageAnalysisUsesCPAAPIKeyIDsForCollidingDisplayKeys(t *testing.T) {
	bucket := time.Date(2026, 4, 22, 10, 0, 0, 0, time.Local)
	provider := &analysisSplitStub{analysis: &servicedto.AnalysisSnapshot{
		Granularity: servicedto.AnalysisGranularityHourly,
		TokenUsage:  []servicedto.AnalysisTokenUsageBucket{{Bucket: bucket, TotalTokens: 300, Requests: 3}},
		APIKeyComposition: []servicedto.AnalysisCompositionItem{
			{Key: "sk-alpha123456", TotalTokens: 100, Requests: 1},
			{Key: "sk-bravo123456", TotalTokens: 200, Requests: 2},
		},
		ModelComposition: []servicedto.AnalysisCompositionItem{{Key: "claude-sonnet", TotalTokens: 300, Requests: 3}},
		Heatmap: []servicedto.AnalysisHeatmapCell{
			{APIKey: "sk-alpha123456", Model: "claude-sonnet", TotalTokens: 100, Requests: 1},
			{APIKey: "sk-bravo123456", Model: "claude-sonnet", TotalTokens: 200, Requests: 2},
		},
	}}
	router := NewRouter(nil, nil, provider, nil, AuthConfig{}, nil, "", OptionalProviders{CPAAPIKeys: usageAnalysisAPIKeyStub{rows: []entities.CPAAPIKey{
		{ID: 1, APIKey: "sk-alpha123456", DisplayKey: "sk-*********123456", KeyAlias: "Primary Key"},
		{ID: 2, APIKey: "sk-bravo123456", DisplayKey: "sk-*********123456"},
	}}})
	resp := serveAPIGet(router, "/api/v1/usage/analysis?range=24h")

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.Code)
	}
	var payload struct {
		APIKeyComposition []struct{ Key, Label string } `json:"api_key_composition"`
		Heatmap           struct {
			APIKeys      []string          `json:"api_keys"`
			APIKeyLabels map[string]string `json:"api_key_labels"`
			Cells        []struct {
				APIKey string `json:"api_key"`
			} `json:"cells"`
		} `json:"heatmap"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode analysis response: %v", err)
	}
	if len(payload.APIKeyComposition) != 2 || payload.APIKeyComposition[0].Key != "1" || payload.APIKeyComposition[1].Key != "2" {
		t.Fatalf("expected API key composition to use ids, got %+v", payload.APIKeyComposition)
	}
	if payload.APIKeyComposition[0].Label != "Primary Key" || payload.APIKeyComposition[1].Label != "sk-*********123456" {
		t.Fatalf("expected API key composition labels to use alias first then redacted key, got %+v", payload.APIKeyComposition)
	}
	if len(payload.Heatmap.APIKeys) != 2 || payload.Heatmap.APIKeys[0] != "2" || payload.Heatmap.APIKeys[1] != "1" {
		t.Fatalf("expected heatmap API keys to use ids sorted by requests, got %+v", payload.Heatmap.APIKeys)
	}
	if payload.Heatmap.APIKeyLabels["1"] != "Primary Key" || payload.Heatmap.APIKeyLabels["2"] != "sk-*********123456" {
		t.Fatalf("expected heatmap labels to be keyed by id, got %+v", payload.Heatmap.APIKeyLabels)
	}
	if len(payload.Heatmap.Cells) != 2 || payload.Heatmap.Cells[0].APIKey != "1" || payload.Heatmap.Cells[1].APIKey != "2" {
		t.Fatalf("expected heatmap cells to keep separate id keys, got %+v", payload.Heatmap.Cells)
	}
	body := resp.Body.String()
	if strings.Contains(body, "sk-alpha123456") || strings.Contains(body, "sk-bravo123456") {
		t.Fatalf("expected raw keys to stay hidden, got %s", body)
	}
}

func TestUsageAnalysisReturnsCanonicalCacheReadAndCreationDetails(t *testing.T) {
	bucket := time.Date(2026, 4, 22, 10, 0, 0, 0, time.Local)
	provider := &analysisSplitStub{analysis: &servicedto.AnalysisSnapshot{
		Granularity: servicedto.AnalysisGranularityHourly,
		TokenUsage: []servicedto.AnalysisTokenUsageBucket{{
			Bucket:              bucket,
			InputTokens:         130,
			OutputTokens:        30,
			CacheReadTokens:     20,
			CacheCreationTokens: 5,
			TotalTokens:         160,
			Requests:            1,
		}},
	}}
	router := NewRouter(nil, nil, provider, nil, AuthConfig{}, nil, "")
	resp := serveAPIGet(router, "/api/v1/usage/analysis?range=24h")

	if resp.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.Code)
	}
	body := resp.Body.String()
	if strings.Contains(body, `"cached_tokens"`) || !strings.Contains(body, `"cache_read_tokens":20`) || !strings.Contains(body, `"cache_creation_tokens":5`) {
		t.Fatalf("expected canonical cache fields in analysis payload, got %s", body)
	}
}

func TestUsageAnalysisRequiresAuthWhenEnabled(t *testing.T) {
	router := NewRouter(nil, nil, &analysisSplitStub{}, nil, AuthConfig{Enabled: true, LoginPassword: "secret", SessionTTL: time.Hour}, nil, "")
	resp := serveAPIGet(router, "/api/v1/usage/analysis")

	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", resp.Code)
	}
}
