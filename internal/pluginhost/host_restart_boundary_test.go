package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdkpluginstore "github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginstore"
)

func TestHostApplyConfigActiveRequiredSchedulerEqualBoundaryRefreshesOnlyRuntimeConfig(t *testing.T) {
	pluginsDir := makePluginDir(t, "quota-policy")
	initialCfg := requiredBoundaryConfig(t, pluginsDir, "")
	equivalentCfg := requiredBoundaryConfig(t, pluginsDir, `
    priority: 7
    enabled: true
    tiers:
      - name: primary
        accounts: [first, second]
`)
	equivalentCfg.ProxyURL = " http://new-proxy.local "
	equivalentCfg.AuthDir = " /new/auth/dir "
	equivalentCfg.ForceModelPrefix = true
	equivalentCfg.OAuthModelAlias = map[string][]config.OAuthModelAlias{
		"Runtime-Provider": {{Name: "upstream-model", Alias: "alias-model"}},
	}
	equivalentCfg.OAuthExcludedModels = map[string][]string{
		"Runtime-Provider": {"hidden-model"},
	}
	var registerCalls atomic.Int32
	var reconfigureCalls atomic.Int32
	var operationHost pluginapi.HostConfigSummary
	plugin := validTestPlugin("quota-policy")
	plugin.Capabilities.Scheduler = schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
		return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "policy", RejectReason: "required policy"}, nil
	})
	plugin.Capabilities.AuthProvider = fakeAuthProvider{
		identifier: "runtime-provider",
	}
	client := &lifecycleTestClient{call: func(_ context.Context, method string, raw []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			registerCalls.Add(1)
			return lifecycleRegistrationResult(plugin)
		case pluginabi.MethodPluginReconfigure:
			reconfigureCalls.Add(1)
			return nil, fmt.Errorf("required scheduler must not be reconfigured")
		case pluginabi.MethodAuthIdentifier:
			return marshalRPCResult(rpcIdentifierResponse{Identifier: "runtime-provider"})
		case pluginabi.MethodAuthLoginStart:
			var req rpcAuthLoginStartRequest
			if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			operationHost = req.Host
			return marshalRPCResult(pluginapi.AuthLoginStartResponse{Provider: req.Provider, State: "runtime-state"})
		default:
			return nil, fmt.Errorf("unexpected plugin method %s", method)
		}
	}}
	loader := &sequencePluginLoader{clients: []pluginClient{client}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)

	if errApply := h.ApplyConfig(context.Background(), initialCfg); errApply != nil {
		t.Fatalf("initial ApplyConfig() error = %v", errApply)
	}
	activeSnapshot := h.Snapshot()
	h.mu.Lock()
	activeLoaded := h.loaded["quota-policy"]
	activeClient := activeLoaded.client
	h.managementRoutes["GET /runtime-route"] = managementRouteRecord{pluginID: "quota-policy"}
	h.resourceRoutes["GET /runtime-resource"] = resourceRouteRecord{pluginID: "quota-policy"}
	h.mu.Unlock()

	if errApply := h.ApplyConfig(context.Background(), equivalentCfg); errApply != nil {
		t.Fatalf("equal-boundary ApplyConfig() error = %v", errApply)
	}
	if h.Snapshot() != activeSnapshot {
		t.Fatal("equal-boundary config replaced the active snapshot")
	}
	h.mu.Lock()
	currentLoaded := h.loaded["quota-policy"]
	loadedUnchanged := currentLoaded == activeLoaded
	clientUnchanged := currentLoaded != nil && currentLoaded.client == activeClient
	runtimeRefreshed := h.runtimeConfig == equivalentCfg
	routesUnchanged := len(h.managementRoutes) == 1 && len(h.resourceRoutes) == 1 &&
		h.managementRoutes["GET /runtime-route"].pluginID == "quota-policy" &&
		h.resourceRoutes["GET /runtime-resource"].pluginID == "quota-policy"
	h.mu.Unlock()
	if !loadedUnchanged || !clientUnchanged {
		t.Fatal("equal-boundary config replaced the active plugin instance or client")
	}
	if !runtimeRefreshed {
		t.Fatal("equal-boundary config did not refresh the active runtime config")
	}
	if !routesUnchanged {
		t.Fatal("equal-boundary config replaced active plugin routes")
	}

	summary := h.hostConfigSummary()
	if summary.ProxyURL != "http://new-proxy.local" || summary.AuthDir != "/new/auth/dir" || !summary.ForceModelPrefix {
		t.Fatalf("hostConfigSummary() = %#v, want refreshed runtime fields", summary)
	}
	if len(summary.OAuthModelAlias["runtime-provider"]) != 1 || summary.OAuthModelAlias["runtime-provider"][0].Alias != "alias-model" {
		t.Fatalf("hostConfigSummary() aliases = %#v, want refreshed alias", summary.OAuthModelAlias)
	}
	if got := summary.ExcludedModels["runtime-provider"]; len(got) != 1 || got[0] != "hidden-model" {
		t.Fatalf("hostConfigSummary() exclusions = %#v, want refreshed exclusion", summary.ExcludedModels)
	}
	resp, handled, errStart := h.StartLogin(context.Background(), "runtime-provider", "http://localhost/callback")
	if errStart != nil || !handled || resp.State != "runtime-state" {
		t.Fatalf("StartLogin() = (%#v, %t, %v), want refreshed runtime operation", resp, handled, errStart)
	}
	if operationHost.ProxyURL != summary.ProxyURL || operationHost.AuthDir != summary.AuthDir || !operationHost.ForceModelPrefix {
		t.Fatalf("StartLogin() host = %#v, want refreshed summary %#v", operationHost, summary)
	}
	if got := registerCalls.Load(); got != 1 {
		t.Fatalf("plugin.register calls = %d, want 1", got)
	}
	if got := reconfigureCalls.Load(); got != 0 {
		t.Fatalf("plugin.reconfigure calls = %d, want 0", got)
	}
	if loader.calls != 1 {
		t.Fatalf("loader calls = %d, want 1", loader.calls)
	}
}

