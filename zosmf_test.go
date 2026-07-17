package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type fakeCopybookMember struct {
	data          []byte
	err           error
	waitForCancel bool
}

type fakeCopybookFetcher struct {
	members map[string]fakeCopybookMember

	mu       sync.Mutex
	requests []string
}

func (f *fakeCopybookFetcher) fetchCopybook(ctx context.Context, dsn string) ([]byte, error) {
	f.mu.Lock()
	f.requests = append(f.requests, dsn)
	f.mu.Unlock()
	member, ok := f.members[dsn]
	if !ok {
		return nil, fmt.Errorf("data set not found: %s", dsn)
	}
	if member.waitForCancel {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if member.err != nil {
		return nil, member.err
	}
	return append([]byte(nil), member.data...), nil
}

func (f *fakeCopybookFetcher) requestedDSNs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	requests := append([]string(nil), f.requests...)
	sort.Strings(requests)
	return requests
}

type zosmfServerMember struct {
	text         string
	binary       []byte
	err          string
	recordLength int
}

type zosmfServerRequest struct {
	dsn         string
	dataType    string
	recordRange string
}

type testZOSMFServer struct {
	mu       sync.Mutex
	requests []zosmfServerRequest
}

func (s *testZOSMFServer) record(req zosmfServerRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
}

func (s *testZOSMFServer) allRequests() []zosmfServerRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]zosmfServerRequest(nil), s.requests...)
}

func (s *testZOSMFServer) requestedDSNs() []string {
	requests := s.allRequests()
	dsns := make([]string, 0, len(requests))
	for _, req := range requests {
		dsns = append(dsns, req.dsn)
	}
	sort.Strings(dsns)
	return dsns
}

func configureTestZOSMF(t *testing.T, members map[string]zosmfServerMember, searchPaths ...string) *testZOSMFServer {
	t.Helper()
	fixture := &testZOSMFServer{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "/zosmf/restfiles/ds/"
		dsn := strings.TrimPrefix(r.URL.Path, prefix)
		dataType := r.Header.Get("X-IBM-Data-Type")
		fixture.record(zosmfServerRequest{
			dsn: dsn, dataType: dataType,
			recordRange: r.Header.Get("X-IBM-Record-Range"),
		})

		member, ok := members[dsn]
		if !ok || member.err != "" {
			message := member.err
			status := http.StatusServiceUnavailable
			if !ok {
				message = "data set not found: " + dsn
				status = http.StatusNotFound
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]string{"message": message})
			return
		}

		switch dataType {
		case "text":
			_, _ = io.WriteString(w, member.text)
		case "binary":
			_, _ = w.Write(member.binary)
		case "record":
			parts := strings.Split(r.Header.Get("X-IBM-Record-Range"), ",")
			count, err := strconv.Atoi(parts[len(parts)-1])
			if err != nil || member.recordLength <= 0 {
				http.Error(w, "invalid record request", http.StatusBadRequest)
				return
			}
			for offset, n := 0, 0; offset+member.recordLength <= len(member.binary) && n < count; offset, n = offset+member.recordLength, n+1 {
				record := member.binary[offset : offset+member.recordLength]
				_ = binary.Write(w, binary.BigEndian, uint32(len(record)))
				_, _ = w.Write(record)
			}
		default:
			http.Error(w, "unexpected data type", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)

	originalLoader := loadDefaultZoweSession
	loadDefaultZoweSession = func() (zoweSession, error) {
		return zosmfSessionForServer(t, server), nil
	}
	t.Cleanup(func() { loadDefaultZoweSession = originalLoader })

	root := t.TempDir()
	stubUserConfigDir(t, root, nil)
	configDir := filepath.Join(root, "cq")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configJSON, err := json.Marshal(config{DSNSearchPath: searchPaths})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), configJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestDSNCopyResolverUsesSearchOrderAndCache(t *testing.T) {
	transport := &fakeCopybookFetcher{members: map[string]fakeCopybookMember{
		"HQL.COB.SRC(ADDRESS)": {data: []byte("05 ADDRESS PIC X(10).\n")},
	}}
	resolver := newDSNCopyResolver([]string{"HQL.CPY.SRC", "HQL.COB.SRC"}, transport)

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
	if got := transport.requestedDSNs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("data set requests = %q, want one probe per library, then cache hits", got)
	}
}

