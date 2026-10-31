package test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
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
	"cpa-usage-keeper/internal/config"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository"
	repodto "cpa-usage-keeper/internal/repository/dto"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
	"gorm.io/gorm"
)

func storedHTTPRouter(t *testing.T, db *gorm.DB, catalog *pricing.Catalog, gate *service.CostReadGate, override ...service.StoredUsageEventsProvider) (http.Handler, *http.Cookie, *http.Cookie) {
	t.Helper()
	sessions := auth.NewSessionManager(time.Hour)
	admin, _, err := sessions.CreateWithSource(auth.SessionSourceStandard)
	if err != nil {
		t.Fatal(err)
	}
	viewer, _, err := sessions.CreateAPIKeyViewerWithSource(42, auth.SessionSourceStandard)
	if err != nil {
		t.Fatal(err)
	}
	cfg := api.AuthConfig{Enabled: true, LoginPassword: "synthetic-password", SessionTTL: time.Hour}
	opt := api.OptionalProviders{CostReadGate: gate, CPAAPIKeys: service.NewCPAAPIKeyService(db)}
	opt.StoredUsageEvents = service.NewStoredUsageEventsService(db)
	if len(override) > 0 {
		opt.StoredUsageEvents = override[0]
	}
	opt.UsageIdentity = service.NewUsageIdentityService(db)
	r := api.NewRouter(nil, nil, service.NewUsageService(db, catalog), nil, cfg, api.NewAuthHandler(cfg, sessions), "", opt)
	return r, &http.Cookie{Name: standardSessionCookieName, Value: admin}, &http.Cookie{Name: standardSessionCookieName, Value: viewer}
}
func storedHTTPSeed(t *testing.T, db *gorm.DB, n int) {
	t.Helper()
	at := time.Now().Add(-time.Hour)
	cost, available := 1.25, true
	rows := make([]entities.UsageEvent, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, entities.UsageEvent{EventKey: fmt.Sprintf("stored-http-%d", i), Timestamp: at, APIGroupKey: "sk-synthetic-private-group", AuthType: "apikey", AuthIndex: "private-observed-index", Provider: "private-provider-secret", Source: "/private/auth/token.json", Model: "stored-model", ServiceTier: "priority", EffectiveServiceTier: "flex", ResponseServiceTier: "default", InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: &cost, CostAvailable: &available})
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
}
func storedDecode(t *testing.T, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if res.Code != 200 {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var p map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}
func storedNoExposure(t *testing.T, s string) {
	t.Helper()
	for _, bad := range []string{"sk-synthetic-private-group", "private-observed-index", "private-provider-secret", "/private/auth/token.json", "ObservedAuth", "APIGroupKey", "\"cost_usd\"", "\"cost_available\"", "pricing_snapshot", "pricing_style", "pricing_selection", "dual_costs"} {
		if strings.Contains(s, bad) {
			t.Fatalf("leaked %q: %s", bad, s)
		}
	}
}
func TestStoredHTTPIsolationAndExposure(t *testing.T) {
	db := openAPITestDatabase(t)
	storedHTTPSeed(t, db, 1)
	set := func(price float64) *pricing.Catalog {
		t.Helper()
		if _, err := repository.UpsertModelPriceSetting(db, repodto.ModelPriceSettingInput{Model: "stored-model", PromptPricePer1M: price}); err != nil {
			t.Fatal(err)
		}
		s, err := repository.LoadPricingSnapshot(context.Background(), db)
		if err != nil {
			t.Fatal(err)
		}
		return pricing.NewCatalog(s)
	}
	for _, price := range []float64{9, 90} {
		r, a, _ := storedHTTPRouter(t, db, set(price), nil)
		current := storedDecode(t, serveAPIGet(r, "/api/v1/usage/events?range=24h", a))
		if current["events"].([]any)[0].(map[string]any)["cost_usd"] != price {
			t.Fatalf("current drift: %+v", current)
		}
		for _, path := range []string{"/api/v1/usage/stored/events?range=24h", "/api/v1/usage/stored/events/export?range=24h&format=json"} {
			res := serveAPIGet(r, path, a)
			p := storedDecode(t, res)
			e := p["events"].([]any)[0].(map[string]any)
			if p["data_source"] != "usage_events" || e["stored_cost_usd"] != 1.25 || e["stored_cost_available"] != true || e["effective_service_tier"] != "flex" {
				t.Fatalf("stored drift: %+v", p)
			}
			storedNoExposure(t, res.Body.String())
		}
		res := serveAPIGet(r, "/api/v1/usage/stored/events/export?range=24h", a)
		if res.Code != 200 {
			t.Fatal(res.Code, res.Body.String())
		}
		storedNoExposure(t, res.Body.String())
		rows, err := csv.NewReader(strings.NewReader(res.Body.String())).ReadAll()
		if err != nil || len(rows) != 2 {
			t.Fatal(rows, err)
		}
		values := map[string]string{}
		for i, k := range rows[0] {
			values[k] = rows[1][i]
		}
		if values["stored_cost_usd"] != "1.25" || values["stored_cost_available"] != "true" || values["data_source"] != "usage_events" {
			t.Fatal(values)
		}
	}
}
func TestStoredHTTPPagingFiltersAuthIntentAndPermits(t *testing.T) {
	db := openAPITestDatabase(t)
	storedHTTPSeed(t, db, 25)
	if err := db.Create(&entities.CPAAPIKey{ID: 42, APIKey: "sk-synthetic-private-group", DisplayKey: "synthetic safe display"}).Error; err != nil {
		t.Fatal(err)
	}
	gate := service.NewCostReadGate()
	r, a, v := storedHTTPRouter(t, db, emptyPricingCatalogForTest(), gate)
	base := "/api/v1/usage/stored/events?range=24h&page_size=20&cursor_mode=true"
	for _, path := range []string{"/api/v1/usage/stored/events?range=24h&api_key_id=42", "/api/v1/usage/stored/events/export?range=24h&api_key_id=42&format=json"} {
		res := serveAPIGet(r, path, a)
		p := storedDecode(t, res)
		storedNoExposure(t, res.Body.String())
		if p["total_count"] != float64(25) {
			t.Fatal(p)
		}
	}
	p := storedDecode(t, serveAPIGet(r, base, a))
	if len(p["events"].([]any)) != 20 || p["total_count"] != float64(25) || p["has_more"] != true {
		t.Fatal(p)
	}
	p2 := storedDecode(t, serveAPIGet(r, base+"&cursor="+p["next_cursor"].(string), a))
	if len(p2["events"].([]any)) != 5 || p2["has_more"] != false {
		t.Fatal(p2)
	}
	page := storedDecode(t, serveAPIGet(r, "/api/v1/usage/stored/events?range=24h&page_size=20&page=2", a))
	if len(page["events"].([]any)) != 5 {
		t.Fatal(page)
	}
	for _, q := range []string{"model=absent", "source=absent", "auth_type=1", "result=failed"} {
		p := storedDecode(t, serveAPIGet(r, "/api/v1/usage/stored/events?range=24h&"+q, a))
		if len(p["events"].([]any)) != 0 {
			t.Fatal(q, p)
		}
	}
	for _, path := range []string{"/api/v1/usage/stored/events?range=24h", "/api/v1/usage/stored/events/export?range=24h"} {
		if res := serveAPIGet(r, path+"&api_key_id=99999", a); res.Code != 404 {
			t.Fatal("unknown Key scope", res.Code, res.Body.String())
		}
		if res := serveAPIGet(r, path); res.Code != 401 {
			t.Fatal(res.Code)
		}
		if res := serveAPIGet(r, path, v); res.Code != 403 {
			t.Fatal(res.Code)
		}
	}
	for _, q := range []string{"cursor=bad", "page_size=100000", "result=bad", "auth_type=bad", "format=xml"} {
		path := "/api/v1/usage/stored/events?range=24h&"
		if strings.HasPrefix(q, "format") {
			path = "/api/v1/usage/stored/events/export?range=24h&"
		}
		if res := serveAPIGet(r, path+q, a); res.Code != 400 {
			t.Fatal(q, res.Code, res.Body.String())
		}
	}
	req := httptest.NewRequest("POST", "/api/v1/usage/stored/events", nil)
	req.AddCookie(a)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 404 { // No mutation route: Gin's existing unmatched-method contract.
		t.Fatal(w.Code)
	}
	req = httptest.NewRequest("POST", "/api/v1/usage/stored/events", nil)
	req.AddCookie(a)
	req.Header.Set("X-CPA-Request-Intent", "fetch")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resume, err := gate.BlockAndDrain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res := serveAPIGet(r, "/api/v1/usage/stored/events?range=24h", a); res.Code != 503 {
		t.Fatal(res.Code)
	}
	if res := serveAPIGet(r, "/api/v1/usage/stored/events?range=24h", v); res.Code != 403 {
		t.Fatal(res.Code)
	}
	resume()
	req = httptest.NewRequest("GET", "/api/v1/usage/stored/events/export?range=24h&format=json", nil)
	cctx, ccancel := context.WithCancel(context.Background())
	ccancel()
	req = req.WithContext(cctx)
	req.AddCookie(a)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	resume, err = gate.BlockAndDrain(ctx)
	if err != nil {
		t.Fatal("leaked permit", err)
	}
	resume()
}
func TestStoredHTTPRedactionTypedSourcesReadOnlyAndHotCoverage(t *testing.T) {
	db := openAPITestDatabase(t)
	storedHTTPSeed(t, db, 1)
	alias := "Safe account"
	identities := []entities.UsageIdentity{
		{Name: "/private/auth/token.json", Alias: &alias, AuthType: entities.UsageIdentityAuthTypeAIProvider, AuthTypeName: "apikey", Identity: "private-observed-index", LookupKey: "sk-synthetic-private-group", Type: "openai"},
		{Name: "Wrong typed account", AuthType: entities.UsageIdentityAuthTypeAuthFile, AuthTypeName: "oauth", Identity: "private-observed-index", Type: "codex"},
	}
	if err := db.Create(&identities).Error; err != nil {
		t.Fatal(err)
	}
	// Archive-only rows must not inflate list counts or exports.
	if err := db.Exec("INSERT INTO usage_events_archive SELECT * FROM usage_events").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entities.UsageEvent{}).Where("id=1").Update("model_alias", "=unsafe-formula").Error; err != nil {
		t.Fatal(err)
	}
	r, a, _ := storedHTTPRouter(t, db, emptyPricingCatalogForTest(), nil)
	reject := func(tx *gorm.DB) {
		sql := strings.ToLower(tx.Statement.SQL.String() + tx.Statement.Table)
		if strings.Contains(sql, "pric") || strings.Contains(sql, "archive") {
			t.Errorf("forbidden stored HTTP query: %s", sql)
			tx.AddError(fmt.Errorf("forbidden read"))
		}
	}
	write := func(tx *gorm.DB) { t.Error("stored HTTP wrote database"); tx.AddError(fmt.Errorf("write forbidden")) }
	for _, cb := range []struct {
		add    func(string, func(*gorm.DB)) error
		remove func(string) error
	}{{db.Callback().Query().After("gorm:query").Register, db.Callback().Query().Remove}, {db.Callback().Row().After("gorm:row").Register, db.Callback().Row().Remove}, {db.Callback().Raw().After("gorm:raw").Register, db.Callback().Raw().Remove}} {
		if err := cb.add("stored_http_reject", reject); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cb.remove("stored_http_reject") })
	}
	for _, cb := range []struct {
		add    func(string, func(*gorm.DB)) error
		remove func(string) error
	}{{db.Callback().Create().Before("gorm:create").Register, db.Callback().Create().Remove}, {db.Callback().Update().Before("gorm:update").Register, db.Callback().Update().Remove}, {db.Callback().Delete().Before("gorm:delete").Register, db.Callback().Delete().Remove}} {
		if err := cb.add("stored_http_write", write); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cb.remove("stored_http_write") })
	}
	for _, path := range []string{"/api/v1/usage/stored/events?range=24h&source=private-observed-index&auth_type=2", "/api/v1/usage/stored/events/export?range=24h&format=json"} {
		res := serveAPIGet(r, path, a)
		p := storedDecode(t, res)
		storedNoExposure(t, res.Body.String())
		events := p["events"].([]any)
		if len(events) != 1 || p["total_count"] != float64(1) || events[0].(map[string]any)["source"] != "Safe account" {
			t.Fatal(p)
		}
	}
	res := serveAPIGet(r, "/api/v1/usage/stored/events/export?range=24h", a)
	if res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	storedNoExposure(t, res.Body.String())
	if !strings.Contains(res.Body.String(), "'=unsafe-formula") {
		t.Fatal("CSV formula not escaped", res.Body.String())
	}
}

