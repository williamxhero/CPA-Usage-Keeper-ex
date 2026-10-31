package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/helper"
	"cpa-usage-keeper/internal/repository"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newCLIDatabase(t *testing.T) (string, *gorm.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private # database.db")
	db, err := gorm.Open(sqlite.Open(helper.BuildSQLiteFileURI(path)), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(migrationRequiredModels()...); err != nil {
		t.Fatal(err)
	}
	return path, db
}

func invokeCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runCLI(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func decodeCLIReport(t *testing.T, text string) reportOutput {
	t.Helper()
	var report reportOutput
	if err := json.Unmarshal([]byte(text), &report); err != nil {
		t.Fatalf("invalid report JSON: %v", err)
	}
	return report
}

func TestCLIMigrationLifecycle(t *testing.T) {
	path, db := newCLIDatabase(t)
	t.Setenv("CPA_BASE_URL", "invalid")
	t.Setenv("CPA_MANAGEMENT_KEY", "must-not-load-secret")
	t.Setenv("SQLITE_PATH", filepath.Join(t.TempDir(), "must-not-create.db"))
	model := "private-model-secret"
	if err := db.Create(&entities.ModelPriceSetting{Model: model, PricingStyle: "openai", PromptPricePer1M: 2, CompletionPricePer1M: 3, CacheReadPricePer1M: 1, CacheWritePricePer1M: 4}).Error; err != nil {
		t.Fatal(err)
	}
	// Exercise creation of report tables on a business schema that predates #18.
	if db.Migrator().HasTable(&entities.PricingMigrationBatch{}) {
		t.Fatal("fixture unexpectedly contains migration report tables")
	}
	code, stdout, stderr := invokeCLI(t, "-db", path, "-action", "run", "-batch", "cli-test", "-batch-size", "1")
	if code != 0 || stderr != "" {
		t.Fatalf("run failed: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	report := decodeCLIReport(t, stdout)
	if report.Batch.Status != entities.PricingMigrationStatusSucceeded || report.Batch.ImportedCount != 1 || !repository.IsStrictPricingActivated(db) {
		t.Fatalf("unexpected migration result: %+v", report)
	}
	if strings.Contains(stdout, path) || strings.Contains(stdout, model) || strings.Contains(stdout, "must-not-load-secret") {
		t.Fatal("migration output exposes sensitive values")
	}
	for _, action := range []string{"run", "report", "rollback", "rollback", "restore", "restore"} {
		code, stdout, stderr = invokeCLI(t, "-db", path, "-action", action, "-batch", "cli-test")
		if code != 0 || stderr != "" {
			t.Fatalf("%s failed: code=%d stderr=%q", action, code, stderr)
		}
		report = decodeCLIReport(t, stdout)
		expected := entities.PricingMigrationStatusSucceeded
		if action == "rollback" {
			expected = entities.PricingMigrationStatusRolledBack
		}
		if report.Batch.Status != expected {
			t.Fatalf("%s status=%s expected=%s", action, report.Batch.Status, expected)
		}
	}
	if !repository.IsStrictPricingActivated(db) {
		t.Fatal("restore did not activate strict pricing")
	}
	var targetCount, legacyCount int64
	if err := db.Model(&entities.OfficialModelPrice{}).Count(&targetCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&entities.ModelPriceSetting{}).Count(&legacyCount).Error; err != nil {
		t.Fatal(err)
	}
	if targetCount != 1 || legacyCount != 1 {
		t.Fatal("lifecycle duplicated target facts or deleted legacy facts")
	}
	code, stdout, stderr = invokeCLI(t, "-db", path, "-action", "list")
	var batches []batchOutput
	if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &batches) != nil || len(batches) != 1 || batches[0].ID != "cli-test" {
		t.Fatalf("unexpected batch list: code=%d output=%q stderr=%q", code, stdout, stderr)
	}
}

func TestCLIRejectsUnsafeArgumentsBeforeChangingDatabase(t *testing.T) {
	path, db := newCLIDatabase(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "sensitive-missing.db")
	cases := [][]string{
		{},
		{"-db", path, "-action", "SECRET-ACTION", "-batch", "safe"},
		{"-db", path, "-action", "run"},
		{"-db", path, "-action", "run", "-batch", "../SECRET-BATCH"},
		{"-db", path, "-action", "run", "-batch", strings.Repeat("b", 97)},
		{"-db", path, "-action", "run", "-batch", "safe", "-batch-size", "0"},
		{"-db", path, "-action", "run", "-batch", "safe", "-batch-size", "1001"},
		{"-db", path, "-action", "run", "-batch", "safe", "-batch-size", "SECRET-NUMBER"},
		{"-db", path, "-action", "report", "-batch", "safe", "-batch-size", "1"},
		{"-db", path, "-action", "list", "-batch", "SECRET-BATCH"},
		{"-db", path, "-action", "list", "SECRET-POSITIONAL"},
		{"-SECRET-FLAG", "SECRET-VALUE"},
		{"-db", missing, "-action", "run", "-batch", "safe"},
		{"-db", filepath.Dir(path), "-action", "run", "-batch", "safe"},
		{"-db", ":memory:", "-action", "run", "-batch", "safe"},
		{"-db", "file:" + path, "-action", "run", "-batch", "safe"},
		{"-db", path + "?mode=rwc&secret=SECRET-QUERY", "-action", "run", "-batch", "safe"},
		{"-db", "https://SECRET-HOST/db", "-action", "run", "-batch", "safe"},
		{"-db", `\\SECRET-HOST\share\database.db`, "-action", "run", "-batch", "safe"},
		{"-db", "//SECRET-HOST/share/database.db", "-action", "run", "-batch", "safe"},
	}
	for _, args := range cases {
		code, stdout, stderr := invokeCLI(t, args...)
		if code == 0 || stdout != "" || stderr == "" {
			t.Fatalf("unsafe arguments accepted: code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if strings.Contains(stderr, "SECRET-") || strings.Contains(stderr, path) || strings.Contains(stderr, missing) {
			t.Fatal("argument error echoes sensitive input")
		}
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("CLI created missing database")
	}
	if db.Migrator().HasTable(&entities.PricingMigrationBatch{}) {
		t.Fatal("invalid arguments created migration tables")
	}
	after, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("invalid arguments changed the database")
	}
}

func TestCLIInvalidDatabaseAndSchema(t *testing.T) {
	for _, contents := range []string{"", "SECRET-CORRUPT-DATABASE"} {
		path := filepath.Join(t.TempDir(), "private.db")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := invokeCLI(t, "-db", path, "-action", "run", "-batch", "safe")
		if code == 0 || stdout != "" || stderr == "" || strings.Contains(stderr, path) || strings.Contains(stderr, "SECRET-") {
			t.Fatalf("invalid database error: code=%d output=%q stderr=%q", code, stdout, stderr)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != contents {
			t.Fatal("CLI modified invalid database")
		}
	}
	path, db := newCLIDatabase(t)
	if err := db.Migrator().DropColumn(&entities.CredentialModelMultiplier{}, "mode"); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := invokeCLI(t, "-db", path, "-action", "run", "-batch", "safe")
	if code == 0 || stdout != "" || !strings.Contains(stderr, "required pricing schema") || db.Migrator().HasTable(&entities.PricingMigrationBatch{}) {
		t.Fatal("CLI repaired incomplete business schema or began migration")
	}
}

func TestCLIReportAndListAreReadOnlyAndErrorsAreSanitized(t *testing.T) {
	path, db := newCLIDatabase(t)
	if err := db.AutoMigrate(migrationReportModels()...); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-action", "list"}, {"-action", "report", "-batch", "missing"}} {
		code, stdout, stderr := invokeCLI(t, append([]string{"-db", path}, args...)...)
		if args[1] == "list" {
			if code != 0 || strings.TrimSpace(stdout) != "[]" || stderr != "" {
				t.Fatal("empty list failed")
			}
		} else if code != 1 || stdout != "" || !strings.Contains(stderr, "not found") {
			t.Fatal("missing batch did not return safe error")
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("report/list changed the database")
	}
	// Driver errors can include trigger messages, SQL text, paths, or secrets.
	if err := db.Exec(`CREATE TRIGGER migration_error BEFORE INSERT ON pricing_migration_batches BEGIN SELECT RAISE(ABORT, 'SECRET-TRIGGER /private/path'); END`).Error; err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := invokeCLI(t, "-db", path, "-action", "run", "-batch", "safe")
	if code != 1 || stdout != "" || stderr == "" || strings.Contains(stderr, "SECRET-") || strings.Contains(stderr, "/private/path") {
		t.Fatal("repository failure leaked underlying error")
	}
}

func TestCLIInvalidPricesReturnFailedReportWithoutAdoption(t *testing.T) {
	path, db := newCLIDatabase(t)
	if err := db.Create(&entities.ModelPriceSetting{Model: "model-a", PricingStyle: "openai", PromptPricePer1M: -2}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&entities.OfficialModelPrice{CanonicalModelID: "model-a", DisplayModelID: "model-a", PricingStyle: "openai", Source: "manual", PromptPricePer1M: new(9.0), CompletionPricePer1M: new(0.0), CacheReadPricePer1M: new(0.0), CacheWritePricePer1M: new(0.0)}).Error; err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := invokeCLI(t, "-db", path, "-action", "run", "-batch", "invalid-test")
	if code != 1 || stderr == "" {
		t.Fatal("invalid facts did not fail the CLI operation")
	}
	report := decodeCLIReport(t, stdout)
	if report.Batch.Status != entities.PricingMigrationStatusFailed || report.Batch.RejectedCount != 1 || repository.IsStrictPricingActivated(db) {
		t.Fatalf("unexpected failed report: %+v", report)
	}
	var target entities.OfficialModelPrice
	if err := db.Where("canonical_model_id = ?", "model-a").First(&target).Error; err != nil {
		t.Fatal(err)
	}
	if target.PromptPricePer1M == nil || *target.PromptPricePer1M != 9 {
		t.Fatal("invalid migration changed administrator target price")
	}
	code, stdout, stderr = invokeCLI(t, "-db", path, "-action", "report", "-batch", "invalid-test")
	if code != 0 || stderr != "" || decodeCLIReport(t, stdout).Batch.Status != entities.PricingMigrationStatusFailed {
		t.Fatal("failed migration report is not durably queryable")
	}
}

func TestCLIReportProjectionExcludesPrivateFields(t *testing.T) {
	report := repository.PricingMigrationReport{
		Batch:  entities.PricingMigrationBatch{ID: "safe", Status: entities.PricingMigrationStatusFailed, PreviousSnapshotJSON: "SECRET-SNAPSHOT", FailureReason: "SECRET-ERROR /private/path"},
		Counts: []entities.PricingMigrationBatchCount{{Category: "SECRET-CATEGORY"}},
		Items:  []entities.PricingMigrationItem{{Category: entities.PricingMigrationCategoryModelPrices, SourceRef: "SECRET-SOURCE", TargetRef: "SECRET-TARGET", InputJSON: "SECRET-INPUT", Reason: "SECRET-REASON"}},
	}
	encoded, err := json.Marshal(summarizeReport(report))
	if err != nil || strings.Contains(string(encoded), "SECRET-") || strings.Contains(string(encoded), "/private/path") {
		t.Fatal("report projection exposed private values")
	}
}
