package test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
	"cpa-usage-keeper/internal/timeutil"
	"gorm.io/gorm"
)

func storedOverviewFactsHash(t *testing.T, db *gorm.DB) [32]byte {
	t.Helper()
	tables := []string{"usage_events", "usage_events_archive", "usage_overview_hourly_stats", "usage_overview_daily_stats", "usage_aggregation_checkpoints"}
	all := map[string][]map[string]any{}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		rows, err := sqlDB.Query("SELECT * FROM " + table)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		var batch []map[string]any
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			row := map[string]any{}
			for i, c := range cols {
				row[c] = values[i]
			}
			batch = append(batch, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		all[table] = batch
	}
	b, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func TestStoredOverviewCurrentNineToNinetyAndReadOnly(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db := openTestDatabase(t)
	at := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	end := at.Add(time.Hour)
	fee, zero := 1.25, int64(0)
	upsertUsageCostResolverPrice(t, db, "priced", 9)
	if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: at, Model: "priced", RequestCount: 1, InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: &fee, UnavailableCostCount: &zero}).Error; err != nil {
		t.Fatal(err)
	}
	f := dto.UsageQueryFilter{Range: "custom", CustomUnit: "hour", StartTime: &at, EndTime: &end, EndExclusive: true}
	for _, price := range []float64{9, 90} {
		upsertUsageCostResolverPrice(t, db, "priced", price)
		current, err := repository.BuildUsageOverviewWithFilterAndRecentCache(db, f, nil, newUsageCostResolverForTest(t, db))
		if err != nil || current.Summary.TotalCost != price {
			t.Fatalf("current=%+v err=%v", current, err)
		}
		stored, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, f, nil)
		if err != nil || stored.Summary.StoredCostUSD != 1.25 || !stored.Summary.StoredCostAvailable {
			t.Fatalf("price %v changed stored amount %+v %v", price, stored, err)
		}
	}
	// Ordinary nil resolver is still unknown, not an implicit stored reader.
	plain, err := repository.BuildUsageOverviewWithFilterAndRecentCache(db, f, nil)
	if err != nil || plain.Summary.TotalCost != 0 || plain.Summary.CostAvailable {
		t.Fatalf("nil inferred stored: %+v %v", plain, err)
	}
	before := storedOverviewFactsHash(t, db)
	queries := storedOverviewRejectSideEffects(t, db)
	r, err := repository.NewStoredUsageOverviewReader(db)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		out, err := r.Read(f)
		if err != nil || out.Summary.StoredCostUSD != 1.25 || !out.Summary.StoredCostAvailable || out.Series.StoredCostUSD[timeutil.FormatStorageTime(at)] != 1.25 {
			t.Fatalf("stored=%+v err=%v", out, err)
		}
	}
	if before != storedOverviewFactsHash(t, db) {
		t.Fatal("reader mutated fee/facts/checkpoints")
	}
	for _, q := range *queries {
		if strings.Contains(q, "usage_events") {
			t.Fatalf("retained complete rollup queried raw: %s", q)
		}
	}
	typ := reflect.TypeFor[dto.StoredUsageOverviewSummaryRecord]()
	for _, name := range []string{"TotalCost", "CostAvailable", "DualCosts", "PricingSnapshotID"} {
		if _, ok := typ.FieldByName(name); ok {
			t.Fatalf("current field leaked: %s", name)
		}
	}
}

