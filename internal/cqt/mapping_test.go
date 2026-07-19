package cqt

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/dsnmap"
	"github.com/Tannex/cq/internal/zosmf"
)

type fakeMappingStore struct {
	mappings []dsnmap.Mapping
	touched  []string
	put      []dsnmap.Mapping
	removed  []string
	putErr   error
}

func (f *fakeMappingStore) Match(name string) (dsnmap.Mapping, bool) {
	for _, mapping := range f.mappings {
		if dsnmap.MatchPattern(name, mapping.Pattern) {
			return mapping, true
		}
	}
	return dsnmap.Mapping{}, false
}

func (f *fakeMappingStore) Matches(name string) []dsnmap.Mapping {
	var matches []dsnmap.Mapping
	for _, mapping := range f.mappings {
		if dsnmap.MatchPattern(name, mapping.Pattern) {
			matches = append(matches, mapping)
		}
	}
	return matches
}

func (f *fakeMappingStore) Put(mapping dsnmap.Mapping) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.put = append(f.put, mapping)
	for i, existing := range f.mappings {
		if existing.Pattern == mapping.Pattern {
			f.mappings[i] = mapping
			return nil
		}
	}
	f.mappings = append(f.mappings, mapping)
	return nil
}

func (f *fakeMappingStore) Remove(pattern string) (bool, error) {
	f.removed = append(f.removed, pattern)
	for i, mapping := range f.mappings {
		if mapping.Pattern == pattern {
			f.mappings = append(f.mappings[:i], f.mappings[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeMappingStore) Touch(pattern string) error {
	f.touched = append(f.touched, pattern)
	return nil
}

// flushMappings delivers the pending debounce timer so queued background
// writes reach the store, mirroring what tea.Tick does after the delay.
func flushMappings(t *testing.T, model *Model) {
	t.Helper()
	applyMessage(t, model, mappingFlushMsg{Seq: model.mappingSeq})
}

func mappedModel(t *testing.T, options Options, store MappingStore) *Model {
	t.Helper()
	browser := &fakeBrowser{
		listDataSets: func(context.Context, zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
			return zosmf.DataSetPage{Items: []zosmf.DataSet{{Name: "A.CUSTOMER.DATA", Organization: "PS"}}}, nil
		},
		readRecords: func(context.Context, zosmf.ReadRecordsRequest) (zosmf.RecordPage, error) {
			return zosmf.RecordPage{Records: []zosmf.Record{{Number: 1, Data: []byte("ABC")}}}, nil
		},
	}
	model, err := NewModel(options, Dependencies{
		LoadSession: func(context.Context, string) (Session, error) {
			return Session{Browser: browser, User: "A", Encoding: "latin1"}, nil
		},
		LoadFile: func(_ context.Context, path string) ([]byte, error) {
			return []byte("01 REC.\n 05 NAME PIC X(3).\n"), nil
		},
		Mappings: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	applyMessage(t, model, tea.WindowSizeMsg{Width: 90, Height: 12})
	executeCommand(t, model, model.Init())
	return model
}

func TestRecordsScreenAutoAppliesMatchedMapping(t *testing.T) {
	store := &fakeMappingStore{mappings: []dsnmap.Mapping{
		{Pattern: "A.CUSTOMER.*", Local: "cust.cpy", Format: "free"},
	}}
	model := mappedModel(t, Options{Prefix: "A*", Codepage: "latin1"}, store)

	executeCommand(t, model, model.openSelection())
	if model.screen != ScreenRecords {
		t.Fatalf("screen = %d, want records", model.screen)
	}
	if model.overlay == nil || model.overlay.Record.Name != "REC" {
		t.Fatalf("mapping overlay not applied: %#v error=%q", model.overlay, model.overlayError)
	}
	if model.overlayMappedPattern != "A.CUSTOMER.*" {
		t.Fatalf("mapped pattern = %q", model.overlayMappedPattern)
	}
	if len(store.touched) != 1 || store.touched[0] != "A.CUSTOMER.*" {
		t.Fatalf("touched = %#v", store.touched)
	}
	if model.records[0].Decoded == nil {
		t.Fatal("records not decoded through auto-applied overlay")
	}
}

func TestAutoApplyFailureDegradesToRawWithStatusMessage(t *testing.T) {
	store := &fakeMappingStore{mappings: []dsnmap.Mapping{
		{Pattern: "A.CUSTOMER.*", Local: "broken.cpy", Format: "auto"},
	}}
	model := mappedModel(t, Options{Prefix: "A*", Codepage: "latin1"}, store)
	model.deps.LoadFile = func(context.Context, string) ([]byte, error) {
		return []byte("this is not a copybook"), nil
	}

	executeCommand(t, model, model.openSelection())
	if model.screen != ScreenRecords || len(model.records) != 1 {
		t.Fatalf("browsing blocked by failed auto-apply: screen=%d records=%d", model.screen, len(model.records))
	}
	if model.overlay != nil || model.recordMode != ModeRaw {
		t.Fatalf("failed mapping still produced an overlay: %#v mode=%d", model.overlay, model.recordMode)
	}
	if model.overlayError == "" {
		t.Fatal("failed auto-apply produced no overlay error message")
	}
}

func TestExplicitCopybookFlagsOverridePersistedMappings(t *testing.T) {
	store := &fakeMappingStore{mappings: []dsnmap.Mapping{
		{Pattern: "*", Local: "mapped.cpy", Format: "free"},
	}}
	model := mappedModel(t, Options{Prefix: "A*", Codepage: "latin1", Copybook: "flag.cpy", Format: "free"}, store)

	executeCommand(t, model, model.openSelection())
	if model.overlaySource.Local != "flag.cpy" {
		t.Fatalf("overlay source = %#v, want the --copybook flag source", model.overlaySource)
	}
	if model.overlayMappedPattern != "" || len(store.touched) != 0 {
		t.Fatalf("mapping applied despite explicit flags: pattern=%q touched=%#v", model.overlayMappedPattern, store.touched)
	}
}

func TestAutoApplyPrefersMemberMappingOverDataSetMapping(t *testing.T) {
	store := &fakeMappingStore{mappings: []dsnmap.Mapping{
		{Pattern: "A.PDS(REPORT)", Local: "report.cpy", Format: "free"},
		{Pattern: "A.PDS", Local: "generic.cpy", Format: "free"},
	}}
	model := recordViewModel(t)
	model.deps.Timeout = defaultRequestTimeout
	model.deps.Mappings = store
	model.deps.LoadFile = func(context.Context, string) ([]byte, error) {
		return []byte("01 REC. 05 NAME PIC X(3).\n"), nil
	}
	model.dataSet = zosmf.DataSet{Name: "A.PDS", Organization: "PO"}
	model.member = &zosmf.Member{Name: "REPORT"}
	model.overlay = nil
	model.overlaySource = CopybookSource{}

	if command := model.autoApplyMapping(model.ws()); command == nil {
		t.Fatal("member mapping produced no overlay command")
	}
	if model.overlayMappedPattern != "A.PDS(REPORT)" {
		t.Fatalf("member mapping not preferred: %q", model.overlayMappedPattern)
	}

	// A member without its own mapping falls back to the data set mapping.
	model.overlayMappedPattern = ""
	model.cancelOverlay()
	model.member = &zosmf.Member{Name: "OTHER"}
	if command := model.autoApplyMapping(model.ws()); command == nil {
		t.Fatal("data set fallback produced no overlay command")
	}
	if model.overlayMappedPattern != "A.PDS" {
		t.Fatalf("data set fallback mapping not used: %q", model.overlayMappedPattern)
	}
}

func TestMappingViewListsMatchesAndMarksApplied(t *testing.T) {
	store := &fakeMappingStore{mappings: []dsnmap.Mapping{
		{Pattern: "HQ.*", Local: "hq.cpy", Format: "fixed", Record: "HQ-REC"},
		{Pattern: "HQ.DATA", Local: "exact.cpy", Format: "free"},
	}}
	model := recordViewModel(t)
	model.deps.Mappings = store
	model.overlayMappedPattern = "HQ.DATA"

	model.handleAction(actionCopybook)
	view := model.mappingView
	if view == nil || view.form != nil {
		t.Fatalf("mapping view did not open in list mode: %#v", view)
	}
	if len(view.entries) != 2 {
		t.Fatalf("entries = %#v", view.entries)
	}
	if !view.entries[view.selected].Applied || view.entries[view.selected].Mapping.Pattern != "HQ.DATA" {
		t.Fatalf("applied entry not selected: %#v selected=%d", view.entries, view.selected)
	}
}

func TestMappingViewOpensAddFormWhenNothingMatches(t *testing.T) {
	model := recordViewModel(t)
	model.deps.Mappings = &fakeMappingStore{}

	model.handleAction(actionCopybook)
	view := model.mappingView
	if view == nil || view.form == nil {
		t.Fatalf("mapping view did not open the add form: %#v", view)
	}
	if view.form.pattern.Value() != "HQ.DATA" {
		t.Fatalf("add form pattern = %q, want the exact data set name", view.form.pattern.Value())
	}
	if view.form.editing {
		t.Fatal("add form exposed the advanced record field")
	}
}

func TestMappingViewAppliesSelectedEntry(t *testing.T) {
	store := &fakeMappingStore{mappings: []dsnmap.Mapping{
		{Pattern: "HQ.*", Local: "hq.cpy", Format: "free"},
	}}
	model := recordViewModel(t)
	model.deps.Timeout = defaultRequestTimeout
	model.deps.Mappings = store
	model.deps.LoadFile = func(context.Context, string) ([]byte, error) {
		return []byte("01 REC. 05 NAME PIC X(3).\n"), nil
	}

	model.handleAction(actionCopybook)
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEnter, "")))

	if model.mappingView != nil {
		t.Fatal("mapping view stayed open after apply")
	}
	if model.overlay == nil || model.overlayMappedPattern != "HQ.*" {
		t.Fatalf("overlay not applied from list: overlay=%v pattern=%q error=%q", model.overlay, model.overlayMappedPattern, model.overlayError)
	}
	if len(store.touched) != 1 || store.touched[0] != "HQ.*" {
		t.Fatalf("touched = %#v", store.touched)
	}
	if len(store.put) != 0 {
		t.Fatalf("applying an existing mapping persisted a duplicate: %#v", store.put)
	}
}

func TestMappingFormApplyPersistsInBackgroundAfterDebounce(t *testing.T) {
	store := &fakeMappingStore{}
	model := recordViewModel(t)
	model.deps.Timeout = defaultRequestTimeout
	model.deps.Mappings = store
	model.deps.LoadFile = func(context.Context, string) ([]byte, error) {
		return []byte("01 REC. 05 NAME PIC X(3).\n"), nil
	}

	model.handleAction(actionCopybook)
	model.mappingView.form.copybook.SetValue("./cust.cpy")
	model.mappingView.form.pattern.SetValue("hq.%ata")
	command := model.handleKey(keyPress(tea.KeyEnter, ""))

	if model.mappingView != nil {
		t.Fatal("mapping view stayed open after apply")
	}
	if len(store.put) != 0 {
		t.Fatalf("mapping persisted synchronously on submit: %#v", store.put)
	}

	// Executing the command chain loads the overlay, queues the save, and
	// delivers the debounce timer.
	executeCommand(t, model, command)
	if model.overlay == nil || model.overlayMappedPattern != "HQ.%ATA" {
		t.Fatalf("overlay not applied: overlay=%v pattern=%q error=%q", model.overlay, model.overlayMappedPattern, model.overlayError)
	}
	flushMappings(t, model)
	if len(store.put) != 1 {
		t.Fatalf("saved mappings = %#v", store.put)
	}
	saved := store.put[0]
	if saved.Pattern != "HQ.%ATA" || saved.Local != "./cust.cpy" {
		t.Fatalf("saved mapping = %#v", saved)
	}
}

func TestMappingFormFailedApplyPersistsNothing(t *testing.T) {
	store := &fakeMappingStore{}
	model := recordViewModel(t)
	model.deps.Timeout = defaultRequestTimeout
	model.deps.Mappings = store
	model.deps.LoadFile = func(context.Context, string) ([]byte, error) {
		return []byte("this is not a copybook"), nil
	}

	model.handleAction(actionCopybook)
	model.mappingView.form.copybook.SetValue("./broken.cpy")
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEnter, "")))

	if model.overlay != nil || model.overlayError == "" {
		t.Fatalf("broken copybook produced an overlay: %#v error=%q", model.overlay, model.overlayError)
	}
	flushMappings(t, model)
	if len(store.put) != 0 {
		t.Fatalf("failed apply persisted a mapping: %#v", store.put)
	}
	if model.pendingMapping != nil {
		t.Fatal("pending mapping retained after failure")
	}
}

