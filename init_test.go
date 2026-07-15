package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubInitCommands(t *testing.T, version string, installErr error) *string {
	t.Helper()
	originalFind := findExecutable
	originalVersion := nodeVersion
	originalInstall := installSidecarDependencies
	findExecutable = func(name string) (string, error) { return "/test/bin/" + name, nil }
	nodeVersion = func(string) ([]byte, error) { return []byte(version), nil }
	var installedIn string
	installSidecarDependencies = func(npm, dir string) error {
		if npm != "/test/bin/npm" {
			t.Errorf("npm executable = %q", npm)
		}
		installedIn = dir
		return installErr
	}
	t.Cleanup(func() {
		findExecutable = originalFind
		nodeVersion = originalVersion
		installSidecarDependencies = originalInstall
	})
	return &installedIn
}

func TestRunInitInstallsSidecarAndUpdatesConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), "config root")
	stubUserConfigDir(t, root, nil)
	installedIn := stubInitCommands(t, "v20.9.0\n", nil)
	configDir := filepath.Join(root, "cq")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"dsnSearchPath":["HLQ.CPY"],"custom":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runWithArgs(t, "init"); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	wantDir := filepath.Join(configDir, "sidecar")
	if err := os.MkdirAll(wantDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wantDir, "stale"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := runWithArgs(t, "init"); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if filepath.Dir(*installedIn) != configDir || !strings.HasPrefix(filepath.Base(*installedIn), ".sidecar-") {
		t.Fatalf("npm ci directory = %q, want a staging directory in %q", *installedIn, configDir)
	}
	for _, name := range []string{"zowe-sidecar.js", "package.json", "package-lock.json"} {
		got, err := os.ReadFile(filepath.Join(wantDir, name))
		if err != nil {
			t.Errorf("installed %s: %v", name, err)
			continue
		}
		want, err := os.ReadFile(filepath.Join("sidecar", name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("bundled %s is stale", name)
		}
	}
	if _, err := os.Stat(filepath.Join(wantDir, "stale")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old sidecar file was not replaced: %v", err)
	}
	contents, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(contents, &got); err != nil {
		t.Fatal(err)
	}
	var command string
	if err := json.Unmarshal(got["sidecar"], &command); err != nil {
		t.Fatal(err)
	}
	wantCommand := `node '` + filepath.Join(wantDir, "zowe-sidecar.js") + `'`
	if command != wantCommand {
		t.Errorf("sidecar command = %q, want %q", command, wantCommand)
	}
	name, args, err := splitCommandSpec(command)
	if err != nil || name != "node" || len(args) != 1 || args[0] != filepath.Join(wantDir, "zowe-sidecar.js") {
		t.Errorf("generated command parses as %q %q, %v", name, args, err)
	}
	if string(got["custom"]) != "true" {
		t.Errorf("custom config was not preserved: %s", contents)
	}
}

func TestRunInitRejectsOldNode(t *testing.T) {
	root := t.TempDir()
	stubUserConfigDir(t, root, nil)
	installedIn := stubInitCommands(t, "v20.8.1", nil)
	err := runWithArgs(t, "init")
	if err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("run() error = %v, want old Node error", err)
	}
	if *installedIn != "" {
		t.Fatalf("npm ci unexpectedly ran in %q", *installedIn)
	}
}

func TestRunInitRequiresNode(t *testing.T) {
	originalFind := findExecutable
	findExecutable = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { findExecutable = originalFind })
	err := runWithArgs(t, "init")
	if err == nil || !strings.Contains(err.Error(), "Node.js is required") {
		t.Fatalf("run() error = %v, want Node.js requirement", err)
	}
}

func TestRunInitRejectsArguments(t *testing.T) {
	err := runWithArgs(t, "init", "extra")
	if err == nil || !strings.Contains(err.Error(), "usage: cq init") {
		t.Fatalf("run() error = %v, want init usage", err)
	}
}

