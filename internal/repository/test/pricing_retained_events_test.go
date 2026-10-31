package test

import (
	"context"
	"math"
	"testing"
	"time"

	"cpa-usage-keeper/internal/repository"
)

// 合法的历史明细清理不应阻止首次升级，旧聚合应被现存事件的结果替换。
func TestLegacyPricingMigrationRebuildsRetainedEvents(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		hotIDs     []int64
		deleteCold bool
	}{
		{"priced_hot_deleted", []int64{1}, false},
		{"unpriced_hot_deleted", []int64{3}, false},
		{"archive_deleted", nil, true},
		{"only_archive_retained", []int64{1, 3}, false},
		{"all_details_deleted", []int64{1, 3}, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := openPublishedPricingEventFixture(t)
			// 旧水位可以高于现存最大 ID，不能把清理后的 ID 空洞当作缺失错误。
			if err := fixture.writer.Exec("UPDATE usage_aggregation_checkpoints SET last_aggregated_usage_event_id = 99 WHERE name = 'overview'").Error; err != nil {
				t.Fatal(err)
			}
			if len(scenario.hotIDs) > 0 {
				if err := fixture.writer.Table("usage_events").Where("id IN ?", scenario.hotIDs).Delete(nil).Error; err != nil {
					t.Fatal(err)
				}
			}
			if scenario.deleteCold {
				if err := fixture.writer.Exec("DELETE FROM usage_events_archive").Error; err != nil {
					t.Fatal(err)
				}
			}

			ctx := context.Background()
			baseline, err := repository.MigrateLegacyPricingEvents(ctx, fixture.writer, fixture.reader, fixture.backupDir, time.Now())
			if err != nil {
				t.Fatalf("retained events must migrate: %v", err)
			}
			if err := repository.CompleteLegacyPricingData(ctx, fixture.writer, fixture.reader, baseline); err != nil {
				t.Fatal(err)
			}
			var expected struct {
				Requests int64
				Tokens   int64
				Cost     float64
			}
			if err := fixture.writer.Raw("SELECT COUNT(*) AS requests, COALESCE(SUM(total_tokens),0) AS tokens, COALESCE(SUM(cost_usd),0) AS cost FROM (SELECT total_tokens,cost_usd FROM usage_events UNION ALL SELECT total_tokens,cost_usd FROM usage_events_archive)").Scan(&expected).Error; err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"usage_overview_hourly_stats", "usage_overview_daily_stats"} {
				var actual struct {
					Requests int64
					Tokens   int64
					Cost     float64
				}
				if err := fixture.writer.Table(table).Select("COALESCE(SUM(request_count),0) AS requests, COALESCE(SUM(total_tokens),0) AS tokens, COALESCE(SUM(cost_usd),0) AS cost").Scan(&actual).Error; err != nil {
					t.Fatal(err)
				}
				if actual.Requests != expected.Requests || actual.Tokens != expected.Tokens || math.Abs(actual.Cost-expected.Cost) > 1e-12 {
					t.Fatalf("%s got %+v want %+v", table, actual, expected)
				}
			}
		})
	}
}
