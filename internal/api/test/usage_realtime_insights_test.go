package test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "cpa-usage-keeper/internal/api"
	"cpa-usage-keeper/internal/auth"
	"cpa-usage-keeper/internal/config"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func TestRealtimeInsightsAPIMapsCachedTotalsAndScopesViewer(t *testing.T) {
	db, err := repository.OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "realtime.db")})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	t.Cleanup(func() { _ = sqlDB.Close() })
	key := entities.CPAAPIKey{ID: 42, APIKey: "sk-viewer123456", KeyAlias: "Viewer"}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ownCost, otherFailedCost, available := 4.25, 99.0, true
	if err := db.Create(&[]entities.UsageEvent{
		{EventKey: "own", Timestamp: now.Add(-time.Minute), APIGroupKey: key.APIKey, Model: "own-model", LatencyMS: 2000, InputTokens: 100, OutputTokens: 20, TotalTokens: 120, CacheReadTokens: 40, CostUSD: &ownCost, CostAvailable: &available},
		{EventKey: "other", Timestamp: now.Add(-time.Minute), APIGroupKey: "sk-other654321", Model: "other-model", OutputTokens: 100, LatencyMS: 1000, Failed: true, CostUSD: &otherFailedCost, CostAvailable: &available},
	}).Error; err != nil {
		t.Fatal(err)
	}
	cache, err := repository.NewUsageRecentEventCache(db, repository.UsageRecentEventCacheOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Close)
	provider := service.NewUsageServiceWithRecentCache(db, cache, emptyPricingCatalogForTest())
	keys := &keyViewerAnalysisKeyStub{row: key}
	sessions := auth.NewSessionManager(time.Hour)
	token, _, err := sessions.CreateAPIKeyViewerWithSource(42, auth.SessionSourceStandard)
	if err != nil {
		t.Fatal(err)
	}
	authConfig := AuthConfig{Enabled: true, LoginPassword: "secret", SessionTTL: time.Hour}
	for _, viewer := range []bool{false, true} {
		router := NewRouter(nil, nil, provider, nil, AuthConfig{}, nil, "", OptionalProviders{CPAAPIKeys: keys})
		path := "/api/v1/usage/overview/realtime?window=15m"
		if viewer {
			router = NewRouter(nil, nil, provider, nil, authConfig, NewAuthHandler(authConfig, sessions), "", OptionalProviders{CPAAPIKeys: keys})
			path = "/api/v1/key-overview/realtime?window=15m&api_key_id=99"
		}
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if viewer {
			request.AddCookie(&http.Cookie{Name: standardSessionCookieName, Value: token})
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("status %d: %s", response.Code, response.Body.String())
		}
		var payload struct {
			ModelSpeed struct {
				Available       bool `json:"available"`
				Start, Mid, End time.Time
				Series          []struct {
					Model       string
					TotalTokens int64 `json:"total_tokens"`
					FirstHalf   struct {
						MeanTPS *float64 `json:"mean_tps"`
					} `json:"first_half"`
					SecondHalf struct {
						MeanTPS *float64 `json:"mean_tps"`
						Count   int64    `json:"speed_sample_count"`
					} `json:"second_half"`
				} `json:"series"`
			} `json:"model_speed"`
			Insights struct {
				Summary struct {
					Requests, Failures int64
					TotalTokens        int64 `json:"total_tokens"`
					Cost               *float64
				}
				Outcomes []json.RawMessage
			}
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		wantModels := 2
		if viewer {
			wantModels = 1
		}
		if !payload.ModelSpeed.Available || len(payload.ModelSpeed.Series) != wantModels || payload.ModelSpeed.Series[0].Model != "own-model" || payload.ModelSpeed.Series[0].TotalTokens != 120 || payload.ModelSpeed.Series[0].FirstHalf.MeanTPS != nil || payload.ModelSpeed.Series[0].SecondHalf.MeanTPS == nil || !overviewAPICostClose(*payload.ModelSpeed.Series[0].SecondHalf.MeanTPS, 10) || payload.ModelSpeed.Series[0].SecondHalf.Count != 1 {
			t.Fatalf("speed mapping/viewer scope: %s", response.Body.String())
		}
		if payload.ModelSpeed.Mid.Sub(payload.ModelSpeed.Start) != 450*time.Second || payload.ModelSpeed.End.Sub(payload.ModelSpeed.Mid) != 450*time.Second {
			t.Fatal("incorrect half bounds")
		}
		wantRequests, wantFailures := int64(2), int64(1)
		if viewer {
			wantRequests, wantFailures = 1, 0
		}
		// The ordinary HTTP reader has no current tariff for this fixture. It
		// must retain facts but never fall back to the stored monetary column.
		if payload.Insights.Summary.Requests != wantRequests || payload.Insights.Summary.Failures != wantFailures || payload.Insights.Summary.TotalTokens != 120 || payload.Insights.Summary.Cost != nil || len(payload.Insights.Outcomes) != 30 {
			t.Fatalf("incorrect insights: requests=%d failures=%d tokens=%d cost=%v outcomes=%d", payload.Insights.Summary.Requests, payload.Insights.Summary.Failures, payload.Insights.Summary.TotalTokens, payload.Insights.Summary.Cost, len(payload.Insights.Outcomes))
		}
		storedFilter := servicedto.StoredUsageRealtimeFilter{RealtimeWindow: "15m", RealtimeEndTime: &now}
		if viewer {
			storedFilter.APIKeyID = "42"
		}
		stored, err := service.NewStoredUsageRealtimeService(db, cache).GetStoredUsageRealtime(context.Background(), storedFilter)
		if err != nil || stored == nil || stored.Insights == nil {
			t.Fatalf("explicit stored realtime read failed: %+v %v", stored, err)
		}
		if stored.Insights.Summary.Requests != wantRequests || stored.Insights.Summary.Failures != wantFailures || stored.Insights.Summary.TotalTokens != 120 || !stored.Insights.Summary.StoredCostAvailable || !overviewAPICostClose(stored.Insights.Summary.StoredCostUSD, ownCost) || len(stored.Insights.Outcomes) != 30 {
			t.Fatalf("explicit stored insight amount/facts/scope changed: %+v", stored.Insights)
		}
		if !stored.ModelSpeed.Available || len(stored.ModelSpeed.Series) != wantModels || stored.ModelSpeed.Series[0].Model != "own-model" || stored.ModelSpeed.Series[0].TotalTokens != 120 || stored.ModelSpeed.Series[0].FirstHalf.MeanTPS != nil || stored.ModelSpeed.Series[0].SecondHalf.MeanTPS == nil || !overviewAPICostClose(*stored.ModelSpeed.Series[0].SecondHalf.MeanTPS, 10) || stored.ModelSpeed.Series[0].SecondHalf.SpeedSampleCount != 1 || stored.ModelSpeed.Mid.Sub(stored.ModelSpeed.Start) != 450*time.Second || stored.ModelSpeed.End.Sub(stored.ModelSpeed.Mid) != 450*time.Second {
			t.Fatalf("stored valuation changed original speed/half-window facts: %+v", stored.ModelSpeed)
		}
		if strings.Contains(response.Body.String(), key.APIKey) || (viewer && strings.Contains(response.Body.String(), "other-model")) {
			t.Fatal("realtime scope leaked")
		}
	}
}
