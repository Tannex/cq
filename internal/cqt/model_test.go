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
		LoadSession: func(context.Context) (Session, error) {
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
	model.datasetPage.selected = 1
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
	model.datasetPage.selected = 1
	command = model.openSelection()
	if model.screen != ScreenMembers || command == nil {
		t.Fatalf("PDSE route screen=%d command=%v", model.screen, command)
	}
	executeCommand(t, model, command)
	if len(browser.memberRequests) != 1 || browser.memberRequests[0].DataSet != "IBMUSER.PDSE" {
		t.Fatalf("member requests = %#v", browser.memberRequests)
	}

	executeCommand(t, model, model.navigateBack())
	model.datasetPage.selected = 2
	if command := model.openSelection(); command != nil {
		t.Fatal("unsupported DSORG dispatched a command")
	}
	if model.status.Level != statusWarn || !strings.Contains(model.status.Text, "unsupported DSORG VS") {
		t.Fatalf("unsupported status = %#v", model.status)
	}
}

func TestModelCrossesNameWindowWithExactBudgetAndOnePageOverlap(t *testing.T) {
	browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		if request.Start == "" {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "D"}, {Name: "E"}, {Name: "F"}}, MoreRows: true}, nil
		}
		if request.Start != "D" {
			t.Fatalf("forward anchor = %q, want one-page-overlap cursor D", request.Start)
		}
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "D"}, {Name: "E"}, {Name: "F"}, {Name: "G"}, {Name: "H"}, {Name: "I"}}, MoreRows: true}, nil
	}}
	model := readyModel(t, Options{Prefix: "A*"}, browser, "A", "", 90, MinTerminalHeight)
	if model.visible != 3 || model.budget != 6 {
		t.Fatalf("visible/budget = %d/%d", model.visible, model.budget)
	}
	for range 5 {
		if command := model.moveSelection(1); command != nil {
			t.Fatal("movement inside the 2x window dispatched a request")
		}
	}
	command := model.moveSelection(1)
	if command == nil {
		t.Fatal("crossing the forward boundary did not dispatch")
	}
	executeCommand(t, model, command)
	last := browser.dataSetRequests[len(browser.dataSetRequests)-1]
	if last.Start != "D" || last.MaxItems != 6 || len(model.datasets) != 6 || model.datasetPage.selectedKey() != "F" {
		t.Fatalf("forward request=%#v rows=%#v selected=%q", last, model.datasets, model.datasetPage.selectedKey())
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
	staleMessage := staleCommand()
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

func TestResizeGrowReissuesExactBudgetAndShrinkDoesNotFetch(t *testing.T) {
	browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		items := make([]zosmf.DataSet, min(request.MaxItems, 5))
		for i := range items {
			items[i].Name = fmt.Sprintf("A.%02d", i)
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
	if got := browser.dataSetRequests[len(browser.dataSetRequests)-1]; got.MaxItems != 20 || got.Start != model.datasetPage.anchor {
		t.Fatalf("grow request = %#v anchor=%q", got, model.datasetPage.anchor)
	}

	pendingGrow := applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 20})
	if pendingGrow == nil {
		t.Fatal("second grow did not produce a request")
	}
	if shrinkCommand := applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 9}); shrinkCommand != nil {
		t.Fatal("shrink dispatched a replacement request")
	}
	before := len(browser.dataSetRequests)
	stale := pendingGrow()
	applyMessage(t, model, stale)
	if len(browser.dataSetRequests) != before+1 {
		t.Fatal("test did not execute the canceled command")
	}
	if len(model.datasets) > model.budget {
		t.Fatalf("shrunken window has %d rows for budget %d", len(model.datasets), model.budget)
	}
	if model.status.Level == statusLoading {
		t.Fatalf("canceled resize request left permanent loading status: %#v", model.status)
	}
}

