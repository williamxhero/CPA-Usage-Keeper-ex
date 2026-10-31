package test

// Original fork contract retained alongside explicitly stored upstream tests.
// Only local declaration names are prefixed to avoid collisions; assertions are unchanged.

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository"
	repositorydto "cpa-usage-keeper/internal/repository/dto"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestForkCurrentBuildCodexQuotaEfficiencyHistoryClassifiesCycleAndTransitionUsageOnce(t *testing.T) {
	// 固定 now 后，父周期可以明确区分一个正在进行的 Weekly 和一个已经结束的 Weekly。
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	db := openTestDatabase(t)
	completed := forkCurrentSeedCodexQuotaEfficiencyCycle(t, db, "codex-auth", now.Add(-11*24*time.Hour), now.Add(-4*24*time.Hour), []forkCurrentCodexQuotaEfficiencySegmentSeed{
		{remaining: 93, first: now.Add(-10 * 24 * time.Hour), last: now.Add(-10*24*time.Hour + 10*time.Minute)},
		{remaining: 92, first: now.Add(-10*24*time.Hour + 20*time.Minute), last: now.Add(-10*24*time.Hour + 30*time.Minute)},
	})
	current := forkCurrentSeedCodexQuotaEfficiencyCycle(t, db, "codex-auth", now.Add(-4*24*time.Hour), now.Add(3*24*time.Hour), []forkCurrentCodexQuotaEfficiencySegmentSeed{
		{remaining: 90, first: now.Add(-3 * time.Hour), last: now.Add(-2*time.Hour - 50*time.Minute)},
		{remaining: 89, first: now.Add(-2*time.Hour - 40*time.Minute), last: now.Add(-2*time.Hour - 30*time.Minute)},
		{remaining: 86, first: now.Add(-2 * time.Hour), last: now.Add(-90 * time.Minute)},
	})

	// 变化区间从前一百分比首次观察之后开始，到后一百分比首次观察为止；相邻区间共享的边界事件只归前一区间。
	forkCurrentSeedCodexQuotaEfficiencyUsage(t, db,
		forkCurrentUsageEventForQuotaEfficiency("completed", "oauth", "codex-auth", completed.WindowStartedAt.Add(time.Hour), 2_000_000),
		forkCurrentUsageEventForQuotaEfficiency("direct-first-observation", "oauth", "codex-auth", now.Add(-3*time.Hour), 700_000),
		forkCurrentUsageEventForQuotaEfficiency("direct-start", "oauth", "codex-auth", now.Add(-2*time.Hour-50*time.Minute), 1_000_000),
		forkCurrentUsageEventForQuotaEfficiency("direct-end", "oauth", "codex-auth", now.Add(-2*time.Hour-40*time.Minute), 400_000),
		forkCurrentUsageEventForQuotaEfficiency("stable", "oauth", "codex-auth", now.Add(-time.Hour-50*time.Minute), 500_000),
		forkCurrentUsageEventForQuotaEfficiency("cross-start", "oauth", "codex-auth", now.Add(-2*time.Hour-30*time.Minute), 3_000_000),
		forkCurrentUsageEventForQuotaEfficiency("cross-end", "oauth", "codex-auth", now.Add(-2*time.Hour), 600_000),
		forkCurrentUsageEventForQuotaEfficiency("wrong-auth-type", "api_key", "codex-auth", now.Add(-2*time.Hour-45*time.Minute), 9_000_000),
		forkCurrentUsageEventForQuotaEfficiency("wrong-auth-index", "oauth", "another-auth", now.Add(-2*time.Hour-45*time.Minute), 9_000_000),
	)

	streamQueryCount := 0
	queryDB := db.Session(&gorm.Session{Logger: forkCurrentCodexQuotaEfficiencyQueryLogger{Interface: logger.Default.LogMode(logger.Silent), streamQueries: &streamQueryCount}})
	result := forkCurrentBuildCodexQuotaEfficiencyForTest(t, queryDB, "codex-auth", now, forkCurrentCodexQuotaEfficiencyPricingResolver(t))

	if len(result.Windows) != 1 || result.SelectedWindow == nil {
		t.Fatalf("expected one selected Weekly window, got %+v", result.Windows)
	}
	if result.SelectedWindow.WindowRole != "primary" || result.SelectedWindow.WindowSeconds != int64((7*24*time.Hour)/time.Second) {
		t.Fatalf("unexpected selected window: %+v", result.SelectedWindow)
	}
	if len(result.Cycles) != 2 || result.Cycles[0].ID != current.ID || result.Cycles[0].Status != "current" {
		t.Fatalf("expected current cycle %d first, got %+v", current.ID, result.Cycles)
	}
	if result.Cycles[1].ID != completed.ID || result.Cycles[1].Status != "completed" {
		t.Fatalf("expected completed cycle %d second, got %+v", completed.ID, result.Cycles)
	}
	if result.Cycles[0].FirstRemainingPercent == nil || *result.Cycles[0].FirstRemainingPercent != 90 || result.Cycles[0].LastRemainingPercent == nil || *result.Cycles[0].LastRemainingPercent != 86 || result.Cycles[0].ObservationCount != 3 {
		t.Fatalf("unexpected current cycle percentage summary: %+v", result.Cycles[0])
	}

	// 周期总量包含全部账号内事件，但区间会排除前一百分比的首次观察和下降后的稳定段。
	currentResult := result.Cycles[0]
	forkCurrentAssertCodexQuotaEfficiencyUsage(t, currentResult.Usage, 6_200_000, 6.2, true)
	if len(currentResult.Transitions) != 2 {
		t.Fatalf("expected two real transitions, got %+v", currentResult.Transitions)
	}
	direct := currentResult.Transitions[0]
	if direct.FromRemainingPercent != 90 || direct.ToRemainingPercent != 89 || direct.PercentagePoints != 1 || !direct.IsDirect {
		t.Fatalf("unexpected direct transition: %+v", direct)
	}
	if !direct.IntervalStartedAt.Equal(now.Add(-3*time.Hour)) || !direct.IntervalEndedAt.Equal(now.Add(-2*time.Hour-40*time.Minute)) {
		t.Fatalf("unexpected direct interval: %+v", direct)
	}
	forkCurrentAssertCodexQuotaEfficiencyUsage(t, direct.Usage, 1_400_000, 1.4, true)
	if direct.TokensPerPoint != 1_400_000 || math.Abs(direct.CostPerPoint-1.4) > 1e-9 {
		t.Fatalf("unexpected direct per-point values: %+v", direct)
	}
	cross := currentResult.Transitions[1]
	if cross.FromRemainingPercent != 89 || cross.ToRemainingPercent != 86 || cross.PercentagePoints != 3 || cross.IsDirect {
		t.Fatalf("unexpected cross transition: %+v", cross)
	}
	if !cross.IntervalStartedAt.Equal(now.Add(-2*time.Hour-40*time.Minute)) || !cross.IntervalEndedAt.Equal(now.Add(-2*time.Hour)) {
		t.Fatalf("unexpected cross interval: %+v", cross)
	}
	forkCurrentAssertCodexQuotaEfficiencyUsage(t, cross.Usage, 3_600_000, 3.6, true)
	if cross.TokensPerPoint != 1_200_000 || math.Abs(cross.CostPerPoint-1.2) > 1e-9 {
		t.Fatalf("unexpected cross per-point values: %+v", cross)
	}
	forkCurrentAssertCodexQuotaEfficiencyUsage(t, result.Cycles[1].Usage, 2_000_000, 2, true)
	if streamQueryCount != 1 {
		t.Fatalf("expected current and completed Weekly cycles to share one ordered UsageEvent stream, got %d", streamQueryCount)
	}
}

