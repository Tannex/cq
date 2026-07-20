// Package compaz implements Compa/z, the z/OSMF operator console used by
// the separate compaz executable (formerly cqt).
package compaz

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/dsnmap"
	"github.com/Tannex/cq/internal/favorites"
	"github.com/Tannex/cq/internal/record"
	"github.com/Tannex/cq/internal/zosmf"
)

const (
	defaultRequestTimeout = 30 * time.Second
	maxScrollOffset       = math.MaxInt
	mouseWheelStep        = 3
)

// z/OSMF reports no recall progress, so a queued HRECALL is watched by
// re-listing the data set until it stops being migrated. Checks start eager
// for recalls served from disk, then back off for tape recalls; the watch
// gives up with a warning after maxRecallPolls attempts (roughly half an
// hour), surfacing the last catalog-check error if there was one.
const maxRecallPolls = 92

func recallPollDelay(attempt int) time.Duration {
	switch {
	case attempt <= 20: // first minute
		return 3 * time.Second
	case attempt <= 40: // next ~3 minutes
		return 10 * time.Second
	default: // up to ~30 minutes in total
		return 30 * time.Second
	}
}

// Options are the approved compaz command-line settings.
type Options struct {
	Prefix      string
	Copybook    string
	CopybookDSN string
	Format      string
	Record      string
	Codepage    string
	// ReadOnly disables edit mode entirely, restoring the strictly read-only
	// console guarantee for operators who want it.
	ReadOnly bool
}

// Session is the credential-safe application session returned by an injected
// loader. Browser contains the bounded read-only operations only.
type Session struct {
	Browser  zosmf.Browser
	User     string
	Encoding string
}

// MappingStore persists DSN → copybook mappings across sessions. A nil store
// disables mapping persistence.
type MappingStore interface {
	Match(name string) (dsnmap.Mapping, bool)
	Matches(name string) []dsnmap.Mapping
	Put(mapping dsnmap.Mapping) error
	Remove(pattern string) (bool, error)
	Touch(pattern string) error
}

// mappingFlushDelay debounces background mapping writes: bursts of add/edit/
// remove actions coalesce into one flush shortly after the user pauses.
const mappingFlushDelay = 750 * time.Millisecond

// mappingOp is one queued background store write: exactly one of put and
// removePattern is set.
type mappingOp struct {
	put           *dsnmap.Mapping
	removePattern string
}

type mappingFlushMsg struct {
	Seq uint64
}

// pendingMappingSave is a mapping waiting for its overlay load to succeed;
// only successful applies are persisted. removeOld names a pattern the save
// replaces (an edit that changed the pattern), removed in the same flush.
type pendingMappingSave struct {
	mapping   dsnmap.Mapping
	removeOld string
}

// FavoriteStore persists data set favorites across sessions. A nil store
// disables favorites.
type FavoriteStore interface {
	Favorites() []favorites.Favorite
	Matches(name string) bool
	Toggle(name string) (bool, error)
	Add(pattern string) error
	Rename(oldPattern, newPattern string) error
	SetNote(pattern, note string) error
	Remove(pattern string) (bool, error)
	Touch(pattern string) error
}

// EventRecorder appends local usage events — data set opens and executed jq
// queries — for the future suggestion engine and the query history. A nil
// recorder disables tracking.
type EventRecorder interface {
	RecordOpen(name string)
	RecordQuery(expr string, ok bool)
}

// Dependencies provide test seams without weakening the production command's
// read-only boundaries.
type Dependencies struct {
	LoadSession   func(ctx context.Context, profile string) (Session, error)
	ListProfiles  func(context.Context) ([]string, error)
	LoadFile      func(context.Context, string) ([]byte, error)
	Mappings      MappingStore
	Favorites     FavoriteStore
	Events        EventRecorder
	DSNSearchPath []string
	Timeout       time.Duration
}

type statusLevel string

const (
	statusLoading statusLevel = "LOADING"
	statusReady   statusLevel = "READY"
	statusWarn    statusLevel = "WARN"
	statusError   statusLevel = "ERROR"
	statusEmpty   statusLevel = "EMPTY"
)

type status struct {
	Level statusLevel
	Text  string
}

type requestMeta struct {
	Generation uint64
	Screen     Screen
	Identity   string
	Profile    string
	Budget     int
	NamePlan   pagePlan[string]
	RecordPlan pagePlan[int64]
}

type profileListMsg struct {
	Profiles []string
	Err      error
}

type sessionResultMsg struct {
	Profile    string
	Generation uint64
	Session    Session
	Err        error
}

type dataSetsResultMsg struct {
	Meta requestMeta
	Page zosmf.DataSetPage
	Err  error
}

type membersResultMsg struct {
	Meta requestMeta
	Page zosmf.MemberPage
	Err  error
}

type recordsResultMsg struct {
	Meta requestMeta
	Page zosmf.RecordPage
	Err  error
}

type recallResultMsg struct {
	Profile string
	DSN     string
	Err     error
}

type recallPollMsg struct {
	Profile string
	DSN     string
	Attempt int
}

type recallCheckMsg struct {
	Profile string
	DSN     string
	Attempt int
	Page    zosmf.DataSetPage
	Err     error
}

type overlayResultMsg struct {
	Profile    string
	Generation uint64
	Source     CopybookSource
	Overlay    *overlay
	Err        error
}

type decodedRow struct {
	Number  int64
	Decoded record.DecodedRecord
	Err     error
}

type decodeResultMsg struct {
	Profile    string
	Generation uint64
	Identity   string
	Overlay    *overlay
	Rows       []decodedRow
	Err        error
}

type recordRow struct {
	Record  zosmf.Record
	Decoded *record.DecodedRecord
	Err     error
}

// Model is the Bubble Tea state machine. It is read-only: actions can only
// navigate, filter, fetch, decode, or change presentation.
//
// The active workspace is embedded so that existing code paths can continue to
// read per-profile fields (datasets, records, pagers, etc.) directly. Inactive
// workspaces are stored in the workspaces slice and swapped in on profile
// switches.
type Model struct {
	workspace

	options Options
	deps    Dependencies
	keys    KeyMap
	help    help.Model
	spinner spinner.Model

	width   int
	height  int
	visible int
	budget  int

	prefixInput textinput.Model
	memberInput textinput.Model
	locateInput textinput.Model
	mappingView *mappingView
	favPopup    *favoritesPopup
	editor      *editorState
	query       *queryPopup

	editGeneration uint64
	editCancel     context.CancelFunc
	editPending    bool

	// mappingOps are background store writes awaiting the debounced flush;
	// mappingSeq invalidates timers superseded by a newer queued op.
	mappingOps []mappingOp
	mappingSeq uint64

	showHelp     bool
	helpVertical int
	jsonCache    jsonContentCache

	profiles   []string
	active     int
	workspaces []*workspace
}

// NewModel validates static options and constructs a model whose work begins in
// Init. No network, filesystem, copybook, or decoding operation runs here.
func NewModel(options Options, deps Dependencies) (*Model, error) {
	if strings.TrimSpace(options.Copybook) != "" && strings.TrimSpace(options.CopybookDSN) != "" {
		return nil, errors.New("provide at most one copybook source: --copybook or --copybook-dsn")
	}
	if strings.TrimSpace(options.Format) == "" {
		options.Format = "auto"
	}
	if strings.TrimSpace(options.Copybook) != "" || strings.TrimSpace(options.CopybookDSN) != "" {
		if _, _, err := (CopybookSource{Local: options.Copybook, DSN: options.CopybookDSN, Format: options.Format, Record: options.Record}).validate(); err != nil {
			return nil, err
		}
	} else if err := validateCopybookFormat(options.Format); err != nil {
		return nil, err
	}
	if deps.Timeout <= 0 {
		deps.Timeout = defaultRequestTimeout
	}
	if deps.LoadFile == nil {
		deps.LoadFile = func(ctx context.Context, path string) ([]byte, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return data, nil
		}
	}

	prefixInput := textinput.New()
	prefixInput.Prompt = "PREFIX  "
	prefixInput.Placeholder = "IBMUSER.*"
	prefixInput.CharLimit = 44
	prefixInput.SetWidth(48)
	prefixStyles := prefixInput.Styles()
	prefixStyles.Cursor.Blink = false
	prefixInput.SetStyles(prefixStyles)
	memberInput := textinput.New()
	memberInput.Prompt = "MEMBER  "
	memberInput.Placeholder = "prefix or pattern"
	memberInput.CharLimit = 8
	memberInput.SetWidth(24)
	memberStyles := memberInput.Styles()
	memberStyles.Cursor.Blink = false
	memberInput.SetStyles(memberStyles)
	locateInput := textinput.New()
	locateInput.Prompt = "RECORD  "
	locateInput.Placeholder = "record number"
	locateInput.CharLimit = 19
	locateInput.SetWidth(24)
	locateStyles := locateInput.Styles()
	locateStyles.Cursor.Blink = false
	locateInput.SetStyles(locateStyles)

	ws := newWorkspace("")
	m := &Model{
		workspace:   ws,
		options:     options,
		deps:        deps,
		keys:        DefaultKeyMap(),
		help:        help.New(),
		spinner:     newStatusSpinner(),
		prefixInput: prefixInput,
		memberInput: memberInput,
		locateInput: locateInput,
		profiles:    []string{""},
	}
	m.workspaces = []*workspace{&m.workspace}
	return m, nil
}

