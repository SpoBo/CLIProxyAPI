package pluginhost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestHostTeardownWaitsForCanceledLoadCleanupAndAllowsImmediateReuse(t *testing.T) {
	first := &lateLoadClient{registration: validTestPlugin("alpha")}
	second := &lateLoadClient{registration: validTestPlugin("alpha")}
	loader := &lateLoadPluginLoader{
		first:         first,
		second:        second,
		firstStarted:  make(chan struct{}),
		firstRelease:  make(chan struct{}),
		secondStarted: make(chan struct{}),
	}
	host := NewForTest(loader)
	cfg := pluginLifecycleTestConfig(t, "alpha")

	ctx, cancel := context.WithCancel(context.Background())
	applyDone := make(chan error, 1)
	go func() { applyDone <- host.ApplyConfig(ctx, cfg) }()
	waitForHostTestSignal(t, loader.firstStarted, "blocked plugin load")
	cancel()
	if errApply := waitForHostTestError(t, applyDone, "canceled plugin apply"); !errors.Is(errApply, context.Canceled) {
		t.Fatalf("ApplyConfig() error = %v, want context.Canceled", errApply)
	}

	teardownDone := make(chan error, 1)
	go func() { teardownDone <- host.TeardownContext(context.Background()) }()
	select {
	case errTeardown := <-teardownDone:
		t.Fatalf("TeardownContext() returned before canceled load cleanup: %v", errTeardown)
	case <-time.After(50 * time.Millisecond):
	}

	close(loader.firstRelease)
	if errTeardown := waitForHostTestError(t, teardownDone, "teardown after load cleanup"); errTeardown != nil {
		t.Fatalf("TeardownContext() error = %v", errTeardown)
	}
	if got := first.shutdown.Load(); got != 1 {
		t.Fatalf("canceled client shutdown calls = %d, want 1", got)
	}
	if host.PluginBusy("alpha") {
		t.Fatal("host retained canceled load after teardown")
	}

	if errApply := host.ApplyConfig(context.Background(), cfg); errApply != nil {
		t.Fatalf("ApplyConfig() after teardown error = %v", errApply)
	}
	waitForHostTestSignal(t, loader.secondStarted, "fresh plugin load")
	if !host.PluginLoaded("alpha") {
		t.Fatal("host was not immediately reusable after teardown")
	}
	if errTeardown := host.TeardownContext(context.Background()); errTeardown != nil {
		t.Fatalf("final TeardownContext() error = %v", errTeardown)
	}
	if got := second.shutdown.Load(); got != 1 {
		t.Fatalf("fresh client shutdown calls = %d, want 1", got)
	}
}

func TestHostTeardownDeadlineDetachesBlockedLoadAndCleanupFinishes(t *testing.T) {
	first := &lateLoadClient{registration: validTestPlugin("alpha")}
	second := &lateLoadClient{registration: validTestPlugin("alpha")}
	loader := &lateLoadPluginLoader{
		first:         first,
		second:        second,
		firstStarted:  make(chan struct{}),
		firstRelease:  make(chan struct{}),
		secondStarted: make(chan struct{}),
	}
	host := NewForTest(loader)
	cfg := pluginLifecycleTestConfig(t, "alpha")

	applyCtx, cancelApply := context.WithCancel(context.Background())
	applyDone := make(chan error, 1)
	go func() { applyDone <- host.ApplyConfig(applyCtx, cfg) }()
	waitForHostTestSignal(t, loader.firstStarted, "blocked plugin load")
	cancelApply()
	if errApply := waitForHostTestError(t, applyDone, "canceled plugin apply"); !errors.Is(errApply, context.Canceled) {
		t.Fatalf("ApplyConfig() error = %v, want context.Canceled", errApply)
	}

	teardownCtx, cancelTeardown := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelTeardown()
	started := time.Now()
	errTeardown := host.TeardownContext(teardownCtx)
	if !errors.Is(errTeardown, context.DeadlineExceeded) {
		t.Fatalf("TeardownContext() error = %v, want context.DeadlineExceeded", errTeardown)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("TeardownContext() took %v, want prompt deadline return", elapsed)
	}

	secondApplyDone := make(chan error, 1)
	go func() { secondApplyDone <- host.ApplyConfig(context.Background(), cfg) }()
	waitForHostTestSignal(t, loader.secondStarted, "fresh load after timed-out teardown")
	if errApply := waitForHostTestError(t, secondApplyDone, "fresh apply after timed-out teardown"); errApply != nil {
		t.Fatalf("ApplyConfig() after timed-out teardown error = %v", errApply)
	}

	close(loader.firstRelease)
	deadline := time.Now().Add(time.Second)
	for first.shutdown.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := first.shutdown.Load(); got != 1 {
		t.Fatalf("detached canceled client shutdown calls = %d, want 1", got)
	}
	host.mu.Lock()
	loadingCount := len(host.loading)
	host.mu.Unlock()
	if loadingCount != 0 {
		t.Fatalf("outstanding load entries = %d, want 0", loadingCount)
	}
	if errFinal := host.TeardownContext(context.Background()); errFinal != nil {
		t.Fatalf("final TeardownContext() error = %v", errFinal)
	}
	if got := second.shutdown.Load(); got != 1 {
		t.Fatalf("fresh client shutdown calls = %d, want 1", got)
	}
}

func pluginLifecycleTestConfig(t *testing.T, id string) *config.Config {
	t.Helper()
	return &config.Config{Plugins: config.PluginsConfig{
		Enabled: true,
		Dir:     makePluginDir(t, id),
		Configs: enabledPluginConfigs(id),
	}}
}

func waitForHostTestError(t *testing.T, ch <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}
