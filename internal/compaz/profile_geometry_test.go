package compaz

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Tannex/cq/internal/zosmf"
	"github.com/charmbracelet/x/ansi"
)

func TestWorkspacePagerGeometryAlignedOnActivation(t *testing.T) {
	datasets := make([]zosmf.DataSet, 30)
	for i := range datasets {
		datasets[i] = zosmf.DataSet{Name: fmt.Sprintf("DEMO.DS%02d", i), Organization: "PO"}
	}
	browser := &fakeBrowser{
		listDataSets: func(_ context.Context, r zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: datasets, ReturnedRows: len(datasets)}, nil
		},
		listMembers: func(_ context.Context, r zosmf.ListMembersRequest) (zosmf.MemberPage, error) {
			items := make([]zosmf.Member, 10)
			for i := range items {
				items[i] = zosmf.Member{Name: fmt.Sprintf("MEM%02d", i), Version: 1, CurrentRecords: 10}
			}
			return zosmf.MemberPage{Items: items, ReturnedRows: len(items)}, nil
		},
	}
	model, err := NewModel(Options{Prefix: "DEMO.*"}, Dependencies{
		LoadSession: func(context.Context, string) (Session, error) {
			return Session{Browser: browser, User: "U", Encoding: "latin1"}, nil
		},
		ListProfiles: func(context.Context) ([]string, error) { return []string{"sandbox", "dev", "prod"}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	applyMessage(t, model, tea.WindowSizeMsg{Width: 120, Height: 40})
	executeCommand(t, model, model.Init())
	if len(model.datasets) != 30 {
		t.Fatalf("datasets = %d", len(model.datasets))
	}
	executeCommand(t, model, model.moveSelection(1))
	executeCommand(t, model, model.moveSelection(1))
	executeCommand(t, model, model.moveSelection(1))
	executeCommand(t, model, model.openSelection())
	if model.screen != ScreenMembers || len(model.members) != 10 {
		t.Fatalf("screen=%d members=%d", model.screen, len(model.members))
	}
	start, end := model.memberPage.windowRange()
	t.Logf("memberPage window=(%d,%d) visible=%d keys=%d", start, end, model.memberPage.visible, len(model.memberPage.keys))
	content := ansi.Strip(model.View().Content)
	if !strings.Contains(content, "MEM00") {
		t.Fatalf("member table did not render cached member rows:\n%s", content)
	}
}
