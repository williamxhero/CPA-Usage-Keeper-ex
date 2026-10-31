package test

import (
	"context"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	repositorydto "cpa-usage-keeper/internal/repository/dto"
	"math"
	"strings"
	"testing"
	"time"
)

func TestStoredQuotaHistoryRejectsInvalidFeesWithoutPublishingPartialHistory(t *testing.T) {
	for _, tc := range []struct {
		name         string
		fee          *float64
		availability *bool
		twice        bool
	}{
		{"null_amount", nil, codexQuotaEfficiencyBool(true), false},
		{"null_availability", codexQuotaEfficiencyFloat(1), nil, false},
		{"positive_infinity", codexQuotaEfficiencyFloat(math.Inf(1)), codexQuotaEfficiencyBool(true), false},
		{"negative_infinity", codexQuotaEfficiencyFloat(math.Inf(-1)), codexQuotaEfficiencyBool(true), false},
		{"overflow", codexQuotaEfficiencyFloat(math.MaxFloat64), codexQuotaEfficiencyBool(true), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDatabase(t)
			now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
			seedCodexQuotaEfficiencyCycle(t, db, "stored-auth", now.Add(-5*time.Hour), now.Add(time.Hour), []codexQuotaEfficiencySegmentSeed{
				{remaining: 90, first: now.Add(-3 * time.Hour), last: now.Add(-3 * time.Hour)},
				{remaining: 89, first: now.Add(-time.Hour), last: now.Add(-time.Hour)},
			})
			e := usageEventForQuotaEfficiency("invalid", "oauth", "stored-auth", now.Add(-2*time.Hour), 100)
			e.CostUSD, e.CostAvailable = tc.fee, tc.availability
			seedCodexQuotaEfficiencyUsage(t, db, e)
			if tc.twice {
				e.ID = 0
				e.EventKey = "invalid-2"
				seedCodexQuotaEfficiencyUsage(t, db, e)
			}
			got, err := repository.BuildStoredCodexQuotaEfficiencyHistory(context.Background(), db, repositorydto.CodexQuotaEfficiencyQuery{AuthIndex: "stored-auth", Now: now, RangeStart: now.Add(-30 * 24 * time.Hour)})
			if err == nil || !strings.Contains(err.Error(), "invalid stored cost") {
				t.Fatalf("invalid stored fee accepted: result=%+v error=%v", got, err)
			}
			if len(got.Cycles) != 0 {
				t.Fatalf("partial history escaped with invalid fee: %+v", got.Cycles)
			}
			var count int64
			if err := db.Model(&entities.UsageEvent{}).Count(&count).Error; err != nil || count < 1 {
				t.Fatalf("reader changed raw facts: count=%d err=%v", count, err)
			}
		})
	}
}
