package access

import (
	"context"
	"net/http"
	"testing"
)

type testProvider struct {
	id string
}

func (p testProvider) Identifier() string {
	return p.id
}

func (p testProvider) Authenticate(context.Context, *http.Request) (*Result, *AuthError) {
	return &Result{Provider: p.id, Principal: p.id}, nil
}

func TestOwnedProviderRegistrationsDoNotRemoveReplacement(t *testing.T) {
	const key = "owned-test"
	UnregisterProvider(key)
	ClearExclusiveProvider()
	defer UnregisterProvider(key)
	defer ClearExclusiveProvider()

	first := testProvider{id: "first"}
	second := testProvider{id: "second"}
	firstRegistration := RegisterProviderOwned(key, first)
	firstExclusive := SetExclusiveProviderOwned(firstRegistration)
	secondRegistration := RegisterProviderOwned(key, second)
	if staleExclusive := SetExclusiveProviderOwned(firstRegistration); ClearExclusiveProviderOwned(staleExclusive) {
		t.Fatal("stale provider registration claimed the exclusive slot")
	}
	secondExclusive := SetExclusiveProviderOwned(secondRegistration)

	if UnregisterProviderOwned(firstRegistration) {
		t.Fatal("stale provider registration removed its replacement")
	}
	if ClearExclusiveProviderOwned(firstExclusive) {
		t.Fatal("stale exclusive registration cleared its replacement")
	}
	providers := RegisteredProviders()
	if len(providers) != 1 || providers[0].Identifier() != "second" {
		t.Fatalf("registered providers = %#v, want second replacement", providers)
	}
	if !ClearExclusiveProviderOwned(secondExclusive) {
		t.Fatal("current exclusive registration was not cleared")
	}
	if !UnregisterProviderOwned(secondRegistration) {
		t.Fatal("current provider registration was not removed")
	}
}

func TestRegisteredProvidersReturnsOnlyExclusiveProvider(t *testing.T) {
	UnregisterProvider("test-a")
	UnregisterProvider("test-b")
	ClearExclusiveProvider()
	defer UnregisterProvider("test-a")
	defer UnregisterProvider("test-b")
	defer ClearExclusiveProvider()

	RegisterProvider("test-a", testProvider{id: "test-a"})
	RegisterProvider("test-b", testProvider{id: "test-b"})
	SetExclusiveProvider("test-b")

	providers := RegisteredProviders()
	if len(providers) != 1 {
		t.Fatalf("RegisteredProviders() len = %d, want 1", len(providers))
	}
	if providers[0].Identifier() != "test-b" {
		t.Fatalf("RegisteredProviders()[0] = %q, want test-b", providers[0].Identifier())
	}
}

func TestRegisteredProvidersRestoresAllProvidersAfterExclusiveCleared(t *testing.T) {
	UnregisterProvider("test-a")
	UnregisterProvider("test-b")
	ClearExclusiveProvider()
	defer UnregisterProvider("test-a")
	defer UnregisterProvider("test-b")
	defer ClearExclusiveProvider()

	RegisterProvider("test-a", testProvider{id: "test-a"})
	RegisterProvider("test-b", testProvider{id: "test-b"})
	SetExclusiveProvider("test-b")
	ClearExclusiveProvider()

	providers := RegisteredProviders()
	if len(providers) != 2 {
		t.Fatalf("RegisteredProviders() len = %d, want 2", len(providers))
	}
	if providers[0].Identifier() != "test-a" || providers[1].Identifier() != "test-b" {
		t.Fatalf("RegisteredProviders() = [%q, %q], want [test-a, test-b]", providers[0].Identifier(), providers[1].Identifier())
	}
}

func TestRegisteredProvidersIgnoresStaleExclusiveProvider(t *testing.T) {
	UnregisterProvider("test-a")
	UnregisterProvider("missing")
	ClearExclusiveProvider()
	defer UnregisterProvider("test-a")
	defer ClearExclusiveProvider()

	RegisterProvider("test-a", testProvider{id: "test-a"})
	SetExclusiveProvider("missing")

	providers := RegisteredProviders()
	if len(providers) != 1 {
		t.Fatalf("RegisteredProviders() len = %d, want 1", len(providers))
	}
	if providers[0].Identifier() != "test-a" {
		t.Fatalf("RegisteredProviders()[0] = %q, want test-a", providers[0].Identifier())
	}
}