func TestStoredHTTPReaderPoolSnapshotAndCancellation(t *testing.T) {
	writer := openAPITestDatabase(t)
	storedHTTPSeed(t, writer, 3)
	var dbs []struct {
		Seq        int
		Name, File string
	}
	if err := writer.Raw("PRAGMA database_list").Scan(&dbs).Error; err != nil {
		t.Fatal(err)
	}
	reader, err := repository.OpenReadDatabase(config.Config{SQLitePath: dbs[0].File})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := reader.DB()
	t.Cleanup(func() { sqlDB.Close() })
	r, a, _ := storedHTTPRouter(t, reader, emptyPricingCatalogForTest(), nil)
	if res := serveAPIGet(r, "/api/v1/usage/stored/events/export?range=24h&format=json", a); res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
	if sqlDB.Stats().InUse != 0 {
		t.Fatal("HTTP reader leak", sqlDB.Stats())
	}
	provider := service.NewStoredUsageEventsService(reader)
	seen := 0
	err = provider.StreamStoredUsageEvents(context.Background(), servicedto.UsageFilter{}, func(row servicedto.StoredUsageEventRecord) error {
		seen++
		if seen == 1 {
			if err := writer.Model(&entities.UsageEvent{}).Where("1=1").Update("cost_usd", 99).Error; err != nil {
				return err
			}
		}
		if row.StoredCostUSD != 1.25 {
			t.Errorf("SELECT snapshot changed mid-stream: %+v", row)
		}
		return nil
	})
	if err != nil || seen != 3 {
		t.Fatal(err, seen)
	}
	ctx, cancel := context.WithCancel(context.Background())
	seen = 0
	err = provider.StreamStoredUsageEvents(ctx, servicedto.UsageFilter{}, func(row servicedto.StoredUsageEventRecord) error { seen++; cancel(); return nil })
	if err != context.Canceled || seen != 1 || sqlDB.Stats().InUse != 0 {
		t.Fatal("cancel leak", err, seen, sqlDB.Stats())
	}
	// Boundaries include timezone parsing and API Key scope, not only empty filters.
	q := url.Values{"range": {"custom"}, "unit": {"hour"}, "start": {time.Now().Truncate(time.Hour).Add(-6 * time.Hour).Format(time.RFC3339)}, "end": {time.Now().Truncate(time.Hour).Format(time.RFC3339)}}
	if res := serveAPIGet(r, "/api/v1/usage/stored/events?"+q.Encode(), a); res.Code != 200 {
		t.Fatal(res.Code, res.Body.String())
	}
}