func TestForkCurrentBuildCodexQuotaEfficiencyHistoryUsesNewCycleStartForOverlappingWeeklyCycles(t *testing.T) {
	// 上游延后 Weekly reset 时，新的理论起点必须立即取代旧周期的重叠部分，不能等到 Keeper 首次观察。
	oldStart := time.Date(2026, 8, 20, 3, 35, 0, 0, time.UTC)
	oldReset := time.Date(2026, 8, 27, 3, 35, 0, 0, time.UTC)
	newStart := time.Date(2026, 8, 23, 16, 21, 0, 0, time.UTC)
	newReset := time.Date(2026, 8, 30, 16, 21, 0, 0, time.UTC)
	newObservedAt := newStart.Add(2 * time.Hour)
	now := time.Date(2026, 8, 25, 4, 0, 0, 0, time.UTC)
	db := openTestDatabase(t)
	oldCycle := forkCurrentSeedCodexQuotaEfficiencyCycle(t, db, "codex-auth", oldStart, oldReset, []forkCurrentCodexQuotaEfficiencySegmentSeed{
		{remaining: 61, first: newStart.Add(-15 * time.Minute), last: newStart.Add(-15 * time.Minute)},
	})
	newCycle := forkCurrentSeedCodexQuotaEfficiencyCycle(t, db, "codex-auth", newStart, newReset, []forkCurrentCodexQuotaEfficiencySegmentSeed{
		{remaining: 100, first: newObservedAt, last: newObservedAt},
	})
	forkCurrentSeedCodexQuotaEfficiencyUsage(t, db,
		forkCurrentUsageEventForQuotaEfficiency("before-new-cycle", "oauth", "codex-auth", newStart.Add(-10*time.Minute), 100),
		forkCurrentUsageEventForQuotaEfficiency("at-new-cycle-start", "oauth", "codex-auth", newStart, 200),
		forkCurrentUsageEventForQuotaEfficiency("before-new-cycle-observed", "oauth", "codex-auth", newStart.Add(30*time.Minute), 300),
	)

	result := forkCurrentBuildCodexQuotaEfficiencyForTest(t, db, "codex-auth", now, forkCurrentCodexQuotaEfficiencyPricingResolver(t))
	if len(result.Cycles) != 2 || result.Cycles[0].ID != newCycle.ID || result.Cycles[1].ID != oldCycle.ID {
		t.Fatalf("expected current and completed Weekly cycles, got %+v", result.Cycles)
	}
	current := result.Cycles[0]
	completed := result.Cycles[1]
	if !current.WindowStartedAt.Equal(newStart) || !current.ResetAt.Equal(newReset) || !completed.ResetAt.Equal(oldReset) {
		t.Fatalf("expected raw cycle boundaries to remain unchanged, got current=%+v completed=%+v", current, completed)
	}
	if !current.EffectiveStartedAt.Equal(newStart) || !completed.EffectiveEndedAt.Equal(newStart) {
		t.Fatalf("expected new cycle start to split overlapping Weekly periods, got current=%+v completed=%+v", current, completed)
	}
	forkCurrentAssertCodexQuotaEfficiencyUsage(t, completed.Usage, 100, 0.0001, true)
	forkCurrentAssertCodexQuotaEfficiencyUsage(t, current.Usage, 500, 0.0005, true)
}

