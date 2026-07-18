package cqt

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/layout"
	"github.com/Tannex/cq/internal/record"
	"github.com/Tannex/cq/internal/zosmf"
	"github.com/Tannex/cq/internal/zowe"
)

type fakeBrowser struct {
	dataSetRequests []zosmf.ListDataSetsRequest
	memberRequests  []zosmf.ListMembersRequest
	recordRequests  []zosmf.ReadRecordsRequest
	fetchRequests   []string

	listDataSets func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error)
	listMembers  func(context.Context, zosmf.ListMembersRequest) (zosmf.MemberPage, error)
	readRecords  func(context.Context, zosmf.ReadRecordsRequest) (zosmf.RecordPage, error)
	fetchText    func(context.Context, string) ([]byte, error)
	encoding     string
}

func (f *fakeBrowser) ListDataSets(ctx context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
	f.dataSetRequests = append(f.dataSetRequests, request)
	if f.listDataSets != nil {
		return f.listDataSets(ctx, request)
	}
	return zosmf.DataSetPage{}, nil
}

func (f *fakeBrowser) ListMembers(ctx context.Context, request zosmf.ListMembersRequest) (zosmf.MemberPage, error) {
	f.memberRequests = append(f.memberRequests, request)
	if f.listMembers != nil {
		return f.listMembers(ctx, request)
	}
	return zosmf.MemberPage{}, nil
}

func (f *fakeBrowser) ReadRecords(ctx context.Context, request zosmf.ReadRecordsRequest) (zosmf.RecordPage, error) {
	f.recordRequests = append(f.recordRequests, request)
	if f.readRecords != nil {
		return f.readRecords(ctx, request)
	}
	return zosmf.RecordPage{}, nil
}

func (f *fakeBrowser) FetchText(ctx context.Context, dsn string) ([]byte, error) {
	f.fetchRequests = append(f.fetchRequests, dsn)
	if f.fetchText != nil {
		return f.fetchText(ctx, dsn)
	}
	return nil, fmt.Errorf("missing %s", dsn)
}

func (f *fakeBrowser) Encoding() (string, error) { return f.encoding, nil }

func newTestModel(t *testing.T, options Options, browser zosmf.Browser, user, encoding string) *Model {
	t.Helper()
	model, err := NewModel(options, Dependencies{
		LoadSession: func(context.Context, string) (Session, error) {
			return Session{Browser: browser, User: user, Encoding: encoding}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func applyMessage(t *testing.T, model *Model, message tea.Msg) tea.Cmd {
	t.Helper()
	updated, cmd := model.Update(message)
	if updated != model {
		t.Fatal("model pointer changed")
	}
	return cmd
}

func executeCommand(t *testing.T, model *Model, command tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{command}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == nil {
			continue
		}
		message := current()
		if batch, ok := message.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		if next := applyMessage(t, model, message); next != nil {
			queue = append(queue, next)
		}
	}
}

func browseResultMessage(t *testing.T, command tea.Cmd) tea.Msg {
	t.Helper()
	queue := []tea.Cmd{command}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == nil {
			continue
		}
		message := current()
		if batch, ok := message.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		switch message.(type) {
		case dataSetsResultMsg, membersResultMsg, recordsResultMsg:
			return message
		}
	}
	t.Fatal("command produced no browse result")
	return nil
}

func decodeResultMessage(t *testing.T, command tea.Cmd) decodeResultMsg {
	t.Helper()
	queue := []tea.Cmd{command}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == nil {
			continue
		}
		message := current()
		if batch, ok := message.(tea.BatchMsg); ok {
			queue = append(queue, batch...)
			continue
		}
		if result, ok := message.(decodeResultMsg); ok {
			return result
		}
	}
	t.Fatal("command produced no decode result")
	return decodeResultMsg{}
}

func readyModel(t *testing.T, options Options, browser zosmf.Browser, user, encoding string, width, height int) *Model {
	t.Helper()
	model := newTestModel(t, options, browser, user, encoding)
	applyMessage(t, model, tea.WindowSizeMsg{Width: width, Height: height})
	executeCommand(t, model, model.Init())
	return model
}

func TestModelEndToEndBrowsesSequentialAndPartitionedDataSets(t *testing.T) {
	var dataTypes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dataTypes = append(dataTypes, r.Header.Get("X-IBM-Data-Type"))
		switch r.URL.Path {
		case "/zosmf/restfiles/ds":
			if got := r.URL.Query().Get("dslevel"); got != "A*" {
				t.Errorf("dslevel = %q, want A*", got)
			}
			if got := r.Header.Get("X-IBM-Max-Items"); got != "10" {
				t.Errorf("data set max items = %q, want 10", got)
			}
			_, _ = io.WriteString(w, `{"items":[{"dsname":"A.DATA","dsorg":"PS"},{"dsname":"A.PDS","dsorg":"PO"}],"returnedRows":2,"moreRows":false}`)
		case "/zosmf/restfiles/ds/A.DATA":
			if got := r.Header.Get("X-IBM-Data-Type"); got != "record" {
				t.Errorf("sequential data type = %q", got)
			}
			if got := r.Header.Get("X-IBM-Record-Range"); got != "0,10" {
				t.Errorf("sequential range = %q, want 0,10", got)
			}
			for _, value := range []string{"ONE", "TWO"} {
				_ = binary.Write(w, binary.BigEndian, uint32(len(value)))
				_, _ = io.WriteString(w, value)
			}
		case "/zosmf/restfiles/ds/A.PDS/member":
			if got := r.Header.Get("X-IBM-Max-Items"); got != "10" {
				t.Errorf("member max items = %q, want 10", got)
			}
			_, _ = io.WriteString(w, `{"items":[{"member":"MEM1"}],"returnedRows":1,"moreRows":false}`)
		case "/zosmf/restfiles/ds/A.PDS(MEM1)":
			if got := r.Header.Get("X-IBM-Data-Type"); got != "record" {
				t.Errorf("member data type = %q", got)
			}
			if got := r.Header.Get("X-IBM-Record-Range"); got != "0,10" {
				t.Errorf("member range = %q, want 0,10", got)
			}
			_ = binary.Write(w, binary.BigEndian, uint32(3))
			_, _ = io.WriteString(w, "MEM")
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

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
	browser := zosmf.New(zowe.Session{
		Protocol: u.Scheme, Host: host, Port: port,
		User: "IBMUSER", Password: "secret", RejectUnauthorized: true,
	}, nil)
	model := readyModel(t, Options{Prefix: "A*", Codepage: "latin1"}, browser, "IBMUSER", "", 90, 10)
	if model.budget != 10 || len(model.datasets) != 2 {
		t.Fatalf("initial bounded data set window: budget=%d rows=%d", model.budget, len(model.datasets))
	}

	executeCommand(t, model, model.openSelection())
	if model.screen != ScreenRecords || len(model.records) != 2 || string(model.records[1].Record.Data) != "TWO" {
		t.Fatalf("sequential record flow screen=%d records=%#v", model.screen, model.records)
	}
	executeCommand(t, model, model.navigateBack())
	model.datasetPage.move(1)
	executeCommand(t, model, model.openSelection())
	if model.screen != ScreenMembers || len(model.members) != 1 || model.members[0].Name != "MEM1" {
		t.Fatalf("member flow screen=%d members=%#v", model.screen, model.members)
	}
	executeCommand(t, model, model.openSelection())
	if model.screen != ScreenRecords || len(model.records) != 1 || string(model.records[0].Record.Data) != "MEM" {
		t.Fatalf("member record flow screen=%d records=%#v", model.screen, model.records)
	}
	for _, dataType := range dataTypes {
		if dataType == "binary" {
			t.Fatal("TUI flow fell back to an unbounded binary download")
		}
	}
}

func TestModelDefersRowsUntilSafeTerminalAndUsesExactBudget(t *testing.T) {
	browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "IBMUSER.DATA", Organization: "PS"}}}, nil
	}}
	model := newTestModel(t, Options{}, browser, "ibmuser", "")

	executeCommand(t, model, model.Init())
	if len(browser.dataSetRequests) != 0 {
		t.Fatalf("session completion fetched rows without a safe terminal: %#v", browser.dataSetRequests)
	}
	if model.prefix != "IBMUSER.*" {
		t.Fatalf("default prefix = %q", model.prefix)
	}
	if cmd := applyMessage(t, model, tea.WindowSizeMsg{Width: MinTerminalWidth - 1, Height: 30}); cmd != nil {
		t.Fatal("tiny terminal dispatched a row command")
	}
	if len(browser.dataSetRequests) != 0 || model.budget != 0 {
		t.Fatalf("tiny state requests=%d budget=%d", len(browser.dataSetRequests), model.budget)
	}

	command := applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 15})
	if command == nil {
		t.Fatal("safe resize did not dispatch initial data set request")
	}
	executeCommand(t, model, command)
	if len(browser.dataSetRequests) != 1 {
		t.Fatalf("requests = %#v", browser.dataSetRequests)
	}
	request := browser.dataSetRequests[0]
	if request.Prefix != "IBMUSER.*" || request.Start != "" || request.MaxItems != 20 {
		t.Fatalf("bounded request = %#v", request)
	}
	if len(model.datasets) != 1 || model.datasetPage.budget != 20 {
		t.Fatalf("model window = %#v", model.datasetPage)
	}
}

