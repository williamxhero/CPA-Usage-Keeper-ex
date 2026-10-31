package test

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	. "cpa-usage-keeper/internal/api"
	"cpa-usage-keeper/internal/entities"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func TestUsageEventListAndExportsKeepSameStoredAmounts(t *testing.T) {
	when := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	provider := &usageEventsStub{events: []servicedto.UsageEventRecord{
		{ID: 41, Timestamp: when, Model: "model-a", TotalTokens: 100, CostUSD: 1.25, CostAvailable: true, PricingStyle: "openai"},
		{ID: 42, Timestamp: when.Add(time.Minute), Model: "model-a", TotalTokens: 50, CostUSD: 0, CostAvailable: false, PricingStyle: "openai"},
	}}
	router := NewRouter(nil, nil, provider, nil, AuthConfig{}, nil, "")
	query := "?range=24h&model=model-a"
	list := serveAPIGet(router, "/api/v1/usage/events"+query)
	if list.Code != http.StatusOK {
		t.Fatalf("列表失败: %d %s", list.Code, list.Body.String())
	}
	var listed struct {
		Events []struct {
			CostUSD       float64 `json:"cost_usd"`
			CostAvailable bool    `json:"cost_available"`
		} `json:"events"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || len(listed.Events) != 2 {
		t.Fatalf("解析列表费用: %+v %v", listed, err)
	}
	if listed.Events[0].CostUSD != 1.25 || !listed.Events[0].CostAvailable || listed.Events[1].CostUSD != 0 || listed.Events[1].CostAvailable {
		t.Fatalf("列表金额或缺价状态不符: %+v", listed.Events)
	}
	if provider.filterCalls != 1 || provider.lastFilter.Model != "model-a" {
		t.Fatalf("列表筛选未传递: %+v", provider.lastFilter)
	}
	jsonExport := serveAPIGet(router, "/api/v1/usage/events/export"+query+"&format=json")
	if jsonExport.Code != http.StatusOK {
		t.Fatalf("JSON 导出失败: %d %s", jsonExport.Code, jsonExport.Body.String())
	}
	var exported struct {
		Events []struct {
			CostUSD          float64         `json:"cost_usd"`
			CostAvailable    bool            `json:"cost_available"`
			DualCosts        json.RawMessage `json:"dual_costs"`
			PricingSelection json.RawMessage `json:"pricing_selection"`
		} `json:"events"`
		TotalCount int64 `json:"total_count"`
	}
	if err := json.Unmarshal(jsonExport.Body.Bytes(), &exported); err != nil || exported.TotalCount != 2 || len(exported.Events) != 2 {
		t.Fatalf("解析 JSON 导出费用: %+v %v", exported, err)
	}
	if exported.Events[0].CostUSD != listed.Events[0].CostUSD || exported.Events[1].CostUSD != listed.Events[1].CostUSD ||
		!exported.Events[0].CostAvailable || exported.Events[1].CostAvailable ||
		len(exported.Events[0].DualCosts) == 0 || len(exported.Events[0].PricingSelection) == 0 || strings.Contains(jsonExport.Body.String(), "pricing_style") {
		t.Fatalf("JSON 导出金额或独立字段合同不符: %s", jsonExport.Body.String())
	}
	csvExport := serveAPIGet(router, "/api/v1/usage/events/export"+query+"&format=csv")
	if csvExport.Code != http.StatusOK {
		t.Fatalf("CSV 导出失败: %d %s", csvExport.Code, csvExport.Body.String())
	}
	records, err := csv.NewReader(strings.NewReader(csvExport.Body.String())).ReadAll()
	if err != nil || len(records) != 3 {
		t.Fatalf("解析 CSV 导出费用: rows=%d err=%v", len(records), err)
	}
	columns := map[string]int{}
	for i, name := range records[0] {
		columns[name] = i
	}
	for _, name := range []string{"cost_usd", "cost_available", "configured_total_cost_usd", "reference_total_cost_usd", "pricing_snapshot_id", "pricing_selection"} {
		if _, exists := columns[name]; !exists {
			t.Fatalf("current CSV lost additive metadata %q: %+v", name, records[0])
		}
	}
	for index := range listed.Events {
		got, err := strconv.ParseFloat(records[index+1][columns["cost_usd"]], 64)
		if err != nil || got != listed.Events[index].CostUSD || records[index+1][columns["cost_available"]] != strconv.FormatBool(listed.Events[index].CostAvailable) {
			t.Fatalf("CSV 第%d行金额与列表不同: %q %v", index, records[index+1], err)
		}
	}
	if provider.exportCalls != 2 || provider.filterCalls != 1 || provider.lastFilter.Model != "model-a" {
		t.Fatalf("列表/导出筛选或流式调用次数变化: %+v", provider)
	}

	// Original stored amounts/availability are also verified against real SQLite
	// and authenticated stored list + JSON + CSV, not a legacy export adapter.
	db := openAPITestDatabase(t)
	fee, zero, yes, no := 1.25, 0.0, true, false
	at := time.Now().Add(-time.Hour)
	rows := []entities.UsageEvent{
		{ID: 41, EventKey: "original-stored-41", Timestamp: at, Model: "model-a", InputTokens: 100, TotalTokens: 100, CostUSD: &fee, CostAvailable: &yes},
		{ID: 42, EventKey: "original-stored-42", Timestamp: at.Add(-time.Minute), Model: "model-a", InputTokens: 50, TotalTokens: 50, CostUSD: &zero, CostAvailable: &no},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	storedRouter, admin, viewer := storedHTTPRouter(t, db, emptyPricingCatalogForTest(), nil)
	type storedEvent struct {
		ID     string `json:"id"`
		Model  string `json:"model"`
		Tokens struct {
			TotalTokens int64 `json:"total_tokens"`
		} `json:"tokens"`
		CostUSD       float64 `json:"stored_cost_usd"`
		CostAvailable bool    `json:"stored_cost_available"`
	}
	var storedList []storedEvent
	for _, path := range []string{"/api/v1/usage/stored/events", "/api/v1/usage/stored/events/export"} {
		target := path + query
		if strings.HasSuffix(path, "export") {
			target += "&format=json"
		}
		if denied := serveAPIGet(storedRouter, target, viewer); denied.Code != http.StatusForbidden {
			t.Fatalf("viewer acquired stored export/list permission: %d", denied.Code)
		}
		res := serveAPIGet(storedRouter, target, admin)
		storedDecode(t, res)
		storedNoExposure(t, res.Body.String())
		var out struct {
			Events     []storedEvent `json:"events"`
			TotalCount int64         `json:"total_count"`
			DataSource string        `json:"data_source"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil || len(out.Events) != 2 || out.TotalCount != 2 || out.DataSource != "usage_events" {
			t.Fatalf("stored list/export decode: %+v %v", out, err)
		}
		if out.Events[0].ID != "41" || out.Events[1].ID != "42" || out.Events[0].Model != "model-a" || out.Events[1].Model != "model-a" || out.Events[0].Tokens.TotalTokens != 100 || out.Events[1].Tokens.TotalTokens != 50 || out.Events[0].CostUSD != 1.25 || !out.Events[0].CostAvailable || out.Events[1].CostUSD != 0 || out.Events[1].CostAvailable {
			t.Fatalf("stored list/export lost original amounts, availability or facts: %+v", out.Events)
		}
		storedList = out.Events
	}
	res := serveAPIGet(storedRouter, "/api/v1/usage/stored/events/export"+query+"&format=csv", admin)
	if res.Code != http.StatusOK {
		t.Fatalf("stored CSV failed: %d %s", res.Code, res.Body.String())
	}
	storedNoExposure(t, res.Body.String())
	records, err = csv.NewReader(strings.NewReader(res.Body.String())).ReadAll()
	if err != nil || len(records) != 3 || len(records[0]) < 3 || strings.Join(records[0][len(records[0])-3:], ",") != "stored_cost_usd,stored_cost_available,data_source" {
		t.Fatalf("stored CSV schema/row drift: %+v %v", records, err)
	}
	for index, event := range storedList {
		row := records[index+1]
		got, err := strconv.ParseFloat(row[len(row)-3], 64)
		if err != nil || got != event.CostUSD || row[len(row)-2] != strconv.FormatBool(event.CostAvailable) || row[len(row)-1] != "usage_events" || row[0] != event.ID {
			t.Fatalf("stored CSV/list mismatch: %+v %+v %v", event, row, err)
		}
	}
}
