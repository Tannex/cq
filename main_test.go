package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func TestRunRejectsJSONWithDataSource(t *testing.T) {
	err := runWithArgs(t,
		"-c", "CUSTOMER.cpy",
		"-j", "records.json",
		"-d", "customer.bin",
	)
	if err == nil || !strings.Contains(err.Error(), "-j cannot be combined") {
		t.Fatalf("run() error = %v, want -j conflict", err)
	}
}

func TestJSONBinaryCLIRoundTrip(t *testing.T) {
	dir := t.TempDir()
	book := filepath.Join(dir, "record.cpy")
	if err := os.WriteFile(book, []byte(`01 TEST-RECORD.
  05 NAME PIC X(4).
  05 AMOUNT PIC S9(3)V99 COMP-3.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	input := `[{"NAME":"AB","AMOUNT":-12.3},{"NAME":"CD","AMOUNT":7.05}]`

	encodeCmd := cqCommand(t, "-c", book, "-j", "-")
	encodeCmd.Stdin = strings.NewReader(input)
	binary, err := encodeCmd.Output()
	if err != nil {
		t.Fatalf("encode command: %v\nstderr: %s", err, commandStderr(err))
	}
	if len(binary) != 14 {
		t.Fatalf("encoded %d bytes, want 14", len(binary))
	}

	decodeCmd := cqCommand(t, "-c", book, "-d", "-")
	decodeCmd.Stdin = bytes.NewReader(binary)
	decoded, err := decodeCmd.Output()
	if err != nil {
		t.Fatalf("decode command: %v\nstderr: %s", err, commandStderr(err))
	}
	var got, want any
	if err := json.Unmarshal(decoded, &got); err != nil {
		t.Fatalf("decode output is not JSON: %v\n%s", err, decoded)
	}
	if err := json.Unmarshal([]byte(input), &want); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("round trip = %s, want %s", decoded, input)
	}
}

func cqCommand(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	helperArgs := append([]string{"-test.run=^TestCQHelperProcess$", "--"}, args...)
	cmd := exec.Command(os.Args[0], helperArgs...)
	cmd.Env = append(os.Environ(), "CQ_HELPER_PROCESS=1", "XDG_CONFIG_HOME="+t.TempDir())
	return cmd
}

func commandStderr(err error) string {
	if exit, ok := err.(*exec.ExitError); ok {
		return string(exit.Stderr)
	}
	return ""
}

func TestCQHelperProcess(t *testing.T) {
	if os.Getenv("CQ_HELPER_PROCESS") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{"cq"}, os.Args[i+1:]...)
			break
		}
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
