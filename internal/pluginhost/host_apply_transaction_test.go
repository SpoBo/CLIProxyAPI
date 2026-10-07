package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestHostApplyConfigRequiredSchedulerHotFailuresAreTransactional(t *testing.T) {
	tests := []struct {
		name            string
		prepareFailure  func(*testing.T, string) string
		replacement     pluginClient
		wantOpenCalls   int
		wantReplacement bool
	}{
		{
			name: "discovery failure",
			prepareFailure: func(t *testing.T, _ string) string {
				t.Helper()
				path := filepath.Join(t.TempDir(), "not-a-directory")
				if errWrite := os.WriteFile(path, []byte("invalid discovery root"), 0o600); errWrite != nil {
					t.Fatalf("WriteFile() error = %v", errWrite)
				}
				return path
			},
			wantOpenCalls: 1,
		},
		{
			name:            "load init failure",
			replacement:     nil,
			wantOpenCalls:   2,
			wantReplacement: true,
		},
		{
			name: "registration failure",
			replacement: &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
				if method != pluginabi.MethodPluginRegister {
					return nil, fmt.Errorf("unexpected replacement method %s", method)
				}
				return nil, fmt.Errorf("replacement registration failed")
			}},
			wantOpenCalls:   2,
			wantReplacement: true,
		},
		{
			name:            "scheduler capability missing",
			replacement:     registrationClient(validTestPlugin("quota-policy")),
			wantOpenCalls:   2,
			wantReplacement: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var oldSchedulerCalls int
			oldPlugin := validTestPlugin("quota-policy")
			oldPlugin.Capabilities.Scheduler = schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				oldSchedulerCalls++
				return pluginapi.SchedulerPickResponse{
					Handled:      true,
					Reject:       true,
					RejectCode:   "prior_policy",
					RejectReason: "prior required scheduler remained active",
				}, nil
			})
			oldPlugin.Capabilities.ThinkingApplier = testThinkingCapability{provider: "required-prior-provider"}
			oldClient := registrationClient(oldPlugin)
			loader := &sequencePluginLoader{clients: []pluginClient{oldClient}}
			if tt.wantReplacement {
				loader.clients = append(loader.clients, tt.replacement)
			}
			h := NewForTest(loader)
			t.Cleanup(h.ShutdownAll)
			pluginsDir, paths := makeVersionedPluginDir(t, "quota-policy", "1.0.0")
			initialCfg := requiredVersionedPluginHostConfig(t, pluginsDir, "1.0.0")
			if errApply := h.ApplyConfig(context.Background(), initialCfg); errApply != nil {
				t.Fatalf("initial ApplyConfig() error = %v", errApply)
			}
			if thinking.GetProviderApplier("required-prior-provider") == nil {
				t.Fatal("initial required scheduler thinking provider was not registered")
			}
			activeSnapshot := h.Snapshot()
			h.mu.Lock()
			activeLoaded := h.loaded["quota-policy"]
			h.managementRoutes["GET /prior"] = managementRouteRecord{pluginID: "quota-policy"}
			h.resourceRoutes["GET /prior-resource"] = resourceRouteRecord{pluginID: "quota-policy"}
			h.modelProviders["quota-policy"] = "prior-provider"
			h.modelRegistrations["quota-policy"] = pluginModelRegistration{pluginID: "quota-policy", provider: "prior-provider"}
			h.providerModels["prior-provider"] = []*registryModelInfo{{ID: "prior-model"}}
			h.mu.Unlock()

			manager := transactionalTestManager(t, h)
			assertPriorRequiredPolicy(t, manager, &oldSchedulerCalls, 1)

			failedDir := pluginsDir
			if tt.prepareFailure != nil {
				failedDir = tt.prepareFailure(t, pluginsDir)
			} else {
				paths["2.0.0"] = writeVersionedPluginFile(t, pluginsDir, "quota-policy", "2.0.0")
			}
			failedCfg := requiredVersionedPluginHostConfig(t, failedDir, "2.0.0")
			if errApply := h.ApplyConfig(context.Background(), failedCfg); errApply == nil {
				t.Fatal("hot ApplyConfig() error = nil, want transactional required-scheduler failure")
			}

			if h.Snapshot() != activeSnapshot {
				t.Fatal("failed hot apply replaced the active snapshot")
			}
			if !h.pluginIdentityCurrent("quota-policy", paths["1.0.0"], "1.0.0") {
				t.Fatal("failed hot apply replaced the active plugin identity")
			}
			h.mu.Lock()
			if h.loaded["quota-policy"] != activeLoaded {
				t.Fatal("failed hot apply replaced the active loaded plugin")
			}
			if len(h.retired["quota-policy"]) != 0 {
				t.Fatalf("failed hot apply retired %d plugins, want 0", len(h.retired["quota-policy"]))
			}
			if len(h.managementRoutes) != 1 || len(h.resourceRoutes) != 1 ||
				h.modelProviders["quota-policy"] != "prior-provider" ||
				h.modelRegistrations["quota-policy"].provider != "prior-provider" ||
				len(h.providerModels["prior-provider"]) != 1 {
				t.Fatalf("failed hot apply mutated active routes/providers: management=%#v resource=%#v modelProviders=%#v modelRegistrations=%#v providerModels=%#v", h.managementRoutes, h.resourceRoutes, h.modelProviders, h.modelRegistrations, h.providerModels)
			}
			h.mu.Unlock()
			if loader.calls != tt.wantOpenCalls {
				t.Fatalf("loader calls = %d, want %d", loader.calls, tt.wantOpenCalls)
			}
			if thinking.GetProviderApplier("required-prior-provider") == nil {
				t.Fatal("failed hot apply removed the prior thinking provider")
			}
			assertPriorRequiredPolicy(t, manager, &oldSchedulerCalls, 2)
		})
	}
}