func TestForkCurrentBuildCodexQuotaEfficiencyHistoryUsesLatestWindowPerRoleAndCutsOverlappingUsage(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	previousObservedAt := now.Add(-2 * time.Hour)
	switchedAt := now.Add(-time.Hour)
	db := openTestDatabase(t)
	oldPrimary := forkCurrentSeedCodexQuotaEfficiencyRoleCycle(t, db, "codex-auth", entities.CodexQuotaWindowRolePrimary, now.Add(-4*time.Hour), now.Add(time.Hour), []forkCurrentCodexQuotaEfficiencySegmentSeed{
		{remaining: 80, first: previousObservedAt, last: previousObservedAt},
	})
	forkCurrentSeedCodexQuotaEfficiencyRoleCycle(t, db, "codex-auth", entities.CodexQuotaWindowRoleSecondary, now.Add(-24*time.Hour), now.Add(6*24*time.Hour), []forkCurrentCodexQuotaEfficiencySegmentSeed{
		{remaining: 70, first: previousObservedAt, last: previousObservedAt},
	})
	newPrimary := forkCurrentSeedCodexQuotaEfficiencyRoleCycle(t, db, "codex-auth", entities.CodexQuotaWindowRolePrimary, now.Add(-24*time.Hour), now.Add(6*24*time.Hour), []forkCurrentCodexQuotaEfficiencySegmentSeed{
		{remaining: 69, first: switchedAt, last: switchedAt},
	})
	forkCurrentSeedCodexQuotaEfficiencyUsage(t, db,
		forkCurrentUsageEventForQuotaEfficiency("before-switch", "oauth", "codex-auth", switchedAt.Add(-30*time.Minute), 100),
		forkCurrentUsageEventForQuotaEfficiency("after-switch", "oauth", "codex-auth", switchedAt.Add(30*time.Minute), 200),
	)

	result := forkCurrentBuildCodexQuotaEfficiencyForTest(t, db, "codex-auth", now, forkCurrentCodexQuotaEfficiencyPricingResolver(t))
	if len(result.Windows) != 2 || result.SelectedWindow == nil {
		t.Fatalf("expected both current and historical roles to remain selectable, got %+v", result.Windows)
	}
	if result.SelectedWindow.WindowRole != "primary" || result.SelectedWindow.WindowSeconds != int64((7*24*time.Hour)/time.Second) || !result.SelectedWindow.HasCurrentCycle {
		t.Fatalf("expected current Primary Weekly selection, got %+v", result.SelectedWindow)
	}
	if len(result.Cycles) != 1 || result.Cycles[0].ID != newPrimary.ID || result.Cycles[0].Status != "current" {
		t.Fatalf("expected the Weekly cycle to fully supersede 5h cycle %d, got %+v", oldPrimary.ID, result.Cycles)
	}
	if !result.Cycles[0].EffectiveStartedAt.Equal(newPrimary.WindowStartedAt) {
		t.Fatalf("expected Weekly theoretical start to remain effective, got %+v", result.Cycles[0])
	}
	forkCurrentAssertCodexQuotaEfficiencyUsage(t, result.Cycles[0].Usage, 300, 0.0003, true)
}

