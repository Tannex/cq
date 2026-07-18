// Package cqt implements the read-only z/OSMF operator console used by the
// separate cqt executable.
package cqt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/record"
	"github.com/Tannex/cq/internal/zosmf"
)

const defaultRequestTimeout = 30 * time.Second

// Options are the approved cqt command-line settings.
type Options struct {
	Prefix      string
	Copybook    string
	CopybookDSN string
	Format      string
	Record      string
	Codepage    string
}

// Session is the credential-safe application session returned by an injected
// loader. Browser contains the bounded read-only operations only.
type Session struct {
	Browser  zosmf.Browser
	User     string
	Encoding string
}

// Dependencies provide test seams without weakening the production command's
// read-only boundaries.
type Dependencies struct {
	LoadSession   func(context.Context) (Session, error)
	LoadFile      func(context.Context, string) ([]byte, error)
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

type requestKind uint8

const (
	requestDataSets requestKind = iota
	requestMembers
	requestRecords
)

type requestMeta struct {
	Kind       requestKind
	Generation uint64
	Screen     Screen
	Identity   string
	Budget     int
	NamePlan   pagePlan[string]
	RecordPlan pagePlan[int64]
}

type sessionResultMsg struct {
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

type overlayResultMsg struct {
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
type Model struct {
	options Options
	deps    Dependencies
	keys    KeyMap
	help    help.Model
	spinner spinner.Model

	width   int
	height  int
	visible int
	budget  int
	screen  Screen

	sessionReady bool
	browser      zosmf.Browser
	user         string
	codepageName string
	charmap      *decode.Charmap

	prefix        string
	memberPattern string
	prefixInput   textinput.Model
	memberInput   textinput.Model
	dialog        *copybookDialog

	datasets     []zosmf.DataSet
	datasetPage  pager[string]
	datasetTotal *int

	dataSet     zosmf.DataSet
	members     []zosmf.Member
	memberPage  pager[string]
	memberTotal *int
	member      *zosmf.Member

	records    []recordRow
	recordPage pager[int64]

	overlay         *overlay
	overlaySource   CopybookSource
	overlayError    string
	recordMode      RecordMode
	decodedMode     RecordMode
	horizontal      int
	jsonVertical    int
	showDiagnostics bool
	showHelp        bool
	helpVertical    int

	status status

	sessionGeneration uint64
	sessionCancel     context.CancelFunc
	browseGeneration  uint64
	browseCancel      context.CancelFunc
	browsePending     *requestMeta
	overlayGeneration uint64
	overlayCancel     context.CancelFunc
	overlayPending    bool
	decodeGeneration  uint64
	decodeCancel      context.CancelFunc
	decodePending     bool
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
	if _, _, err := (CopybookSource{Local: firstNonEmpty(options.Copybook, ""), DSN: options.CopybookDSN, Format: options.Format, Record: options.Record}).validate(); err != nil {
		if strings.TrimSpace(options.Copybook) != "" || strings.TrimSpace(options.CopybookDSN) != "" {
			return nil, err
		}
		// With no source, only the format value needs validation.
		switch strings.ToLower(strings.TrimSpace(options.Format)) {
		case "auto", "fixed", "free":
		default:
			return nil, fmt.Errorf("copybook format %q is invalid; use auto, fixed, or free", options.Format)
		}
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

	m := &Model{
		options:     options,
		deps:        deps,
		keys:        DefaultKeyMap(),
		help:        help.New(),
		spinner:     newStatusSpinner(),
		screen:      ScreenDataSets,
		recordMode:  ModeRaw,
		decodedMode: ModeTable,
		prefixInput: prefixInput,
		memberInput: memberInput,
		status:      status{Level: statusLoading, Text: "loading Zowe session"},
	}
	return m, nil
}

func firstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
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

// Init loads the Zowe session in a typed command. Window-size handling remains
// independent, so a session can resolve in a tiny terminal without dispatching
// a row request.
func (m *Model) Init() tea.Cmd {
	m.cancelSession()
	m.sessionGeneration++
	generation := m.sessionGeneration
	ctx, cancel := context.WithTimeout(context.Background(), m.deps.Timeout)
	m.sessionCancel = cancel
	loader := m.deps.LoadSession
	return m.loadingCommand(func() tea.Msg {
		if loader == nil {
			return sessionResultMsg{Generation: generation, Err: errors.New("Zowe session loader is unavailable")}
		}
		session, err := loader(ctx)
		return sessionResultMsg{Generation: generation, Session: session, Err: err}
	})
}

// Update implements tea.Model.
func (m *Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		return m, m.handleResize(msg.Width, msg.Height)
	case sessionResultMsg:
		return m, m.handleSessionResult(msg)
	case dataSetsResultMsg:
		return m, m.handleDataSetsResult(msg)
	case membersResultMsg:
		return m, m.handleMembersResult(msg)
	case recordsResultMsg:
		return m, m.handleRecordsResult(msg)
	case overlayResultMsg:
		return m, m.handleOverlayResult(msg)
	case decodeResultMsg:
		return m, m.handleDecodeResult(msg)
	case spinner.TickMsg:
		level, _ := m.effectiveStatus()
		if level != statusLoading {
			return m, nil
		}
		updated, command := m.spinner.Update(msg)
		m.spinner = updated
		return m, command
	case tea.KeyPressMsg:
		return m, m.handleKey(msg)
	default:
		return m, nil
	}
}

func (m *Model) handleSessionResult(msg sessionResultMsg) tea.Cmd {
	if msg.Generation != m.sessionGeneration {
		return nil
	}
	m.cancelSession()
	if msg.Err != nil {
		m.status = status{Level: statusError, Text: msg.Err.Error()}
		return nil
	}
	if msg.Session.Browser == nil {
		m.status = status{Level: statusError, Text: "Zowe session did not provide a browser client"}
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
		m.status = status{Level: statusError, Text: err.Error()}
		return nil
	}
	m.sessionReady = true
	m.browser = msg.Session.Browser
	m.user = strings.ToUpper(strings.TrimSpace(msg.Session.User))
	m.codepageName = cm.Name()
	m.charmap = cm

	m.prefix = strings.ToUpper(strings.TrimSpace(m.options.Prefix))
	if m.prefix == "" && m.user != "" {
		m.prefix = m.user + ".*"
	}
	m.prefixInput.SetValue(m.prefix)
	m.status = status{Level: statusReady, Text: "session ready"}

	var commands []tea.Cmd
	if source := m.initialCopybookSource(); !source.empty() {
		commands = append(commands, m.startOverlay(source))
	}
	if m.prefix == "" {
		m.status = status{Level: statusReady, Text: "enter a data set prefix"}
		commands = append(commands, m.prefixInput.Focus())
	} else if m.budget > 0 {
		m.datasetPage.reset("", m.visible, m.budget)
		commands = append(commands, m.startDataSets(m.datasetPage.initialPlan("")))
	}
	return tea.Batch(commands...)
}

func (m *Model) initialCopybookSource() CopybookSource {
	return CopybookSource{
		Local: m.options.Copybook, DSN: m.options.CopybookDSN, Format: m.options.Format, Record: m.options.Record,
	}
}

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	ctx := keyContext{
		Screen: m.screen, Mode: m.recordMode,
		InputFocused: m.inputFocused(), DialogOpen: m.dialog != nil,
		ShowHelp: m.showHelp,
	}
	selectedAction := m.keys.actionFor(msg, ctx)
	if m.dialog != nil {
		switch selectedAction {
		case actionAccept:
			source := m.dialog.source()
			if _, _, err := source.validate(); err != nil {
				m.dialog.err = err.Error()
				return nil
			}
			m.dialog = nil
			return m.startOverlay(source)
		case actionCancel:
			m.dialog = nil
			return nil
		case actionNextField:
			return m.dialog.moveFocus(1)
		case actionPreviousField:
			return m.dialog.moveFocus(-1)
		default:
			return m.dialog.update(msg)
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
			if m.prefixInput.Focused() {
				updated, cmd := m.prefixInput.Update(msg)
				m.prefixInput = updated
				return cmd
			}
			updated, cmd := m.memberInput.Update(msg)
			m.memberInput = updated
			return cmd
		}
	}
	return m.handleAction(selectedAction)
}

func (m *Model) handleAction(selected action) tea.Cmd {
	switch selected {
	case actionNone:
		return nil
	case actionQuit:
		m.cancelAll()
		return tea.Quit
	case actionHelp:
		m.showHelp = !m.showHelp
		m.helpVertical = 0
		return nil
	case actionSearch:
		if m.screen == ScreenDataSets {
			m.prefixInput.SetValue(m.prefix)
			m.prefixInput.CursorEnd()
			return m.prefixInput.Focus()
		}
		if m.screen == ScreenMembers {
			m.memberInput.SetValue(m.memberPattern)
			m.memberInput.CursorEnd()
			return m.memberInput.Focus()
		}
	case actionUp:
		return m.moveSelection(-1)
	case actionDown:
		return m.moveSelection(1)
	case actionPageUp:
		if m.screen == ScreenRecords && m.recordMode == ModeJSON {
			m.jsonVertical = max(0, m.jsonVertical-max(1, m.visible))
			return nil
		}
		return m.moveSelection(-max(1, m.visible))
	case actionPageDown:
		if m.screen == ScreenRecords && m.recordMode == ModeJSON {
			m.jsonVertical = min(m.maxJSONVertical(), m.jsonVertical+max(1, m.visible))
			return nil
		}
		return m.moveSelection(max(1, m.visible))
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
	case actionCopybook:
		m.dialog = newCopybookDialog(m.overlaySource)
		m.dialog.setWidth(m.width)
		return nil
	case actionClearOverlay:
		m.cancelOverlay()
		m.overlayGeneration++
		m.cancelDecode()
		m.overlay = nil
		m.overlaySource = CopybookSource{}
		m.overlayError = ""
		m.recordMode = ModeRaw
		m.horizontal = 0
		m.jsonVertical = 0
		for i := range m.records {
			m.records[i].Decoded = nil
			m.records[i].Err = nil
		}
		m.status = status{Level: statusReady, Text: "copybook overlay cleared"}
		return nil
	case actionToggleOverlay:
		if m.overlay == nil {
			m.status = status{Level: statusWarn, Text: "load a copybook with c before enabling an overlay"}
			return nil
		}
		if m.recordMode == ModeRaw {
			m.recordMode = m.decodedMode
		} else {
			m.decodedMode = m.recordMode
			m.recordMode = ModeRaw
		}
		m.horizontal = 0
		m.jsonVertical = 0
		return nil
	case actionToggleView:
		if m.overlay == nil {
			m.status = status{Level: statusWarn, Text: "load a copybook with c before selecting table or JSON"}
			return nil
		}
		if m.decodedMode == ModeTable {
			m.decodedMode = ModeJSON
		} else {
			m.decodedMode = ModeTable
		}
		if m.recordMode != ModeRaw {
			m.recordMode = m.decodedMode
		}
		m.horizontal = 0
		m.jsonVertical = 0
		return nil
	case actionDiagnostics:
		m.showDiagnostics = !m.showDiagnostics
		return nil
	case actionWideLeft:
		if m.horizontal > 0 {
			m.horizontal--
		}
		return nil
	case actionWideRight:
		if m.horizontal < m.maxHorizontal() {
			m.horizontal++
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
		m.helpVertical = 1<<31 - 1
		return nil
	}
	return nil
}

func (m *Model) inputFocused() bool {
	return m.prefixInput.Focused() || m.memberInput.Focused()
}

func (m *Model) acceptSearch() tea.Cmd {
	if m.prefixInput.Focused() {
		prefix := strings.ToUpper(strings.TrimSpace(m.prefixInput.Value()))
		if prefix == "" {
			m.status = status{Level: statusError, Text: "data set prefix must not be empty"}
			return nil
		}
		m.prefixInput.Blur()
		m.cancelBrowse()
		m.cancelDecode()
		m.prefix = prefix
		m.datasetPage.reset("", m.visible, m.budget)
		m.datasets = nil
		m.datasetTotal = nil
		m.dataSet = zosmf.DataSet{}
		m.members = nil
		m.memberTotal = nil
		m.memberPage.reset("", m.visible, m.budget)
		m.member = nil
		m.records = nil
		m.recordPage.reset(0, m.visible, m.budget)
		if m.budget <= 0 {
			return nil
		}
		return m.startDataSets(m.datasetPage.initialPlan(""))
	}
	pattern := strings.ToUpper(strings.TrimSpace(m.memberInput.Value()))
	if pattern != "" && len(pattern) < 8 && !strings.ContainsAny(pattern, "*%") {
		pattern += "*"
	}
	m.memberInput.Blur()
	m.cancelBrowse()
	m.cancelDecode()
	m.memberPattern = pattern
	m.memberPage.reset("", m.visible, m.budget)
	m.members = nil
	m.memberTotal = nil
	m.member = nil
	m.records = nil
	m.recordPage.reset(0, m.visible, m.budget)
	if m.budget <= 0 {
		return nil
	}
	return m.startMembers(m.memberPage.initialPlan(""))
}

func (m *Model) cancelSearch() {
	if m.prefixInput.Focused() {
		m.prefixInput.SetValue(m.prefix)
		m.prefixInput.Blur()
	}
	if m.memberInput.Focused() {
		m.memberInput.SetValue(m.memberPattern)
		m.memberInput.Blur()
	}
}

func (m *Model) moveSelection(delta int) tea.Cmd {
	switch m.screen {
	case ScreenDataSets:
		m.datasetPage.move(delta)
	case ScreenMembers:
		m.memberPage.move(delta)
	case ScreenRecords:
		selected := m.recordPage.selectedKey()
		m.recordPage.move(delta)
		if m.recordPage.selectedKey() != selected {
			m.jsonVertical = 0
		}
	}
	return m.maybePrefetch()
}

func (m *Model) activePagerTop() {
	switch m.screen {
	case ScreenDataSets:
		m.datasetPage.top()
	case ScreenMembers:
		m.memberPage.top()
	case ScreenRecords:
		selected := m.recordPage.selectedKey()
		m.recordPage.top()
		if m.recordPage.selectedKey() != selected {
			m.jsonVertical = 0
		}
	}
}

func (m *Model) activePagerBottom() tea.Cmd {
	switch m.screen {
	case ScreenDataSets:
		m.datasetPage.bottom()
	case ScreenMembers:
		m.memberPage.bottom()
	case ScreenRecords:
		selected := m.recordPage.selectedKey()
		m.recordPage.bottom()
		if m.recordPage.selectedKey() != selected {
			m.jsonVertical = 0
		}
	}
	return m.maybePrefetch()
}

func (m *Model) openSelection() tea.Cmd {
	if m.budget <= 0 {
		return nil
	}
	switch m.screen {
	case ScreenDataSets:
		index := m.datasetPage.selectedIndex()
		if index < 0 || index >= len(m.datasets) {
			return nil
		}
		selected := m.datasets[index]
		organization := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(selected.Organization), " ", ""))
		switch organization {
		case "PS", "SEQ", "PS-L", "PSL":
			m.cancelBrowse()
			m.cancelDecode()
			m.dataSet = selected
			m.member = nil
			m.members = nil
			m.memberTotal = nil
			m.memberPage.reset("", m.visible, m.budget)
			m.screen = ScreenRecords
			m.records = nil
			m.recordPage.reset(0, m.visible, m.budget)
			m.horizontal = 0
			m.jsonVertical = 0
			return m.startRecords(m.recordPage.initialPlan(0))
		case "PO", "PO-E", "POE", "PDS", "PDSE":
			m.cancelBrowse()
			m.cancelDecode()
			m.dataSet = selected
			m.member = nil
			m.records = nil
			m.recordPage.reset(0, m.visible, m.budget)
			m.screen = ScreenMembers
			m.members = nil
			m.memberPattern = ""
			m.memberInput.SetValue("")
			m.memberPage.reset("", m.visible, m.budget)
			return m.startMembers(m.memberPage.initialPlan(""))
		default:
			if organization == "" {
				organization = "unknown"
			}
			m.status = status{Level: statusWarn, Text: fmt.Sprintf("%s uses unsupported DSORG %s; sequential, large-format sequential, PDS, and PDSE data sets are readable", selected.Name, organization)}
		}
	case ScreenMembers:
		index := m.memberPage.selectedIndex()
		if index < 0 || index >= len(m.members) {
			return nil
		}
		selected := m.members[index]
		m.cancelBrowse()
		m.cancelDecode()
		m.member = &selected
		m.screen = ScreenRecords
		m.records = nil
		m.recordPage.reset(0, m.visible, m.budget)
		m.horizontal = 0
		m.jsonVertical = 0
		return m.startRecords(m.recordPage.initialPlan(0))
	}
	return nil
}

func (m *Model) navigateBack() tea.Cmd {
	m.cancelSearch()
	switch m.screen {
	case ScreenRecords:
		m.cancelBrowse()
		m.cancelDecode()
		fromMember := m.member != nil
		m.records = nil
		m.recordPage.reset(0, m.visible, m.budget)
		m.horizontal = 0
		m.jsonVertical = 0
		if fromMember {
			m.member = nil
			m.screen = ScreenMembers
			m.status = status{Level: statusReady, Text: "returned to cached member list"}
			return m.ensureActivePage()
		}
		m.screen = ScreenDataSets
		m.status = status{Level: statusReady, Text: "returned to cached data set list"}
		return m.ensureActivePage()
	case ScreenMembers:
		m.cancelBrowse()
		m.cancelDecode()
		m.members = nil
		m.memberTotal = nil
		m.memberPage.reset("", m.visible, m.budget)
		m.records = nil
		m.recordPage.reset(0, m.visible, m.budget)
		m.screen = ScreenDataSets
		m.dataSet = zosmf.DataSet{}
		m.member = nil
		m.status = status{Level: statusReady, Text: "returned to cached data set list"}
		return m.ensureActivePage()
	}
	return nil
}

func (m *Model) refresh() tea.Cmd {
	if !m.sessionReady || m.budget <= 0 {
		return nil
	}
	m.cancelBrowse()
	switch m.screen {
	case ScreenDataSets:
		preserve := m.datasetPage.selectedKey()
		m.datasets = nil
		m.datasetTotal = nil
		m.datasetPage.reset("", m.visible, m.budget)
		return m.startDataSets(pagePlan[string]{Preserve: preserve, Direction: pageRefresh})
	case ScreenMembers:
		preserve := m.memberPage.selectedKey()
		m.members = nil
		m.memberTotal = nil
		m.memberPage.reset("", m.visible, m.budget)
		return m.startMembers(pagePlan[string]{Preserve: preserve, Direction: pageRefresh})
	case ScreenRecords:
		preserve := m.recordPage.selectedKey()
		m.cancelDecode()
		m.records = nil
		m.recordPage.reset(0, m.visible, m.budget)
		return m.startRecords(pagePlan[int64]{Preserve: preserve, Direction: pageRefresh})
	}
	return nil
}

func (m *Model) ensureActivePage() tea.Cmd {
	if !m.sessionReady || m.budget <= 0 {
		return nil
	}
	switch m.screen {
	case ScreenDataSets:
		if len(m.datasets) == 0 && m.prefix != "" {
			return m.startDataSets(m.datasetPage.initialPlan(""))
		}
	case ScreenMembers:
		if len(m.members) == 0 && m.dataSet.Name != "" {
			return m.startMembers(m.memberPage.initialPlan(""))
		}
	case ScreenRecords:
		if len(m.records) == 0 && m.dataSet.Name != "" {
			return m.startRecords(m.recordPage.initialPlan(0))
		}
	}
	return m.maybePrefetch()
}

func (m *Model) handleResize(width, height int) tea.Cmd {
	oldBudget := m.budget
	m.width, m.height = width, height
	m.visible = VisibleRows(width, height)
	m.budget = RowBudget(m.visible)
	m.help.SetWidth(max(1, width))
	m.prefixInput.SetWidth(max(8, width-10))
	m.memberInput.SetWidth(max(8, min(24, width-10)))
	if m.dialog != nil {
		m.dialog.setWidth(width)
	}

	m.datasetPage.resize(m.visible, m.budget)
	m.memberPage.resize(m.visible, m.budget)
	m.recordPage.resize(m.visible, m.budget)
	m.jsonVertical = min(m.jsonVertical, m.maxJSONVertical())

	if m.budget <= 0 {
		m.cancelBrowse()
		return nil
	}
	if !m.sessionReady {
		return nil
	}
	if m.browsePending != nil && m.browsePending.Budget != m.budget {
		m.cancelBrowse()
	}
	if oldBudget != m.budget {
		m.statusForRetainedWindow()
	}
	if oldBudget == 0 {
		return m.ensureActivePage()
	}
	return m.maybePrefetch()
}

func (m *Model) statusForRetainedWindow() {
	count := m.activeRowCount()
	if count == 0 {
		m.status = status{Level: statusEmpty, Text: "window resized; no cached rows"}
		return
	}
	m.status = status{Level: statusReady, Text: fmt.Sprintf("window resized; %d cached rows retained", count)}
}

func (m *Model) maybePrefetch() tea.Cmd {
	if !m.canFetch() || m.browsePending != nil {
		return nil
	}
	switch m.screen {
	case ScreenDataSets:
		if plan, ok := forwardNamePlan(&m.datasetPage); ok {
			return m.startDataSets(plan)
		}
	case ScreenMembers:
		if plan, ok := forwardNamePlan(&m.memberPage); ok {
			return m.startMembers(plan)
		}
	case ScreenRecords:
		numbers := make([]int64, len(m.records))
		for i, row := range m.records {
			numbers[i] = row.Record.Number
		}
		if plan, ok := forwardRecordPlan(&m.recordPage, numbers); ok {
			return m.startRecords(plan)
		}
	}
	return nil
}

func (m *Model) startDataSets(plan pagePlan[string]) tea.Cmd {
	if !m.canFetch() || m.prefix == "" {
		return nil
	}
	m.cancelBrowse()
	m.browseGeneration++
	meta := requestMeta{
		Kind: requestDataSets, Generation: m.browseGeneration, Screen: ScreenDataSets,
		Identity: m.prefix, Budget: m.budget, NamePlan: plan,
	}
	m.browsePending = &meta
	ctx, cancel := context.WithTimeout(context.Background(), m.deps.Timeout)
	m.browseCancel = cancel
	m.status = status{Level: statusLoading, Text: fmt.Sprintf("listing data sets for %s", m.prefix)}
	browser := m.browser
	request := zosmf.ListDataSetsRequest{Prefix: m.prefix, Start: plan.Anchor, MaxItems: m.budget}
	return m.loadingCommand(func() tea.Msg {
		page, err := browser.ListDataSets(ctx, request)
		return dataSetsResultMsg{Meta: meta, Page: page, Err: err}
	})
}

func (m *Model) startMembers(plan pagePlan[string]) tea.Cmd {
	if !m.canFetch() || m.dataSet.Name == "" {
		return nil
	}
	m.cancelBrowse()
	m.browseGeneration++
	identity := m.dataSet.Name + "|" + m.memberPattern
	meta := requestMeta{
		Kind: requestMembers, Generation: m.browseGeneration, Screen: ScreenMembers,
		Identity: identity, Budget: m.budget, NamePlan: plan,
	}
	m.browsePending = &meta
	ctx, cancel := context.WithTimeout(context.Background(), m.deps.Timeout)
	m.browseCancel = cancel
	m.status = status{Level: statusLoading, Text: fmt.Sprintf("listing members of %s", m.dataSet.Name)}
	browser := m.browser
	request := zosmf.ListMembersRequest{DataSet: m.dataSet.Name, Start: plan.Anchor, Pattern: m.memberPattern, MaxItems: m.budget}
	return m.loadingCommand(func() tea.Msg {
		page, err := browser.ListMembers(ctx, request)
		return membersResultMsg{Meta: meta, Page: page, Err: err}
	})
}

func (m *Model) startRecords(plan pagePlan[int64]) tea.Cmd {
	if !m.canFetch() || m.dataSet.Name == "" {
		return nil
	}
	m.cancelBrowse()
	m.browseGeneration++
	member := ""
	if m.member != nil {
		member = m.member.Name
	}
	identity := m.dataSet.Name + "(" + member + ")"
	meta := requestMeta{
		Kind: requestRecords, Generation: m.browseGeneration, Screen: ScreenRecords,
		Identity: identity, Budget: m.budget, RecordPlan: plan,
	}
	m.browsePending = &meta
	ctx, cancel := context.WithTimeout(context.Background(), m.deps.Timeout)
	m.browseCancel = cancel
	m.status = status{Level: statusLoading, Text: fmt.Sprintf("reading records from %s", identity)}
	browser := m.browser
	request := zosmf.ReadRecordsRequest{DataSet: m.dataSet.Name, Member: member, Start: plan.Anchor, MaxItems: m.budget}
	return m.loadingCommand(func() tea.Msg {
		page, err := browser.ReadRecords(ctx, request)
		return recordsResultMsg{Meta: meta, Page: page, Err: err}
	})
}

func (m *Model) canFetch() bool {
	return m.sessionReady && m.browser != nil && m.budget > 0
}

func (m *Model) handleDataSetsResult(msg dataSetsResultMsg) tea.Cmd {
	if !m.acceptBrowse(msg.Meta) {
		return nil
	}
	m.finishBrowse()
	if msg.Err != nil {
		m.status = status{Level: statusError, Text: msg.Err.Error()}
		return nil
	}
	if msg.Meta.NamePlan.Direction == pageForward && len(msg.Page.Items) == 0 {
		m.datasetPage.more = false
		m.status = status{Level: statusReady, Text: "end of data set results"}
		return nil
	}
	items, overReturned := boundedWindow(msg.Page.Items, msg.Meta.Budget)
	keys := make([]string, len(items))
	for i, item := range items {
		keys[i] = strings.ToUpper(strings.TrimSpace(item.Name))
	}
	more := msg.Page.MoreRows || overReturned
	before := len(m.datasets)
	if msg.Meta.NamePlan.Direction == pageForward {
		m.datasets = mergeCached(m.datasets, items, func(item zosmf.DataSet) string { return strings.ToUpper(strings.TrimSpace(item.Name)) })
		if len(m.datasets) == before {
			more = false
		}
	} else {
		m.datasets = append([]zosmf.DataSet(nil), items...)
	}
	m.datasetPage.apply(keys, more, msg.Meta.NamePlan)
	if msg.Page.TotalRows != nil || msg.Meta.NamePlan.Direction != pageForward {
		m.datasetTotal = cloneInt(msg.Page.TotalRows)
	}
	m.statusForCount(len(m.datasets), "cached data sets")
	return m.maybePrefetch()
}

func (m *Model) handleMembersResult(msg membersResultMsg) tea.Cmd {
	if !m.acceptBrowse(msg.Meta) {
		return nil
	}
	m.finishBrowse()
	if msg.Err != nil {
		m.status = status{Level: statusError, Text: msg.Err.Error()}
		return nil
	}
	if msg.Meta.NamePlan.Direction == pageForward && len(msg.Page.Items) == 0 {
		m.memberPage.more = false
		m.status = status{Level: statusReady, Text: "end of member results"}
		return nil
	}
	items, overReturned := boundedWindow(msg.Page.Items, msg.Meta.Budget)
	keys := make([]string, len(items))
	for i, item := range items {
		keys[i] = strings.ToUpper(strings.TrimSpace(item.Name))
	}
	more := msg.Page.MoreRows || overReturned
	before := len(m.members)
	if msg.Meta.NamePlan.Direction == pageForward {
		m.members = mergeCached(m.members, items, func(item zosmf.Member) string { return strings.ToUpper(strings.TrimSpace(item.Name)) })
		if len(m.members) == before {
			more = false
		}
	} else {
		m.members = append([]zosmf.Member(nil), items...)
	}
	m.memberPage.apply(keys, more, msg.Meta.NamePlan)
	if msg.Page.TotalRows != nil || msg.Meta.NamePlan.Direction != pageForward {
		m.memberTotal = cloneInt(msg.Page.TotalRows)
	}
	m.statusForCount(len(m.members), "cached members")
	return m.maybePrefetch()
}

func (m *Model) handleRecordsResult(msg recordsResultMsg) tea.Cmd {
	if !m.acceptBrowse(msg.Meta) {
		return nil
	}
	m.finishBrowse()
	if msg.Err != nil {
		m.status = status{Level: statusError, Text: msg.Err.Error()}
		return nil
	}
	if msg.Meta.RecordPlan.Direction == pageForward && len(msg.Page.Records) == 0 {
		m.recordPage.more = false
		m.status = status{Level: statusReady, Text: "end of records"}
		return nil
	}
	selected := m.recordPage.selectedKey()
	items, overReturned := boundedWindow(msg.Page.Records, msg.Meta.Budget)
	keys := make([]string, len(items))
	incoming := make([]recordRow, len(items))
	for i, item := range items {
		keys[i] = strconv.FormatInt(item.Number, 10)
		incoming[i] = recordRow{Record: zosmf.Record{Number: item.Number, Data: append([]byte(nil), item.Data...)}}
	}
	more := msg.Page.MoreRows || overReturned
	before := len(m.records)
	if msg.Meta.RecordPlan.Direction == pageForward {
		m.records = mergeCached(m.records, incoming, func(row recordRow) string { return strconv.FormatInt(row.Record.Number, 10) })
		if len(m.records) == before {
			more = false
		}
	} else {
		m.records = incoming
	}
	m.recordPage.apply(keys, more, msg.Meta.RecordPlan)
	if m.recordPage.selectedKey() != selected {
		m.jsonVertical = 0
	}
	if len(m.records) == 0 {
		m.status = status{Level: statusEmpty, Text: "no records returned"}
		return nil
	}
	m.status = status{Level: statusReady, Text: fmt.Sprintf("%d records cached", len(m.records))}
	if m.overlay != nil {
		return tea.Batch(m.startDecode(), m.maybePrefetch())
	}
	return m.maybePrefetch()
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
	copy := *value
	return &copy
}

func (m *Model) statusForCount(count int, noun string) {
	if count == 0 {
		m.status = status{Level: statusEmpty, Text: "no " + noun + " returned"}
		return
	}
	m.status = status{Level: statusReady, Text: fmt.Sprintf("%d %s", count, noun)}
}

func (m *Model) acceptBrowse(meta requestMeta) bool {
	if m.browsePending == nil {
		return false
	}
	pending := m.browsePending
	if pending.Generation != meta.Generation || pending.Kind != meta.Kind || pending.Screen != meta.Screen || pending.Identity != meta.Identity || pending.Budget != meta.Budget {
		return false
	}
	if meta.Budget != m.budget || meta.Screen != m.screen {
		return false
	}
	switch meta.Screen {
	case ScreenDataSets:
		return meta.Identity == m.prefix
	case ScreenMembers:
		return meta.Identity == m.dataSet.Name+"|"+m.memberPattern
	case ScreenRecords:
		member := ""
		if m.member != nil {
			member = m.member.Name
		}
		return meta.Identity == m.dataSet.Name+"("+member+")"
	default:
		return false
	}
}

func (m *Model) finishBrowse() {
	if m.browseCancel != nil {
		m.browseCancel()
	}
	m.browseCancel = nil
	m.browsePending = nil
}

func (m *Model) startOverlay(source CopybookSource) tea.Cmd {
	m.cancelOverlay()
	m.overlayError = ""
	m.overlayGeneration++
	generation := m.overlayGeneration
	m.overlayPending = true
	ctx, cancel := context.WithTimeout(context.Background(), m.deps.Timeout)
	m.overlayCancel = cancel
	browser := m.browser
	codepage := m.codepageName
	loadFile := m.deps.LoadFile
	searchPaths := append([]string(nil), m.deps.DSNSearchPath...)
	m.status = status{Level: statusLoading, Text: "loading copybook overlay " + source.label()}
	return m.loadingCommand(func() tea.Msg {
		built, err := buildOverlay(ctx, source, codepage, browser, loadFile, searchPaths)
		return overlayResultMsg{Generation: generation, Source: source, Overlay: built, Err: err}
	})
}

func (m *Model) handleOverlayResult(msg overlayResultMsg) tea.Cmd {
	if msg.Generation != m.overlayGeneration || !m.overlayPending {
		return nil
	}
	m.cancelOverlay()
	if msg.Err != nil {
		m.overlayError = msg.Err.Error()
		if m.overlay != nil {
			m.overlayError += "; previous overlay retained"
		}
		if m.browsePending == nil {
			m.status = status{Level: statusError, Text: m.overlayError}
		}
		return nil
	}
	m.overlayError = ""
	m.cancelDecode()
	m.overlay = msg.Overlay
	m.overlaySource = msg.Overlay.Source
	for i := range m.records {
		m.records[i].Decoded = nil
		m.records[i].Err = nil
	}
	m.recordMode = ModeTable
	m.decodedMode = ModeTable
	m.horizontal = 0
	m.jsonVertical = 0
	if m.browsePending != nil {
		return nil
	}
	m.status = status{Level: statusReady, Text: fmt.Sprintf("overlay %s record %s", msg.Overlay.Source.label(), msg.Overlay.Record.Name)}
	if len(m.records) > 0 && m.screen == ScreenRecords {
		return m.startDecode()
	}
	return nil
}

func (m *Model) startDecode() tea.Cmd {
	if m.overlay == nil || len(m.records) == 0 || m.decodePending {
		return nil
	}
	records := make([]zosmf.Record, 0, len(m.records))
	for _, row := range m.records {
		if row.Decoded != nil || row.Err != nil {
			continue
		}
		records = append(records, zosmf.Record{Number: row.Record.Number, Data: append([]byte(nil), row.Record.Data...)})
	}
	if len(records) == 0 {
		return nil
	}

	m.decodeGeneration++
	generation := m.decodeGeneration
	m.decodePending = true
	ctx, cancel := context.WithCancel(context.Background())
	m.decodeCancel = cancel
	overlay := m.overlay
	identity := m.recordIdentity()
	m.status = status{Level: statusLoading, Text: fmt.Sprintf("decoding %d cached records with %s", len(records), overlay.Record.Name)}
	return m.loadingCommand(func() tea.Msg {
		rows := make([]decodedRow, 0, len(records))
		for _, raw := range records {
			if err := ctx.Err(); err != nil {
				return decodeResultMsg{Generation: generation, Identity: identity, Overlay: overlay, Err: err}
			}
			decoded, err := overlay.Decoder.DecodeDisplay(raw.Data)
			rows = append(rows, decodedRow{Number: raw.Number, Decoded: decoded, Err: err})
		}
		return decodeResultMsg{Generation: generation, Identity: identity, Overlay: overlay, Rows: rows}
	})
}

func (m *Model) handleDecodeResult(msg decodeResultMsg) tea.Cmd {
	if msg.Generation != m.decodeGeneration || !m.decodePending || msg.Overlay != m.overlay || msg.Identity != m.recordIdentity() || m.screen != ScreenRecords {
		return nil
	}
	m.cancelDecode()
	if msg.Err != nil {
		if !errors.Is(msg.Err, context.Canceled) {
			m.status = status{Level: statusError, Text: msg.Err.Error()}
		}
		return nil
	}
	byNumber := make(map[int64]decodedRow, len(msg.Rows))
	for _, row := range msg.Rows {
		byNumber[row.Number] = row
	}
	for i := range m.records {
		result, ok := byNumber[m.records[i].Record.Number]
		if !ok {
			continue
		}
		m.records[i].Err = result.Err
		if result.Err != nil {
			m.records[i].Decoded = nil
			continue
		}
		decoded := result.Decoded
		m.records[i].Decoded = &decoded
	}

	var nextDecode tea.Cmd
	if m.browsePending == nil {
		nextDecode = m.startDecode()
	}
	if nextDecode == nil && m.browsePending == nil {
		diagnostics := 0
		structural := 0
		for _, row := range m.records {
			if row.Err != nil {
				structural++
				continue
			}
			if row.Decoded != nil {
				diagnostics += len(row.Decoded.Diagnostics)
			}
		}
		if diagnostics > 0 || structural > 0 {
			m.status = status{Level: statusWarn, Text: fmt.Sprintf("decoded %d cached records; %d field diagnostics, %d row errors", len(m.records), diagnostics, structural)}
		} else {
			m.status = status{Level: statusReady, Text: fmt.Sprintf("decoded %d cached records with %s", len(m.records), m.overlay.Record.Name)}
		}
	}
	return tea.Batch(nextDecode, m.maybePrefetch())
}

func (m *Model) recordIdentity() string {
	member := ""
	if m.member != nil {
		member = m.member.Name
	}
	return fmt.Sprintf("%s(%s)", m.dataSet.Name, member)
}

func (m *Model) cancelSession() {
	if m.sessionCancel != nil {
		m.sessionCancel()
	}
	m.sessionCancel = nil
}

func (m *Model) cancelBrowse() {
	if m.browseCancel != nil {
		m.browseCancel()
	}
	m.browseCancel = nil
	m.browsePending = nil
	m.browseGeneration++
}

func (m *Model) cancelOverlay() {
	if m.overlayCancel != nil {
		m.overlayCancel()
	}
	m.overlayCancel = nil
	m.overlayPending = false
}

func (m *Model) cancelDecode() {
	if m.decodeCancel != nil {
		m.decodeCancel()
	}
	m.decodeCancel = nil
	m.decodePending = false
	m.decodeGeneration++
}

func (m *Model) cancelAll() {
	m.cancelSession()
	m.cancelBrowse()
	m.cancelOverlay()
	m.cancelDecode()
}
