package pluginhost

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestInvalidSchedulerSelectionDoesNotEchoStableCandidateIdentifier(t *testing.T) {
	const stableID = "codex-owner@example.com.json"
	_, valid, reason := normalizeSchedulerResponse(pluginapi.SchedulerPickResponse{
		Handled: true,
		AuthID:  stableID,
	}, pluginapi.SchedulerPickRequest{Candidates: []pluginapi.SchedulerAuthCandidate{{ID: "other-auth"}}})
	if valid {
		t.Fatal("normalizeSchedulerResponse() accepted unknown auth identifier")
	}
	if strings.Contains(reason, stableID) {
		t.Fatalf("scheduler validation reason exposed stable candidate identifier: %q", reason)
	}
}