func TestForkCurrentBuildCodexQuotaEfficiencyHistoryMarksReusedWeeklyCurrentAfterMultipleFiveHourDetours(t *testing.T) {
	// 复用父行会把 Weekly 移到多个错误 5h 周期之后；所有被其理论区间覆盖的 detour 都必须隐藏。
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	db := openTestDatabase(t)
	weeklyStart := now.Add(-24 * time.Hour)
	weeklyReset := weeklyStart.Add(7 * 24 * time.Hour)
	weekly := forkCurrentSeedCodexQuotaEfficiencyCycle(t, db, "codex-auth", weeklyStart, weeklyReset, []forkCurrentCodexQuotaEfficiencySegmentSeed{
		{remaining: 90, first: now.Add(-4 * time.Hour), last: now.Add(-4 * time.Hour)},
	})
	firstFiveHour := forkCurrentSeedCodexQuotaEfficiencyCycle(t, db, "codex-auth", now.Add(-6*time.Hour), now.Add(-time.Hour), []forkCurrentCodexQuotaEfficiencySegmentSeed{
		{remaining: 50, first: now.Add(-3 * time.Hour), last: now.Add(-3 * time.Hour)},
	})
	secondFiveHour := forkCurrentSeedCodexQuotaEfficiencyCycle(t, db, "codex-auth", now.Add(-5*time.Hour), now, []forkCurrentCodexQuotaEfficiencySegmentSeed{
		{remaining: 40, first: now.Add(-2 * time.Hour), last: now.Add(-2 * time.Hour)},
	})
	restored := codexQuotaHistoryObservation("codex-auth", "primary", 604_800, weeklyReset, 89, now.Add(-time.Hour))
	if err := repository.WriteCodexMainQuotaObservations(context.Background(), db, []repositorydto.CodexMainQuotaObservation{restored}); err != nil {
		t.Fatalf("reuse Weekly cycle: %v", err)
	}
	forkCurrentSeedCodexQuotaEfficiencyUsage(t, db,
		forkCurrentUsageEventForQuotaEfficiency("during-first-detour", "oauth", "codex-auth", now.Add(-11*time.Hour/2), 100),
		forkCurrentUsageEventForQuotaEfficiency("during-second-detour", "oauth", "codex-auth", now.Add(-9*time.Hour/2), 200),
	)

	result := forkCurrentBuildCodexQuotaEfficiencyForTest(t, db, "codex-auth", now, forkCurrentCodexQuotaEfficiencyPricingResolver(t))
	if result.SelectedWindow == nil || result.SelectedWindow.WindowSeconds != int64((7*24*time.Hour)/time.Second) || !result.SelectedWindow.HasCurrentCycle {
		t.Fatalf("expected restored Weekly window selection, got %+v", result.SelectedWindow)
	}
	if len(result.Cycles) != 1 || result.Cycles[0].ID != weekly.ID || result.Cycles[0].Status != "current" {
		t.Fatalf("expected reused Weekly current and intermediate 5h cycles %d/%d hidden, got %+v", firstFiveHour.ID, secondFiveHour.ID, result.Cycles)
	}
	forkCurrentAssertCodexQuotaEfficiencyUsage(t, result.Cycles[0].Usage, 300, 0.0003, true)
}