func validateCopybookFormat(format string) error {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "auto", "fixed", "free":
		return nil
	default:
		return fmt.Errorf("copybook format %q is invalid; use auto, fixed, or free", format)
	}
}

func newStatusSpinner() spinner.Model {
	return spinner.New(spinner.WithSpinner(spinner.MiniDot))
}

func (m *Model) loadingCommand(command tea.Cmd) tea.Cmd {
	if command == nil {
		return nil
	}
	return tea.Batch(command, m.spinner.Tick)
}

func (m *Model) ws() *workspace {
	return &m.workspace
}

func (m *Model) hasTabs() bool {
	return len(m.profiles) > 1
}

func (m *Model) activeProfile() string {
	if m.active < 0 || m.active >= len(m.profiles) {
		return ""
	}
	return m.profiles[m.active]
}

func (m *Model) saveActiveWorkspace() {
	if m.active >= 0 && m.active < len(m.workspaces) {
		*m.workspaces[m.active] = m.workspace
	}
}

func (m *Model) loadWorkspace(index int) {
	if index < 0 || index >= len(m.workspaces) {
		return
	}
	m.workspace = *m.workspaces[index]
	m.active = index
	// A workspace created or last active under a different terminal size
	// carries stale pager geometry; align it before anything renders or
	// resets a pager from its own visible/budget values.
	m.workspace.resizePagers(m.visible, m.budget)
}

func (m *Model) syncInputsFromWorkspace() {
	m.prefixInput.SetValue(m.workspace.prefix)
	m.memberInput.SetValue(m.workspace.memberPattern)
	m.locateInput.SetValue("")
}

// Init loads the Zowe session in a typed command. Window-size handling remains
// independent, so a session can resolve in a tiny terminal without dispatching
// a row request.
func (m *Model) Init() tea.Cmd {
	if m.deps.ListProfiles == nil {
		return m.loadActiveSession()
	}
	return m.loadingCommand(m.loadProfileList())
}

func (m *Model) loadProfileList() tea.Cmd {
	loader := m.deps.ListProfiles
	timeout := m.deps.Timeout
	return func() tea.Msg {
		if loader == nil {
			return profileListMsg{Profiles: []string{""}}
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		profiles, err := loader(ctx)
		return profileListMsg{Profiles: profiles, Err: err}
	}
}

func (m *Model) handleProfileList(msg profileListMsg) tea.Cmd {
	if msg.Err != nil {
		m.workspace.status = status{Level: statusError, Text: msg.Err.Error()}
		return nil
	}
	profiles := msg.Profiles
	if len(profiles) == 0 {
		profiles = []string{""}
	}
	m.profiles = profiles
	if len(profiles) == 1 {
		m.workspace.profile = profiles[0]
		m.workspaces = []*workspace{&m.workspace}
	} else {
		m.workspaces = make([]*workspace, len(profiles))
		for i, profile := range profiles {
			ws := newWorkspace(profile)
			m.workspaces[i] = &ws
		}
		m.loadWorkspace(0)
	}
	m.syncInputsFromWorkspace()
	return m.loadActiveSession()
}

func (m *Model) loadActiveSession() tea.Cmd {
	ws := m.ws()
	ws.cancelSession()
	ws.sessionGeneration++
	generation := ws.sessionGeneration
	profile := m.activeProfile()
	ctx, cancel := context.WithTimeout(context.Background(), m.deps.Timeout)
	ws.sessionCancel = cancel
	loader := m.deps.LoadSession
	ws.status = status{Level: statusLoading, Text: ws.sessionStatusText()}
	return m.loadingCommand(func() tea.Msg {
		if loader == nil {
			return sessionResultMsg{Profile: profile, Generation: generation, Err: errors.New("Zowe session loader is unavailable")}
		}
		session, err := loader(ctx, profile)
		return sessionResultMsg{Profile: profile, Generation: generation, Session: session, Err: err}
	})
}

func (m *Model) targetWorkspace(profile string) *workspace {
	if len(m.profiles) == 0 {
		if m.workspace.profile == profile {
			return &m.workspace
		}
		return nil
	}
	for i, p := range m.profiles {
		if p == profile {
			if i == m.active {
				return &m.workspace
			}
			return m.workspaces[i]
		}
	}
	return nil
}

// Update implements tea.Model.
func (m *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		return m, m.handleResize(msg.Width, msg.Height)
	case profileListMsg:
		return m, m.handleProfileList(msg)
	case sessionResultMsg:
		if ws := m.targetWorkspace(msg.Profile); ws != nil {
			return m, m.handleSessionResult(ws, msg)
		}
	case dataSetsResultMsg:
		if ws := m.targetWorkspace(msg.Meta.Profile); ws != nil {
			return m, m.handleDataSetsResult(ws, msg)
		}
	case membersResultMsg:
		if ws := m.targetWorkspace(msg.Meta.Profile); ws != nil {
			return m, m.handleMembersResult(ws, msg)
		}
	case recordsResultMsg:
		if ws := m.targetWorkspace(msg.Meta.Profile); ws != nil {
			return m, m.handleRecordsResult(ws, msg)
		}
	case overlayResultMsg:
		if ws := m.targetWorkspace(msg.Profile); ws != nil {
			return m, m.handleOverlayResult(ws, msg)
		}
	case recallResultMsg:
		if ws := m.targetWorkspace(msg.Profile); ws != nil {
			return m, m.handleRecallResult(ws, msg)
		}
	case recallPollMsg:
		if ws := m.targetWorkspace(msg.Profile); ws != nil {
			return m, m.handleRecallPoll(ws, msg)
		}
	case recallCheckMsg:
		if ws := m.targetWorkspace(msg.Profile); ws != nil {
			return m, m.handleRecallCheck(ws, msg)
		}
	case decodeResultMsg:
		ws := m.targetWorkspace(msg.Profile)
		if ws != nil {
			return m, m.handleDecodeResult(ws, msg)
		}
	case mappingFlushMsg:
		if msg.Seq == m.mappingSeq {
			m.flushMappingOps()
		}
		return m, nil
	case editorNavFlushMsg:
		m.handleEditorNavFlush()
		return m, nil
	case editFetchResultMsg:
		return m, m.handleEditFetchResult(msg)
	case editSaveResultMsg:
		return m, m.handleEditSaveResult(msg)
	case queryEvalMsg:
		return m, m.handleQueryEval(msg)
	case queryBulkMsg:
		return m, m.handleQueryBulk(msg)
	case spinner.TickMsg:
		var commands []tea.Cmd
		if m.query != nil && m.query.running {
			updated, command := m.query.spin.Update(msg)
			m.query.spin = updated
			commands = append(commands, command)
		}
		// Recalls keep the status spinner alive so list rows can animate their
		// RECALL indicator even after the status line has moved on.
		if level, _ := m.effectiveStatus(); level == statusLoading || m.ws().hasRecalls() {
			updated, command := m.spinner.Update(msg)
			m.spinner = updated
			commands = append(commands, command)
		}
		return m, tea.Batch(commands...)
	case tea.KeyPressMsg:
		return m, m.handleKey(msg)
	case tea.MouseWheelMsg:
		return m, m.handleMouseWheel(msg)
	default:
		return m, nil
	}
	return m, nil
}

