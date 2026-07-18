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
	"github.com/Tannex/cq/internal/zosmf"
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
		_, err := loadSession(ctx, "")
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

	session, err := loadSession(context.Background(), "")
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

func TestRunDemoModeWiresModelWithoutError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	originalRunner := runProgram
	var model *cqt.Model
	runProgram = func(m *cqt.Model) error {
		model = m
		return nil
	}
	t.Cleanup(func() { runProgram = originalRunner })

	var stdout, stderr bytes.Buffer
	if err := run([]string{"--demo"}, &stdout, &stderr); err != nil {
		t.Fatalf("run(--demo) error = %v", err)
	}
	if model == nil {
		t.Fatal("run(--demo) did not create a model")
	}
}

func TestDemoSessionReturnsConfiguredSession(t *testing.T) {
	session, err := loadDemoSession(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if session.User != "DEMOUSER" || session.Encoding != "latin1" || session.Browser == nil {
		t.Fatalf("demo session = %#v", session)
	}
}

func TestDemoBrowserPagesDataSets(t *testing.T) {
	browser := &demoBrowser{}

	first, err := browser.ListDataSets(context.Background(), zosmf.ListDataSetsRequest{Prefix: "DEMO.*", MaxItems: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 10 || !first.MoreRows {
		t.Fatalf("first page = %d items, more=%v", len(first.Items), first.MoreRows)
	}
	for i := 1; i < len(first.Items); i++ {
		if first.Items[i-1].Name >= first.Items[i].Name {
			t.Fatalf("items not sorted: %q before %q", first.Items[i-1].Name, first.Items[i].Name)
		}
	}

	mid := first.Items[len(first.Items)/2].Name
	midPage, err := browser.ListDataSets(context.Background(), zosmf.ListDataSetsRequest{
		Prefix:   "DEMO.*",
		Start:    mid,
		MaxItems: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(midPage.Items) == 0 {
		t.Fatal("mid-list page returned no items")
	}
	if midPage.Items[0].Name <= mid {
		t.Fatalf("mid-list page did not start after %q: first=%q", mid, midPage.Items[0].Name)
	}
	if !midPage.MoreRows && len(first.Items)+len(midPage.Items) < len(demoDataSetDefs) {
		t.Fatalf("mid-list page incorrectly reported end of data")
	}

	lastStart := "DEMO.TEST.DATA"
	lastPage, err := browser.ListDataSets(context.Background(), zosmf.ListDataSetsRequest{
		Prefix:   "DEMO.*",
		Start:    lastStart,
		MaxItems: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantLast := 1
	if len(lastPage.Items) != wantLast || lastPage.MoreRows {
		t.Fatalf("last page = %d items, more=%v, want %d/false", len(lastPage.Items), lastPage.MoreRows, wantLast)
	}
	if lastPage.Items[0].Name != "DEMO.UTILITY.CTL" {
		t.Fatalf("last page first item = %q, want DEMO.UTILITY.CTL", lastPage.Items[0].Name)
	}
}

func TestDemoBrowserPagesMembersAndRecords(t *testing.T) {
	browser := &demoBrowser{}

	members, err := browser.ListMembers(context.Background(), zosmf.ListMembersRequest{
		DataSet:  "DEMO.COPYLIB",
		MaxItems: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(members.Items) < 8 || len(members.Items) > 12 {
		t.Fatalf("member count = %d, want 8..12", len(members.Items))
	}
	if members.MoreRows {
		t.Fatal("member page reported more rows when all fit")
	}

	records, err := browser.ReadRecords(context.Background(), zosmf.ReadRecordsRequest{
		DataSet:  "DEMO.CUSTOMER.MASTER",
		Start:    50,
		MaxItems: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(records.Records) != 10 {
		t.Fatalf("record window = %d, want 10", len(records.Records))
	}
	if records.Records[0].Number != 51 {
		t.Fatalf("first record number = %d, want 51", records.Records[0].Number)
	}
	if !records.MoreRows {
		t.Fatal("record page should report more rows")
	}

	end, err := browser.ReadRecords(context.Background(), zosmf.ReadRecordsRequest{
		DataSet:  "DEMO.CUSTOMER.MASTER",
		Start:    115,
		MaxItems: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(end.Records) != 5 || end.MoreRows {
		t.Fatalf("tail page = %d items, more=%v, want 5/false", len(end.Records), end.MoreRows)
	}
}

func TestDemoBrowserFetchTextReturnsValidCopybook(t *testing.T) {
	browser := &demoBrowser{}
	raw, err := browser.FetchText(context.Background(), "DEMO.COPYLIB(CUSTREC)")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "01  CUSTOMER-REC") {
		t.Fatalf("copybook missing record: %q", string(raw))
	}
	if !strings.Contains(string(raw), "CUST-ID") {
		t.Fatalf("copybook missing fields: %q", string(raw))
	}
}

func TestDemoBrowserRespectsContextCancellation(t *testing.T) {
	browser := &demoBrowser{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := browser.ListDataSets(ctx, zosmf.ListDataSetsRequest{Prefix: "DEMO.*", MaxItems: 10}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListDataSets error = %v, want context.Canceled", err)
	}
	if _, err := browser.ListMembers(ctx, zosmf.ListMembersRequest{DataSet: "DEMO.COPYLIB"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ListMembers error = %v, want context.Canceled", err)
	}
	if _, err := browser.ReadRecords(ctx, zosmf.ReadRecordsRequest{DataSet: "DEMO.CUSTOMER.MASTER"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadRecords error = %v, want context.Canceled", err)
	}
}
