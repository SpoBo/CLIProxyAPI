package auth

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type schedulerStorageSentinel struct {
	Marker string
}

func (schedulerStorageSentinel) SaveTokenToFile(string) error { return nil }

func TestSchedulerAuthCandidatesExposeOnlySafeQuotaObservations(t *testing.T) {
	authObservedAt := time.Unix(1_800_000_000, 0).UTC()
	modelObservedAt := authObservedAt.Add(time.Minute)
	const stableAuthID = "codex-owner@example.com.json"
	auth := &Auth{
		ID:       stableAuthID,
		Provider: " Codex ",
		FileName: "/credentials/account.json",
		ProxyURL: "http://user:password@proxy.invalid",
		Attributes: map[string]string{
			"region":       "us-east",
			"email":        "private@example.com",
			"path":         "/credentials/account.json",
			"access_token": "secret-token",
		},
		Metadata: map[string]any{
			"access_token": "secret-token",
			"email":        "private@example.com",
			"storage":      map[string]any{"path": "/credentials/account.json"},
		},
		Quota: QuotaState{
			ObservedAt: authObservedAt,
			Signals: map[string]string{
				"X-Codex-Primary-Used-Percent": "72",
				"X-Codex-Primary-Reset-At":     "1800000300",
				"Authorization":                "Bearer secret-token",
			},
		},
		ModelStates: map[string]*ModelState{
			"gpt-5.4": {
				Quota: QuotaState{
					ObservedAt: modelObservedAt,
					Signals: map[string]string{
						"X-Codex-Primary-Used-Percent": "83",
					},
				},
			},
		},
	}

	candidates := schedulerAuthCandidates([]*Auth{auth}, "gpt-5.4")
	if len(candidates) != 1 {
		t.Fatalf("schedulerAuthCandidates() len = %d, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.ID != stableAuthID {
		t.Fatalf("candidate.ID = %q, want stable auth identifier %q", candidate.ID, stableAuthID)
	}
	if candidate.Provider != "codex" {
		t.Fatalf("candidate.Provider = %q, want codex", candidate.Provider)
	}
	wantAuthSignals := map[string]string{
		"X-Codex-Primary-Used-Percent": "72",
		"X-Codex-Primary-Reset-At":     "1800000300",
	}
	if !candidate.Quota.ObservedAt.Equal(authObservedAt) || !reflect.DeepEqual(candidate.Quota.Signals, wantAuthSignals) {
		t.Fatalf("candidate.Quota = %#v, want observed safe auth quota", candidate.Quota)
	}
	if candidate.ModelQuota == nil || !candidate.ModelQuota.ObservedAt.Equal(modelObservedAt) || !reflect.DeepEqual(candidate.ModelQuota.Signals, auth.ModelStates["gpt-5.4"].Quota.Signals) {
		t.Fatalf("candidate.ModelQuota = %#v, want requested-model quota", candidate.ModelQuota)
	}
	if candidate.Attributes != nil {
		t.Fatalf("candidate.Attributes = %#v, want nil", candidate.Attributes)
	}
	if candidate.Metadata != nil {
		t.Fatalf("candidate.Metadata = %#v, want nil", candidate.Metadata)
	}
	rawCandidate, errMarshal := json.Marshal(candidate)
	if errMarshal != nil {
		t.Fatalf("json.Marshal(candidate) error = %v", errMarshal)
	}
	for _, sensitive := range []string{"secret-token", "private@example.com", "/credentials/account.json", "user:password"} {
		if strings.Contains(string(rawCandidate), sensitive) {
			t.Fatalf("serialized scheduler candidate exposed sensitive value %q: %s", sensitive, rawCandidate)
		}
	}

	candidate.Quota.Signals["X-Codex-Primary-Used-Percent"] = "mutated"
	candidate.ModelQuota.Signals["X-Codex-Primary-Used-Percent"] = "mutated"
	if auth.Quota.Signals["X-Codex-Primary-Used-Percent"] != "72" ||
		auth.ModelStates["gpt-5.4"].Quota.Signals["X-Codex-Primary-Used-Percent"] != "83" ||
		auth.Attributes["region"] != "us-east" {
		t.Fatal("scheduler candidate mutation reached host auth state")
	}
}

func TestSchedulerAuthCandidatesExcludePluginVirtualSourceAndStorage(t *testing.T) {
	const (
		sourcePath         = "/srv/cliproxy/private/provider-accounts.json"
		storageMarker      = "scheduler-storage-sentinel"
		hostInternalMarker = "scheduler-host-internal-sentinel"
	)
	auth := &Auth{
		ID:       "plugin-auth-1",
		Provider: "plugin-provider",
		FileName: sourcePath,
		Storage:  schedulerStorageSentinel{Marker: storageMarker},
		Attributes: map[string]string{
			"region":           "us-east",
			"host_internal_id": hostInternalMarker,
		},
	}
	MarkPluginVirtualAuth(auth, sourcePath, 3)

	candidates := schedulerAuthCandidates([]*Auth{auth}, "test-model")
	if len(candidates) != 1 {
		t.Fatalf("schedulerAuthCandidates() len = %d, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.Attributes != nil {
		t.Fatalf("candidate.Attributes = %#v, want nil", candidate.Attributes)
	}
	for _, key := range []string{AttributeAuthIndexSeed, AttributePluginVirtual, AttributeVirtualSource, "host_internal_id"} {
		if _, ok := candidate.Attributes[key]; ok {
			t.Fatalf("candidate.Attributes contains host-private key %q", key)
		}
	}

	rawCandidate, errMarshal := json.Marshal(candidate)
	if errMarshal != nil {
		t.Fatalf("json.Marshal(candidate) error = %v", errMarshal)
	}
	for _, privateValue := range []string{sourcePath, "provider-accounts.json", storageMarker, hostInternalMarker} {
		if strings.Contains(string(rawCandidate), privateValue) {
			t.Fatalf("serialized scheduler candidate exposed host-private value %q: %s", privateValue, rawCandidate)
		}
	}
}

func TestSchedulerAuthCandidatesExcludeEveryPreviouslyAllowlistedAttributeValue(t *testing.T) {
	markers := map[string]string{
		"priority":      "token-priority-secret",
		AttributeWeight: "owner@example.com",
		"region":        "/srv/private/credentials.json",
		"team":          "scheduler-storage-sentinel",
	}
	auth := &Auth{
		ID:         "auth-sensitive-attributes",
		Provider:   "codex",
		Attributes: markers,
	}

	candidates := schedulerAuthCandidates([]*Auth{auth}, "gpt-5.4")
	if len(candidates) != 1 {
		t.Fatalf("schedulerAuthCandidates() len = %d, want 1", len(candidates))
	}
	candidate := candidates[0]
	if candidate.Attributes != nil {
		t.Fatalf("candidate.Attributes = %#v, want nil", candidate.Attributes)
	}
	rawCandidate, errMarshal := json.Marshal(candidate)
	if errMarshal != nil {
		t.Fatalf("json.Marshal(candidate) error = %v", errMarshal)
	}
	for key, marker := range markers {
		if strings.Contains(string(rawCandidate), marker) {
			t.Fatalf("serialized scheduler candidate exposed %s marker %q: %s", key, marker, rawCandidate)
		}
	}
}

func TestSchedulerAuthCandidatesExposePriorityAndWeightAsTypedFields(t *testing.T) {
	auth := &Auth{
		ID:       "auth-routing-fields",
		Provider: "codex",
		Attributes: map[string]string{
			"priority":      "17",
			AttributeWeight: "23",
		},
	}

	candidate := schedulerAuthCandidates([]*Auth{auth}, "gpt-5.4")[0]
	if candidate.Priority != 17 || candidate.Weight != 23 {
		t.Fatalf("candidate priority/weight = %d/%d, want 17/23", candidate.Priority, candidate.Weight)
	}
	if candidate.Attributes != nil {
		t.Fatalf("candidate.Attributes = %#v, want nil", candidate.Attributes)
	}
}

func TestSchedulerAuthCandidatesEmitNonNullQuotaSignals(t *testing.T) {
	candidate := schedulerAuthCandidates([]*Auth{{ID: "auth-empty-quota", Provider: "claude"}}, "claude-opus")[0]
	if candidate.Quota.Signals == nil {
		t.Fatal("candidate.Quota.Signals is nil; strict scheduler wire decoders reject null")
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		t.Fatalf("json.Marshal(candidate) error = %v", err)
	}
	if strings.Contains(string(raw), `"Signals":null`) {
		t.Fatalf("serialized scheduler candidate contains null quota signals: %s", raw)
	}
}