func TestTokenOnlySessionFocusesPrefixWithoutAutomaticQuery(t *testing.T) {
	browser := &fakeBrowser{}
	model := readyModel(t, Options{}, browser, "", "cp037", 90, 15)
	if len(browser.dataSetRequests) != 0 {
		t.Fatalf("token-only session queried without a prefix: %#v", browser.dataSetRequests)
	}
	if !model.prefixInput.Focused() || model.status.Text != "enter a data set prefix" {
		t.Fatalf("prefix focus=%v status=%#v", model.prefixInput.Focused(), model.status)
	}
}

func TestDatasetRoutingMembersAndUnsupportedOrganizations(t *testing.T) {
	browser := &fakeBrowser{
		listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{
				{Name: "IBMUSER.SEQ", Organization: "PS"},
				{Name: "IBMUSER.LARGE", Organization: "PS-L"},
				{Name: "IBMUSER.PDSE", Organization: "PO-E"},
				{Name: "IBMUSER.VSAM", Organization: "VS"},
			}}, nil
		},
		readRecords: func(_ context.Context, request zosmf.ReadRecordsRequest) (zosmf.RecordPage, error) {
			return zosmf.RecordPage{Records: []zosmf.Record{{Number: 1, Data: []byte("A")}}, Start: request.Start}, nil
		},
		listMembers: func(context.Context, zosmf.ListMembersRequest) (zosmf.MemberPage, error) {
			return zosmf.MemberPage{Items: []zosmf.Member{{Name: "MEM1"}}}, nil
		},
	}
	model := readyModel(t, Options{Prefix: "IBMUSER.*", Codepage: "latin1"}, browser, "IBMUSER", "", 100, 15)

	command := model.openSelection()
	if model.screen != ScreenRecords || command == nil {
		t.Fatalf("PS route screen=%d command=%v", model.screen, command)
	}
	executeCommand(t, model, command)
	if len(browser.recordRequests) != 1 || browser.recordRequests[0].DataSet != "IBMUSER.SEQ" || browser.recordRequests[0].Member != "" || browser.recordRequests[0].MaxItems != model.budget {
		t.Fatalf("PS record request = %#v", browser.recordRequests)
	}

	executeCommand(t, model, model.navigateBack())
	model.datasetPage.move(1)
	command = model.openSelection()
	if model.screen != ScreenRecords || command == nil {
		t.Fatalf("PS-L route screen=%d command=%v", model.screen, command)
	}
	executeCommand(t, model, command)
	if len(browser.recordRequests) != 2 || browser.recordRequests[1].DataSet != "IBMUSER.LARGE" || browser.recordRequests[1].Member != "" || browser.recordRequests[1].MaxItems != model.budget {
		t.Fatalf("PS-L record request = %#v", browser.recordRequests)
	}

	executeCommand(t, model, model.navigateBack())
	model.datasetPage.move(1)
	command = model.openSelection()
	if model.screen != ScreenMembers || command == nil {
		t.Fatalf("PDSE route screen=%d command=%v", model.screen, command)
	}
	executeCommand(t, model, command)
	if len(browser.memberRequests) != 1 || browser.memberRequests[0].DataSet != "IBMUSER.PDSE" {
		t.Fatalf("member requests = %#v", browser.memberRequests)
	}

	executeCommand(t, model, model.navigateBack())
	model.datasetPage.move(1)
	if command := model.openSelection(); command != nil {
		t.Fatal("unsupported DSORG dispatched a command")
	}
	if model.status.Level != statusWarn || !strings.Contains(model.status.Text, "unsupported DSORG VS") {
		t.Fatalf("unsupported status = %#v", model.status)
	}
}

