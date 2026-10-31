package test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
	"gorm.io/gorm"
)

func storedEventsRejectSideEffects(t *testing.T, db *gorm.DB) *[]string {
	t.Helper()
	queries := []string{}
	reject := func(tx *gorm.DB) {
		sql := strings.ToLower(tx.Statement.SQL.String())
		verb := strings.TrimSpace(sql)
		if strings.HasPrefix(verb, "update ") || strings.HasPrefix(verb, "insert ") || strings.HasPrefix(verb, "delete ") || strings.HasPrefix(verb, "replace ") || strings.HasPrefix(verb, "create ") || strings.HasPrefix(verb, "alter ") || strings.HasPrefix(verb, "drop ") {
			tx.AddError(fmt.Errorf("stored reader raw write: %s", sql))
			t.Errorf("stored reader raw write: %s", sql)
		}
		if strings.Contains(sql+tx.Statement.Table, "pric") || strings.Contains(sql, "archive") {
			tx.AddError(fmt.Errorf("forbidden stored event read: %s", sql))
			t.Errorf("forbidden read: %s", sql)
		}
	}
	write := func(tx *gorm.DB) {
		tx.AddError(errors.New("stored reader wrote DB"))
		t.Error("stored reader wrote DB")
	}
	for _, c := range []struct {
		register func(string, func(*gorm.DB)) error
		remove   func(string) error
	}{
		{db.Callback().Query().After("gorm:query").Register, db.Callback().Query().Remove},
		{db.Callback().Row().After("gorm:row").Register, db.Callback().Row().Remove},
		{db.Callback().Raw().After("gorm:raw").Register, db.Callback().Raw().Remove},
	} {
		if err := c.register("stored_events_no_price", reject); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.remove("stored_events_no_price") })
	}
	for _, c := range []struct {
		register func(string, func(*gorm.DB)) error
		remove   func(string) error
	}{
		{db.Callback().Create().Before("gorm:create").Register, db.Callback().Create().Remove},
		{db.Callback().Update().Before("gorm:update").Register, db.Callback().Update().Remove},
		{db.Callback().Delete().Before("gorm:delete").Register, db.Callback().Delete().Remove},
	} {
		if err := c.register("stored_events_no_write", write); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.remove("stored_events_no_write") })
	}
	if err := db.Callback().Row().After("gorm:row").Register("stored_events_capture", func(tx *gorm.DB) {
		queries = append(queries, strings.ToLower(tx.Statement.SQL.String()))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Callback().Row().Remove("stored_events_capture") })
	return &queries
}

func TestStoredEventsCurrentOppositeFeesAndPriceMutation(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		current, next, stored float64
	}{{"opposite", 100, 200, 33.44}, {"immutable", 9, 90, 1.25}} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDatabase(t)
			at := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
			upsertUsageCostResolverPrice(t, db, "priced", tc.current)
			if err := db.Create(&entities.UsageEvent{EventKey: "fees", Timestamp: at, Model: "priced", InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: &tc.stored, CostAvailable: windowAvailablePtr(true)}).Error; err != nil {
				t.Fatal(err)
			}
			current, err := repository.ListUsageEventsWithFilter(db, dto.UsageQueryFilter{}, newUsageCostResolverForTest(t, db))
			if err != nil {
				t.Fatal(err)
			}
			if len(current.Events) != 1 || current.Events[0].CostUSD != tc.current {
				t.Fatalf("current changed: %+v", current)
			}
			upsertUsageCostResolverPrice(t, db, "priced", tc.next)
			resolver := newUsageCostResolverForTest(t, db)
			current, err = repository.ListUsageEventsWithFilter(db, dto.UsageQueryFilter{}, resolver)
			if err != nil || current.Events[0].CostUSD != tc.next {
				t.Fatalf("current must reprice: %+v %v", current, err)
			}
			storedEventsRejectSideEffects(t, db)
			for i := 0; i < 2; i++ {
				page, err := repository.ListStoredUsageEventsWithFilter(db, dto.UsageQueryFilter{})
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Events) != 1 || page.Events[0].StoredCostUSD != tc.stored || !page.Events[0].StoredCostAvailable || page.DataSource != "usage_events" {
					t.Fatalf("stored drift: %+v", page)
				}
				n := 0
				err = repository.StreamStoredUsageEventsWithFilter(db, dto.UsageQueryFilter{}, func(row dto.StoredUsageEventRecord) error {
					n++
					if row.StoredCostUSD != tc.stored || !row.StoredCostAvailable {
						t.Fatalf("stream drift: %+v", row)
					}
					return nil
				})
				if err != nil || n != 1 {
					t.Fatalf("stream: %d %v", n, err)
				}
			}
			// Omitting the current resolver must not select persisted mode.
			without, err := repository.ListUsageEventsWithFilter(db, dto.UsageQueryFilter{})
			if err != nil || without.Events[0].CostUSD != 0 || without.Events[0].CostAvailable {
				t.Fatalf("nil resolver inferred stored: %+v %v", without, err)
			}
		})
	}
}

