package cliproxy

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestBuilderValidatesRequiredSchedulerConfig(t *testing.T) {
	cfg := &config.Config{Plugins: config.PluginsConfig{RequiredScheduler: "quota-policy"}}

	service, errBuild := NewBuilder().WithConfig(cfg).WithConfigPath(t.TempDir() + "/config.yaml").Build()
	if errBuild == nil || !strings.Contains(errBuild.Error(), "required scheduler") {
		t.Fatalf("Build() error = %v, want required scheduler config validation failure", errBuild)
	}
	if service != nil {
		t.Fatal("Build() returned a service with invalid required scheduler config")
	}
}

func TestBuilderRejectsUnavailableRequiredScheduler(t *testing.T) {
	enabled := true
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled:           true,
			Dir:               t.TempDir(),
			RequiredScheduler: "quota-policy",
			Configs: map[string]config.PluginInstanceConfig{
				"quota-policy": {Enabled: &enabled},
			},
		},
	}

	service, errBuild := NewBuilder().WithConfig(cfg).WithConfigPath(t.TempDir() + "/config.yaml").Build()
	if errBuild == nil || !strings.Contains(errBuild.Error(), "required scheduler") {
		t.Fatalf("Build() error = %v, want required scheduler failure", errBuild)
	}
	if service != nil {
		t.Fatal("Build() returned a service with unavailable required scheduler")
	}
}