func TestModelPrefetchesNamesOneVisiblePageBeforeCacheEnd(t *testing.T) {
	browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		if request.Start == "" {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "D"}, {Name: "E"}, {Name: "F"}}, MoreRows: true}, nil
		}
		if request.Start != "F" {
			t.Fatalf("forward anchor = %q, want last cached name F", request.Start)
		}
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "G"}, {Name: "H"}, {Name: "I"}, {Name: "J"}, {Name: "K"}, {Name: "L"}}, MoreRows: true}, nil
	}}
	model := readyModel(t, Options{Prefix: "A*"}, browser, "A", "", 90, MinTerminalHeight(false))
	if model.visible != 3 || model.budget != 6 {
		t.Fatalf("visible/budget = %d/%d", model.visible, model.budget)
	}
	for range 2 {
		if command := model.moveSelection(1); command != nil {
			t.Fatal("prefetch started before the final visible page")
		}
	}
	command := model.moveSelection(1)
	if command == nil {
		t.Fatal("entering the final visible page did not prefetch")
	}
	executeCommand(t, model, command)
	last := browser.dataSetRequests[len(browser.dataSetRequests)-1]
	if last.Start != "F" || last.MaxItems != 6 || len(model.datasets) != 12 || model.datasetPage.selectedKey() != "D" {
		t.Fatalf("forward request=%#v rows=%#v selected=%q", last, model.datasets, model.datasetPage.selectedKey())
	}
}

func TestCachedBackwardNavigationDoesNotRefetch(t *testing.T) {
	browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		if request.Start == "" {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "D"}, {Name: "E"}, {Name: "F"}}, MoreRows: true}, nil
		}
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "G"}, {Name: "H"}, {Name: "I"}, {Name: "J"}, {Name: "K"}, {Name: "L"}}}, nil
	}}
	model := readyModel(t, Options{Prefix: "A*"}, browser, "A", "", 90, MinTerminalHeight(false))
	executeCommand(t, model, model.moveSelection(3))
	requests := len(browser.dataSetRequests)
	if requests != 2 || len(model.datasets) != 12 {
		t.Fatalf("requests=%d cache=%d", requests, len(model.datasets))
	}
	if command := model.moveSelection(-3); command != nil {
		t.Fatal("backward movement through cached rows dispatched a request")
	}
	if len(browser.dataSetRequests) != requests || model.datasetPage.selectedKey() != "A" {
		t.Fatalf("requests=%d selected=%q", len(browser.dataSetRequests), model.datasetPage.selectedKey())
	}
}

func TestRepeatedMovementDoesNotRestartPendingPrefetch(t *testing.T) {
	browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "D"}, {Name: "E"}, {Name: "F"}}, MoreRows: true}, nil
	}}
	model := readyModel(t, Options{Prefix: "A*"}, browser, "A", "", 90, MinTerminalHeight(false))
	pending := model.moveSelection(3)
	if pending == nil || model.browsePending == nil {
		t.Fatal("test requires pending prefetch")
	}
	generation := model.browsePending.Generation
	if command := model.moveSelection(1); command != nil {
		t.Fatal("movement restarted an in-flight prefetch")
	}
	if model.browsePending == nil || model.browsePending.Generation != generation {
		t.Fatalf("pending generation changed from %d to %#v", generation, model.browsePending)
	}
	selected := model.datasetPage.selectedKey()
	windowStart := model.datasetPage.windowStart
	executeCommand(t, model, pending)
	if model.datasetPage.selectedKey() != selected || model.datasetPage.windowStart != windowStart {
		t.Fatalf("forward result moved live cursor/window from %q/%d to %q/%d", selected, windowStart, model.datasetPage.selectedKey(), model.datasetPage.windowStart)
	}
}

func TestMemberCacheSurvivesChildRecordsAndClearsOnBackToDataSets(t *testing.T) {
	browser := &fakeBrowser{
		listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.PDS", Organization: "PO"}}}, nil
		},
		listMembers: func(context.Context, zosmf.ListMembersRequest) (zosmf.MemberPage, error) {
			return zosmf.MemberPage{Items: []zosmf.Member{{Name: "MEM1"}, {Name: "MEM2"}}}, nil
		},
		readRecords: func(context.Context, zosmf.ReadRecordsRequest) (zosmf.RecordPage, error) {
			return zosmf.RecordPage{Records: []zosmf.Record{{Number: 1, Data: []byte("X")}}}, nil
		},
	}
	model := readyModel(t, Options{Prefix: "A*", Codepage: "latin1"}, browser, "A", "", 90, 15)
	executeCommand(t, model, model.openSelection())
	memberRequests := len(browser.memberRequests)
	executeCommand(t, model, model.openSelection())
	if len(model.records) != 1 {
		t.Fatalf("record cache=%#v", model.records)
	}
	if command := model.navigateBack(); command != nil {
		t.Fatal("return to cached members refetched")
	}
	if len(model.members) != 2 || len(model.records) != 0 || len(browser.memberRequests) != memberRequests {
		t.Fatalf("members=%#v records=%#v requests=%d", model.members, model.records, len(browser.memberRequests))
	}
	if command := model.navigateBack(); command != nil {
		t.Fatal("return to cached data sets refetched")
	}
	if len(model.members) != 0 || len(model.datasets) != 1 {
		t.Fatalf("member cache=%#v data set cache=%#v", model.members, model.datasets)
	}
}

