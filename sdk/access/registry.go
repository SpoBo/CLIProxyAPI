package access

import (
	"context"
	"net/http"
	"strings"
	"sync"
)

// Provider validates credentials for incoming requests.
type Provider interface {
	Identifier() string
	Authenticate(ctx context.Context, r *http.Request) (*Result, *AuthError)
}

// Result conveys authentication outcome.
type Result struct {
	Provider  string
	Principal string
	Metadata  map[string]string
}

type providerRegistration struct {
	provider Provider
	owner    uint64
}

// ProviderRegistration identifies one exact provider installation.
type ProviderRegistration struct {
	typ   string
	owner uint64
}

// ExclusiveProviderRegistration identifies one exact exclusive-provider selection.
type ExclusiveProviderRegistration struct {
	owner uint64
}

var (
	registryMu        sync.RWMutex
	registry          = make(map[string]providerRegistration)
	order             []string
	exclusiveProvider string
	exclusiveOwner    uint64
	nextRegistration  uint64
)

// RegisterProvider registers a pre-built provider instance for a given type identifier.
func RegisterProvider(typ string, provider Provider) {
	RegisterProviderOwned(typ, provider)
}

// RegisterProviderOwned registers a provider and returns its exact installation identity.
func RegisterProviderOwned(typ string, provider Provider) ProviderRegistration {
	normalizedType := strings.TrimSpace(typ)
	if normalizedType == "" || provider == nil {
		return ProviderRegistration{}
	}

	registryMu.Lock()
	if _, exists := registry[normalizedType]; !exists {
		order = append(order, normalizedType)
	}
	nextRegistration++
	registration := ProviderRegistration{typ: normalizedType, owner: nextRegistration}
	registry[normalizedType] = providerRegistration{provider: provider, owner: registration.owner}
	registryMu.Unlock()
	return registration
}

// UnregisterProvider removes a provider by type identifier.
func UnregisterProvider(typ string) {
	normalizedType := strings.TrimSpace(typ)
	if normalizedType == "" {
		return
	}
	registryMu.Lock()
	if _, exists := registry[normalizedType]; !exists {
		registryMu.Unlock()
		return
	}
	delete(registry, normalizedType)
	removeProviderOrderLocked(normalizedType)
	registryMu.Unlock()
}

// UnregisterProviderOwned removes a provider only when registration is still current.
func UnregisterProviderOwned(registration ProviderRegistration) bool {
	if registration.typ == "" || registration.owner == 0 {
		return false
	}
	registryMu.Lock()
	current, exists := registry[registration.typ]
	if !exists || current.owner != registration.owner {
		registryMu.Unlock()
		return false
	}
	delete(registry, registration.typ)
	removeProviderOrderLocked(registration.typ)
	registryMu.Unlock()
	return true
}

func removeProviderOrderLocked(typ string) {
	for index := range order {
		if order[index] != typ {
			continue
		}
		order = append(order[:index], order[index+1:]...)
		break
	}
}

// SetExclusiveProvider restricts RegisteredProviders to a single provider key when present.
func SetExclusiveProvider(typ string) {
	normalizedType := strings.TrimSpace(typ)
	registryMu.Lock()
	nextRegistration++
	exclusiveProvider = normalizedType
	exclusiveOwner = nextRegistration
	registryMu.Unlock()
}

// SetExclusiveProviderOwned selects the provider only when its exact installation is still current.
func SetExclusiveProviderOwned(providerRegistration ProviderRegistration) ExclusiveProviderRegistration {
	if providerRegistration.typ == "" || providerRegistration.owner == 0 {
		return ExclusiveProviderRegistration{}
	}
	registryMu.Lock()
	current, exists := registry[providerRegistration.typ]
	if !exists || current.owner != providerRegistration.owner {
		registryMu.Unlock()
		return ExclusiveProviderRegistration{}
	}
	nextRegistration++
	exclusiveProvider = providerRegistration.typ
	exclusiveOwner = nextRegistration
	registration := ExclusiveProviderRegistration{owner: exclusiveOwner}
	registryMu.Unlock()
	return registration
}

// ClearExclusiveProvider removes any active provider restriction.
func ClearExclusiveProvider() {
	registryMu.Lock()
	exclusiveProvider = ""
	exclusiveOwner = 0
	registryMu.Unlock()
}

// ClearExclusiveProviderOwned clears the exclusive selection only when it is still current.
func ClearExclusiveProviderOwned(registration ExclusiveProviderRegistration) bool {
	if registration.owner == 0 {
		return false
	}
	registryMu.Lock()
	if exclusiveOwner != registration.owner {
		registryMu.Unlock()
		return false
	}
	exclusiveProvider = ""
	exclusiveOwner = 0
	registryMu.Unlock()
	return true
}

// RegisteredProviders returns the global provider instances in registration order.
func RegisteredProviders() []Provider {
	registryMu.RLock()
	if len(order) == 0 {
		registryMu.RUnlock()
		return nil
	}
	if exclusiveProvider != "" {
		if registration, exists := registry[exclusiveProvider]; exists && registration.provider != nil {
			registryMu.RUnlock()
			return []Provider{registration.provider}
		}
	}
	providers := make([]Provider, 0, len(order))
	for _, providerType := range order {
		registration, exists := registry[providerType]
		if !exists || registration.provider == nil {
			continue
		}
		providers = append(providers, registration.provider)
	}
	registryMu.RUnlock()
	return providers
}
