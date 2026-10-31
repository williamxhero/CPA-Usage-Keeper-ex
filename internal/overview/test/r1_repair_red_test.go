package test

import (
	"math"
	"reflect"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/overview"
)

func TestR1OrdinaryRowsPropagateUnknownFeesOrderIndependently(t *testing.T) {
	now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	known := 2.5
	available := true
	events := []entities.UsageEvent{
		{ID: 1, APIGroupKey: "key", Model: "model", Timestamp: now, InputTokens: 10, TotalTokens: 10, OutputTokens: 100, LatencyMS: 1000, CostUSD: &known, CostAvailable: &available},
		{ID: 2, APIGroupKey: "key", Model: "model", Timestamp: now, InputTokens: 20, TotalTokens: 20, OutputTokens: 50, LatencyMS: 1000},
	}
	original := append([]entities.UsageEvent(nil), events...)
	var firstHourly []entities.UsageOverviewHourlyStat
	var firstDaily []entities.UsageOverviewDailyStat
	for _, order := range [][]entities.UsageEvent{events, {events[1], events[0]}} {
		hourly, daily, next, err := overview.BuildOrdinaryRows(order)
		if err != nil {
			t.Fatalf("ordinary rows: %v", err)
		}
		if next != 2 || len(hourly) != 1 || len(daily) != 1 {
			t.Fatalf("rows=%d/%d next=%d", len(hourly), len(daily), next)
		}
		for _, row := range []struct {
			cost                           *float64
			requests, tokens, speedSamples int64
		}{
			{hourly[0].CostUSD, hourly[0].RequestCount, hourly[0].TotalTokens, hourly[0].SpeedSampleCount},
			{daily[0].CostUSD, daily[0].RequestCount, daily[0].TotalTokens, daily[0].SpeedSampleCount},
		} {
			if row.cost != nil || row.requests != 2 || row.tokens != 30 || row.speedSamples != 2 {
				t.Fatalf("unknown fee was made complete: %+v", row)
			}
		}
		if hourly[0].UnavailableCostCount != nil || daily[0].UnavailableCostCount != nil || hourly[0].SpeedTPSSum != 150 || daily[0].SpeedTPSSum != 150 {
			t.Fatalf("unknown fee/speed state hourly=%+v daily=%+v", hourly[0], daily[0])
		}
		if firstHourly != nil && (!reflect.DeepEqual(firstHourly, hourly) || !reflect.DeepEqual(firstDaily, daily)) {
			t.Fatal("permuting known/unknown events changed output")
		}
		firstHourly, firstDaily = hourly, daily
	}
	if !reflect.DeepEqual(events, original) {
		t.Fatal("builder mutated facts")
	}
}

func TestR1OrdinaryRowsKeepTenDimensionGrouping(t *testing.T) {
	base := entities.UsageEvent{ID: 1, Timestamp: time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC), APIGroupKey: "key", Model: "model", AuthIndex: "auth", ModelAlias: new("alias"), ServiceTier: "tier", ResponseServiceTier: "response", ReasoningEffort: "reasoning", Endpoint: "endpoint", ExecutorType: "executor", TotalTokens: 1}
	changes := []func(*entities.UsageEvent){
		func(e *entities.UsageEvent) { e.Timestamp = e.Timestamp.Add(time.Hour) },
		func(e *entities.UsageEvent) { e.APIGroupKey = "other" }, func(e *entities.UsageEvent) { e.Model = "other" },
		func(e *entities.UsageEvent) { e.AuthIndex = "other" }, func(e *entities.UsageEvent) { e.ModelAlias = new("other") },
		func(e *entities.UsageEvent) { e.ServiceTier = "other" }, func(e *entities.UsageEvent) { e.ResponseServiceTier = "other" },
		func(e *entities.UsageEvent) { e.ReasoningEffort = "other" }, func(e *entities.UsageEvent) { e.Endpoint = "other" }, func(e *entities.UsageEvent) { e.ExecutorType = "other" },
	}
	for i, change := range changes {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			other := base
			other.ID = 2
			change(&other)
			h, d, _, err := overview.BuildOrdinaryRows([]entities.UsageEvent{base, other})
			wantDaily := 2
			if i == 0 {
				wantDaily = 1
			}
			if err != nil || len(h) != 2 || len(d) != wantDaily {
				t.Fatalf("dimension %d collapsed: h=%d d=%d err=%v", i, len(h), len(d), err)
			}
		})
	}
}

func TestR1StoredRowsRejectUninitializedAndNonFiniteFees(t *testing.T) {
	now := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	for _, event := range []entities.UsageEvent{
		{ID: 1, Model: "model", Timestamp: now},
		{ID: 2, Model: "model", Timestamp: now, CostUSD: func() *float64 { v := math.NaN(); return &v }(), CostAvailable: func() *bool { v := true; return &v }()},
	} {
		if _, _, _, err := overview.BuildStoredRows([]entities.UsageEvent{event}); err == nil {
			t.Fatalf("stored rows accepted invalid event %d", event.ID)
		}
	}
}
