package pluginhost

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestRegisterFrontendAuthProvidersSerializesWithTeardown(t *testing.T) {
	const key = "plugin:auth-race:barrier-auth"
	sdkaccess.UnregisterProvider(key)
	sdkaccess.ClearExclusiveProvider()
	defer sdkaccess.UnregisterProvider(key)
	defer sdkaccess.ClearExclusiveProvider()

	provider := &blockingFrontendAuthProvider{
		identifier: "barrier-auth",
		started:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	host := newHostWithRecords(capabilityRecord{
		id: "auth-race",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			FrontendAuthProvider:          provider,
			FrontendAuthProviderExclusive: true,
		}},
	})
	defer provider.unblock()

	registrationDone := make(chan struct{})
	go func() {
		host.RegisterFrontendAuthProviders()
		close(registrationDone)
	}()
	waitForHostTestSignal(t, provider.started, "frontend auth registration barrier")

	teardownDone := make(chan error, 1)
	go func() { teardownDone <- host.TeardownContext(context.Background()) }()
	select {
	case errTeardown := <-teardownDone:
		t.Fatalf("TeardownContext() returned while captured auth registration was blocked: %v", errTeardown)
	case <-time.After(50 * time.Millisecond):
	}

	provider.unblock()
	waitForHostTestSignal(t, registrationDone, "frontend auth registration")
	if errTeardown := waitForHostTestError(t, teardownDone, "teardown after auth registration"); errTeardown != nil {
		t.Fatalf("TeardownContext() error = %v", errTeardown)
	}
	if registeredProviderIdentifier(key) {
		t.Fatal("frontend auth provider was republished after teardown")
	}
	host.mu.Lock()
	ownedKeys := len(host.accessProviderKeys)
	ownedRegistrations := len(host.accessProviderRegistrations)
	host.mu.Unlock()
	if ownedKeys != 0 || ownedRegistrations != 0 {
		t.Fatalf("frontend auth ownership retained after teardown: keys=%d registrations=%d", ownedKeys, ownedRegistrations)
	}
}

func TestRegisterUsagePluginsSerializesWithTeardown(t *testing.T) {
	const name = "plugin:usage-race"
	coreusage.UnregisterNamedPlugin(name)
	defer coreusage.UnregisterNamedPlugin(name)

	host := newHostWithRecords(capabilityRecord{
		id: "usage-race",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			UsagePlugin: usagePluginFunc(func(context.Context, pluginapi.UsageRecord) {}),
		}},
	})

	host.mu.Lock()
	registrationDone := make(chan struct{})
	go func() {
		host.RegisterUsagePlugins()
		close(registrationDone)
	}()
	if !waitForApplyLock(host, 250*time.Millisecond) {
		host.mu.Unlock()
		waitForHostTestSignal(t, registrationDone, "unserialized usage registration")
		t.Fatal("RegisterUsagePlugins did not acquire the lifecycle lock")
	}
	teardownDone := make(chan error, 1)
	go func() { teardownDone <- host.TeardownContext(context.Background()) }()
	host.mu.Unlock()

	waitForHostTestSignal(t, registrationDone, "usage registration")
	if errTeardown := waitForHostTestError(t, teardownDone, "teardown after usage registration"); errTeardown != nil {
		t.Fatalf("TeardownContext() error = %v", errTeardown)
	}
	if plugin := coreusage.RegisteredNamedPlugin(name); plugin != nil {
		t.Fatalf("usage plugin remained globally registered after teardown: %#v", plugin)
	}
	host.mu.Lock()
	ownedNames := len(host.usagePluginNames)
	ownedRegistrations := len(host.usagePluginRegistrations)
	host.mu.Unlock()
	if ownedNames != 0 || ownedRegistrations != 0 {
		t.Fatalf("usage ownership retained after teardown: names=%d registrations=%d", ownedNames, ownedRegistrations)
	}
}

