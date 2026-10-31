package test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	keeperapp "cpa-usage-keeper/internal/app"
)

type rankingRunnerStub struct {
	started chan struct{}
	stopped chan struct{}
}

func (s *rankingRunnerStub) Run(ctx context.Context) error {
	close(s.started)
	<-ctx.Done()
	if s.stopped != nil {
		close(s.stopped)
	}
	return nil
}

func TestAppConstructsAndStartsRankingRunner(t *testing.T) {
	// Initialize is the upstream's explicit readiness boundary. Reusing that
	// prepared application must not rerun migration or lose the fork's runners.
	cfg := pricingRuntimeConfig(t)
	application := newInitializedApp(t, cfg)
	if application.Ranking == nil || application.LocalRanking == nil {
		t.Fatal("expected App to construct community and local ranking runners")
	}
	started, stopped := make(chan struct{}), make(chan struct{})
	application.Ranking = &rankingRunnerStub{started: started, stopped: stopped}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- application.RunContext(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("ranking runtime stop: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("ranking runtime did not stop")
		}
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("expected App.RunContext to start ranking runner after listening")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("ranking runner did not stop with application context")
	}
}

func TestRankingRunnerDoesNotStartBeforeListener(t *testing.T) {
	cfg := databasePoolTestConfig(filepath.Join(t.TempDir(), "ranking-listener.db"))
	cfg.AppPort = "invalid-port"
	application, err := keeperapp.NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	started := make(chan struct{})
	application.Ranking = &rankingRunnerStub{started: started}
	if err := application.Run(); err == nil {
		t.Fatal("expected invalid port error")
	}
	select {
	case <-started:
		t.Fatal("ranking runner started before a successful listener")
	default:
	}
}
