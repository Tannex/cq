package compaz

import (
	"context"
	"fmt"
	"os"
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
	// autoOpen queues an exact data set name to open when the listing fetch
	// that armed it lands (the favorites "open" action). autoOpenGeneration
	// binds the queue to that fetch: a result from any other generation
	// clears the queue without firing, so superseding browses (new prefix,
	// refresh, back-navigation) cannot trigger a stale open.
	autoOpen           string
	autoOpenGeneration uint64
	dataSet            zosmf.DataSet
	members            []zosmf.Member
	memberPage         pager[string]
	memberTotal        *int
	member             *zosmf.Member

	// recalls tracks data sets with an HRECALL in flight, keyed by upper-case
	// name, so the list can mark them and catalog polling knows when to stop.
	// The value holds the most recent catalog-check error, surfaced if the
	// watch gives up.
	recalls map[string]string

	// jobOwner defaults once from the session user at session load and is
	// not user-editable in v1 (mirrors the "<Zowe user>.*" data set default
	// precedent). jobPrefix defaults to "*" and is edited via a dedicated
	// search-line input, the same way ws.prefix is.
	jobOwner  string
	jobPrefix string
	jobs      []zosmf.Job
	jobPage   pager[string]
	job       zosmf.Job

	// spoolFile may point at the synthetic JCL entry (ID < 0) that compaz
	// prepends client-side, since z/OSMF's spool file list never includes
	// the submitted JCL itself.
	spoolFiles    []zosmf.SpoolFile
	spoolFilePage pager[string]
	spoolFile     *zosmf.SpoolFile

	// spoolContent is keyed by line number (like records are keyed by
	// record number), not by line text, since spool lines are not unique.
	spoolContent []spoolLine
	// spoolLongest mirrors rawLongest for the spool content viewer's
	// horizontal pan.
	spoolLongest     int
	spoolContentPage pager[int64]

	// bulkRecordsPath caches a whole-data-set record download (jq bulk
	// search) keyed by bulkRecordsIdentity, so closing and reopening the
	// query console does not re-download. Dropped when the identity changes,
	// on refresh, after a successful edit save, and at quit.
	bulkRecordsPath     string
	bulkRecordsIdentity string

	records []recordRow
	// rawLongest caches the widest rendered raw line across the cached
	// records, so horizontal panning does not rescan the whole cache.
	rawLongest int
	recordPage pager[int64]
	// syntaxKind caches the detected content type of the cached records;
	// syntaxSampled remembers the cache size at detection time so the verdict
	// refreshes once per loaded page, not per render frame.
	syntaxKind    sourceKind
	syntaxSampled int

	overlay       *overlay
	overlaySource CopybookSource
	overlayError  string
	// overlayMappedPattern names the persisted mapping pattern behind the
	// current overlay source, or "" when the source was chosen manually.
	overlayMappedPattern string
	// pendingMapping is a mapping save deferred until its overlay load
	// succeeds, so failed copybooks never enter the store.
	pendingMapping  *pendingMappingSave
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

func (ws *workspace) recallPending(name string) bool {
	_, ok := ws.recalls[name]
	return ok
}

func (ws *workspace) markRecall(name string) {
	if ws.recalls == nil {
		ws.recalls = make(map[string]string)
	}
	ws.recalls[name] = ""
}

func (ws *workspace) setRecallIssue(name, issue string) {
	if ws.recallPending(name) {
		ws.recalls[name] = issue
	}
}

func (ws *workspace) recallIssue(name string) string {
	return ws.recalls[name]
}

func (ws *workspace) clearRecall(name string) {
	delete(ws.recalls, name)
}

func (ws *workspace) hasRecalls() bool {
	return len(ws.recalls) > 0
}

// bulkRecords returns the cached whole-data-set download for the records
// currently browsed, lazily dropping a file that belongs to other records.
func (ws *workspace) bulkRecords() string {
	if ws.bulkRecordsPath != "" && ws.bulkRecordsIdentity != ws.recordIdentity() {
		ws.dropBulkRecords()
	}
	return ws.bulkRecordsPath
}

func (ws *workspace) setBulkRecords(path string) {
	ws.dropBulkRecords()
	ws.bulkRecordsPath = path
	ws.bulkRecordsIdentity = ws.recordIdentity()
}

func (ws *workspace) dropBulkRecords() {
	if ws.bulkRecordsPath != "" {
		_ = os.Remove(ws.bulkRecordsPath)
		ws.bulkRecordsPath = ""
	}
	ws.bulkRecordsIdentity = ""
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
	if pending.Generation != meta.Generation || pending.Screen != meta.Screen || pending.Identity != meta.Identity || pending.Budget != meta.Budget {
		return false
	}
	if meta.Budget != budget || meta.Screen != screen {
		return false
	}
	switch meta.Screen {
	case ScreenDataSets:
		return meta.Identity == ws.prefix
	case ScreenMembers:
		return meta.Identity == ws.memberIdentity()
	case ScreenRecords:
		return meta.Identity == ws.recordIdentity()
	case ScreenJobs:
		return meta.Identity == ws.jobIdentity()
	case ScreenSpoolFiles:
		return meta.Identity == ws.spoolFileListIdentity()
	case ScreenSpoolContent:
		return meta.Identity == ws.spoolContentIdentity()
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

func (ws *workspace) memberIdentity() string {
	return ws.dataSet.Name + "|" + ws.memberPattern
}

func (ws *workspace) recordIdentity() string {
	member := ""
	if ws.member != nil {
		member = ws.member.Name
	}
	return fmt.Sprintf("%s(%s)", ws.dataSet.Name, member)
}

func (ws *workspace) jobIdentity() string {
	return ws.jobOwner + "|" + ws.jobPrefix
}

func (ws *workspace) spoolFileListIdentity() string {
	return ws.job.JobName + "|" + ws.job.JobID
}

func (ws *workspace) spoolContentIdentity() string {
	file := ""
	if ws.spoolFile != nil {
		file = spoolFileKey(*ws.spoolFile)
	}
	return fmt.Sprintf("%s|%s|%s", ws.job.JobName, ws.job.JobID, file)
}

// spoolFileKey is the pager/identity key for a spool file: its numeric id,
// or "JCL" for the synthetic submitted-JCL entry compaz prepends client-side
// (ID < 0), which also doubles as the FileID z/OSMF expects for that pseudo
// spool file.
func spoolFileKey(file zosmf.SpoolFile) string {
	if file.ID < 0 {
		return "JCL"
	}
	return strconv.Itoa(file.ID)
}

// matchName is the name persisted DSN → copybook mappings are matched
// against: DSN(MEMBER) for members and the plain DSN otherwise.
func (ws *workspace) matchName() string {
	if ws.member != nil {
		return fmt.Sprintf("%s(%s)", ws.dataSet.Name, ws.member.Name)
	}
	return ws.dataSet.Name
}

func (ws *workspace) resetRecordState() {
	ws.records = nil
	ws.rawLongest = 0
	ws.syntaxKind = sourcePlain
	ws.syntaxSampled = 0
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

func (ws *workspace) resetSpoolContentState() {
	ws.spoolContent = nil
	ws.spoolLongest = 0
	ws.spoolContentPage.reset(ws.spoolContentPage.visible, ws.spoolContentPage.budget)
	ws.horizontal = 0
}

func (ws *workspace) resetSpoolFilesState() {
	ws.spoolFiles = nil
	ws.spoolFilePage.reset(ws.spoolFilePage.visible, ws.spoolFilePage.budget)
	ws.spoolFile = nil
	ws.resetSpoolContentState()
}

func (ws *workspace) resetJobsState() {
	ws.jobs = nil
	ws.jobPage.reset(ws.jobPage.visible, ws.jobPage.budget)
	ws.job = zosmf.Job{}
	ws.resetSpoolFilesState()
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

func (ws *workspace) resizePagers(visible, budget int) {
	ws.datasetPage.resize(visible, budget)
	ws.memberPage.resize(visible, budget)
	ws.recordPage.resize(visible, budget)
	ws.jobPage.resize(visible, budget)
	ws.spoolFilePage.resize(visible, budget)
	ws.spoolContentPage.resize(visible, budget)
}

func (ws *workspace) activePager() pagerNavigator {
	switch ws.screen {
	case ScreenDataSets:
		return &ws.datasetPage
	case ScreenMembers:
		return &ws.memberPage
	case ScreenRecords:
		return &ws.recordPage
	case ScreenJobs:
		return &ws.jobPage
	case ScreenSpoolFiles:
		return &ws.spoolFilePage
	case ScreenSpoolContent:
		return &ws.spoolContentPage
	default:
		return nil
	}
}

// statusForCount reports an empty result; a non-empty table speaks for itself,
// so the status text stays blank.
func (ws *workspace) statusForCount(count int, noun string) {
	if count == 0 {
		ws.status = status{Level: statusEmpty, Text: "no " + noun + " returned"}
		return
	}
	ws.status = status{Level: statusReady}
}

func (ws *workspace) recordRange() (int64, int64) {
	if len(ws.records) == 0 {
		return 0, 0
	}
	return ws.records[0].Record.Number, ws.records[len(ws.records)-1].Record.Number
}
