package test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
)

// 恢复游标写失败必须连同两个桶一起回滚；否则重启会再次累加已经写入的金额。
func TestLegacyPricingOverviewCursorFailureRollsBackBothBuckets(t *testing.T) {
	fixture := openPublishedPricingOverviewFixture(t)
	if err := fixture.writer.Exec("DELETE FROM usage_events_archive").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	baseline, err := repository.MigrateLegacyPricingEvents(ctx, fixture.writer, fixture.reader, fixture.backupDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.writer.Exec(`CREATE TRIGGER fail_overview_cursor BEFORE UPDATE OF cursors_json ON pricing_migration_state
		WHEN OLD.phase = 'overview_rebuilding' AND NEW.cursors_json <> OLD.cursors_json
		BEGIN SELECT RAISE(ABORT, 'forced overview cursor failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteLegacyPricingData(ctx, fixture.writer, fixture.reader, baseline); err == nil {
		t.Fatal("游标写入失败不应报告成功")
	}
	for _, table := range []string{"usage_overview_hourly_stats", "usage_overview_daily_stats"} {
		var rows int64
		if err := fixture.writer.Table(table).Count(&rows).Error; err != nil || rows != 0 {
			t.Fatalf("%s failed page left partial rows: %d %v", table, rows, err)
		}
	}
	var state entities.PricingMigrationState
	if err := fixture.writer.Where("id = ?", 1).Take(&state).Error; err != nil {
		t.Fatal(err)
	}
	var cursors repository.PricingMigrationEventCursors
	if state.CursorsJSON == nil {
		t.Fatal("已提交的 M5 初始游标丢失")
	}
	if err := json.Unmarshal([]byte(*state.CursorsJSON), &cursors); err != nil {
		t.Fatal(err)
	}
	if state.DataComplete || cursors.Overview == nil || cursors.Overview.HotAfterID != 0 {
		t.Fatalf("失败页游标被推进或错误宣告完成：%+v", state)
	}
	if err := fixture.writer.Exec("DROP TRIGGER fail_overview_cursor").Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteLegacyPricingData(ctx, fixture.writer, fixture.reader, baseline); err != nil {
		t.Fatalf("故障解除后恢复失败：%v", err)
	}
	for _, table := range []string{"usage_overview_hourly_stats", "usage_overview_daily_stats"} {
		assertLegacyOverviewFee(t, fixture, table, "model-a", 0.00078, 0, 1, 120)
	}
}

// 首次清空两张旧统计表失败时，初始阶段、游标和旧桶必须整体保留。
func TestLegacyPricingOverviewStartFailurePreservesOldBuckets(t *testing.T) {
	fixture := openPublishedPricingOverviewFixture(t)
	ctx := context.Background()
	baseline, err := repository.MigrateLegacyPricingEvents(ctx, fixture.writer, fixture.reader, fixture.backupDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.writer.Exec(`CREATE TRIGGER fail_overview_clear BEFORE DELETE ON usage_overview_daily_stats BEGIN SELECT RAISE(ABORT, 'forced daily clear failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteLegacyPricingData(ctx, fixture.writer, fixture.reader, baseline); err == nil {
		t.Fatal("expected initialization failure")
	}
	for _, table := range []string{"usage_overview_hourly_stats", "usage_overview_daily_stats"} {
		var requests int64
		if err := fixture.writer.Table(table).Select("COALESCE(SUM(request_count),0)").Scan(&requests).Error; err != nil || requests != 1 {
			t.Fatalf("%s old rows lost: %d %v", table, requests, err)
		}
	}
	var state entities.PricingMigrationState
	if err := fixture.writer.Where("id = 1").Take(&state).Error; err != nil {
		t.Fatal(err)
	}
	var cursors repository.PricingMigrationEventCursors
	if state.CursorsJSON == nil {
		t.Fatal("missing event cursor")
	}
	if err := json.Unmarshal([]byte(*state.CursorsJSON), &cursors); err != nil {
		t.Fatal(err)
	}
	if state.Phase != "events_backfilled" || state.DataComplete || cursors.Overview != nil {
		t.Fatalf("failed clear advanced state: %+v", state)
	}
	if err := fixture.writer.Exec("DROP TRIGGER fail_overview_clear").Error; err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteLegacyPricingData(ctx, fixture.writer, fixture.reader, baseline); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"usage_overview_hourly_stats", "usage_overview_daily_stats"} {
		assertLegacyOverviewFee(t, fixture, table, "model-a", 0.00078, 0, 1, 120)
	}
}