func TestHostApplyConfigActiveRequiredSchedulerRejectsInvalidRuntimeBeforeEqualBoundary(t *testing.T) {
	pluginsDir := makePluginDir(t, "quota-policy")
	initialCfg := requiredBoundaryConfig(t, pluginsDir, "")
	var registerCalls atomic.Int32
	var reconfigureCalls atomic.Int32
	client := requiredBoundaryClient(&registerCalls, &reconfigureCalls)
	loader := &sequencePluginLoader{clients: []pluginClient{client}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)

	if errApply := h.ApplyConfig(context.Background(), initialCfg); errApply != nil {
		t.Fatalf("initial ApplyConfig() error = %v", errApply)
	}
	activeSnapshot := h.Snapshot()
	h.mu.Lock()
	activeLoaded := h.loaded["quota-policy"]
	activeRuntimeConfig := h.runtimeConfig
	h.managementRoutes["GET /validation-route"] = managementRouteRecord{pluginID: "quota-policy"}
	h.resourceRoutes["GET /validation-resource"] = resourceRouteRecord{pluginID: "quota-policy"}
	h.mu.Unlock()

	invalid := requiredBoundaryConfig(t, pluginsDir, "")
	invalid.Home.Enabled = true
	if errApply := h.ApplyConfig(context.Background(), invalid); errApply == nil || !strings.Contains(errApply.Error(), "incompatible with Home") {
		t.Fatalf("invalid equal-boundary ApplyConfig() error = %v, want Home incompatibility", errApply)
	}
	if h.Snapshot() != activeSnapshot {
		t.Fatal("invalid equal-boundary config replaced the active snapshot")
	}
	h.mu.Lock()
	stateUnchanged := h.loaded["quota-policy"] == activeLoaded && h.runtimeConfig == activeRuntimeConfig &&
		len(h.managementRoutes) == 1 && len(h.resourceRoutes) == 1 &&
		h.managementRoutes["GET /validation-route"].pluginID == "quota-policy" &&
		h.resourceRoutes["GET /validation-resource"].pluginID == "quota-policy"
	h.mu.Unlock()
	if !stateUnchanged {
		t.Fatal("invalid equal-boundary config mutated active plugin/runtime state")
	}
	if got := registerCalls.Load(); got != 1 {
		t.Fatalf("plugin.register calls = %d, want 1", got)
	}
	if got := reconfigureCalls.Load(); got != 0 {
		t.Fatalf("plugin.reconfigure calls = %d, want 0", got)
	}
	if loader.calls != 1 {
		t.Fatalf("loader calls = %d, want 1", loader.calls)
	}
}

