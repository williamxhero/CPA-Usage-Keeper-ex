package test

import (
	"context"
	"errors"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/service"
	servicedto "cpa-usage-keeper/internal/service/dto"
)

func TestStoredRealtimeServiceScopesActiveKeyAndRejectsDeletedKey(t *testing.T) {
	withUsageServiceLocation(t, "UTC")
	db := openUsageServiceTestDatabase(t)
	id := seedUsageFilterAPIKeys(t, db)
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	cache := newServiceRecentCacheFromEvents(t, db, now, []entities.UsageEvent{
		storedUsageEventFee(entities.UsageEvent{EventKey: "selected", APIGroupKey: "sk-target-key", Model: "a", Timestamp: now.Add(-time.Minute), TotalTokens: 30}, 1.25, true),
		storedUsageEventFee(entities.UsageEvent{EventKey: "other", APIGroupKey: "sk-other-key", Model: "b", Timestamp: now.Add(-time.Minute), TotalTokens: 300}, 90, true),
	})
	filter := servicedto.StoredUsageRealtimeFilter{RealtimeWindow: "15m", RealtimeEndTime: &now, APIKeyID: id}
	for _, useCache := range []bool{false, true} {
		provider := service.NewStoredUsageRealtimeService(db, nil)
		if useCache {
			provider = service.NewStoredUsageRealtimeService(db, cache)
		}
		got, err := provider.GetStoredUsageRealtime(context.Background(), filter)
		if err != nil || got == nil || got.Insights == nil || got.Insights.Summary.Requests != 1 || got.Insights.Summary.TotalTokens != 30 || got.Insights.Summary.StoredCostUSD != 1.25 || !got.Insights.Summary.StoredCostAvailable {
			t.Fatalf("active key scope failed: got=%+v err=%v", got, err)
		}
		for _, invalid := range []string{"not-numeric", "999999"} {
			bad := filter
			bad.APIKeyID = invalid
			if _, err := provider.GetStoredUsageRealtime(context.Background(), bad); err == nil {
				t.Fatalf("invalid key %s widened scope", invalid)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := provider.GetStoredUsageRealtime(ctx, filter); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation became success: %v", err)
		}
	}
	if err := db.Model(&entities.CPAAPIKey{}).Where("id = ?", id).Update("is_deleted", true).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewStoredUsageRealtimeService(db, cache).GetStoredUsageRealtime(context.Background(), filter); err == nil {
		t.Fatal("deleted API key widened scope")
	}
}

func TestStoredRealtimeServiceRejectsUnsupportedWindows(t *testing.T) {
	db := openUsageServiceTestDatabase(t)
	provider := service.NewStoredUsageRealtimeService(db, nil)
	for _, window := range []string{"", "24h", "all", "15m,60m"} {
		_, err := provider.GetStoredUsageRealtime(context.Background(), servicedto.StoredUsageRealtimeFilter{RealtimeWindow: window})
		var validation *service.StoredUsageRealtimeValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("invalid window %q did not return typed validation error: %v", window, err)
		}
	}
}