func TestStoredOverviewFeePairsAndPrunedRaw(t *testing.T) {
	for _, source := range []string{"raw", "hourly", "daily"} {
		for _, tc := range []struct {
			name      string
			cost      *float64
			available *bool
			bad       bool
		}{
			{"null_amount", nil, windowAvailablePtr(true), true}, {"null_availability", windowCostPtr(0), nil, true}, {"infinity", windowCostPtr(math.Inf(1)), windowAvailablePtr(true), true}, {"nan", windowCostPtr(math.NaN()), windowAvailablePtr(true), true}, {"free", windowCostPtr(0), windowAvailablePtr(true), false}, {"unavailable_numeric", windowCostPtr(7.25), windowAvailablePtr(false), false},
		} {
			t.Run(source+"/"+tc.name, func(t *testing.T) {
				withRepositoryTestLocation(t, "UTC")
				db := openTestDatabase(t)
				start := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
				end := start.Add(time.Hour)
				f := dto.UsageQueryFilter{Range: "custom", CustomUnit: "hour", StartTime: &start, EndTime: &end, EndExclusive: true}
				var count *int64
				if tc.available != nil {
					v := int64(0)
					if !*tc.available {
						v = 1
					}
					count = &v
				}
				switch source {
				case "raw":
					f.CustomUnit = ""
					end = start.Add(15 * time.Minute)
					if err := db.Create(&entities.UsageEvent{Timestamp: start.Add(time.Minute), CostUSD: tc.cost, CostAvailable: tc.available, TotalTokens: 10}).Error; err != nil {
						t.Fatal(err)
					}
				case "hourly":
					if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: start, RequestCount: 1, CostUSD: tc.cost, UnavailableCostCount: count, TotalTokens: 10}).Error; err != nil {
						t.Fatal(err)
					}
				case "daily":
					f.CustomUnit = "day"
					end = start.AddDate(0, 0, 1)
					if err := db.Create(&entities.UsageOverviewDailyStat{BucketStart: start, RequestCount: 1, CostUSD: tc.cost, UnavailableCostCount: count, TotalTokens: 10}).Error; err != nil {
						t.Fatal(err)
					}
				}
				if source != "raw" {
					if err := db.Migrator().DropTable(&entities.UsageEvent{}); err != nil {
						t.Fatal(err)
					}
				}
				storedOverviewRejectSideEffects(t, db)
				out, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, f, nil)
				if tc.bad {
					if err == nil || out != nil {
						t.Fatalf("unknown fee accepted: %+v %v", out, err)
					}
					return
				}
				if err != nil || out.Summary.StoredCostUSD != *tc.cost || out.Summary.StoredCostAvailable != *tc.available || out.Summary.TokenCount != 10 {
					t.Fatalf("pair lost %+v %v", out, err)
				}
			})
		}
	}
}

func TestStoredOverviewFractionalHalfOpenEdgesAndExactQueries(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db := openTestDatabase(t)
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	start := base.Add(-time.Minute).Add(123456789)
	end := base.Add(time.Hour).Add(time.Minute).Add(123456789)
	fee, yes, zero := 2.0, true, int64(0)
	for i, at := range []time.Time{start.Add(-time.Nanosecond), start, base.Add(time.Minute), end.Add(-time.Nanosecond), end} {
		cost := fee
		if i == 2 {
			cost = 999
		}
		if err := db.Create(&entities.UsageEvent{Timestamp: at, CostUSD: &cost, CostAvailable: &yes, TotalTokens: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: base, RequestCount: 1, TotalTokens: 5, CostUSD: windowCostPtr(3), UnavailableCostCount: &zero}).Error; err != nil {
		t.Fatal(err)
	}
	queries := storedOverviewRejectSideEffects(t, db)
	out, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, dto.UsageQueryFilter{Range: "4h", StartTime: &start, EndTime: &end, QueryNow: &end}, nil)
	if err != nil || out.Summary.StoredCostUSD != 7 || out.Summary.RequestCount != 3 || out.Summary.TokenCount != 7 {
		t.Fatalf("bounds/double count %+v %v", out, err)
	}
	if len(*queries) != 3 {
		t.Fatalf("expected 1 hourly+2 narrow raw reads: %v", *queries)
	}
	for _, q := range *queries {
		if strings.Contains(q, "usage_events") && (!strings.Contains(q, "timestamp >=") || !strings.Contains(q, "timestamp <") || strings.Contains(q, "archive")) {
			t.Fatalf("raw range unbounded: %s", q)
		}
	}
}

