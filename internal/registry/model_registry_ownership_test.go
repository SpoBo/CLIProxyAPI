package registry

import "testing"

func TestModelClientRegistrationOwnershipPreventsStaleRemoval(t *testing.T) {
	r := newTestModelRegistry()

	first := r.RegisterClientOwned("shared-client", "provider-a", []*ModelInfo{{ID: "model-a"}})
	second := r.RegisterClientOwned("shared-client", "provider-b", []*ModelInfo{{ID: "model-b"}})

	if r.UnregisterClientOwned(first) {
		t.Fatal("stale owner removed replacement registration")
	}
	if r.ClientSupportsModel("shared-client", "model-a") {
		t.Fatal("replacement retained first owner's model")
	}
	if !r.ClientSupportsModel("shared-client", "model-b") {
		t.Fatal("replacement model was removed by stale owner")
	}
	if !r.UnregisterClientOwned(second) {
		t.Fatal("current owner could not remove its registration")
	}
	if r.ClientSupportsModel("shared-client", "model-b") {
		t.Fatal("current owner's model remained registered")
	}
}