func TestResizeShrinkUpdatesStatusForRetainedRowsWithoutFetching(t *testing.T) {
	browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		items := make([]zosmf.DataSet, request.MaxItems)
		for i := range items {
			items[i].Name = fmt.Sprintf("A.%03d", i)
		}
		return zosmf.DataSetPage{Items: items, MoreRows: true}, nil
	}}
	model := readyModel(t, Options{Prefix: "A*"}, browser, "A", "", 90, 15)
	before := len(browser.dataSetRequests)
	if command := applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 10}); command != nil {
		t.Fatal("shrink dispatched a request")
	}
	if len(browser.dataSetRequests) != before || len(model.datasets) != 10 {
		t.Fatalf("requests=%d before=%d retained=%d", len(browser.dataSetRequests), before, len(model.datasets))
	}
	if model.status.Level != statusReady || !strings.Contains(model.status.Text, "10 rows retained") {
		t.Fatalf("shrink status = %#v", model.status)
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

func TestEnsureActivePageReloadsTrimmedNonemptyPage(t *testing.T) {
	browser := &fakeBrowser{listDataSets: func(_ context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
		return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "D"}}}, nil
	}}
	model := newTestModel(t, Options{Prefix: "A*"}, browser, "A", "")
	model.sessionReady = true
	model.browser = browser
	model.prefix = "A*"
	model.screen = ScreenRecords
	model.visible = 2
	model.budget = 4
	model.datasets = []zosmf.DataSet{{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "D"}}
	model.datasetPage = pager[string]{keys: []string{"A", "B", "C", "D"}, selected: 2, budget: 4, anchor: "A", preserve: "C", needsReload: true}

	command := model.navigateBack()
	if command == nil {
		t.Fatal("returning to a trimmed nonempty page did not reload it")
	}
	executeCommand(t, model, command)
	request := browser.dataSetRequests[len(browser.dataSetRequests)-1]
	if request.MaxItems != 4 || request.Start != "A" {
		t.Fatalf("reload request = %#v", request)
	}
	if selected := model.datasetPage.selectedKey(); selected != "C" {
		t.Fatalf("selection after reload = %q, want C", selected)
	}
}

func TestJSONPageKeysScrollAndRecordSelectionResetsOffset(t *testing.T) {
	model := recordViewModel(t)
	model.visible = 3
	model.recordMode = ModeJSON
	decoded := record.DecodedRecord{Value: record.Object{
		{Name: "A", Value: "one"}, {Name: "B", Value: "two"}, {Name: "C", Value: "three"}, {Name: "D", Value: "four"},
	}}
	model.records[0].Decoded = &decoded
	if before := model.recordJSONView(); !strings.Contains(before, `"A": "one"`) {
		t.Fatalf("initial JSON view = %q", before)
	}

	model.handleAction(actionPageDown)
	if model.jsonVertical == 0 {
		t.Fatal("PageDown did not scroll the JSON viewport")
	}
	if selected := model.recordPage.selectedKey(); selected != "1" {
		t.Fatalf("PageDown changed selected record to %q", selected)
	}
	if after := model.recordJSONView(); strings.Contains(after, `"A": "one"`) || !strings.Contains(after, `"D": "four"`) {
		t.Fatalf("scrolled JSON view = %q", after)
	}
	model.handleAction(actionDown)
	if model.recordPage.selectedKey() != "2" || model.jsonVertical != 0 {
		t.Fatalf("record change selected=%q offset=%d", model.recordPage.selectedKey(), model.jsonVertical)
	}
}

func TestOverlayCompletionPreservesPendingBrowseStatus(t *testing.T) {
	model := recordViewModel(t)
	model.overlayGeneration = 3
	model.overlayPending = true
	model.browsePending = &requestMeta{Generation: 7}
	model.status = status{Level: statusLoading, Text: "reading records from HQ.DATA()"}
	built := &overlay{Source: CopybookSource{Local: "layout.cpy"}, Record: &layout.Record{Field: &layout.Field{Name: "ROW"}}}

	if command := model.handleOverlayResult(overlayResultMsg{Generation: 3, Overlay: built}); command != nil {
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
	model.records = []recordRow{{Record: zosmf.Record{Number: 1, Data: []byte("VALUE")}, Decoded: &decoded}}
	model.recordPage.reset(0, model.visible, model.budget)
	model.recordPage.apply([]string{"1"}, false, model.recordPage.initialPlan(0))
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

func TestSessionErrorAndEmptyResultStates(t *testing.T) {
	model, err := NewModel(Options{}, Dependencies{LoadSession: func(context.Context) (Session, error) {
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
