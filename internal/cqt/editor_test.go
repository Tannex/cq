package cqt

import (
	"context"
	"net/http"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/zosmf"
)

func ctrlKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModCtrl})
}

// editableFakeBrowser layers the optional TextEditor surface over fakeBrowser.
type editableFakeBrowser struct {
	fakeBrowser
	texts    map[string]string
	versions map[string]int
	reads    []string
	writes   []zosmf.WriteTextRequest
	writeErr error
}

func newEditableBrowser() *editableFakeBrowser {
	return &editableFakeBrowser{
		fakeBrowser: fakeBrowser{
			listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
				return zosmf.DataSetPage{Items: []zosmf.DataSet{
					{Name: "A.CONTROL.CARDS", Organization: "PS", RecordFormat: "FB", RecordLength: "10"},
					{Name: "A.LOADLIB", Organization: "PO", RecordFormat: "U", RecordLength: "0"},
					{Name: "A.PARMLIB", Organization: "PO", RecordFormat: "FB", RecordLength: "10"},
				}}, nil
			},
			listMembers: func(context.Context, zosmf.ListMembersRequest) (zosmf.MemberPage, error) {
				return zosmf.MemberPage{Items: []zosmf.Member{{Name: "SYSIN"}}}, nil
			},
		},
		texts:    map[string]string{"A.CONTROL.CARDS": "LINE ONE\nLINE TWO\n", "A.PARMLIB(SYSIN)": "PARM=1\n"},
		versions: map[string]int{},
	}
}

func (f *editableFakeBrowser) etag(target string) string {
	return "v" + strings.Repeat("i", f.versions[target]+1)
}

func (f *editableFakeBrowser) ReadText(_ context.Context, dsn string) (zosmf.TextContent, error) {
	f.reads = append(f.reads, dsn)
	return zosmf.TextContent{Text: []byte(f.texts[dsn]), ETag: f.etag(dsn)}, nil
}

func (f *editableFakeBrowser) WriteText(_ context.Context, request zosmf.WriteTextRequest) (string, error) {
	f.writes = append(f.writes, request)
	if f.writeErr != nil {
		return "", f.writeErr
	}
	if request.ETag != "" && request.ETag != f.etag(request.Target) {
		return "", &zosmf.HTTPError{StatusCode: http.StatusPreconditionFailed, Resource: request.Target}
	}
	f.texts[request.Target] = string(request.Body)
	f.versions[request.Target]++
	return f.etag(request.Target), nil
}

func editorModel(t *testing.T, options Options) (*Model, *editableFakeBrowser) {
	t.Helper()
	browser := newEditableBrowser()
	options.Codepage = "latin1"
	if options.Prefix == "" {
		options.Prefix = "A*"
	}
	model := newTestModel(t, options, browser, "A", "latin1")
	applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 14})
	executeCommand(t, model, model.Init())
	return model, browser
}

func TestEditOpensSequentialDataSetAndKeepsEnterBehavior(t *testing.T) {
	model, browser := editorModel(t, Options{})
	executeCommand(t, model, model.handleKey(keyPress('e', "e")))
	if model.editor == nil {
		t.Fatalf("editor did not open: %v", model.status)
	}
	if model.editor.target.label() != "A.CONTROL.CARDS" {
		t.Fatalf("target = %q", model.editor.target.label())
	}
	if got := model.editor.area.Value(); got != "LINE ONE\nLINE TWO" {
		t.Fatalf("buffer = %q", got)
	}
	if model.editor.etag == "" || len(browser.reads) != 1 {
		t.Fatalf("etag = %q reads = %v", model.editor.etag, browser.reads)
	}
	if model.editor.dirty() {
		t.Fatal("freshly opened editor is dirty")
	}
	view := model.View().Content
	if !strings.Contains(view, "EDIT") || !strings.Contains(view, "A.CONTROL.CARDS") {
		t.Fatalf("editor chrome missing: %q", view)
	}

	// enter still browses records instead of editing.
	model.editor = nil
	if command := model.openSelection(); command == nil {
		t.Fatal("enter no longer opens the browse screen")
	}
	if model.screen != ScreenRecords {
		t.Fatalf("screen after enter = %d", model.screen)
	}
}

