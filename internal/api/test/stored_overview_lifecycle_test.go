package test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/service"
	"gorm.io/gorm"
)

func TestStoredOverviewHTTPReadOnlyRawRollupAndProviderLifecycle(t *testing.T) {
	db := openAPITestDatabase(t)
	fee, ok := 1.25, true
	overviewSeed(t, db, time.Now().Truncate(time.Hour).Add(time.Nanosecond), &fee, &ok)
	gate := service.NewCostReadGate()
	r, a, _ := overviewHTTPRouter(t, db, emptyPricingCatalogForTest(), gate)
	entered, proceed := make(chan struct{}), make(chan struct{})
	var once sync.Once
	callback := func(tx *gorm.DB) {
		sql := strings.ToLower(tx.Statement.SQL.String() + tx.Statement.Table)
		if strings.Contains(sql, "pric") || strings.Contains(sql, "archive") {
			t.Error("forbidden overview query", sql)
			tx.AddError(fmt.Errorf("forbidden query"))
		}
		if strings.Contains(sql, "usage_events") {
			once.Do(func() { close(entered); <-proceed })
		}
	}
	for _, cb := range []struct {
		add    func(string, func(*gorm.DB)) error
		remove func(string) error
	}{
		{db.Callback().Query().After("gorm:query").Register, db.Callback().Query().Remove}, {db.Callback().Row().After("gorm:row").Register, db.Callback().Row().Remove}, {db.Callback().Raw().After("gorm:raw").Register, db.Callback().Raw().Remove},
	} {
		if err := cb.add("overview_lifecycle_read", callback); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cb.remove("overview_lifecycle_read") })
	}
	write := func(tx *gorm.DB) { t.Error("stored overview wrote"); tx.AddError(fmt.Errorf("write forbidden")) }
	for _, cb := range []struct {
		add    func(string, func(*gorm.DB)) error
		remove func(string) error
	}{
		{db.Callback().Create().Before("gorm:create").Register, db.Callback().Create().Remove}, {db.Callback().Update().Before("gorm:update").Register, db.Callback().Update().Remove}, {db.Callback().Delete().Before("gorm:delete").Register, db.Callback().Delete().Remove},
	} {
		if err := cb.add("overview_lifecycle_write", write); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cb.remove("overview_lifecycle_write") })
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- serveAPIGet(r, "/api/v1/usage/stored/overview?range=24h", a) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("real SQLite reader not entered")
	}
	canceled, cancel := context.WithCancel(context.Background())
	// BlockAndDrain holds until the active HTTP handler releases its permit. Its
	// cancellation must reopen admissions without releasing that active permit.
	blocked := make(chan error, 1)
	go func() { _, err := gate.BlockAndDrain(canceled); blocked <- err }()
	// Observe the gate actually blocked while the real SQLite read is in flight;
	// bounded scheduler yields avoid sleep-based timing and pre-canceled-only proof.
	deadline := time.Now().Add(time.Second)
	for {
		release, admitted := gate.Acquire()
		if !admitted {
			break
		}
		release()
		if time.Now().After(deadline) {
			close(proceed)
			cancel()
			t.Fatal("drain never blocked active HTTP read")
		}
		runtime.Gosched()
	}
	select {
	case err := <-blocked:
		close(proceed)
		t.Fatal("drain returned before active reader released", err)
	default:
	}
	cancel()
	if err := <-blocked; err == nil {
		t.Fatal("drain accepted canceled context")
	}
	close(proceed)
	select {
	case res := <-done:
		if out := overviewDecode(t, res); out.Summary.StoredCostUSD != fee {
			t.Fatal(out)
		}
	case <-time.After(time.Second):
		t.Fatal("read did not finish")
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	resume, err := gate.BlockAndDrain(ctx)
	if err != nil {
		t.Fatal("active permit leaked", err)
	}
	resume()
	overviewDecode(t, serveAPIGet(r, "/api/v1/usage/stored/overview?range=24h", a))
}

func TestStoredOverviewHTTPRollupUnknownNeverFree(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fee       *float64
		unknown   *int64
		code      int
		available bool
	}{
		{"null-fee", nil, overviewInt(0), 500, false}, {"null-count", overviewFloat(0), nil, 500, false}, {"known-unavailable", overviewFloat(0), overviewInt(1), 200, false}, {"known-free", overviewFloat(0), overviewInt(0), 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openAPITestDatabase(t)
			at := time.Now().Truncate(time.Hour).Add(-2 * time.Hour)
			row := entities.UsageOverviewHourlyStat{BucketStart: at, Model: "model", RequestCount: 1, CostUSD: tc.fee, UnavailableCostCount: tc.unknown}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			r, a, _ := overviewHTTPRouter(t, db, emptyPricingCatalogForTest(), nil)
			w := serveAPIGet(r, "/api/v1/usage/stored/overview?"+overviewCustomHour(), a)
			if w.Code != tc.code {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.code == 200 {
				out := overviewDecode(t, w)
				if out.Summary.StoredCostUSD != 0 || out.Summary.StoredCostAvailable != tc.available {
					t.Fatal(out)
				}
			}
		})
	}
}
func overviewInt(v int64) *int64 { return &v }