func TestDSNCopyResolverPrefersEarlierLibrary(t *testing.T) {
	transport := &fakeCopybookFetcher{members: map[string]fakeCopybookMember{
		"HQL.CPY.SRC(ADDRESS)": {data: []byte("05 ADDRESS PIC X(10).\n")},
		"HQL.COB.SRC(ADDRESS)": {data: []byte("05 ADDRESS PIC X(99).\n")},
	}}
	resolver := newDSNCopyResolver([]string{"HQL.CPY.SRC", "HQL.COB.SRC"}, transport)

	src, err := resolver.Resolve("ADDRESS")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if src != "05 ADDRESS PIC X(10).\n" {
		t.Fatalf("Resolve() = %q, want the member from HQL.CPY.SRC even when both libraries have it", src)
	}
	got := transport.requestedDSNs()
	for _, dsn := range got {
		if dsn != "HQL.CPY.SRC(ADDRESS)" && dsn != "HQL.COB.SRC(ADDRESS)" {
			t.Fatalf("data set requests = %q, want only search-path probes", got)
		}
	}
	if !slices.Contains(got, "HQL.CPY.SRC(ADDRESS)") {
		t.Fatalf("data set requests = %q, want the winning library probed", got)
	}
}

func TestDSNCopyResolverDoesNotWaitForSlowerLaterLibrary(t *testing.T) {
	transport := &fakeCopybookFetcher{members: map[string]fakeCopybookMember{
		"HQL.CPY.SRC(ADDRESS)": {data: []byte("05 ADDRESS PIC X(10).\n")},
		"HQL.COB.SRC(ADDRESS)": {waitForCancel: true},
	}}
	resolver := newDSNCopyResolver([]string{"HQL.CPY.SRC", "HQL.COB.SRC"}, transport)

	src, err := resolver.Resolve("ADDRESS")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if src != "05 ADDRESS PIC X(10).\n" {
		t.Fatalf("Resolve() = %q, want the member from HQL.CPY.SRC", src)
	}
}

