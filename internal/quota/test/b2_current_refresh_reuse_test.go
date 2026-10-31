package test

import (
	"context"
	"cpa-usage-keeper/internal/entities"
	. "cpa-usage-keeper/internal/quota"
	"strconv"
	"testing"
	"time"
)

func TestB2CurrentHeaderManualScheduledReuseExplicitCatalog(t *testing.T) {
	for _, entry := range []string{"header", "manual", "scheduled"} {
		t.Run(entry, func(t *testing.T) {
			db := openQuotaTestDatabase(t)
			seedUsageIdentity(t, db, entities.UsageIdentity{Identity: "b2", Provider: "codex", Type: "codex", AuthType: entities.UsageIdentityAuthTypeCredential})
			reset := time.Now().Add(-time.Hour).Truncate(time.Second)
			seedUsageEvent(t, db, entities.UsageEvent{AuthIndex: "b2", AuthType: "oauth", Model: "b2-model", Timestamp: reset.Add(-2 * time.Hour), InputTokens: 1_000_000, TotalTokens: 1_000_000, CostUSD: floatPtr(1.25), CostAvailable: boolPtr(true)})
			handler := &refreshHandlerStub{output: ProviderOutput{Provider: "codex", Result: CodexResult{Usage: &CodexUsagePayload{RateLimit: &CodexRateLimitInfo{PrimaryWindow: &CodexUsageWindow{UsedPercent: 4, LimitWindowSeconds: 5 * 3600, ResetAt: reset.Unix()}}}}}}
			catalog := b2Catalog(t, 9)
			service := NewServiceWithRegistry(db, NewProviderRegistry(map[string]ProviderHandler{"codex": handler}), catalog)
			t.Cleanup(service.StopRefreshTasks)
			setRefreshCooldown(service, func(time.Duration) {})
			count := b2RejectPricingReads(t, db)
			switch entry {
			case "header":
				h := codexUsageHeader("4")
				h.Set("X-Codex-Primary-Reset-At", strconv.FormatInt(reset.Unix(), 10))
				if !applyUsageHeaderSnapshot(service, context.Background(), codexUsageHeaderSnapshotWithHeaders("b2", reset.Add(time.Minute), h)) {
					t.Fatal("header not applied")
				}
			case "manual":
				if _, err := service.Refresh(context.Background(), RefreshRequest{AuthIndexes: []string{"b2"}, Source: RefreshSourceManual}); err != nil {
					t.Fatal(err)
				}
			case "scheduled":
				if err := service.RunAutoRefresh(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			task := waitForRefreshTask(t, service, "b2", RefreshTaskStatusCompleted)
			row := task.Quota.Quota[0]
			assertWindowUsage(t, row, 1_000_000, 9)
			if row.PricingSnapshotID != catalog.NewResolver().SnapshotID() || row.DualCosts == nil || row.StoredCostAvailable != nil || row.WindowCostSource != "" {
				t.Fatalf("current source changed: %+v", row)
			}
			if *count != 0 {
				t.Fatal("live pricing table read")
			}
		})
	}
}
