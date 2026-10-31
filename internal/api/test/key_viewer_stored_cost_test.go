package test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/api"
	"cpa-usage-keeper/internal/auth"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository"
	repodto "cpa-usage-keeper/internal/repository/dto"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
	"cpa-usage-keeper/internal/timeutil"
)

// Viewer 的两个普通入口保持 current 定价和 session scope；同一 fixture
// 的历史金额在独立 stored HTTP/service 上验证，不能借旧断言开放 Viewer 权限。
func TestKeyViewerOverviewAndAnalysisReadOnlySessionKeyStoredCosts(t *testing.T) {
	db := openAPITestDatabase(t)
	keys := []entities.CPAAPIKey{
		{ID: 42, APIKey: "sk-viewer", DisplayKey: "Viewer Key"},
		{ID: 99, APIKey: "sk-other", DisplayKey: "Other Key"},
	}
	if err := db.Create(&keys).Error; err != nil {
		t.Fatalf("seed Keys: %v", err)
	}
	bucket := timeutil.NormalizeStorageTime(time.Now()).Truncate(time.Hour).Add(-2 * time.Hour)
	viewerCost, otherCost, unavailable := 2.5, 99.0, int64(0)
	rows := []entities.UsageOverviewHourlyStat{
		{BucketStart: bucket, APIGroupKey: "sk-viewer", Model: "model-a", RequestCount: 2, SuccessCount: 2, InputTokens: 100, TotalTokens: 100, CostUSD: &viewerCost, UnavailableCostCount: &unavailable},
		{BucketStart: bucket, APIGroupKey: "sk-other", Model: "model-a", RequestCount: 3, SuccessCount: 3, InputTokens: 999, TotalTokens: 999, CostUSD: &otherCost, UnavailableCostCount: &unavailable},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatalf("seed stored overview rows: %v", err)
	}
	if _, err := repository.UpsertModelPriceSetting(db, repodto.ModelPriceSettingInput{Model: "model-a", PromptPricePer1M: 9}); err != nil {
		t.Fatalf("seed different current price: %v", err)
	}
	snapshot, err := repository.LoadPricingSnapshot(context.Background(), db)
	if err != nil {
		t.Fatalf("load current price: %v", err)
	}
	sessions := auth.NewSessionManager(time.Hour)
	token, _, err := sessions.CreateAPIKeyViewerWithSource(42, auth.SessionSourceStandard)
	if err != nil {
		t.Fatalf("create Viewer session: %v", err)
	}
	config := api.AuthConfig{Enabled: true, LoginPassword: "secret", SessionTTL: time.Hour}
	catalog := pricing.NewCatalog(snapshot)
	router := api.NewRouter(nil, nil, service.NewUsageService(db, catalog), nil, config, api.NewAuthHandler(config, sessions), "", api.OptionalProviders{CPAAPIKeys: &keyViewerAnalysisKeyStub{row: keys[0]}, StoredUsageOverview: service.NewStoredUsageOverviewService(db)})
	cookie := &http.Cookie{Name: standardSessionCookieName, Value: token}

	overview := serveAPIGet(router, "/api/v1/key-overview?range=24h&api_key_id=99", cookie)
	if overview.Code != http.StatusOK {
		t.Fatalf("Viewer overview status=%d body=%s", overview.Code, overview.Body.String())
	}
	var overviewPayload struct {
		Usage struct {
			TotalRequests int64 `json:"total_requests"`
			TotalTokens   int64 `json:"total_tokens"`
		} `json:"usage"`
		Summary struct {
			TotalCost     float64 `json:"total_cost"`
			CostAvailable bool    `json:"cost_available"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(overview.Body.Bytes(), &overviewPayload); err != nil {
		t.Fatalf("decode Viewer overview: %v", err)
	}
	if overviewPayload.Usage.TotalRequests != 2 || overviewPayload.Usage.TotalTokens != 100 || !overviewAPICostClose(overviewPayload.Summary.TotalCost, 0.0009) || !overviewPayload.Summary.CostAvailable {
		t.Fatalf("Viewer overview lost current price or session scope: %+v", overviewPayload)
	}

	analysis := serveAPIGet(router, "/api/v1/key-analysis?range=24h&api_key_id=99", cookie)
	if analysis.Code != http.StatusOK {
		t.Fatalf("Viewer Analysis status=%d body=%s", analysis.Code, analysis.Body.String())
	}
	var analysisPayload struct {
		CostBreakdown struct {
			TotalCostUSD  float64 `json:"total_cost_usd"`
			CostAvailable bool    `json:"cost_available"`
		} `json:"cost_breakdown"`
		TokenUsage []struct {
			TotalTokens int64   `json:"total_tokens"`
			CostUSD     float64 `json:"cost_usd"`
		} `json:"token_usage"`
		APIKeyComposition []struct {
			Key     string  `json:"key"`
			CostUSD float64 `json:"cost_usd"`
		} `json:"api_key_composition"`
		CredentialsComposition []json.RawMessage `json:"credentials_composition"`
		AuthFilesComposition   []json.RawMessage `json:"auth_files_composition"`
		AIProviderComposition  []json.RawMessage `json:"ai_provider_composition"`
	}
	if err := json.Unmarshal(analysis.Body.Bytes(), &analysisPayload); err != nil {
		t.Fatalf("decode Viewer Analysis: %v", err)
	}
	if !analysisPayload.CostBreakdown.CostAvailable || !overviewAPICostClose(analysisPayload.CostBreakdown.TotalCostUSD, 0.0009) ||
		len(analysisPayload.TokenUsage) != 1 || analysisPayload.TokenUsage[0].TotalTokens != 100 || !overviewAPICostClose(analysisPayload.TokenUsage[0].CostUSD, 0.0009) ||
		len(analysisPayload.APIKeyComposition) != 1 || analysisPayload.APIKeyComposition[0].Key != "42" || !overviewAPICostClose(analysisPayload.APIKeyComposition[0].CostUSD, 0.0009) ||
		len(analysisPayload.CredentialsComposition) != 0 || len(analysisPayload.AuthFilesComposition) != 0 || len(analysisPayload.AIProviderComposition) != 0 {
		t.Fatalf("Viewer Analysis leaked another Key or source identity: %+v", analysisPayload)
	}
	adminToken, _, err := sessions.CreateWithSource(auth.SessionSourceStandard)
	if err != nil {
		t.Fatal(err)
	}
	admin := &http.Cookie{Name: standardSessionCookieName, Value: adminToken}
	storedPath := "/api/v1/usage/stored/overview?range=24h&api_key_id=42"
	if denied := serveAPIGet(router, storedPath, cookie); denied.Code != http.StatusForbidden {
		t.Fatalf("Viewer acquired admin stored access: %d %s", denied.Code, denied.Body.String())
	}
	checkStored := func() {
		t.Helper()
		stored := overviewDecode(t, serveAPIGet(router, storedPath, admin))
		if stored.Summary.RequestCount != 2 || stored.Summary.TokenCount != 100 || !stored.Summary.StoredCostAvailable || !overviewAPICostClose(stored.Summary.StoredCostUSD, viewerCost) {
			t.Fatalf("explicit stored overview lost original amount/facts/scope: %+v", stored.Summary)
		}
		analysis, err := service.NewStoredUsageAnalysisService(db).GetStoredAnalysis(context.Background(), servicedto.StoredAnalysisFilter{Range: "24h", StartTime: &stored.Range.Start, EndTime: &stored.Range.End, EndExclusive: true, APIKeyID: "42"})
		if err != nil || analysis == nil {
			t.Fatalf("explicit stored analysis failed: %+v %v", analysis, err)
		}
		if !analysis.CostSummary.CostAvailable || !overviewAPICostClose(analysis.CostSummary.TotalCostUSD, viewerCost) ||
			len(analysis.TokenUsage) != 1 || analysis.TokenUsage[0].TotalTokens != 100 || !overviewAPICostClose(analysis.TokenUsage[0].CostUSD, viewerCost) ||
			len(analysis.APIKeyComposition) != 1 || analysis.APIKeyComposition[0].Key != keys[0].APIKey || !overviewAPICostClose(analysis.APIKeyComposition[0].CostUSD, viewerCost) ||
			len(analysis.CredentialsComposition) != 0 || len(analysis.AuthFilesComposition) != 0 || len(analysis.AIProviderComposition) != 0 {
			t.Fatalf("explicit stored analysis lost original amount/facts/key/identity scope: %+v", analysis)
		}
	}
	checkStored()
	for _, deleted := range []bool{false, true} {
		if deleted {
			if err := repository.DeleteModelPriceSettingRequired(db, "model-a"); err != nil {
				t.Fatal(err)
			}
		} else if _, err := repository.UpsertModelPriceSetting(db, repodto.ModelPriceSettingInput{Model: "model-a", PromptPricePer1M: 90}); err != nil {
			t.Fatal(err)
		}
		snapshot, err := repository.LoadPricingSnapshot(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		catalog.Replace(snapshot)
		current := serveAPIGet(router, "/api/v1/key-overview?range=24h&api_key_id=99", cookie)
		if current.Code != http.StatusOK || json.Unmarshal(current.Body.Bytes(), &overviewPayload) != nil {
			t.Fatalf("current Viewer lost history after price mutation: %d %s", current.Code, current.Body.String())
		}
		want := 0.009
		if deleted {
			want = 0
		}
		if overviewPayload.Usage.TotalRequests != 2 || overviewPayload.Usage.TotalTokens != 100 || !overviewAPICostClose(overviewPayload.Summary.TotalCost, want) || overviewPayload.Summary.CostAvailable == deleted {
			t.Fatalf("current Viewer price mutation widened scope or fell back to stored: %+v", overviewPayload)
		}
		checkStored()
	}
	for _, raw := range []string{keys[0].APIKey, keys[1].APIKey} {
		if strings.Contains(overview.Body.String(), raw) || strings.Contains(analysis.Body.String(), raw) {
			t.Fatal("Viewer HTTP leaked raw key")
		}
	}
}
