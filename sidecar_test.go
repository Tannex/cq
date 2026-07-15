package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var testExecutable = os.Args[0]

// fakeMember is one data set served by the fake sidecar. Text carries UTF-8
// content, B64 binary content; Chunk splits the response into frames of that
// many bytes; Slow streams the content over and over until canceled.
type fakeMember struct {
	Text  string `json:"text,omitempty"`
	B64   string `json:"b64,omitempty"`
	Error string `json:"error,omitempty"`
	Chunk int    `json:"chunk,omitempty"`
	Slow  bool   `json:"slow,omitempty"`
}

// TestSidecarHelperProcess is not a test: re-invoked via -test.run it acts
// as a fake sidecar speaking the protocol from fixtures in the environment.
func TestSidecarHelperProcess(t *testing.T) {
	if os.Getenv("CQ_SIDECAR_HELPER") != "1" {
		return
	}
	fakeSidecarMain()
	os.Exit(0)
}

func fakeSidecarMain() {
	var members map[string]fakeMember
	if err := json.Unmarshal([]byte(os.Getenv("CQ_SIDECAR_DATA")), &members); err != nil {
		members = nil
	}

	var outMu sync.Mutex
	emit := func(frame map[string]any) {
		b, _ := json.Marshal(frame)
		outMu.Lock()
		_, _ = os.Stdout.Write(append(b, '\n'))
		outMu.Unlock()
	}

	if msg := os.Getenv("CQ_SIDECAR_FAIL"); msg != "" {
		emit(map[string]any{"error": msg})
		os.Exit(1)
	}
	emit(map[string]any{"ready": true})

	var reqLog *os.File
	if path := os.Getenv("CQ_SIDECAR_REQLOG"); path != "" {
		reqLog, _ = os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	}

	var mu sync.Mutex
	canceled := make(map[uint64]chan struct{})

	serve := func(req sidecarRequest, stop chan struct{}) {
		m, ok := members[req.DSN]
		if !ok || m.Error != "" {
			msg := m.Error
			if msg == "" {
				msg = "data set not found: " + req.DSN
			}
			emit(map[string]any{"id": req.ID, "error": msg})
			return
		}
		content := []byte(m.Text)
		if m.B64 != "" {
			content, _ = base64.StdEncoding.DecodeString(m.B64)
		}
		// A records hint bounds the transfer like a server-side record range.
		if req.Op == "download" && req.Records > 0 && req.Reclen > 0 && !m.Slow {
			if limit := req.Records * req.Reclen; limit < len(content) {
				content = content[:limit]
			}
		}
		chunk := len(content)
		if m.Chunk > 0 {
			chunk = m.Chunk
		}
		for {
			for i := 0; i < len(content); i += chunk {
				end := min(i+chunk, len(content))
				select {
				case <-stop:
					return
				default:
				}
				emit(map[string]any{"id": req.ID, "data": base64.StdEncoding.EncodeToString(content[i:end])})
			}
			if !m.Slow {
				emit(map[string]any{"id": req.ID, "end": true})
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var req sidecarRequest
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			emit(map[string]any{"error": "bad request: " + sc.Text()})
			continue
		}
		if reqLog != nil {
			_, _ = reqLog.Write(append(append([]byte{}, sc.Bytes()...), '\n'))
		}
		if req.Op == "cancel" {
			mu.Lock()
			if stop, ok := canceled[req.ID]; ok {
				close(stop)
				delete(canceled, req.ID)
			}
			mu.Unlock()
			continue
		}
		stop := make(chan struct{})
		mu.Lock()
		canceled[req.ID] = stop
		mu.Unlock()
		go serve(req, stop)
	}
}

func fakeSidecarCommand(t *testing.T, members map[string]fakeMember) string {
	t.Helper()
	b, err := json.Marshal(members)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CQ_SIDECAR_HELPER", "1")
	t.Setenv("CQ_SIDECAR_DATA", string(b))
	t.Setenv("CQ_SIDECAR_REQLOG", filepath.Join(t.TempDir(), "requests.ndjson"))
	return testExecutable + " -test.run=TestSidecarHelperProcess"
}

// fakeSidecarRequests returns the view/download requests the fake sidecar
// received, sorted by DSN: probes run concurrently, so arrival order is not
// deterministic.
func fakeSidecarRequests(t *testing.T) []sidecarRequest {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("CQ_SIDECAR_REQLOG"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var reqs []sidecarRequest
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var req sidecarRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatalf("bad request log line %q: %v", line, err)
		}
		if req.Op != "cancel" {
			reqs = append(reqs, req)
		}
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].DSN < reqs[j].DSN })
	return reqs
}

