//go:build cgo && linux

package cliproxy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
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

func requiredSchedulerPluginDir(t *testing.T, registration string, schedulerResponse string, invalidFile bool) string {
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
	source := fmt.Sprintf(`#include <stdint.h>
#include <stdlib.h>
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

static int fixture_call(const char *method, const uint8_t *request, size_t request_len, cliproxy_buffer *response) {
    (void)request; (void)request_len;
    const char *payload = strcmp(method, "scheduler.pick") == 0 ? scheduler_response : registration;
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
`, strconv.Quote(registration), strconv.Quote(schedulerResponse))
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
