package thinking

import (
	"bytes"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestRegisterPluginProviderPreservesHigherPriorityReplacementBehavior(t *testing.T) {
	const provider = "legacy-thinking-priority-test"
	ClearPluginProviders()
	defer ClearPluginProviders()

	if !RegisterPluginProvider("shared-plugin", provider, 1, taggedThinkingApplier("first")) {
		t.Fatal("first legacy registration failed")
	}
	if !RegisterPluginProvider("shared-plugin", provider, 2, taggedThinkingApplier("second")) {
		t.Fatal("higher-priority legacy replacement failed")
	}
	applier := GetProviderApplier(provider)
	got, errApply := applier.Apply(nil, ThinkingConfig{}, nil)
	if errApply != nil || !bytes.Equal(got, []byte("second")) {
		t.Fatalf("replacement Apply() = %q, %v; want second", got, errApply)
	}
}

func TestOwnedPluginProviderDoesNotRemoveReplacement(t *testing.T) {
	const provider = "owned-thinking-test"
	ClearPluginProviders()
	defer ClearPluginProviders()

	first := taggedThinkingApplier("first")
	second := taggedThinkingApplier("second")
	firstRegistration, ok := RegisterPluginProviderOwned("shared-plugin", provider, 1, first)
	if !ok {
		t.Fatal("first owned registration failed")
	}
	secondRegistration, ok := RegisterPluginProviderOwned("shared-plugin", provider, 1, second)
	if !ok {
		t.Fatal("replacement owned registration failed")
	}
	if UnregisterPluginProviderOwned(firstRegistration) {
		t.Fatal("stale thinking registration removed its replacement")
	}
	applier := GetProviderApplier(provider)
	if applier == nil {
		t.Fatal("replacement thinking provider was removed")
	}
	got, errApply := applier.Apply(nil, ThinkingConfig{}, nil)
	if errApply != nil || !bytes.Equal(got, []byte("second")) {
		t.Fatalf("replacement Apply() = %q, %v; want second", got, errApply)
	}
	if !UnregisterPluginProviderOwned(secondRegistration) {
		t.Fatal("current thinking registration was not removed")
	}
	if GetProviderApplier(provider) != nil {
		t.Fatal("thinking provider remained after current owner removal")
	}
}

type taggedThinkingApplier string

func (a taggedThinkingApplier) Apply([]byte, ThinkingConfig, *registry.ModelInfo) ([]byte, error) {
	return []byte(a), nil
}
