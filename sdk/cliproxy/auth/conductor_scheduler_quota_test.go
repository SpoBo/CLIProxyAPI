package auth

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSchedulerAuthCandidatesExposeOnlySafeQuotaObservations(t *testing.T) {
	authObservedAt := time.Unix(1_800_000_000, 0).UTC()
	modelObservedAt := authObservedAt.Add(time.Minute)
	auth := &Auth{
		ID:       "auth-1",
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
	if !reflect.DeepEqual(candidate.Attributes, map[string]string{"region": "us-east"}) {
		t.Fatalf("candidate.Attributes = %#v, want only safe routing attributes", candidate.Attributes)
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
	candidate.Attributes["region"] = "mutated"
	if auth.Quota.Signals["X-Codex-Primary-Used-Percent"] != "72" ||
		auth.ModelStates["gpt-5.4"].Quota.Signals["X-Codex-Primary-Used-Percent"] != "83" ||
		auth.Attributes["region"] != "us-east" {
		t.Fatal("scheduler candidate mutation reached host auth state")
	}
}
