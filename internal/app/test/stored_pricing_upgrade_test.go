package test

import (
	"context"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"

	keeperapp "cpa-usage-keeper/internal/app"
	"cpa-usage-keeper/internal/backup"
	"cpa-usage-keeper/internal/entities"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestStoredUpgradeDefaultStartupDoesNotReplayAndRejectsUnprovenBaseline(t *testing.T) {
	cfg := pricingRuntimeConfig(t)
	seed := seedPublishedPricingRuntime(t, cfg)
	closePricingStartupPools(t, seed, seed)
	runtime := startPricingEvidenceRuntime(t, cfg)
	base := "http://" + cfg.ListenAddress() + cfg.AppBasePath
	waitPricingRuntimePhase(t, base, "ready")
	status, body := pricingRuntimeRequest(t, http.MethodGet, base+"/api/v1/usage/events?range=custom&start=2026-09-23&end=2026-09-24", nil)
	if status != http.StatusOK {
		t.Fatalf("ordinary startup lost current history reads: %d %s", status, body)
	}
	var ordinary entities.PricingMigrationState
	if err := runtime.app.DB.Take(&ordinary).Error; err != nil {
		t.Fatal(err)
	}
	if ordinary.InitKind != "legacy" || ordinary.Phase != "opening" || ordinary.DataComplete || ordinary.BackupPath != nil || ordinary.BaselineJSON != nil || ordinary.CursorsJSON != nil {
		t.Fatalf("ordinary RunContext implicitly upgraded history: %+v", ordinary)
	}
	var event entities.UsageEvent
	if err := runtime.app.DB.Where("event_key = ?", "old-usage").Take(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.CostUSD != nil || event.CostAvailable != nil || event.InputTokens != 100 || event.TotalTokens != 100 {
		t.Fatalf("ordinary startup repriced or lost legacy facts: %+v", event)
	}
	if err := runtime.app.InitializeStoredPricingUpgrade(context.Background()); err == nil {
		t.Fatal("upgrade accepted an already initialized application")
	}
	if err := runtime.app.RunStoredPricingUpgradeContext(context.Background()); err == nil {
		t.Fatal("upgrade restarted an already running application")
	}
	runtime.stop(t)

	// Once ordinary startup has prepared a current schema, it is no longer a
	// proven pre-upgrade v1 file. Do not relax the fixed-before-structure guard,
	// create a misleading v1 backup, or print v1 recovery guidance for that file.
	originalHooks := logrus.StandardLogger().ReplaceHooks(make(logrus.LevelHooks))
	hook := logtest.NewGlobal()
	t.Cleanup(func() { logrus.StandardLogger().ReplaceHooks(originalHooks) })
	rejected, err := keeperapp.NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rejected.Close() })
	if err := rejected.InitializeStoredPricingUpgrade(context.Background()); err == nil {
		t.Fatal("structured database without pre-structure evidence was accepted")
	}
	var preserved entities.PricingMigrationState
	if err := rejected.DB.Take(&preserved).Error; err != nil {
		t.Fatal(err)
	}
	if preserved.BackupPath != nil || preserved.BaselineJSON != nil || preserved.CursorsJSON != nil || preserved.DataComplete {
		t.Fatalf("unsafe operation invented pre-upgrade protection: %+v", preserved)
	}
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "To roll back") {
			t.Fatalf("unproven current-schema backup was advertised as v1-safe: %+v", entry)
		}
	}
}