func TestForkCurrentBuildCodexQuotaEfficiencyHistoryClassifiesSingleWindowKindsByDuration(t *testing.T) {
	tests := []struct {
		name     string
		role     entities.CodexQuotaWindowRole
		duration time.Duration
		wantKind string
	}{
		{name: "primary five hour", role: entities.CodexQuotaWindowRolePrimary, duration: 5 * time.Hour, wantKind: "five_hour"},
		{name: "secondary weekly", role: entities.CodexQuotaWindowRoleSecondary, duration: 7 * 24 * time.Hour, wantKind: "weekly"},
		{name: "primary thirty day monthly", role: entities.CodexQuotaWindowRolePrimary, duration: 30 * 24 * time.Hour, wantKind: "monthly"},
		{name: "primary average monthly", role: entities.CodexQuotaWindowRolePrimary, duration: 365 * 24 * time.Hour / 12, wantKind: "monthly"},
		{name: "secondary thirty day monthly", role: entities.CodexQuotaWindowRoleSecondary, duration: 30 * 24 * time.Hour, wantKind: "monthly"},
		{name: "secondary average monthly", role: entities.CodexQuotaWindowRoleSecondary, duration: 365 * 24 * time.Hour / 12, wantKind: "monthly"},
		{name: "unknown positive window", role: entities.CodexQuotaWindowRolePrimary, duration: 12 * time.Hour},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
			db := openTestDatabase(t)
			forkCurrentSeedCodexQuotaEfficiencyRoleCycle(t, db, "free-codex-auth", test.role, now.Add(-test.duration/2), now.Add(test.duration/2), []forkCurrentCodexQuotaEfficiencySegmentSeed{
				{remaining: 100, first: now.Add(-time.Minute), last: now.Add(-time.Minute)},
			})

			result := forkCurrentBuildCodexQuotaEfficiencyForTest(t, db, "free-codex-auth", now, forkCurrentCodexQuotaEfficiencyPricingResolver(t))
			if len(result.Windows) != 1 || result.SelectedWindow == nil || result.SelectedWindow.WindowRole != string(test.role) || !result.SelectedWindow.HasCurrentCycle {
				t.Fatalf("expected one current %s window, got %+v", test.role, result)
			}
			if result.SelectedWindow.WindowSeconds != int64(test.duration/time.Second) {
				t.Fatalf("unexpected real window seconds: %+v", result.SelectedWindow)
			}
			if test.wantKind != "" {
				if result.SelectedWindow.WindowKind == nil || *result.SelectedWindow.WindowKind != test.wantKind {
					t.Fatalf("expected window kind %q, got %+v", test.wantKind, result.SelectedWindow)
				}
			} else if result.SelectedWindow.WindowKind != nil {
				t.Fatalf("expected unknown window kind fallback, got %+v", result.SelectedWindow)
			}
		})
	}
}

