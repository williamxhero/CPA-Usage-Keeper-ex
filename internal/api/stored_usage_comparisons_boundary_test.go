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
func comparisonBoundaryFixture(t *testing.T, zone string, now time.Time) (*gorm.DB, http.Handler, *http.Cookie) {
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
	registerStoredUsageComparisonsRouteWithClock(g, service.NewStoredUsageComparisonsService(db), func() time.Time { return now })
	return db, r, &http.Cookie{Name: sessionCookieName, Value: token}
}

type comparisonBoundaryResult struct {
	servicedto.StoredUsageComparisons
	Summary servicedto.StoredUsageOverviewSummary
	Tokens  map[string]int64
}

func comparisonBoundaryGet(t *testing.T, r http.Handler, cookie *http.Cookie, q url.Values) comparisonBoundaryResult {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/usage/stored/comparisons?"+q.Encode(), nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var out servicedto.StoredUsageComparisons
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	summary := servicedto.StoredUsageOverviewSummary{}
	tokens := map[string]int64{}
	for _, v := range out.Models {
		summary.RequestCount += v.Requests
		summary.StoredCostUSD += v.StoredCostUSD
		for k, n := range v.TokenBuckets {
			tokens[k] += n
		}
	}
	return comparisonBoundaryResult{out, summary, tokens}
}
func TestStoredComparisonHTTPBoundaryRepeatedHoursSix(t *testing.T) {
	for _, tc := range []struct{ zone, start string }{{"Europe/Berlin", "2026-10-25T02:00:00+02:00"}, {"Asia/Shanghai", "2026-10-25T02:00:00+08:00"}} {
		t.Run(tc.zone, func(t *testing.T) {
			start, err := time.Parse(time.RFC3339, tc.start)
			if err != nil {
				t.Fatal(err)
			}
			end := start.Add(6 * time.Hour)
			db, r, a := comparisonBoundaryFixture(t, tc.zone, end.Add(time.Hour))
			fee, n := 1.0, int64(0)
			var rows []entities.UsageOverviewHourlyStat
			for i := -1; i <= 6; i++ {
				rows = append(rows, entities.UsageOverviewHourlyStat{BucketStart: start.Add(time.Duration(i) * time.Hour).In(time.Local), APIGroupKey: "secret", Model: "model", RequestCount: 1, TotalTokens: 10, CostUSD: &fee, UnavailableCostCount: &n})
			}
			if err := db.Create(&rows).Error; err != nil {
				t.Fatal(err)
			}
			out := comparisonBoundaryGet(t, r, a, url.Values{"range": {"custom"}, "unit": {"hour"}, "start": {start.Format(time.RFC3339)}, "end": {end.Add(-time.Hour).Format(time.RFC3339)}})
			if out.Summary.RequestCount != 6 || out.Summary.StoredCostUSD != 6 || len(out.Buckets) != 6 || !out.Range.End.Equal(end) {
				t.Fatalf("repeated hours collapsed: %+v", out)
			}
		})
	}
}
func TestStoredComparisonHTTPBoundaryMidnightLegacyDay(t *testing.T) {
	loc, err := time.LoadLocation("America/Santiago")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, loc)
	db, r, a := comparisonBoundaryFixture(t, "America/Santiago", now)
	legacy := time.Date(2026, 9, 6, 0, 0, 0, 0, loc)
	fee, n := 2.0, int64(0)
	row := entities.UsageOverviewDailyStat{BucketStart: legacy, Model: "model", APIGroupKey: "secret", RequestCount: 2, TotalTokens: 100, CostUSD: &fee, UnavailableCostCount: &n}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	out := comparisonBoundaryGet(t, r, a, url.Values{"range": {"custom"}, "unit": {"day"}, "start": {"2026-09-06"}, "end": {"2026-09-06"}})
	if out.Summary.StoredCostUSD != 2 || out.Tokens["2026-09-06"] != 100 {
		t.Fatal(out)
	}
	var after entities.UsageOverviewDailyStat
	if err := db.First(&after, row.ID).Error; err != nil || !after.BucketStart.Equal(legacy) {
		t.Fatal("legacy key mutated", after, err)
	}
}
func TestStoredComparisonHTTPBoundaryFractionalHalfOpenAndHotOnly(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 23, 45, 123456789, time.UTC)
	db, r, a := comparisonBoundaryFixture(t, "UTC", now)
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
	out := comparisonBoundaryGet(t, r, a, url.Values{"range": {"24h"}})
	if out.Summary.RequestCount != 2 || out.Summary.StoredCostUSD != 2 || !out.Range.Start.Equal(start) || !out.Range.End.Equal(now) || !out.Range.EndExclusive || out.Coverage.ArchiveCoverageAsserted {
		t.Fatal("fractional/hot boundaries changed", out)
	}
}
func TestStoredComparisonHTTPBoundaryTodayYesterdayClamp(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 23, 45, 0, time.UTC)
	db, r, a := comparisonBoundaryFixture(t, "UTC", now)
	fee, ok := 1.0, true
	for i, at := range []time.Time{now.Add(-time.Minute), now.Add(-24 * time.Hour)} {
		if err := db.Create(&entities.UsageEvent{EventKey: fmt.Sprint(i), Timestamp: at, Model: "model", CostUSD: &fee, CostAvailable: &ok, TotalTokens: 10}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := repository.AggregateUsageOverviewStats(context.Background(), db, now); err != nil {
		t.Fatal(err)
	}
	today := comparisonBoundaryGet(t, r, a, url.Values{"range": {"today"}})
	yesterday := comparisonBoundaryGet(t, r, a, url.Values{"range": {"yesterday"}})
	if today.Summary.RequestCount != 1 || yesterday.Summary.RequestCount != 1 || !today.Range.End.Equal(now) || yesterday.Range.End.After(today.Range.Start) {
		t.Fatal(today, yesterday)
	}
}

// The overview parser permits up to 365 calendar days, not the event reader's
// shorter retention window. A 303-day candidate rejection was a test error.
func TestStoredComparisonHTTPDayRangeContract(t *testing.T) {
	_, router, cookie := comparisonBoundaryFixture(t, "Asia/Shanghai", time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))
	for _, tc := range []struct {
		start, end string
		status     int
	}{
		{"2026-01-01", "2026-10-30", http.StatusOK},
		{"2025-10-31", "2026-10-30", http.StatusOK},
		{"2025-10-30", "2026-10-30", http.StatusBadRequest},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/usage/stored/comparisons?range=custom&unit=day&start="+tc.start+"&end="+tc.end, nil)
		req.AddCookie(cookie)
		out := httptest.NewRecorder()
		router.ServeHTTP(out, req)
		if out.Code != tc.status {
			t.Fatalf("%s..%s: status=%d want=%d body=%s", tc.start, tc.end, out.Code, tc.status, out.Body.String())
		}
	}
}
