package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	stubUserConfigDir(t, dir, nil)
	configDir := filepath.Join(dir, "cq")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dsnSearchPath":[" HQL.CPY.SRC ","HQL.COB.SRC"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	want := []string{"HQL.CPY.SRC", "HQL.COB.SRC"}
	if !reflect.DeepEqual(cfg.DSNSearchPath, want) {
		t.Fatalf("DSNSearchPath = %q, want %q", cfg.DSNSearchPath, want)
	}
}

func TestLoadConfigMissingDefaultIsOptional(t *testing.T) {
	dir := t.TempDir()
	stubUserConfigDir(t, dir, nil)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if len(cfg.DSNSearchPath) != 0 {
		t.Fatalf("DSNSearchPath = %q, want empty", cfg.DSNSearchPath)
	}
}

func TestLoadConfigWithoutUserConfigDirectoryIsOptional(t *testing.T) {
	stubUserConfigDir(t, "", os.ErrNotExist)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if len(cfg.DSNSearchPath) != 0 {
		t.Fatalf("DSNSearchPath = %q, want empty", cfg.DSNSearchPath)
	}
}

func TestLoadConfigRejectsMemberInSearchPath(t *testing.T) {
	dir := t.TempDir()
	stubUserConfigDir(t, dir, nil)
	configDir := filepath.Join(dir, "cq")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(path, []byte(`{"dsnSearchPath":["HQL.CPY.SRC(MEMBER)"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadConfig()
	if err == nil || !strings.Contains(err.Error(), "without a member") {
		t.Fatalf("loadConfig() error = %v, want library validation", err)
	}
}

func stubUserConfigDir(t *testing.T, dir string, err error) {
	t.Helper()
	original := userConfigDir
	userConfigDir = func() (string, error) { return dir, err }
	t.Cleanup(func() { userConfigDir = original })
}