func TestRunInitReportsNPMFailure(t *testing.T) {
	root := t.TempDir()
	stubUserConfigDir(t, root, nil)
	stubInitCommands(t, "v22.1.0", errors.New("exit status 1"))
	err := runWithArgs(t, "init")
	if err == nil || !strings.Contains(err.Error(), "npm ci") {
		t.Fatalf("run() error = %v, want npm ci error", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "cq", "config.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("config should not be updated after npm failure; stat error = %v", statErr)
	}
}

func TestRunInitNPMFailurePreservesExistingInstallation(t *testing.T) {
	root := t.TempDir()
	stubUserConfigDir(t, root, nil)
	stubInitCommands(t, "v22.1.0", errors.New("exit status 1"))
	configDir := filepath.Join(root, "cq")
	installDir := filepath.Join(configDir, sidecarSubdir)
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(installDir, "working-sidecar")
	if err := os.WriteFile(sentinel, []byte("working"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.json")
	existingConfig := []byte(`{"dsnSearchPath":["HLQ.CPY"],"sidecar":"node existing.js"}`)
	if err := os.WriteFile(configPath, existingConfig, 0o600); err != nil {
		t.Fatal(err)
	}

	err := runWithArgs(t, "init")
	if err == nil || !strings.Contains(err.Error(), "npm ci") {
		t.Fatalf("run() error = %v, want npm ci error", err)
	}
	gotSentinel, err := os.ReadFile(sentinel)
	if err != nil || string(gotSentinel) != "working" {
		t.Fatalf("existing installation changed: contents %q, error %v", gotSentinel, err)
	}
	gotConfig, err := os.ReadFile(configPath)
	if err != nil || string(gotConfig) != string(existingConfig) {
		t.Fatalf("existing config changed: contents %q, error %v", gotConfig, err)
	}
	matches, err := filepath.Glob(filepath.Join(configDir, ".sidecar-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("staging directories remain after npm failure: %v", matches)
	}
}

func TestRunInitCreatesDefaultConfig(t *testing.T) {
	root := t.TempDir()
	stubUserConfigDir(t, root, nil)
	stubInitCommands(t, "v22.1.0", nil)
	if err := runWithArgs(t, "init"); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(root, "cq", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg config
	if err := json.Unmarshal(contents, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.DSNSearchPath == nil {
		t.Errorf("dsnSearchPath should be an empty array: %s", contents)
	}
	want := "node " + filepath.Join(root, "cq", "sidecar", sidecarScript)
	if cfg.Sidecar != want {
		t.Errorf("sidecar = %q, want %q", cfg.Sidecar, want)
	}
}

func TestRunInitRequiresNPMBeforeReplacingSidecar(t *testing.T) {
	root := t.TempDir()
	stubUserConfigDir(t, root, nil)
	originalFind := findExecutable
	originalVersion := nodeVersion
	findExecutable = func(name string) (string, error) {
		if name == "npm" {
			return "", errors.New("not found")
		}
		return "/test/bin/node", nil
	}
	nodeVersion = func(string) ([]byte, error) { return []byte("v22.0.0"), nil }
	t.Cleanup(func() {
		findExecutable = originalFind
		nodeVersion = originalVersion
	})
	installDir := filepath.Join(root, "cq", "sidecar")
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(installDir, "working")
	if err := os.WriteFile(sentinel, []byte("yes"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runWithArgs(t, "init")
	if err == nil || !strings.Contains(err.Error(), "npm is required") {
		t.Fatalf("run() error = %v, want npm requirement", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("existing sidecar was changed: %v", err)
	}
}

func TestRunInitValidatesConfigBeforeReplacingSidecar(t *testing.T) {
	root := t.TempDir()
	stubUserConfigDir(t, root, nil)
	stubInitCommands(t, "v22.0.0", nil)
	installDir := filepath.Join(root, "cq", "sidecar")
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(installDir, "working")
	if err := os.WriteFile(sentinel, []byte("yes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cq", "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runWithArgs(t, "init")
	if err == nil || !strings.Contains(err.Error(), "parse config") {
		t.Fatalf("run() error = %v, want config parse error", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("existing sidecar was changed: %v", err)
	}
}

func TestParseNodeVersion(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  [3]int
	}{
		{"v20.9.0\n", [3]int{20, 9, 0}},
		{"22.12.1", [3]int{22, 12, 1}},
		{"v20.9.0-pre", [3]int{20, 9, 0}},
	} {
		got, err := parseNodeVersion(tc.input)
		if err != nil || got != tc.want {
			t.Errorf("parseNodeVersion(%q) = %v, %v; want %v", tc.input, got, err, tc.want)
		}
	}
	if _, err := parseNodeVersion("unknown"); err == nil {
		t.Error("parseNodeVersion(unknown) unexpectedly succeeded")
	}
}

func TestQuoteCommandArgRoundTrips(t *testing.T) {
	paths := []string{
		"/tmp/cq config/sidecar/zowe-sidecar.js",
		`/tmp/cq-"config/sidecar/zowe-sidecar.js`,
		"/tmp/cq-'config/sidecar/zowe-sidecar.js",
		`/tmp/cq-'"config/sidecar/zowe-sidecar.js`,
	}
	for _, path := range paths {
		command := "node " + quoteCommandArg(path)
		name, args, err := splitCommandSpec(command)
		if err != nil {
			t.Errorf("splitCommandSpec(%q) error = %v", command, err)
			continue
		}
		if name != "node" || len(args) != 1 || args[0] != path {
			t.Errorf("splitCommandSpec(%q) = %q %q, want node [%q]", command, name, args, path)
		}
	}
}
