package main

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestDSNCopyResolverUsesSearchOrderAndCache(t *testing.T) {
	s := startFakeSidecar(t, map[string]fakeMember{
		"HQL.COB.SRC(ADDRESS)": {Text: "05 ADDRESS PIC X(10).\n"},
	})
	resolver := newDSNCopyResolver([]string{"HQL.CPY.SRC", "HQL.COB.SRC"}, s)

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
	if got := fakeSidecarDSNs(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("sidecar requests = %q, want one probe per library, then cache hits", got)
	}
}

func TestDSNCopyResolverPrefersEarlierLibrary(t *testing.T) {
	s := startFakeSidecar(t, map[string]fakeMember{
		"HQL.CPY.SRC(ADDRESS)": {Text: "05 ADDRESS PIC X(10).\n"},
		"HQL.COB.SRC(ADDRESS)": {Text: "05 ADDRESS PIC X(99).\n"},
	})
	resolver := newDSNCopyResolver([]string{"HQL.CPY.SRC", "HQL.COB.SRC"}, s)

	src, err := resolver.Resolve("ADDRESS")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if src != "05 ADDRESS PIC X(10).\n" {
		t.Fatalf("Resolve() = %q, want the member from HQL.CPY.SRC even when both libraries have it", src)
	}
	// The winning library must have been probed; the losing probe may have
	// been canceled before its request went out.
	got := fakeSidecarDSNs(t)
	for _, dsn := range got {
		if dsn != "HQL.CPY.SRC(ADDRESS)" && dsn != "HQL.COB.SRC(ADDRESS)" {
			t.Fatalf("sidecar requests = %q, want only search-path probes", got)
		}
	}
	if !slices.Contains(got, "HQL.CPY.SRC(ADDRESS)") {
		t.Fatalf("sidecar requests = %q, want the winning library probed", got)
	}
}

func TestDSNCopyResolverDoesNotWaitForSlowerLaterLibrary(t *testing.T) {
	// The second library streams forever; resolution must return as soon as
	// the first library's copy arrives instead of waiting for every probe.
	s := startFakeSidecar(t, map[string]fakeMember{
		"HQL.CPY.SRC(ADDRESS)": {Text: "05 ADDRESS PIC X(10).\n"},
		"HQL.COB.SRC(ADDRESS)": {Text: strings.Repeat("X", 64), Chunk: 8, Slow: true},
	})
	resolver := newDSNCopyResolver([]string{"HQL.CPY.SRC", "HQL.COB.SRC"}, s)

	src, err := resolver.Resolve("ADDRESS")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if src != "05 ADDRESS PIC X(10).\n" {
		t.Fatalf("Resolve() = %q, want the member from HQL.CPY.SRC", src)
	}
}

func TestDSNCopyResolverCachesFailuresAndListsThemInSearchOrder(t *testing.T) {
	s := startFakeSidecar(t, map[string]fakeMember{
		"HQL.CPY.SRC(ADDRESS)": {Error: "first library failure"},
		"HQL.COB.SRC(ADDRESS)": {Error: "second library failure"},
	})
	resolver := newDSNCopyResolver([]string{"HQL.CPY.SRC", "HQL.COB.SRC"}, s)

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
	if got := fakeSidecarDSNs(t); len(got) != 2 {
		t.Fatalf("sidecar requests = %q, want the failed lookup cached after one probe per library", got)
	}
}

func TestDSNCopyResolverRequiresSearchPath(t *testing.T) {
	resolver := newDSNCopyResolver(nil, nil)

	_, err := resolver.Resolve("ADDRESS")
	if err == nil || !strings.Contains(err.Error(), "dsnSearchPath") {
		t.Fatalf("Resolve() error = %v, want config guidance", err)
	}
}

func TestRunReportsDataSetErrorBeforeOutput(t *testing.T) {
	configureFakeSidecar(t, map[string]fakeMember{
		"HQ.COPYLIB(CUSTOMER)": {Text: "01 CUSTOMER.\n   05 NAME PIC X(3).\n"},
		"HQ.CUSTOMER.DATA":     {Error: "connection lost"},
	})
	stdout := captureStdout(t)

	err := runWithArgs(t,
		"--copybook-dsn", "HQ.COPYLIB(CUSTOMER)",
		"--data-dsn", "HQ.CUSTOMER.DATA",
		"-codepage", "ascii",
	)
	got := stdout()
	if err == nil || !strings.Contains(err.Error(), "connection lost") {
		t.Fatalf("run() error = %v, want the data set failure", err)
	}
	if len(got) != 0 {
		t.Fatalf("run() output = %q, want none when the download fails", got)
	}
}

