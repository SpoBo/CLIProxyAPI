package pluginhost

import (
	"context"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestHostTeardownRequiredSchedulerRemovesRuntimeAndAllowsReuse(t *testing.T) {
	pluginsDir := makePluginDir(t, "quota-policy")
	cfg := requiredBoundaryConfig(t, pluginsDir, "")
	first := newTestSymbolLookup(&testPlugin{registerResult: requiredTeardownPlugin()})
	second := newTestSymbolLookup(&testPlugin{registerResult: requiredTeardownPlugin()})
	host := NewForTest(&sequencePluginLoader{clients: []pluginClient{first, second}})
	t.Cleanup(host.ShutdownAll)

	if errApply := host.ApplyConfig(context.Background(), cfg); errApply != nil {
		t.Fatalf("initial ApplyConfig() error = %v", errApply)
	}
	if thinking.GetProviderApplier("teardown-thinking") == nil {
		t.Fatal("required plugin thinking provider was not registered")
	}
	if !host.HasRequestInterceptors() {
		t.Fatal("required plugin request hook was not registered")
	}

	modelRegistry := newFakeModelRegistry()
	modelRegistry.RegisterClient("plugin:quota-policy:teardown-provider", "teardown-provider", []*registry.ModelInfo{{ID: "teardown-model"}})
	modelRegistry.RegisterClient("plugin:quota-policy:teardown-provider:executor", "teardown-provider", []*registry.ModelInfo{{ID: "teardown-model"}})
	executorManager := newFakeExecutorManager()
	executorManager.RegisterExecutor(&executorAdapter{host: host, pluginID: "quota-policy", provider: "teardown-provider"})
	accessKey := "plugin:quota-policy:teardown-auth"
	sdkaccess.RegisterProvider(accessKey, teardownAccessProvider{id: accessKey})
	t.Cleanup(func() { sdkaccess.UnregisterProvider(accessKey) })

	host.mu.Lock()
	host.modelRegistry = modelRegistry
	host.modelClientIDs = map[string]struct{}{"plugin:quota-policy:teardown-provider": {}}
	host.executorModelClientIDs = map[string]struct{}{"plugin:quota-policy:teardown-provider:executor": {}}
	host.executorManager = executorManager
	host.executorProviders = map[string]struct{}{"teardown-provider": {}}
	host.accessProviderKeys = map[string]struct{}{accessKey: {}}
	host.managementRoutes["GET /teardown"] = managementRouteRecord{pluginID: "quota-policy"}
	host.resourceRoutes["GET /v0/resource/plugins/quota-policy/teardown"] = resourceRouteRecord{pluginID: "quota-policy"}
	host.mu.Unlock()
	if len(modelRegistry.clients) == 0 {
		t.Fatal("required plugin models were not registered")
	}
	if _, okExecutor := executorManager.Executor("teardown-provider"); !okExecutor {
		t.Fatal("required plugin executor was not registered")
	}
	host.mu.Lock()
	if len(host.managementRoutes) == 0 || len(host.resourceRoutes) == 0 {
		host.mu.Unlock()
		t.Fatal("required plugin routes were not registered")
	}
	host.mu.Unlock()

	if errTeardown := host.TeardownContext(context.Background()); errTeardown != nil {
		t.Fatalf("TeardownContext() error = %v", errTeardown)
	}
	if host.PluginBusy("quota-policy") || host.PluginRegistered("quota-policy") || host.HasScheduler() {
		t.Fatal("teardown retained required plugin runtime state")
	}
	if host.HasRequestInterceptors() {
		t.Fatal("teardown retained request hooks")
	}
	if len(modelRegistry.clients) != 0 {
		t.Fatalf("teardown retained model clients: %#v", modelRegistry.clients)
	}
	if _, okExecutor := executorManager.Executor("teardown-provider"); okExecutor {
		t.Fatal("teardown retained plugin executor")
	}
	if registeredProviderIdentifier(accessKey) {
		t.Fatal("teardown retained frontend auth provider")
	}
	if thinking.GetProviderApplier("teardown-thinking") != nil {
		t.Fatal("teardown retained thinking provider")
	}
	host.mu.Lock()
	if host.runtimeConfig != nil || host.requiredBoundary != nil || len(host.managementRoutes) != 0 || len(host.resourceRoutes) != 0 {
		host.mu.Unlock()
		t.Fatal("teardown retained lifecycle config or routes")
	}
	host.mu.Unlock()
	if first.shutdownCalls != 1 {
		t.Fatalf("first plugin shutdown calls = %d, want 1", first.shutdownCalls)
	}

	if errApply := host.ApplyConfig(context.Background(), cfg); errApply != nil {
		t.Fatalf("ApplyConfig() after teardown error = %v", errApply)
	}
	if !host.PluginRegistered("quota-policy") || !host.HasScheduler() {
		t.Fatal("host did not accept fresh required startup after teardown")
	}
}

func TestHostApplyConfigDoesNotMutateOrRetainCallerConfig(t *testing.T) {
	enabled := false
	cfg := &config.Config{
		SDKConfig: config.SDKConfig{ProxyURL: "  http://proxy.invalid  "},
		AuthDir:   "  /private/auth  ",
		Plugins: config.PluginsConfig{
			Enabled: false,
			Dir:     "  ~/plugins  ",
			Configs: map[string]config.PluginInstanceConfig{
				"disabled": {Enabled: &enabled},
			},
		},
		OAuthExcludedModels: map[string][]string{"Codex": {"secret-model"}},
	}
	before := cfg.CloneForRuntime()
	host := NewForTest(newTestSymbolLoader())
	t.Cleanup(host.ShutdownAll)

	if errApply := host.ApplyConfig(context.Background(), cfg); errApply != nil {
		t.Fatalf("ApplyConfig() error = %v", errApply)
	}
	if !reflect.DeepEqual(cfg, before) {
		t.Fatalf("ApplyConfig() mutated caller config\ngot:  %#v\nwant: %#v", cfg, before)
	}

	const iterations = 200
	var readers sync.WaitGroup
	var changed atomic.Bool
	readers.Add(1)
	go func() {
		defer readers.Done()
		for index := 0; index < iterations; index++ {
			summary := host.hostConfigSummary()
			if summary.ProxyURL != "http://proxy.invalid" || summary.AuthDir != "/private/auth" {
				changed.Store(true)
			}
		}
	}()
	for index := 0; index < iterations; index++ {
		cfg.ProxyURL = "http://caller-mutated.invalid"
		cfg.AuthDir = "/caller/mutated"
		cfg.OAuthExcludedModels["Codex"][0] = "caller-mutated-model"
	}
	readers.Wait()
	if changed.Load() {
		t.Fatal("host runtime config changed when caller mutated its config after ApplyConfig")
	}
}

func TestHostRequiredStartupFailureDoesNotPublishGlobalThinkingProviders(t *testing.T) {
	thinking.ClearPluginProviders()
	t.Cleanup(thinking.ClearPluginProviders)
	prior := passthroughThinkingApplier{}
	if !thinking.RegisterPluginProvider("prior-host", "prior-thinking", 1, prior) {
		t.Fatal("failed to register prior thinking provider")
	}

	pluginsDir := makePluginDir(t, "optional-policy")
	plugin := requiredTeardownPlugin()
	plugin.Metadata.Name = "optional-policy"
	client := newTestSymbolLookup(&testPlugin{registerResult: plugin})
	host := NewForTest(&sequencePluginLoader{clients: []pluginClient{client}})
	t.Cleanup(host.ShutdownAll)
	enabled := true
	cfg := &config.Config{Plugins: config.PluginsConfig{
		Enabled:           true,
		Dir:               pluginsDir,
		RequiredScheduler: "quota-policy",
		Configs: map[string]config.PluginInstanceConfig{
			"optional-policy": {Enabled: &enabled},
			"quota-policy":    {Enabled: &enabled},
		},
	}}

	if errApply := host.ApplyConfig(context.Background(), cfg); errApply == nil {
		t.Fatal("ApplyConfig() error = nil, want missing required scheduler failure")
	}
	if errTeardown := host.TeardownContext(context.Background()); errTeardown != nil {
		t.Fatalf("TeardownContext() after failed startup error = %v", errTeardown)
	}
	if thinking.GetProviderApplier("prior-thinking") == nil {
		t.Fatal("failed required startup cleanup replaced prior global thinking provider state")
	}
	if thinking.GetProviderApplier("teardown-thinking") != nil {
		t.Fatal("failed required startup published a plugin thinking provider")
	}
}

func requiredTeardownPlugin() pluginapi.Plugin {
	plugin := validTestPlugin("quota-policy")
	plugin.Capabilities.Scheduler = schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
		return pluginapi.SchedulerPickResponse{Handled: true, Reject: true}, nil
	})
	plugin.Capabilities.RequestInterceptor = requestInterceptorFunc(func(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
		return pluginapi.RequestInterceptResponse{}, nil
	})
	plugin.Capabilities.ThinkingApplier = testThinkingCapability{provider: "teardown-thinking"}
	return plugin
}

type teardownAccessProvider struct{ id string }

func (p teardownAccessProvider) Identifier() string { return p.id }
func (teardownAccessProvider) Authenticate(context.Context, *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	return nil, sdkaccess.NewNotHandledError()
}

type passthroughThinkingApplier struct{}

func (passthroughThinkingApplier) Apply(body []byte, _ thinking.ThinkingConfig, _ *registry.ModelInfo) ([]byte, error) {
	return append([]byte(nil), body...), nil
}