func TestForkCurrentBuildCodexQuotaEfficiencyHistoryRestoresFiveHourPrimaryWithWeeklySecondary(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	previousObservedAt := now.Add(-5 * time.Hour)
	restoredAt := now.Add(-time.Hour)
	db := openTestDatabase(t)
	oldPrimary := forkCurrentSeedCodexQuotaEfficiencyRoleCycle(t, db, "codex-auth", entities.CodexQuotaWindowRolePrimary, now.Add(-24*time.Hour), now.Add(6*24*time.Hour), []forkCurrentCodexQuotaEfficiencySegmentSeed{
		{remaining: 70, first: previousObservedAt, last: previousObservedAt},
	})
	newPrimary := forkCurrentSeedCodexQuotaEfficiencyRoleCycle(t, db, "codex-auth", entities.CodexQuotaWindowRolePrimary, now.Add(-4*time.Hour), now.Add(time.Hour), []forkCurrentCodexQuotaEfficiencySegmentSeed{
		{remaining: 90, first: restoredAt, last: restoredAt},
	})
	forkCurrentSeedCodexQuotaEfficiencyRoleCycle(t, db, "codex-auth", entities.CodexQuotaWindowRoleSecondary, now.Add(-24*time.Hour), now.Add(6*24*time.Hour), []forkCurrentCodexQuotaEfficiencySegmentSeed{
		{remaining: 69, first: restoredAt, last: restoredAt},
	})
	forkCurrentSeedCodexQuotaEfficiencyUsage(t, db,
		forkCurrentUsageEventForQuotaEfficiency("before-five-hour-start", "oauth", "codex-auth", now.Add(-5*time.Hour), 100),
		forkCurrentUsageEventForQuotaEfficiency("before-five-hour-observed", "oauth", "codex-auth", now.Add(-3*time.Hour), 200),
		forkCurrentUsageEventForQuotaEfficiency("after-five-hour-observed", "oauth", "codex-auth", now.Add(-30*time.Minute), 300),
	)

	result := forkCurrentBuildCodexQuotaEfficiencyForTest(t, db, "codex-auth", now, forkCurrentCodexQuotaEfficiencyPricingResolver(t))
	if len(result.Windows) != 2 || result.SelectedWindow == nil || result.SelectedWindow.WindowRole != "primary" || result.SelectedWindow.WindowSeconds != int64((5*time.Hour)/time.Second) || !result.SelectedWindow.HasCurrentCycle {
		t.Fatalf("expected restored Primary 5h selection, got %+v", result)
	}
	if result.Windows[1].WindowRole != "secondary" || result.Windows[1].WindowSeconds != int64((7*24*time.Hour)/time.Second) || !result.Windows[1].HasCurrentCycle {
		t.Fatalf("expected current Secondary Weekly window, got %+v", result.Windows)
	}
	if len(result.Cycles) != 2 || result.Cycles[0].ID != newPrimary.ID || result.Cycles[0].Status != "current" || result.Cycles[1].ID != oldPrimary.ID || result.Cycles[1].Status != "completed" {
		t.Fatalf("expected current 5h and completed Weekly cycles under Primary, got %+v", result.Cycles)
	}
	if !result.Cycles[0].EffectiveStartedAt.Equal(newPrimary.WindowStartedAt) || !result.Cycles[1].EffectiveEndedAt.Equal(newPrimary.WindowStartedAt) {
		t.Fatalf("expected restored 5h theoretical start to split Primary periods, got %+v", result.Cycles)
	}
	forkCurrentAssertCodexQuotaEfficiencyUsage(t, result.Cycles[0].Usage, 500, 0.0005, true)
	forkCurrentAssertCodexQuotaEfficiencyUsage(t, result.Cycles[1].Usage, 100, 0.0001, true)
}

type forkCurrentCodexQuotaEfficiencyQueryLogger struct {
	logger.Interface
	streamQueries *int
}

func (l forkCurrentCodexQuotaEfficiencyQueryLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	if strings.Contains(sql, "FROM usage_events INDEXED BY idx_usage_events_auth_index_timestamp_id") &&
		strings.Contains(sql, "ORDER BY timestamp ASC, id ASC") &&
		!strings.Contains(sql, "CASE WHEN") {
		(*l.streamQueries)++
	}
	l.Interface.Trace(ctx, begin, func() (string, int64) { return sql, 0 }, err)
}

func TestForkCurrentCodexQuotaEfficiencyUsageRangeUsesExistingAuthTimeIndex(t *testing.T) {
	db := openTestDatabase(t)
	var plan []struct {
		Detail string `gorm:"column:detail"`
	}
	err := db.Raw(`EXPLAIN QUERY PLAN
		SELECT total_tokens
		FROM usage_events INDEXED BY idx_usage_events_auth_index_timestamp_id
		WHERE auth_type = ? AND auth_index = ? AND timestamp >= ? AND timestamp < ?`,
		"oauth", "codex-auth", "2026-08-01T00:00:00Z", "2026-08-22T00:00:00Z").Scan(&plan).Error
	if err != nil {
		t.Fatalf("explain Codex quota efficiency usage range: %v", err)
	}
	details := make([]string, 0, len(plan))
	for _, row := range plan {
		details = append(details, row.Detail)
	}
	joined := strings.Join(details, "\n")
	if !strings.Contains(joined, "idx_usage_events_auth_index_timestamp_id") || !strings.Contains(joined, "timestamp>?") || !strings.Contains(joined, "timestamp<?") {
		t.Fatalf("expected existing auth+timestamp range index, got plan:\n%s", joined)
	}
}