func (m *Model) handleSessionResult(ws *workspace, msg sessionResultMsg) tea.Cmd {
	if msg.Generation != ws.sessionGeneration {
		return nil
	}
	ws.cancelSession()
	if msg.Err != nil {
		ws.status = status{Level: statusError, Text: msg.Err.Error()}
		return nil
	}
	if msg.Session.Browser == nil {
		ws.status = status{Level: statusError, Text: "Zowe session did not provide a browser client"}
		return nil
	}

	codepage := strings.TrimSpace(m.options.Codepage)
	if codepage == "" {
		codepage = strings.TrimSpace(msg.Session.Encoding)
	}
	if codepage == "" {
		codepage = "cp037"
	}
	cm, err := decode.Codepage(codepage)
	if err != nil {
		ws.status = status{Level: statusError, Text: err.Error()}
		return nil
	}
	ws.sessionReady = true
	ws.browser = msg.Session.Browser
	ws.user = strings.ToUpper(strings.TrimSpace(msg.Session.User))
	ws.codepageName = cm.Name()
	ws.charmap = cm

	ws.prefix = strings.ToUpper(strings.TrimSpace(m.options.Prefix))
	if ws.prefix == "" && ws.user != "" {
		ws.prefix = ws.user + ".*"
	}
	ws.status = status{Level: statusReady, Text: "session ready"}

	// Only the active workspace should drive initial fetch and input focus.
	if ws != &m.workspace {
		return nil
	}
	m.prefixInput.SetValue(ws.prefix)

	var commands []tea.Cmd
	if source := m.initialCopybookSource(); !source.empty() {
		commands = append(commands, m.startOverlay(ws, source))
	}
	if ws.prefix == "" {
		ws.status = status{Level: statusReady, Text: "enter a data set prefix"}
		commands = append(commands, m.prefixInput.Focus())
	} else if m.budget > 0 {
		ws.datasetPage.reset(m.visible, m.budget)
		commands = append(commands, m.startDataSets(ws, ws.datasetPage.initialPlan("")))
	}
	return tea.Batch(commands...)
}

func (m *Model) initialCopybookSource() CopybookSource {
	return CopybookSource{
		Local: m.options.Copybook, DSN: m.options.CopybookDSN, Format: m.options.Format, Record: m.options.Record,
	}
}

func (m *Model) handleMouseWheel(msg tea.MouseWheelMsg) tea.Cmd {
	if m.editor != nil {
		updated, cmd := m.editor.area.Update(msg)
		m.editor.area = updated
		return cmd
	}
	if m.query != nil {
		switch msg.Button {
		case tea.MouseWheelUp:
			m.query.scrollBy(-mouseWheelStep)
		case tea.MouseWheelDown:
			m.query.scrollBy(mouseWheelStep)
		}
		return nil
	}
	if view := m.mappingView; view != nil {
		if view.form == nil {
			switch msg.Button {
			case tea.MouseWheelUp:
				view.move(-1)
			case tea.MouseWheelDown:
				view.move(1)
			}
		}
		return nil
	}
	switch msg.Button {
	case tea.MouseWheelLeft:
		return m.handleAction(actionWideLeft)
	case tea.MouseWheelRight:
		return m.handleAction(actionWideRight)
	}

	delta := mouseWheelStep
	if msg.Button == tea.MouseWheelUp {
		delta = -delta
	} else if msg.Button != tea.MouseWheelDown {
		return nil
	}

	if m.showHelp {
		m.helpVertical = max(0, m.helpVertical+delta)
		return nil
	}
	if m.favPopup != nil {
		m.favPopup.move(delta)
		return nil
	}
	if m.scrollJSON(delta) {
		return nil
	}
	return m.moveSelection(delta)
}

// scrollJSON adjusts the JSON viewport when it is the active scroll target,
// so input handlers do not each re-derive the screen+mode special case.
func (m *Model) scrollJSON(delta int) bool {
	ws := m.ws()
	if ws.screen != ScreenRecords || ws.recordMode != ModeJSON {
		return false
	}
	ws.jsonVertical = min(m.maxJSONVertical(), max(0, ws.jsonVertical+delta))
	return true
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	ws := m.ws()
	ctx := keyContext{
		Screen: ws.screen, Mode: ws.recordMode,
		InputFocused: m.inputFocused(), DialogOpen: m.mappingView != nil,
		DialogFormFocused: m.mappingView != nil && m.mappingView.form != nil,
		ShowHelp:          m.showHelp, Tabs: m.hasTabs(),
		FavoritesOpen: m.favPopup != nil, FavoritesInput: m.favPopup != nil && m.favPopup.inputActive(),
		EditorOpen: m.editor != nil, EditorConfirm: m.editor != nil && m.editor.confirmDiscard,
		QueryOpen: m.query != nil,
	}
	selectedAction := m.keys.actionFor(msg, ctx)
	if m.editor != nil {
		return m.handleEditorKey(msg, selectedAction)
	}
	if m.favPopup != nil {
		return m.handleFavoritesKey(msg, selectedAction)
	}
	if m.query != nil {
		return m.handleQueryKey(msg, selectedAction)
	}
	if view := m.mappingView; view != nil {
		if view.form != nil {
			switch selectedAction {
			case actionAccept:
				return m.submitMappingForm(ws)
			case actionCancel:
				view.form = nil
				if len(view.entries) == 0 {
					m.mappingView = nil
				}
				return nil
			case actionNextField:
				return view.form.moveFocus(1)
			case actionPreviousField:
				return view.form.moveFocus(-1)
			default:
				return view.form.update(msg)
			}
		}
		switch selectedAction {
		case actionUp:
			view.move(-1)
			return nil
		case actionDown:
			view.move(1)
			return nil
		case actionAccept:
			return m.applySelectedMapping(ws)
		case actionMappingAdd:
			view.openForm(CopybookSource{}, view.target, false)
			view.setWidth(m.width)
			return nil
		case actionMappingEdit:
			if entry := view.selectedEntry(); entry != nil {
				view.openForm(mappingSource(entry.Mapping), entry.Mapping.Pattern, true)
				view.setWidth(m.width)
			}
			return nil
		case actionMappingRemove:
			return m.removeSelectedMapping(ws)
		case actionCancel:
			m.mappingView = nil
			return nil
		default:
			return nil
		}
	}
	if m.inputFocused() {
		switch selectedAction {
		case actionAccept:
			return m.acceptSearch()
		case actionCancel:
			m.cancelSearch()
			return nil
		default:
			input := m.focusedInput()
			updated, cmd := input.Update(msg)
			*input = updated
			return cmd
		}
	}
	return m.handleAction(selectedAction)
}

func (m *Model) handleAction(selected action) tea.Cmd {
	ws := m.ws()
	switch selected {
	case actionNone:
		return nil
	case actionQuit:
		m.flushMappingOps()
		if m.query != nil {
			m.query.stopSearch()
		}
		m.workspace.dropBulkRecords()
		for _, ws := range m.workspaces {
			ws.dropBulkRecords()
		}
		m.cancelAll()
		return tea.Quit
	case actionHelp:
		m.showHelp = !m.showHelp
		m.helpVertical = 0
		return nil
	case actionNextProfile:
		return m.switchProfile(1)
	case actionPreviousProfile:
		return m.switchProfile(-1)
	case actionSearch:
		if ws.screen == ScreenDataSets {
			m.prefixInput.SetValue(ws.prefix)
			m.prefixInput.CursorEnd()
			return m.prefixInput.Focus()
		}
		if ws.screen == ScreenMembers {
			m.memberInput.SetValue(ws.memberPattern)
			m.memberInput.CursorEnd()
			return m.memberInput.Focus()
		}
		if ws.screen == ScreenRecords {
			m.locateInput.SetValue("")
			return m.locateInput.Focus()
		}
	case actionUp:
		return m.moveSelection(-1)
	case actionDown:
		return m.moveSelection(1)
	case actionPageUp:
		if m.scrollJSON(-max(1, m.visible)) {
			return nil
		}
		return m.pageSelection(scrollUp)
	case actionPageDown:
		if m.scrollJSON(max(1, m.visible)) {
			return nil
		}
		return m.pageSelection(scrollDown)
	case actionTop:
		m.activePagerTop()
		return nil
	case actionBottom:
		return m.activePagerBottom()
	case actionOpen:
		return m.openSelection()
	case actionBack:
		return m.navigateBack()
	case actionRefresh:
		return m.refresh()
	case actionToggleFavorite:
		m.toggleFavorite()
		return nil
	case actionFavorites:
		m.openFavoritesPopup()
		return nil
	case actionRecall:
		return m.startRecall()
	case actionEdit:
		return m.beginEdit()
	case actionQuery:
		return m.openQueryPopup()
	case actionCopybook:
		m.openMappingView(ws)
		return nil
	case actionClearOverlay:
		m.clearOverlay()
		return nil
	case actionToggleOverlay:
		if ws.overlay == nil {
			ws.status = status{Level: statusWarn, Text: "load a copybook with c before enabling an overlay"}
			return nil
		}
		if ws.recordMode == ModeRaw {
			ws.recordMode = ws.decodedMode
		} else {
			ws.decodedMode = ws.recordMode
			ws.recordMode = ModeRaw
		}
		ws.horizontal = 0
		ws.jsonVertical = 0
		return nil
	case actionToggleView:
		if ws.overlay == nil {
			ws.status = status{Level: statusWarn, Text: "load a copybook with c before selecting table or JSON"}
			return nil
		}
		if ws.decodedMode == ModeTable {
			ws.decodedMode = ModeJSON
		} else {
			ws.decodedMode = ModeTable
		}
		if ws.recordMode != ModeRaw {
			ws.recordMode = ws.decodedMode
		}
		ws.horizontal = 0
		ws.jsonVertical = 0
		return nil
	case actionDiagnostics:
		ws.showDiagnostics = !ws.showDiagnostics
		return nil
	case actionWideLeft:
		if ws.horizontal > 0 {
			ws.horizontal--
		}
		return nil
	case actionWideRight:
		if ws.horizontal < m.maxHorizontal() {
			ws.horizontal++
		}
		return nil
	case actionHelpUp:
		m.helpVertical = max(0, m.helpVertical-1)
		return nil
	case actionHelpDown:
		m.helpVertical++
		return nil
	case actionHelpPageUp:
		m.helpVertical = max(0, m.helpVertical-max(1, m.visible))
		return nil
	case actionHelpPageDown:
		m.helpVertical += max(1, m.visible)
		return nil
	case actionHelpTop:
		m.helpVertical = 0
		return nil
	case actionHelpBottom:
		m.helpVertical = maxScrollOffset
		return nil
	}
	return nil
}

