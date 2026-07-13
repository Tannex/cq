package main

import (
	"io"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

var testExecutable = os.Args[0]

func useZoweHelper(t *testing.T, wantArgs []string, stdout, stderr string, exitCode int) {
	t.Helper()
	original := zoweCommand
	zoweCommand = func(args ...string) *exec.Cmd {
		if !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("zowe args = %q, want %q", args, wantArgs)
		}
		return zoweHelperCommand(stdout, stderr, exitCode)
	}
	t.Cleanup(func() { zoweCommand = original })
}

func zoweHelperCommand(stdout, stderr string, exitCode int) *exec.Cmd {
	cmd := exec.Command(testExecutable, "-test.run=TestZoweHelperProcess")
	cmd.Env = append(os.Environ(),
		"CQ_ZOWE_HELPER=1",
		"CQ_ZOWE_STDOUT="+stdout,
		"CQ_ZOWE_STDERR="+stderr,
		"CQ_ZOWE_EXIT="+strconv.Itoa(exitCode),
	)
	return cmd
}

func TestZoweHelperProcess(t *testing.T) {
	if os.Getenv("CQ_ZOWE_HELPER") != "1" {
		return
	}
	_, _ = io.WriteString(os.Stdout, os.Getenv("CQ_ZOWE_STDOUT"))
	_, _ = io.WriteString(os.Stderr, os.Getenv("CQ_ZOWE_STDERR"))
	exitCode, _ := strconv.Atoi(os.Getenv("CQ_ZOWE_EXIT"))
	os.Exit(exitCode)
}

func TestFetchZoweCopybook(t *testing.T) {
	const dsn = "HQ.COPYLIB(CUSTOMER)"
	const copybook = "01 CUSTOMER.\n   05 NAME PIC X(10).\n"
	useZoweHelper(t,
		[]string{"zos-files", "view", "data-set", dsn},
		copybook, "", 0,
	)

	got, err := fetchZoweDataSet(dsn, false)
	if err != nil {
		t.Fatalf("fetchZoweDataSet() error = %v", err)
	}
	if string(got) != copybook {
		t.Fatalf("fetchZoweDataSet() = %q, want %q", got, copybook)
	}
}

func TestStreamZoweDataBinary(t *testing.T) {
	const dsn = "HQ.CUSTOMER.DATA"
	useZoweHelper(t,
		[]string{"zos-files", "view", "data-set", dsn, "--binary"},
		"raw-record-data", "", 0,
	)

	stream, err := openZoweDataSet(dsn, true)
	if err != nil {
		t.Fatalf("openZoweDataSet() error = %v", err)
	}
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("reading stream: %v", err)
	}
	if err := stream.wait(); err != nil {
		t.Fatalf("waiting for stream: %v", err)
	}
	if string(got) != "raw-record-data" {
		t.Fatalf("stream = %q, want raw-record-data", got)
	}
}

func TestZoweErrorIncludesStderr(t *testing.T) {
	const dsn = "HQ.MISSING.DATA"
	useZoweHelper(t,
		[]string{"zos-files", "view", "data-set", dsn},
		"", "dataset not found", 8,
	)

	_, err := fetchZoweDataSet(dsn, false)
	if err == nil || !strings.Contains(err.Error(), "dataset not found") {
		t.Fatalf("fetchZoweDataSet() error = %v, want Zowe stderr", err)
	}
}

func TestZoweStreamErrorIncludesStderr(t *testing.T) {
	const dsn = "HQ.MISSING.DATA"
	useZoweHelper(t,
		[]string{"zos-files", "view", "data-set", dsn, "--binary"},
		"", "not authorized", 8,
	)

	stream, err := openZoweDataSet(dsn, true)
	if err != nil {
		t.Fatalf("openZoweDataSet() error = %v", err)
	}
	if _, err := io.ReadAll(stream); err != nil {
		t.Fatalf("reading stream: %v", err)
	}
	if err := stream.wait(); err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("stream.wait() error = %v, want Zowe stderr", err)
	}
}

func TestRunWithZoweDataSets(t *testing.T) {
	const copybookDSN = "HQ.COPYLIB(CUSTOMER)"
	const dataDSN = "HQ.CUSTOMER.DATA"
	originalCommand := zoweCommand
	zoweCommand = func(args ...string) *exec.Cmd {
		switch {
		case reflect.DeepEqual(args, []string{"zos-files", "view", "data-set", copybookDSN}):
			return zoweHelperCommand("01 CUSTOMER.\n   05 NAME PIC X(3).\n", "", 0)
		case reflect.DeepEqual(args, []string{"zos-files", "view", "data-set", dataDSN, "--binary"}):
			return zoweHelperCommand("BOB", "", 0)
		default:
			t.Fatalf("unexpected zowe args: %q", args)
			return nil
		}
	}
	t.Cleanup(func() { zoweCommand = originalCommand })

	out, err := os.CreateTemp(t.TempDir(), "cq-output-*.json")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	originalStdout := os.Stdout
	os.Stdout = out
	t.Cleanup(func() { os.Stdout = originalStdout })

	err = runWithArgs(t,
		"--copybook-dsn", copybookDSN,
		"--data-dsn", dataDSN,
		"-codepage", "ascii",
	)
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"NAME":"BOB"`) {
		t.Fatalf("run() output = %s, want decoded Zowe record", got)
	}
}

func TestRunPrefersZoweErrorForPartialRecord(t *testing.T) {
	const copybookDSN = "HQ.COPYLIB(CUSTOMER)"
	const dataDSN = "HQ.CUSTOMER.DATA"
	originalCommand := zoweCommand
	zoweCommand = func(args ...string) *exec.Cmd {
		switch {
		case reflect.DeepEqual(args, []string{"zos-files", "view", "data-set", copybookDSN}):
			return zoweHelperCommand("01 CUSTOMER.\n   05 NAME PIC X(3).\n", "", 0)
		case reflect.DeepEqual(args, []string{"zos-files", "view", "data-set", dataDSN, "--binary"}):
			return zoweHelperCommand("BO", "connection lost", 8)
		default:
			t.Fatalf("unexpected zowe args: %q", args)
			return nil
		}
	}
	t.Cleanup(func() { zoweCommand = originalCommand })

	err := runWithArgs(t,
		"--copybook-dsn", copybookDSN,
		"--data-dsn", dataDSN,
		"-codepage", "ascii",
	)
	if err == nil || !strings.Contains(err.Error(), "connection lost") {
		t.Fatalf("run() error = %v, want Zowe failure", err)
	}
}