func TestMemberPrefixSearchAndNavigationToRecords(t *testing.T) {
	browser := &fakeBrowser{
		listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "HQ.PDS", Organization: "PO"}}}, nil
		},
		listMembers: func(context.Context, zosmf.ListMembersRequest) (zosmf.MemberPage, error) {
			return zosmf.MemberPage{Items: []zosmf.Member{{Name: "MEMBER1"}}}, nil
		},
		readRecords: func(_ context.Context, request zosmf.ReadRecordsRequest) (zosmf.RecordPage, error) {
			return zosmf.RecordPage{Records: []zosmf.Record{{Number: 1, Data: []byte("X")}}, Start: request.Start}, nil
		},
	}
	model := readyModel(t, Options{Prefix: "HQ.*", Codepage: "latin1"}, browser, "HQ", "", 90, 15)
	executeCommand(t, model, model.openSelection())

	model.memberInput.Focus()
	model.memberInput.SetValue("mem")
	command := model.acceptSearch()
	if !strings.Contains(model.status.Text, "filter MEM*") {
		t.Fatalf("effective member filter not echoed in status: %#v", model.status)
	}
	executeCommand(t, model, command)
	last := browser.memberRequests[len(browser.memberRequests)-1]
	if last.Pattern != "MEM*" || last.Start != "" || last.MaxItems != model.budget {
		t.Fatalf("member prefix request = %#v", last)
	}
	model.memberInput.Focus()
	model.memberInput.SetValue("EIGHTCHR")
	executeCommand(t, model, model.acceptSearch())
	if exact := browser.memberRequests[len(browser.memberRequests)-1].Pattern; exact != "EIGHTCHR" {
		t.Fatalf("eight-character member filter = %q, want exact pattern", exact)
	}
	command = model.openSelection()
	executeCommand(t, model, command)
	if model.screen != ScreenRecords || len(browser.recordRequests) != 1 || browser.recordRequests[0].Member != "MEMBER1" {
		t.Fatalf("member navigation screen=%d records=%#v", model.screen, browser.recordRequests)
	}
}

func TestLocateCachedRecordSelectsItWithoutFetching(t *testing.T) {
	browser := &fakeBrowser{
		listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "HQ.DATA", Organization: "PS"}}}, nil
		},
		readRecords: func(context.Context, zosmf.ReadRecordsRequest) (zosmf.RecordPage, error) {
			return zosmf.RecordPage{Records: []zosmf.Record{{Number: 10}, {Number: 20}, {Number: 30}}}, nil
		},
	}
	model := readyModel(t, Options{Prefix: "HQ.*"}, browser, "HQ", "", 90, 15)
	executeCommand(t, model, model.openSelection())
	before := len(browser.recordRequests)
	model.jsonVertical = 4
	model.locateInput.Focus()
	model.locateInput.SetValue("20")

	if command := model.acceptSearch(); command != nil {
		t.Fatal("cached locate dispatched a fetch")
	}
	if got := model.recordPage.selectedKey(); got != "20" {
		t.Fatalf("selected record = %q, want 20", got)
	}
	if model.jsonVertical != 0 || model.locateInput.Focused() {
		t.Fatalf("cached locate vertical=%d focused=%v", model.jsonVertical, model.locateInput.Focused())
	}
	if len(browser.recordRequests) != before {
		t.Fatalf("cached locate made %d requests, want %d", len(browser.recordRequests), before)
	}
}

func TestLocateUncachedRecordFetchesFromRequestedAnchor(t *testing.T) {
	browser := &fakeBrowser{
		listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "HQ.DATA", Organization: "PS"}}}, nil
		},
		readRecords: func(_ context.Context, request zosmf.ReadRecordsRequest) (zosmf.RecordPage, error) {
			number := request.Start
			if number == 0 {
				number = 1
			}
			return zosmf.RecordPage{Records: []zosmf.Record{{Number: number}}}, nil
		},
	}
	model := readyModel(t, Options{Prefix: "HQ.*"}, browser, "HQ", "", 90, 15)
	executeCommand(t, model, model.openSelection())
	model.locateInput.Focus()
	model.locateInput.SetValue("900")

	command := model.acceptSearch()
	if command == nil {
		t.Fatal("uncached locate did not dispatch a fetch")
	}
	executeCommand(t, model, command)
	last := browser.recordRequests[len(browser.recordRequests)-1]
	if last.Start != 900 || last.MaxItems != model.budget {
		t.Fatalf("locate request = %#v", last)
	}
	if len(model.records) != 1 || model.records[0].Record.Number != 900 || model.recordPage.selectedKey() != "900" {
		t.Fatalf("located records=%#v selected=%q", model.records, model.recordPage.selectedKey())
	}
}

func TestLocateRejectsInvalidRecordNumber(t *testing.T) {
	model := newTestModel(t, Options{}, &fakeBrowser{}, "HQ", "latin1")
	model.screen = ScreenRecords
	model.locateInput.Focus()
	model.locateInput.SetValue("not-a-number")

	if command := model.acceptSearch(); command != nil {
		t.Fatal("invalid locate dispatched a command")
	}
	if model.status.Level != statusError || model.status.Text != "record number must be a positive integer" {
		t.Fatalf("invalid locate status = %#v", model.status)
	}
	if !model.locateInput.Focused() {
		t.Fatal("invalid locate did not retain input focus")
	}
}

func TestStaleBrowseResultIsRejectedAfterPrefixGenerationChanges(t *testing.T) {
	canceledOldContext := false
	browser := &fakeBrowser{}
	browser.listDataSets = func(ctx context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		if request.Prefix == "A*" && len(browser.dataSetRequests) > 1 {
			canceledOldContext = errors.Is(ctx.Err(), context.Canceled)
		}
		name := strings.TrimSuffix(request.Prefix, "*") + ".RESULT"
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: name}}}, nil
	}
	model := readyModel(t, Options{Prefix: "A*"}, browser, "A", "", 90, 15)
	if model.datasets[0].Name != "A.RESULT" {
		t.Fatalf("initial data = %#v", model.datasets)
	}

	staleCommand := model.refresh()
	model.prefixInput.Focus()
	model.prefixInput.SetValue("B*")
	freshCommand := model.acceptSearch()
	staleMessage := browseResultMessage(t, staleCommand)
	applyMessage(t, model, staleMessage)
	if !canceledOldContext {
		t.Fatal("superseded browse command did not receive cancellation")
	}
	if len(model.datasets) != 0 || model.prefix != "B*" {
		t.Fatalf("stale result mutated new prefix state: prefix=%q data=%#v", model.prefix, model.datasets)
	}
	executeCommand(t, model, freshCommand)
	if len(model.datasets) != 1 || model.datasets[0].Name != "B.RESULT" {
		t.Fatalf("fresh result = %#v", model.datasets)
	}
}

