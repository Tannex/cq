package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cq.json")
	if err := os.WriteFile(path, []byte(`{"DSNSearchPath":[" HQL.CPY.SRC ","HQL.COB.SRC"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	want := []string{"HQL.CPY.SRC", "HQL.COB.SRC"}
	if !reflect.DeepEqual(cfg.DSNSearchPath, want) {
		t.Fatalf("DSNSearchPath = %q, want %q", cfg.DSNSearchPath, want)
	}
}

func TestLoadConfigMissingDefaultIsOptional(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	cfg, err := loadConfig("")
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if len(cfg.DSNSearchPath) != 0 {
		t.Fatalf("DSNSearchPath = %q, want empty", cfg.DSNSearchPath)
	}
}

func TestLoadConfigRejectsMemberInSearchPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cq.json")
	if err := os.WriteFile(path, []byte(`{"DSNSearchPath":["HQL.CPY.SRC(MEMBER)"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "without a member") {
		t.Fatalf("loadConfig() error = %v, want library validation", err)
	}
}