func TestHostApplyConfigActiveRequiredSchedulerMutationsRequireRestartBeforeStateChange(t *testing.T) {
	otherDir := filepath.Join(t.TempDir(), "other-plugins")
	tests := []struct {
		name string
		cfg  func(*testing.T, string) *config.Config
	}{
		{name: "global plugin disable", cfg: func(t *testing.T, dir string) *config.Config {
			cfg := requiredBoundaryConfig(t, dir, "")
			cfg.Plugins.Enabled = false
			return cfg
		}},
		{name: "required to optional", cfg: func(t *testing.T, dir string) *config.Config {
			cfg := requiredBoundaryConfig(t, dir, "")
			cfg.Plugins.RequiredScheduler = ""
			return cfg
		}},
		{name: "required name", cfg: func(t *testing.T, dir string) *config.Config {
			cfg := requiredBoundaryConfig(t, dir, "")
			cfg.Plugins.RequiredScheduler = "other-policy"
			cfg.Plugins.Configs["other-policy"] = cfg.Plugins.Configs["quota-policy"]
			return cfg
		}},
		{name: "plugin directory", cfg: func(t *testing.T, _ string) *config.Config {
			return requiredBoundaryConfig(t, otherDir, "")
		}},
		{name: "required plugin disabled", cfg: func(t *testing.T, dir string) *config.Config {
			cfg := requiredBoundaryConfig(t, dir, "")
			disabled := false
			item := cfg.Plugins.Configs["quota-policy"]
			item.Enabled = &disabled
			cfg.Plugins.Configs["quota-policy"] = item
			return cfg
		}},
		{name: "enabled plugin set", cfg: func(t *testing.T, dir string) *config.Config {
			cfg := requiredBoundaryConfig(t, dir, "")
			enabled := true
			cfg.Plugins.Configs["observer"] = config.PluginInstanceConfig{Enabled: &enabled}
			return cfg
		}},
		{name: "required priority", cfg: func(t *testing.T, dir string) *config.Config {
			cfg := requiredBoundaryConfig(t, dir, "")
			item := cfg.Plugins.Configs["quota-policy"]
			item.Priority++
			cfg.Plugins.Configs["quota-policy"] = item
			return cfg
		}},
		{name: "required config", cfg: func(t *testing.T, dir string) *config.Config {
			return requiredBoundaryConfig(t, dir, `
    enabled: true
    priority: 7
    tiers:
      - name: reserve
        accounts: [third]
`)
		}},
		{name: "required store path", cfg: func(t *testing.T, dir string) *config.Config {
			return requiredBoundaryConfig(t, dir, `
    enabled: true
    priority: 7
    tiers:
      - name: primary
        accounts: [first, second]
    store:
      path: releases/quota-policy-v2.so
`)
		}},
		{name: "store sources", cfg: func(t *testing.T, dir string) *config.Config {
			cfg := requiredBoundaryConfig(t, dir, "")
			cfg.Plugins.StoreSources = []string{"https://plugins.example.invalid/index.yaml"}
			return cfg
		}},
		{name: "store auth", cfg: func(t *testing.T, dir string) *config.Config {
			cfg := requiredBoundaryConfig(t, dir, "")
			cfg.Plugins.StoreAuth = []sdkpluginstore.AuthConfig{{
				Match:    "https://plugins.example.invalid/private/",
				ApplyTo:  []string{sdkpluginstore.RequestKindArtifact},
				Type:     sdkpluginstore.AuthTypeBearer,
				TokenEnv: "PLUGIN_STORE_TOKEN",
			}}
			return cfg
		}},
		{name: "auth revision", cfg: func(t *testing.T, dir string) *config.Config {
			cfg := requiredBoundaryConfig(t, dir, "")
			cfg.Plugins.AuthRevision = 2
			return cfg
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pluginsDir := makePluginDir(t, "quota-policy")
			initialCfg := requiredBoundaryConfig(t, pluginsDir, "")
			var registerCalls atomic.Int32
			var reconfigureCalls atomic.Int32
			client := requiredBoundaryClient(&registerCalls, &reconfigureCalls)
			loader := &sequencePluginLoader{clients: []pluginClient{client}}
			h := NewForTest(loader)
			t.Cleanup(h.ShutdownAll)
			if errApply := h.ApplyConfig(context.Background(), initialCfg); errApply != nil {
				t.Fatalf("initial ApplyConfig() error = %v", errApply)
			}
			activeSnapshot := h.Snapshot()
			h.mu.Lock()
			activeLoaded := h.loaded["quota-policy"]
			activeRuntimeConfig := h.runtimeConfig
			h.managementRoutes["GET /prior"] = managementRouteRecord{pluginID: "quota-policy"}
			h.resourceRoutes["GET /prior-resource"] = resourceRouteRecord{pluginID: "quota-policy"}
			h.mu.Unlock()

			errApply := h.ApplyConfig(context.Background(), tt.cfg(t, pluginsDir))
			if !errors.Is(errApply, config.ErrRestartRequired) {
				t.Fatalf("ApplyConfig() error = %v, want ErrRestartRequired", errApply)
			}
			var restartErr *config.RestartRequiredError
			if !errors.As(errApply, &restartErr) {
				t.Fatalf("ApplyConfig() error type = %T, want *config.RestartRequiredError", errApply)
			}
			if h.Snapshot() != activeSnapshot {
				t.Fatal("rejected required config replaced the active snapshot")
			}
			h.mu.Lock()
			if h.loaded["quota-policy"] != activeLoaded || h.runtimeConfig != activeRuntimeConfig {
				t.Fatal("rejected required config replaced active plugin/runtime state")
			}
			if len(h.managementRoutes) != 1 || len(h.resourceRoutes) != 1 || len(h.retired["quota-policy"]) != 0 {
				t.Fatalf("rejected required config mutated routes or retired plugins: management=%d resources=%d retired=%d", len(h.managementRoutes), len(h.resourceRoutes), len(h.retired["quota-policy"]))
			}
			h.mu.Unlock()
			if got := registerCalls.Load(); got != 1 {
				t.Fatalf("plugin.register calls = %d, want 1", got)
			}
			if got := reconfigureCalls.Load(); got != 0 {
				t.Fatalf("plugin.reconfigure calls = %d, want 0", got)
			}
			if loader.calls != 1 {
				t.Fatalf("loader calls = %d, want 1", loader.calls)
			}
		})
	}
}