func TestStoredEventsFeeValidationAndKnownZeroUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cost      *float64
		available *bool
		bad       bool
	}{
		{"null_amount", nil, windowAvailablePtr(true), true}, {"null_availability", windowCostPtr(0), nil, true},
		{"positive_inf", windowCostPtr(math.Inf(1)), windowAvailablePtr(true), true}, {"negative_inf_unavailable", windowCostPtr(math.Inf(-1)), windowAvailablePtr(false), true},
		{"nan", windowCostPtr(math.NaN()), windowAvailablePtr(true), true},
		{"free", windowCostPtr(0), windowAvailablePtr(true), false}, {"unavailable_zero", windowCostPtr(0), windowAvailablePtr(false), false},
		{"unavailable_subtotal", windowCostPtr(7.25), windowAvailablePtr(false), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDatabase(t)
			if err := db.Create(&entities.UsageEvent{EventKey: tc.name, Timestamp: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC), CostUSD: tc.cost, CostAvailable: tc.available}).Error; err != nil {
				t.Fatal(err)
			}
			storedEventsRejectSideEffects(t, db)
			page, err := repository.ListStoredUsageEventsWithFilter(db, dto.UsageQueryFilter{})
			emitted := 0
			streamErr := repository.StreamStoredUsageEventsWithFilter(db, dto.UsageQueryFilter{}, func(r dto.StoredUsageEventRecord) error { emitted++; return nil })
			if tc.bad {
				if err == nil || streamErr == nil || emitted != 0 || page != nil {
					t.Fatalf("invalid fee accepted: %+v %v %v emitted=%d", page, err, streamErr, emitted)
				}
				return
			}
			if err != nil || streamErr != nil || emitted != 1 || len(page.Events) != 1 || page.Events[0].StoredCostUSD != *tc.cost || page.Events[0].StoredCostAvailable != *tc.available {
				t.Fatalf("fee pair lost: %+v %v %v", page, err, streamErr)
			}
		})
	}
}

