package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"cpa-usage-keeper/internal/backup"
	"cpa-usage-keeper/internal/config"
	"cpa-usage-keeper/internal/entities"
	"cpa-usage-keeper/internal/repository"
	repodto "cpa-usage-keeper/internal/repository/dto"
	"gorm.io/gorm"
)

func TestCLI(t *testing.T) {
	binaryPath := buildCLI(t)
	t.Run("stored pricing upgrade is explicit", func(t *testing.T) {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("explicit=%t", explicit), func(t *testing.T) {
				runtimeDir := t.TempDir()
				workDir := filepath.Join(runtimeDir, "data")
				if err := os.Mkdir(workDir, 0o700); err != nil {
					t.Fatal(err)
				}
				cfg := config.Config{SQLitePath: filepath.Join(workDir, "app.db"), BackupDir: filepath.Join(workDir, "backups")}
				seedCLILegacyPricing(t, cfg)
				runCLIToPhase(t, binaryPath, runtimeDir, explicit, "ready")
				db, reader, err := repository.OpenUnmigratedDatabasePools(cfg)
				if err != nil {
					t.Fatal(err)
				}
				closePools := func() { closeCLIPools(t, db, reader) }
				defer func() { closePools() }()
				var state entities.PricingMigrationState
				var event entities.UsageEvent
				if err := db.Take(&state).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Where("event_key = ?", "cli-old-usage").Take(&event).Error; err != nil {
					t.Fatal(err)
				}
				if event.InputTokens != 100 || event.TotalTokens != 100 || state.InitKind != "legacy" {
					t.Fatalf("CLI changed historical facts/identity: %+v %+v", event, state)
				}
				if !explicit {
					if state.DataComplete || state.Phase != "opening" || state.BackupPath != nil || state.BaselineJSON != nil || state.CursorsJSON != nil || event.CostUSD != nil || event.CostAvailable != nil {
						t.Fatalf("default CLI implicitly upgraded stored history: %+v %+v", state, event)
					}
					closePools()
					closePools = func() {}
					// Ordinary startup creates current structure, not a v1 backup.
					// A later explicit invocation must refuse unproven provenance.
					output := runCLIToPhase(t, binaryPath, runtimeDir, true, "failed")
					if !strings.Contains(output, "no fixed legacy baseline") || strings.Contains(output, "To roll back") {
						t.Fatalf("CLI did not reject unproven protection safely: %s", output)
					}
					files, err := filepath.Glob(filepath.Join(cfg.BackupDir, "*"))
					if err != nil || len(files) != 0 {
						t.Fatalf("rejected CLI created a misleading v1 backup: %v %v", files, err)
					}
					return
				}
				if !state.SchemaComplete || !state.DataComplete || state.Phase != "data_complete" || state.BackupPath == nil || state.BaselineJSON == nil || state.CursorsJSON == nil || event.CostUSD == nil || event.CostAvailable == nil || !*event.CostAvailable || math.Abs(*event.CostUSD-0.0002) > 1e-12 {
					t.Fatalf("explicit CLI did not protect and complete stored upgrade: %+v %+v", state, event)
				}
				var baseline repository.PricingLegacyBaseline
				if err := json.Unmarshal([]byte(*state.BaselineJSON), &baseline); err != nil || baseline.Fixed == nil {
					t.Fatalf("explicit CLI has no fixed pre-structure evidence: %+v %v", baseline, err)
				}
				if _, err := backup.PricingBackupSHA256(context.Background(), *state.BackupPath); err != nil {
					t.Fatalf("explicit CLI backup is unavailable or not static: %v", err)
				}
				var checkpoint entities.UsageAggregationCheckpoint
				if err := db.Where("name = ?", entities.UsageAggregationCheckpointOverview).Take(&checkpoint).Error; err != nil || checkpoint.LastAggregatedUsageEventID != event.ID {
					t.Fatalf("CLI lost aggregation checkpoint: %+v %v", checkpoint, err)
				}
			})
		}
	})

	t.Run("version flags", func(t *testing.T) {
		for _, flagName := range []string{"-v", "--version"} {
			t.Run(flagName, func(t *testing.T) {
				command := exec.Command(binaryPath, flagName)
				command.Dir = t.TempDir()
				command.Env = []string{}
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("run %s: %v\n%s", flagName, err, output)
				}
				if got := strings.TrimSpace(string(output)); got != "v9.8.7" {
					t.Fatalf("expected injected version, got %q", got)
				}
			})
		}
	})

	t.Run("authentication configuration", func(t *testing.T) {
		baseEnv := []string{
			"TZ=UTC",
			"CPA_BASE_URL=http://127.0.0.1:8317",
			"CPA_MANAGEMENT_KEY=test-management-key",
		}
		for _, testCase := range []struct {
			name    string
			env     []string
			message string
		}{
			{
				name:    "missing auth setting and password",
				message: "AUTH_ENABLED is not set, so authentication defaults to true; LOGIN_PASSWORD is required",
			},
			{
				name:    "public example password",
				env:     []string{"AUTH_ENABLED=true", "LOGIN_PASSWORD=replace-with-your-login-password"},
				message: "LOGIN_PASSWORD must not use the public example value",
			},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				command := exec.Command(binaryPath)
				command.Dir = t.TempDir()
				command.Env = append(append([]string{}, baseEnv...), testCase.env...)
				output, err := command.CombinedOutput()
				if err == nil {
					t.Fatal("expected invalid authentication configuration to stop startup")
				}
				text := string(output)
				if !strings.Contains(text, "initialize app") || !strings.Contains(text, testCase.message) {
					t.Fatalf("expected startup log to explain the authentication configuration error, got %q", text)
				}
			})
		}
	})

	t.Run("host flag", func(t *testing.T) {
		port := reserveLoopbackPort(t)
		runtimeDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(runtimeDir, "data"), 0o700); err != nil {
			t.Fatalf("create work directory: %v", err)
		}
		envPath := filepath.Join(runtimeDir, "keeper.env")
		envContent := strings.Join([]string{
			"APP_HOST=192.0.2.1",
			"APP_PORT=" + port,
			"CPA_BASE_URL=http://127.0.0.1:1",
			"CPA_MANAGEMENT_KEY=secret",
			"REDIS_QUEUE_ADDR=127.0.0.1:1",
			"AUTH_ENABLED=false",
			"WORK_DIR=./data",
			"LOG_FILE_ENABLED=false",
			"BACKUP_ENABLED=false",
			"TZ=UTC",
			"",
		}, "\n")
		if err := os.WriteFile(envPath, []byte(envContent), 0o600); err != nil {
			t.Fatalf("write env file: %v", err)
		}

		command := exec.Command(binaryPath, "--env", envPath, "--host", "127.0.0.1")
		command.Dir = runtimeDir
		command.Env = []string{}
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = &output
		if err := command.Start(); err != nil {
			t.Fatalf("start CLI: %v", err)
		}
		waitResult := make(chan error, 1)
		go func() {
			waitResult <- command.Wait()
		}()

		client := &http.Client{
			Transport: &http.Transport{Proxy: nil},
			Timeout:   250 * time.Millisecond,
		}
		healthURL := fmt.Sprintf("http://127.0.0.1:%s/healthz", port)
		deadline := time.NewTimer(10 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		for {
			response, err := client.Get(healthURL)
			if err == nil {
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					_ = command.Process.Kill()
					<-waitResult
					return
				}
			}

			select {
			case err := <-waitResult:
				t.Fatalf("CLI exited before binding command-line host: %v\n%s", err, output.String())
			case <-deadline.C:
				_ = command.Process.Kill()
				<-waitResult
				t.Fatalf("timeout waiting for %s\n%s", healthURL, output.String())
			case <-ticker.C:
			}
		}
	})
}

