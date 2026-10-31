package test

import (
	"context"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/poller"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/service"
	"errors"
	"fmt"
	"gorm.io/gorm"
	"strings"
	"testing"
)

func TestR1RedisPostCommitAggregationFailureKeepsTruthfulCounts(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async=%t", async), func(t *testing.T) {
			db := openSyncTestDatabase(t)
			seedRedisInboxMessagesForTest(t, db, `{"timestamp":"2026-10-29T08:00:00Z","provider":"claude","endpoint":"/v1/messages","auth_type":"api_key","model":"sonnet","execution_id":"fixture-execution","request_id":"r1-commit","tokens":{"input_tokens":1,"output_tokens":2}}`)
			runner := poller.NewUsageAggregationRunner(db)
			options := service.SyncServiceOptions{PricingCatalog: emptyPricingCatalogForTest(), BaseURL: "https://cpa.example.com"}
			if async {
				options.UsageAggregationNotifier = runner
			}
			syncer := service.NewSyncServiceWithOptions(db, options)
			failure := errors.New("r1 injected overview failure after facts commit")
			callback := "r1_overview_insert_failure"
			if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "usage_overview_hourly_stats" {
					tx.AddError(failure)
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer db.Callback().Create().Remove(callback)
			result, err := syncer.ProcessRedisUsageInbox(context.Background())
			if result == nil || result.InsertedEvents != 1 || result.ProcessedRows != 1 {
				t.Fatalf("committed counts hidden: result=%+v err=%v", result, err)
			}
			if async {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = runner.RunOnce(context.Background()); err == nil {
					t.Fatal("async injected aggregation failure did not fire")
				}
			} else if err == nil || !strings.Contains(err.Error(), failure.Error()) {
				t.Fatalf("expected postcommit failure: %v", err)
			}
			var count int64
			if err := db.Model(&entities.UsageEvent{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("committed facts=%d err=%v", count, err)
			}
			var inbox entities.RedisUsageInbox
			if err := db.Take(&inbox).Error; err != nil || inbox.Status != repository.RedisUsageInboxStatusProcessed {
				t.Fatalf("inbox=%+v err=%v", inbox, err)
			}
			if err := db.Callback().Create().Remove(callback); err != nil {
				t.Fatal(err)
			}
			retry, err := syncer.ProcessRedisUsageInbox(context.Background())
			if err != nil || retry.InsertedEvents != 0 || !retry.Empty {
				t.Fatalf("retry=%+v err=%v", retry, err)
			}
			if async {
				for i := 0; i < 4; i++ {
					if _, err := runner.RunOnce(context.Background()); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := repository.AggregateUsageOverviewStats(context.Background(), db, inbox.UpdatedAt); err != nil {
				t.Fatal(err)
			}
			var row entities.UsageOverviewHourlyStat
			if err := db.Take(&row).Error; err != nil || row.RequestCount != 1 || row.TotalTokens != 3 {
				t.Fatalf("rollup=%+v err=%v", row, err)
			}
			if err := db.Model(&entities.UsageEvent{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("retry duplicated facts=%d err=%v", count, err)
			}
			var fact entities.UsageEvent
			if err := db.Take(&fact).Error; err != nil || fact.CostUSD != nil || fact.CostAvailable != nil {
				t.Fatalf("fact was repriced=%+v err=%v", fact, err)
			}
		})
	}
}
