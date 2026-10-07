package usage

import (
	"context"
	"testing"
)

func TestStreamFromContextDefaultsMissingToFalse(t *testing.T) {
	if StreamFromContext(context.Background()) {
		t.Fatalf("StreamFromContext(background) = true, want false")
	}
}

func TestStreamFromContextHonorsExplicitTrue(t *testing.T) {
	ctx := WithStream(context.Background(), true)
	if !StreamFromContext(ctx) {
		t.Fatalf("StreamFromContext(true) = false, want true")
	}
}

func TestRecordStreamField(t *testing.T) {
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
		Stream:   true,
	}
	if !record.Stream {
		t.Fatalf("Record.Stream = false, want true")
	}
}

func TestRecordBaseURLField(t *testing.T) {
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
		BaseURL:  "https://custom-gateway.example.com/v1",
	}
	if record.BaseURL != "https://custom-gateway.example.com/v1" {
		t.Fatalf("Record.BaseURL = %q, want %q", record.BaseURL, "https://custom-gateway.example.com/v1")
	}
}

func TestGenerateEnabledDefaultsNilToTrue(t *testing.T) {
	if !GenerateEnabled(nil) {
		t.Fatalf("GenerateEnabled(nil) = false, want true")
	}
}

func TestGenerateEnabledHonorsExplicitFalse(t *testing.T) {
	if GenerateEnabled(GenerateFlag(false)) {
		t.Fatalf("GenerateEnabled(false) = true, want false")
	}
}

func TestGenerateEnabledHonorsExplicitTrue(t *testing.T) {
	if !GenerateEnabled(GenerateFlag(true)) {
		t.Fatalf("GenerateEnabled(true) = false, want true")
	}
}

func TestGenerateFromContextDefaultsMissingToTrue(t *testing.T) {
	if !GenerateFromContext(context.Background()) {
		t.Fatalf("GenerateFromContext(background) = false, want true")
	}
}

func TestGenerateFromContextHonorsExplicitFalse(t *testing.T) {
	ctx := WithGenerate(context.Background(), false)
	if GenerateFromContext(ctx) {
		t.Fatalf("GenerateFromContext(false) = true, want false")
	}
}

func TestRecordOmittedGenerateIsEnabled(t *testing.T) {
	// Existing callers construct Record without setting Generate.
	// Omission must remain distinguishable from explicit false and default to true.
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
	}
	if record.Generate != nil {
		t.Fatalf("Record.Generate = %v, want nil for omitted field", record.Generate)
	}
	if !GenerateEnabled(record.Generate) {
		t.Fatalf("GenerateEnabled(omitted) = false, want true")
	}
}

func TestManagerOwnedNamedRegistrationDoesNotRemoveReplacement(t *testing.T) {
	manager := NewManager(1)
	first := &usagePluginStub{}
	second := &usagePluginStub{}
	firstRegistration := manager.RegisterNamedOwned("shared", first)
	secondRegistration := manager.RegisterNamedOwned("shared", second)

	if manager.UnregisterNamedOwned(firstRegistration) {
		t.Fatal("stale named registration removed its replacement")
	}
	if got := manager.NamedPlugin("shared"); got != second {
		t.Fatalf("NamedPlugin(shared) = %#v, want replacement", got)
	}
	if !manager.UnregisterNamedOwned(secondRegistration) {
		t.Fatal("current named registration was not removed")
	}
	if got := manager.NamedPlugin("shared"); got != nil {
		t.Fatalf("NamedPlugin(shared) = %#v, want nil", got)
	}
}

func TestManagerUnregisterNamedRemovesOnlyTargetAndRepairsIndexes(t *testing.T) {
	manager := NewManager(1)
	unnamed := &usagePluginStub{}
	first := &usagePluginStub{}
	second := &usagePluginStub{}
	manager.Register(unnamed)
	manager.RegisterNamed("first", first)
	manager.RegisterNamed("second", second)

	manager.UnregisterNamed("first")
	if len(manager.plugins) != 2 || manager.plugins[0] != unnamed || manager.plugins[1] != second {
		t.Fatalf("plugins after first removal = %#v, want unnamed and second", manager.plugins)
	}
	if _, exists := manager.named["first"]; exists {
		t.Fatal("removed named plugin retained its index")
	}
	if index := manager.named["second"]; index != 1 {
		t.Fatalf("second named plugin index = %d, want 1", index)
	}

	manager.UnregisterNamed("second")
	if len(manager.plugins) != 1 || manager.plugins[0] != unnamed || len(manager.named) != 0 {
		t.Fatalf("plugins after second removal = %#v, named = %#v", manager.plugins, manager.named)
	}
}

type usagePluginStub struct{}

func (*usagePluginStub) HandleUsage(context.Context, Record) {}