func TestMappingFormEditReplacesPatternWithoutOrphans(t *testing.T) {
	store := &fakeMappingStore{mappings: []dsnmap.Mapping{
		{Pattern: "HQ.DATA", Local: "hq.cpy", Format: "free"},
	}}
	model := recordViewModel(t)
	model.deps.Timeout = defaultRequestTimeout
	model.deps.Mappings = store
	model.deps.LoadFile = func(context.Context, string) ([]byte, error) {
		return []byte("01 REC. 05 NAME PIC X(3).\n"), nil
	}

	model.handleAction(actionCopybook)
	model.handleKey(keyPress('e', "e"))
	form := model.mappingView.form
	if form == nil || !form.editing || form.original != "HQ.DATA" {
		t.Fatalf("edit form not opened for entry: %#v", form)
	}
	if form.copybook.Value() != "hq.cpy" || form.record.Value() != "" {
		t.Fatalf("edit form not prefilled: copybook=%q record=%q", form.copybook.Value(), form.record.Value())
	}
	form.pattern.SetValue("HQ.*")
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEnter, "")))

	flushMappings(t, model)
	if len(store.removed) != 1 || store.removed[0] != "HQ.DATA" {
		t.Fatalf("old pattern not removed: %#v", store.removed)
	}
	if len(store.put) != 1 || store.put[0].Pattern != "HQ.*" || store.put[0].Local != "hq.cpy" {
		t.Fatalf("edited mapping = %#v", store.put)
	}
	if model.overlayMappedPattern != "HQ.*" {
		t.Fatalf("attribution = %q", model.overlayMappedPattern)
	}
}