func TestForkCurrentBuildCodexQuotaEfficiencyHistoryMarksMissingPricingUnavailable(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	db := openTestDatabase(t)
	cycle := forkCurrentSeedCodexQuotaEfficiencyCycle(t, db, "codex-auth", now.Add(-24*time.Hour), now.Add(24*time.Hour), []forkCurrentCodexQuotaEfficiencySegmentSeed{
		{remaining: 90, first: now.Add(-2 * time.Hour), last: now.Add(-90 * time.Minute)},
		{remaining: 89, first: now.Add(-time.Hour), last: now.Add(-30 * time.Minute)},
	})
	event := forkCurrentUsageEventForQuotaEfficiency("missing-price", "oauth", "codex-auth", now.Add(-75*time.Minute), 1234)
	event.Model = "unpriced-model"
	// 模拟旧数据只有 total_tokens、没有计价分项；有 Token 且无模型价格仍必须明确标记不可计价。
	event.InputTokens = 0
	forkCurrentSeedCodexQuotaEfficiencyUsage(t, db, event)

	result := forkCurrentBuildCodexQuotaEfficiencyForTest(t, db, "codex-auth", now, forkCurrentCodexQuotaEfficiencyPricingResolver(t))
	if len(result.Cycles) != 1 || result.Cycles[0].Status != "current" || result.Cycles[0].ID != cycle.ID || len(result.Cycles[0].Transitions) != 1 {
		t.Fatalf("unexpected current cycle: %+v", result.Cycles)
	}
	forkCurrentAssertCodexQuotaEfficiencyUsage(t, result.Cycles[0].Usage, 1234, 0, false)
	forkCurrentAssertCodexQuotaEfficiencyUsage(t, result.Cycles[0].Transitions[0].Usage, 1234, 0, false)
	if result.Cycles[0].Transitions[0].CostPerPointAvailable {
		t.Fatalf("missing price must not be rendered as zero cost: %+v", result.Cycles[0].Transitions[0])
	}
}

func TestForkCurrentBuildCodexQuotaEfficiencyHistoryKeepsPricingRuleDimensionsSeparate(t *testing.T) {
	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	db := openTestDatabase(t)
	forkCurrentSeedCodexQuotaEfficiencyCycle(t, db, "codex-auth", now.Add(-24*time.Hour), now.Add(24*time.Hour), []forkCurrentCodexQuotaEfficiencySegmentSeed{
		{remaining: 90, first: now.Add(-2 * time.Hour), last: now.Add(-90 * time.Minute)},
		{remaining: 89, first: now.Add(-time.Hour), last: now.Add(-30 * time.Minute)},
	})
	defaultTier := forkCurrentUsageEventForQuotaEfficiency("default-tier", "oauth", "codex-auth", now.Add(-90*time.Minute), 1_000_000)
	priorityTier := forkCurrentUsageEventForQuotaEfficiency("priority-tier", "oauth", "codex-auth", now.Add(-time.Hour), 1_000_000)
	priorityTier.ServiceTier = "priority"
	forkCurrentSeedCodexQuotaEfficiencyUsage(t, db, defaultTier, priorityTier)

	result := forkCurrentBuildCodexQuotaEfficiencyForTest(t, db, "codex-auth", now, forkCurrentCodexQuotaEfficiencyPricingResolverWithRules(t, []pricing.RuleConfig{{
		Key: "service_tier", Value: "priority", Multiplier: 2,
	}}))
	if len(result.Cycles) != 1 || result.Cycles[0].Status != "current" || len(result.Cycles[0].Transitions) != 1 {
		t.Fatalf("unexpected current cycle: %+v", result.Cycles)
	}
	forkCurrentAssertCodexQuotaEfficiencyUsage(t, result.Cycles[0].Usage, 2_000_000, 3, true)
	forkCurrentAssertCodexQuotaEfficiencyUsage(t, result.Cycles[0].Transitions[0].Usage, 2_000_000, 3, true)
}

type forkCurrentCodexQuotaEfficiencySegmentSeed struct {
	remaining int
	first     time.Time
	last      time.Time
}

func forkCurrentSeedCodexQuotaEfficiencyCycle(t testing.TB, db *gorm.DB, authIndex string, start, reset time.Time, segments []forkCurrentCodexQuotaEfficiencySegmentSeed) entities.QuotaCycle {
	return forkCurrentSeedCodexQuotaEfficiencyRoleCycle(t, db, authIndex, entities.CodexQuotaWindowRolePrimary, start, reset, segments)
}