func TestRunExpandsNestedCopiesThroughSearchPath(t *testing.T) {
	configureFakeSidecar(t, map[string]fakeMember{
		"HQ.COPYLIB(CUSTOMER)": {Text: "01 CUSTOMER.\n   COPY DETAILS.\n"},
		"HQL.COB.SRC(DETAILS)": {Text: "05 NAME PIC X(3).\n COPY FLAGS.\n"},
		"HQL.CPY.SRC(FLAGS)":   {Text: "05 FLAG PIC X(1).\n"},
		"HQL.COB.SRC(FLAGS)":   {Text: "05 FLAG PIC X(9).\n"},
		"HQ.CUSTOMER.DATA":     {B64: base64.StdEncoding.EncodeToString([]byte("BOBY"))},
	}, "HQL.CPY.SRC", "HQL.COB.SRC")
	stdout := captureStdout(t)

	err := runWithArgs(t,
		"--copybook-dsn", "HQ.COPYLIB(CUSTOMER)",
		"--data-dsn", "HQ.CUSTOMER.DATA",
		"-codepage", "ascii",
	)
	got := stdout()
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	// Search order still picks the one-byte FLAG from HQL.CPY.SRC over the
	// nine-byte one, even though both libraries were probed.
	if !strings.Contains(got, `"NAME":"BOB"`) || !strings.Contains(got, `"FLAG":"Y"`) {
		t.Fatalf("run() output = %s, want nested COPY fields", got)
	}
	// FLAGS resolves from HQL.CPY.SRC, so the losing HQL.COB.SRC(FLAGS)
	// probe may have been canceled before its request went out.
	required := []string{
		"HQ.COPYLIB(CUSTOMER)",
		"HQ.CUSTOMER.DATA",
		"HQL.COB.SRC(DETAILS)",
		"HQL.CPY.SRC(DETAILS)",
		"HQL.CPY.SRC(FLAGS)",
	}
	dsns := fakeSidecarDSNs(t)
	for _, dsn := range required {
		if !slices.Contains(dsns, dsn) {
			t.Fatalf("sidecar requests = %q, want them to include %q", dsns, dsn)
		}
	}
	allowed := append(required, "HQL.COB.SRC(FLAGS)")
	for _, dsn := range dsns {
		if !slices.Contains(allowed, dsn) {
			t.Fatalf("sidecar requests = %q, want only %q", dsns, allowed)
		}
	}
}

func TestRunVerboseWritesDebugToStderrOnly(t *testing.T) {
	configureFakeSidecar(t, map[string]fakeMember{
		"HQ.COPYLIB(CUSTOMER)": {Text: "01 CUSTOMER.\n   05 NAME PIC X(3).\n"},
		"HQ.CUSTOMER.DATA":     {B64: base64.StdEncoding.EncodeToString([]byte("BOB"))},
	})

	captureRun := func(args ...string) (string, string) {
		t.Helper()
		errFile, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
		if err != nil {
			t.Fatal(err)
		}
		stdout := captureStdout(t)
		origErr := os.Stderr
		os.Stderr = errFile
		runErr := runWithArgs(t, args...)
		os.Stderr = origErr
		out := stdout()
		if err := errors.Join(errFile.Close(), runErr); err != nil {
			t.Fatalf("run() error = %v", err)
		}
		stderr, err := os.ReadFile(errFile.Name())
		if err != nil {
			t.Fatal(err)
		}
		return out, string(stderr)
	}

	stdout, stderr := captureRun("--verbose",
		"--copybook-dsn", "HQ.COPYLIB(CUSTOMER)",
		"--data-dsn", "HQ.CUSTOMER.DATA",
		"-codepage", "ascii",
	)
	if !strings.Contains(stdout, `"NAME":"BOB"`) {
		t.Fatalf("run() stdout = %s, want decoded record", stdout)
	}
	if strings.Contains(stdout, "cq: ") {
		t.Fatalf("run() stdout = %s, want no debug lines", stdout)
	}
	for _, want := range []string{
		"sidecar view HQ.COPYLIB(CUSTOMER)",
		"sidecar download HQ.CUSTOMER.DATA",
		"record CUSTOMER: 3 bytes per record",
		"decoded 1 records",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("verbose stderr = %q, want it to contain %q", stderr, want)
		}
	}

	stdout, stderr = captureRun(
		"--copybook-dsn", "HQ.COPYLIB(CUSTOMER)",
		"--data-dsn", "HQ.CUSTOMER.DATA",
		"-codepage", "ascii",
	)
	if !strings.Contains(stdout, `"NAME":"BOB"`) {
		t.Fatalf("run() stdout = %s, want decoded record", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr without --verbose = %q, want empty", stderr)
	}
}
