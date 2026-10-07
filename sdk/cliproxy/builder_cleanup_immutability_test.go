package cliproxy

import (
	"context"
	"reflect"
	"runtime"
	"testing"

	configaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestBuilderFailedOwnedPluginStartupCleansHostAndPreservesGlobalAccess(t *testing.T) {
	pluginsDir := buildCliproxySchedulerPluginFixture(t)
	configaccess.Register(nil)
	configaccess.Register(&config.SDKConfig{APIKeys: []string{"prior-key"}})
	priorProviders := sdkaccess.RegisteredProviders()
	t.Cleanup(func() { configaccess.Register(nil) })

	var allocated *pluginhost.Host
	builder := NewBuilder()
	builder.pluginHostFactory = func() *pluginhost.Host {
		allocated = pluginhost.New()
		return allocated
	}

	enabled := true
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{APIKeys: []string{"replacement-key"}},
		AuthDir:   t.TempDir(),
		Plugins: config.PluginsConfig{
			Enabled:           true,
			Dir:               pluginsDir,
			RequiredScheduler: "quota-policy",
			Configs: map[string]config.PluginInstanceConfig{
				"scheduler":    {Enabled: &enabled},
				"quota-policy": {Enabled: &enabled},
			},
		},
	}
	before := cfg.CloneForRuntime()
	service, errBuild := builder.WithConfig(cfg).WithConfigPath(t.TempDir() + "/config.yaml").Build()
	if errBuild == nil {
		t.Fatal("Build() error = nil, want missing required scheduler failure")
	}
	if service != nil {
		t.Fatal("Build() returned service after required scheduler failure")
	}
	if allocated == nil {
		t.Fatal("builder did not allocate a plugin host")
	}
	if allocated.PluginBusy("scheduler") || len(allocated.RegisteredPlugins()) != 0 {
		t.Fatal("failed build retained a plugin client or registration")
	}
	if gotProviders := sdkaccess.RegisteredProviders(); !sameProviders(gotProviders, priorProviders) {
		t.Fatalf("failed build changed global access providers: got %#v want %#v", gotProviders, priorProviders)
	}
	if !reflect.DeepEqual(cfg, before) {
		t.Fatal("Build() mutated caller-owned config")
	}
}

func TestBuilderStoresPrivateRuntimeConfig(t *testing.T) {
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{ProxyURL: "http://original.invalid"},
		AuthDir:   t.TempDir(),
	}
	service, errBuild := NewBuilder().WithConfig(cfg).WithConfigPath(t.TempDir() + "/config.yaml").Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}
	t.Cleanup(func() { _ = service.Shutdown(nil) })

	cfg.ProxyURL = "http://caller-mutated.invalid"
	if service.cfg == cfg {
		t.Fatal("service published caller-owned config pointer")
	}
	if service.cfg.ProxyURL != "http://original.invalid" {
		t.Fatalf("service config ProxyURL = %q, want private original snapshot", service.cfg.ProxyURL)
	}
}

func TestServiceShutdownTearsDownRequiredPluginHostAndAllowsReuse(t *testing.T) {
	pluginsDir := buildCliproxySchedulerPluginFixture(t)
	enabled := true
	cfg := &config.Config{
		AuthDir: t.TempDir(),
		Plugins: config.PluginsConfig{
			Enabled:           true,
			Dir:               pluginsDir,
			RequiredScheduler: "scheduler",
			Configs: map[string]config.PluginInstanceConfig{
				"scheduler": {Enabled: &enabled},
			},
		},
	}
	service, errBuild := NewBuilder().WithConfig(cfg).WithConfigPath(t.TempDir() + "/config.yaml").Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}
	sdktranslator.SetPluginHooks(service.pluginHost)
	if errShutdown := service.Shutdown(context.Background()); errShutdown != nil {
		t.Fatalf("Shutdown() error = %v", errShutdown)
	}
	if sdktranslator.HasPluginHooks() {
		t.Fatal("Shutdown() retained global translator plugin hooks")
	}
	if service.pluginHost.PluginBusy("scheduler") || service.pluginHost.HasScheduler() {
		t.Fatal("Shutdown() retained required scheduler client or capability")
	}
	if errApply := service.pluginHost.ApplyConfig(context.Background(), cfg); errApply != nil {
		t.Fatalf("required ApplyConfig() after service shutdown error = %v", errApply)
	}
	service.pluginHost.ShutdownAll()
}

func sameProviders(left, right []sdkaccess.Provider) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func buildCliproxySchedulerPluginFixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("builder native plugin fixture currently requires Linux")
	}
	return buildNativeSchedulerFixture(t)
}
