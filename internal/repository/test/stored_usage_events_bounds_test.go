package test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/dto"
)

func TestStoredEventsBoundedDefaultsLimitAndFilterParity(t *testing.T) {
	db := openTestDatabase(t)
	at := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	events := make([]entities.UsageEvent, 130)
	for i := range events {
		events[i] = entities.UsageEvent{EventKey: fmt.Sprint(i), Timestamp: at, Model: "model", APIGroupKey: "group", AuthType: "oauth", AuthIndex: "identity", Failed: i%2 == 0, CostUSD: windowCostPtr(1.25), CostAvailable: windowAvailablePtr(true)}
	}
	if err := db.Create(&events).Error; err != nil {
		t.Fatal(err)
	}
	storedEventsRejectSideEffects(t, db)
	page, err := repository.ListStoredUsageEventsWithFilter(db, dto.UsageQueryFilter{})
	if err != nil || len(page.Events) != 100 || page.TotalCount != 130 || page.TotalPages != 2 || page.Page != 1 || page.PageSize != 100 {
		t.Fatalf("default row bound: %+v %v", page, err)
	}
	page, err = repository.ListStoredUsageEventsWithFilter(db, dto.UsageQueryFilter{PageSize: -1, Limit: 7, Page: 2})
	if err != nil || len(page.Events) != 7 || page.PageSize != 7 || page.Events[0].Metadata.ID != events[122].ID {
		t.Fatalf("limit fallback: %+v %v", page, err)
	}
	page, err = repository.ListStoredUsageEventsWithFilter(db, dto.UsageQueryFilter{PageSize: 20, Limit: 7, Page: 7, CursorMode: true})
	if err != nil || len(page.Events) != 20 || !page.HasMore || page.Events[0].Metadata.ID != events[129].ID {
		t.Fatalf("cursor ignored offset/bound: %+v %v", page, err)
	}
	start, end := at.Add(-time.Second), at
	for _, filter := range []dto.UsageQueryFilter{
		{StartTime: &start, EndTime: &end, EndExclusive: true, PageSize: 200},
		{StartTime: &start, EndTime: &end, EndExclusive: false, PageSize: 200},
		{Model: "model", Result: "failure", PageSize: 200},
		{Model: "model", Result: "success", PageSize: 200},
		{Model: "alias-not-a-model", PageSize: 200},
		{AuthType: "apikey", AuthIndex: "identity", PageSize: 200},
		{AuthType: "oauth", AuthIndex: "identity", PageSize: 200},
		{APIGroupKey: "other", PageSize: 200},
	} {
		stored, err := repository.ListStoredUsageEventsWithFilter(db, filter)
		if err != nil {
			t.Fatal(err)
		}
		current, err := repository.ListUsageEventsWithFilter(db, filter)
		if err != nil {
			t.Fatal(err)
		}
		var storedIDs, currentIDs, streamIDs []int64
		for _, r := range stored.Events {
			storedIDs = append(storedIDs, r.Metadata.ID)
		}
		for _, r := range current.Events {
			currentIDs = append(currentIDs, r.ID)
		}
		if err := repository.StreamStoredUsageEventsWithFilter(db, filter, func(r dto.StoredUsageEventRecord) error { streamIDs = append(streamIDs, r.Metadata.ID); return nil }); err != nil {
			t.Fatal(err)
		}
		if stored.TotalCount != current.TotalCount || !slices.Equal(storedIDs, currentIDs) || !slices.Equal(storedIDs, streamIDs) {
			t.Fatalf("bounded/filter parity: filter=%+v stored=%v current=%v stream=%v", filter, storedIDs, currentIDs, streamIDs)
		}
	}
}
