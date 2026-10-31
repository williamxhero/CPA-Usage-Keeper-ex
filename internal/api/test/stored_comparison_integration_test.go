package test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"gorm.io/gorm"
)

func comparisonHTTPRouter(t *testing.T, db *gorm.DB, catalog *pricing.Catalog, gate *service.CostReadGate) (http.Handler, *http.Cookie, *http.Cookie) {
	t.Helper()
	sessions := auth.NewSessionManager(time.Hour)
	a, _, err := sessions.CreateWithSource(auth.SessionSourceStandard)
	if err != nil {
		t.Fatal(err)
	}
	v, _, err := sessions.CreateAPIKeyViewerWithSource(42, auth.SessionSourceStandard)
	if err != nil {
		t.Fatal(err)
	}
	cfg := api.AuthConfig{Enabled: true, SessionTTL: time.Hour}
	r := api.NewRouter(nil, nil, service.NewUsageService(db, catalog), nil, cfg, api.NewAuthHandler(cfg, sessions), "", api.OptionalProviders{CostReadGate: gate, StoredUsageComparisons: service.NewStoredUsageComparisonsService(db), CPAAPIKeys: service.NewCPAAPIKeyService(db)})
	return r, &http.Cookie{Name: standardSessionCookieName, Value: a}, &http.Cookie{Name: standardSessionCookieName, Value: v}
}

type comparisonResult struct {
	servicedto.StoredUsageComparisons
	Summary servicedto.StoredUsageOverviewSummary
}

