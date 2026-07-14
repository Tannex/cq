package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
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

func zoweDownloadHelperCommand(path string, data []byte, stderr string, exitCode int) *exec.Cmd {
	cmd := zoweHelperCommand("", stderr, exitCode)
	cmd.Env = append(cmd.Env,
		"CQ_ZOWE_FILE="+path,
		"CQ_ZOWE_FILE_DATA="+base64.StdEncoding.EncodeToString(data),
	)
	return cmd
}

func zoweDownloadPath(args []string, dsn string) (string, bool) {
	if len(args) != 8 {
		return "", false
	}
	want := []string{"zos-files", "download", "data-set", dsn,
		"--binary", "--file", args[6], "--overwrite"}
	return args[6], reflect.DeepEqual(args, want)
}

func TestZoweHelperProcess(t *testing.T) {
	if os.Getenv("CQ_ZOWE_HELPER") != "1" {
		return
	}
	if path := os.Getenv("CQ_ZOWE_FILE"); path != "" {
		data, err := base64.StdEncoding.DecodeString(os.Getenv("CQ_ZOWE_FILE_DATA"))
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
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

	got, err := fetchZoweCopybook(dsn)
	if err != nil {
		t.Fatalf("fetchZoweCopybook() error = %v", err)
	}
	if string(got) != copybook {
		t.Fatalf("fetchZoweCopybook() = %q, want %q", got, copybook)
	}
}

func TestDownloadZoweDataBinary(t *testing.T) {
	const dsn = "HQ.CUSTOMER.DATA"
	wantData := []byte{0x00, 0x0e, 0xff, '\n'}
	original := zoweCommand
	zoweCommand = func(args ...string) *exec.Cmd {
		path, ok := zoweDownloadPath(args, dsn)
		if !ok {
			t.Fatalf("unexpected zowe args: %q", args)
		}
		return zoweDownloadHelperCommand(path, wantData, "", 0)
	}
	t.Cleanup(func() { zoweCommand = original })

	data, err := downloadZoweDataSet(dsn)
	if err != nil {
		t.Fatalf("downloadZoweDataSet() error = %v", err)
	}
	path := data.Name()
	got, err := io.ReadAll(data)
	if err != nil {
		t.Fatalf("reading download: %v", err)
	}
	if !reflect.DeepEqual(got, wantData) {
		t.Fatalf("download = %v, want %v", got, wantData)
	}
	if err := data.Close(); err != nil {
		t.Fatalf("closing download: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temporary download still exists at %q", path)
	}
}

func TestZoweErrorIncludesStderr(t *testing.T) {
	const dsn = "HQ.MISSING.DATA"
	useZoweHelper(t,
		[]string{"zos-files", "view", "data-set", dsn},
		"", "dataset not found", 8,
	)

	_, err := fetchZoweCopybook(dsn)
	if err == nil || !strings.Contains(err.Error(), "dataset not found") {
		t.Fatalf("fetchZoweCopybook() error = %v, want Zowe stderr", err)
	}
}

func TestZoweDownloadErrorIncludesStderrAndRemovesTemporaryFile(t *testing.T) {
	const dsn = "HQ.MISSING.DATA"
	original := zoweCommand
	var path string
	zoweCommand = func(args ...string) *exec.Cmd {
		var ok bool
		path, ok = zoweDownloadPath(args, dsn)
		if !ok {
			t.Fatalf("unexpected zowe args: %q", args)
		}
		return zoweDownloadHelperCommand(path, []byte("partial"), "not authorized", 8)
	}
	t.Cleanup(func() { zoweCommand = original })

	_, err := downloadZoweDataSet(dsn)
	if err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("downloadZoweDataSet() error = %v, want Zowe stderr", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("partial temporary download still exists at %q", path)
	}
}

// recordZoweCalls swaps zoweCommand for stub and returns the DSNs it was
// called with, sorted: the resolver probes libraries concurrently, so the
// call order is not deterministic.
func recordZoweCalls(t *testing.T, stub func(dsn string) *exec.Cmd) func() []string {
	t.Helper()
	original := zoweCommand
	var mu sync.Mutex
	var calls []string
	zoweCommand = func(args ...string) *exec.Cmd {
		dsn := args[3]
		mu.Lock()
		calls = append(calls, dsn)
		mu.Unlock()
		return stub(dsn)
	}
	t.Cleanup(func() { zoweCommand = original })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		sorted := append([]string{}, calls...)
		sort.Strings(sorted)
		return sorted
	}
}