func (m *Model) switchProfile(delta int) tea.Cmd {
	if !m.hasTabs() {
		return nil
	}
	m.cancelSearch()
	m.saveActiveWorkspace()
	m.active += delta
	if m.active >= len(m.profiles) {
		m.active = 0
	} else if m.active < 0 {
		m.active = len(m.profiles) - 1
	}
	m.loadWorkspace(m.active)
	ws := m.ws()
	m.syncInputsFromWorkspace()
	m.helpVertical = 0
	var command tea.Cmd
	if !ws.sessionReady && ws.sessionCancel == nil {
		command = m.loadActiveSession()
	} else {
		command = m.ensureActivePage()
	}
	// The tick loop stops once the outgoing workspace goes idle; restart it so
	// the incoming workspace's RECALL indicators keep animating.
	if ws.hasRecalls() {
		command = tea.Batch(command, m.spinner.Tick)
	}
	return command
}

func (m *Model) clearOverlay() {
	ws := m.ws()
	ws.cancelOverlay()
	ws.pendingMapping = nil
	ws.overlayGeneration++
	ws.cancelDecode()
	ws.overlay = nil
	ws.overlaySource = CopybookSource{}
	ws.overlayError = ""
	ws.overlayMappedPattern = ""
	ws.recordMode = ModeRaw
	ws.horizontal = 0
	ws.jsonVertical = 0
	for i := range ws.records {
		ws.records[i].Decoded = nil
		ws.records[i].Err = nil
	}
	ws.status = status{Level: statusReady, Text: "copybook overlay cleared"}
}

// openMappingView opens the combined mapping screen: every persisted mapping
// matching the current data set, most precise first, so unintentional matches
// are visible. With no matches it opens straight into the add form seeded with
// the exact data set name.
func (m *Model) openMappingView(ws *workspace) {
	view := &mappingView{target: ws.matchName()}
	if m.deps.Mappings != nil {
		seen := map[string]struct{}{}
		add := func(mappings []dsnmap.Mapping) {
			for _, mapping := range mappings {
				if _, ok := seen[mapping.Pattern]; ok {
					continue
				}
				seen[mapping.Pattern] = struct{}{}
				view.entries = append(view.entries, mappingEntry{
					Mapping: mapping, Applied: mapping.Pattern == ws.overlayMappedPattern,
				})
			}
		}
		add(m.deps.Mappings.Matches(view.target))
		if ws.member != nil {
			add(m.deps.Mappings.Matches(ws.dataSet.Name))
		}
	}
	for i, entry := range view.entries {
		if entry.Applied {
			view.selected = i
		}
	}
	if len(view.entries) == 0 {
		view.openForm(ws.overlaySource, view.target, false)
	}
	view.setWidth(m.width)
	m.mappingView = view
}

// applySelectedMapping applies the selected persisted mapping as the overlay.
func (m *Model) applySelectedMapping(ws *workspace) tea.Cmd {
	view := m.mappingView
	entry := view.selectedEntry()
	if entry == nil {
		return nil
	}
	m.mappingView = nil
	if m.deps.Mappings != nil {
		_ = m.deps.Mappings.Touch(entry.Mapping.Pattern)
	}
	ws.overlayMappedPattern = entry.Mapping.Pattern
	return m.startOverlay(ws, mappingSource(entry.Mapping))
}

// removeSelectedMapping drops the selected mapping from the list and queues
// the background removal.
func (m *Model) removeSelectedMapping(ws *workspace) tea.Cmd {
	view := m.mappingView
	pattern := view.removeSelected()
	if pattern == "" {
		return nil
	}
	if ws.overlayMappedPattern == pattern {
		ws.overlayMappedPattern = ""
	}
	view.err = ""
	view.note = "removed mapping " + pattern
	return m.queueMappingOps(mappingOp{removePattern: pattern})
}

// submitMappingForm applies the form: a copybook value loads the overlay and,
// on success, persists the mapping in the background; an empty copybook clears
// the overlay and removes the mapping through the same background path.
func (m *Model) submitMappingForm(ws *workspace) tea.Cmd {
	view := m.mappingView
	form := view.form
	pattern := dsnmap.NormalizePattern(form.pattern.Value())
	if pattern == "" {
		view.err = "enter a DSN pattern"
		return nil
	}
	if err := dsnmap.ValidatePattern(pattern); err != nil {
		view.err = err.Error()
		return nil
	}
	source := form.formSource()
	if source.empty() {
		removeTarget := form.original
		if removeTarget == "" {
			removeTarget = pattern
		}
		m.mappingView = nil
		if ws.overlay != nil || ws.overlayPending {
			m.clearOverlay()
		}
		if ws.overlayMappedPattern == removeTarget {
			ws.overlayMappedPattern = ""
		}
		command := m.queueMappingOps(mappingOp{removePattern: removeTarget})
		if command != nil {
			ws.status = status{Level: statusReady, Text: "copybook cleared; removed mapping " + removeTarget}
		}
		return command
	}
	validated, _, err := source.validate()
	if err != nil {
		view.err = err.Error()
		return nil
	}
	m.mappingView = nil
	ws.overlayMappedPattern = ""
	command := m.startOverlay(ws, validated)
	ws.pendingMapping = &pendingMappingSave{
		mapping: dsnmap.Mapping{
			Pattern: pattern, Local: validated.Local, DSN: validated.DSN, Record: validated.Record,
		},
		removeOld: form.original,
	}
	return command
}

// queueMappingOps schedules background store writes behind the debounce
// timer. Ops queue even while a flush is pending; a newer op re-arms the
// timer and invalidates the older one.
func (m *Model) queueMappingOps(ops ...mappingOp) tea.Cmd {
	if m.deps.Mappings == nil || len(ops) == 0 {
		return nil
	}
	m.mappingOps = append(m.mappingOps, ops...)
	m.mappingSeq++
	seq := m.mappingSeq
	return tea.Tick(mappingFlushDelay, func(time.Time) tea.Msg { return mappingFlushMsg{Seq: seq} })
}

// flushMappingOps writes queued mapping changes to the store, in order.
// Failures surface in the status line but never block browsing.
func (m *Model) flushMappingOps() {
	ops := m.mappingOps
	m.mappingOps = nil
	if m.deps.Mappings == nil {
		return
	}
	for _, op := range ops {
		var err error
		switch {
		case op.removePattern != "":
			_, err = m.deps.Mappings.Remove(op.removePattern)
		case op.put != nil:
			err = m.deps.Mappings.Put(*op.put)
		}
		if err != nil {
			m.ws().status = status{Level: statusError, Text: "mapping save failed: " + err.Error()}
		}
	}
}

// autoApplyMapping starts the overlay for a persisted mapping when a records
// screen is entered with no overlay active. Explicit --copybook/--copybook-dsn
// flags override persisted mappings, and a failed load degrades to the raw
// view through the normal overlay error path.
func (m *Model) autoApplyMapping(ws *workspace) tea.Cmd {
	if m.deps.Mappings == nil || ws.overlay != nil || ws.overlayPending || !ws.overlaySource.empty() {
		return nil
	}
	if !m.initialCopybookSource().empty() {
		return nil
	}
	mapping, ok := m.deps.Mappings.Match(ws.matchName())
	if !ok && ws.member != nil {
		mapping, ok = m.deps.Mappings.Match(ws.dataSet.Name)
	}
	if !ok {
		return nil
	}
	_ = m.deps.Mappings.Touch(mapping.Pattern)
	ws.overlayMappedPattern = mapping.Pattern
	return m.startOverlay(ws, mappingSource(mapping))
}

