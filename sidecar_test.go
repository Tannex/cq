package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

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
// as a fake sidecar speaking the protocol from fixtures in the environment,
// the same trick TestZoweHelperProcess uses for the zowe CLI.
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

	var mu sync.Mutex
	canceled := make(map[uint64]chan struct{})

	serve := func(id uint64, dsn string, stop chan struct{}) {
		m, ok := members[dsn]
		if !ok || m.Error != "" {
			msg := m.Error
			if msg == "" {
				msg = "data set not found: " + dsn
			}
			emit(map[string]any{"id": id, "error": msg})
			return
		}
		content := []byte(m.Text)
		if m.B64 != "" {
			content, _ = base64.StdEncoding.DecodeString(m.B64)
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
				emit(map[string]any{"id": id, "data": base64.StdEncoding.EncodeToString(content[i:end])})
			}
			if !m.Slow {
				emit(map[string]any{"id": id, "end": true})
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
		go serve(req.ID, req.DSN, stop)
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
	return testExecutable + " -test.run=TestSidecarHelperProcess"
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

func TestSidecarFetchesCopybookAcrossChunks(t *testing.T) {
	const copybook = "01 CUSTOMER.\n   05 NAME PIC X(10).\n"
	s := startFakeSidecar(t, map[string]fakeMember{
		"HQ.COPYLIB(CUSTOMER)": {Text: copybook, Chunk: 7},
	})

	got, err := s.fetchCopybook("HQ.COPYLIB(CUSTOMER)")
	if err != nil {
		t.Fatalf("fetchCopybook() error = %v", err)
	}
	if string(got) != copybook {
		t.Fatalf("fetchCopybook() = %q, want %q", got, copybook)
	}

	_, err = s.fetchCopybook("HQ.COPYLIB(MISSING)")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("fetchCopybook(missing) error = %v, want not-found", err)
	}
}

func TestSidecarStreamsBinaryDataSet(t *testing.T) {
	binary := []byte{0x00, 0x0e, 0xff, 'B', 'O', 'B', '\n', 0x7f}
	s := startFakeSidecar(t, map[string]fakeMember{
		"HQ.CUSTOMER.DATA": {B64: base64.StdEncoding.EncodeToString(binary), Chunk: 3},
	})

	rc, err := s.openDataSet("HQ.CUSTOMER.DATA")
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

func TestSidecarResolverProbesShareOneProcess(t *testing.T) {
	s := startFakeSidecar(t, map[string]fakeMember{
		"HQL.COB.SRC(ADDRESS)": {Text: "05 ADDRESS PIC X(10).\n"},
	})
	resolver := newDSNCopyResolver([]string{"HQL.CPY.SRC", "HQL.COB.SRC"}, s)

	src, err := resolver.Resolve("ADDRESS")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if src != "05 ADDRESS PIC X(10).\n" {
		t.Fatalf("Resolve() = %q, want the member from HQL.COB.SRC", src)
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

func TestSidecarStreamCloseCancelsAndKeepsMuxUsable(t *testing.T) {
	s := startFakeSidecar(t, map[string]fakeMember{
		"HQ.ENDLESS.DATA":  {Text: strings.Repeat("X", 64), Chunk: 8, Slow: true},
		"HQ.COPYLIB(TINY)": {Text: "05 A PIC X.\n"},
	})

	rc, err := s.openDataSet("HQ.ENDLESS.DATA")
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

	got, err := s.fetchCopybook("HQ.COPYLIB(TINY)")
	if err != nil {
		t.Fatalf("fetchCopybook() after cancel error = %v", err)
	}
	if string(got) != "05 A PIC X.\n" {
		t.Fatalf("fetchCopybook() after cancel = %q", got)
	}
}

func TestRunWithSidecarStreamsEndToEnd(t *testing.T) {
	configRoot := t.TempDir()
	stubUserConfigDir(t, configRoot, nil)
	configDir := filepath.Join(configRoot, "cq")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"DSNSearchPath":["HQL.CPY.SRC"]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	originalCommand := zoweCommand
	zoweCommand = func(args ...string) *exec.Cmd {
		t.Errorf("zowe CLI invoked with %q despite --sidecar", args)
		return zoweHelperCommand("", "unexpected CLI call", 8)
	}
	t.Cleanup(func() { zoweCommand = originalCommand })

	command := fakeSidecarCommand(t, map[string]fakeMember{
		"HQ.COPYLIB(CUSTOMER)": {Text: "01 CUSTOMER.\n   COPY DETAILS.\n"},
		"HQL.CPY.SRC(DETAILS)": {Text: "05 NAME PIC X(3).\n05 FLAG PIC X(1).\n"},
		"HQ.CUSTOMER.DATA":     {B64: base64.StdEncoding.EncodeToString([]byte("BOBY")), Chunk: 2},
	})

	out, err := os.CreateTemp(t.TempDir(), "cq-output-*.json")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	originalStdout := os.Stdout
	os.Stdout = out
	t.Cleanup(func() { os.Stdout = originalStdout })

	err = runWithArgs(t,
		"--sidecar", command,
		"--copybook-dsn", "HQ.COPYLIB(CUSTOMER)",
		"--data-dsn", "HQ.CUSTOMER.DATA",
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
		t.Fatalf("run() output = %s, want record decoded through the sidecar", got)
	}
}