func TestDSNCopyResolverUsesSearchOrderAndCache(t *testing.T) {
	sortedCalls := recordZoweCalls(t, func(dsn string) *exec.Cmd {
		switch dsn {
		case "HQL.CPY.SRC(ADDRESS)":
			return zoweHelperCommand("", "member not found", 8)
		case "HQL.COB.SRC(ADDRESS)":
			return zoweHelperCommand("05 ADDRESS PIC X(10).\n", "", 0)
		default:
			t.Errorf("unexpected DSN %q", dsn)
			return zoweHelperCommand("", "unexpected DSN", 8)
		}
	})
	resolver := newDSNCopyResolver([]string{"HQL.CPY.SRC", "HQL.COB.SRC"})

	for range 2 {
		src, err := resolver.Resolve("ADDRESS")
		if err != nil {
			t.Fatalf("Resolve() error = %v", err)
		}
		if src != "05 ADDRESS PIC X(10).\n" {
			t.Fatalf("Resolve() = %q, want the member from HQL.COB.SRC", src)
		}
	}
	want := []string{"HQL.COB.SRC(ADDRESS)", "HQL.CPY.SRC(ADDRESS)"}
	if got := sortedCalls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Zowe calls = %q, want %q", got, want)
	}
}

func TestDSNCopyResolverPrefersEarlierLibrary(t *testing.T) {
	sortedCalls := recordZoweCalls(t, func(dsn string) *exec.Cmd {
		switch dsn {
		case "HQL.CPY.SRC(ADDRESS)":
			return zoweHelperCommand("05 ADDRESS PIC X(10).\n", "", 0)
		case "HQL.COB.SRC(ADDRESS)":
			return zoweHelperCommand("05 ADDRESS PIC X(99).\n", "", 0)
		default:
			t.Errorf("unexpected DSN %q", dsn)
			return zoweHelperCommand("", "unexpected DSN", 8)
		}
	})
	resolver := newDSNCopyResolver([]string{"HQL.CPY.SRC", "HQL.COB.SRC"})

	src, err := resolver.Resolve("ADDRESS")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if src != "05 ADDRESS PIC X(10).\n" {
		t.Fatalf("Resolve() = %q, want the member from HQL.CPY.SRC even when both libraries have it", src)
	}
	if got := sortedCalls(); len(got) != 2 {
		t.Fatalf("Zowe calls = %q, want both libraries probed", got)
	}
}

func TestDSNCopyResolverCachesFailuresAndListsThemInSearchOrder(t *testing.T) {
	sortedCalls := recordZoweCalls(t, func(dsn string) *exec.Cmd {
		switch dsn {
		case "HQL.CPY.SRC(ADDRESS)":
			return zoweHelperCommand("", "first library failure", 8)
		case "HQL.COB.SRC(ADDRESS)":
			return zoweHelperCommand("", "second library failure", 8)
		default:
			t.Errorf("unexpected DSN %q", dsn)
			return zoweHelperCommand("", "unexpected DSN", 8)
		}
	})
	resolver := newDSNCopyResolver([]string{"HQL.CPY.SRC", "HQL.COB.SRC"})

	for range 2 {
		_, err := resolver.Resolve("ADDRESS")
		if err == nil {
			t.Fatal("Resolve() error = nil, want failure from every library")
		}
		first := strings.Index(err.Error(), "first library failure")
		second := strings.Index(err.Error(), "second library failure")
		if first < 0 || second < 0 || second < first {
			t.Fatalf("Resolve() error = %v, want failures in search order", err)
		}
	}
	if got := sortedCalls(); len(got) != 2 {
		t.Fatalf("Zowe calls = %q, want the failed lookup cached after one probe per library", got)
	}
}

