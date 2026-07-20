package compaz

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/zosmf"
)

type fakeEventRecorder struct {
	opens   []string
	queries []string
	oks     []bool
}

func (f *fakeEventRecorder) RecordOpen(name string) { f.opens = append(f.opens, name) }
func (f *fakeEventRecorder) RecordQuery(expr string, ok bool) {
	f.queries = append(f.queries, expr)
	f.oks = append(f.oks, ok)
}

func TestUsageEventsRecordOpensAndQueries(t *testing.T) {
	recorder := &fakeEventRecorder{}
	browser := &fakeBrowser{
		listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.CUSTOMER.DATA", Organization: "PS"}}}, nil
		},
		readRecords: func(context.Context, zosmf.ReadRecordsRequest) (zosmf.RecordPage, error) {
			return zosmf.RecordPage{Records: []zosmf.Record{{Number: 1, Data: []byte("ABC")}}}, nil
		},
	}
	model, err := NewModel(Options{Prefix: "A*", Codepage: "latin1", Copybook: "cust.cpy"}, Dependencies{
		LoadSession: func(context.Context, string) (Session, error) {
			return Session{Browser: browser, User: "A", Encoding: "latin1"}, nil
		},
		LoadFile: func(context.Context, string) ([]byte, error) {
			return []byte("01 REC.\n 05 NAME PIC X(3).\n"), nil
		},
		Events: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 13})
	executeCommand(t, model, model.Init())

	executeCommand(t, model, model.openSelection())
	if len(recorder.opens) != 1 || recorder.opens[0] != "A.CUSTOMER.DATA" {
		t.Fatalf("opens = %#v", recorder.opens)
	}

	openQuery(t, model)
	runQueryExpr(t, model, ".NAME")
	runQueryExpr(t, model, "bad(")
	if len(recorder.queries) != 2 || recorder.queries[0] != ".NAME" || recorder.queries[1] != "bad(" {
		t.Fatalf("queries = %#v", recorder.queries)
	}
	if !recorder.oks[0] || recorder.oks[1] {
		t.Fatalf("outcomes = %#v", recorder.oks)
	}
}

func TestUsageEventsRecordMemberOpens(t *testing.T) {
	recorder := &fakeEventRecorder{}
	browser := &fakeBrowser{
		listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.PDS", Organization: "PO"}}}, nil
		},
		listMembers: func(context.Context, zosmf.ListMembersRequest) (zosmf.MemberPage, error) {
			return zosmf.MemberPage{Items: []zosmf.Member{{Name: "MEM1"}}}, nil
		},
	}
	model, err := NewModel(Options{Prefix: "A*"}, Dependencies{
		LoadSession: func(context.Context, string) (Session, error) {
			return Session{Browser: browser, User: "A", Encoding: "latin1"}, nil
		},
		Events: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 13})
	executeCommand(t, model, model.Init())

	executeCommand(t, model, model.openSelection()) // PDS
	executeCommand(t, model, model.openSelection()) // member
	if len(recorder.opens) != 2 || recorder.opens[0] != "A.PDS" || recorder.opens[1] != "A.PDS(MEM1)" {
		t.Fatalf("opens = %#v", recorder.opens)
	}
}

func TestMigratedOrUnsupportedOpensAreNotRecorded(t *testing.T) {
	recorder := &fakeEventRecorder{}
	browser := &fakeBrowser{
		listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{
				{Name: "A.MIGRATED", Organization: "PS", Volume: "MIGRAT"},
				{Name: "A.VSAM", Organization: "VS"},
			}}, nil
		},
	}
	model, err := NewModel(Options{Prefix: "A*"}, Dependencies{
		LoadSession: func(context.Context, string) (Session, error) {
			return Session{Browser: browser, User: "A", Encoding: "latin1"}, nil
		},
		Events: recorder,
	})
	if err != nil {
		t.Fatal(err)
	}
	applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 13})
	executeCommand(t, model, model.Init())

	executeCommand(t, model, model.openSelection()) // migrated: warns, no open
	executeCommand(t, model, model.moveSelection(1))
	executeCommand(t, model, model.openSelection()) // unsupported DSORG
	if len(recorder.opens) != 0 {
		t.Fatalf("opens = %#v, want none", recorder.opens)
	}
}
