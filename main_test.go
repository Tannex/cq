package main

import (
	"os"
	"strings"
	"testing"
)

func runWithArgs(t *testing.T, args ...string) error {
	t.Helper()
	original := os.Args
	os.Args = append([]string{"cq"}, args...)
	t.Cleanup(func() { os.Args = original })
	return run()
}

func TestRunRejectsPositionalCopybook(t *testing.T) {
	err := runWithArgs(t, "CUSTOMER.cpy", "customer.bin")
	if err == nil || !strings.Contains(err.Error(), "provide exactly one copybook source") {
		t.Fatalf("run() error = %v, want missing -c error", err)
	}
}

func TestRunRequiresCopybookFlag(t *testing.T) {
	err := runWithArgs(t)
	if err == nil || !strings.Contains(err.Error(), "provide exactly one copybook source") {
		t.Fatalf("run() error = %v, want missing -c error", err)
	}
}

func TestRunAcceptsCopybookAndDataFlags(t *testing.T) {
	err := runWithArgs(t,
		"-c", "testdata/copybooks/cb2xml/Vendor.cbl",
		"-d", "does-not-exist.bin",
	)
	if err == nil || !strings.Contains(err.Error(), "does-not-exist.bin") {
		t.Fatalf("run() error = %v, want attempted -d path", err)
	}
}

func TestRunAcceptsPositionalData(t *testing.T) {
	err := runWithArgs(t,
		"-c", "testdata/copybooks/cb2xml/Vendor.cbl",
		"does-not-exist.bin",
	)
	if err == nil || !strings.Contains(err.Error(), "does-not-exist.bin") {
		t.Fatalf("run() error = %v, want attempted positional data path", err)
	}
}

func TestRunRejectsDuplicateDataPaths(t *testing.T) {
	err := runWithArgs(t,
		"-c", "testdata/copybooks/cb2xml/Vendor.cbl",
		"-d", "first.bin",
		"second.bin",
	)
	if err == nil || !strings.Contains(err.Error(), "provide only one data source") {
		t.Fatalf("run() error = %v, want duplicate DATA error", err)
	}
}

func TestRunRejectsDuplicateCopybookSources(t *testing.T) {
	err := runWithArgs(t,
		"-c", "CUSTOMER.cpy",
		"--copybook-dsn", "HQ.COPYLIB(CUSTOMER)",
	)
	if err == nil || !strings.Contains(err.Error(), "provide exactly one copybook source") {
		t.Fatalf("run() error = %v, want duplicate copybook error", err)
	}
}

func TestRunRejectsDuplicateDataSources(t *testing.T) {
	err := runWithArgs(t,
		"-c", "CUSTOMER.cpy",
		"-d", "customer.bin",
		"--data-dsn", "HQ.CUSTOMER.DATA",
	)
	if err == nil || !strings.Contains(err.Error(), "provide only one data source") {
		t.Fatalf("run() error = %v, want duplicate data error", err)
	}
}