func comparisonDecode(t *testing.T, res *httptest.ResponseRecorder) comparisonResult {
	t.Helper()
	storedDecode(t, res)
	storedNoExposure(t, res.Body.String())
	var out servicedto.StoredUsageComparisons
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Range.EndExclusive || !out.Range.Start.Before(out.Range.End) || out.Coverage.ArchiveCoverageAsserted || out.Coverage.CacheUsed || !strings.Contains(out.Coverage.BoundarySource, "hot") || len(out.Coverage.RollupSources) != 2 {
		t.Fatalf("coverage/range drift: %+v", out)
	}
	summary := servicedto.StoredUsageOverviewSummary{StoredCostAvailable: true}
	for _, item := range out.Models {
		summary.RequestCount += item.Requests
		summary.StoredCostUSD += item.StoredCostUSD
		summary.StoredCostAvailable = summary.StoredCostAvailable && item.StoredCostAvailable
	}
	return comparisonResult{out, summary}
}
func comparisonSeed(t *testing.T, db *gorm.DB, at time.Time, fee *float64, available *bool) {
	t.Helper()
	e := entities.UsageEvent{EventKey: fmt.Sprintf("comparison-%d", at.UnixNano()), Timestamp: at, Model: "stored-model", APIGroupKey: "private-comparison-key", AuthIndex: "private-observed-index", Source: "/private/auth/token.json", InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: fee, CostAvailable: available}
	if err := db.Create(&e).Error; err != nil {
		t.Fatal(err)
	}
}
func comparisonCustomHour() string {
	end := time.Now().In(time.Local).Truncate(time.Hour).Add(-time.Hour)
	q := url.Values{"range": {"custom"}, "unit": {"hour"}, "start": {end.Add(-5 * time.Hour).Format(time.RFC3339)}, "end": {end.Format(time.RFC3339)}}
	return q.Encode()
}
func TestStoredComparisonHTTPCurrentIsolationAndRanges(t *testing.T) {
	db := openAPITestDatabase(t)
	fee, ok := 1.25, true
	comparisonSeed(t, db, time.Now().Add(-2*time.Hour), &fee, &ok)
	if err := repository.AggregateUsageOverviewStats(context.Background(), db, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, price := range []float64{9, 90} {
		if _, err := repository.UpsertModelPriceSetting(db, repodto.ModelPriceSettingInput{Model: "stored-model", PromptPricePer1M: price}); err != nil {
			t.Fatal(err)
		}
		snap, err := repository.LoadPricingSnapshot(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		r, a, _ := comparisonHTTPRouter(t, db, pricing.NewCatalog(snap), nil)
		current := storedDecode(t, serveAPIGet(r, "/api/v1/usage/overview?range=24h", a))
		if current["summary"].(map[string]any)["total_cost"] != price {
			t.Fatalf("current price drift: %v", current)
		}
		for _, query := range []string{"range=24h", "range=7d", comparisonCustomHour(), "range=custom&unit=day&start=" + time.Now().AddDate(0, 0, -1).Format(time.DateOnly) + "&end=" + time.Now().Format(time.DateOnly)} {
			out := comparisonDecode(t, serveAPIGet(r, "/api/v1/usage/stored/comparisons?"+query, a))
			if out.Summary.StoredCostUSD != fee || out.Summary.RequestCount != 1 || !out.Summary.StoredCostAvailable {
				t.Fatalf("%s: %+v", query, out)
			}
		}
		for _, value := range []string{"today", "yesterday"} {
			comparisonDecode(t, serveAPIGet(r, "/api/v1/usage/stored/comparisons?range="+value, a))
		}
	}
}
func TestStoredComparisonHTTPAuthScopeValidationAndPermit(t *testing.T) {
	db := openAPITestDatabase(t)
	fee, ok := 2.0, true
	comparisonSeed(t, db, time.Now().Add(-2*time.Hour), &fee, &ok)
	if err := repository.AggregateUsageOverviewStats(context.Background(), db, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entities.CPAAPIKey{ID: 42, APIKey: "private-comparison-key"}).Error; err != nil {
		t.Fatal(err)
	}
	gate := service.NewCostReadGate()
	r, a, v := comparisonHTTPRouter(t, db, emptyPricingCatalogForTest(), gate)
	path := "/api/v1/usage/stored/comparisons?range=24h"
	if w := serveAPIGet(r, path); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := serveAPIGet(r, path, v); w.Code != 403 {
		t.Fatal(w.Code)
	}
	out := comparisonDecode(t, serveAPIGet(r, path+"&api_key_id=42", a))
	if out.Summary.StoredCostUSD != 2 {
		t.Fatal(out)
	}
	if err := db.Create(&entities.CPAAPIKey{ID: 43, APIKey: "absent-key"}).Error; err != nil {
		t.Fatal(err)
	}
	if out := comparisonDecode(t, serveAPIGet(r, path+"&api_key_id=43", a)); out.Summary.RequestCount != 0 {
		t.Fatal(out)
	}
	for _, tc := range []struct {
		q    string
		code int
	}{{"api_key_id=bad", 400}, {"api_key_id=99999", 404}, {"model=", 400}, {"result=failed", 400}, {"auth_index=x", 400}, {"source=x", 400}, {"auth_type=1", 400}, {"cursor=x", 400}, {"cursor_mode=false", 400}, {"page=1", 400}, {"window=15m", 400}, {"range=24h", 400}, {"start=x", 400}} {
		if w := serveAPIGet(r, path+"&"+tc.q, a); w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.q, w.Code, w.Body.String())
		}
	}
	for _, q := range []string{"range=custom&unit=hour&start=2020-01-01T00:00:00Z&end=2020-01-01T04:00:00Z", "range=custom&unit=day&start=2020-01-01&end=2020-01-02"} {
		if w := serveAPIGet(r, "/api/v1/usage/stored/comparisons?"+q, a); w.Code != 409 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	res := serveAPIGet(r, path, a)
	if !strings.Contains(res.Header().Get("Cache-Control"), "no-store") {
		t.Fatal(res.Header())
	}
	for _, intent := range []string{"", "fetch"} {
		req := httptest.NewRequest("POST", "/api/v1/usage/stored/comparisons", nil)
		req.AddCookie(a)
		req.Header.Set("X-CPA-Usage-Keeper-Request", intent)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != 404 {
			t.Fatal(w.Code)
		} // no mutation route, with or without intent
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resume, err := gate.BlockAndDrain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if w := serveAPIGet(r, path, a); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if w := serveAPIGet(r, path, v); w.Code != 403 {
		t.Fatal(w.Code)
	}
	resume()
	comparisonDecode(t, serveAPIGet(r, path, a))
	canceled, stop := context.WithCancel(context.Background())
	stop()
	req := httptest.NewRequest("GET", path, nil).WithContext(canceled)
	req.AddCookie(a)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == 200 {
		t.Fatal("canceled request succeeded")
	}
	resume, err = gate.BlockAndDrain(ctx)
	if err != nil {
		t.Fatal("permit leaked", err)
	}
	resume()
	if err := db.Model(&entities.CPAAPIKey{}).Where("id=42").Update("is_deleted", true).Error; err != nil {
		t.Fatal(err)
	}
	if w := serveAPIGet(r, path+"&api_key_id=42", a); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestStoredComparisonHTTPUnknownUnavailableZeroAndNonfinite(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fee       *float64
		available *bool
		code      int
	}{
		{"null-fee", nil, comparisonBool(true), 500}, {"null-flag", comparisonFloat(0), nil, 500}, {"false-zero", comparisonFloat(0), comparisonBool(false), 200}, {"true-zero", comparisonFloat(0), comparisonBool(true), 200}, {"nonfinite", comparisonFloat(math.Inf(1)), comparisonBool(true), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openAPITestDatabase(t)
			comparisonSeed(t, db, time.Now().Truncate(time.Hour).Add(time.Nanosecond), tc.fee, tc.available)
			r, a, _ := comparisonHTTPRouter(t, db, emptyPricingCatalogForTest(), nil)
			w := serveAPIGet(r, "/api/v1/usage/stored/comparisons?range=24h", a)
			if w.Code != tc.code {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if tc.code == 200 {
				out := comparisonDecode(t, w)
				if out.Summary.StoredCostUSD != 0 || out.Summary.StoredCostAvailable != *tc.available {
					t.Fatal(out)
				}
			} else if w.Body.String() != "{\"error\":\"internal server error\"}" {
				t.Fatal("internal error leak", w.Body.String())
			}
		})
	}
}
func comparisonFloat(v float64) *float64 { return &v }
func comparisonBool(v bool) *bool        { return &v }
func TestStoredComparisonHTTPRetainedRollupsMissingRawAndReadOnly(t *testing.T) {
	db := openAPITestDatabase(t)
	fee, unknown := 3.0, int64(0)
	at := time.Now().Truncate(time.Hour).Add(-2 * time.Hour)
	row := entities.UsageOverviewHourlyStat{BucketStart: at, APIGroupKey: "private-comparison-key", Model: "stored-model", RequestCount: 1, TotalTokens: 20, CostUSD: &fee, UnavailableCostCount: &unknown}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	yesterday := time.Now().AddDate(0, 0, -1)
	day := time.Date(yesterday.Year(), yesterday.Month(), yesterday.Day(), 0, 0, 0, 0, time.Local)
	dailyFee := 7.0
	if err := db.Create(&entities.UsageOverviewDailyStat{BucketStart: day, APIGroupKey: "private-comparison-key", Model: "stored-model", RequestCount: 2, TotalTokens: 40, CostUSD: &dailyFee, UnavailableCostCount: &unknown}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DROP TABLE usage_events").Error; err != nil {
		t.Fatal(err)
	}
	r, a, _ := comparisonHTTPRouter(t, db, emptyPricingCatalogForTest(), nil)
	reject := func(tx *gorm.DB) {
		sql := strings.ToLower(tx.Statement.SQL.String() + tx.Statement.Table)
		if strings.Contains(sql, "pric") || strings.Contains(sql, "archive") {
			t.Error("forbidden query", sql)
			tx.AddError(fmt.Errorf("forbidden read"))
		}
	}
	write := func(tx *gorm.DB) { t.Error("comparison wrote database"); tx.AddError(fmt.Errorf("write forbidden")) }
	for _, cb := range []struct {
		add    func(string, func(*gorm.DB)) error
		remove func(string) error
	}{
		{db.Callback().Query().After("gorm:query").Register, db.Callback().Query().Remove}, {db.Callback().Row().After("gorm:row").Register, db.Callback().Row().Remove}, {db.Callback().Raw().After("gorm:raw").Register, db.Callback().Raw().Remove},
	} {
		if err := cb.add("comparison_readonly", reject); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cb.remove("comparison_readonly") })
	}
	for _, cb := range []struct {
		add    func(string, func(*gorm.DB)) error
		remove func(string) error
	}{
		{db.Callback().Create().Before("gorm:create").Register, db.Callback().Create().Remove}, {db.Callback().Update().Before("gorm:update").Register, db.Callback().Update().Remove}, {db.Callback().Delete().Before("gorm:delete").Register, db.Callback().Delete().Remove},
	} {
		if err := cb.add("comparison_write", write); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cb.remove("comparison_write") })
	}
	out := comparisonDecode(t, serveAPIGet(r, "/api/v1/usage/stored/comparisons?"+comparisonCustomHour(), a))
	if out.Summary.StoredCostUSD != 3 || out.Summary.RequestCount != 1 {
		t.Fatal(out)
	}
	dailyQuery := url.Values{"range": {"custom"}, "unit": {"day"}, "start": {day.Format(time.DateOnly)}, "end": {day.Format(time.DateOnly)}}
	daily := comparisonDecode(t, serveAPIGet(r, "/api/v1/usage/stored/comparisons?"+dailyQuery.Encode(), a))
	if daily.Summary.StoredCostUSD != dailyFee || daily.Summary.RequestCount != 2 {
		t.Fatal("retained daily rollup lost", daily)
	}
	w := serveAPIGet(r, "/api/v1/usage/stored/comparisons?range=24h", a)
	if w.Code != 500 || strings.Contains(w.Body.String(), "table") {
		t.Fatal("missing hot table fabricated coverage", w.Code, w.Body.String())
	}
}