func TestHostTeardownRemovesOnlyExactGlobalRegistrations(t *testing.T) {
	const (
		pluginID     = "shared-global-owner"
		authKey      = "plugin:shared-global-owner:shared-auth"
		usageName    = "plugin:shared-global-owner"
		thinkingName = "shared-thinking-owner"
	)
	sdkaccess.UnregisterProvider(authKey)
	sdkaccess.ClearExclusiveProvider()
	coreusage.UnregisterNamedPlugin(usageName)
	thinking.ClearPluginProviders()
	defer sdkaccess.UnregisterProvider(authKey)
	defer sdkaccess.ClearExclusiveProvider()
	defer coreusage.UnregisterNamedPlugin(usageName)
	defer thinking.ClearPluginProviders()

	newRecord := func(tag string) capabilityRecord {
		return capabilityRecord{
			id: pluginID,
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				FrontendAuthProvider: frontendAuthProviderFunc{identifier: "shared-auth", authenticate: func(context.Context, pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
					return pluginapi.FrontendAuthResponse{Authenticated: true, Principal: tag}, nil
				}},
				FrontendAuthProviderExclusive: true,
				UsagePlugin:                   usagePluginFunc(func(context.Context, pluginapi.UsageRecord) {}),
				ThinkingApplier:               testThinkingCapability{provider: thinkingName},
			}},
		}
	}
	hostA := newHostWithRecords(newRecord("host-a"))
	hostB := newHostWithRecords(newRecord("host-b"))

	hostA.RegisterFrontendAuthProviders()
	hostA.RegisterUsagePlugins()
	hostA.refreshThinkingProviders(hostA.activeRecords())
	hostB.RegisterFrontendAuthProviders()
	hostB.RegisterUsagePlugins()
	hostB.refreshThinkingProviders(hostB.activeRecords())

	if errTeardown := hostA.TeardownContext(context.Background()); errTeardown != nil {
		t.Fatalf("host A TeardownContext() error = %v", errTeardown)
	}
	providers := sdkaccess.RegisteredProviders()
	if len(providers) != 1 {
		t.Fatalf("registered auth providers after host A teardown = %d, want 1", len(providers))
	}
	if adapter, ok := providers[0].(*accessAdapter); !ok || adapter.host != hostB {
		t.Fatalf("auth replacement after host A teardown = %#v, want host B adapter", providers[0])
	}
	if adapter, ok := coreusage.RegisteredNamedPlugin(usageName).(*usageAdapter); !ok || adapter.host != hostB {
		t.Fatalf("usage replacement after host A teardown = %#v, want host B adapter", coreusage.RegisteredNamedPlugin(usageName))
	}
	if adapter, ok := thinking.GetProviderApplier(thinkingName).(*thinkingAdapter); !ok || adapter.host != hostB {
		t.Fatalf("thinking replacement after host A teardown = %#v, want host B adapter", thinking.GetProviderApplier(thinkingName))
	}

	if errTeardown := hostB.TeardownContext(context.Background()); errTeardown != nil {
		t.Fatalf("host B TeardownContext() error = %v", errTeardown)
	}
	if registeredProviderIdentifier(authKey) {
		t.Fatal("host B auth provider remained after host B teardown")
	}
	if coreusage.RegisteredNamedPlugin(usageName) != nil {
		t.Fatal("host B usage plugin remained after host B teardown")
	}
	if thinking.GetProviderApplier(thinkingName) != nil {
		t.Fatal("host B thinking provider remained after host B teardown")
	}
}

func waitForApplyLock(host *Host, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(host.applyMu) == 1 {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return len(host.applyMu) == 1
}

type blockingFrontendAuthProvider struct {
	identifier  string
	started     chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
}

func (p *blockingFrontendAuthProvider) Identifier() string {
	p.startOnce.Do(func() { close(p.started) })
	<-p.release
	return p.identifier
}

func (*blockingFrontendAuthProvider) Authenticate(context.Context, pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
	return pluginapi.FrontendAuthResponse{}, nil
}

func (p *blockingFrontendAuthProvider) unblock() {
	p.releaseOnce.Do(func() { close(p.release) })
}

var _ sdkaccess.Provider = (*accessAdapter)(nil)