func TestMappingViewRemoveQueuesBackgroundRemoval(t *testing.T) {
	store := &fakeMappingStore{mappings: []dsnmap.Mapping{
		{Pattern: "HQ.DATA", Local: "hq.cpy", Format: "free"},
	}}
	model := recordViewModel(t)
	model.deps.Mappings = store
	model.overlayMappedPattern = "HQ.DATA"

	model.handleAction(actionCopybook)
	command := model.handleKey(keyPress('x', "x"))
	if command == nil {
		t.Fatal("remove queued no debounce timer")
	}
	if len(model.mappingView.entries) != 0 {
		t.Fatalf("entry still listed after remove: %#v", model.mappingView.entries)
	}
	if !strings.Contains(model.mappingView.note, "removed mapping HQ.DATA") {
		t.Fatalf("note = %q", model.mappingView.note)
	}
	if model.overlayMappedPattern != "" {
		t.Fatalf("attribution retained after remove: %q", model.overlayMappedPattern)
	}
	if len(store.removed) != 0 {
		t.Fatalf("removal hit the store before the flush: %#v", store.removed)
	}

	flushMappings(t, model)
	if len(store.removed) != 1 || store.removed[0] != "HQ.DATA" {
		t.Fatalf("removed = %#v", store.removed)
	}
}