func TestStoredUpgradeCompletedRestartPreservesProtectedBackupAndStoredAmounts(t *testing.T) {
	cfg := testAppConfig(t)
	seed := seedPublishedPricingRuntime(t, cfg)
	closePricingStartupPools(t, seed, seed)
	application := newStoredUpgradeInitializedApp(t, cfg)
	var completed entities.PricingMigrationState
	if err := application.DB.Take(&completed).Error; err != nil {
		t.Fatal(err)
	}
	if completed.InitKind != "legacy" || !completed.SchemaComplete || !completed.DataComplete || completed.Phase != "data_complete" || completed.BackupPath == nil || completed.BaselineJSON == nil || completed.CursorsJSON == nil {
		t.Fatalf("explicit operation did not complete protected migration: %+v", completed)
	}
	if _, err := os.Stat(*completed.BackupPath); err != nil {
		t.Fatal(err)
	}
	var event entities.UsageEvent
	if err := application.DB.Where("event_key = ?", "old-usage").Take(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.CostUSD == nil || event.CostAvailable == nil || !*event.CostAvailable || math.Abs(*event.CostUSD-0.0002) > 1e-12 || event.InputTokens != 100 || event.TotalTokens != 100 {
		t.Fatalf("explicit operation lost frozen-price amounts/facts: %+v", event)
	}
	var checkpoint entities.UsageAggregationCheckpoint
	if err := application.DB.Where("name = ?", entities.UsageAggregationCheckpointOverview).Take(&checkpoint).Error; err != nil || checkpoint.LastAggregatedUsageEventID != event.ID {
		t.Fatalf("explicit operation changed checkpoint: %+v %v", checkpoint, err)
	}
	protectedPath := *completed.BackupPath
	fingerprint, err := backup.PricingBackupSHA256(context.Background(), protectedPath)
	if err != nil {
		t.Fatal(err)
	}
	// A completed upgrade must not replay the old frozen tariff on a later run.
	if err := application.DB.Model(&entities.UsageEvent{}).Where("id = ?", event.ID).Updates(map[string]any{"cost_usd": 7.0, "cost_available": false}).Error; err != nil {
		t.Fatal(err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		restarted, err := keeperapp.NewWithConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = restarted.Close() })
		if explicit {
			err = restarted.InitializeStoredPricingUpgrade(context.Background())
		} else {
			err = restarted.Initialize(context.Background())
		}
		if err != nil {
			t.Fatal(err)
		}
		var preserved entities.PricingMigrationState
		if err := restarted.DB.Take(&preserved).Error; err != nil {
			t.Fatal(err)
		}
		if !preserved.DataComplete || preserved.BackupPath == nil || *preserved.BackupPath != protectedPath || preserved.BaselineJSON == nil || *preserved.BaselineJSON != *completed.BaselineJSON || preserved.CursorsJSON == nil || *preserved.CursorsJSON != *completed.CursorsJSON {
			t.Fatalf("restart replaced protection/recovery evidence: %+v", preserved)
		}
		var stored entities.UsageEvent
		if err := restarted.DB.Where("id = ?", event.ID).Take(&stored).Error; err != nil {
			t.Fatal(err)
		}
		if stored.CostUSD == nil || *stored.CostUSD != 7 || stored.CostAvailable == nil || *stored.CostAvailable || stored.TotalTokens != 100 {
			t.Fatalf("completed restart replayed old fees: %+v", stored)
		}
		if err := restarted.Close(); err != nil {
			t.Fatal(err)
		}
	}
	after, err := backup.PricingBackupSHA256(context.Background(), protectedPath)
	if err != nil || after != fingerprint {
		t.Fatalf("protected backup changed across normal/explicit restart: %s %s %v", fingerprint, after, err)
	}
}

func TestStoredUpgradeRejectsNilAndTLSFailureBeforeOpeningDatabase(t *testing.T) {
	var missing *keeperapp.App
	if err := missing.InitializeStoredPricingUpgrade(nil); err == nil {
		t.Fatal("nil explicit initializer succeeded")
	}
	if err := missing.RunStoredPricingUpgradeContext(nil); err == nil {
		t.Fatal("nil explicit runtime succeeded")
	}
	cfg := pricingRuntimeConfig(t)
	cfg.TLSEnabled = true
	cfg.TLSCertFile, cfg.TLSKeyFile = t.TempDir()+"/missing-cert.pem", t.TempDir()+"/missing-key.pem"
	application, err := keeperapp.NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	if err := application.RunStoredPricingUpgradeContext(context.Background()); err == nil {
		t.Fatal("explicit upgrade ran without a functioning TLS shell")
	}
	if _, err := os.Stat(cfg.SQLitePath); !os.IsNotExist(err) {
		t.Fatalf("explicit TLS failure opened the database: %v", err)
	}
}
