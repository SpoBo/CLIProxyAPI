package cliproxy

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func buildNativeSchedulerFixture(t *testing.T) string {
	t.Helper()
	if _, errLookPath := exec.LookPath("go"); errLookPath != nil {
		t.Skip("go toolchain is unavailable")
	}
	root := cliproxyModuleRoot(t)
	pluginsDir := t.TempDir()
	archDir := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH)
	if errMkdir := os.MkdirAll(archDir, 0o755); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	output := filepath.Join(archDir, "scheduler.so")
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

func cliproxyModuleRoot(t *testing.T) string {
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
