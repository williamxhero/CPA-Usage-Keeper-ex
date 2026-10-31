package test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"cpa-usage-keeper/internal/config"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/pricing"
	"cpa-usage-keeper/internal/repository"
	"gorm.io/gorm"
)

func openLegacyProtectionFixture(t *testing.T) (*gorm.DB, *gorm.DB, string) {
	t.Helper()
	root := t.TempDir()
	db, reader, err := repository.OpenUnmigratedDatabasePools(config.Config{SQLitePath: filepath.Join(root, "old.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if reader != db {
			if sqlDB, err := reader.DB(); err == nil {
				_ = sqlDB.Close()
			}
		}
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	// 先建旧业务物理表，再引导；创建控制表不能反过来决定 legacy 身份。
	for _, statement := range []string{
		`CREATE TABLE usage_events (id INTEGER PRIMARY KEY, timestamp TEXT, total_tokens INTEGER, input_tokens INTEGER, failed BOOLEAN)`,
		`CREATE TABLE usage_events_archive (id INTEGER PRIMARY KEY, timestamp TEXT, total_tokens INTEGER, input_tokens INTEGER, failed BOOLEAN)`,
		`CREATE TABLE usage_overview_hourly_stats (id INTEGER PRIMARY KEY, bucket_start TEXT, request_count INTEGER, success_count INTEGER, failure_count INTEGER, total_tokens INTEGER, input_tokens INTEGER)`,
		`CREATE TABLE usage_overview_daily_stats (id INTEGER PRIMARY KEY, bucket_start TEXT, request_count INTEGER, success_count INTEGER, failure_count INTEGER, total_tokens INTEGER, input_tokens INTEGER)`,
		`CREATE TABLE usage_overview_aggregation_checkpoints (id INTEGER PRIMARY KEY, name TEXT, last_aggregated_usage_event_id INTEGER)`,
		`CREATE TABLE model_price_settings (id INTEGER PRIMARY KEY, model TEXT, prompt_price_per1_m REAL, completion_price_per1_m REAL, price_multiplier REAL)`,
		`CREATE TABLE model_price_rules (id INTEGER PRIMARY KEY, model_price_setting_id INTEGER, key TEXT, value TEXT, multiplier REAL)`,
		`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT)`,
		`INSERT INTO usage_events (id,timestamp,total_tokens,input_tokens,failed) VALUES (1,'2026-09-01T10:00:00Z',10,8,0),(3,'2026-09-01T10:01:00Z',30,25,0),(5,'2026-09-01T10:02:00Z',50,40,1),(8,'2026-09-01T11:00:00Z',80,70,0)`,
		`INSERT INTO usage_events_archive (id,timestamp,total_tokens,input_tokens,failed) VALUES (2,'2026-09-01T10:03:00Z',20,15,0)`,
		`INSERT INTO usage_overview_hourly_stats (id,bucket_start,request_count,success_count,failure_count,total_tokens,input_tokens) VALUES (1,'2026-09-01T10:00:00Z',4,3,1,110,88)`,
		`INSERT INTO usage_overview_daily_stats (id,bucket_start,request_count,success_count,failure_count,total_tokens,input_tokens) VALUES (1,'2026-09-01T00:00:00Z',4,3,1,110,88)`,
		`INSERT INTO usage_overview_aggregation_checkpoints (id,name,last_aggregated_usage_event_id) VALUES (1,'overview',5)`,
		`INSERT INTO model_price_settings (id,model,prompt_price_per1_m,completion_price_per1_m,price_multiplier) VALUES (1,'old-model',1.25,2.5,1.5)`,
		`INSERT INTO model_price_rules (id,model_price_setting_id,key,value,multiplier) VALUES (1,1,'auth_index','old-auth',0.75)`,
		`INSERT INTO schema_migrations (version,applied_at) VALUES ('20260514_create_usage_overview_stats','2026-05-14')`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("seed old physical schema: %v", err)
		}
	}
	state, err := repository.BootstrapPricingInitialization(context.Background(), db)
	if err != nil || state.InitKind != repository.PricingInitKindLegacy {
		t.Fatalf("bootstrap old schema: %+v %v", state, err)
	}
	if err := repository.EnsurePricingBootstrapInbox(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.InsertPricingBootstrapInboxRawMessages(context.Background(), db, "redis_pull:usage", []string{`{"id":1}`, `{"id":1}`}, time.Now()); err != nil {
		t.Fatal(err)
	}
	return db, reader, filepath.Join(root, "backups")
}

func TestPricingLegacyProtectionBindsVerifiedBackupAndReusesItAfterFailure(t *testing.T) {
	db, reader, backupDir := openLegacyProtectionFixture(t)
	ctx := context.Background()
	// 备份保留旧事件原始 nullable 字段，不在保护阶段重写。
	if err := db.Exec("UPDATE usage_events SET failed = NULL WHERE id = 1").Error; err != nil {
		t.Fatal(err)
	}
	first, err := repository.ProtectPricingLegacyMigration(ctx, db, reader, backupDir, time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if first.SchemaVersion != 1 || first.Overview.Cursor != 5 || first.Hot.Count != 4 || first.Hot.MaxID != 8 || first.Archive.Count != 1 || first.InboxMaxID != 2 {
		t.Fatalf("unexpected original evidence: %+v", first)
	}
	if len(first.ModelPriceSettings) != 1 || len(first.ModelPriceRules) != 1 || len(first.SchemaMigrations) != 1 {
		t.Fatalf("original prices or schema missing: %+v", first)
	}
	var state entities.PricingMigrationState
	if err := db.Where("id = 1").Take(&state).Error; err != nil || state.BackupPath == nil || state.BaselineJSON == nil || state.Phase != "protected" {
		t.Fatalf("uncommitted protection: %+v %v", state, err)
	}
	var stored repository.PricingLegacyBaseline
	if err := json.Unmarshal([]byte(*state.BaselineJSON), &stored); err != nil || stored.InboxMaxID != 2 {
		t.Fatalf("persisted inbox boundary: %+v %v", stored, err)
	}
	// 模拟备份后的接收和破坏性旧 migration 中断：重启不得重新备份已变化的 live 库。
	if _, err := repository.InsertPricingBootstrapInboxRawMessages(ctx, db, "redis_pull:usage", []string{`{"id":3}`}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM usage_overview_hourly_stats").Error; err != nil {
		t.Fatal(err)
	}
	if readSQL, err := reader.DB(); err != nil {
		t.Fatal(err)
	} else if err := readSQL.Close(); err != nil {
		t.Fatal(err)
	}
	if writeSQL, err := db.DB(); err != nil {
		t.Fatal(err)
	} else if err := writeSQL.Close(); err != nil {
		t.Fatal(err)
	}
	// 真实关闭连接后重开同一文件，不依赖当前进程保留的内存状态。
	reopened, reopenedReader, err := repository.OpenUnmigratedDatabasePools(config.Config{SQLitePath: filepath.Join(filepath.Dir(backupDir), "old.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if readSQL, err := reopenedReader.DB(); err == nil {
			_ = readSQL.Close()
		}
		if writeSQL, err := reopened.DB(); err == nil {
			_ = writeSQL.Close()
		}
	})
	again, err := repository.ProtectPricingLegacyMigration(ctx, reopened, reopenedReader, filepath.Join(t.TempDir(), "different"), time.Now())
	if err != nil || again.InboxMaxID != 2 || again.Hot.Count != 4 {
		t.Fatalf("did not reuse original verified backup: %+v %v", again, err)
	}
	var reused entities.PricingMigrationState
	if err := reopened.Where("id = 1").Take(&reused).Error; err != nil || *reused.BackupPath != *state.BackupPath {
		t.Fatalf("backup path changed on restart: %+v %v", reused, err)
	}
}

func TestPricingLegacyProtectionRechecksOnlyOriginalEvidenceAfterFixedBaseline(t *testing.T) {
	db, reader, backupDir := openLegacyProtectionFixture(t)
	ctx := context.Background()
	first, err := repository.ProtectPricingLegacyMigration(ctx, db, reader, backupDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	first.Fixed = &repository.PricingMigrationFixedBaseline{
		OverviewCursor: 5, HotMaxID: 8, ArchiveMaxID: 2,
		Configs: []pricing.ModelPricingConfig{},
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entities.PricingMigrationState{}).Where("id = ?", 1).Update("baseline_json", string(encoded)).Error; err != nil {
		t.Fatal(err)
	}
	again, err := repository.ProtectPricingLegacyMigration(ctx, db, reader, filepath.Join(t.TempDir(), "do-not-overwrite"), time.Now())
	if err != nil || again.Fixed == nil || again.Fixed.HotMaxID != 8 || again.InboxMaxID != first.InboxMaxID {
		t.Fatalf("M3 固定字段不应使原备份复验失败：baseline=%+v err=%v", again, err)
	}
}

func TestPricingLegacyProtectionAcceptsCursorPastRetainedMaxID(t *testing.T) {
	db, reader, backupDir := openLegacyProtectionFixture(t)
	for _, statement := range []string{
		"UPDATE usage_overview_aggregation_checkpoints SET last_aggregated_usage_event_id = 20",
		"INSERT INTO usage_overview_hourly_stats (id,bucket_start,request_count,success_count,failure_count,total_tokens,input_tokens) VALUES (2,'2026-09-01T11:00:00Z',1,1,0,80,70)",
		"UPDATE usage_overview_daily_stats SET request_count = 5, success_count = 4, total_tokens = 190, input_tokens = 158",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	baseline, err := repository.ProtectPricingLegacyMigration(context.Background(), db, reader, backupDir, time.Now())
	if err != nil || baseline.Overview.Cursor != 20 || baseline.Hot.MaxID != 8 {
		t.Fatalf("complete coverage with larger cursor rejected: %+v %v", baseline, err)
	}
}

func TestPricingLegacyProtectionBacksUpOldGroupsWithoutRequiringCoverage(t *testing.T) {
	for _, table := range []string{"usage_overview_hourly_stats", "usage_overview_daily_stats"} {
		t.Run(table, func(t *testing.T) {
			db, reader, backupDir := openLegacyProtectionFixture(t)
			if err := db.Table(table).Where("id = ?", 1).Update("bucket_start", "2026-09-02T00:00:00Z").Error; err != nil {
				t.Fatal(err)
			}
			if _, err := repository.ProtectPricingLegacyMigration(context.Background(), db, reader, backupDir, time.Now()); err != nil {
				t.Fatal(err)
			}
			var state entities.PricingMigrationState
			if err := db.Where("id = ?", 1).Take(&state).Error; err != nil || state.BackupPath == nil || state.BaselineJSON == nil {
				t.Fatalf("backup not recorded: %+v %v", state, err)
			}
		})
	}
}

func TestPricingLegacyProtectionReadsEarlierSchemaWithoutArchiveOrRules(t *testing.T) {
	db, reader, backupDir := openLegacyProtectionFixture(t)
	for _, statement := range []string{
		"DROP TABLE usage_events_archive",
		"DROP TABLE model_price_rules",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	baseline, err := repository.ProtectPricingLegacyMigration(context.Background(), db, reader, backupDir, time.Now())
	if err != nil || baseline.Archive.Count != 0 || len(baseline.ModelPriceRules) != 0 {
		t.Fatalf("earlier schema rejected: %+v %v", baseline, err)
	}
}

// M1 只记录旧表实际 schema 和现存 ID 边界，不根据聚合列推断明细表必须具备哪些列。
func TestPricingLegacyProtectionCapturesRetainedBoundsFromEarlierSchema(t *testing.T) {
	db, reader, backupDir := openLegacyProtectionFixture(t)
	if err := db.Exec("ALTER TABLE usage_events_archive DROP COLUMN input_tokens").Error; err != nil {
		t.Fatal(err)
	}
	baseline, err := repository.ProtectPricingLegacyMigration(context.Background(), db, reader, backupDir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Archive.Count != 1 || baseline.Archive.MinID != 2 || baseline.Archive.MaxID != 2 || baseline.Overview.Cursor != 5 {
		t.Fatalf("unexpected retained archive bounds: %+v", baseline)
	}
	for _, column := range baseline.SchemaColumns["usage_events_archive"] {
		if column == "input_tokens" {
			t.Fatal("baseline invented a column absent from the backup")
		}
	}
}

func TestPricingLegacyProtectionRejectsInvalidEventOrCursorBounds(t *testing.T) {
	for _, scenario := range []struct {
		name string
		stmt string
	}{
		{"overlap", `INSERT INTO usage_events_archive (id,timestamp,total_tokens,input_tokens,failed) VALUES (3,'2026-08-01T10:00:00Z',1,1,0)`},
		{"negative_checkpoint", `UPDATE usage_overview_aggregation_checkpoints SET last_aggregated_usage_event_id = -1`},
		{"missing_event_id", `ALTER TABLE usage_events_archive RENAME COLUMN id TO old_id`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db, reader, backupDir := openLegacyProtectionFixture(t)
			if err := db.Exec(scenario.stmt).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := repository.ProtectPricingLegacyMigration(context.Background(), db, reader, backupDir, time.Now()); err == nil {
				t.Fatal("inconsistent old data passed protection")
			}
			var state entities.PricingMigrationState
			if err := db.Where("id = 1").Take(&state).Error; err != nil || state.BackupPath != nil || state.BaselineJSON != nil {
				t.Fatalf("failed protection was recorded: %+v %v", state, err)
			}
		})
	}
}