func mappingSource(mapping dsnmap.Mapping) CopybookSource {
	return CopybookSource{Local: mapping.Local, DSN: mapping.DSN, Format: mapping.Format, Record: mapping.Record}
}

func (m *Model) focusedInput() *textinput.Model {
	for _, input := range []*textinput.Model{&m.prefixInput, &m.memberInput, &m.locateInput} {
		if input.Focused() {
			return input
		}
	}
	return nil
}

func (m *Model) inputFocused() bool {
	return m.focusedInput() != nil
}

func (m *Model) acceptSearch() tea.Cmd {
	ws := m.ws()
	if m.locateInput.Focused() {
		return m.acceptRecordLocation()
	}
	if m.prefixInput.Focused() {
		prefix := strings.ToUpper(strings.TrimSpace(m.prefixInput.Value()))
		if prefix == "" {
			ws.status = status{Level: statusError, Text: "data set prefix must not be empty"}
			return nil
		}
		m.prefixInput.Blur()
		ws.cancelBrowse()
		ws.cancelDecode()
		ws.prefix = prefix
		ws.datasetPage.reset(m.visible, m.budget)
		ws.datasets = nil
		ws.datasetTotal = nil
		ws.dataSet = zosmf.DataSet{}
		ws.resetMemberState()
		if m.budget <= 0 {
			return nil
		}
		return m.startDataSets(ws, ws.datasetPage.initialPlan(""))
	}
	pattern := strings.ToUpper(strings.TrimSpace(m.memberInput.Value()))
	if pattern != "" && len(pattern) < 8 && !strings.ContainsAny(pattern, "*%") {
		pattern += "*"
	}
	m.memberInput.Blur()
	ws.cancelBrowse()
	ws.cancelDecode()
	ws.memberPattern = pattern
	ws.resetMemberState()
	ws.status = status{Level: statusReady, Text: "member filter " + displayOr(pattern, "*")}
	if m.budget <= 0 {
		return nil
	}
	return m.startMembers(ws, ws.memberPage.initialPlan(""))
}

func (m *Model) acceptRecordLocation() tea.Cmd {
	ws := m.ws()
	value := strings.TrimSpace(m.locateInput.Value())
	number, err := strconv.ParseInt(value, 10, 64)
	if err != nil || number <= 0 {
		ws.status = status{Level: statusError, Text: "record number must be a positive integer"}
		return nil
	}
	m.locateInput.Blur()
	if ws.recordPage.selectKey(strconv.FormatInt(number, 10)) {
		ws.jsonVertical = 0
		ws.status = status{Level: statusReady, Text: fmt.Sprintf("located record %d", number)}
		return m.maybePrefetch(ws)
	}

	ws.cancelBrowse()
	ws.cancelDecode()
	ws.resetRecordState()
	return m.startRecords(ws, ws.recordPage.initialPlan(number))
}

func (m *Model) cancelSearch() {
	if input := m.focusedInput(); input != nil {
		input.Blur()
		m.syncInputsFromWorkspace()
	}
}

func (m *Model) activePager() pagerNavigator {
	return m.ws().activePager()
}

func (m *Model) updateActivePager(update func(pagerNavigator)) {
	ws := m.ws()
	pager := ws.activePager()
	if pager == nil {
		return
	}
	selected := pager.selectedKey()
	update(pager)
	if ws.screen == ScreenRecords && pager.selectedKey() != selected {
		ws.jsonVertical = 0
	}
}

func (m *Model) moveSelection(delta int) tea.Cmd {
	m.updateActivePager(func(pager pagerNavigator) { pager.move(delta) })
	return m.maybePrefetch(m.ws())
}

func (m *Model) pageSelection(direction scrollDirection) tea.Cmd {
	m.updateActivePager(func(pager pagerNavigator) { pager.page(direction) })
	return m.maybePrefetch(m.ws())
}

func (m *Model) activePagerTop() {
	m.updateActivePager(func(pager pagerNavigator) { pager.top() })
}

func (m *Model) activePagerBottom() tea.Cmd {
	m.updateActivePager(func(pager pagerNavigator) { pager.bottom() })
	return m.maybePrefetch(m.ws())
}

func (m *Model) openSelection() tea.Cmd {
	ws := m.ws()
	if m.budget <= 0 {
		return nil
	}
	switch ws.screen {
	case ScreenDataSets:
		index := ws.datasetPage.selectedIndex()
		if index < 0 || index >= len(ws.datasets) {
			return nil
		}
		selected := ws.datasets[index]
		if zosmf.IsMigrated(selected) {
			if ws.recallPending(strings.ToUpper(strings.TrimSpace(selected.Name))) {
				ws.status = status{Level: statusLoading, Text: "recall of " + selected.Name + " is in progress"}
			} else {
				ws.status = status{Level: statusWarn, Text: selected.Name + " is migrated; press R to recall it"}
			}
			return nil
		}
		organization := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(selected.Organization), " ", ""))
		switch organization {
		case "PS", "SEQ", "PS-L", "PSL":
			ws.cancelBrowse()
			ws.cancelDecode()
			ws.dataSet = selected
			ws.resetMemberState()
			ws.screen = ScreenRecords
			m.recordOpen(selected.Name)
			return tea.Batch(m.startRecords(ws, ws.recordPage.initialPlan(0)), m.autoApplyMapping(ws))
		case "PO", "PO-E", "POE", "PDS", "PDSE":
			ws.cancelBrowse()
			ws.cancelDecode()
			ws.dataSet = selected
			ws.resetMemberState()
			ws.screen = ScreenMembers
			ws.memberPattern = ""
			m.memberInput.SetValue("")
			m.recordOpen(selected.Name)
			return m.startMembers(ws, ws.memberPage.initialPlan(""))
		default:
			if organization == "" {
				organization = "unknown"
			}
			ws.status = status{Level: statusWarn, Text: fmt.Sprintf("%s uses unsupported DSORG %s; sequential, large-format sequential, PDS, and PDSE data sets are readable", selected.Name, organization)}
		}
	case ScreenMembers:
		index := ws.memberPage.selectedIndex()
		if index < 0 || index >= len(ws.members) {
			return nil
		}
		selected := ws.members[index]
		ws.cancelBrowse()
		ws.cancelDecode()
		ws.member = &selected
		ws.screen = ScreenRecords
		ws.resetRecordState()
		m.recordOpen(fmt.Sprintf("%s(%s)", ws.dataSet.Name, selected.Name))
		return tea.Batch(m.startRecords(ws, ws.recordPage.initialPlan(0)), m.autoApplyMapping(ws))
	}
	return nil
}

// recordOpen tracks a data set or member open for the local usage log. It
// records the navigation, not the fetch outcome: an open whose read later
// fails still reflects what the user reached for, which is what a
// suggestion engine should rank.
func (m *Model) recordOpen(name string) {
	if m.deps.Events != nil {
		m.deps.Events.RecordOpen(strings.ToUpper(strings.TrimSpace(name)))
	}
}

func (m *Model) navigateBack() tea.Cmd {
	ws := m.ws()
	m.cancelSearch()
	switch ws.screen {
	case ScreenRecords:
		ws.cancelBrowse()
		ws.cancelDecode()
		fromMember := ws.member != nil
		ws.resetRecordState()
		if fromMember {
			ws.member = nil
			ws.screen = ScreenMembers
			ws.statusForCount(len(ws.members), "members")
			return m.ensureActivePage()
		}
		ws.screen = ScreenDataSets
		ws.statusForCount(len(ws.datasets), "data sets")
		return m.ensureActivePage()
	case ScreenMembers:
		ws.cancelBrowse()
		ws.cancelDecode()
		ws.resetMemberState()
		ws.screen = ScreenDataSets
		ws.dataSet = zosmf.DataSet{}
		ws.statusForCount(len(ws.datasets), "data sets")
		return m.ensureActivePage()
	}
	return nil
}