func TestHostApplyConfigOptionalToRequiredRequiresRestartBeforePluginCall(t *testing.T) {
	pluginsDir := makePluginDir(t, "quota-policy")
	optionalCfg := requiredBoundaryConfig(t, pluginsDir, "")
	optionalCfg.Plugins.RequiredScheduler = ""
	var registerCalls atomic.Int32
	var reconfigureCalls atomic.Int32
	client := requiredBoundaryClient(&registerCalls, &reconfigureCalls)
	loader := &sequencePluginLoader{clients: []pluginClient{client}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	if errApply := h.ApplyConfig(context.Background(), optionalCfg); errApply != nil {
		t.Fatalf("initial optional ApplyConfig() error = %v", errApply)
	}
	activeSnapshot := h.Snapshot()

	errApply := h.ApplyConfig(context.Background(), requiredBoundaryConfig(t, pluginsDir, ""))
	if !errors.Is(errApply, config.ErrRestartRequired) {
		t.Fatalf("optional-to-required ApplyConfig() error = %v, want ErrRestartRequired", errApply)
	}
	if h.Snapshot() != activeSnapshot {
		t.Fatal("optional-to-required rejection replaced snapshot")
	}
	if got := reconfigureCalls.Load(); got != 0 {
		t.Fatalf("plugin.reconfigure calls = %d, want 0", got)
	}
}

func TestHostApplyConfigUnrelatedConfigChangeKeepsRequiredSchedulerActive(t *testing.T) {
	pluginsDir := makePluginDir(t, "quota-policy")
	initialCfg := requiredBoundaryConfig(t, pluginsDir, "")
	var registerCalls atomic.Int32
	var reconfigureCalls atomic.Int32
	client := requiredBoundaryClient(&registerCalls, &reconfigureCalls)
	loader := &sequencePluginLoader{clients: []pluginClient{client}}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	if errApply := h.ApplyConfig(context.Background(), initialCfg); errApply != nil {
		t.Fatalf("initial ApplyConfig() error = %v", errApply)
	}
	activeSnapshot := h.Snapshot()
	h.mu.Lock()
	activeLoaded := h.loaded["quota-policy"]
	h.mu.Unlock()

	unrelated := requiredBoundaryConfig(t, pluginsDir, "")
	unrelated.Debug = true
	unrelated.Port = 9123
	if errApply := h.ApplyConfig(context.Background(), unrelated); errApply != nil {
		t.Fatalf("unrelated ApplyConfig() error = %v", errApply)
	}
	if h.Snapshot() != activeSnapshot {
		t.Fatal("unrelated config change replaced required scheduler snapshot")
	}
	h.mu.Lock()
	if h.loaded["quota-policy"] != activeLoaded {
		t.Fatal("unrelated config change replaced required scheduler instance")
	}
	h.mu.Unlock()
	if got := reconfigureCalls.Load(); got != 0 {
		t.Fatalf("plugin.reconfigure calls = %d, want 0", got)
	}
}

func TestManagerSelectAuthRequiredSchedulerFailuresNeverUseNativeFallback(t *testing.T) {
	tests := []struct {
		name      string
		scheduler pluginapi.Scheduler
		setup     func(*Host)
		noRecord  bool
	}{
		{name: "missing", noRecord: true},
		{name: "unavailable", scheduler: nil},
		{
			name: "fused",
			scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "native-fallback"}, nil
			}),
			setup: func(host *Host) { host.fusePlugin("quota-policy", "test", "fused") },
		},
		{
			name: "panic",
			scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				panic("scheduler panic")
			}),
		},
		{
			name: "scheduler error",
			scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{}, errors.New("scheduler failed")
			}),
		},
		{
			name: "unhandled",
			scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{Handled: false}, nil
			}),
		},
		{
			name: "empty invalid response",
			scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{Handled: true}, nil
			}),
		},
		{
			name: "unknown auth id",
			scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "unknown-auth"}, nil
			}),
		},
		{
			name: "invalid delegate",
			scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{Handled: true, DelegateBuiltin: "unknown-selector"}, nil
			}),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var host *Host
			if test.noRecord {
				host = newHostWithRecords()
			} else {
				host = newHostWithRecords(capabilityRecord{
					id: "quota-policy",
					plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
						Scheduler: test.scheduler,
					}},
				})
			}
			host.Snapshot().requiredScheduler = "quota-policy"
			if test.setup != nil {
				test.setup(host)
			}
			manager := restartBoundaryTestManager(t, host)

			selected, errSelect := manager.SelectAuth(context.Background(), "restart-boundary-provider", "", cliproxyexecutor.Options{})
			if errSelect == nil {
				t.Fatalf("SelectAuth() = %#v, nil; want required-scheduler error", selected)
			}
			if selected != nil {
				t.Fatalf("SelectAuth() selected %#v after required-scheduler failure; native fallback must remain blocked", selected)
			}
		})
	}
}

