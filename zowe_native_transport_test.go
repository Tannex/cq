package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func zoweSessionForServer(t *testing.T, server *httptest.Server) zoweSession {
	t.Helper()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return zoweSession{
		Profile: "test", Protocol: u.Scheme, Host: host, Port: port,
		User: "IBMUSER", Password: "secret", RejectUnauthorized: true,
	}
}

func TestNativeZoweTransportFetchesCopybookWithToken(t *testing.T) {
	var requestErr error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasCSRFHeader := r.Header[http.CanonicalHeaderKey("X-CSRF-ZOSMF-HEADER")]
		switch {
		case r.Method != http.MethodGet:
			requestErr = fmt.Errorf("method = %s", r.Method)
		case r.URL.Path != "/gateway/zosmf/restfiles/ds/HQ.COPYLIB(CUSTOMER)":
			requestErr = fmt.Errorf("path = %s", r.URL.Path)
		case r.Header.Get("X-IBM-Data-Type") != "text":
			requestErr = fmt.Errorf("data type = %s", r.Header.Get("X-IBM-Data-Type"))
		case !hasCSRFHeader:
			requestErr = fmt.Errorf("missing X-CSRF-ZOSMF-HEADER")
		case r.Header.Get("Cookie") != "LtpaToken2=token-value":
			requestErr = fmt.Errorf("cookie = %s", r.Header.Get("Cookie"))
		}
		_, _ = io.WriteString(w, "01 CUSTOMER.\n  05 NAME PIC X(3).\n")
	}))
	defer server.Close()
	session := zoweSessionForServer(t, server)
	session.BasePath = "/gateway"
	session.TokenType = "LtpaToken2"
	session.TokenValue = "token-value"
	transport := newNativeZoweTransport(session)

	got, err := transport.fetchCopybook(context.Background(), "HQ.COPYLIB(CUSTOMER)")
	if err != nil {
		t.Fatalf("fetchCopybook() error = %v", err)
	}
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	if !strings.Contains(string(got), "01 CUSTOMER") {
		t.Fatalf("fetchCopybook() = %q", got)
	}
}

func TestNativeZoweTransportRejectsUntrustedTLSCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "must not be trusted")
	}))
	defer server.Close()
	transport := newNativeZoweTransport(zoweSessionForServer(t, server))

	_, err := transport.fetchCopybook(context.Background(), "HQ.COPYLIB(CUSTOMER)")
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("fetchCopybook() error = %v, want certificate verification failure", err)
	}
}

func TestNativeZoweTransportStreamsBinaryWithBasicAuth(t *testing.T) {
	want := []byte{0x00, 0xff, 0xc1, 0x12}
	var requestErr error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		switch {
		case !ok || user != "IBMUSER" || password != "secret":
			requestErr = fmt.Errorf("basic auth = %q/%q/%v", user, password, ok)
		case r.Header.Get("X-IBM-Data-Type") != "binary":
			requestErr = fmt.Errorf("data type = %s", r.Header.Get("X-IBM-Data-Type"))
		}
		_, _ = w.Write(want)
	}))
	defer server.Close()
	transport := newNativeZoweTransport(zoweSessionForServer(t, server))

	stream, err := transport.openDataSet("HQ.DATA", downloadHint{})
	if err != nil {
		t.Fatalf("openDataSet() error = %v", err)
	}
	got, readErr := io.ReadAll(stream)
	closeErr := stream.Close()
	if err := errorsJoin(readErr, closeErr); err != nil {
		t.Fatal(err)
	}
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	if string(got) != string(want) {
		t.Fatalf("download = %v, want %v", got, want)
	}
}

func TestNativeZoweTransportDecodesRangedRecords(t *testing.T) {
	var requestErr error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-IBM-Data-Type"); got != "record" {
			requestErr = fmt.Errorf("data type = %q", got)
		}
		if got := r.Header.Get("X-IBM-Record-Range"); got != "0,2" {
			requestErr = fmt.Errorf("record range = %q", got)
		}
		for _, record := range [][]byte{[]byte("ABC"), []byte("DEF")} {
			_ = binary.Write(w, binary.BigEndian, uint32(len(record)))
			_, _ = w.Write(record)
		}
	}))
	defer server.Close()
	transport := newNativeZoweTransport(zoweSessionForServer(t, server))

	stream, err := transport.openDataSet("HQ.DATA", downloadHint{Records: 2, RecordLength: 3})
	if err != nil {
		t.Fatalf("openDataSet() error = %v", err)
	}
	defer stream.Close()
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	if string(got) != "ABCDEF" {
		t.Fatalf("download = %q, want ABCDEF", got)
	}
}

func TestNativeZoweTransportFallsBackWithoutDuplicatingRecords(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-IBM-Data-Type") == "record" {
			_ = binary.Write(w, binary.BigEndian, uint32(3))
			_, _ = io.WriteString(w, "ABC")
			_ = binary.Write(w, binary.BigEndian, uint32(4)) // unexpected LRECL
			_, _ = io.WriteString(w, "WXYZ")
			return
		}
		_, _ = io.WriteString(w, "ABCDEF")
	}))
	defer server.Close()
	transport := newNativeZoweTransport(zoweSessionForServer(t, server))

	stream, err := transport.openDataSet("HQ.DATA", downloadHint{Records: 2, RecordLength: 3})
	if err != nil {
		t.Fatalf("openDataSet() error = %v", err)
	}
	defer stream.Close()
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(got) != "ABCDEF" {
		t.Fatalf("download = %q, want fallback without duplicated ABC", got)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want ranged request plus plain fallback", requests.Load())
	}
}