func TestResizeUsesNewExactBudgetAndRejectsOldPendingResult(t *testing.T) {
	browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		start := 0
		if request.Start != "" {
			_, _ = fmt.Sscanf(request.Start, "A.%02d", &start)
			start++
		}
		items := make([]zosmf.DataSet, request.MaxItems)
		for i := range items {
			items[i].Name = fmt.Sprintf("A.%02d", start+i)
		}
		return zosmf.DataSetPage{Items: items, MoreRows: true}, nil
	}}
	model := readyModel(t, Options{Prefix: "A.*"}, browser, "A", "", 90, 10)
	if model.budget != 10 || browser.dataSetRequests[0].MaxItems != 10 {
		t.Fatalf("initial budget=%d requests=%#v", model.budget, browser.dataSetRequests)
	}

	growCommand := applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 15})
	if growCommand == nil || model.budget != 20 {
		t.Fatalf("grow command=%v budget=%d", growCommand, model.budget)
	}
	executeCommand(t, model, growCommand)
	if got := browser.dataSetRequests[len(browser.dataSetRequests)-1]; got.MaxItems != 20 || got.Start != "A.09" {
		t.Fatalf("grow request = %#v", got)
	}
	if len(model.datasets) != 30 {
		t.Fatalf("grown cache has %d rows, want 30", len(model.datasets))
	}

	pending := model.activePagerBottom()
	if pending == nil || mBrowseBudget(model) != 20 {
		t.Fatalf("bottom prefetch command=%v pending budget=%d", pending, mBrowseBudget(model))
	}
	shrinkCommand := applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 9})
	if shrinkCommand == nil || model.budget != 8 || mBrowseBudget(model) != 8 {
		t.Fatalf("shrink replacement=%v budget=%d pending=%d", shrinkCommand, model.budget, mBrowseBudget(model))
	}

	before := len(model.datasets)
	stale := browseResultMessage(t, pending)
	applyMessage(t, model, stale)
	if len(model.datasets) != before {
		t.Fatalf("stale old-budget result changed cache from %d to %d", before, len(model.datasets))
	}
	executeCommand(t, model, shrinkCommand)
	if got := browser.dataSetRequests[len(browser.dataSetRequests)-1]; got.MaxItems != 8 || got.Start != "A.29" {
		t.Fatalf("shrink replacement request = %#v", got)
	}
}

func mBrowseBudget(model *Model) int {
	if model.browsePending == nil {
		return 0
	}
	return model.browsePending.Budget
}

func TestResizeShrinkPreservesStatusAndRetainedRowsWithoutFetching(t *testing.T) {
	browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		items := make([]zosmf.DataSet, request.MaxItems)
		for i := range items {
			items[i].Name = fmt.Sprintf("A.%03d", i)
		}
		return zosmf.DataSetPage{Items: items, MoreRows: true}, nil
	}}
	model := readyModel(t, Options{Prefix: "A*"}, browser, "A", "", 90, 15)
	before := len(browser.dataSetRequests)
	beforeStatus := model.status
	if command := applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 10}); command != nil {
		t.Fatal("shrink dispatched a request")
	}
	if len(browser.dataSetRequests) != before || len(model.datasets) != 20 {
		t.Fatalf("requests=%d before=%d cached=%d", len(browser.dataSetRequests), before, len(model.datasets))
	}
	if model.status != beforeStatus {
		t.Fatalf("shrink changed status from %#v to %#v", beforeStatus, model.status)
	}
}

func TestModelBoundsOverReturnedRowsFromInjectedBrowser(t *testing.T) {
	makeDataSets := func(count int, organization string) []zosmf.DataSet {
		items := make([]zosmf.DataSet, count)
		for i := range items {
			items[i] = zosmf.DataSet{Name: fmt.Sprintf("A.%03d", i), Organization: organization}
		}
		return items
	}
	makeMembers := func(count int) []zosmf.Member {
		items := make([]zosmf.Member, count)
		for i := range items {
			items[i].Name = fmt.Sprintf("M%07d", i)
		}
		return items
	}
	makeRecords := func(count int) []zosmf.Record {
		items := make([]zosmf.Record, count)
		for i := range items {
			items[i] = zosmf.Record{Number: int64(i + 1), Data: []byte("X")}
		}
		return items
	}

	t.Run("data sets", func(t *testing.T) {
		browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: makeDataSets(request.MaxItems+3, "PS")}, nil
		}}
		model := readyModel(t, Options{Prefix: "A*"}, browser, "A", "", 90, 10)
		if len(model.datasets) != model.budget || !model.datasetPage.more {
			t.Fatalf("data set window=%d budget=%d more=%v", len(model.datasets), model.budget, model.datasetPage.more)
		}
	})

	t.Run("members", func(t *testing.T) {
		browser := &fakeBrowser{
			listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
				return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.PDS", Organization: "PO"}}}, nil
			},
			listMembers: func(_ context.Context, request zosmf.ListMembersRequest) (zosmf.MemberPage, error) {
				return zosmf.MemberPage{Items: makeMembers(request.MaxItems + 3)}, nil
			},
		}
		model := readyModel(t, Options{Prefix: "A*"}, browser, "A", "", 90, 10)
		executeCommand(t, model, model.openSelection())
		if len(model.members) != model.budget || !model.memberPage.more {
			t.Fatalf("member window=%d budget=%d more=%v", len(model.members), model.budget, model.memberPage.more)
		}
	})

	t.Run("records", func(t *testing.T) {
		browser := &fakeBrowser{
			listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
				return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.DATA", Organization: "PS"}}}, nil
			},
			readRecords: func(_ context.Context, request zosmf.ReadRecordsRequest) (zosmf.RecordPage, error) {
				return zosmf.RecordPage{Records: makeRecords(request.MaxItems + 3)}, nil
			},
		}
		model := readyModel(t, Options{Prefix: "A*", Codepage: "latin1"}, browser, "A", "", 90, 10)
		executeCommand(t, model, model.openSelection())
		if len(model.records) != model.budget || !model.recordPage.more {
			t.Fatalf("record window=%d budget=%d more=%v", len(model.records), model.budget, model.recordPage.more)
		}
	})
}