func TestHostApplyConfigOptionalDiscoveryFailurePreservesLegacyClear(t *testing.T) {
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
	manager := restartBoundaryTestManager(t, h)
	if selected, errSelect := manager.SelectAuth(context.Background(), "restart-boundary-provider", "", cliproxyexecutor.Options{}); errSelect == nil || selected != nil {
		t.Fatalf("initial optional scheduler selection = %#v, %v; want policy rejection", selected, errSelect)
	}

	nonDirectory := filepath.Join(t.TempDir(), "not-a-directory")
	if errWrite := os.WriteFile(nonDirectory, []byte("invalid plugin discovery root"), 0o600); errWrite != nil {
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
	if thinking.GetProviderApplier("optional-clear-provider") != nil {
		t.Fatal("optional discovery failure retained thinking provider")
	}
	selected, errSelect := manager.SelectAuth(context.Background(), "restart-boundary-provider", "", cliproxyexecutor.Options{})
	if errSelect != nil || selected == nil || selected.ID != "native-fallback" {
		t.Fatalf("selection after optional discovery failure = %#v, %v; want native fallback", selected, errSelect)
	}
}

func requiredBoundaryConfig(t *testing.T, pluginsDir, requiredConfig string) *config.Config {
	t.Helper()
	if requiredConfig == "" {
		requiredConfig = `
    enabled: true
    priority: 7
    tiers:
      - name: primary
        accounts: [first, second]
`
	}
	requiredConfig = indentRequiredBoundaryYAML(requiredConfig)
	raw := fmt.Sprintf(`plugins:
  enabled: true
  dir: %q
  required-scheduler: quota-policy
  auth-revision: 1
  configs:
    quota-policy:
%s
    observer:
      enabled: false
`, pluginsDir, requiredConfig)
	cfg, errParse := config.ParseConfigBytes([]byte(raw))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v\n%s", errParse, raw)
	}
	return cfg
}