func TestDSNCopyResolverRequiresSearchPath(t *testing.T) {
	resolver := newDSNCopyResolver(nil)

	_, err := resolver.Resolve("ADDRESS")
	if err == nil || !strings.Contains(err.Error(), "DSNSearchPath") {
		t.Fatalf("Resolve() error = %v, want config guidance", err)
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
		}
		if path, ok := zoweDownloadPath(args, dataDSN); ok {
			return zoweDownloadHelperCommand(path, []byte("BOB"), "", 0)
		}
		t.Fatalf("unexpected zowe args: %q", args)
		return nil
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
		}
		if path, ok := zoweDownloadPath(args, dataDSN); ok {
			return zoweDownloadHelperCommand(path, []byte("BO"), "connection lost", 8)
		}
		t.Fatalf("unexpected zowe args: %q", args)
		return nil
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
	if err == nil || !strings.Contains(err.Error(), "connection lost") {
		t.Fatalf("run() error = %v, want Zowe failure", err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("run() output = %q, want none before Zowe download succeeds", got)
	}
}

func TestRunExpandsNestedCopiesThroughSearchPath(t *testing.T) {
	const copybookDSN = "HQ.COPYLIB(CUSTOMER)"
	const dataDSN = "HQ.CUSTOMER.DATA"
	configRoot := t.TempDir()
	stubUserConfigDir(t, configRoot, nil)
	configDir := filepath.Join(configRoot, "cq")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"DSNSearchPath":["HQL.CPY.SRC","HQL.COB.SRC"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	originalCommand := zoweCommand
	var mu sync.Mutex
	var calls []string
	zoweCommand = func(args ...string) *exec.Cmd {
		dsn := args[3]
		mu.Lock()
		calls = append(calls, dsn)
		mu.Unlock()
		switch dsn {
		case copybookDSN:
			return zoweHelperCommand("01 CUSTOMER.\n   COPY DETAILS.\n", "", 0)
		case "HQL.CPY.SRC(DETAILS)":
			return zoweHelperCommand("", "member not found", 8)
		case "HQL.COB.SRC(DETAILS)":
			return zoweHelperCommand("05 NAME PIC X(3).\n COPY FLAGS.\n", "", 0)
		case "HQL.CPY.SRC(FLAGS)":
			return zoweHelperCommand("05 FLAG PIC X(1).\n", "", 0)
		case "HQL.COB.SRC(FLAGS)":
			return zoweHelperCommand("05 FLAG PIC X(9).\n", "", 0)
		case dataDSN:
			path, ok := zoweDownloadPath(args, dataDSN)
			if !ok {
				t.Errorf("unexpected zowe download args: %q", args)
				return zoweHelperCommand("", "unexpected args", 8)
			}
			return zoweDownloadHelperCommand(path, []byte("BOBY"), "", 0)
		default:
			t.Errorf("unexpected zowe args: %q", args)
			return zoweHelperCommand("", "unexpected DSN", 8)
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
	if !strings.Contains(string(got), `"NAME":"BOB"`) || !strings.Contains(string(got), `"FLAG":"Y"`) {
		t.Fatalf("run() output = %s, want nested COPY fields", got)
	}
	// Library probes run concurrently, so compare the calls sorted. Both
	// libraries are probed for every member; search order still picks the
	// one-byte FLAG from HQL.CPY.SRC over the nine-byte one.
	wantCalls := []string{
		copybookDSN,
		"HQL.COB.SRC(DETAILS)",
		"HQL.COB.SRC(FLAGS)",
		"HQL.CPY.SRC(DETAILS)",
		"HQL.CPY.SRC(FLAGS)",
		dataDSN,
	}
	sort.Strings(wantCalls)
	sort.Strings(calls)
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("Zowe calls = %q, want %q", calls, wantCalls)
	}
}
