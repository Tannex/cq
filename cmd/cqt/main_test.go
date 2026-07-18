package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tannex/cq/internal/buildinfo"
	"github.com/Tannex/cq/internal/cqt"
	"github.com/Tannex/cq/internal/zowe"
)

func TestLoadSessionReturnsWhenContextIsCanceled(t *testing.T) {
	originalLoader := loadDefaultSession
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	loadDefaultSession = func() (zowe.Session, error) {
		close(started)
		<-release
		close(finished)
		return zowe.Session{}, nil
	}
	t.Cleanup(func() {
		close(release)
		<-finished
		loadDefaultSession = originalLoader
	})

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := loadSession(ctx)
		result <- err
	}()
	<-started
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("loadSession error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("loadSession did not return after cancellation")
	}
}

func TestLoadSessionMapsLoadedSession(t *testing.T) {
	originalLoader := loadDefaultSession
	loadDefaultSession = func() (zowe.Session, error) {
		return zowe.Session{
			Protocol: "https", Host: "example.com", Port: 443,
			User: "IBMUSER", Encoding: "cp1047",
		}, nil
	}
	t.Cleanup(func() { loadDefaultSession = originalLoader })

	session, err := loadSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.Browser == nil || session.User != "IBMUSER" || session.Encoding != "cp1047" {
		t.Fatalf("session = %#v", session)
	}
}

func TestRunPrintsCQTVersionWithoutStartingProgram(t *testing.T) {
	originalVersion := buildinfo.Version
	buildinfo.Version = "v2.3.4"
	t.Cleanup(func() { buildinfo.Version = originalVersion })
	originalRunner := runProgram
	called := false
	runProgram = func(*cqt.Model) error {
		called = true
		return nil
	}
	t.Cleanup(func() { runProgram = originalRunner })

	var stdout, stderr bytes.Buffer
	if err := run([]string{"--version"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "cqt v2.3.4\n" || stderr.Len() != 0 || called {
		t.Fatalf("stdout=%q stderr=%q called=%v", stdout.String(), stderr.String(), called)
	}
}

func TestRunSupportsApprovedFlagsAndAliases(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	originalRunner := runProgram
	called := 0
	runProgram = func(*cqt.Model) error {
		called++
		return nil
	}
	t.Cleanup(func() { runProgram = originalRunner })

	for _, arguments := range [][]string{
		{"--prefix", "IBMUSER.*", "-c", "customer.cpy", "--format", "free", "--record", "CUSTOMER", "--codepage", "cp037"},
		{"--prefix", "IBMUSER.*", "--copybook", "customer.cpy"},
		{"--prefix", "IBMUSER.*", "--copybook-dsn", "HLQ.COPYLIB(CUSTOMER)"},
	} {
		var stdout, stderr bytes.Buffer
		if err := run(arguments, &stdout, &stderr); err != nil {
			t.Fatalf("run(%v): %v stderr=%q", arguments, err, stderr.String())
		}
	}
	if called != 3 {
		t.Fatalf("program calls = %d", called)
	}
}

func TestRunRejectsInvalidFlagCombinationsAndPositionals(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"--copybook", "a.cpy", "--copybook-dsn", "HLQ.CPY(A)"}, want: "at most one copybook source"},
		{args: []string{"--format", "variable"}, want: "auto, fixed, or free"},
		{args: []string{"unexpected"}, want: "unexpected positional arguments"},
	}
	for _, test := range tests {
		var stdout, stderr bytes.Buffer
		err := run(test.args, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("run(%v) error = %v, want %q", test.args, err, test.want)
		}
	}
}
