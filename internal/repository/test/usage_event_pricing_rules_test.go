package test

import (
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	repodto "cpa-usage-keeper/internal/repository/dto"
)

func TestRequestEventsListAndStreamKeepStoredFeeWithoutCurrentPricing(t *testing.T) {
	db := openTestDatabase(t)
	timestamp := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	storedCost, available := 6.0, true
	if err := db.Create(&entities.UsageEvent{
		EventKey: "request-rule", Timestamp: timestamp, APIGroupKey: "group-a", Model: "model-a", ServiceTier: "priority", ReasoningEffort: "xhigh", InputTokens: 1_000_000, TotalTokens: 1_000_000,
		CostUSD: &storedCost, CostAvailable: &available,
	}).Error; err != nil {
		t.Fatalf("seed request event: %v", err)
	}
	page, err := repository.ListStoredUsageEventsWithFilter(db, repodto.UsageQueryFilter{PageSize: 10})
	if err != nil {
		t.Fatalf("ListUsageEventsWithFilter: %v", err)
	}
	if len(page.Events) != 1 || page.Events[0].StoredCostUSD != storedCost || !page.Events[0].StoredCostAvailable {
		t.Fatalf("request event lost stored fee after pricing removal: %+v", page.Events)
	}

	streamed := make([]repodto.StoredUsageEventRecord, 0, 1)
	if err := repository.StreamStoredUsageEventsWithFilter(db, repodto.UsageQueryFilter{}, func(record repodto.StoredUsageEventRecord) error {
		streamed = append(streamed, record)
		return nil
	}); err != nil {
		t.Fatalf("StreamStoredUsageEventsWithFilter: %v", err)
	}
	if len(streamed) != 1 || streamed[0].StoredCostUSD != storedCost || !streamed[0].StoredCostAvailable {
		t.Fatalf("streamed request event lost stored fee: %+v", streamed)
	}
}