func TestHostApplyConfigRequiredSchedulerReconfigureFailurePreservesPriorPolicy(t *testing.T) {
	var oldSchedulerCalls int
	oldPlugin := validTestPlugin("quota-policy")
	oldPlugin.Capabilities.Scheduler = schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
		oldSchedulerCalls++
		return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "prior_policy", RejectReason: "prior configuration remained active"}, nil
	})
	plugin := &testPlugin{registerResult: oldPlugin, panicOnReload: true}
	loader := newTestSymbolLoader()
	loader.lookups["quota-policy"] = newTestSymbolLookup(plugin)
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	cfg := &config.Config{Plugins: config.PluginsConfig{
		Enabled:           true,
		Dir:               makePluginDir(t, "quota-policy"),
		RequiredScheduler: "quota-policy",
		Configs:           enabledPluginConfigs("quota-policy"),
	}}
	if errApply := h.ApplyConfig(context.Background(), cfg); errApply != nil {
		t.Fatalf("initial ApplyConfig() error = %v", errApply)
	}
	activeSnapshot := h.Snapshot()
	manager := transactionalTestManager(t, h)
	assertPriorRequiredPolicy(t, manager, &oldSchedulerCalls, 1)

	if errApply := h.ApplyConfig(context.Background(), cfg); errApply == nil {
		t.Fatal("reconfigure ApplyConfig() error = nil, want configuration failure")
	}
	if h.Snapshot() != activeSnapshot {
		t.Fatal("failed reconfigure replaced the active snapshot")
	}
	if h.isPluginFused("quota-policy") {
		t.Fatal("failed candidate reconfigure fused the prior required scheduler")
	}
	assertPriorRequiredPolicy(t, manager, &oldSchedulerCalls, 2)
}