func indentRequiredBoundaryYAML(raw string) string {
	lines := strings.Split(strings.Trim(raw, "\n\r"), "\n")
	minimum := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if minimum < 0 || indent < minimum {
			minimum = indent
		}
	}
	for index, line := range lines {
		if minimum > 0 && len(line) >= minimum {
			line = line[minimum:]
		}
		lines[index] = "      " + line
	}
	return strings.Join(lines, "\n")
}

func requiredBoundaryClient(registerCalls, reconfigureCalls *atomic.Int32) pluginClient {
	plugin := validTestPlugin("quota-policy")
	plugin.Capabilities.Scheduler = schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
		return pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "policy", RejectReason: "required policy"}, nil
	})
	return &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			registerCalls.Add(1)
			return lifecycleRegistrationResult(plugin)
		case pluginabi.MethodPluginReconfigure:
			reconfigureCalls.Add(1)
			return nil, fmt.Errorf("required scheduler must not be reconfigured")
		default:
			return nil, fmt.Errorf("unexpected plugin method %s", method)
		}
	}}
}

func restartBoundaryTestManager(t *testing.T, host *Host) *coreauth.Manager {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(restartBoundaryTestExecutor{})
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: "native-fallback", Provider: "restart-boundary-provider", Status: coreauth.StatusActive}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	manager.SetPluginScheduler(host)
	return manager
}

type restartBoundaryTestExecutor struct{}

func (restartBoundaryTestExecutor) Identifier() string { return "restart-boundary-provider" }
func (restartBoundaryTestExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (restartBoundaryTestExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (restartBoundaryTestExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}
func (restartBoundaryTestExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (restartBoundaryTestExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