func TestNavigateBackReusesCachedParentWithoutRefetch(t *testing.T) {
	browser := &fakeBrowser{}
	model := newTestModel(t, Options{Prefix: "A*"}, browser, "A", "")
	model.sessionReady = true
	model.browser = browser
	model.prefix = "A*"
	model.screen = ScreenRecords
	model.visible = 2
	model.budget = 4
	model.dataSet = zosmf.DataSet{Name: "A.DATA", Organization: "PS"}
	model.datasets = []zosmf.DataSet{{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "D"}}
	model.datasetPage.reset(model.visible, model.budget)
	model.datasetPage.apply([]string{"A", "B", "C", "D"}, false, model.datasetPage.initialPlan(""))
	model.datasetPage.move(2)
	model.records = []recordRow{{Record: zosmf.Record{Number: 1, Data: []byte("X")}}}
	model.recordPage.reset(model.visible, model.budget)
	model.recordPage.apply([]string{"1"}, false, model.recordPage.initialPlan(0))

	if command := model.navigateBack(); command != nil {
		t.Fatal("returning to a cached parent unexpectedly refetched")
	}
	if len(browser.dataSetRequests) != 0 || len(model.datasets) != 4 || model.datasetPage.selectedKey() != "C" || model.datasetPage.windowStart != 1 {
		t.Fatalf("requests=%#v cache=%#v selected=%q start=%d", browser.dataSetRequests, model.datasets, model.datasetPage.selectedKey(), model.datasetPage.windowStart)
	}
	if len(model.records) != 0 {
		t.Fatalf("child record cache survived Back: %#v", model.records)
	}
}

func TestRecordPrefetchUsesLastCachedNumberAndExactBudget(t *testing.T) {
	browser := &fakeBrowser{
		listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.DATA", Organization: "PS"}}}, nil
		},
		readRecords: func(_ context.Context, request zosmf.ReadRecordsRequest) (zosmf.RecordPage, error) {
			records := make([]zosmf.Record, request.MaxItems)
			for i := range records {
				number := request.Start + int64(i) + 1
				records[i] = zosmf.Record{Number: number, Data: []byte("X")}
			}
			return zosmf.RecordPage{Records: records, MoreRows: request.Start == 0}, nil
		},
	}
	model := readyModel(t, Options{Prefix: "A*", Codepage: "latin1"}, browser, "A", "", 90, MinTerminalHeight(false))
	executeCommand(t, model, model.openSelection())
	if len(model.records) != 6 {
		t.Fatalf("initial record cache=%d", len(model.records))
	}
	command := model.moveSelection(3)
	if command == nil {
		t.Fatal("entering final visible page did not prefetch records")
	}
	executeCommand(t, model, command)
	last := browser.recordRequests[len(browser.recordRequests)-1]
	if last.Start != 6 || last.MaxItems != 6 || len(model.records) != 12 {
		t.Fatalf("request=%#v cache=%d", last, len(model.records))
	}
	requests := len(browser.recordRequests)
	if command := model.moveSelection(-3); command != nil || len(browser.recordRequests) != requests {
		t.Fatalf("cached backward movement command=%v requests=%d", command, len(browser.recordRequests))
	}
}

