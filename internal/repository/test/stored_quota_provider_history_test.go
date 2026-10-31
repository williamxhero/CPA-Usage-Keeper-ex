package test

import (
	"context"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	repositorydto "cpa-usage-keeper/internal/repository/dto"
)

func TestStoredQuotaHistoryProviderKeyIsolationAcrossWriteReadAndDelete(t *testing.T) {
	db := openTestDatabase(t)
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	reset := now.Add(5 * time.Hour)
	codex := codexQuotaHistoryObservation("shared-auth", "primary", 18_000, reset, 91, now)
	claude := codexQuotaHistoryObservation("shared-auth", "primary", 18_000, reset, 75, now)
	claude.Provider, claude.QuotaKey = "claude", "five_hour"
	if err := repository.WriteCodexMainQuotaObservations(context.Background(), db, []repositorydto.CodexMainQuotaObservation{codex, claude}); err != nil {
		t.Fatal(err)
	}
	var cycles []entities.QuotaCycle
	if err := db.Order("provider ASC").Find(&cycles).Error; err != nil {
		t.Fatal(err)
	}
	if len(cycles) != 2 || cycles[0].Provider != "claude" || cycles[0].QuotaKey != "five_hour" || cycles[1].Provider != "codex" || cycles[1].QuotaKey != "rate_limit.primary_window" {
		t.Fatalf("provider/key were not isolated: %+v", cycles)
	}
	state, err := repository.LoadLatestQuotaHistoryState(context.Background(), db, "claude", "shared-auth", "primary")
	if err != nil || !state.Found || state.TailRemainingPercent != 75 {
		t.Fatalf("Claude recovery: %+v err=%v", state, err)
	}
	read, err := repository.BuildStoredCodexQuotaEfficiencyHistory(context.Background(), db, repositorydto.CodexQuotaEfficiencyQuery{
		Provider: "claude", AuthIndex: "shared-auth", Now: now.Add(time.Minute), RangeStart: now.Add(-30 * 24 * time.Hour),
	})
	if err != nil || len(read.Cycles) != 1 || read.Cycles[0].LastRemainingPercent == nil || *read.Cycles[0].LastRemainingPercent != 75 {
		t.Fatalf("Claude history query crossed provider boundary: %+v err=%v", read, err)
	}
	if _, err := repository.DeleteQuotaCycle(context.Background(), db, "codex", "shared-auth", cycles[0].ID); err == nil {
		t.Fatal("Codex deletion accepted Claude cycle")
	}
	if _, err := repository.DeleteQuotaCycle(context.Background(), db, "claude", "shared-auth", cycles[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := db.Find(&cycles).Error; err != nil {
		t.Fatal(err)
	}
	if len(cycles) != 1 || cycles[0].Provider != "codex" {
		t.Fatalf("Claude deletion touched Codex cycle: %+v", cycles)
	}
}

func TestStoredQuotaHistoryUsageIsolatedByProvider(t *testing.T) {
	db := openTestDatabase(t)
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	for _, provider := range []string{"codex", "claude"} {
		observation := codexQuotaHistoryObservation("shared-auth", "primary", 18000, now.Add(time.Hour), 75, now.Add(-time.Hour))
		observation.Provider = provider
		observation.QuotaKey, _ = repositorydto.QuotaWindowKey(provider, "primary")
		if err := repository.WriteCodexMainQuotaObservations(context.Background(), db, []repositorydto.CodexMainQuotaObservation{observation}); err != nil {
			t.Fatal(err)
		}
	}
	for i, provider := range []string{"codex", "claude"} {
		event := usageEventForQuotaEfficiency(provider, "oauth", "shared-auth", now.Add(-30*time.Minute), int64(i+1)*1000000)
		event.Provider = provider
		seedCodexQuotaEfficiencyUsage(t, db, event)
	}
	for i, provider := range []string{"codex", "claude"} {
		result, err := repository.BuildStoredCodexQuotaEfficiencyHistory(context.Background(), db, repositorydto.CodexQuotaEfficiencyQuery{Provider: provider, AuthIndex: "shared-auth", Now: now, RangeStart: now.Add(-24 * time.Hour)})
		if err != nil || len(result.Cycles) != 1 {
			t.Fatalf("%s: %+v %v", provider, result, err)
		}
		assertStoredCodexQuotaEfficiencyUsage(t, result.Cycles[0].Usage, int64(i+1)*1000000, float64(i+1), true)
	}
}

func TestStoredDeleteCodexQuotaCycleKeepsRemainingRoleHistoryVisible(t *testing.T) {
	db := openTestDatabase(t)
	now := time.Now().Truncate(time.Second)
	old := seedCodexQuotaEfficiencyRoleCycle(t, db, "delete-auth", entities.CodexQuotaWindowRolePrimary,
		now.Add(-4*24*time.Hour), now.Add(3*24*time.Hour), []codexQuotaEfficiencySegmentSeed{{remaining: 33, first: now.Add(-2 * time.Hour), last: now.Add(-2 * time.Hour)}})
	latest := seedCodexQuotaEfficiencyRoleCycle(t, db, "delete-auth", entities.CodexQuotaWindowRolePrimary,
		now.Add(-2*24*time.Hour), now.Add(5*24*time.Hour), []codexQuotaEfficiencySegmentSeed{{remaining: 100, first: now.Add(-time.Hour), last: now.Add(-time.Hour)}})
	seedCodexQuotaEfficiencyRoleCycle(t, db, "delete-auth", entities.CodexQuotaWindowRoleSecondary,
		now.Add(-time.Hour), now.Add(4*time.Hour), []codexQuotaEfficiencySegmentSeed{{remaining: 80, first: now.Add(-time.Hour), last: now.Add(-time.Hour)}})
	if _, err := repository.DeleteCodexQuotaCycle(context.Background(), db, "delete-auth", latest.ID); err != nil {
		t.Fatal(err)
	}
	role := "primary"
	query := repositorydto.CodexQuotaEfficiencyQuery{AuthIndex: "delete-auth", WindowRole: &role, Now: now, RangeStart: now.Add(-30 * 24 * time.Hour)}
	history, err := repository.BuildStoredCodexQuotaEfficiencyHistory(context.Background(), db, query)
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Windows) != 2 || history.SelectedWindow == nil || len(history.Cycles) != 1 || history.Cycles[0].ID != old.ID || history.Cycles[0].Status != "current" {
		t.Fatalf("remaining primary history is inaccessible: %+v", history)
	}
	defaultQuery := query
	defaultQuery.WindowRole = nil
	defaultHistory, err := repository.BuildStoredCodexQuotaEfficiencyHistory(context.Background(), db, defaultQuery)
	if err != nil || defaultHistory.SelectedWindow == nil || defaultHistory.SelectedWindow.WindowRole != "secondary" {
		t.Fatalf("older history replaced the latest default role: %+v, %v", defaultHistory, err)
	}
	// 最新角色自然到期后，仍保留它作为默认历史；旧角色较远的 reset 不能抢回默认选择。
	defaultQuery.Now = now.Add(5 * time.Hour)
	defaultHistory, err = repository.BuildStoredCodexQuotaEfficiencyHistory(context.Background(), db, defaultQuery)
	if err != nil || defaultHistory.SelectedWindow == nil || defaultHistory.SelectedWindow.WindowRole != "secondary" || len(defaultHistory.Cycles) != 1 || defaultHistory.Cycles[0].Status != "completed" {
		t.Fatalf("expired latest role lost its default selection: %+v, %v", defaultHistory, err)
	}
	if _, err := repository.DeleteCodexQuotaCycle(context.Background(), db, "delete-auth", old.ID); err != nil {
		t.Fatal(err)
	}
	history, err = repository.BuildStoredCodexQuotaEfficiencyHistory(context.Background(), db, query)
	if err != nil || len(history.Windows) != 1 || history.SelectedWindow != nil {
		t.Fatalf("empty primary role still selectable: %+v, %v", history, err)
	}
}
