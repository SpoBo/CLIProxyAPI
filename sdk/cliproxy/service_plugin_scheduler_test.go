package cliproxy

import (
	"context"
	"reflect"
	"testing"
	"unsafe"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestBuilderBuildInjectsPluginHostScheduler(t *testing.T) {
	host := pluginhost.New()
	service, errBuild := NewBuilder().
		WithConfig(&config.Config{AuthDir: t.TempDir()}).
		WithConfigPath(t.TempDir() + "/config.yaml").
		WithPluginHost(host).
		Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}

	got := pluginSchedulerFromManager(t, service.coreManager)
	if got != host {
		t.Fatalf("plugin scheduler = %p, want host %p", got, host)
	}
}

func TestServiceSyncPluginRuntimeConfigInjectsPluginHostScheduler(t *testing.T) {
	host := pluginhost.New()
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
		pluginHost:  host,
	}

	if ok := service.syncPluginRuntimeConfig(context.Background()); !ok {
		t.Fatal("syncPluginRuntimeConfig() = false, want true")
	}

	got := pluginSchedulerFromManager(t, service.coreManager)
	if got != host {
		t.Fatalf("plugin scheduler = %p, want host %p", got, host)
	}
}

func TestServiceSyncPluginRuntimeConfigRejectsUnavailableRequiredScheduler(t *testing.T) {
	enabled := true
	host := pluginhost.New()
	service := &Service{
		cfg: &config.Config{Plugins: config.PluginsConfig{
			Enabled:           true,
			Dir:               t.TempDir(),
			RequiredScheduler: "quota-policy",
			Configs: map[string]config.PluginInstanceConfig{
				"quota-policy": {Enabled: &enabled},
			},
		}},
		coreManager: coreauth.NewManager(nil, nil, nil),
		pluginHost:  host,
	}

	if ok := service.syncPluginRuntimeConfig(context.Background()); ok {
		t.Fatal("syncPluginRuntimeConfig() = true, want false")
	}
}

func TestServiceCommitConfigUpdateRejectsHomeWithRequiredScheduler(t *testing.T) {
	enabled := true
	current := &config.Config{}
	service := &Service{cfg: current}
	incompatible := &config.Config{
		Home: internalconfig.HomeConfig{Enabled: true},
		Plugins: config.PluginsConfig{
			Enabled:           true,
			RequiredScheduler: "quota-policy",
			Configs: map[string]config.PluginInstanceConfig{
				"quota-policy": {Enabled: &enabled},
			},
		},
	}

	if commit := service.commitConfigUpdate(incompatible); commit.cfg != nil {
		t.Fatalf("commitConfigUpdate() = %#v, want rejected config", commit)
	}
	service.cfgMu.RLock()
	got := service.cfg
	service.cfgMu.RUnlock()
	if got != current || got.Home.Enabled {
		t.Fatalf("rejected hot apply published incompatible config: %#v", got)
	}
}

func TestServiceSyncPluginRuntimeConfigClearsPluginSchedulerWithoutHost(t *testing.T) {
	host := pluginhost.New()
	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
		pluginHost:  host,
	}
	service.coreManager.SetPluginScheduler(host)
	service.pluginHost = nil

	if ok := service.syncPluginRuntimeConfig(context.Background()); ok {
		t.Fatal("syncPluginRuntimeConfig() = true, want false")
	}

	got := pluginSchedulerFromManager(t, service.coreManager)
	if got != nil {
		t.Fatalf("plugin scheduler = %p, want nil", got)
	}
}

func pluginSchedulerFromManager(t *testing.T, manager *coreauth.Manager) *pluginhost.Host {
	t.Helper()
	if manager == nil {
		t.Fatal("manager = nil")
	}
	value := reflect.ValueOf(manager).Elem().FieldByName("pluginScheduler")
	if !value.IsValid() {
		t.Fatal("pluginScheduler field not found")
	}
	scheduler := reflect.NewAt(value.Type(), unsafe.Pointer(value.UnsafeAddr())).Elem().Interface()
	if scheduler == nil {
		return nil
	}
	host, ok := scheduler.(*pluginhost.Host)
	if !ok {
		t.Fatalf("pluginScheduler type = %T, want *pluginhost.Host", scheduler)
	}
	return host
}
