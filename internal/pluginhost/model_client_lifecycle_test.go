package pluginhost

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestRegisterModelsSerializesBlockedPublicationWithTeardown(t *testing.T) {
	models := newBlockingModelRegistry()
	host := newHostWithRecords(capabilityRecord{
		id: "stale-model-client",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelRegistrar: staticModelRegistrar("shared-provider", "shared-model"),
		}},
	})

	registrationDone := make(chan struct{})
	go func() {
		host.RegisterModels(context.Background(), models)
		close(registrationDone)
	}()
	waitForHostTestSignal(t, models.registerStarted, "blocked model publication")

	teardownDone := make(chan error, 1)
	go func() { teardownDone <- host.TeardownContext(context.Background()) }()
	returnedEarly := false
	select {
	case <-teardownDone:
		returnedEarly = true
	case <-time.After(50 * time.Millisecond):
	}
	models.unblock()
	waitForHostTestSignal(t, registrationDone, "model publication")
	if !returnedEarly {
		if err := waitForHostTestError(t, teardownDone, "teardown after model publication"); err != nil {
			t.Fatalf("TeardownContext() error = %v", err)
		}
	}
	if returnedEarly {
		t.Fatal("TeardownContext returned while model publication was blocked")
	}
	if models.hasClient("plugin:stale-model-client:shared-provider") {
		t.Fatal("blocked model publication republished after teardown")
	}
}

func TestRegisterExecutorsSerializesBlockedModelPublicationWithTeardown(t *testing.T) {
	models := newBlockingModelRegistry()
	manager := newFakeExecutorManager()
	host := newHostWithRecords(capabilityRecord{
		id: "stale-executor-client",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ModelRegistrar: staticModelRegistrar("shared-provider", "shared-model"),
			Executor:       &fakeExecutor{identifier: "shared-provider"},
		}},
	})
	host.RegisterModels(context.Background(), models)

	registrationDone := make(chan struct{})
	go func() {
		host.RegisterExecutors(manager, models)
		close(registrationDone)
	}()
	waitForHostTestSignal(t, models.registerStarted, "blocked executor model publication")

	teardownDone := make(chan error, 1)
	go func() { teardownDone <- host.TeardownContext(context.Background()) }()
	returnedEarly := false
	select {
	case <-teardownDone:
		returnedEarly = true
	case <-time.After(50 * time.Millisecond):
	}
	models.unblock()
	waitForHostTestSignal(t, registrationDone, "executor model publication")
	if !returnedEarly {
		if err := waitForHostTestError(t, teardownDone, "teardown after executor model publication"); err != nil {
			t.Fatalf("TeardownContext() error = %v", err)
		}
	}
	if returnedEarly {
		t.Fatal("TeardownContext returned while executor model publication was blocked")
	}
	if models.hasClient(pluginExecutorModelClientID("stale-executor-client", "shared-provider")) {
		t.Fatal("blocked executor model publication republished after teardown")
	}
}

func TestHostTeardownPreservesReplacementModelClient(t *testing.T) {
	models := newFakeModelRegistry()
	newRecord := func(model string) capabilityRecord {
		return capabilityRecord{
			id: "shared-model-owner",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelRegistrar: staticModelRegistrar("shared-provider", model),
			}},
		}
	}
	hostA := newHostWithRecords(newRecord("model-a"))
	hostB := newHostWithRecords(newRecord("model-b"))
	hostA.RegisterModels(context.Background(), models)
	hostB.RegisterModels(context.Background(), models)
	clientID := "plugin:shared-model-owner:shared-provider"

	if err := hostA.TeardownContext(context.Background()); err != nil {
		t.Fatalf("host A TeardownContext() error = %v", err)
	}
	if client := models.clients[clientID]; client == nil || len(client.models) != 1 || client.models[0].ID != "model-b" {
		t.Fatalf("host A teardown removed host B model client: %#v", client)
	}
	if err := hostB.TeardownContext(context.Background()); err != nil {
		t.Fatalf("host B TeardownContext() error = %v", err)
	}
	if models.clients[clientID] != nil {
		t.Fatal("host B model client remained after host B teardown")
	}
}