func forkCurrentSeedCodexQuotaEfficiencyRoleCycle(t testing.TB, db *gorm.DB, authIndex string, role entities.CodexQuotaWindowRole, start, reset time.Time, segments []forkCurrentCodexQuotaEfficiencySegmentSeed) entities.QuotaCycle {
	t.Helper()
	quotaKey := "rate_limit.primary_window"
	if role == entities.CodexQuotaWindowRoleSecondary {
		quotaKey = "rate_limit.secondary_window"
	}
	cycle := entities.QuotaCycle{
		Provider:        "codex",
		AuthIndex:       authIndex,
		QuotaKey:        quotaKey,
		WindowSeconds:   int64(reset.Sub(start) / time.Second),
		ResetAtSource:   entities.QuotaResetAtSourceAbsolute,
		WindowStartedAt: start,
		ResetAt:         reset,
		FirstObservedAt: segments[0].first,
		LastObservedAt:  segments[len(segments)-1].last,
		CreatedAt:       segments[0].first,
		UpdatedAt:       segments[len(segments)-1].last,
	}
	if err := db.Create(&cycle).Error; err != nil {
		t.Fatalf("seed Codex quota cycle: %v", err)
	}
	for _, seed := range segments {
		segment := entities.QuotaPercentSegment{
			CycleID:          cycle.ID,
			RemainingPercent: seed.remaining,
			FirstObservedAt:  seed.first,
			LastObservedAt:   seed.last,
			ObservationCount: 1,
			CreatedAt:        seed.first,
			UpdatedAt:        seed.last,
		}
		if err := db.Create(&segment).Error; err != nil {
			t.Fatalf("seed Codex quota segment: %v", err)
		}
	}
	return cycle
}

func forkCurrentUsageEventForQuotaEfficiency(key, authType, authIndex string, timestamp time.Time, inputTokens int64) entities.UsageEvent {
	return entities.UsageEvent{
		EventKey:    key,
		Provider:    "codex",
		Model:       "priced-model",
		AuthType:    authType,
		AuthIndex:   authIndex,
		Timestamp:   timestamp,
		InputTokens: inputTokens,
		TotalTokens: inputTokens,
		CreatedAt:   timestamp,
	}
}

func forkCurrentSeedCodexQuotaEfficiencyUsage(t testing.TB, db *gorm.DB, events ...entities.UsageEvent) {
	t.Helper()
	if err := db.Create(&events).Error; err != nil {
		t.Fatalf("seed quota efficiency usage events: %v", err)
	}
}

func forkCurrentCodexQuotaEfficiencyPricingResolver(t testing.TB) pricing.Resolver {
	return forkCurrentCodexQuotaEfficiencyPricingResolverWithRules(t, nil)
}

func forkCurrentCodexQuotaEfficiencyPricingResolverWithRules(t testing.TB, rules []pricing.RuleConfig) pricing.Resolver {
	t.Helper()
	multiplier := 1.0
	snapshot, err := pricing.CompileSnapshot([]pricing.ModelConfig{{
		Pricing: entities.ModelPriceSetting{
			Model:            "priced-model",
			PromptPricePer1M: 1,
			PriceMultiplier:  &multiplier,
		},
		Rules: rules,
	}})
	if err != nil {
		t.Fatalf("compile quota efficiency pricing snapshot: %v", err)
	}
	return pricing.NewCatalog(snapshot).NewResolver()
}

func forkCurrentAssertCodexQuotaEfficiencyUsage(t *testing.T, usage repositorydto.CodexQuotaEfficiencyUsage, tokens int64, cost float64, available bool) {
	t.Helper()
	if usage.TotalTokens != tokens || math.Abs(usage.TotalCostUSD-cost) > 1e-9 || usage.CostAvailable != available {
		t.Fatalf("unexpected quota efficiency usage: got %+v, want tokens=%d cost=%f available=%v", usage, tokens, cost, available)
	}
}

func forkCurrentBuildCodexQuotaEfficiencyForTest(t *testing.T, db *gorm.DB, authIndex string, now time.Time, resolver pricing.Resolver) repositorydto.CodexQuotaEfficiencyHistory {
	t.Helper()
	result, err := repository.BuildCodexQuotaEfficiencyHistory(context.Background(), db, repositorydto.CodexQuotaEfficiencyQuery{
		AuthIndex: authIndex, Now: now, RangeStart: now.Add(-30 * 24 * time.Hour),
	}, resolver)
	if err != nil {
		t.Fatalf("BuildCodexQuotaEfficiencyHistory: %v", err)
	}
	return result
}
