package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"cpa-usage-keeper/internal/auth"
	"cpa-usage-keeper/internal/config"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Use the real authenticated Gin handler and real SQLite provider; only now is
// fixed. NewRouter's complete registration/auth is covered in api/test.
func overviewBoundaryFixture(t *testing.T, zone string, now time.Time) (*gorm.DB, http.Handler, *http.Cookie) {
	t.Helper()
	old := time.Local
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
	db, err := repository.OpenDatabase(config.Config{SQLitePath: filepath.Join(t.TempDir(), "boundary.db")})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	sessions := auth.NewSessionManager(time.Hour)
	token, _, err := sessions.CreateWithSource(auth.SessionSourceStandard)
	if err != nil {
		t.Fatal(err)
	}
	cfg := AuthConfig{Enabled: true}
	a := NewAuthHandler(cfg, sessions)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(requestIntentMiddleware(), a.adminMiddleware(), costReadMiddleware(service.NewCostReadGate(), ""))
	registerStoredUsageOverviewRouteWithClock(g, service.NewStoredUsageOverviewService(db), func() time.Time { return now })
	return db, r, &http.Cookie{Name: sessionCookieName, Value: token}
}
func overviewBoundaryGet(t *testing.T, r http.Handler, cookie *http.Cookie, q url.Values) servicedto.StoredUsageOverview {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/usage/stored/overview?"+q.Encode(), nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out servicedto.StoredUsageOverview
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestStoredOverviewHTTPBoundaryRepeatedHoursSix(t *testing.T) {
	for _, tc := range []struct{ zone, start string }{{"Europe/Berlin", "2026-10-25T02:00:00+02:00"}, {"Asia/Shanghai", "2026-10-25T02:00:00+08:00"}} {
		t.Run(tc.zone, func(t *testing.T) {
			start, err := time.Parse(time.RFC3339, tc.start)
			if err != nil {
				t.Fatal(err)
			}
			end := start.Add(6 * time.Hour)
			db, r, a := overviewBoundaryFixture(t, tc.zone, end.Add(time.Hour))
			fee, n := 1.0, int64(0)
			var rows []entities.UsageOverviewHourlyStat
			for i := -1; i <= 6; i++ {
				rows = append(rows, entities.UsageOverviewHourlyStat{BucketStart: start.Add(time.Duration(i) * time.Hour).In(time.Local), APIGroupKey: "secret", Model: "model", RequestCount: 1, TotalTokens: 10, CostUSD: &fee, UnavailableCostCount: &n})
			}
			if err := db.Create(&rows).Error; err != nil {
				t.Fatal(err)
			}
			out := overviewBoundaryGet(t, r, a, url.Values{"range": {"custom"}, "unit": {"hour"}, "start": {start.Format(time.RFC3339)}, "end": {end.Add(-time.Hour).Format(time.RFC3339)}})
			if out.Summary.RequestCount != 6 || out.Summary.StoredCostUSD != 6 || len(out.Series.Requests) != 6 || !out.Range.End.Equal(end) {
				t.Fatalf("repeated hours collapsed: %+v", out)
			}
		})
	}
}
func TestStoredOverviewHTTPBoundaryMidnightLegacyDay(t *testing.T) {
	loc, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, loc)
	db, r, a := overviewBoundaryFixture(t, "America/Santiago", now)
	legacy := time.Date(2026, 9, 6, 0, 0, 0, 0, loc)
	fee, n := 2.0, int64(0)
	row := entities.UsageOverviewDailyStat{BucketStart: legacy, Model: "model", APIGroupKey: "secret", RequestCount: 2, TotalTokens: 100, CostUSD: &fee, UnavailableCostCount: &n}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	out := overviewBoundaryGet(t, r, a, url.Values{"range": {"custom"}, "unit": {"day"}, "start": {"2026-09-06"}, "end": {"2026-09-06"}})
	if out.Summary.StoredCostUSD != 2 || out.Series.Tokens["2026-09-06"] != 100 || out.Series.RPM["2026-09-06"] != 2.0/(23*60) {
		t.Fatal(out)
	}
	var after entities.UsageOverviewDailyStat
	if err := db.First(&after, row.ID).Error; err != nil || !after.BucketStart.Equal(legacy) {
		t.Fatal("legacy key mutated", after, err)
	}
}
func TestStoredOverviewHTTPBoundaryFractionalHalfOpenAndHotOnly(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 23, 45, 123456789, time.UTC)
	db, r, a := overviewBoundaryFixture(t, "UTC", now)
	fee, ok := 1.0, true
	start := now.Add(-24 * time.Hour)
	for i, at := range []time.Time{start.Add(-time.Nanosecond), start, now.Add(-time.Nanosecond), now} {
		row := entities.UsageEvent{EventKey: fmt.Sprint(i), Timestamp: at, Model: "model", CostUSD: &fee, CostAvailable: &ok, TotalTokens: 10}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("INSERT INTO usage_events_archive SELECT * FROM usage_events").Error; err != nil {
		t.Fatal(err)
	}
	out := overviewBoundaryGet(t, r, a, url.Values{"range": {"24h"}})
	if out.Summary.RequestCount != 2 || out.Summary.StoredCostUSD != 2 || !out.Range.Start.Equal(start) || !out.Range.End.Equal(now) || !out.Range.EndExclusive || out.Coverage.ArchiveCoverageAsserted {
		t.Fatal("fractional/hot boundaries changed", out)
	}
}
func TestStoredOverviewHTTPBoundaryTodayYesterdayClamp(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 23, 45, 0, time.UTC)
	db, r, a := overviewBoundaryFixture(t, "UTC", now)
	fee, ok := 1.0, true
	for i, at := range []time.Time{now.Add(-time.Minute), now.Add(-24 * time.Hour)} {
		if err := db.Create(&entities.UsageEvent{EventKey: fmt.Sprint(i), Timestamp: at, Model: "model", CostUSD: &fee, CostAvailable: &ok, TotalTokens: 10}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.AggregateUsageOverviewStats(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	today := overviewBoundaryGet(t, r, a, url.Values{"range": {"today"}})
	yesterday := overviewBoundaryGet(t, r, a, url.Values{"range": {"yesterday"}})
	if today.Summary.RequestCount != 1 || yesterday.Summary.RequestCount != 1 || !today.Range.End.Equal(now) || yesterday.Range.End.After(today.Range.Start) {
		t.Fatal(today, yesterday)
	}
}