func (m *Model) refresh() tea.Cmd {
	ws := m.ws()
	if !ws.sessionReady || m.budget <= 0 {
		return nil
	}
	ws.cancelBrowse()
	switch ws.screen {
	case ScreenDataSets:
		plan := ws.datasetPage.refreshPlan()
		ws.datasets = nil
		ws.datasetTotal = nil
		ws.datasetPage.reset(m.visible, m.budget)
		return m.startDataSets(ws, plan)
	case ScreenMembers:
		plan := ws.memberPage.refreshPlan()
		ws.members = nil
		ws.memberTotal = nil
		ws.memberPage.reset(m.visible, m.budget)
		return m.startMembers(ws, plan)
	case ScreenRecords:
		plan := ws.recordPage.refreshPlan()
		ws.cancelDecode()
		// A refresh means the data may have changed on the host; any cached
		// bulk download is stale.
		ws.dropBulkRecords()
		ws.records = nil
		ws.rawLongest = 0
		ws.syntaxKind = sourcePlain
		ws.syntaxSampled = 0
		ws.recordPage.reset(m.visible, m.budget)
		return m.startRecords(ws, plan)
	}
	return nil
}

// startRecall submits an HRECALL for the selected migrated data set. z/OSMF
// only acknowledges the request, so completion is watched by polling the
// catalog; the row shows a RECALL indicator until the data set leaves
// migration storage.
func (m *Model) startRecall() tea.Cmd {
	ws := m.ws()
	if !ws.sessionReady || ws.browser == nil {
		return nil
	}
	index := ws.datasetPage.selectedIndex()
	if index < 0 || index >= len(ws.datasets) {
		return nil
	}
	selected := ws.datasets[index]
	name := strings.ToUpper(strings.TrimSpace(selected.Name))
	if ws.recallPending(name) {
		ws.status = status{Level: statusLoading, Text: "recall of " + name + " is already in progress"}
		return nil
	}
	if !zosmf.IsMigrated(selected) {
		ws.status = status{Level: statusWarn, Text: name + " is not migrated"}
		return nil
	}
	recaller, ok := ws.browser.(zosmf.Recaller)
	if !ok {
		ws.status = status{Level: statusWarn, Text: "this session cannot recall data sets"}
		return nil
	}
	ws.markRecall(name)
	profile := ws.profile
	ctx, cancel := context.WithTimeout(context.Background(), m.deps.Timeout)
	ws.status = status{Level: statusLoading, Text: "requesting recall of " + name}
	return m.loadingCommand(func() tea.Msg {
		defer cancel()
		err := recaller.RecallDataSet(ctx, name)
		return recallResultMsg{Profile: profile, DSN: name, Err: err}
	})
}

func (m *Model) handleRecallResult(ws *workspace, msg recallResultMsg) tea.Cmd {
	if !ws.recallPending(msg.DSN) {
		return nil
	}
	if msg.Err != nil {
		ws.clearRecall(msg.DSN)
		ws.status = status{Level: statusError, Text: fmt.Sprintf("recall of %s failed: %v", msg.DSN, msg.Err)}
		return nil
	}
	if ws == &m.workspace && ws.browsePending == nil {
		ws.status = status{Level: statusLoading, Text: "recalling " + msg.DSN}
	}
	return scheduleRecallPoll(msg.Profile, msg.DSN, 1)
}

func scheduleRecallPoll(profile, dsn string, attempt int) tea.Cmd {
	return tea.Tick(recallPollDelay(attempt), func(time.Time) tea.Msg {
		return recallPollMsg{Profile: profile, DSN: dsn, Attempt: attempt}
	})
}

// handleRecallPoll re-lists one data set to see whether its recall finished.
// The request runs outside the browse pipeline so paging and refreshes are
// never displaced by the watcher.
func (m *Model) handleRecallPoll(ws *workspace, msg recallPollMsg) tea.Cmd {
	if !ws.recallPending(msg.DSN) || ws.browser == nil {
		return nil
	}
	browser := ws.browser
	ctx, cancel := context.WithTimeout(context.Background(), m.deps.Timeout)
	request := zosmf.ListDataSetsRequest{Prefix: msg.DSN, MaxItems: 1}
	return func() tea.Msg {
		defer cancel()
		page, err := browser.ListDataSets(ctx, request)
		return recallCheckMsg{Profile: msg.Profile, DSN: msg.DSN, Attempt: msg.Attempt, Page: page, Err: err}
	}
}

func (m *Model) handleRecallCheck(ws *workspace, msg recallCheckMsg) tea.Cmd {
	if !ws.recallPending(msg.DSN) {
		return nil
	}
	if msg.Err != nil {
		ws.setRecallIssue(msg.DSN, msg.Err.Error())
	} else if resolveRecalledDataSet(ws, msg.DSN, msg.Page.Items) {
		return nil
	}
	if msg.Attempt >= maxRecallPolls {
		text := "recall of " + msg.DSN + " is still running on the host; refresh later with r"
		if issue := ws.recallIssue(msg.DSN); issue != "" {
			text = "recall watch for " + msg.DSN + " gave up; last catalog check failed: " + issue
		}
		ws.clearRecall(msg.DSN)
		ws.status = status{Level: statusWarn, Text: text}
		return nil
	}
	return scheduleRecallPoll(msg.Profile, msg.DSN, msg.Attempt+1)
}

// resolveRecalledDataSet finishes the recall watch for dsn if items show it on
// primary storage again: the cached row refreshes in place and the status
// reports the new volume. Browse results reuse this so a manual refresh
// resolves an indicator even when the watcher's own check has not fired yet.
func resolveRecalledDataSet(ws *workspace, dsn string, items []zosmf.DataSet) bool {
	for _, item := range items {
		if strings.ToUpper(strings.TrimSpace(item.Name)) != dsn {
			continue
		}
		if zosmf.IsMigrated(item) {
			return false
		}
		ws.clearRecall(dsn)
		replaceDataSetEntry(ws, dsn, item)
		text := "recalled " + dsn
		if volume := displayOr(item.Volume, item.Volumes); volume != "" {
			text += " to " + volume
		}
		if ws.browsePending == nil {
			ws.status = status{Level: statusReady, Text: text}
		}
		return true
	}
	return false
}

// replaceDataSetEntry refreshes one cached catalog row after a recall, so the
// volume and attributes reflect primary storage without a full refresh.
func replaceDataSetEntry(ws *workspace, name string, item zosmf.DataSet) {
	for i := range ws.datasets {
		if strings.ToUpper(strings.TrimSpace(ws.datasets[i].Name)) == name {
			ws.datasets[i] = item
			return
		}
	}
}

func (m *Model) ensureActivePage() tea.Cmd {
	ws := m.ws()
	if !ws.sessionReady || m.budget <= 0 {
		return nil
	}
	switch ws.screen {
	case ScreenDataSets:
		if len(ws.datasets) == 0 && ws.prefix != "" {
			return m.startDataSets(ws, ws.datasetPage.initialPlan(""))
		}
	case ScreenMembers:
		if len(ws.members) == 0 && ws.dataSet.Name != "" {
			return m.startMembers(ws, ws.memberPage.initialPlan(""))
		}
	case ScreenRecords:
		if len(ws.records) == 0 && ws.dataSet.Name != "" {
			return m.startRecords(ws, ws.recordPage.initialPlan(0))
		}
	}
	return m.maybePrefetch(ws)
}

func (m *Model) handleResize(width, height int) tea.Cmd {
	ws := m.ws()
	oldBudget := m.budget
	m.width, m.height = width, height
	m.visible = VisibleRows(width, height)
	m.budget = RowBudget(m.visible)
	m.help.SetWidth(max(1, width))
	m.prefixInput.SetWidth(max(8, width-10))
	m.memberInput.SetWidth(max(8, min(24, width-10)))
	m.locateInput.SetWidth(max(8, min(24, width-10)))
	if m.mappingView != nil {
		m.mappingView.setWidth(width)
	}
	if m.favPopup != nil {
		m.favPopup.setWidth(width)
	}
	if m.query != nil {
		m.query.setWidth(width)
	}
	m.resizeEditor()

	ws.resizePagers(m.visible, m.budget)
	ws.jsonVertical = min(ws.jsonVertical, m.maxJSONVertical())

	if m.budget <= 0 {
		ws.cancelBrowse()
		return nil
	}
	if !ws.sessionReady {
		return nil
	}
	if ws.browsePending != nil && ws.browsePending.Budget != m.budget {
		ws.cancelBrowse()
	}
	if oldBudget == 0 {
		return m.ensureActivePage()
	}
	return m.maybePrefetch(ws)
}

func (m *Model) maybePrefetch(ws *workspace) tea.Cmd {
	if ws != &m.workspace {
		return nil
	}
	if !ws.canFetch(m.budget) || ws.browsePending != nil {
		return nil
	}
	switch ws.screen {
	case ScreenDataSets:
		if plan, ok := forwardNamePlan(&ws.datasetPage); ok {
			return m.startDataSets(ws, plan)
		}
	case ScreenMembers:
		if plan, ok := forwardNamePlan(&ws.memberPage); ok {
			return m.startMembers(ws, plan)
		}
	case ScreenRecords:
		if plan, ok := forwardRecordPlan(&ws.recordPage); ok {
			return m.startRecords(ws, plan)
		}
	}
	return nil
}