func TestStoredOverviewBoundedInputsAndCancellation(t *testing.T) {
	db := openTestDatabase(t)
	at := time.Now()
	end := at.Add(time.Hour)
	for _, f := range []dto.UsageQueryFilter{{}, {StartTime: &at}, {EndTime: &end}, {StartTime: &end, EndTime: &at}, {StartTime: &at, EndTime: &end, ComparisonOnly: true}} {
		if out, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, f, nil); err == nil || out != nil {
			t.Fatalf("unbounded/unsupported accepted %+v %v", out, err)
		}
	}
	if _, err := repository.NewStoredUsageOverviewReader(nil); err == nil {
		t.Fatal("nil database accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db.WithContext(ctx), dto.UsageQueryFilter{StartTime: &at, EndTime: &end}, nil)
	if !errors.Is(err, context.Canceled) || out != nil {
		t.Fatalf("cancellation ignored %+v %v", out, err)
	}
}

func TestStoredOverviewLongWindowDisjointDailyHourlyAndRaw(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db := openTestDatabase(t)
	day := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	start := day.Add(30 * time.Minute)
	end := day.AddDate(0, 0, 8).Add(90 * time.Minute)
	zero := int64(0)
	yes := true
	// Only the daily interior should count, not matching hourly or raw decoys.
	for i := 1; i < 8; i++ {
		at := day.AddDate(0, 0, i)
		fee := 10.0
		if err := db.Create(&entities.UsageOverviewDailyStat{BucketStart: at, RequestCount: 1, TotalTokens: 10, CostUSD: &fee, UnavailableCostCount: &zero}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: at, CostUSD: windowCostPtr(999), UnavailableCostCount: &zero}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, at := range []time.Time{day.Add(time.Hour), day.AddDate(0, 0, 8)} {
		if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: at, RequestCount: 1, TotalTokens: 2, CostUSD: windowCostPtr(2), UnavailableCostCount: &zero}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, at := range []time.Time{start, end.Add(-time.Second)} {
		if err := db.Create(&entities.UsageEvent{Timestamp: at, CostUSD: windowCostPtr(3), CostAvailable: &yes, TotalTokens: 3}).Error; err != nil {
			t.Fatal(err)
		}
	}
	queries := storedOverviewRejectSideEffects(t, db)
	out, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, dto.UsageQueryFilter{Range: "7d", StartTime: &start, EndTime: &end, QueryNow: &end}, nil)
	if err != nil || out.Summary.StoredCostUSD != 80 || out.Summary.RequestCount != 11 || out.Summary.TokenCount != 80 {
		t.Fatalf("long sources overlap/lost %+v %v", out, err)
	}
	if len(*queries) != 5 {
		t.Fatalf("expected daily+2hourly+2raw: %v", *queries)
	}
	t.Logf("stored overview exact source SQL: %v", *queries)
	if out.Summary.DailyAverageStoredCostUSD == nil || math.Abs(*out.Summary.DailyAverageStoredCostUSD-80/(193.0/24)) > 1e-12 {
		t.Fatalf("daily average lost %+v", out.Summary)
	}
}

func TestStoredOverviewDSTBoundarySegmentsDoNotScanHistory(t *testing.T) {
	for _, zone := range []string{"Europe/Berlin", "Asia/Kathmandu", "America/Santiago"} {
		t.Run(zone, func(t *testing.T) {
			withRepositoryTestLocation(t, zone)
			db := openTestDatabase(t)
			start := time.Date(2026, 10, 25, 0, 30, 0, 125000000, time.UTC).In(time.Local)
			end := start.Add(2 * time.Hour)
			if zone == "America/Santiago" {
				start = time.Date(2026, 9, 6, 3, 30, 0, 125000000, time.UTC).In(time.Local)
				end = start.Add(2 * time.Hour)
			}
			// In all zones the interior is exactly one absolute full hour.
			full := start.Truncate(time.Hour).Add(time.Hour)
			stop := end.Truncate(time.Hour)
			zero := int64(0)
			yes := true
			for _, at := range []time.Time{start, start.Add(-time.Nanosecond), end.Add(-time.Nanosecond), end, full.Add(time.Minute)} {
				if err := db.Create(&entities.UsageEvent{Timestamp: at, CostUSD: windowCostPtr(2), CostAvailable: &yes, TotalTokens: 1}).Error; err != nil {
					t.Fatal(err)
				}
			}
			for at := full; at.Before(stop); at = at.Add(time.Hour) {
				if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: at, RequestCount: 1, TotalTokens: 5, CostUSD: windowCostPtr(3), UnavailableCostCount: &zero}).Error; err != nil {
					t.Fatal(err)
				}
			}
			queries := storedOverviewRejectSideEffects(t, db)
			out, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, dto.UsageQueryFilter{Range: "4h", StartTime: &start, EndTime: &end, QueryNow: &end}, nil)
			if err != nil || out.Summary.StoredCostUSD != 7 || out.Summary.RequestCount != 3 || out.Summary.TokenCount != 7 {
				t.Fatalf("DST raw/rollup bounds %+v %v", out, err)
			}
			for _, q := range *queries {
				if strings.Contains(q, "from `usage_events`") && (!strings.Contains(q, "timestamp >=") || !strings.Contains(q, "timestamp <")) {
					t.Fatalf("unbounded DST raw: %s", q)
				}
			}
			t.Logf("%s exact source queries: %v", zone, *queries)
		})
	}
}