func TestHostTeardownPreservesReplacementExecutorModelClient(t *testing.T) {
	models := newFakeModelRegistry()
	newRecord := func(model string) capabilityRecord {
		return capabilityRecord{
			id: "shared-executor-owner",
			plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				ModelRegistrar: staticModelRegistrar("shared-provider", model),
				Executor:       &fakeExecutor{identifier: "shared-provider"},
			}},
		}
	}
	hostA := newHostWithRecords(newRecord("model-a"))
	hostB := newHostWithRecords(newRecord("model-b"))
	hostA.RegisterModels(context.Background(), models)
	hostB.RegisterModels(context.Background(), models)
	hostA.RegisterExecutors(newFakeExecutorManager(), models)
	hostB.RegisterExecutors(newFakeExecutorManager(), models)
	clientID := pluginExecutorModelClientID("shared-executor-owner", "shared-provider")

	if err := hostA.TeardownContext(context.Background()); err != nil {
		t.Fatalf("host A TeardownContext() error = %v", err)
	}
	if client := models.clients[clientID]; client == nil || len(client.models) != 1 || client.models[0].ID != "model-b" {
		t.Fatalf("host A teardown removed host B executor model client: %#v", client)
	}
	if err := hostB.TeardownContext(context.Background()); err != nil {
		t.Fatalf("host B TeardownContext() error = %v", err)
	}
	if models.clients[clientID] != nil {
		t.Fatal("host B executor model client remained after host B teardown")
	}
}

type blockingModelRegistry struct {
	mu              sync.Mutex
	clients         map[string]*fakeModelClient
	registerStarted chan struct{}
	release         chan struct{}
	startOnce       sync.Once
	releaseOnce     sync.Once
	nextOwner       uint64
}

func newBlockingModelRegistry() *blockingModelRegistry {
	return &blockingModelRegistry{
		clients:         make(map[string]*fakeModelClient),
		registerStarted: make(chan struct{}),
		release:         make(chan struct{}),
	}
}

func (r *blockingModelRegistry) RegisterClient(clientID, provider string, models []*registry.ModelInfo) {
	_ = r.RegisterClientOwned(clientID, provider, models)
}

func (r *blockingModelRegistry) RegisterClientOwned(clientID, provider string, models []*registry.ModelInfo) registry.ModelClientRegistration {
	r.startOnce.Do(func() { close(r.registerStarted) })
	<-r.release
	r.mu.Lock()
	r.nextOwner++
	owner := registry.ModelClientRegistration{ClientID: clientID, OwnerID: r.nextOwner}
	r.clients[clientID] = &fakeModelClient{provider: provider, models: models, owner: owner}
	r.mu.Unlock()
	return owner
}

func (r *blockingModelRegistry) UnregisterClient(clientID string) {
	r.mu.Lock()
	delete(r.clients, clientID)
	r.mu.Unlock()
}

func (r *blockingModelRegistry) UnregisterClientOwned(owner registry.ModelClientRegistration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	client := r.clients[owner.ClientID]
	if client == nil || client.owner != owner {
		return false
	}
	delete(r.clients, owner.ClientID)
	return true
}

func (r *blockingModelRegistry) GetModelProviders(modelID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	providers := make([]string, 0)
	for _, client := range r.clients {
		for _, model := range client.models {
			if model != nil && model.ID == modelID {
				providers = append(providers, client.provider)
				break
			}
		}
	}
	sort.Strings(providers)
	return providers
}

func (r *blockingModelRegistry) hasClient(clientID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.clients[clientID] != nil
}

func (r *blockingModelRegistry) unblock() {
	r.releaseOnce.Do(func() { close(r.release) })
}