func fakeSidecarDSNs(t *testing.T) []string {
	t.Helper()
	var dsns []string
	for _, req := range fakeSidecarRequests(t) {
		dsns = append(dsns, req.DSN)
	}
	return dsns
}

func startFakeSidecar(t *testing.T, members map[string]fakeMember) *zoweSidecar {
	t.Helper()
	s, err := startSidecar(fakeSidecarCommand(t, members))
	if err != nil {
		t.Fatalf("startSidecar() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// configureFakeSidecar points cq's user config at a fake sidecar serving the
// given members, so run() exercises the real transport wiring.
func configureFakeSidecar(t *testing.T, members map[string]fakeMember, searchPaths ...string) {
	t.Helper()
	configRoot := t.TempDir()
	stubUserConfigDir(t, configRoot, nil)
	configDir := filepath.Join(configRoot, "cq")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	command := fakeSidecarCommand(t, members)
	configJSON, err := json.Marshal(config{DSNSearchPath: searchPaths, Sidecar: command})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), configJSON, 0o600); err != nil {
		t.Fatal(err)
	}
}

// captureStdout redirects os.Stdout for the test; the returned func stops
// capturing and returns everything written.
func captureStdout(t *testing.T) func() string {
	t.Helper()
	out, err := os.CreateTemp(t.TempDir(), "cq-output-*.json")
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = out
	return func() string {
		os.Stdout = original
		if err := out.Close(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(out.Name())
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
}

func TestSidecarFetchesCopybookAcrossChunks(t *testing.T) {
	const copybook = "01 CUSTOMER.\n   05 NAME PIC X(10).\n"
	s := startFakeSidecar(t, map[string]fakeMember{
		"HQ.COPYLIB(CUSTOMER)": {Text: copybook, Chunk: 7},
	})

	got, err := s.fetchCopybook(context.Background(), "HQ.COPYLIB(CUSTOMER)")
	if err != nil {
		t.Fatalf("fetchCopybook() error = %v", err)
	}
	if string(got) != copybook {
		t.Fatalf("fetchCopybook() = %q, want %q", got, copybook)
	}

	_, err = s.fetchCopybook(context.Background(), "HQ.COPYLIB(MISSING)")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("fetchCopybook(missing) error = %v, want not-found", err)
	}
}

func TestSidecarStreamsBinaryDataSet(t *testing.T) {
	binary := []byte{0x00, 0x0e, 0xff, 'B', 'O', 'B', '\n', 0x7f}
	s := startFakeSidecar(t, map[string]fakeMember{
		"HQ.CUSTOMER.DATA": {B64: base64.StdEncoding.EncodeToString(binary), Chunk: 3},
	})

	rc, err := s.openDataSet("HQ.CUSTOMER.DATA", downloadHint{})
	if err != nil {
		t.Fatalf("openDataSet() error = %v", err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading stream: %v", err)
	}
	if string(got) != string(binary) {
		t.Fatalf("stream = %v, want %v", got, binary)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestSidecarStartupErrorSurfaces(t *testing.T) {
	t.Setenv("CQ_SIDECAR_HELPER", "1")
	t.Setenv("CQ_SIDECAR_FAIL", "no default zosmf profile found")

	_, err := startSidecar(testExecutable + " -test.run=TestSidecarHelperProcess")
	if err == nil || !strings.Contains(err.Error(), "no default zosmf profile found") {
		t.Fatalf("startSidecar() error = %v, want the sidecar's startup error", err)
	}
}

func TestLazySidecarDefaultCommandErrorExplainsSetup(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no cq-zowe-sidecar anywhere

	l := newLazySidecar("")
	t.Cleanup(func() { _ = l.Close() })
	_, err := l.fetchCopybook(context.Background(), "HQ.COPYLIB(CUSTOMER)")
	if err == nil || !strings.Contains(err.Error(), "npm install") {
		t.Fatalf("fetchCopybook() error = %v, want setup guidance", err)
	}
}

func TestSidecarStreamCloseCancelsAndKeepsMuxUsable(t *testing.T) {
	s := startFakeSidecar(t, map[string]fakeMember{
		"HQ.ENDLESS.DATA":  {Text: strings.Repeat("X", 64), Chunk: 8, Slow: true},
		"HQ.COPYLIB(TINY)": {Text: "05 A PIC X.\n"},
	})

	rc, err := s.openDataSet("HQ.ENDLESS.DATA", downloadHint{})
	if err != nil {
		t.Fatalf("openDataSet() error = %v", err)
	}
	buf := make([]byte, 16)
	if _, err := io.ReadFull(rc, buf); err != nil {
		t.Fatalf("reading start of endless stream: %v", err)
	}
	if err := rc.Close(); err != nil { // must cancel, not hang
		t.Fatalf("Close() error = %v", err)
	}
	// Reads past any buffered bytes must fail on the closed stream, not wait
	// for frames the abandoned request will never receive.
	if _, err := io.ReadAll(rc); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Read() after Close error = %v, want io.ErrClosedPipe", err)
	}

	got, err := s.fetchCopybook(context.Background(), "HQ.COPYLIB(TINY)")
	if err != nil {
		t.Fatalf("fetchCopybook() after cancel error = %v", err)
	}
	if string(got) != "05 A PIC X.\n" {
		t.Fatalf("fetchCopybook() after cancel = %q", got)
	}
}

func TestRunWithSidecarStreamsEndToEnd(t *testing.T) {
	configureFakeSidecar(t, map[string]fakeMember{
		"HQ.COPYLIB(CUSTOMER)": {Text: "01 CUSTOMER.\n   COPY DETAILS.\n"},
		"HQL.CPY.SRC(DETAILS)": {Text: "05 NAME PIC X(3).\n05 FLAG PIC X(1).\n"},
		"HQ.CUSTOMER.DATA":     {B64: base64.StdEncoding.EncodeToString([]byte("BOBY")), Chunk: 2},
	}, "HQL.CPY.SRC")
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
	if !strings.Contains(got, `"NAME":"BOB"`) || !strings.Contains(got, `"FLAG":"Y"`) {
		t.Fatalf("run() output = %s, want record decoded through the sidecar", got)
	}
}

func TestRunMaxSendsRecordHint(t *testing.T) {
	configureFakeSidecar(t, map[string]fakeMember{
		"HQ.COPYLIB(CUSTOMER)": {Text: "01 CUSTOMER.\n   05 NAME PIC X(3).\n   05 FLAG PIC X(1).\n"},
		"HQ.CUSTOMER.DATA":     {B64: base64.StdEncoding.EncodeToString([]byte("BOBYSUEN"))},
	})
	stdout := captureStdout(t)

	err := runWithArgs(t,
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
	for _, req := range fakeSidecarRequests(t) {
		if req.Op == "download" {
			if req.Records != 1 || req.Reclen != 4 {
				t.Fatalf("download request = %+v, want records=1 reclen=4", req)
			}
			return
		}
	}
	t.Fatal("no download request reached the sidecar")
}

func TestRunWhereKeepsFullDownload(t *testing.T) {
	copybook := "01 CUSTOMER.\n" +
		"   05 FLAG PIC X(1).\n" +
		"      88 FLAG-Y VALUE \"Y\".\n" +
		"   05 NAME PIC X(3).\n"
	configureFakeSidecar(t, map[string]fakeMember{
		"HQ.COPYLIB(CUSTOMER)": {Text: copybook},
		"HQ.CUSTOMER.DATA":     {B64: base64.StdEncoding.EncodeToString([]byte("NBOBYSUE"))},
	})
	stdout := captureStdout(t)

	err := runWithArgs(t,
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
	for _, req := range fakeSidecarRequests(t) {
		if req.Op == "download" && req.Records != 0 {
			t.Fatalf("download request = %+v, want no record bound with -where", req)
		}
	}
}
