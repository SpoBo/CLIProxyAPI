package pluginhost

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

func (h *Host) PickAuth(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	required := h.requiredScheduler()
	record := h.schedulerRecord()
	if record == nil {
		if required != "" {
			return pluginapi.SchedulerPickResponse{}, true, fmt.Errorf("required scheduler %q is unavailable", required)
		}
		return pluginapi.SchedulerPickResponse{}, false, nil
	}

	resp, handled, errPick := h.callScheduler(ctx, *record, req)
	if errPick != nil {
		return resp, handled, errPick
	}
	if !handled {
		if required != "" {
			return pluginapi.SchedulerPickResponse{}, true, fmt.Errorf("required scheduler %q failed during selection", required)
		}
		return resp, false, nil
	}
	if !resp.Handled {
		if required != "" {
			return pluginapi.SchedulerPickResponse{}, true, fmt.Errorf("required scheduler %q returned no decision", required)
		}
		return pluginapi.SchedulerPickResponse{}, false, nil
	}

	reportedAuthID := resp.AuthID != ""
	resp, valid, reason := normalizeSchedulerResponse(resp, req)
	if !valid {
		log.WithField("plugin_id", record.id).Warnf("pluginhost: scheduler returned invalid response: %s", reason)
		if reportedAuthID {
			return pluginapi.SchedulerPickResponse{}, true, fmt.Errorf("scheduler %q returned invalid auth selection: %s", record.id, reason)
		}
		if required != "" {
			return pluginapi.SchedulerPickResponse{}, true, fmt.Errorf("required scheduler %q returned invalid response: %s", required, reason)
		}
		return pluginapi.SchedulerPickResponse{}, false, nil
	}
	return resp, true, nil
}

func (h *Host) HasScheduler() bool {
	return h.requiredScheduler() != "" || h.schedulerRecord() != nil
}

func (h *Host) SchedulerWantsAcrossPriorities() bool {
	record := h.schedulerRecord()
	if record == nil {
		return false
	}
	return schedulerWantsAcrossPriorities(record.plugin.Capabilities)
}

func (h *Host) schedulerRecord() *capabilityRecord {
	if h == nil {
		return nil
	}
	snap := h.Snapshot()
	required := strings.TrimSpace(snap.requiredScheduler)
	for _, record := range h.activeRecordsFromSnapshot(snap) {
		if required != "" && record.id != required {
			continue
		}
		if h.isPluginFused(record.id) || record.plugin.Capabilities.Scheduler == nil {
			continue
		}
		copyRecord := record
		return &copyRecord
	}
	return nil
}

func (h *Host) requiredScheduler() string {
	if h == nil {
		return ""
	}
	return strings.TrimSpace(h.Snapshot().requiredScheduler)
}

func (h *Host) callScheduler(ctx context.Context, record capabilityRecord, req pluginapi.SchedulerPickRequest) (resp pluginapi.SchedulerPickResponse, handled bool, err error) {
	scheduler := record.plugin.Capabilities.Scheduler
	if h == nil || scheduler == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.SchedulerPickResponse{}, false, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "Scheduler.Pick", recovered)
			resp = pluginapi.SchedulerPickResponse{}
			handled = false
			err = nil
		}
	}()

	req.Plugin = record.meta
	resp, errPick := scheduler.Pick(ctx, req)
	if errPick != nil {
		log.WithField("plugin_id", record.id).WithError(errPick).Warn("pluginhost: scheduler rejected auth pick")
		return pluginapi.SchedulerPickResponse{}, true, errPick
	}
	return resp, true, nil
}

func normalizeSchedulerResponse(resp pluginapi.SchedulerPickResponse, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, string) {
	resp.DelegateBuiltin = strings.TrimSpace(resp.DelegateBuiltin)
	resp.RejectCode = strings.TrimSpace(resp.RejectCode)
	resp.RejectReason = strings.TrimSpace(resp.RejectReason)

	if resp.Reject {
		if resp.RejectCode == "" {
			resp.RejectCode = "auth_unavailable"
		}
		if resp.RejectReason == "" {
			resp.RejectReason = "scheduler rejected candidate selection"
		}
		return resp, true, ""
	}

	hasAuthID := resp.AuthID != ""
	hasDelegate := resp.DelegateBuiltin != ""
	if !hasAuthID && !hasDelegate {
		return pluginapi.SchedulerPickResponse{}, false, "missing auth id or delegate"
	}
	if hasAuthID {
		if resp.AuthID != strings.TrimSpace(resp.AuthID) {
			return pluginapi.SchedulerPickResponse{}, false, "non-canonical auth id"
		}
		if !schedulerCandidateExists(req.Candidates, resp.AuthID) {
			return pluginapi.SchedulerPickResponse{}, false, "unknown auth id"
		}
		return resp, true, ""
	}
	if !validSchedulerBuiltin(resp.DelegateBuiltin) {
		return pluginapi.SchedulerPickResponse{}, false, "unknown delegate"
	}
	return resp, true, ""
}

func schedulerCandidateExists(candidates []pluginapi.SchedulerAuthCandidate, authID string) bool {
	for _, candidate := range candidates {
		if candidate.ID == authID {
			return true
		}
	}
	return false
}

func validSchedulerBuiltin(delegate string) bool {
	switch delegate {
	case pluginapi.SchedulerBuiltinRoundRobin, pluginapi.SchedulerBuiltinFillFirst:
		return true
	default:
		return false
	}
}