func TestStoredEventsFiltersCursorPageMetadataAndHotOnlyCoverage(t *testing.T) {
	db := openTestDatabase(t)
	start := time.Date(2026, 7, 20, 12, 0, 0, 123456789, time.UTC)
	end := start.Add(time.Hour)
	alias, ip, ua := " alias ", "127.0.0.1", "fixture-agent"
	ttft := int64(45)
	status := 201
	stream := true
	base := entities.UsageEvent{APIGroupKey: "group", Model: "model", ModelAlias: &alias, ResponseModel: "response", ServiceTier: "priority", EffectiveServiceTier: "flex", ResponseServiceTier: "auto", ReasoningEffort: "xhigh", AuthType: "oauth", AuthIndex: "shared", Provider: "provider", Source: "source", RequestID: "request", Endpoint: "endpoint", ExecutorType: "responses", ClientIP: &ip, UserAgent: &ua, TTFTMS: &ttft, StatusCode: &status, Stream: &stream, LatencyMS: 90, InputTokens: 8, OutputTokens: 2, TotalTokens: 10, CostUSD: windowCostPtr(3.5), CostAvailable: windowAvailablePtr(true)}
	var wanted []int64
	for i := 0; i < 9; i++ {
		e := base
		e.EventKey = fmt.Sprint(i)
		e.Timestamp = start
		switch i {
		case 0:
			e.Timestamp = start.Add(-time.Second)
		case 1:
			e.Timestamp = end
		case 2:
			e.APIGroupKey = "other"
		case 3:
			e.Model = "alias"
		case 4:
			e.AuthType = "apikey"
		case 5:
			e.AuthIndex = "other"
		case 6:
			e.Failed = true
		}
		if err := db.Create(&e).Error; err != nil {
			t.Fatal(err)
		}
		if i >= 7 {
			wanted = append(wanted, e.ID)
		}
	}
	// Hot/archive duplicate plus archive-only history must not expand Events' existing hot-table contract.
	for _, e := range []entities.UsageEventArchive{{ID: wanted[0], EventKey: "duplicate", Timestamp: start, CostUSD: windowCostPtr(999), CostAvailable: windowAvailablePtr(true)}, {ID: 999, EventKey: "archive-only", Timestamp: start, CostUSD: windowCostPtr(999), CostAvailable: windowAvailablePtr(true)}} {
		if err := db.Create(&e).Error; err != nil {
			t.Fatal(err)
		}
	}
	queries := storedEventsRejectSideEffects(t, db)
	filter := dto.UsageQueryFilter{StartTime: &start, EndTime: &end, EndExclusive: true, APIGroupKey: " group ", Model: " model ", AuthIndex: " shared ", AuthType: " oauth ", Result: "success", PageSize: 1, CursorMode: true}
	first, err := repository.ListStoredUsageEventsWithFilter(db, filter)
	if err != nil {
		t.Fatal(err)
	}
	if first.TotalCount != 2 || first.TotalPages != 2 || !first.HasMore || len(first.Events) != 1 || first.Events[0].Metadata.ID != wanted[1] {
		t.Fatalf("first cursor: %+v", first)
	}
	m := first.Events[0].Metadata
	if m.Model != "model" || m.ModelAlias != "alias" || m.ResponseModel != "response" || m.ServiceTier != "priority" || m.EffectiveServiceTier != "flex" || m.ResponseServiceTier != "auto" || m.ReasoningEffort != "xhigh" || m.AuthIndex != "shared" || m.AuthType != "oauth" || m.Provider != "provider" || m.RequestID != "request" || m.Source != "source" || m.ClientIP == nil || *m.ClientIP != ip || m.UserAgent == nil || *m.UserAgent != ua || m.TTFTMS == nil || *m.TTFTMS != ttft || m.StatusCode == nil || *m.StatusCode != status || m.Stream == nil || !*m.Stream || m.TotalTokens != 10 {
		t.Fatalf("metadata lost: %+v", m)
	}
	filter.CursorTimestamp = &m.Timestamp
	filter.CursorID = m.ID
	second, err := repository.ListStoredUsageEventsWithFilter(db, filter)
	if err != nil || second.TotalCount != -1 || second.HasMore || len(second.Events) != 1 || second.Events[0].Metadata.ID != wanted[0] {
		t.Fatalf("continuation: %+v %v", second, err)
	}
	filter.CursorMode = false
	filter.CursorTimestamp = nil
	filter.Page = 2
	filter.SkipTotalCount = true
	page, err := repository.ListStoredUsageEventsWithFilter(db, filter)
	if err != nil || page.TotalCount != -1 || len(page.Events) != 1 || page.Events[0].Metadata.ID != wanted[0] {
		t.Fatalf("offset page: %+v %v", page, err)
	}
	filter.Page = 1
	filter.Offset = 1
	page, err = repository.ListStoredUsageEventsWithFilter(db, filter)
	if err != nil || page.Events[0].Metadata.ID != wanted[0] {
		t.Fatalf("explicit offset: %+v %v", page, err)
	}
	var ids []int64
	err = repository.StreamStoredUsageEventsWithFilter(db, filter, func(r dto.StoredUsageEventRecord) error { ids = append(ids, r.Metadata.ID); return nil })
	if err != nil || !slices.Equal(ids, []int64{wanted[1], wanted[0]}) {
		t.Fatalf("stream pagination leak: %v %v", ids, err)
	}
	text := strings.Join(*queries, "\n")
	t.Logf("stored event SQL snapshots:\n%s", text)
	for _, required := range []string{"cost_usd", "cost_available", "timestamp >=", "timestamp <", "api_group_key =", "model =", "auth_index =", "auth_type =", "failed =", "id <", "limit 2", "offset 1"} {
		if !strings.Contains(text, required) {
			t.Fatalf("SQL misses %s: %s", required, text)
		}
	}
	for _, sql := range *queries {
		if strings.HasPrefix(sql, "select count") {
			continue
		}
		if strings.Contains(sql, "select *") || strings.Contains(sql, "session_id") || strings.Contains(sql, "event_key") {
			t.Fatalf("unbounded projection: %s", sql)
		}
	}
}