func TestMappingFormClearingCopybookRemovesMappingAndOverlay(t *testing.T) {
	store := &fakeMappingStore{mappings: []dsnmap.Mapping{
		{Pattern: "HQ.DATA", Local: "hq.cpy", Format: "free"},
	}}
	model := recordViewModel(t)
	model.deps.Mappings = store
	model.overlayMappedPattern = "HQ.DATA"

	model.handleAction(actionCopybook)
	model.handleKey(keyPress('e', "e"))
	model.mappingView.form.copybook.SetValue("  ")
	command := model.handleKey(keyPress(tea.KeyEnter, ""))
	if command == nil {
		t.Fatal("clear queued no removal")
	}
	if model.mappingView != nil {
		t.Fatal("mapping view stayed open after clear")
	}
	if model.overlay != nil || model.overlayMappedPattern != "" {
		t.Fatalf("overlay retained after clear: %#v pattern=%q", model.overlay, model.overlayMappedPattern)
	}
	if !strings.Contains(model.status.Text, "removed mapping HQ.DATA") {
		t.Fatalf("status = %#v", model.status)
	}

	flushMappings(t, model)
	if len(store.removed) != 1 || store.removed[0] != "HQ.DATA" {
		t.Fatalf("removed = %#v", store.removed)
	}
}