func TestEditOnMemberListOpensMemberAndNonEditableWarns(t *testing.T) {
	model, _ := editorModel(t, Options{})

	// Load-module library (RECFM U) member browsing: select A.LOADLIB.
	model.datasetPage.move(1)
	executeCommand(t, model, model.handleKey(keyPress('e', "e")))
	if model.editor != nil {
		t.Fatal("RECFM U library opened an editor")
	}
	if model.status.Level != statusWarn || !strings.Contains(model.status.Text, "RECFM U") {
		t.Fatalf("status = %#v", model.status)
	}

	// Open A.PARMLIB members and edit SYSIN.
	model.datasetPage.move(1)
	executeCommand(t, model, model.openSelection())
	if model.screen != ScreenMembers {
		t.Fatalf("screen = %d", model.screen)
	}
	executeCommand(t, model, model.handleKey(keyPress('e', "e")))
	if model.editor == nil || model.editor.target.label() != "A.PARMLIB(SYSIN)" {
		t.Fatalf("member editor = %#v status = %#v", model.editor, model.status)
	}
}

func TestBufferEditsNeverWriteWithoutExplicitSave(t *testing.T) {
	model, browser := editorModel(t, Options{})
	executeCommand(t, model, model.handleKey(keyPress('e', "e")))

	executeCommand(t, model, model.handleKey(keyPress('q', "q")))
	executeCommand(t, model, model.handleKey(keyPress('x', "x")))
	if !model.editor.dirty() {
		t.Fatal("typing did not mark the buffer dirty")
	}
	if !strings.Contains(model.View().Content, "MODIFIED") {
		t.Fatal("dirty indicator not shown")
	}
	if len(browser.writes) != 0 {
		t.Fatalf("writes without save = %#v", browser.writes)
	}

	executeCommand(t, model, model.handleKey(ctrlKey('s')))
	if len(browser.writes) != 1 {
		t.Fatalf("writes after ctrl+s = %d", len(browser.writes))
	}
	write := browser.writes[0]
	if write.Target != "A.CONTROL.CARDS" || write.ETag != "vi" {
		t.Fatalf("write = %#v", write)
	}
	if !strings.HasPrefix(string(write.Body), "qxLINE ONE\n") || !strings.HasSuffix(string(write.Body), "\n") {
		t.Fatalf("body = %q", write.Body)
	}
	if model.editor.dirty() {
		t.Fatal("buffer still dirty after successful save")
	}
	if model.editor.etag != "vii" {
		t.Fatalf("etag after save = %q", model.editor.etag)
	}
	if model.status.Level != statusReady || !strings.Contains(model.status.Text, "saved A.CONTROL.CARDS") {
		t.Fatalf("status = %#v", model.status)
	}
}

func TestSaveRejectsLinesLongerThanRecordLength(t *testing.T) {
	model, browser := editorModel(t, Options{})
	executeCommand(t, model, model.handleKey(keyPress('e', "e")))
	model.editor.area.SetValue("OK\nTHIS LINE IS FAR TOO LONG FOR LRECL TEN\nOK")

	executeCommand(t, model, model.handleKey(ctrlKey('s')))
	if len(browser.writes) != 0 {
		t.Fatalf("oversized buffer was written: %#v", browser.writes)
	}
	if model.status.Level != statusError || !strings.Contains(model.status.Text, "line 2") || !strings.Contains(model.status.Text, "LRECL 10") {
		t.Fatalf("status = %#v", model.status)
	}
}