func TestStoredEventsStreamCancellationErrorAndInputs(t *testing.T) {
	if _, err := repository.ListStoredUsageEventsWithFilter(nil, dto.UsageQueryFilter{}); err == nil {
		t.Fatal("nil DB accepted")
	}
	if err := repository.StreamStoredUsageEventsWithFilter(nil, dto.UsageQueryFilter{}, nil); err == nil {
		t.Fatal("nil inputs accepted")
	}
	db := openTestDatabase(t)
	at := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := db.Create(&entities.UsageEvent{EventKey: fmt.Sprint(i), Timestamp: at, CostUSD: windowCostPtr(1), CostAvailable: windowAvailablePtr(true)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	storedEventsRejectSideEffects(t, db)
	if err := repository.StreamStoredUsageEventsWithFilter(db, dto.UsageQueryFilter{}, nil); err == nil {
		t.Fatal("nil emit accepted")
	}
	sentinel := errors.New("stop")
	n := 0
	err := repository.StreamStoredUsageEventsWithFilter(db, dto.UsageQueryFilter{}, func(dto.StoredUsageEventRecord) error { n++; return sentinel })
	if !errors.Is(err, sentinel) || n != 1 {
		t.Fatalf("callback error: %d %v", n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	n = 0
	err = repository.StreamStoredUsageEventsWithFilter(db.WithContext(ctx), dto.UsageQueryFilter{}, func(dto.StoredUsageEventRecord) error { n++; cancel(); return nil })
	if !errors.Is(err, context.Canceled) || n != 1 {
		t.Fatalf("cancel ignored: %d %v", n, err)
	}
	n = 0
	err = repository.StreamStoredUsageEventsWithFilter(db.WithContext(ctx), dto.UsageQueryFilter{}, func(dto.StoredUsageEventRecord) error { n++; return nil })
	if !errors.Is(err, context.Canceled) || n != 0 {
		t.Fatalf("pre-cancel ignored: %d %v", n, err)
	}
	if _, err := repository.ListStoredUsageEventsWithFilter(db.WithContext(ctx), dto.UsageQueryFilter{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("list cancellation: %v", err)
	}
	// A maximum-size bounded list remains limited (no cursor overread integer overflow).
	if _, err := repository.ListStoredUsageEventsWithFilter(db, dto.UsageQueryFilter{PageSize: math.MaxInt, CursorMode: true}); err == nil {
		t.Fatal("overflow became unbounded query")
	}
	if _, err := repository.ListStoredUsageEventsWithFilter(db, dto.UsageQueryFilter{Page: math.MaxInt, PageSize: 100}); err == nil {
		t.Fatal("offset overflow accepted")
	}
	encoded, err := json.Marshal(dto.StoredUsageEventRecord{StoredCostUSD: 7.25, StoredCostAvailable: false})
	if err != nil || !strings.Contains(string(encoded), `"stored_cost_usd":7.25`) || !strings.Contains(string(encoded), `"stored_cost_available":false`) || strings.Contains(string(encoded), `"cost_usd"`) {
		t.Fatalf("stored JSON namespace: %s %v", encoded, err)
	}
	typ := reflect.TypeFor[dto.StoredUsageEventRecord]()
	for _, name := range []string{"CostUSD", "CostAvailable", "PricingSnapshotID", "DualCosts"} {
		if _, ok := typ.FieldByName(name); ok {
			t.Fatalf("stored DTO impersonates current %s", name)
		}
	}
}

// collectStoredRequestEventsForTest collects only the explicit stored stream;
// ordinary current fixture helpers and numeric assertions remain unchanged.
func collectStoredRequestEventsForTest(db *gorm.DB, filter dto.UsageQueryFilter) ([]dto.StoredUsageEventRecord, error) {
	var records []dto.StoredUsageEventRecord
	err := repository.StreamStoredUsageEventsWithFilter(db, filter, func(row dto.StoredUsageEventRecord) error { records = append(records, row); return nil })
	return records, err
}
