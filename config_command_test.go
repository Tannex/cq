package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunConfigCreatesAndLaunchesConfig(t *testing.T) {
	root := t.TempDir()
	stubUserConfigDir(t, root, nil)
	originalLaunch := launchConfigEditor
	var launchedPath string
	launchConfigEditor = func(path string) error {
		launchedPath = path
		return nil
	}
	t.Cleanup(func() { launchConfigEditor = originalLaunch })

	if err := runWithArgs(t, "config"); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	wantPath := filepath.Join(root, "cq", "config.json")
	if launchedPath != wantPath {
		t.Fatalf("launched path = %q, want %q", launchedPath, wantPath)
	}
	got, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != initialConfig {
		t.Fatalf("config contents = %q, want %q", got, initialConfig)
	}
}

func TestRunConfigPreservesExistingFile(t *testing.T) {
	root := t.TempDir()
	stubUserConfigDir(t, root, nil)
	configDir := filepath.Join(root, "cq")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "config.json")
	const existing = `{"DSNSearchPath":["HQL.CPY.SRC"]}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	originalLaunch := launchConfigEditor
	launchConfigEditor = func(string) error { return nil }
	t.Cleanup(func() { launchConfigEditor = originalLaunch })

	if err := runWithArgs(t, "config"); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != existing {
		t.Fatalf("config contents = %q, want preserved %q", got, existing)
	}
}

func TestRunConfigRejectsArguments(t *testing.T) {
	err := runWithArgs(t, "config", "extra")
	if err == nil || !strings.Contains(err.Error(), "usage: cq config") {
		t.Fatalf("run() error = %v, want config usage", err)
	}
}

func TestConfigEditorInvocation(t *testing.T) {
	tests := []struct {
		name        string
		goos        string
		environment map[string]string
		wantName    string
		wantArgs    []string
	}{
		{name: "visual", goos: "linux", environment: map[string]string{"VISUAL": "code --wait", "EDITOR": "vim"}, wantName: "code", wantArgs: []string{"--wait", "/tmp/config.json"}},
		{name: "quoted editor", goos: "windows", environment: map[string]string{"EDITOR": `"C:\Program Files\Editor\editor.exe" --wait`}, wantName: `C:\Program Files\Editor\editor.exe`, wantArgs: []string{"--wait", "/tmp/config.json"}},
		{name: "macos fallback", goos: "darwin", wantName: "open", wantArgs: []string{"-t", "/tmp/config.json"}},
		{name: "windows fallback", goos: "windows", wantName: "notepad.exe", wantArgs: []string{"/tmp/config.json"}},
		{name: "linux fallback", goos: "linux", wantName: "xdg-open", wantArgs: []string{"/tmp/config.json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string { return tt.environment[key] }
			name, args, err := configEditorInvocation(tt.goos, getenv, "/tmp/config.json")
			if err != nil {
				t.Fatalf("configEditorInvocation() error = %v", err)
			}
			if name != tt.wantName || !reflect.DeepEqual(args, tt.wantArgs) {
				t.Fatalf("configEditorInvocation() = %q %q, want %q %q", name, args, tt.wantName, tt.wantArgs)
			}
		})
	}
}