// Start the real built binary with an isolated file database and loopback-only
// unavailable dependencies. Health alone is insufficient: initialization must
// reach the requested public startup phase before inspecting persisted results.
func runCLIToPhase(t *testing.T, binaryPath, runtimeDir string, explicit bool, phase string) string {
	t.Helper()
	port := reserveLoopbackPort(t)
	args := []string{}
	if explicit {
		args = append(args, "--upgrade-stored-pricing")
	}
	command := exec.Command(binaryPath, args...)
	command.Dir = runtimeDir
	command.Env = []string{
		"APP_HOST=127.0.0.1", "APP_PORT=" + port, "WORK_DIR=./data",
		"CPA_BASE_URL=http://127.0.0.1:1", "CPA_MANAGEMENT_KEY=fixture-only",
		"REDIS_QUEUE_ADDR=127.0.0.1:1", "AUTH_ENABLED=false",
		"LOG_FILE_ENABLED=false", "BACKUP_ENABLED=false", "TZ=UTC",
	}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waitResult := make(chan error, 1)
	go func() { waitResult <- command.Wait() }()
	stopped := false
	stop := func() {
		if !stopped {
			_ = command.Process.Kill()
			<-waitResult
			stopped = true
		}
	}
	defer stop()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 250 * time.Millisecond}
	defer client.CloseIdleConnections()
	statusURL := fmt.Sprintf("http://127.0.0.1:%s/api/v1/startup/status", port)
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := client.Get(statusURL)
		if err == nil {
			var state struct {
				Phase string `json:"phase"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&state)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil {
				if state.Phase == phase {
					stop()
					return output.String()
				}
				if state.Phase == "failed" {
					stop()
					t.Fatalf("CLI startup failed instead of %s: %s", phase, output.String())
				}
			}
		}
		select {
		case err := <-waitResult:
			stopped = true
			t.Fatalf("CLI exited before phase %s: %v\n%s", phase, err, output.String())
		case <-deadline.C:
			stop()
			t.Fatalf("timeout waiting for CLI phase %s\n%s", phase, output.String())
		case <-ticker.C:
		}
	}
}

func closeCLIPools(t *testing.T, pools ...*gorm.DB) {
	t.Helper()
	seen := make(map[*gorm.DB]bool)
	for _, db := range pools {
		if !seen[db] {
			seen[db] = true
			pool, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			if err := pool.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func seedCLILegacyPricing(t *testing.T, cfg config.Config) {
	t.Helper()
	db, err := repository.OpenDatabase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer closeCLIPools(t, db)
	if _, err := repository.UpsertModelPriceSetting(db, repodto.ModelPriceSettingInput{Model: "cli-old-model", PromptPricePer1M: 2}); err != nil {
		t.Fatal(err)
	}
	event := entities.UsageEvent{EventKey: "cli-old-usage", APIGroupKey: "fixture-key", Model: "cli-old-model", Timestamp: time.Date(2026, 9, 23, 10, 15, 0, 0, time.UTC), InputTokens: 100, TotalTokens: 100}
	if _, _, err := repository.InsertUsageEvents(db, []entities.UsageEvent{event}); err != nil {
		t.Fatal(err)
	}
	if err := repository.AggregateUsageOverviewStats(context.Background(), db, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"usage_events", "usage_events_archive"} {
		for _, column := range []string{"cost_usd", "cost_available"} {
			if err := db.Exec("ALTER TABLE " + table + " DROP COLUMN " + column).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, table := range []string{"usage_overview_hourly_stats", "usage_overview_daily_stats"} {
		for _, column := range []string{"cost_usd", "unavailable_cost_count"} {
			if err := db.Exec("ALTER TABLE " + table + " DROP COLUMN " + column).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, statement := range []string{
		"ALTER TABLE model_price_settings DROP COLUMN branches_json",
		"DROP TABLE pricing_state", "DROP TABLE pricing_migration_state",
		"DELETE FROM schema_migrations WHERE version = '20261009_pricing_storage_structure'",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func buildCLI(t *testing.T) string {
	t.Helper()
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	binaryPath := filepath.Join(t.TempDir(), "cpa-usage-keeper")
	if runtime.GOOS == "windows" {
		binaryPath += ".exe"
	}
	build := exec.Command(
		"go", "build",
		"-ldflags=-X cpa-usage-keeper/internal/version.Version=v9.8.7",
		"-o", binaryPath,
		"./cmd/server/main.go",
	)
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	return binaryPath
}

func reserveLoopbackPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release loopback port: %v", err)
	}
	return strconv.Itoa(port)
}
