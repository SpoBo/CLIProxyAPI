package pluginhost

import (
	"context"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestManagerSelectAuthPluginAuthIDMustResolveExactly(t *testing.T) {
	tests := []struct {
		name       string
		offeredID  string
		responseID string
	}{
		{name: "padded offered id", offeredID: " allowed ", responseID: " allowed "},
		{name: "mismatched id", offeredID: "allowed", responseID: "missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, fixture := managerWithOptionalScheduler(t, func(req pluginapi.SchedulerPickRequest) pluginapi.SchedulerPickResponse {
				assertSchedulerCandidates(t, req, test.offeredID, "unauthorized-native")
				return pluginapi.SchedulerPickResponse{Handled: true, AuthID: test.responseID}
			}, test.offeredID)

			selected, errSelect := manager.SelectAuth(context.Background(), "restart-boundary-provider", "", cliproxyexecutor.Options{})
			if errSelect == nil {
				t.Fatalf("SelectAuth() = %#v, nil; want invalid scheduler selection error", selected)
			}
			if selected != nil {
				t.Fatalf("SelectAuth() selected %#v; unauthorized native fallback must remain blocked", selected)
			}
			if fixture.calls != 1 {
				t.Fatalf("scheduler calls = %d, want 1", fixture.calls)
			}
		})
	}
}

func TestManagerSelectAuthPluginExactIDStillSucceeds(t *testing.T) {
	manager, _ := managerWithOptionalScheduler(t, func(req pluginapi.SchedulerPickRequest) pluginapi.SchedulerPickResponse {
		assertSchedulerCandidates(t, req, "allowed", "unauthorized-native")
		return pluginapi.SchedulerPickResponse{Handled: true, AuthID: "allowed"}
	}, "allowed")

	selected, errSelect := manager.SelectAuth(context.Background(), "restart-boundary-provider", "", cliproxyexecutor.Options{})
	if errSelect != nil || selected == nil || selected.ID != "allowed" {
		t.Fatalf("SelectAuth() = %#v, %v; want exact plugin-selected credential", selected, errSelect)
	}
}

func TestManagerSelectAuthOptionalSchedulerUnhandledStillUsesNativeFallback(t *testing.T) {
	manager, _ := managerWithOptionalScheduler(t, func(req pluginapi.SchedulerPickRequest) pluginapi.SchedulerPickResponse {
		assertSchedulerCandidates(t, req, "allowed", "unauthorized-native")
		return pluginapi.SchedulerPickResponse{Handled: false}
	}, "allowed")

	selected, errSelect := manager.SelectAuth(context.Background(), "restart-boundary-provider", "", cliproxyexecutor.Options{})
	if errSelect != nil || selected == nil || selected.ID != "unauthorized-native" {
		t.Fatalf("SelectAuth() = %#v, %v; want unchanged native fallback", selected, errSelect)
	}
}

type managerSchedulerFixture struct {
	calls int
	pick  func(pluginapi.SchedulerPickRequest) pluginapi.SchedulerPickResponse
}

func (fixture *managerSchedulerFixture) Pick(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
	fixture.calls++
	return fixture.pick(req), nil
}

func managerWithOptionalScheduler(t *testing.T, pick func(pluginapi.SchedulerPickRequest) pluginapi.SchedulerPickResponse, offeredID string) (*coreauth.Manager, *managerSchedulerFixture) {
	t.Helper()
	fixture := &managerSchedulerFixture{pick: pick}
	host := newHostWithRecords(capabilityRecord{
		id: "native-scheduler-fixture",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			Scheduler:                 fixture,
			SchedulerAcrossPriorities: true,
		}},
	})
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(restartBoundaryTestExecutor{})
	for _, auth := range []*coreauth.Auth{
		{ID: offeredID, Provider: "restart-boundary-provider", Status: coreauth.StatusActive, Attributes: map[string]string{"priority": "1"}},
		{ID: "unauthorized-native", Provider: "restart-boundary-provider", Status: coreauth.StatusActive, Attributes: map[string]string{"priority": "100"}},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%q) error = %v", auth.ID, errRegister)
		}
	}
	manager.SetPluginScheduler(host)
	return manager, fixture
}

func assertSchedulerCandidates(t *testing.T, req pluginapi.SchedulerPickRequest, wantIDs ...string) {
	t.Helper()
	seen := make(map[string]bool, len(req.Candidates))
	for _, candidate := range req.Candidates {
		seen[candidate.ID] = true
	}
	for _, wantID := range wantIDs {
		if !seen[wantID] {
			t.Fatalf("scheduler candidates = %#v, missing exact ID %q", req.Candidates, wantID)
		}
	}
	if strings.TrimSpace(req.Provider) != "restart-boundary-provider" {
		t.Fatalf("scheduler provider = %q", req.Provider)
	}
}
