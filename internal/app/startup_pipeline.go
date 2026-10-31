package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"cpa-usage-keeper/internal/api"
	"cpa-usage-keeper/internal/cpa"
	"cpa-usage-keeper/internal/poller"
	"cpa-usage-keeper/internal/repository"
	"cpa-usage-keeper/internal/repository/storedpricing"
	"github.com/sirupsen/logrus"
)

// Initialize prepares current services without replaying historical stored fees.
// RunContext starts the durable inbox before ordinary schema initialization.
func (a *App) Initialize(ctx context.Context) error {
	return a.initialize(ctx, false)
}

// InitializeStoredPricingUpgrade explicitly opts into the protected legacy
// stored-fee upgrade. It must precede any initialization on this App instance.
// It does not change the default startup or select stored prices for current APIs.
func (a *App) InitializeStoredPricingUpgrade(ctx context.Context) error {
	return a.initialize(ctx, true)
}

func (a *App) initialize(ctx context.Context, upgradeStored bool) error {
	if a == nil || a.Config == nil || a.StartupShell == nil {
		return fmt.Errorf("startup shell is not initialized")
	}
	if a.DB != nil || a.Router != nil {
		return fmt.Errorf("application initialization already started")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if a.backgroundContext == nil {
		a.startBackgroundContext(ctx)
	}
	db, reader, err := repository.OpenUnmigratedDatabasePools(*a.Config)
	if err != nil {
		return fmt.Errorf("open unmigrated database: %w", err)
	}
	a.DB, a.ReadDB = db, reader
	state, err := repository.BootstrapPricingInitialization(ctx, db)
	if err != nil {
		return fmt.Errorf("fix pricing initialization identity: %w", err)
	}
	if err := repository.EnsurePricingBootstrapInbox(ctx, db); err != nil {
		return fmt.Errorf("prepare durable usage inbox: %w", err)
	}
	ingest, bridge := newPricingBootstrapIngestRunner(*a.Config, db)
	a.ingestBridge = bridge
	a.RedisIngest = ingest
	if a.serveStartup {
		a.ingestStarted = true
		a.startBackgroundTask(func() {
			if runErr := ingest.Run(a.backgroundContext); runErr != nil {
				logrus.WithError(runErr).Error("pricing bootstrap ingest stopped")
			}
		})
	}
	if err := a.StartupShell.Publish(api.StartupPhaseMigrating, nil); err != nil {
		return err
	}
	switch state.InitKind {
	case repository.PricingInitKindFresh:
		if err := repository.InitializeFreshPricingDatabase(ctx, db); err != nil {
			return fmt.Errorf("initialize fresh pricing database: %w", err)
		}
	case repository.PricingInitKindLegacy:
		// Do not replay stored fees or clear fork aggregates on ordinary startup.
		// Historical stored valuation requires an explicit, independently verified operation.
		if upgradeStored && !state.DataComplete {
			if err := verifyStoredPricingUpgradePreconditions(ctx, db, state); err != nil {
				return err
			}
			baseline, err := repository.MigrateLegacyPricingEvents(ctx, db, reader, a.Config.BackupDir, time.Now())
			if err != nil {
				a.logLegacyPricingRollback(ctx)
				return fmt.Errorf("migrate legacy pricing events: %w", err)
			}
			if err := repository.CompleteLegacyPricingData(ctx, db, reader, baseline); err != nil {
				a.logLegacyPricingRollback(ctx)
				return fmt.Errorf("complete legacy pricing data: %w", err)
			}
		}
	default:
		return fmt.Errorf("unknown pricing initialization identity")
	}
	if upgradeStored {
		if err := repository.VerifyPricingStartupDataComplete(ctx, db); err != nil {
			return fmt.Errorf("verify explicit stored pricing upgrade: %w", err)
		}
	}
	// Run pending versioned migrations without implicitly replaying historical stored fees.
	if err := repository.RunDatabaseMigrationsWithBackup(ctx, db, reader, a.Config.BackupDir); err != nil {
		return fmt.Errorf("run remaining database migrations: %w", err)
	}
	// Fresh schema marks historical migrations applied without executing their bodies.
	if err := storedpricing.CreateSchema(db.WithContext(ctx)); err != nil {
		return fmt.Errorf("prepare stored pricing structure: %w", err)
	}
	if err := repository.SeedOfficialModelPrices(db.WithContext(ctx)); err != nil {
		return fmt.Errorf("seed fork official prices: %w", err)
	}
	ready, err := buildReadyApp(*a.Config, db, reader, a.LogCloser, a.backgroundContext, ingest)
	if err != nil {
		return fmt.Errorf("prepare business services: %w", err)
	}
	a.installReadyApp(ready)
	if err := bridge.ActivateNormalWriter(poller.NewRedisInboxWriter(db), a.MetadataSync); err != nil {
		return fmt.Errorf("activate normal usage ingest: %w", err)
	}
	return nil
}

// installReadyApp 只移交已构造的业务字段；HTTP 外壳、生命周期和唯一接收实例仍属原 App。
func (a *App) installReadyApp(ready *App) {
	a.Poller, a.RedisIngest, a.RedisProcess = ready.Poller, ready.RedisIngest, ready.RedisProcess
	a.CPAErrors, a.UsageAggregation = ready.CPAErrors, ready.UsageAggregation
	a.Ranking, a.LocalRanking = ready.Ranking, ready.LocalRanking
	a.PluginAPIKeys = ready.PluginAPIKeys
	a.Maintenance = ready.Maintenance
	a.MetadataSync, a.QuotaService, a.QuotaAutoRefresh = ready.MetadataSync, ready.QuotaService, ready.QuotaAutoRefresh
	a.BackupMaintenance, a.RecentUsageCache = ready.BackupMaintenance, ready.RecentUsageCache
	a.CostReadGate, a.PricingCatalog, a.PricingService = ready.CostReadGate, ready.PricingCatalog, ready.PricingService
	a.StoredPricing = ready.StoredPricing
	a.Router = ready.Router
}

// RunContext 先监听独立 HTTP 外壳，再运行同一 Initialize；失败仍提供公开状态与静态页面直到进程停止。
func (a *App) RunContext(ctx context.Context) error {
	return a.runContext(ctx, false)
}

// RunStoredPricingUpgradeContext is the explicit operator maintenance startup:
// keep the real HTTP shell and durable inbox alive while upgrading stored fees.
// No persisted configuration can enable this operation on an ordinary restart.
func (a *App) RunStoredPricingUpgradeContext(ctx context.Context) error {
	return a.runContext(ctx, true)
}

func (a *App) runContext(ctx context.Context, upgradeStored bool) error {
	if a == nil || a.Config == nil || a.StartupShell == nil {
		return fmt.Errorf("application startup shell is not initialized")
	}
	if upgradeStored && (a.DB != nil || a.Router != nil) {
		return fmt.Errorf("stored pricing upgrade requires an uninitialized application")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	listener, err := net.Listen("tcp", a.Config.ListenAddress())
	if err != nil {
		return fmt.Errorf("listen for startup shell: %w", err)
	}
	server := NewHTTPServer(*a.Config, a.StartupShell)
	if a.Config.TLSEnabled {
		certificate, err := tls.LoadX509KeyPair(a.Config.TLSCertFile, a.Config.TLSKeyFile)
		if err != nil {
			_ = listener.Close()
			return fmt.Errorf("load startup TLS certificate: %w", err)
		}
		server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{certificate}}
	}
	acceptReady := &startupAcceptReadyListener{Listener: listener, ready: make(chan struct{})}
	serverDone := make(chan error, 1)
	go func() {
		if a.Config.TLSEnabled {
			serverDone <- server.ServeTLS(acceptReady, "", "")
		} else {
			serverDone <- server.Serve(acceptReady)
		}
	}()
	select {
	case <-acceptReady.ready:
	case err := <-serverDone:
		return fmt.Errorf("serve startup shell: %w", err)
	}
	defer a.stopBackgroundTasks()
	a.serveStartup = true
	var initializeErr error
	// Explicit Initialize callers already crossed the readiness boundary. Keep
	// their prepared services and runners; never replay initialization on Run.
	if a.DB == nil || a.Router == nil {
		if upgradeStored {
			initializeErr = a.InitializeStoredPricingUpgrade(ctx)
		} else {
			initializeErr = a.Initialize(ctx)
		}
	}
	if err := initializeErr; err != nil {
		logrus.WithError(err).Error("pricing startup failed")
		if publishErr := a.StartupShell.Publish(api.StartupPhaseFailed, nil); publishErr != nil {
			logrus.WithError(publishErr).Error("publish pricing startup failure")
		}
	} else {
		a.startReadyBackgroundTasks(a.backgroundContext)
		if err := a.StartupShell.ActivateReady(a.Router); err != nil {
			logrus.WithError(err).Error("activate business router")
			_ = a.StartupShell.Publish(api.StartupPhaseFailed, nil)
		} else {
			// 就绪后只检查一次，版本提示不参与迁移、收数或本地历史读取的门禁。
			a.startBackgroundTask(func() {
				client := cpa.NewClient(a.Config.CPABaseURL, a.Config.CPAManagementKey, a.Config.RequestTimeout, a.Config.TLSSkipVerify)
				if _, err := client.CheckMinimumVersion(a.backgroundContext); err != nil && a.backgroundContext.Err() == nil {
					logrus.Warn(err)
				}
			})
		}
	}
	select {
	case err := <-serverDone:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown startup HTTP shell: %w", err)
		}
		return nil
	}
}

// startupAcceptReadyListener 只确认 HTTP Serve 已进入接收循环，避免监听或 TLS 失败后仍改动旧库。
type startupAcceptReadyListener struct {
	net.Listener
	ready chan struct{}
	once  sync.Once
}

func (l *startupAcceptReadyListener) Accept() (net.Conn, error) {
	l.once.Do(func() { close(l.ready) })
	return l.Listener.Accept()
}