func TestMappingFormValidatesPattern(t *testing.T) {
	model := recordViewModel(t)
	model.deps.Mappings = &fakeMappingStore{}

	model.handleAction(actionCopybook)
	model.mappingView.form.copybook.SetValue("HQ.COPYLIB(CUST)")
	model.mappingView.form.pattern.SetValue("  ")
	if command := model.handleKey(keyPress(tea.KeyEnter, "")); command != nil {
		t.Fatal("patternless apply returned a command")
	}
	if !strings.Contains(model.mappingView.err, "pattern") {
		t.Fatalf("empty-pattern error = %q", model.mappingView.err)
	}

	model.mappingView.form.pattern.SetValue(`/HQ\.(/`)
	if command := model.handleKey(keyPress(tea.KeyEnter, "")); command != nil {
		t.Fatal("invalid-regex apply returned a command")
	}
	if !strings.Contains(model.mappingView.err, "invalid regex") {
		t.Fatalf("invalid-regex error = %q", model.mappingView.err)
	}
}

func TestMappingFormKeepsRegexPatternCase(t *testing.T) {
	store := &fakeMappingStore{}
	model := recordViewModel(t)
	model.deps.Timeout = defaultRequestTimeout
	model.deps.Mappings = store
	model.deps.LoadFile = func(context.Context, string) ([]byte, error) {
		return []byte("01 REC. 05 NAME PIC X(3).\n"), nil
	}

	model.handleAction(actionCopybook)
	model.mappingView.form.copybook.SetValue("./cust.cpy")
	model.mappingView.form.pattern.SetValue(` /hq\.d\w+/ `)
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEnter, "")))

	flushMappings(t, model)
	if len(store.put) != 1 || store.put[0].Pattern != `/hq\.d\w+/` {
		t.Fatalf("saved mappings = %#v", store.put)
	}
	if model.overlayMappedPattern != `/hq\.d\w+/` {
		t.Fatalf("mapped pattern attribution = %q", model.overlayMappedPattern)
	}
}

func TestQuitFlushesPendingMappingWrites(t *testing.T) {
	store := &fakeMappingStore{mappings: []dsnmap.Mapping{
		{Pattern: "HQ.DATA", Local: "hq.cpy", Format: "free"},
	}}
	model := recordViewModel(t)
	model.deps.Mappings = store

	model.handleAction(actionCopybook)
	model.handleKey(keyPress('x', "x"))
	if len(store.removed) != 0 {
		t.Fatalf("removal flushed early: %#v", store.removed)
	}
	model.mappingView = nil
	model.handleAction(actionQuit)
	if len(store.removed) != 1 || store.removed[0] != "HQ.DATA" {
		t.Fatalf("quit did not flush pending writes: %#v", store.removed)
	}
}

func TestSupersededDebounceTimerDoesNotFlushEarly(t *testing.T) {
	store := &fakeMappingStore{mappings: []dsnmap.Mapping{
		{Pattern: "HQ.DATA", Local: "hq.cpy", Format: "free"},
		{Pattern: "HQ.*", Local: "wild.cpy", Format: "free"},
	}}
	model := recordViewModel(t)
	model.deps.Mappings = store

	model.handleAction(actionCopybook)
	model.handleKey(keyPress('x', "x"))
	staleSeq := model.mappingSeq
	model.handleKey(keyPress('x', "x"))

	applyMessage(t, model, mappingFlushMsg{Seq: staleSeq})
	if len(store.removed) != 0 {
		t.Fatalf("stale timer flushed ops: %#v", store.removed)
	}
	flushMappings(t, model)
	if len(store.removed) != 2 {
		t.Fatalf("removed = %#v", store.removed)
	}
}