func (m *Model) startDataSets(ws *workspace, plan pagePlan[string]) tea.Cmd {
	if !ws.canFetch(m.budget) || ws.prefix == "" {
		return nil
	}
	ws.cancelBrowse()
	ws.browseGeneration++
	meta := requestMeta{
		Generation: ws.browseGeneration, Screen: ScreenDataSets,
		Identity: ws.prefix, Profile: ws.profile, Budget: m.budget, NamePlan: plan,
	}
	ws.browsePending = &meta
	ctx, cancel := context.WithTimeout(context.Background(), m.deps.Timeout)
	ws.browseCancel = cancel
	ws.status = status{Level: statusLoading, Text: fmt.Sprintf("listing data sets for %s", ws.prefix)}
	browser := ws.browser
	request := zosmf.ListDataSetsRequest{Prefix: ws.prefix, Start: plan.Anchor, MaxItems: m.budget}
	return m.loadingCommand(func() tea.Msg {
		page, err := browser.ListDataSets(ctx, request)
		return dataSetsResultMsg{Meta: meta, Page: page, Err: err}
	})
}

func (m *Model) startMembers(ws *workspace, plan pagePlan[string]) tea.Cmd {
	if !ws.canFetch(m.budget) || ws.dataSet.Name == "" {
		return nil
	}
	ws.cancelBrowse()
	ws.browseGeneration++
	meta := requestMeta{
		Generation: ws.browseGeneration, Screen: ScreenMembers,
		Identity: ws.memberIdentity(), Profile: ws.profile, Budget: m.budget, NamePlan: plan,
	}
	ws.browsePending = &meta
	ctx, cancel := context.WithTimeout(context.Background(), m.deps.Timeout)
	ws.browseCancel = cancel
	ws.status = status{Level: statusLoading, Text: fmt.Sprintf("listing members of %s with filter %s", ws.dataSet.Name, displayOr(ws.memberPattern, "*"))}
	browser := ws.browser
	request := zosmf.ListMembersRequest{DataSet: ws.dataSet.Name, Start: plan.Anchor, Pattern: ws.memberPattern, MaxItems: m.budget}
	return m.loadingCommand(func() tea.Msg {
		page, err := browser.ListMembers(ctx, request)
		return membersResultMsg{Meta: meta, Page: page, Err: err}
	})
}

func (m *Model) startRecords(ws *workspace, plan pagePlan[int64]) tea.Cmd {
	if !ws.canFetch(m.budget) || ws.dataSet.Name == "" {
		return nil
	}
	ws.cancelBrowse()
	ws.browseGeneration++
	member := ""
	if ws.member != nil {
		member = ws.member.Name
	}
	identity := ws.recordIdentity()
	meta := requestMeta{
		Generation: ws.browseGeneration, Screen: ScreenRecords,
		Identity: identity, Profile: ws.profile, Budget: m.budget, RecordPlan: plan,
	}
	ws.browsePending = &meta
	ctx, cancel := context.WithTimeout(context.Background(), m.deps.Timeout)
	ws.browseCancel = cancel
	ws.status = status{Level: statusLoading, Text: fmt.Sprintf("reading records from %s", identity)}
	browser := ws.browser
	request := zosmf.ReadRecordsRequest{DataSet: ws.dataSet.Name, Member: member, Start: plan.Anchor, MaxItems: m.budget}
	return m.loadingCommand(func() tea.Msg {
		page, err := browser.ReadRecords(ctx, request)
		return recordsResultMsg{Meta: meta, Page: page, Err: err}
	})
}

func (m *Model) handleDataSetsResult(ws *workspace, msg dataSetsResultMsg) tea.Cmd {
	if !ws.acceptBrowse(msg.Meta, m.budget, ws.screen) {
		return nil
	}
	ws.finishBrowse()
	// The favorites "open" action queues at most one auto-open, bound to the
	// fetch that armed it; an accepted result from any other generation means
	// that fetch was superseded, so the queue clears without firing.
	autoOpen := ""
	if ws.autoOpen != "" {
		if msg.Meta.Generation == ws.autoOpenGeneration {
			autoOpen = ws.autoOpen
		}
		ws.autoOpen = ""
	}
	if msg.Err != nil {
		ws.status = status{Level: statusError, Text: msg.Err.Error()}
		return nil
	}
	var ended bool
	ws.datasets, ws.datasetTotal, ended = applyBrowsePage(
		ws.datasets, msg.Page.Items, &ws.datasetPage, ws.datasetTotal, msg.Page.TotalRows,
		msg.Page.MoreRows, msg.Meta.Budget, msg.Meta.NamePlan,
		func(item zosmf.DataSet) string { return strings.ToUpper(strings.TrimSpace(item.Name)) },
	)
	if ended {
		ws.status = status{Level: statusReady, Text: "end of data set results"}
		return nil
	}
	ws.statusForCount(len(ws.datasets), "data sets")
	// A refresh or page fetch may observe a finished recall before the
	// watcher's next check; resolve those indicators from this page too.
	if ws.hasRecalls() {
		pending := make([]string, 0, len(ws.recalls))
		for name := range ws.recalls {
			pending = append(pending, name)
		}
		for _, name := range pending {
			resolveRecalledDataSet(ws, name, msg.Page.Items)
		}
	}
	if ws != &m.workspace {
		// A backgrounded profile cannot change screens; leave the favorite
		// selected so switching back shows it instead of silently dropping
		// the open.
		if autoOpen != "" {
			ws.datasetPage.selectKey(autoOpen)
		}
		return nil
	}
	if autoOpen != "" {
		if ws.datasetPage.selectKey(autoOpen) {
			return m.openSelection()
		}
		ws.status = status{Level: statusWarn, Text: "favorite " + autoOpen + " was not found"}
	}
	return m.maybePrefetch(ws)
}

func (m *Model) handleMembersResult(ws *workspace, msg membersResultMsg) tea.Cmd {
	if !ws.acceptBrowse(msg.Meta, m.budget, ws.screen) {
		return nil
	}
	ws.finishBrowse()
	if msg.Err != nil {
		ws.status = status{Level: statusError, Text: msg.Err.Error()}
		return nil
	}
	var ended bool
	ws.members, ws.memberTotal, ended = applyBrowsePage(
		ws.members, msg.Page.Items, &ws.memberPage, ws.memberTotal, msg.Page.TotalRows,
		msg.Page.MoreRows, msg.Meta.Budget, msg.Meta.NamePlan,
		func(item zosmf.Member) string { return strings.ToUpper(strings.TrimSpace(item.Name)) },
	)
	if ended {
		ws.status = status{Level: statusReady, Text: "end of member results"}
		return nil
	}
	ws.statusForCount(len(ws.members), "members")
	if ws != &m.workspace {
		return nil
	}
	return m.maybePrefetch(ws)
}

func (m *Model) handleRecordsResult(ws *workspace, msg recordsResultMsg) tea.Cmd {
	if !ws.acceptBrowse(msg.Meta, m.budget, ws.screen) {
		return nil
	}
	ws.finishBrowse()
	if msg.Err != nil {
		ws.status = status{Level: statusError, Text: msg.Err.Error()}
		return nil
	}
	selected := ws.recordPage.selectedKey()
	incoming := make([]recordRow, len(msg.Page.Records))
	for i, item := range msg.Page.Records {
		incoming[i] = recordRow{Record: zosmf.Record{Number: item.Number, Data: append([]byte(nil), item.Data...)}}
	}
	var ended bool
	ws.records, _, ended = applyBrowsePage(
		ws.records, incoming, &ws.recordPage, nil, nil,
		msg.Page.MoreRows, msg.Meta.Budget, msg.Meta.RecordPlan,
		func(row recordRow) string { return strconv.FormatInt(row.Record.Number, 10) },
	)
	if msg.Meta.RecordPlan.Direction == pageForward {
		// Forward pages only append; scanning just the incoming rows keeps the
		// running maximum without re-decoding the whole cache on every fetch.
		bounded, _ := boundedWindow(incoming, msg.Meta.Budget)
		ws.rawLongest = max(ws.rawLongest, longestRawDisplayWidth(bounded, ws.charmap))
	} else {
		ws.rawLongest = longestRawDisplayWidth(ws.records, ws.charmap)
	}
	if ended {
		ws.status = status{Level: statusReady, Text: "end of records"}
		return nil
	}
	if ws.recordPage.selectedKey() != selected {
		ws.jsonVertical = 0
	}
	ws.statusForCount(len(ws.records), "records")
	if len(ws.records) == 0 {
		return nil
	}
	if ws != &m.workspace {
		return nil
	}
	if ws.overlay != nil {
		return tea.Batch(m.startDecode(ws), m.maybePrefetch(ws))
	}
	return m.maybePrefetch(ws)
}