func TestDSNCopyResolverCachesFailuresAndListsThemInSearchOrder(t *testing.T) {
	transport := &fakeCopybookFetcher{members: map[string]fakeCopybookMember{
		"HQL.CPY.SRC(ADDRESS)": {err: errors.New("first library failure")},
		"HQL.COB.SRC(ADDRESS)": {err: errors.New("second library failure")},
	}}
	resolver := newDSNCopyResolver([]string{"HQL.CPY.SRC", "HQL.COB.SRC"}, transport)

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
	if got := transport.requestedDSNs(); len(got) != 2 {
		t.Fatalf("data set requests = %q, want the failed lookup cached after one probe per library", got)
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
	configureTestZOSMF(t, map[string]zosmfServerMember{
		"HQ.COPYLIB(CUSTOMER)": {text: "01 CUSTOMER.\n   05 NAME PIC X(3).\n"},
		"HQ.CUSTOMER.DATA":     {err: "connection lost"},
	})
	stdout := captureStdout(t)

	err := runWithArgs(
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
	fixture := configureTestZOSMF(t, map[string]zosmfServerMember{
		"HQ.COPYLIB(CUSTOMER)": {text: "01 CUSTOMER.\n   COPY DETAILS.\n"},
		"HQL.COB.SRC(DETAILS)": {text: "05 NAME PIC X(3).\n COPY FLAGS.\n"},
		"HQL.CPY.SRC(FLAGS)":   {text: "05 FLAG PIC X(1).\n"},
		"HQL.COB.SRC(FLAGS)":   {text: "05 FLAG PIC X(9).\n"},
		"HQ.CUSTOMER.DATA":     {binary: []byte("BOBY")},
	}, "HQL.CPY.SRC", "HQL.COB.SRC")
	stdout := captureStdout(t)

	err := runWithArgs(
		"--copybook-dsn", "HQ.COPYLIB(CUSTOMER)",
		"--data-dsn", "HQ.CUSTOMER.DATA",
		"-codepage", "ascii",
	)
	got := stdout()
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(got, `"NAME":"BOB"`) || !strings.Contains(got, `"FLAG":"Y"`) {
		t.Fatalf("run() output = %s, want nested COPY fields", got)
	}
	required := []string{
		"HQ.COPYLIB(CUSTOMER)",
		"HQ.CUSTOMER.DATA",
		"HQL.COB.SRC(DETAILS)",
		"HQL.CPY.SRC(DETAILS)",
		"HQL.CPY.SRC(FLAGS)",
	}
	dsns := fixture.requestedDSNs()
	for _, dsn := range required {
		if !slices.Contains(dsns, dsn) {
			t.Fatalf("data set requests = %q, want them to include %q", dsns, dsn)
		}
	}
	allowed := append(required, "HQL.COB.SRC(FLAGS)")
	for _, dsn := range dsns {
		if !slices.Contains(allowed, dsn) {
			t.Fatalf("data set requests = %q, want only %q", dsns, allowed)
		}
	}
}

func TestRunVerboseWritesDebugToStderrOnly(t *testing.T) {
	configureTestZOSMF(t, map[string]zosmfServerMember{
		"HQ.COPYLIB(CUSTOMER)": {text: "01 CUSTOMER.\n   05 NAME PIC X(3).\n"},
		"HQ.CUSTOMER.DATA":     {binary: []byte("BOB")},
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
		runErr := runWithArgs(args...)
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
		"z/OSMF view HQ.COPYLIB(CUSTOMER)",
		"z/OSMF download HQ.CUSTOMER.DATA",
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

func TestRunMaxSendsRecordRange(t *testing.T) {
	fixture := configureTestZOSMF(t, map[string]zosmfServerMember{
		"HQ.COPYLIB(CUSTOMER)": {text: "01 CUSTOMER.\n   05 NAME PIC X(3).\n   05 FLAG PIC X(1).\n"},
		"HQ.CUSTOMER.DATA":     {binary: []byte("BOBYSUEN"), recordLength: 4},
	})
	stdout := captureStdout(t)

	err := runWithArgs(
		"--copybook-dsn", "HQ.COPYLIB(CUSTOMER)",
		"--data-dsn", "HQ.CUSTOMER.DATA",
		"-codepage", "ascii",
		"-max", "1",
	)
	got := stdout()
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(got, `"NAME":"BOB"`) || strings.Contains(got, `"NAME":"SUE"`) {
		t.Fatalf("run() output = %s, want only the first record", got)
	}
	for _, req := range fixture.allRequests() {
		if req.dsn == "HQ.CUSTOMER.DATA" {
			if req.dataType != "record" || req.recordRange != "0,1" {
				t.Fatalf("download request = %+v, want a one-record range", req)
			}
			return
		}
	}
	t.Fatal("no data-set download request reached z/OSMF")
}

func TestRunWhereKeepsFullDownload(t *testing.T) {
	copybook := "01 CUSTOMER.\n" +
		"   05 FLAG PIC X(1).\n" +
		"      88 FLAG-Y VALUE \"Y\".\n" +
		"   05 NAME PIC X(3).\n"
	fixture := configureTestZOSMF(t, map[string]zosmfServerMember{
		"HQ.COPYLIB(CUSTOMER)": {text: copybook},
		"HQ.CUSTOMER.DATA":     {binary: []byte("NBOBYSUE"), recordLength: 4},
	})
	stdout := captureStdout(t)

	err := runWithArgs(
		"--copybook-dsn", "HQ.COPYLIB(CUSTOMER)",
		"--data-dsn", "HQ.CUSTOMER.DATA",
		"-codepage", "ascii",
		"-max", "1",
		"-where", "FLAG-Y",
	)
	got := stdout()
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(got, `"NAME":"SUE"`) {
		t.Fatalf("run() output = %s, want the matching record", got)
	}
	for _, req := range fixture.allRequests() {
		if req.dsn == "HQ.CUSTOMER.DATA" && (req.dataType != "binary" || req.recordRange != "") {
			t.Fatalf("download request = %+v, want an unbounded binary stream with -where", req)
		}
	}
}