func TestHostApplyConfigRequiredSchedulerHotSuccessCommitsCandidate(t *testing.T) {
	var oldSchedulerCalls int
	oldPlugin := validTestPlugin("quota-policy")
	oldPlugin.Capabilities.Scheduler = schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
		oldSchedulerCalls++
		return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "old_policy", RejectReason: "old policy"}, nil
	})
	newPlugin := validTestPlugin("quota-policy")
	newPlugin.Capabilities.Scheduler = schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
		return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "native-fallback"}, nil
	})
	loader := &sequencePluginLoader{clients: []pluginClient{registrationClient(oldPlugin), registrationClient(newPlugin)}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	pluginsDir, paths := makeVersionedPluginDir(t, "quota-policy", "1.0.0")
	if errApply := h.ApplyConfig(context.Background(), requiredVersionedPluginHostConfig(t, pluginsDir, "1.0.0")); errApply != nil {
		t.Fatalf("initial ApplyConfig() error = %v", errApply)
	}
	activeSnapshot := h.Snapshot()
	manager := transactionalTestManager(t, h)
	assertPriorRequiredPolicy(t, manager, &oldSchedulerCalls, 1)

	paths["2.0.0"] = writeVersionedPluginFile(t, pluginsDir, "quota-policy", "2.0.0")
	if errApply := h.ApplyConfig(context.Background(), requiredVersionedPluginHostConfig(t, pluginsDir, "2.0.0")); errApply != nil {
		t.Fatalf("hot ApplyConfig() error = %v", errApply)
	}
	if h.Snapshot() == activeSnapshot {
		t.Fatal("successful hot apply did not publish the candidate snapshot")
	}
	if !h.pluginIdentityCurrent("quota-policy", paths["2.0.0"], "2.0.0") {
		t.Fatal("successful hot apply did not publish the candidate identity")
	}
	h.mu.Lock()
	retiredCount := len(h.retired["quota-policy"])
	h.mu.Unlock()
	if retiredCount != 1 {
		t.Fatalf("retired plugin count = %d, want 1 after commit", retiredCount)
	}
	selected, errSelect := manager.SelectAuth(context.Background(), "transaction-provider", "", cliproxyexecutor.Options{})
	if errSelect != nil || selected == nil || selected.ID != "native-fallback" {
		t.Fatalf("selection after successful hot apply = %#v, %v; want candidate scheduler decision", selected, errSelect)
	}
	if oldSchedulerCalls != 1 {
		t.Fatalf("old scheduler calls after commit = %d, want 1", oldSchedulerCalls)
	}
}

