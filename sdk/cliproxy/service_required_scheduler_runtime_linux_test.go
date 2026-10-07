//go:build cgo && linux

package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	sdkpluginstore "github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginstore"
)

func TestServiceApplyConfigRuntimeRejectsRequiredSchedulerFailures(t *testing.T) {
	for _, test := range []struct {
		name         string
		registration string
		invalidFile  bool
	}{
		{name: "load failure", invalidFile: true},
		{name: "registration failure", registration: `{"ok":false,"error":{"code":"register_failed","message":"fixture registration failure"}}`},
		{name: "scheduler capability missing", registration: `{"ok":true,"result":{"schema_version":6,"metadata":{"name":"quota-policy","version":"1.0.0"},"capabilities":{}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			pluginsDir := requiredSchedulerPluginDir(t, test.registration, "", test.invalidFile)
			enabled := true
			cfg := &config.Config{
				ClaudeKey: []config.ClaudeKey{{APIKey: "must-not-register"}},
				Plugins: config.PluginsConfig{
					Enabled:           true,
					Dir:               pluginsDir,
					RequiredScheduler: "quota-policy",
					Configs: map[string]config.PluginInstanceConfig{
						"quota-policy": {Enabled: &enabled},
					},
				},
			}
			host := pluginhost.New()
			t.Cleanup(host.ShutdownAll)
			manager := coreauth.NewManager(nil, nil, nil)
			service := &Service{cfg: &config.Config{}, coreManager: manager, pluginHost: host}
			commit := service.commitConfigUpdate(cfg)

			if service.applyConfigRuntime(context.Background(), commit, true) {
				t.Fatal("applyConfigRuntime() = true, want required scheduler rejection")
			}
			if auths := manager.List(); len(auths) != 0 {
				t.Fatalf("runtime apply continued after scheduler rejection and registered %d auths", len(auths))
			}
		})
	}
}

func TestFailedRequiredSchedulerDiscoveryHotApplyCannotRestoreNativeSelection(t *testing.T) {
	registration := `{"ok":true,"result":{"schema_version":6,"metadata":{"Name":"quota-policy","Version":"1.0.0","Author":"test","GitHubRepository":"https://github.com/router-for-me/CLIProxyAPI"},"capabilities":{"scheduler":true}}}`
	schedulerResponse := `{"ok":true,"result":{"handled":true,"reject":true,"reject_code":"policy_denied","reject_reason":"fixture policy rejection"}}`
	pluginsDir := requiredSchedulerPluginDir(t, registration, schedulerResponse, false)
	enabled := true
	validCfg := &config.Config{Plugins: config.PluginsConfig{
		Enabled:           true,
		Dir:               pluginsDir,
		RequiredScheduler: "quota-policy",
		Configs: map[string]config.PluginInstanceConfig{
			"quota-policy": {Enabled: &enabled},
		},
	}}
	host := pluginhost.New()
	t.Cleanup(host.ShutdownAll)
	if errApply := host.ApplyConfig(context.Background(), validCfg); errApply != nil {
		t.Fatalf("initial ApplyConfig() error = %v", errApply)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(serviceTestPluginExecutor{})
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{ID: "native-fallback", Provider: "plugin-provider", Status: coreauth.StatusActive}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	manager.SetPluginScheduler(host)
	if selected, errSelect := manager.SelectAuth(context.Background(), "plugin-provider", "", cliproxyexecutor.Options{}); errSelect == nil || selected != nil {
		t.Fatalf("initial required scheduler selection = %#v, %v; want policy rejection", selected, errSelect)
	}
	activeSnapshot := host.Snapshot()

	nonDirectory := filepath.Join(t.TempDir(), "not-a-plugin-directory")
	if errWrite := os.WriteFile(nonDirectory, []byte("invalid plugin discovery root"), 0o600); errWrite != nil {
		t.Fatalf("WriteFile(non-directory) error = %v", errWrite)
	}
	failedCfg := *validCfg
	failedCfg.Plugins = validCfg.Plugins
	failedCfg.Plugins.Dir = nonDirectory
	if errApply := host.ApplyConfig(context.Background(), &failedCfg); errApply == nil {
		t.Fatal("hot ApplyConfig() error = nil, want plugin discovery failure")
	}
	if host.Snapshot() != activeSnapshot {
		t.Fatal("failed hot apply replaced the active required-scheduler snapshot")
	}

	if selected, errSelect := manager.SelectAuth(context.Background(), "plugin-provider", "", cliproxyexecutor.Options{}); errSelect == nil || selected != nil {
		t.Fatalf("selection after failed hot apply = %#v, %v; native fallback was re-enabled", selected, errSelect)
	}

	manager.SetConfig(validCfg)
	service := &Service{cfg: validCfg, coreManager: manager, pluginHost: host}
	incompatible := *validCfg
	incompatible.Home.Enabled = true
	if service.applyConfigUpdateWithAuthSynthesis(context.Background(), &incompatible, false) {
		t.Fatal("runtime hot apply accepted Home with a required scheduler")
	}
	if manager.HomeEnabled() {
		t.Fatal("rejected runtime hot apply enabled Home dispatch")
	}
	if selected, errSelect := manager.SelectAuth(context.Background(), "plugin-provider", "", cliproxyexecutor.Options{}); errSelect == nil || selected != nil {
		t.Fatalf("selection after rejected Home hot apply = %#v, %v; Home bypassed required scheduler", selected, errSelect)
	}
}

func TestServiceApplyConfigRuntimeKeepsDefaultWithoutPluginHost(t *testing.T) {
	service := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	commit := service.commitConfigUpdate(&config.Config{})

	if !service.applyConfigRuntime(context.Background(), commit, false) {
		t.Fatal("applyConfigRuntime() = false, want default config to remain accepted")
	}
}

func TestServiceRequiredSchedulerMutationIsRejectedBeforeAnyRuntimeStateChanges(t *testing.T) {
	registration := `{"ok":true,"result":{"schema_version":6,"metadata":{"Name":"quota-policy","Version":"1.0.0","Author":"test","GitHubRepository":"https://github.com/router-for-me/CLIProxyAPI"},"capabilities":{"scheduler":true}}}`
	schedulerResponse := `{"ok":true,"result":{"handled":true,"reject":true,"reject_code":"policy_denied","reject_reason":"fixture policy rejection"}}`
	pluginsDir := requiredSchedulerPluginDir(t, registration, schedulerResponse, false)
	current := requiredSchedulerServiceConfig(pluginsDir)
	host := pluginhost.New()
	t.Cleanup(host.ShutdownAll)
	if errApply := host.ApplyConfig(context.Background(), current); errApply != nil {
		t.Fatalf("initial ApplyConfig() error = %v", errApply)
	}
	activeSnapshot := host.Snapshot()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.SetConfig(current)
	manager.SetPluginScheduler(host)
	initialSelector := manager.Selector()
	discoveryManager := newDiscoveryAdvertiserManager()
	discoveryManager.lastCfg = current
	discoveryManager.generation = 41
	pprofMarker := &pprofServer{}
	service := &Service{
		cfg:              current,
		coreManager:      manager,
		pluginHost:       host,
		discoveryManager: discoveryManager,
		pprofServer:      pprofMarker,
	}
	pprofCalls := 0
	clientCalls := 0
	service.applyPprofConfigContextFn = func(context.Context, *config.Config) bool {
		pprofCalls++
		return true
	}
	service.updateServerClientsContextFn = func(context.Context, *config.Config) bool {
		clientCalls++
		return true
	}

	mutated := cloneRequiredSchedulerServiceConfig(current)
	mutated.Plugins.RequiredScheduler = ""
	mutated.Routing.Strategy = "fill-first"
	mutated.Debug = true
	if service.applyConfigUpdateWithAuthSynthesis(context.Background(), mutated, false) {
		t.Fatal("required scheduler mutation hot reload succeeded, want restart-required rejection")
	}
	service.cfgMu.RLock()
	gotCfg := service.cfg
	service.cfgMu.RUnlock()
	if gotCfg != current {
		t.Fatal("rejected reload published s.cfg")
	}
	if manager.Selector() != initialSelector {
		t.Fatal("rejected reload changed auth manager selector")
	}
	if pprofCalls != 0 || service.pprofServer != pprofMarker {
		t.Fatalf("rejected reload changed pprof state: calls=%d server=%p", pprofCalls, service.pprofServer)
	}
	discoveryManager.mu.Lock()
	if discoveryManager.lastCfg != current || discoveryManager.generation != 41 {
		t.Fatalf("rejected reload changed discovery state: cfg=%p generation=%d", discoveryManager.lastCfg, discoveryManager.generation)
	}
	discoveryManager.mu.Unlock()
	if clientCalls != 0 {
		t.Fatalf("rejected reload updated server clients %d times", clientCalls)
	}
	if host.Snapshot() != activeSnapshot {
		t.Fatal("rejected reload changed plugin snapshot")
	}
	if pluginSchedulerFromManager(t, manager) != host {
		t.Fatal("rejected reload changed manager plugin scheduler")
	}
}

func TestServiceOptionalToRequiredTransitionIsRejectedBeforeConfigPublish(t *testing.T) {
	optional := &config.Config{}
	host := pluginhost.New()
	t.Cleanup(host.ShutdownAll)
	if errApply := host.ApplyConfig(context.Background(), optional); errApply != nil {
		t.Fatalf("initial optional ApplyConfig() error = %v", errApply)
	}
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	initialSelector := manager.Selector()
	service := &Service{cfg: optional, coreManager: manager, pluginHost: host}
	enabled := true
	required := &config.Config{
		Routing: internalconfig.RoutingConfig{Strategy: "fill-first"},
		Plugins: config.PluginsConfig{
			Enabled:           true,
			Dir:               t.TempDir(),
			RequiredScheduler: "quota-policy",
			Configs: map[string]config.PluginInstanceConfig{
				"quota-policy": {Enabled: &enabled},
			},
		},
	}
	if service.applyConfigUpdateWithAuthSynthesis(context.Background(), required, false) {
		t.Fatal("optional-to-required hot transition succeeded")
	}
	service.cfgMu.RLock()
	gotCfg := service.cfg
	service.cfgMu.RUnlock()
	if gotCfg != optional {
		t.Fatal("optional-to-required rejection published s.cfg")
	}
	if manager.Selector() != initialSelector {
		t.Fatal("optional-to-required rejection changed manager selector")
	}
}

func TestServiceStoreAuthMutationRequiresRestartBeforePluginCalls(t *testing.T) {
	registration := `{"ok":true,"result":{"schema_version":6,"metadata":{"Name":"quota-policy","Version":"1.0.0","Author":"test","GitHubRepository":"https://github.com/router-for-me/CLIProxyAPI"},"capabilities":{"scheduler":true}}}`
	schedulerResponse := `{"ok":true,"result":{"handled":true,"reject":true,"reject_code":"policy_denied","reject_reason":"fixture policy rejection"}}`
	callLog := filepath.Join(t.TempDir(), "plugin-calls.log")
	pluginsDir := requiredSchedulerPluginDir(t, registration, schedulerResponse, false, callLog)
	current := requiredSchedulerServiceConfig(pluginsDir)
	host := pluginhost.New()
	t.Cleanup(host.ShutdownAll)
	if errApply := host.ApplyConfig(context.Background(), current); errApply != nil {
		t.Fatalf("initial ApplyConfig() error = %v", errApply)
	}
	activeSnapshot := host.Snapshot()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.SetConfig(current)
	manager.SetPluginScheduler(host)
	initialSelector := manager.Selector()
	service := &Service{cfg: current, coreManager: manager, pluginHost: host}

	mutated := cloneRequiredSchedulerServiceConfig(current)
	mutated.Plugins.StoreAuth = []sdkpluginstore.AuthConfig{{
		Match:    "https://plugins.example.invalid/private/",
		ApplyTo:  []string{sdkpluginstore.RequestKindArtifact},
		Type:     sdkpluginstore.AuthTypeBearer,
		TokenEnv: "PLUGIN_STORE_TOKEN",
	}}
	errPreflight := host.PreflightConfig(mutated)
	if !errors.Is(errPreflight, internalconfig.ErrRestartRequired) {
		t.Fatalf("PreflightConfig() error = %v, want ErrRestartRequired", errPreflight)
	}
	var restartErr *internalconfig.RestartRequiredError
	if !errors.As(errPreflight, &restartErr) {
		t.Fatalf("PreflightConfig() error type = %T, want *RestartRequiredError", errPreflight)
	}
	if service.applyConfigUpdateWithAuthSynthesis(context.Background(), mutated, false) {
		t.Fatal("store-auth mutation hot reload succeeded, want restart-required rejection")
	}

	service.cfgMu.RLock()
	gotCfg := service.cfg
	service.cfgMu.RUnlock()
	if gotCfg != current || service.configSequence != 0 {
		t.Fatalf("rejected store-auth reload changed service config state: cfg=%p sequence=%d", gotCfg, service.configSequence)
	}
	if manager.Selector() != initialSelector || pluginSchedulerFromManager(t, manager) != host {
		t.Fatal("rejected store-auth reload changed manager state")
	}
	if host.Snapshot() != activeSnapshot {
		t.Fatal("rejected store-auth reload changed plugin snapshot")
	}
	rawCalls, errRead := os.ReadFile(callLog)
	if errRead != nil {
		t.Fatalf("ReadFile(plugin call log) error = %v", errRead)
	}
	calls := string(rawCalls)
	if got := strings.Count(calls, "plugin.register\n"); got != 1 {
		t.Fatalf("plugin.register calls = %d, want initial call only; log=%q", got, calls)
	}
	if got := strings.Count(calls, "plugin.reconfigure\n"); got != 0 {
		t.Fatalf("plugin.reconfigure calls = %d, want 0; log=%q", got, calls)
	}
}

func TestServiceUnrelatedHotReloadPreservesActiveRequiredScheduler(t *testing.T) {
	registration := `{"ok":true,"result":{"schema_version":6,"metadata":{"Name":"quota-policy","Version":"1.0.0","Author":"test","GitHubRepository":"https://github.com/router-for-me/CLIProxyAPI"},"capabilities":{"scheduler":true}}}`
	schedulerResponse := `{"ok":true,"result":{"handled":true,"reject":true,"reject_code":"policy_denied","reject_reason":"fixture policy rejection"}}`
	pluginsDir := requiredSchedulerPluginDir(t, registration, schedulerResponse, false)
	current := requiredSchedulerServiceConfig(pluginsDir)
	host := pluginhost.New()
	t.Cleanup(host.ShutdownAll)
	if errApply := host.ApplyConfig(context.Background(), current); errApply != nil {
		t.Fatalf("initial ApplyConfig() error = %v", errApply)
	}
	activeSnapshot := host.Snapshot()
	manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	manager.SetConfig(current)
	manager.SetPluginScheduler(host)
	service := &Service{cfg: current, coreManager: manager, pluginHost: host}
	pprofCalls := 0
	clientCalls := 0
	service.applyPprofConfigContextFn = func(context.Context, *config.Config) bool {
		pprofCalls++
		return true
	}
	service.updateServerClientsContextFn = func(context.Context, *config.Config) bool {
		clientCalls++
		return true
	}

	unrelated := cloneRequiredSchedulerServiceConfig(current)
	unrelated.Routing.Strategy = "fill-first"
	unrelated.Debug = true
	if !service.applyConfigUpdateWithAuthSynthesis(context.Background(), unrelated, false) {
		t.Fatal("unrelated hot reload failed")
	}
	service.cfgMu.RLock()
	gotCfg := service.cfg
	service.cfgMu.RUnlock()
	if gotCfg == unrelated {
		t.Fatal("unrelated hot reload retained caller-owned config pointer")
	}
	if !reflect.DeepEqual(gotCfg, unrelated) {
		t.Fatal("unrelated hot reload did not publish an equivalent private config snapshot")
	}
	if _, ok := manager.Selector().(*coreauth.FillFirstSelector); !ok {
		t.Fatalf("unrelated hot reload selector = %T, want *FillFirstSelector", manager.Selector())
	}
	if pprofCalls != 1 || clientCalls != 1 {
		t.Fatalf("unrelated hot reload calls: pprof=%d clients=%d, want 1 each", pprofCalls, clientCalls)
	}
	if host.Snapshot() != activeSnapshot {
		t.Fatal("unrelated hot reload replaced required scheduler snapshot")
	}
	if pluginSchedulerFromManager(t, manager) != host {
		t.Fatal("unrelated hot reload replaced required scheduler client")
	}
}

func requiredSchedulerServiceConfig(pluginsDir string) *config.Config {
	enabled := true
	return &config.Config{Plugins: config.PluginsConfig{
		Enabled:           true,
		Dir:               pluginsDir,
		RequiredScheduler: "quota-policy",
		Configs: map[string]config.PluginInstanceConfig{
			"quota-policy": {Enabled: &enabled},
		},
	}}
}

func cloneRequiredSchedulerServiceConfig(cfg *config.Config) *config.Config {
	cloned := *cfg
	cloned.Plugins = cfg.Plugins
	cloned.Plugins.StoreSources = append([]string(nil), cfg.Plugins.StoreSources...)
	cloned.Plugins.Configs = make(map[string]config.PluginInstanceConfig, len(cfg.Plugins.Configs))
	for id, item := range cfg.Plugins.Configs {
		cloned.Plugins.Configs[id] = item
	}
	return &cloned
}

func requiredSchedulerPluginDir(t *testing.T, registration string, schedulerResponse string, invalidFile bool, callLogPath ...string) string {
	t.Helper()
	root := t.TempDir()
	archDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	if errMkdir := os.MkdirAll(archDir, 0o755); errMkdir != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdir)
	}
	pluginPath := filepath.Join(archDir, "quota-policy.so")
	if invalidFile {
		if errWrite := os.WriteFile(pluginPath, []byte("not a shared library"), 0o644); errWrite != nil {
			t.Fatalf("WriteFile() error = %v", errWrite)
		}
		return root
	}
	if _, errLookPath := exec.LookPath("cc"); errLookPath != nil {
		t.Skip("C compiler required for native plugin integration test")
	}
	callLog := ""
	if len(callLogPath) > 0 {
		callLog = callLogPath[0]
	}
	source := fmt.Sprintf(`#include <stdint.h>
#include <stdlib.h>
#include <stdio.h>
#include <string.h>

typedef struct { void *ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_plugin_call_fn)(const char *, const uint8_t *, size_t, cliproxy_buffer *);
typedef void (*cliproxy_plugin_free_fn)(void *, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct {
    uint32_t abi_version;
    cliproxy_plugin_call_fn call;
    cliproxy_plugin_free_fn free_buffer;
    cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

static const char *registration = %s;
static const char *scheduler_response = %s;
static const char *call_log = %s;
static const char *reconfigure_response = "{\"ok\":false,\"error\":{\"code\":\"restart_required\",\"message\":\"required scheduler reconfigure must not be called\"}}";

static void record_call(const char *method) {
    if (call_log[0] == '\0') return;
    if (strcmp(method, "plugin.register") != 0 && strcmp(method, "plugin.reconfigure") != 0) return;
    FILE *file = fopen(call_log, "a");
    if (file == NULL) return;
    fprintf(file, "%%s\n", method);
    fclose(file);
}

static int fixture_call(const char *method, const uint8_t *request, size_t request_len, cliproxy_buffer *response) {
    (void)request; (void)request_len;
    record_call(method);
    const char *payload = strcmp(method, "scheduler.pick") == 0 ? scheduler_response :
        (strcmp(method, "plugin.reconfigure") == 0 ? reconfigure_response : registration);
    size_t len = strlen(payload);
    response->ptr = malloc(len);
    response->len = len;
    memcpy(response->ptr, payload, len);
    return 0;
}

static void fixture_free(void *ptr, size_t len) { (void)len; free(ptr); }

int cliproxy_plugin_init(const void *host, cliproxy_plugin_api *plugin) {
    (void)host;
    plugin->abi_version = 1;
    plugin->call = fixture_call;
    plugin->free_buffer = fixture_free;
    plugin->shutdown = NULL;
    return 0;
}
`, strconv.Quote(registration), strconv.Quote(schedulerResponse), strconv.Quote(callLog))
	sourcePath := filepath.Join(t.TempDir(), "plugin.c")
	if errWrite := os.WriteFile(sourcePath, []byte(source), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(source) error = %v", errWrite)
	}
	cmd := exec.Command("cc", "-shared", "-fPIC", "-o", pluginPath, sourcePath)
	if output, errBuild := cmd.CombinedOutput(); errBuild != nil {
		t.Fatalf("compile native plugin: %v\n%s", errBuild, output)
	}
	return root
}