func TestDecodeInFlightSurvivesRecordCacheGrowth(t *testing.T) {
	built, err := buildOverlay(context.Background(), CopybookSource{Local: "book", Format: "free"}, "latin1", &fakeBrowser{}, func(context.Context, string) ([]byte, error) {
		return []byte("01 R. 05 FIELD PIC X.\n"), nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	model := recordViewModel(t)
	model.overlay = built
	model.records = []recordRow{
		{Record: zosmf.Record{Number: 1, Data: []byte("A")}},
		{Record: zosmf.Record{Number: 2, Data: []byte("B")}},
	}
	model.recordPage.reset(model.visible, model.budget)
	model.recordPage.apply([]string{"1", "2"}, true, model.recordPage.initialPlan(0))

	firstDecode := model.startDecode(model.ws())
	if firstDecode == nil {
		t.Fatal("initial decode did not start")
	}
	model.records = append(model.records, recordRow{Record: zosmf.Record{Number: 3, Data: []byte("C")}})
	model.recordPage.apply([]string{"3"}, false, pagePlan[int64]{Anchor: 2, Direction: pageForward})

	next := applyMessage(t, model, decodeResultMessage(t, firstDecode))
	if model.records[0].Decoded == nil || model.records[1].Decoded == nil || model.records[2].Decoded != nil {
		t.Fatalf("decode state after first batch = %#v", model.records)
	}
	firstPointer := model.records[0].Decoded
	if next == nil {
		t.Fatal("newly appended undecoded record did not schedule a second batch")
	}
	executeCommand(t, model, next)
	if model.records[0].Decoded != firstPointer || model.records[2].Decoded == nil {
		t.Fatalf("cached decode pointers were replaced or new row remained undecoded: %#v", model.records)
	}
}

func TestListPageActionsPreserveCursorScreenRow(t *testing.T) {
	model := recordViewModel(t)
	model.visible = 5
	model.budget = 40
	model.records = make([]recordRow, 20)
	keys := make([]string, len(model.records))
	for i := range model.records {
		number := int64(i + 1)
		keys[i] = strconv.FormatInt(number, 10)
		model.records[i] = recordRow{Record: zosmf.Record{Number: number, Data: []byte("RAW")}}
	}
	model.recordPage.reset(model.visible, model.budget)
	model.recordPage.apply(keys, false, model.recordPage.initialPlan(0))
	model.recordPage.move(6)
	assertPagerWindow(t, &model.recordPage, 6, 3, 3, scrollDown)

	model.jsonVertical = 4
	model.handleAction(actionPageDown)
	assertPagerWindow(t, &model.recordPage, 11, 8, 3, scrollIdle)
	if model.jsonVertical != 0 {
		t.Fatalf("record page movement retained JSON offset %d", model.jsonVertical)
	}
	model.handleAction(actionPageUp)
	assertPagerWindow(t, &model.recordPage, 6, 3, 3, scrollIdle)
}

func TestJSONPageKeysScrollAndRecordSelectionResetsOffset(t *testing.T) {
	model := recordViewModel(t)
	model.visible = 3
	model.budget = 16
	model.recordMode = ModeJSON
	model.records = make([]recordRow, 8)
	keys := make([]string, len(model.records))
	for i := range model.records {
		number := int64(i + 1)
		keys[i] = strconv.FormatInt(number, 10)
		model.records[i] = recordRow{Record: zosmf.Record{Number: number, Data: []byte("RAW")}}
	}
	model.recordPage.reset(model.visible, model.budget)
	model.recordPage.apply(keys, false, model.recordPage.initialPlan(0))
	model.recordPage.move(4)
	decoded := record.DecodedRecord{Value: record.Object{
		{Name: "A", Value: "one"}, {Name: "B", Value: "two"}, {Name: "C", Value: "three"}, {Name: "D", Value: "four"},
	}}
	model.records[4].Decoded = &decoded
	if before := model.recordJSONView(); !strings.Contains(before, `"A": "one"`) {
		t.Fatalf("initial JSON view = %q", before)
	}

	model.handleAction(actionPageDown)
	if model.jsonVertical == 0 {
		t.Fatal("PageDown did not scroll the JSON viewport")
	}
	if selected := model.recordPage.selectedKey(); selected != "5" || model.recordPage.windowStart != 3 {
		t.Fatalf("PageDown changed record viewport to %q/%d", selected, model.recordPage.windowStart)
	}
	if after := model.recordJSONView(); strings.Contains(after, `"A": "one"`) || !strings.Contains(after, `"D": "four"`) {
		t.Fatalf("scrolled JSON view = %q", after)
	}
	model.handleAction(actionDown)
	if model.recordPage.selectedKey() != "6" || model.recordPage.windowStart != 4 || model.jsonVertical != 0 {
		t.Fatalf("record change selected=%q start=%d offset=%d", model.recordPage.selectedKey(), model.recordPage.windowStart, model.jsonVertical)
	}
}

func TestEmptyCopybookDialogClearsActiveOverlay(t *testing.T) {
	model := recordViewModel(t)
	decoded := record.DecodedRecord{Value: record.Object{{Name: "FIELD", Value: "VALUE"}}}
	model.overlay = &overlay{
		Source: CopybookSource{Local: "layout.cpy"},
		Record: &layout.Record{Field: &layout.Field{Name: "ROW"}},
	}
	model.overlaySource = model.overlay.Source
	model.recordMode = ModeTable
	model.horizontal = 3
	model.jsonVertical = 4
	model.records = []recordRow{{
		Record:  zosmf.Record{Number: 1, Data: []byte("RAW")},
		Decoded: &decoded,
		Err:     errors.New("old diagnostic"),
	}}
	model.dialog = newCopybookDialog(CopybookSource{})

	if command := model.handleKey(keyPress(tea.KeyEnter, "")); command != nil {
		t.Fatal("empty copybook dialog returned a command")
	}
	if model.dialog != nil || model.overlay != nil || !model.overlaySource.empty() {
		t.Fatalf("dialog/overlay not cleared: dialog=%v overlay=%v source=%#v", model.dialog, model.overlay, model.overlaySource)
	}
	if model.recordMode != ModeRaw || model.horizontal != 0 || model.jsonVertical != 0 {
		t.Fatalf("presentation not reset: mode=%d horizontal=%d vertical=%d", model.recordMode, model.horizontal, model.jsonVertical)
	}
	if model.records[0].Decoded != nil || model.records[0].Err != nil {
		t.Fatalf("decoded state retained: %#v", model.records[0])
	}
	if model.status.Level != statusReady || model.status.Text != "copybook overlay cleared" {
		t.Fatalf("clear status = %#v", model.status)
	}
}

func TestEmptyCopybookDialogWithoutOverlayClosesCleanly(t *testing.T) {
	model := recordViewModel(t)
	model.status = status{Level: statusReady, Text: "unchanged"}
	model.dialog = newCopybookDialog(CopybookSource{})

	if command := model.handleKey(keyPress(tea.KeyEnter, "")); command != nil {
		t.Fatal("empty copybook dialog returned a command")
	}
	if model.dialog != nil {
		t.Fatal("empty copybook dialog remained open")
	}
	if model.status.Level != statusReady || model.status.Text != "unchanged" {
		t.Fatalf("empty dialog changed status: %#v", model.status)
	}
}

func TestOverlayCompletionPreservesPendingBrowseStatus(t *testing.T) {
	model := recordViewModel(t)
	model.overlayGeneration = 3
	model.overlayPending = true
	model.browsePending = &requestMeta{Generation: 7}
	model.status = status{Level: statusLoading, Text: "reading records from HQ.DATA()"}
	built := &overlay{Source: CopybookSource{Local: "layout.cpy"}, Record: &layout.Record{Field: &layout.Field{Name: "ROW"}}}

	if command := model.handleOverlayResult(model.ws(), overlayResultMsg{Generation: 3, Overlay: built}); command != nil {
		t.Fatal("overlay completion started decode while browse was pending")
	}
	if model.status.Level != statusLoading || model.status.Text != "reading records from HQ.DATA()" {
		t.Fatalf("pending browse status was overwritten: %#v", model.status)
	}
}

func TestPresentationTogglesReuseDecodedValuesWithoutRefetch(t *testing.T) {
	browser := &fakeBrowser{}
	model := readyModel(t, Options{Prefix: "A*", Codepage: "latin1"}, browser, "A", "", 90, 15)
	model.screen = ScreenRecords
	model.dataSet = zosmf.DataSet{Name: "A.DATA", Organization: "PS"}
	decoded := record.DecodedRecord{Value: record.Object{{Name: "FIELD", Value: "VALUE"}}}
	model.records = make([]recordRow, 12)
	keys := make([]string, len(model.records))
	for i := range model.records {
		number := int64(i + 1)
		keys[i] = strconv.FormatInt(number, 10)
		model.records[i] = recordRow{Record: zosmf.Record{Number: number, Data: []byte("VALUE")}, Decoded: &decoded}
	}
	model.recordPage.reset(model.visible, model.budget)
	model.recordPage.apply(keys, false, model.recordPage.initialPlan(0))
	model.recordPage.bottom()
	model.recordPage.move(-2)
	windowStart := model.recordPage.windowStart
	selected := model.recordPage.selectedKey()
	model.overlay = &overlay{Columns: []fieldColumn{{Path: "FIELD", Parts: []string{"FIELD"}}}}
	model.recordMode = ModeTable
	before := len(browser.recordRequests)
	pointer := model.records[0].Decoded

	model.handleAction(actionToggleView)
	if model.recordMode != ModeJSON || model.records[0].Decoded != pointer {
		t.Fatalf("JSON toggle mode=%d decoded pointer changed", model.recordMode)
	}
	model.handleAction(actionToggleOverlay)
	if model.recordMode != ModeRaw || model.records[0].Decoded != pointer {
		t.Fatalf("raw toggle mode=%d decoded pointer changed", model.recordMode)
	}
	model.handleAction(actionToggleOverlay)
	if model.recordMode != ModeJSON || model.records[0].Decoded != pointer {
		t.Fatalf("overlay restore mode=%d decoded pointer changed", model.recordMode)
	}
	model.handleAction(actionToggleOverlay)
	model.handleAction(actionToggleView)
	if model.recordMode != ModeRaw || model.decodedMode != ModeTable {
		t.Fatalf("view toggle in raw mode changed active mode: mode=%d decoded=%d", model.recordMode, model.decodedMode)
	}
	model.handleAction(actionToggleOverlay)
	if model.recordMode != ModeTable || model.records[0].Decoded != pointer {
		t.Fatalf("overlay restore after raw view toggle mode=%d decoded pointer changed", model.recordMode)
	}
	if model.recordPage.windowStart != windowStart || model.recordPage.selectedKey() != selected {
		t.Fatalf("presentation toggles moved record viewport from %q/%d to %q/%d", selected, windowStart, model.recordPage.selectedKey(), model.recordPage.windowStart)
	}
	if len(browser.recordRequests) != before {
		t.Fatalf("presentation toggle refetched records: before=%d after=%d", before, len(browser.recordRequests))
	}
}

func TestFocusedModelInputSuppressesQuitAndFunctionKeys(t *testing.T) {
	browser := &fakeBrowser{}
	model := readyModel(t, Options{}, browser, "", "", 90, 15)
	if !model.prefixInput.Focused() {
		t.Fatal("test requires focused prefix input")
	}
	if cmd := model.handleKey(keyPress('q', "q")); cmd != nil {
		message := cmd()
		if _, quit := message.(tea.QuitMsg); quit {
			t.Fatal("focused q quit the application")
		}
	}
	before := model.horizontal
	model.handleKey(keyPress(tea.KeyF11, ""))
	if model.horizontal != before {
		t.Fatal("focused F11 scrolled the application")
	}
	if !strings.Contains(model.prefixInput.Value(), "q") {
		t.Fatalf("focused q was not delivered to input: %q", model.prefixInput.Value())
	}
}

func TestMouseWheelRoutesToListJSONAndHelp(t *testing.T) {
	model := recordWindowModel(t, 5, 10)
	model.recordMode = ModeRaw
	applyMessage(t, model, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if got := model.recordPage.selectedIndex(); got != mouseWheelStep {
		t.Fatalf("wheel-down selected index = %d, want %d", got, mouseWheelStep)
	}
	applyMessage(t, model, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if got := model.recordPage.selectedIndex(); got != 0 {
		t.Fatalf("wheel-up selected index = %d, want 0", got)
	}

	fields := make(record.Object, 0, 8)
	for i := range 8 {
		fields = append(fields, record.Member{Name: fmt.Sprintf("FIELD-%d", i), Value: "VALUE"})
	}
	decoded := record.DecodedRecord{Value: fields}
	model.records[0].Decoded = &decoded
	model.recordMode = ModeJSON
	model.visible = 3
	applyMessage(t, model, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if model.jsonVertical != mouseWheelStep {
		t.Fatalf("JSON wheel offset = %d, want %d", model.jsonVertical, mouseWheelStep)
	}

	model.showHelp = true
	model.helpVertical = 0
	model.jsonVertical = 0
	applyMessage(t, model, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if model.helpVertical != mouseWheelStep || model.jsonVertical != 0 {
		t.Fatalf("help wheel offset=%d JSON offset=%d", model.helpVertical, model.jsonVertical)
	}
}

func TestSessionErrorAndEmptyResultStates(t *testing.T) {
	model, err := NewModel(Options{}, Dependencies{LoadSession: func(context.Context, string) (Session, error) {
		return Session{}, errors.New("profile missing")
	}})
	if err != nil {
		t.Fatal(err)
	}
	applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 15})
	executeCommand(t, model, model.Init())
	if model.status.Level != statusError || model.status.Text != "profile missing" {
		t.Fatalf("session error status = %#v", model.status)
	}
	if !strings.Contains(model.View().Content, "ERROR") {
		t.Fatalf("error view missing state label: %q", model.View().Content)
	}

	empty := readyModel(t, Options{Prefix: "A*"}, &fakeBrowser{}, "A", "", 90, 15)
	if empty.status.Level != statusEmpty || !strings.Contains(empty.View().Content, "EMPTY") {
		t.Fatalf("empty state status=%#v view=%q", empty.status, empty.View().Content)
	}
}
