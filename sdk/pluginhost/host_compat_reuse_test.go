package pluginhost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

type legacyApplyConfigHost interface {
	ApplyConfig(context.Context, RuntimeConfig)
}

var _ legacyApplyConfigHost = (*Host)(nil)
var _ func(*Host, context.Context, RuntimeConfig) = (*Host).ApplyConfig
var _ func(*Host, context.Context, RuntimeConfig) error = (*Host).ApplyConfigWithError

func TestHostApplyConfigWithErrorRejectsInvalidHostClearly(t *testing.T) {
	var nilHost *Host
	if errApply := nilHost.ApplyConfigWithError(context.Background(), RuntimeConfig{}); !errors.Is(errApply, ErrInvalidHost) {
		t.Fatalf("ApplyConfigWithError() error = %v, want ErrInvalidHost", errApply)
	}
	emptyHost := &Host{}
	if errApply := emptyHost.ApplyConfigWithError(context.Background(), RuntimeConfig{}); !errors.Is(errApply, ErrInvalidHost) {
		t.Fatalf("empty Host ApplyConfigWithError() error = %v, want ErrInvalidHost", errApply)
	}
}

func TestHostApplyConfigWithErrorDoesNotMutateInput(t *testing.T) {
	enabled := false
	cfg := RuntimeConfig{
		Enabled: false,
		Dir:     "  ~/plugins  ",
		Configs: map[string]PluginInstanceConfig{
			"disabled": {
				Enabled: &enabled,
				Raw: yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Value: "account"},
					{Kind: yaml.ScalarNode, Value: "owner@example.com"},
				}},
			},
		},
		OAuthExcludedModels: map[string][]string{"Codex": {"private-model"}},
	}
	before, errMarshal := yaml.Marshal(cfg)
	if errMarshal != nil {
		t.Fatalf("yaml.Marshal(input) error = %v", errMarshal)
	}
	host := New()
	t.Cleanup(host.ShutdownAll)

	if errApply := host.ApplyConfigWithError(context.Background(), cfg); errApply != nil {
		t.Fatalf("ApplyConfigWithError() error = %v", errApply)
	}
	after, errMarshal := yaml.Marshal(cfg)
	if errMarshal != nil {
		t.Fatalf("yaml.Marshal(input after apply) error = %v", errMarshal)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("ApplyConfigWithError() mutated input\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestHostShutdownAllowsFreshRequiredSchedulerStartup(t *testing.T) {
	pluginsDir := buildSchedulerPluginFixture(t)
	enabled := true
	cfg := RuntimeConfig{
		Enabled:           true,
		Dir:               pluginsDir,
		RequiredScheduler: "scheduler",
		Configs: map[string]PluginInstanceConfig{
			"scheduler": {Enabled: &enabled},
		},
	}
	host := New()
	t.Cleanup(host.ShutdownAll)

	if errApply := host.ApplyConfigWithError(context.Background(), cfg); errApply != nil {
		t.Fatalf("initial ApplyConfigWithError() error = %v", errApply)
	}
	host.ShutdownAll()
	if errApply := host.ApplyConfigWithError(context.Background(), cfg); errApply != nil {
		t.Fatalf("ApplyConfigWithError() after ShutdownAll error = %v", errApply)
	}
	if !host.HasScheduler() {
		t.Fatal("fresh required scheduler did not become active after host reuse")
	}
}

func buildSchedulerPluginFixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "freebsd" {
		t.Skip("native shared-library fixture is unsupported on this platform")
	}
	if _, errLookPath := exec.LookPath("go"); errLookPath != nil {
		t.Skip("go toolchain is unavailable")
	}
	root := moduleRoot(t)
	pluginsDir := t.TempDir()
	archDir := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH)
	if errMkdir := os.MkdirAll(archDir, 0o755); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	ext := ".so"
	if runtime.GOOS == "darwin" {
		ext = ".dylib"
	}
	output := filepath.Join(archDir, "scheduler"+ext)
	fixtureDir := filepath.Join(t.TempDir(), "scheduler-fixture")
	if errMkdir := os.MkdirAll(fixtureDir, 0o755); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	source, errRead := os.ReadFile(filepath.Join(root, "examples", "plugin", "scheduler", "go", "main.go"))
	if errRead != nil {
		t.Fatal(errRead)
	}
	if errWrite := os.WriteFile(filepath.Join(fixtureDir, "main.go"), source, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	module := "module schedulerfixture\n\ngo 1.26.0\n\nrequire github.com/router-for-me/CLIProxyAPI/v8 v8.0.0\n\nreplace github.com/router-for-me/CLIProxyAPI/v8 => " + root + "\n"
	if errWrite := os.WriteFile(filepath.Join(fixtureDir, "go.mod"), []byte(module), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cmd := exec.Command("go", "build", "-mod=mod", "-buildmode=c-shared", "-o", output, ".")
	cmd.Dir = fixtureDir
	if raw, errBuild := cmd.CombinedOutput(); errBuild != nil {
		t.Skipf("native scheduler fixture build unavailable: %v\n%s", errBuild, raw)
	}
	return pluginsDir
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, errGetwd := os.Getwd()
	if errGetwd != nil {
		t.Fatal(errGetwd)
	}
	for {
		if _, errStat := os.Stat(filepath.Join(dir, "go.mod")); errStat == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