func TestHostApplyConfigOptionalDiscoveryFailureRestoresLegacyClear(t *testing.T) {
	plugin := validTestPlugin("optional-policy")
	plugin.Capabilities.Scheduler = schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
		return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "optional_policy", RejectReason: "optional policy active"}, nil
	})
	plugin.Capabilities.ThinkingApplier = testThinkingCapability{provider: "optional-clear-provider"}
	loader := newTestSymbolLoader()
	loader.lookups["optional-policy"] = newTestSymbolLookup(&testPlugin{registerResult: plugin})
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	initialCfg := &config.Config{Plugins: config.PluginsConfig{
		Enabled: true,
		Dir:     makePluginDir(t, "optional-policy"),
		Configs: enabledPluginConfigs("optional-policy"),
	}}
	if errApply := h.ApplyConfig(context.Background(), initialCfg); errApply != nil {
		t.Fatalf("initial ApplyConfig() error = %v", errApply)
	}
	if thinking.GetProviderApplier("optional-clear-provider") == nil {
		t.Fatal("optional thinking provider was not registered")
	}
	manager := transactionalTestManager(t, h)
	if selected, errSelect := manager.SelectAuth(context.Background(), "transaction-provider", "", cliproxyexecutor.Options{}); errSelect == nil || selected != nil {
		t.Fatalf("initial optional scheduler selection = %#v, %v; want policy rejection", selected, errSelect)
	}

	h.mu.Lock()
	h.managementRoutes["GET /optional"] = managementRouteRecord{pluginID: "optional-policy"}
	h.resourceRoutes["GET /optional-resource"] = resourceRouteRecord{pluginID: "optional-policy"}
	h.mu.Unlock()
	nonDirectory := filepath.Join(t.TempDir(), "not-a-directory")
	if errWrite := os.WriteFile(nonDirectory, []byte("invalid discovery root"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	failedCfg := &config.Config{Plugins: config.PluginsConfig{Enabled: true, Dir: nonDirectory}}
	if errApply := h.ApplyConfig(context.Background(), failedCfg); errApply != nil {
		t.Fatalf("optional discovery ApplyConfig() error = %v, want nil legacy behavior", errApply)
	}

	snap := h.Snapshot()
	if snap.enabled || len(snap.records) != 0 || snap.requiredScheduler != "" {
		t.Fatalf("Snapshot() = %#v, want empty disabled snapshot", snap)
	}
	h.mu.Lock()
	if h.runtimeConfig != failedCfg || len(h.activePluginPaths) != 0 || len(h.activePluginVersions) != 0 ||
		len(h.pluginFileVersions) != 0 || len(h.managementRoutes) != 0 || len(h.resourceRoutes) != 0 {
		t.Fatalf("optional discovery failure did not clear legacy state: runtime=%p want=%p paths=%#v versions=%#v files=%#v management=%#v resources=%#v", h.runtimeConfig, failedCfg, h.activePluginPaths, h.activePluginVersions, h.pluginFileVersions, h.managementRoutes, h.resourceRoutes)
	}
	h.mu.Unlock()
	if thinking.GetProviderApplier("optional-clear-provider") != nil {
		t.Fatal("optional discovery failure retained thinking provider")
	}
	selected, errSelect := manager.SelectAuth(context.Background(), "transaction-provider", "", cliproxyexecutor.Options{})
	if errSelect != nil || selected == nil || selected.ID != "native-fallback" {
		t.Fatalf("selection after optional discovery failure = %#v, %v; want native fallback", selected, errSelect)
	}
}

func registrationClient(plugin pluginapi.Plugin) *lifecycleTestClient {
	return &lifecycleTestClient{call: func(ctx context.Context, method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
			return lifecycleRegistrationResult(plugin)
		case pluginabi.MethodPluginQuiesce:
			return marshalRPCResult(rpcEmptyResponse{})
		case pluginabi.MethodThinkingIdentifier:
			if plugin.Capabilities.ThinkingApplier == nil {
				return nil, fmt.Errorf("thinking applier unavailable")
			}
			return marshalRPCResult(rpcIdentifierResponse{Identifier: plugin.Capabilities.ThinkingApplier.Identifier()})
		case pluginabi.MethodSchedulerPick:
			if plugin.Capabilities.Scheduler == nil {
				return nil, fmt.Errorf("scheduler unavailable")
			}
			var req pluginapi.SchedulerPickRequest
			if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			resp, errPick := plugin.Capabilities.Scheduler.Pick(ctx, req)
			if errPick != nil {
				return nil, errPick
			}
			return marshalRPCResult(resp)
		default:
			return nil, fmt.Errorf("unexpected plugin method %s", method)
		}
	}}
}

func requiredVersionedPluginHostConfig(t *testing.T, pluginsDir, version string) *config.Config {
	t.Helper()
	return &config.Config{Plugins: config.PluginsConfig{
		Enabled:           true,
		Dir:               pluginsDir,
		RequiredScheduler: "quota-policy",
		Configs: map[string]config.PluginInstanceConfig{
			"quota-policy": enabledPluginConfigWithStoreVersion(t, version),
		},
	}}
}

func transactionalTestManager(t *testing.T, host *Host) *coreauth.Manager {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(transactionalTestExecutor{})
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: "native-fallback", Provider: "transaction-provider", Status: coreauth.StatusActive}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	manager.SetPluginScheduler(host)
	return manager
}

func assertPriorRequiredPolicy(t *testing.T, manager *coreauth.Manager, calls *int, wantCalls int) {
	t.Helper()
	selected, errSelect := manager.SelectAuth(context.Background(), "transaction-provider", "", cliproxyexecutor.Options{})
	if errSelect == nil || selected != nil {
		t.Fatalf("required scheduler selection = %#v, %v; want prior policy rejection", selected, errSelect)
	}
	if *calls != wantCalls {
		t.Fatalf("prior scheduler calls = %d, want %d", *calls, wantCalls)
	}
}

type transactionalTestExecutor struct{}

func (transactionalTestExecutor) Identifier() string { return "transaction-provider" }
func (transactionalTestExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (transactionalTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (transactionalTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}
func (transactionalTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (transactionalTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