type storedHTTPFaultProvider struct {
	service.StoredUsageEventsProvider
	cancel  context.CancelFunc
	failure error
}

func (p storedHTTPFaultProvider) StreamStoredUsageEvents(ctx context.Context, f servicedto.UsageFilter, emit func(servicedto.StoredUsageEventRecord) error) error {
	return p.StoredUsageEventsProvider.StreamStoredUsageEvents(ctx, f, func(row servicedto.StoredUsageEventRecord) error {
		if err := emit(row); err != nil {
			return err
		}
		if p.cancel != nil {
			p.cancel()
		}
		return p.failure
	})
}
func TestStoredHTTPLateRealReaderFailureAndCancelAreNotSuccessfulExports(t *testing.T) {
	db := openAPITestDatabase(t)
	storedHTTPSeed(t, db, 3)
	sqlDB, _ := db.DB()
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			gate := service.NewCostReadGate()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider := storedHTTPFaultProvider{StoredUsageEventsProvider: service.NewStoredUsageEventsService(db), failure: errors.New("synthetic late reader error")}
			if cancelled {
				provider.cancel = cancel
				provider.failure = nil
			}
			r, a, _ := storedHTTPRouter(t, db, emptyPricingCatalogForTest(), gate, provider)
			for _, format := range []string{"csv", "json"} {
				req := httptest.NewRequest("GET", "/api/v1/usage/stored/events/export?range=24h&format="+format, nil).WithContext(ctx)
				req.AddCookie(a)
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if w.Code != 500 || strings.Contains(w.Body.String(), "stored_cost_usd") || w.Header().Get("Content-Disposition") != "" {
					t.Fatal("partial export claimed success", w.Code, w.Body.String())
				}
			}
			c, stop := context.WithTimeout(context.Background(), time.Second)
			defer stop()
			resume, err := gate.BlockAndDrain(c)
			if err != nil {
				t.Fatal(err)
			}
			resume()
			if sqlDB.Stats().InUse != 0 {
				t.Fatal(sqlDB.Stats())
			}
		})
	}
}

