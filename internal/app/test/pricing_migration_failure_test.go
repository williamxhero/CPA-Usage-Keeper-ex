package test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	keeperapp "cpa-usage-keeper/internal/app"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestLegacyMigrationFailureLogsVerifiedV1Rollback(t *testing.T) {
	for _, scenario := range []string{"baseline_fixed", "overview_rebuilding", "backup_missing", "backup_changed", "backup_creation_failed", "fresh", "completed"} {
		t.Run(scenario, func(t *testing.T) {
			oldHooks := logrus.StandardLogger().ReplaceHooks(make(logrus.LevelHooks))
			hook := logtest.NewGlobal()
			t.Cleanup(func() { logrus.StandardLogger().ReplaceHooks(oldHooks) })
			cfg := testAppConfig(t)
			wantGuidance := scenario == "baseline_fixed" || scenario == "overview_rebuilding"
			if scenario != "fresh" {
				seed := seedPublishedPricingRuntime(t, cfg)
				if _, err := repository.BootstrapPricingInitialization(context.Background(), seed); err != nil {
					t.Fatal(err)
				}
				switch scenario {
				case "baseline_fixed", "overview_rebuilding":
					statement := `CREATE TRIGGER fail_upgrade BEFORE UPDATE OF phase ON pricing_migration_state WHEN NEW.phase = '` + scenario + `' BEGIN SELECT RAISE(ABORT, 'injected migration failure'); END`
					if err := seed.Exec(statement).Error; err != nil {
						t.Fatal(err)
					}
				case "backup_missing", "backup_changed":
					if _, err := repository.ProtectPricingLegacyMigration(context.Background(), seed, seed, cfg.BackupDir, time.Now()); err != nil {
						t.Fatal(err)
					}
					var state entities.PricingMigrationState
					if err := seed.Take(&state).Error; err != nil || state.BackupPath == nil {
						t.Fatalf("backup state: %+v %v", state, err)
					}
					if scenario == "backup_missing" {
						if err := os.Remove(*state.BackupPath); err != nil {
							t.Fatal(err)
						}
					} else if err := os.WriteFile(*state.BackupPath, []byte("not the verified backup"), 0600); err != nil {
						t.Fatal(err)
					}
				case "backup_creation_failed":
					cfg.BackupDir = filepath.Join(t.TempDir(), "not-a-directory")
					if err := os.WriteFile(cfg.BackupDir, []byte("blocked"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				closePricingStartupPools(t, seed, seed)
				if scenario == "completed" {
					application := newStoredUpgradeInitializedApp(t, cfg)
					if err := application.Close(); err != nil {
						t.Fatal(err)
					}
					hook.Reset()
				}
			}
			application, err := keeperapp.NewWithConfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = application.Close() })
			err = application.InitializeStoredPricingUpgrade(context.Background())
			success := scenario == "fresh" || scenario == "completed"
			if (err == nil) != success {
				t.Fatalf("startup error=%v, want success=%t", err, success)
			}
			if wantGuidance && !strings.Contains(err.Error(), "injected migration failure") {
				t.Fatalf("lost original error: %v", err)
			}
			count := 0
			for _, entry := range hook.AllEntries() {
				if !strings.Contains(entry.Message, "To roll back") {
					continue
				}
				count++
				if entry.Level != logrus.WarnLevel {
					t.Fatalf("unexpected log level: %s", entry.Level)
				}
				for _, part := range []string{"v1-to-v2 database migration failed", "stop Keeper v2", "preserve the failed database", "copy the pre-upgrade backup at backup_path", "WORK_DIR as app.db", "start Keeper v1 with that WORK_DIR", "/data/app.db", "Do not open the partially migrated database with v1", "usage received after that point will not be included"} {
					if !strings.Contains(entry.Message, part) {
						t.Fatalf("missing %q: %s", part, entry.Message)
					}
				}
				var state entities.PricingMigrationState
				if err := application.DB.Take(&state).Error; err != nil {
					t.Fatal(err)
				}
				if state.BackupPath == nil || entry.Data["backup_path"] != *state.BackupPath || state.DataComplete {
					t.Fatalf("invalid recovery guidance: %+v state=%+v", entry.Data, state)
				}
			}
			want := 0
			if wantGuidance {
				want = 1
			}
			if count != want {
				t.Fatalf("guidance count=%d, want %d", count, want)
			}
		})
	}
}