func TestNativeZoweTransportFallsBackAfterRangeHTTPError(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("X-IBM-Data-Type") == "record" {
			http.Error(w, "record mode unsupported", http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, "ABCDEF")
	}))
	defer server.Close()
	transport := newNativeZoweTransport(zoweSessionForServer(t, server))

	stream, err := transport.openDataSet("HQ.DATA", downloadHint{Records: 2, RecordLength: 3})
	if err != nil {
		t.Fatalf("openDataSet() error = %v", err)
	}
	defer stream.Close()
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ABCDEF" || requests.Load() != 2 {
		t.Fatalf("download = %q, requests = %d", got, requests.Load())
	}
}

func TestNativeZoweTransportFormatsZOSMFError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"data set is not cataloged"}`)
	}))
	defer server.Close()
	transport := newNativeZoweTransport(zoweSessionForServer(t, server))

	_, err := transport.fetchCopybook(context.Background(), "HQ.MISSING")
	if err == nil || !strings.Contains(err.Error(), "z/OSMF 404") || !strings.Contains(err.Error(), "not cataloged") {
		t.Fatalf("fetchCopybook() error = %v", err)
	}
}

func TestRunSelectsCodepageForZoweDataSet(t *testing.T) {
	tests := []struct {
		name     string
		encoding string
		data     []byte
		args     []string
	}{
		{name: "profile encoding", encoding: "ascii", data: []byte("BOB")},
		{name: "explicit flag", encoding: "cp037", data: []byte("BOB"), args: []string{"-codepage", "ascii"}},
		{name: "cp037 fallback", data: []byte{0xc2, 0xd6, 0xc2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Header.Get("X-IBM-Data-Type") {
				case "text":
					_, _ = io.WriteString(w, "01 CUSTOMER.\n  05 NAME PIC X(3).\n")
				case "binary":
					_, _ = w.Write(tt.data)
				default:
					http.Error(w, "unexpected data type", http.StatusBadRequest)
				}
			}))
			defer server.Close()
			session := zoweSessionForServer(t, server)
			session.Encoding = tt.encoding
			originalLoader := loadDefaultZoweSession
			loadDefaultZoweSession = func() (zoweSession, error) { return session, nil }
			t.Cleanup(func() { loadDefaultZoweSession = originalLoader })
			stubUserConfigDir(t, t.TempDir(), nil)
			stdout := captureStdout(t)

			args := []string{
				"--copybook-dsn", "HQ.COPYLIB(CUSTOMER)",
				"--data-dsn", "HQ.CUSTOMER.DATA",
			}
			args = append(args, tt.args...)
			err := runWithArgs(t, args...)
			got := stdout()
			if err != nil {
				t.Fatalf("run() error = %v", err)
			}
			if !strings.Contains(got, `"NAME":"BOB"`) {
				t.Fatalf("run() output = %s", got)
			}
		})
	}
}

func TestRunReportsUnsupportedZoweEncoding(t *testing.T) {
	copybook := filepath.Join(t.TempDir(), "customer.cpy")
	if err := os.WriteFile(copybook, []byte("01 CUSTOMER.\n  05 NAME PIC X(3).\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalLoader := loadDefaultZoweSession
	loadDefaultZoweSession = func() (zoweSession, error) {
		return zoweSession{Encoding: "utf-8"}, nil
	}
	t.Cleanup(func() { loadDefaultZoweSession = originalLoader })
	stubUserConfigDir(t, t.TempDir(), nil)

	err := runWithArgs(t, "-c", copybook, "--data-dsn", "HQ.CUSTOMER.DATA")
	if err == nil || !strings.Contains(err.Error(), `decode: unknown codepage "utf-8"`) {
		t.Fatalf("run() error = %v, want normal unsupported-codepage error", err)
	}
}

func TestRunLocalInputDoesNotLoadZoweConfiguration(t *testing.T) {
	var loads atomic.Int32
	originalLoader := loadDefaultZoweSession
	loadDefaultZoweSession = func() (zoweSession, error) {
		loads.Add(1)
		return zoweSession{}, fmt.Errorf("must not be called")
	}
	t.Cleanup(func() { loadDefaultZoweSession = originalLoader })
	stubUserConfigDir(t, t.TempDir(), nil)
	stdout := captureStdout(t)
	root := t.TempDir()
	copybook := filepath.Join(root, "customer.cpy")
	data := filepath.Join(root, "customer.bin")
	if err := os.WriteFile(copybook, []byte("01 CUSTOMER.\n  05 NAME PIC X(3).\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(data, []byte{0xc2, 0xd6, 0xc2}, 0o600); err != nil {
		t.Fatal(err)
	}

	err := runWithArgs(t, "-c", copybook, "-d", data)
	got := stdout()
	if err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !strings.Contains(got, `"NAME":"BOB"`) {
		t.Fatalf("run() output = %s", got)
	}
	if loads.Load() != 0 {
		t.Fatalf("Zowe config loads = %d, want zero for local input", loads.Load())
	}
}

// errorsJoin keeps this test file compatible with the same multi-error check
// used by production without obscuring which close/read operation failed.
func errorsJoin(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
