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

func (f *fakeMappingStore) Put(mapping dsnmap.Mapping) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.put = append(f.put, mapping)
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

func ctrlKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModCtrl})
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

func TestCopybookDialogPrefillsFromMappingAndDefaultsPatternToExactName(t *testing.T) {
	store := &fakeMappingStore{mappings: []dsnmap.Mapping{
		{Pattern: "HQ.*", Local: "hq.cpy", Format: "fixed", Record: "HQ-REC"},
	}}
	model := recordViewModel(t)
	model.deps.Mappings = store

	model.handleAction(actionCopybook)
	if model.dialog == nil {
		t.Fatal("dialog did not open")
	}
	if model.dialog.local.Value() != "hq.cpy" || model.dialog.format.Value() != "fixed" || model.dialog.record.Value() != "HQ-REC" {
		t.Fatalf("dialog not prefilled from mapping: local=%q format=%q record=%q",
			model.dialog.local.Value(), model.dialog.format.Value(), model.dialog.record.Value())
	}
	if model.dialog.pattern.Value() != "HQ.*" || !strings.Contains(model.dialog.note, "HQ.*") {
		t.Fatalf("matched mapping not indicated: pattern=%q note=%q", model.dialog.pattern.Value(), model.dialog.note)
	}

	// Without any matching mapping the pattern defaults to the exact name.
	model.dialog = nil
	model.deps.Mappings = &fakeMappingStore{}
	model.handleAction(actionCopybook)
	if model.dialog.pattern.Value() != "HQ.DATA" || model.dialog.note != "" {
		t.Fatalf("default pattern = %q note = %q", model.dialog.pattern.Value(), model.dialog.note)
	}
}

func TestDialogSavesMappingWithEditedPatternAndAppliesOverlay(t *testing.T) {
	store := &fakeMappingStore{}
	model := recordViewModel(t)
	model.deps.Timeout = defaultRequestTimeout
	model.deps.Mappings = store
	model.deps.LoadFile = func(context.Context, string) ([]byte, error) {
		return []byte("01 REC. 05 NAME PIC X(3).\n"), nil
	}

	model.handleAction(actionCopybook)
	model.dialog.local.SetValue("cust.cpy")
	model.dialog.format.SetValue("free")
	model.dialog.pattern.SetValue("hq.%ata")
	executeCommand(t, model, model.handleKey(ctrlKey('s')))

	if len(store.put) != 1 {
		t.Fatalf("saved mappings = %#v", store.put)
	}
	saved := store.put[0]
	if saved.Pattern != "HQ.%ATA" || saved.Local != "cust.cpy" || saved.Format != "free" {
		t.Fatalf("saved mapping = %#v", saved)
	}
	if model.dialog != nil {
		t.Fatal("dialog stayed open after save")
	}
	if model.overlay == nil || model.overlayMappedPattern != "HQ.%ATA" {
		t.Fatalf("overlay not applied on save: overlay=%v pattern=%q error=%q", model.overlay, model.overlayMappedPattern, model.overlayError)
	}
}

func TestDialogSaveValidatesSourceAndPattern(t *testing.T) {
	model := recordViewModel(t)
	model.deps.Mappings = &fakeMappingStore{}

	model.handleAction(actionCopybook)
	if command := model.handleKey(ctrlKey('s')); command != nil {
		t.Fatal("empty save returned a command")
	}
	if !strings.Contains(model.dialog.err, "copybook source") {
		t.Fatalf("empty-source save error = %q", model.dialog.err)
	}

	model.dialog.local.SetValue("cust.cpy")
	model.dialog.pattern.SetValue("  ")
	if command := model.handleKey(ctrlKey('s')); command != nil {
		t.Fatal("patternless save returned a command")
	}
	if !strings.Contains(model.dialog.err, "pattern") {
		t.Fatalf("empty-pattern save error = %q", model.dialog.err)
	}

	model.dialog.pattern.SetValue(`/HQ\.(/`)
	if command := model.handleKey(ctrlKey('s')); command != nil {
		t.Fatal("invalid-regex save returned a command")
	}
	if !strings.Contains(model.dialog.err, "invalid regex") {
		t.Fatalf("invalid-regex save error = %q", model.dialog.err)
	}
}

func TestDialogSavesRegexPatternWithoutUppercasing(t *testing.T) {
	store := &fakeMappingStore{}
	model := recordViewModel(t)
	model.deps.Timeout = defaultRequestTimeout
	model.deps.Mappings = store
	model.deps.LoadFile = func(context.Context, string) ([]byte, error) {
		return []byte("01 REC. 05 NAME PIC X(3).\n"), nil
	}

	model.handleAction(actionCopybook)
	model.dialog.local.SetValue("cust.cpy")
	model.dialog.format.SetValue("free")
	model.dialog.pattern.SetValue(` /hq\.d\w+/ `)
	executeCommand(t, model, model.handleKey(ctrlKey('s')))

	if len(store.put) != 1 || store.put[0].Pattern != `/hq\.d\w+/` {
		t.Fatalf("saved mappings = %#v", store.put)
	}
	if model.overlayMappedPattern != `/hq\.d\w+/` {
		t.Fatalf("mapped pattern attribution = %q", model.overlayMappedPattern)
	}
}

func TestDialogRemovesMappingAndStaysOpen(t *testing.T) {
	store := &fakeMappingStore{mappings: []dsnmap.Mapping{
		{Pattern: "HQ.DATA", Local: "hq.cpy", Format: "free"},
	}}
	model := recordViewModel(t)
	model.deps.Mappings = store
	model.overlayMappedPattern = "HQ.DATA"

	model.handleAction(actionCopybook)
	if command := model.handleKey(ctrlKey('r')); command != nil {
		t.Fatal("remove returned a command")
	}
	if len(store.removed) != 1 || store.removed[0] != "HQ.DATA" {
		t.Fatalf("removed = %#v", store.removed)
	}
	if model.dialog == nil || !strings.Contains(model.dialog.note, "removed mapping HQ.DATA") {
		t.Fatalf("dialog state after remove: %#v", model.dialog)
	}
	if model.overlayMappedPattern != "" {
		t.Fatalf("mapped pattern retained after remove: %q", model.overlayMappedPattern)
	}

	// Removing again reports that nothing is stored under the pattern.
	model.handleKey(ctrlKey('r'))
	if !strings.Contains(model.dialog.err, "no mapping stored") {
		t.Fatalf("second remove error = %q", model.dialog.err)
	}
}

func TestManualDialogApplyClearsMappedPatternAttribution(t *testing.T) {
	model := recordViewModel(t)
	model.deps.Timeout = defaultRequestTimeout
	model.deps.Mappings = &fakeMappingStore{}
	model.deps.LoadFile = func(context.Context, string) ([]byte, error) {
		return []byte("01 REC. 05 NAME PIC X(3).\n"), nil
	}
	model.overlayMappedPattern = "OLD.*"

	model.handleAction(actionCopybook)
	model.dialog.local.SetValue("manual.cpy")
	model.dialog.format.SetValue("free")
	executeCommand(t, model, model.handleKey(keyPress(tea.KeyEnter, "")))

	if model.overlayMappedPattern != "" {
		t.Fatalf("manual apply kept mapping attribution: %q", model.overlayMappedPattern)
	}
	if model.overlay == nil {
		t.Fatalf("manual overlay not applied: %q", model.overlayError)
	}
}