func TestStoredHTTPUnknownFeesNotFree(t *testing.T) {
	for _, tc := range []struct {
		name      string
		amount    any
		available any
		bad       bool
	}{{"null", nil, true, true}, {"null_available", 1.25, nil, true}, {"inf", math.Inf(1), true, true}, {"nan", math.NaN(), true, true}, {"false", 7.25, false, false}, {"zero", 0.0, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			db := openAPITestDatabase(t)
			storedHTTPSeed(t, db, 2)
			if err := db.Model(&entities.UsageEvent{}).Where("id = ?", 1).Updates(map[string]any{"cost_usd": tc.amount, "cost_available": tc.available}).Error; err != nil {
				t.Fatal(err)
			}
			r, a, _ := storedHTTPRouter(t, db, emptyPricingCatalogForTest(), nil)
			for _, path := range []string{"/api/v1/usage/stored/events?range=24h", "/api/v1/usage/stored/events/export?range=24h&format=json", "/api/v1/usage/stored/events/export?range=24h&format=csv"} {
				res := serveAPIGet(r, path, a)
				if tc.bad {
					if res.Code != 500 || strings.Contains(res.Body.String(), "stored_cost_usd") {
						t.Fatal("invalid fee accepted", res.Code, res.Body.String())
					}
				} else if res.Code != 200 {
					t.Fatal(res.Code, res.Body.String())
				} else if !strings.Contains(path, "format=csv") {
					p := storedDecode(t, res)
					e := p["events"].([]any)[1].(map[string]any)
					if e["stored_cost_usd"] != tc.amount || e["stored_cost_available"] != tc.available {
						t.Fatalf("lost numeric/availability pair: %+v", e)
					}
				} else {
					rows, err := csv.NewReader(strings.NewReader(res.Body.String())).ReadAll()
					if err != nil {
						t.Fatal(err)
					}
					values := map[string]string{}
					for i, k := range rows[0] {
						values[k] = rows[2][i]
					}
					if values["stored_cost_usd"] != fmt.Sprint(tc.amount) || values["stored_cost_available"] != fmt.Sprint(tc.available) {
						t.Fatal(values)
					}
				}
			}
		})
	}
}