func TestSaveConflictKeepsBufferAndReportsReload(t *testing.T) {
	model, browser := editorModel(t, Options{})
	executeCommand(t, model, model.handleKey(keyPress('e', "e")))
	executeCommand(t, model, model.handleKey(keyPress('!', "!")))
	// The host content changes underneath the editor.
	browser.versions["A.CONTROL.CARDS"]++

	executeCommand(t, model, model.handleKey(ctrlKey('s')))
	if model.editor == nil || !model.editor.dirty() {
		t.Fatal("conflict lost the buffer")
	}
	if model.status.Level != statusError || !strings.Contains(model.status.Text, "changed on the host") || !strings.Contains(model.status.Text, "ctrl+r") {
		t.Fatalf("status = %#v", model.status)
	}

	// ctrl+r explicitly reloads from the host, replacing the buffer and etag.
	executeCommand(t, model, model.handleKey(ctrlKey('r')))
	if model.editor.dirty() {
		t.Fatal("reload kept stale buffer")
	}
	if model.editor.etag != browser.etag("A.CONTROL.CARDS") {
		t.Fatalf("etag after reload = %q", model.editor.etag)
	}

	executeCommand(t, model, model.handleKey(keyPress('y', "y")))
	executeCommand(t, model, model.handleKey(ctrlKey('s')))
	if len(browser.writes) != 2 || browser.texts["A.CONTROL.CARDS"] != "yLINE ONE\nLINE TWO\n" {
		t.Fatalf("save after reload: writes=%d text=%q", len(browser.writes), browser.texts["A.CONTROL.CARDS"])
	}
}

func TestDirtyExitRequiresConfirmationAndCleanExitIsImmediate(t *testing.T) {
	model, _ := editorModel(t, Options{})
	executeCommand(t, model, model.handleKey(keyPress('e', "e")))

	// Clean exit closes immediately.
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEscape, "")))
	if model.editor != nil {
		t.Fatal("clean esc did not close the editor")
	}

	executeCommand(t, model, model.handleKey(keyPress('e', "e")))
	executeCommand(t, model, model.handleKey(keyPress('z', "z")))
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEscape, "")))
	if model.editor == nil || !model.editor.confirmDiscard {
		t.Fatal("dirty esc did not ask for confirmation")
	}
	// esc during confirmation keeps editing.
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEscape, "")))
	if model.editor == nil || model.editor.confirmDiscard {
		t.Fatal("esc did not return to editing")
	}
	if model.editor.area.Value() != "zLINE ONE\nLINE TWO" {
		t.Fatalf("buffer changed during confirmation: %q", model.editor.area.Value())
	}
	// d in confirmation discards.
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEscape, "")))
	executeCommand(t, model, model.handleKey(keyPress('d', "d")))
	if model.editor != nil {
		t.Fatal("discard did not close the editor")
	}
	if !strings.Contains(model.status.Text, "discarded") {
		t.Fatalf("status = %#v", model.status)
	}
}

func TestEditorRoutesQToBufferAndCtrlCQuits(t *testing.T) {
	model, _ := editorModel(t, Options{})
	executeCommand(t, model, model.handleKey(keyPress('e', "e")))

	if cmd := model.handleKey(keyPress('q', "q")); cmd != nil {
		t.Fatal("q produced a command inside the editor")
	}
	if !strings.HasPrefix(model.editor.area.Value(), "q") {
		t.Fatalf("q not typed into buffer: %q", model.editor.area.Value())
	}
	command := model.handleKey(ctrlKey('c'))
	if command == nil {
		t.Fatal("ctrl+c did not quit")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatalf("ctrl+c command = %T", command())
	}
}

func TestReadOnlyOptionDisablesEdit(t *testing.T) {
	model, browser := editorModel(t, Options{ReadOnly: true})
	executeCommand(t, model, model.handleKey(keyPress('e', "e")))
	if model.editor != nil || len(browser.reads) != 0 {
		t.Fatal("read-only mode still opened the editor")
	}
	if model.status.Level != statusWarn || !strings.Contains(model.status.Text, "--read-only") {
		t.Fatalf("status = %#v", model.status)
	}
}

func TestEditWithoutWriteSurfaceWarns(t *testing.T) {
	browser := &fakeBrowser{
		listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.DATA", Organization: "PS", RecordFormat: "FB", RecordLength: "80"}}}, nil
		},
	}
	model := newTestModel(t, Options{Prefix: "A*", Codepage: "latin1"}, browser, "A", "latin1")
	applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 14})
	executeCommand(t, model, model.Init())

	executeCommand(t, model, model.handleKey(keyPress('e', "e")))
	if model.editor != nil {
		t.Fatal("editor opened without a write surface")
	}
	if model.status.Level != statusWarn || !strings.Contains(model.status.Text, "does not support editing") {
		t.Fatalf("status = %#v", model.status)
	}
}
