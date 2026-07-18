package cqt

import (
	"context"
	"fmt"
	"strconv"

	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/zosmf"
)

// workspace holds all per-profile state. The active workspace is embedded in
// Model so existing code can keep accessing fields like model.datasets directly.
type workspace struct {
	profile string

	sessionReady bool
	browser      zosmf.Browser
	user         string
	codepageName string
	charmap      *decode.Charmap

	prefix        string
	memberPattern string

	screen Screen

	datasets     []zosmf.DataSet
	datasetPage  pager[string]
	datasetTotal *int
	dataSet      zosmf.DataSet
	members      []zosmf.Member
	memberPage   pager[string]
	memberTotal  *int
	member       *zosmf.Member

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

func newWorkspace(profile string) workspace {
	return workspace{
		profile:     profile,
		screen:      ScreenDataSets,
		recordMode:  ModeRaw,
		decodedMode: ModeTable,
		status:      status{Level: statusLoading, Text: "loading Zowe session"},
	}
}

func (ws *workspace) sessionStatusText() string {
	if ws.profile == "" {
		return "loading Zowe session"
	}
	return fmt.Sprintf("loading Zowe session %s", ws.profile)
}

func (ws *workspace) acceptBrowse(meta requestMeta, budget int, screen Screen) bool {
	if ws.browsePending == nil {
		return false
	}
	pending := ws.browsePending
	if pending.Generation != meta.Generation || pending.Kind != meta.Kind || pending.Screen != meta.Screen || pending.Identity != meta.Identity || pending.Budget != meta.Budget {
		return false
	}
	if meta.Budget != budget || meta.Screen != screen {
		return false
	}
	switch meta.Screen {
	case ScreenDataSets:
		return meta.Identity == ws.prefix
	case ScreenMembers:
		return meta.Identity == ws.dataSet.Name+"|"+ws.memberPattern
	case ScreenRecords:
		member := ""
		if ws.member != nil {
			member = ws.member.Name
		}
		return meta.Identity == ws.dataSet.Name+"("+member+")"
	default:
		return false
	}
}

func (ws *workspace) finishBrowse() {
	if ws.browseCancel != nil {
		ws.browseCancel()
	}
	ws.browseCancel = nil
	ws.browsePending = nil
}

func (ws *workspace) canFetch(budget int) bool {
	return ws.sessionReady && ws.browser != nil && budget > 0
}

func (ws *workspace) recordIdentity() string {
	member := ""
	if ws.member != nil {
		member = ws.member.Name
	}
	return fmt.Sprintf("%s(%s)", ws.dataSet.Name, member)
}

func (ws *workspace) resetRecordState() {
	ws.records = nil
	ws.recordPage.reset(ws.recordPage.visible, ws.recordPage.budget)
	ws.horizontal = 0
	ws.jsonVertical = 0
}

func (ws *workspace) resetMemberState() {
	ws.members = nil
	ws.memberTotal = nil
	ws.memberPage.reset(ws.memberPage.visible, ws.memberPage.budget)
	ws.member = nil
	ws.resetRecordState()
}

func (ws *workspace) cancelSession() {
	if ws.sessionCancel != nil {
		ws.sessionCancel()
	}
	ws.sessionCancel = nil
}

func (ws *workspace) cancelBrowse() {
	if ws.browseCancel != nil {
		ws.browseCancel()
	}
	ws.browseCancel = nil
	ws.browsePending = nil
	ws.browseGeneration++
}

func (ws *workspace) cancelOverlay() {
	if ws.overlayCancel != nil {
		ws.overlayCancel()
	}
	ws.overlayCancel = nil
	ws.overlayPending = false
}

func (ws *workspace) cancelDecode() {
	if ws.decodeCancel != nil {
		ws.decodeCancel()
	}
	ws.decodeCancel = nil
	ws.decodePending = false
	ws.decodeGeneration++
}

func (ws *workspace) cancelAll() {
	ws.cancelSession()
	ws.cancelBrowse()
	ws.cancelOverlay()
	ws.cancelDecode()
}

func (ws *workspace) activePager() pagerNavigator {
	switch ws.screen {
	case ScreenDataSets:
		return &ws.datasetPage
	case ScreenMembers:
		return &ws.memberPage
	case ScreenRecords:
		return &ws.recordPage
	default:
		return nil
	}
}

func (ws *workspace) statusForCount(count int, noun string) {
	if count == 0 {
		ws.status = status{Level: statusEmpty, Text: "no " + noun + " returned"}
		return
	}
	ws.status = status{Level: statusReady, Text: fmt.Sprintf("%d %s", count, noun)}
}

func (ws *workspace) recordNumberWidth() int {
	width := 8
	for _, row := range ws.records {
		if digits := len(strconv.FormatInt(row.Record.Number, 10)); digits > width {
			width = digits
		}
	}
	return width
}

func (ws *workspace) recordRange() (int64, int64) {
	if len(ws.records) == 0 {
		return 0, 0
	}
	return ws.records[0].Record.Number, ws.records[len(ws.records)-1].Record.Number
}