func applyBrowsePage[T any, A comparable](
	cached, incoming []T,
	pager *pager[A],
	currentTotal, responseTotal *int,
	moreRows bool,
	budget int,
	plan pagePlan[A],
	key func(T) string,
) ([]T, *int, bool) {
	if plan.Direction == pageForward && len(incoming) == 0 {
		pager.more = false
		return cached, currentTotal, true
	}

	items, overReturned := boundedWindow(incoming, budget)
	keys := make([]string, len(items))
	for i, item := range items {
		keys[i] = key(item)
	}
	more := moreRows || overReturned
	if plan.Direction == pageForward {
		before := len(cached)
		cached = mergeCached(cached, items, key)
		if len(cached) == before {
			more = false
		}
	} else {
		cached = append([]T(nil), items...)
	}
	pager.apply(keys, more, plan)
	if responseTotal != nil || plan.Direction != pageForward {
		currentTotal = cloneInt(responseTotal)
	}
	return cached, currentTotal, false
}

func boundedWindow[T any](items []T, budget int) ([]T, bool) {
	if budget > 0 && len(items) > budget {
		return items[:budget], true
	}
	return items, false
}

func mergeCached[T any](existing, incoming []T, key func(T) string) []T {
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	merged := make([]T, 0, len(existing)+len(incoming))
	for _, item := range existing {
		itemKey := key(item)
		seen[itemKey] = struct{}{}
		merged = append(merged, item)
	}
	for _, item := range incoming {
		itemKey := key(item)
		if _, ok := seen[itemKey]; ok {
			continue
		}
		seen[itemKey] = struct{}{}
		merged = append(merged, item)
	}
	return merged
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func (m *Model) startOverlay(ws *workspace, source CopybookSource) tea.Cmd {
	ws.cancelOverlay()
	// A new overlay request supersedes any not-yet-persisted mapping; the
	// form handler re-stashes its own pending save after this call.
	ws.pendingMapping = nil
	ws.overlayError = ""
	ws.overlayGeneration++
	generation := ws.overlayGeneration
	ws.overlayPending = true
	ctx, cancel := context.WithTimeout(context.Background(), m.deps.Timeout)
	ws.overlayCancel = cancel
	browser := ws.browser
	codepage := ws.codepageName
	loadFile := m.deps.LoadFile
	searchPaths := append([]string(nil), m.deps.DSNSearchPath...)
	ws.status = status{Level: statusLoading, Text: "loading copybook overlay " + source.label()}
	return m.loadingCommand(func() tea.Msg {
		built, err := buildOverlay(ctx, source, codepage, browser, loadFile, searchPaths)
		return overlayResultMsg{Profile: ws.profile, Generation: generation, Source: source, Overlay: built, Err: err}
	})
}

func (m *Model) handleOverlayResult(ws *workspace, msg overlayResultMsg) tea.Cmd {
	if msg.Generation != ws.overlayGeneration || !ws.overlayPending {
		return nil
	}
	ws.cancelOverlay()
	if msg.Err != nil {
		ws.pendingMapping = nil
		ws.overlayError = msg.Err.Error()
		if ws.overlay != nil {
			ws.overlayError += "; previous overlay retained"
		}
		if ws.browsePending == nil {
			ws.status = status{Level: statusError, Text: ws.overlayError}
		}
		return nil
	}
	ws.overlayError = ""
	ws.cancelDecode()
	ws.overlay = msg.Overlay
	ws.overlaySource = msg.Overlay.Source
	for i := range ws.records {
		ws.records[i].Decoded = nil
		ws.records[i].Err = nil
	}
	ws.recordMode = ModeTable
	ws.decodedMode = ModeTable
	ws.horizontal = 0
	ws.jsonVertical = 0
	saveCmd := m.persistPendingMapping(ws)
	if ws.browsePending != nil {
		return saveCmd
	}
	ws.status = status{Level: statusReady, Text: fmt.Sprintf("overlay %s record %s", msg.Overlay.Source.label(), msg.Overlay.Record.Name)}
	if ws != &m.workspace {
		return saveCmd
	}
	if len(ws.records) > 0 && ws.screen == ScreenRecords {
		return tea.Batch(saveCmd, m.startDecode(ws))
	}
	return saveCmd
}

// persistPendingMapping queues the background save for an overlay that was
// applied through the mapping form, once the copybook has actually loaded.
func (m *Model) persistPendingMapping(ws *workspace) tea.Cmd {
	pending := ws.pendingMapping
	if pending == nil {
		return nil
	}
	ws.pendingMapping = nil
	ws.overlayMappedPattern = pending.mapping.Pattern
	ops := make([]mappingOp, 0, 2)
	if pending.removeOld != "" && pending.removeOld != pending.mapping.Pattern {
		ops = append(ops, mappingOp{removePattern: pending.removeOld})
	}
	ops = append(ops, mappingOp{put: &pending.mapping})
	return m.queueMappingOps(ops...)
}

func (m *Model) startDecode(ws *workspace) tea.Cmd {
	if ws.overlay == nil || len(ws.records) == 0 || ws.decodePending {
		return nil
	}
	records := make([]zosmf.Record, 0, len(ws.records))
	for _, row := range ws.records {
		if row.Decoded != nil || row.Err != nil {
			continue
		}
		records = append(records, zosmf.Record{Number: row.Record.Number, Data: append([]byte(nil), row.Record.Data...)})
	}
	if len(records) == 0 {
		return nil
	}

	ws.decodeGeneration++
	generation := ws.decodeGeneration
	ws.decodePending = true
	ctx, cancel := context.WithCancel(context.Background())
	ws.decodeCancel = cancel
	overlay := ws.overlay
	identity := ws.recordIdentity()
	// effectiveStatus reports the transient "decoding records" state while
	// decodePending is set; ws.status keeps its post-fetch value.
	return m.loadingCommand(func() tea.Msg {
		rows := make([]decodedRow, 0, len(records))
		for _, raw := range records {
			if err := ctx.Err(); err != nil {
				return decodeResultMsg{Profile: ws.profile, Generation: generation, Identity: identity, Overlay: overlay, Err: err}
			}
			decoded, err := overlay.Decoder.DecodeDisplay(raw.Data)
			rows = append(rows, decodedRow{Number: raw.Number, Decoded: decoded, Err: err})
		}
		return decodeResultMsg{Profile: ws.profile, Generation: generation, Identity: identity, Overlay: overlay, Rows: rows}
	})
}

func (m *Model) handleDecodeResult(ws *workspace, msg decodeResultMsg) tea.Cmd {
	if msg.Generation != ws.decodeGeneration || !ws.decodePending || msg.Overlay != ws.overlay || msg.Identity != ws.recordIdentity() || ws.screen != ScreenRecords {
		return nil
	}
	ws.cancelDecode()
	if msg.Err != nil {
		if !errors.Is(msg.Err, context.Canceled) {
			ws.status = status{Level: statusError, Text: msg.Err.Error()}
		}
		return nil
	}
	byNumber := make(map[int64]decodedRow, len(msg.Rows))
	for _, row := range msg.Rows {
		byNumber[row.Number] = row
	}
	for i := range ws.records {
		result, ok := byNumber[ws.records[i].Record.Number]
		if !ok {
			continue
		}
		ws.records[i].Err = result.Err
		if result.Err != nil {
			ws.records[i].Decoded = nil
			continue
		}
		decoded := result.Decoded
		ws.records[i].Decoded = &decoded
	}

	if ws != &m.workspace {
		return nil
	}
	var nextDecode tea.Cmd
	if ws.browsePending == nil {
		nextDecode = m.startDecode(ws)
	}
	return tea.Batch(nextDecode, m.maybePrefetch(ws))
}

func (m *Model) cancelAll() {
	// Cancel requests for every workspace so profile switches and shutdown do
	// not leak in-flight goroutines tied to inactive profiles. The active
	// profile's live cancel funcs sit on the embedded m.workspace, which is
	// only copied into m.workspaces on a profile switch, so cancel it too.
	m.workspace.cancelAll()
	for _, ws := range m.workspaces {
		ws.cancelAll()
	}
	m.cancelEdit()
	if m.query != nil {
		m.query.stopSearch()
	}
}
