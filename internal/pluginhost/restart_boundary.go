package pluginhost

import (
	"reflect"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkpluginstore "github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginstore"
)

type requiredSchedulerBoundary struct {
	enabled           bool
	dir               string
	requiredScheduler string
	storeSources      []string
	storeAuth         []sdkpluginstore.AuthConfig
	authRevision      int64
	configs           map[string]requiredSchedulerBoundaryItem
}

type requiredSchedulerBoundaryItem struct {
	enabled  bool
	priority int
	config   any
}

func requiredSchedulerBoundaryFromConfig(cfg *config.Config) (*requiredSchedulerBoundary, error) {
	if cfg == nil {
		return nil, nil
	}
	dir, errResolve := config.ResolvePluginsDir(cfg.Plugins.Dir)
	if errResolve != nil {
		return nil, errResolve
	}
	boundary := &requiredSchedulerBoundary{
		enabled:           cfg.Plugins.Enabled,
		dir:               dir,
		requiredScheduler: strings.TrimSpace(cfg.Plugins.RequiredScheduler),
		storeSources:      normalizeRequiredBoundarySources(cfg.Plugins.StoreSources),
		storeAuth:         sdkpluginstore.NormalizeAuthConfigs(cfg.Plugins.StoreAuth),
		authRevision:      cfg.Plugins.AuthRevision,
		configs:           make(map[string]requiredSchedulerBoundaryItem, len(cfg.Plugins.Configs)),
	}
	ids := make([]string, 0, len(cfg.Plugins.Configs))
	for id := range cfg.Plugins.Configs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		item := cfg.Plugins.Configs[id]
		enabled := item.Enabled != nil && *item.Enabled
		node := normalizedConfigNode(item, enabled)
		var semanticConfig any
		if node != nil && node.Kind != 0 {
			if errDecode := node.Decode(&semanticConfig); errDecode != nil {
				return nil, errDecode
			}
		}
		boundary.configs[id] = requiredSchedulerBoundaryItem{
			enabled:  enabled,
			priority: item.Priority,
			config:   semanticConfig,
		}
	}
	return boundary, nil
}

func normalizeRequiredBoundarySources(sources []string) []string {
	if len(sources) == 0 {
		return nil
	}
	out := make([]string, 0, len(sources))
	for _, source := range sources {
		if source = strings.TrimSpace(source); source != "" {
			out = append(out, source)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func requiredSchedulerBoundariesEqual(left, right *requiredSchedulerBoundary) bool {
	return reflect.DeepEqual(left, right)
}

// PreflightConfig rejects runtime transitions across the required-scheduler
// restart boundary without changing host or plugin state.
func (h *Host) PreflightConfig(cfg *config.Config) error {
	_, errPreflight := h.preflightConfig(cfg)
	return errPreflight
}

func (h *Host) preflightConfig(cfg *config.Config) (bool, error) {
	if h == nil {
		return false, nil
	}
	incomingRequired := cfg != nil && strings.TrimSpace(cfg.Plugins.RequiredScheduler) != ""
	h.mu.Lock()
	runtimeConfigured := h.runtimeConfig != nil
	activeBoundary := h.requiredBoundary
	h.mu.Unlock()

	if !runtimeConfigured || (activeBoundary == nil && !incomingRequired) {
		return false, nil
	}
	if activeBoundary == nil {
		return false, restartRequiredSchedulerError("enabling a required scheduler at runtime is not supported")
	}
	incoming, errBoundary := requiredSchedulerBoundaryFromConfig(cfg)
	if errBoundary != nil {
		return false, restartRequiredSchedulerError("plugin runtime settings changed and could not be compared: " + errBoundary.Error())
	}
	if !requiredSchedulerBoundariesEqual(activeBoundary, incoming) {
		return false, restartRequiredSchedulerError("plugin binary, host configuration, required scheduler, or tier topology changed")
	}
	return true, nil
}

func restartRequiredSchedulerError(reason string) error {
	return &config.RestartRequiredError{
		Component: "required scheduler runtime configuration",
		Reason:    reason,
	}
}