func TestStoredOverviewReaderPoolPinsDisjointSources(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db, writerSQL, readerSQL := openTestDatabasePools(t, "stored-overview-snapshot.db")
	at := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	start := at.Add(-15 * time.Minute)
	end := at.Add(75 * time.Minute)
	zero := int64(0)
	yes := true
	if err := db.Create(&entities.UsageOverviewHourlyStat{BucketStart: at, RequestCount: 1, CostUSD: windowCostPtr(3), UnavailableCostCount: &zero}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entities.UsageEvent{Timestamp: start.Add(time.Minute), CostUSD: windowCostPtr(2), CostAvailable: &yes}).Error; err != nil {
		t.Fatal(err)
	}
	changed := false
	after := func(tx *gorm.DB) {
		sql := strings.ToLower(tx.Statement.SQL.String())
		if !changed && strings.Contains(sql, "from `usage_overview_hourly_stats`") {
			changed = true
			if _, err := writerSQL.Exec("UPDATE usage_events SET cost_usd=200"); err != nil {
				tx.AddError(err)
			}
		}
	}
	if err := db.Callback().Query().After("gorm:query").Register("stored_overview_snapshot", after); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove("stored_overview_snapshot") })
	out, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, dto.UsageQueryFilter{Range: "4h", StartTime: &start, EndTime: &end, QueryNow: &end}, nil)
	if err != nil || !changed || out.Summary.StoredCostUSD != 5 || out.Summary.RequestCount != 2 {
		t.Fatalf("mixed reader snapshot %+v %v changed=%t", out, err, changed)
	}
	var fee float64
	if err := readerSQL.QueryRow("SELECT cost_usd FROM usage_events").Scan(&fee); err != nil || fee != 200 {
		t.Fatalf("fixture concurrent commit missing %v %v", fee, err)
	}
	// Exhaust the writer pool: the second read must still use the read pool.
	writerSQL.SetMaxOpenConns(1)
	held, err := writerSQL.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	second, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db.WithContext(ctx), dto.UsageQueryFilter{Range: "4h", StartTime: &start, EndTime: &end, QueryNow: &end}, nil)
	cancel()
	held.Close()
	if err != nil || second.Summary.StoredCostUSD != 203 {
		t.Fatalf("read routed through exhausted writer %+v %v", second, err)
	}
	if readerSQL.Stats().InUse != 0 || writerSQL.Stats().InUse != 0 {
		t.Fatalf("read connection leaked reader=%+v writer=%+v", readerSQL.Stats(), writerSQL.Stats())
	}
}

// Event helper guards all reads/writes; overview additionally captures Find's
// fully consumed rollup queries so exact source-query counts remain meaningful.
func storedOverviewRejectSideEffects(t *testing.T, db *gorm.DB) *[]string {
	queries := storedEventsRejectSideEffects(t, db)
	if err := db.Callback().Query().After("gorm:query").Register("stored_overview_capture", func(tx *gorm.DB) { *queries = append(*queries, strings.ToLower(tx.Statement.SQL.String())) }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Query().Remove("stored_overview_capture") })
	return queries
}

func TestStoredOverviewRawRangeVarsAndNoMetadataProjection(t *testing.T) {
	withRepositoryTestLocation(t, "UTC")
	db := openTestDatabase(t)
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	start := base.Add(-time.Minute).Add(123456789)
	end := base.Add(time.Hour).Add(time.Minute).Add(123456789)
	type query struct {
		sql  string
		vars []any
	}
	var seen []query
	capture := func(tx *gorm.DB) {
		seen = append(seen, query{tx.Statement.SQL.String(), append([]any(nil), tx.Statement.Vars...)})
	}
	db.Callback().Row().After("gorm:row").Register("stored_overview_vars", capture)
	db.Callback().Query().After("gorm:query").Register("stored_overview_vars", capture)
	t.Cleanup(func() {
		db.Callback().Row().Remove("stored_overview_vars")
		db.Callback().Query().Remove("stored_overview_vars")
	})
	out, err := repository.BuildStoredUsageOverviewWithFilterAndRecentCache(db, dto.UsageQueryFilter{Range: "4h", StartTime: &start, EndTime: &end, QueryNow: &end, APIGroupKey: "selected"}, nil)
	if err != nil || out.Summary.RequestCount != 0 {
		t.Fatalf("empty bounded read %+v %v", out, err)
	}
	if len(seen) != 3 {
		t.Fatalf("expected exact three source queries %+v", seen)
	}
	want := [][]any{{timeutil.FormatStorageTime(base), timeutil.FormatStorageTime(base.Add(time.Hour)), "selected"}, {"2026-07-20T11:59:00", "2026-07-20T12:00:01", "selected"}, {"2026-07-20T13:00:00", "2026-07-20T13:01:01", "selected"}}
	// First rollup projection also binds finite fee validation constants.
	if !reflect.DeepEqual(seen[0].vars[2:], want[0]) {
		t.Fatalf("rollup bounds changed %+v", seen[0])
	}
	for i := 1; i < 3; i++ {
		if !reflect.DeepEqual(seen[i].vars, want[i]) {
			t.Fatalf("raw edge %d widened %+v want %+v", i, seen[i], want[i])
		}
		for _, forbidden := range []string{"model", "auth_index", "auth_type", "request_id", "select *"} {
			if strings.Contains(strings.ToLower(seen[i].sql), forbidden) {
				t.Fatalf("nonessential boundary projection %s", seen[i].sql)
			}
		}
	}
	t.Logf("exact source SQL and bound parameters: %+v", seen)
}
