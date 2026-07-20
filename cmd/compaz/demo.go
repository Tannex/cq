package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tannex/cq/internal/compaz"
	"github.com/Tannex/cq/internal/dsnmap"
	"github.com/Tannex/cq/internal/zosmf"
)

// demoBrowser is a deterministic, in-memory zosmf.Browser implementation used
// by the hidden --demo flag. It requires no z/OSMF connection and produces
// plausible data sets, PDS members, and records for visual inspection.
type demoBrowser struct{}

func loadDemoSession(ctx context.Context, profile string) (compaz.Session, error) {
	if err := ctx.Err(); err != nil {
		return compaz.Session{}, err
	}
	user := "DEMOUSER"
	if profile != "" {
		user = strings.ToUpper(profile) + "USR"
	}
	return compaz.Session{
		Browser:  &demoBrowser{},
		User:     user,
		Encoding: "latin1",
	}, nil
}

// listDemoProfiles exposes multiple fake profiles so the status-line profile
// strip and per-profile workspace behavior can be demoed offline.
func listDemoProfiles(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []string{"sandbox", "dev", "prod"}, nil
}

func (d *demoBrowser) ListDataSets(ctx context.Context, request zosmf.ListDataSetsRequest) (zosmf.DataSetPage, error) {
	if err := ctx.Err(); err != nil {
		return zosmf.DataSetPage{}, err
	}
	matched := filterDataSets(demoDataSets(), request.Prefix)
	items, more := paginate(matched, strings.ToUpper(strings.TrimSpace(request.Start)), request.MaxItems, func(item zosmf.DataSet) string { return item.Name })
	return zosmf.DataSetPage{
		Items:        items,
		ReturnedRows: len(items),
		MoreRows:     more,
	}, nil
}

func (d *demoBrowser) ListMembers(ctx context.Context, request zosmf.ListMembersRequest) (zosmf.MemberPage, error) {
	if err := ctx.Err(); err != nil {
		return zosmf.MemberPage{}, err
	}
	members := demoMembers(request.DataSet)
	pattern := strings.ToUpper(strings.TrimSpace(request.Pattern))
	matched := filterMembers(members, pattern)
	items, more := paginate(matched, strings.ToUpper(strings.TrimSpace(request.Start)), request.MaxItems, func(member zosmf.Member) string { return member.Name })
	return zosmf.MemberPage{
		Items:        items,
		ReturnedRows: len(items),
		MoreRows:     more,
	}, nil
}

func (d *demoBrowser) ReadRecords(ctx context.Context, request zosmf.ReadRecordsRequest) (zosmf.RecordPage, error) {
	if err := ctx.Err(); err != nil {
		return zosmf.RecordPage{}, err
	}
	all := demoRecords(request.DataSet, request.Member)
	start := request.Start
	if start < 0 {
		start = 0
	}
	if start >= int64(len(all)) {
		return zosmf.RecordPage{Records: []zosmf.Record{}, Start: start}, nil
	}
	end := start + int64(request.MaxItems)
	if end > int64(len(all)) {
		end = int64(len(all))
	}
	records := all[start:end]
	return zosmf.RecordPage{
		Records:      records,
		Start:        start,
		ReturnedRows: len(records),
		MoreRows:     end < int64(len(all)),
	}, nil
}

func (d *demoBrowser) FetchText(_ context.Context, _ string) ([]byte, error) {
	return []byte(demoCopybook), nil
}

func (d *demoBrowser) Encoding() (string, error) {
	return "latin1", nil
}

// demoTexts keeps in-memory edited content so the full edit/save flow —
// including ETag conflict detection — works offline. It is shared across demo
// profiles so edits survive profile switches.
var demoTexts = struct {
	sync.Mutex
	content map[string]string
	version map[string]int
}{content: map[string]string{}, version: map[string]int{}}

func demoBaseText(target string) string {
	dataSet, member, _ := splitDemoTarget(target)
	records := demoRecords(dataSet, member)
	lines := make([]string, len(records))
	for i, record := range records {
		lines[i] = strings.TrimRight(string(record.Data), " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

func splitDemoTarget(target string) (dataSet, member string, found bool) {
	trimmed := strings.ToUpper(strings.TrimSpace(target))
	open := strings.IndexByte(trimmed, '(')
	if open < 0 || !strings.HasSuffix(trimmed, ")") {
		return trimmed, "", false
	}
	return trimmed[:open], trimmed[open+1 : len(trimmed)-1], true
}

func demoETag(target string) string {
	return fmt.Sprintf("demo-%d", demoTexts.version[target])
}

// ReadText serves the demo text content with a version-based ETag.
func (d *demoBrowser) ReadText(ctx context.Context, dsn string) (zosmf.TextContent, error) {
	if err := ctx.Err(); err != nil {
		return zosmf.TextContent{}, err
	}
	target := strings.ToUpper(strings.TrimSpace(dsn))
	demoTexts.Lock()
	defer demoTexts.Unlock()
	text, ok := demoTexts.content[target]
	if !ok {
		text = demoBaseText(target)
	}
	return zosmf.TextContent{Text: []byte(text), ETag: demoETag(target)}, nil
}

// WriteText stores edits in memory and enforces If-Match semantics so the
// conflict path is demonstrable offline.
func (d *demoBrowser) WriteText(ctx context.Context, request zosmf.WriteTextRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	target := strings.ToUpper(strings.TrimSpace(request.Target))
	demoTexts.Lock()
	defer demoTexts.Unlock()
	if request.ETag != "" && request.ETag != demoETag(target) {
		return "", &zosmf.HTTPError{StatusCode: http.StatusPreconditionFailed, Resource: target, Message: "the data set changed after the entity tag was captured"}
	}
	demoTexts.content[target] = string(request.Body)
	demoTexts.version[target]++
	return demoETag(target), nil
}

var demoDataSetDefs = []struct {
	name  string
	org   string
	vol   string
	recfm string
	lrecl string
	cdate string
	rdate string
}{
	{"DEMO.CUSTOMER.MASTER", "PS", "DEMO01", "FB", "80", "2026/01/02", "2026/07/18"},
	{"DEMO.CUSTOMER.TRANSACTIONS", "PS", "DEMO01", "FB", "80", "2026/02/10", "2026/07/17"},
	{"DEMO.CUSTOMER.ACCOUNTS", "PS", "DEMO01", "FB", "80", "2026/03/05", "2026/07/16"},
	{"DEMO.PARTS.MASTER", "PS", "DEMO01", "FB", "80", "2026/01/15", "2026/07/15"},
	{"DEMO.INVENTORY", "PS", "DEMO01", "FB", "80", "2026/02/20", "2026/07/14"},
	{"DEMO.ORDERS.HEADER", "PS", "DEMO01", "FB", "80", "2026/04/01", "2026/07/13"},
	{"DEMO.ORDERS.DETAIL", "PS", "DEMO01", "FB", "80", "2026/04/02", "2026/07/12"},
	{"DEMO.EMPLOYEE.MASTER", "PS", "DEMO02", "FB", "80", "2026/01/20", "2026/07/11"},
	{"DEMO.PAYROLL.WEEKLY", "PS", "DEMO02", "FB", "80", "2026/05/01", "2026/07/10"},
	{"DEMO.SALES.DAILY", "PS", "DEMO02", "FB", "80", "2026/06/01", "2026/07/09"},
	{"DEMO.LOG.DAY001", "PS", "DEMO02", "VB", "255", "2026/07/01", "2026/07/08"},
	{"DEMO.LOG.DAY002", "PS", "DEMO02", "VB", "255", "2026/07/02", "2026/07/07"},
	{"DEMO.LOG.DAY003", "PS", "DEMO02", "VB", "255", "2026/07/03", "2026/07/06"},
	{"DEMO.COPYLIB", "PO", "DEMO03", "FB", "80", "2026/01/01", "2026/07/18"},
	{"DEMO.COBCOPY", "PO", "DEMO03", "FB", "80", "2026/01/05", "2026/07/17"},
	{"DEMO.JCLLIB", "PO", "DEMO03", "FB", "80", "2026/02/01", "2026/07/16"},
	{"DEMO.PROCLIB", "PO", "DEMO03", "FB", "80", "2026/02/05", "2026/07/15"},
	{"DEMO.LOADLIB", "PO-E", "DEMO04", "U", "0", "2026/03/01", "2026/07/14"},
	{"DEMO.SOURCE.COBOL", "PO", "DEMO03", "FB", "80", "2026/03/10", "2026/07/13"},
	{"DEMO.SOURCE.ASM", "PO", "DEMO03", "FB", "80", "2026/03/12", "2026/07/12"},
	{"DEMO.SOURCE.PL1", "PO", "DEMO03", "FB", "80", "2026/03/15", "2026/07/11"},
	{"DEMO.SOURCE.C", "PO", "DEMO03", "FB", "80", "2026/03/18", "2026/07/10"},
	{"DEMO.SOURCE.REXX", "PO", "DEMO03", "FB", "80", "2026/03/20", "2026/07/09"},
	{"DEMO.TEST.DATA", "PS", "DEMO02", "FB", "80", "2026/04/15", "2026/07/08"},
	{"DEMO.REPORT.MONTHLY", "PS", "DEMO02", "FB", "132", "2026/05/15", "2026/07/07"},
	{"DEMO.ARCHIVE.Y2026M07", "PS", "DEMO05", "FB", "80", "2026/07/01", "2026/07/06"},
	{"DEMO.CONFIG.PARM", "PS", "DEMO02", "FB", "80", "2026/01/30", "2026/07/05"},
	{"DEMO.INDEX.DATA", "PS", "DEMO02", "FB", "80", "2026/02/28", "2026/07/04"},
	{"DEMO.UTILITY.CTL", "PS", "DEMO02", "FB", "80", "2026/03/25", "2026/07/03"},
	{"DEMO.TEMP.WORK", "PS", "DEMO02", "FB", "80", "2026/07/18", "2026/07/02"},
}

func demoDataSets() []zosmf.DataSet {
	items := make([]zosmf.DataSet, len(demoDataSetDefs))
	for i, def := range demoDataSetDefs {
		items[i] = zosmf.DataSet{
			Name:          def.name,
			Organization:  def.org,
			Volume:        def.vol,
			RecordFormat:  def.recfm,
			RecordLength:  def.lrecl,
			CreationDate:  def.cdate,
			ReferenceDate: def.rdate,
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

var demoMemberNames = [][]string{
	{"CUSTREC", "ORDREC", "EMPLREC", "PAYREC", "INVREC", "CFGREC", "RPTREC", "ARCREC", "TSTREC", "WRKREC"},
	{"RUNJOB", "SORTJOB", "COPYJOB", "PRINTJOB", "BACKUP", "RESTORE", "COMPILE", "LINK", "TESTJOB", "CLEANUP"},
	{"CUSTMAIN", "ORDMAIN", "PAYMAIN", "INVMENU", "RPTGEN", "BATCH01", "BATCH02", "BATCH03", "BATCH04", "BATCH05"},
	{"IOEXIT", "GETMAIN", "PUTLINE", "SORTEXIT", "DATAMGT", "UTIL01", "UTIL02", "UTIL03", "UTIL04", "UTIL05"},
	{"PL1MAIN", "PL1SUB1", "PL1SUB2", "PL1IO", "PL1UTIL", "PL1TEST", "PL1DATA", "PL1MENU", "PL1RPT", "PL1WORK"},
	{"CMAIN", "CUTIL1", "CUTIL2", "CIO", "CTABLES", "CTESTS", "CMENU", "CRPT", "CWORK", "CHDRS"},
	{"REXX1", "REXX2", "REXX3", "REXX4", "REXX5", "REXX6", "REXX7", "REXX8", "REXX9", "REXX10"},
	{"CUSTPGM", "ORDPGM", "PAYPGM", "INVPGM", "RPTPGM", "MENUPGM", "BATCHPGM", "UTILPGM", "TESTPGM", "WRKPGM"},
}

func demoMembers(dataSet string) []zosmf.Member {
	if !isPartitioned(dataSet) {
		return nil
	}
	hash := demoHash(dataSet)
	nameList := demoMemberNames[hash%len(demoMemberNames)]
	count := 8 + (hash % 5) // 8..12 members
	if count > len(nameList) {
		count = len(nameList)
	}
	members := make([]zosmf.Member, count)
	for i := 0; i < count; i++ {
		members[i] = zosmf.Member{
			Name:            nameList[i],
			Version:         1,
			Modification:    1 + (hash+i)%20,
			CreationDate:    "2026/01/01",
			ModifiedDate:    "2026/07/01",
			CurrentRecords:  120,
			InitialRecords:  120,
			ModifiedRecords: 120,
			User:            "DEMOUSER",
		}
	}
	return members
}

func isPartitioned(dataSet string) bool {
	upper := strings.ToUpper(strings.TrimSpace(dataSet))
	for _, def := range demoDataSetDefs {
		if strings.ToUpper(def.name) != upper {
			continue
		}
		org := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(def.org), " ", ""))
		return org == "PO" || org == "PO-E" || org == "POE" || org == "PDS" || org == "PDSE"
	}
	return false
}

var demoNames = []string{
	"ALICE JOHNSON", "BOB SMITH", "CAROL WHITE", "DAVID BROWN", "EVE DAVIS",
	"FRANK MILLER", "GRACE WILSON", "HENRY MOORE", "IRENE TAYLOR", "JACK ANDERSON",
	"KAREN THOMAS", "LARRY JACKSON", "MARY HARRIS", "NATHAN MARTIN", "OLIVIA THOMPSON",
	"PETER GARCIA", "QUINN ROBINSON", "RACHEL CLARK", "SAM LEWIS", "TINA LEE",
}

const demoRecordCount = 120
const demoRecordLength = 80

var demoBaseDate = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func demoRecords(dataSet, member string) []zosmf.Record {
	upperSet := strings.ToUpper(strings.TrimSpace(dataSet))
	upperMember := strings.ToUpper(strings.TrimSpace(member))
	switch {
	case strings.Contains(upperSet, "JCLLIB"):
		return demoSourceRecords(demoJCLLines(upperMember))
	case strings.Contains(upperSet, "COBOL"):
		return demoSourceRecords(demoCOBOLLines(upperMember))
	}
	seed := demoHash(dataSet + "(" + strings.ToUpper(strings.TrimSpace(member)) + ")")
	records := make([]zosmf.Record, demoRecordCount)
	for i := 0; i < demoRecordCount; i++ {
		n := i + 1
		id := fmt.Sprintf("%06d", n)
		name := demoName((seed + i) % len(demoNames))
		amount := fmt.Sprintf("%9.2f", 100.0+float64(n)*7.11+float64(seed%100))
		date := demoBaseDate.Add(time.Duration((n+seed%30)%365) * 24 * time.Hour).Format("2006-01-02")
		line := fmt.Sprintf("%6s%-20s%9s%10s", id, name, amount, date)
		if len(line) < demoRecordLength {
			line += strings.Repeat(" ", demoRecordLength-len(line))
		} else if len(line) > demoRecordLength {
			line = line[:demoRecordLength]
		}
		records[i] = zosmf.Record{Number: int64(n), Data: []byte(line)}
	}
	return records
}

// demoSourceRecords pads source lines to the demo record length so JCL and
// COBOL members look like real fixed-block library members.
func demoSourceRecords(lines []string) []zosmf.Record {
	records := make([]zosmf.Record, len(lines))
	for i, line := range lines {
		if len(line) < demoRecordLength {
			line += strings.Repeat(" ", demoRecordLength-len(line))
		} else if len(line) > demoRecordLength {
			line = line[:demoRecordLength]
		}
		records[i] = zosmf.Record{Number: int64(i + 1), Data: []byte(line)}
	}
	return records
}

// demoJCLLines exercises every highlighted JCL token class: comments,
// statement names, operations, keyword parameters, in-stream data, delimiters.
func demoJCLLines(member string) []string {
	job := member
	if job == "" {
		job = "DEMOJOB"
	}
	return []string{
		"//" + job + " JOB (ACCT01),'NIGHTLY BATCH',CLASS=A,MSGCLASS=X,NOTIFY=&SYSUID",
		"//*",
		"//* NIGHTLY CUSTOMER MASTER REFRESH - DEMO MEMBER",
		"//*",
		"//JOBLIB   DD DSN=DEMO.LOADLIB,DISP=SHR",
		"//STEP01   EXEC PGM=IEFBR14",
		"//NEWFILE  DD DSN=DEMO.OUTPUT.DAILY,DISP=(NEW,CATLG,DELETE),",
		"//            SPACE=(TRK,(15,5),RLSE),UNIT=SYSDA,",
		"//            DCB=(RECFM=FB,LRECL=80,BLKSIZE=27920)",
		"//STEP02   EXEC PGM=SORT,COND=(0,NE,STEP01)",
		"//SYSOUT   DD SYSOUT=*",
		"//SORTIN   DD DSN=DEMO.CUSTOMER.MASTER,DISP=SHR",
		"//SORTOUT  DD DSN=&&SORTED,DISP=(NEW,PASS)",
		"//SYSIN    DD *",
		"  SORT FIELDS=(1,6,CH,A)",
		"  INCLUDE COND=(27,9,ZD,GT,0)",
		"/*",
		"//STEP03   EXEC PGM=IDCAMS",
		"//SYSPRINT DD SYSOUT=*",
		"//SYSIN    DD *",
		"  REPRO INFILE(SORTIN) OUTFILE(SORTOUT)",
		"/*",
		"//",
	}
}

// demoCOBOLLines exercises the COBOL highlighter: sequence numbers, a
// column-7 comment, divisions, PIC clauses, literals, and procedure verbs.
func demoCOBOLLines(member string) []string {
	program := member
	if program == "" {
		program = "DEMOPGM"
	}
	return []string{
		"000100 IDENTIFICATION DIVISION.",
		"000200 PROGRAM-ID. " + program + ".",
		"000300*  DEMO PROGRAM - REFRESHES THE CUSTOMER MASTER EXTRACT.",
		"000400 ENVIRONMENT DIVISION.",
		"000500 DATA DIVISION.",
		"000600 WORKING-STORAGE SECTION.",
		"000700 01  WS-CUSTOMER.",
		"000800     05  WS-ID          PIC 9(6).",
		"000900     05  WS-NAME        PIC X(20).",
		"001000     05  WS-AMOUNT      PIC S9(7)V99 COMP-3.",
		"001100     05  WS-STATUS      PIC X.",
		"001200         88  WS-ACTIVE  VALUE 'A'.",
		"001300 PROCEDURE DIVISION.",
		"001400 MAIN-PARA.",
		"001500     INITIALIZE WS-CUSTOMER",
		"001600     MOVE 'PENDING' TO WS-NAME",
		"001700     PERFORM UNTIL WS-ID > 000100",
		"001800        ADD 1 TO WS-ID",
		"001900     END-PERFORM",
		"002000     IF WS-ACTIVE",
		"002100        DISPLAY 'ACTIVE: ' WS-NAME",
		"002200     ELSE",
		"002300        DISPLAY 'DORMANT: ' WS-NAME",
		"002400     END-IF",
		"002500     GOBACK.",
	}
}

func demoName(index int) string {
	name := demoNames[index%len(demoNames)]
	if len(name) >= 20 {
		return name[:20]
	}
	return name + strings.Repeat(" ", 20-len(name))
}

// demoCopybook mixes field kinds (zoned numeric, text, numeric-edited) so the
// copybook table's type-aware styling is fully exercised in demo mode.
const demoCopybook = `       01  CUSTOMER-REC.
           05  CUST-ID      PIC 9(6).
           05  CUST-NAME    PIC X(20).
           05  CUST-AMOUNT  PIC ZZZZZ9.99.
           05  CUST-DATE    PIC X(10).
           05  FILLER       PIC X(35).`

func filterDataSets(items []zosmf.DataSet, prefix string) []zosmf.DataSet {
	prefix = strings.ToUpper(strings.TrimSpace(prefix))
	if prefix == "" || prefix == "*" {
		return items
	}
	var matched []zosmf.DataSet
	for _, item := range items {
		if matchPattern(item.Name, prefix) {
			matched = append(matched, item)
		}
	}
	return matched
}

func filterMembers(members []zosmf.Member, pattern string) []zosmf.Member {
	pattern = strings.ToUpper(strings.TrimSpace(pattern))
	if pattern == "" || pattern == "*" {
		return members
	}
	var matched []zosmf.Member
	for _, member := range members {
		if matchPattern(member.Name, pattern) {
			matched = append(matched, member)
		}
	}
	return matched
}

func paginate[T any](items []T, start string, maxItems int, name func(T) string) ([]T, bool) {
	idx := 0
	if start != "" {
		for i, item := range items {
			itemName := name(item)
			if itemName == start {
				idx = i + 1
				break
			}
			if itemName > start {
				idx = i
				break
			}
		}
	}
	if idx >= len(items) {
		return nil, false
	}
	end := idx + maxItems
	if end > len(items) {
		end = len(items)
	}
	return items[idx:end], end < len(items)
}

func matchPattern(name, pattern string) bool {
	return dsnmap.MatchPattern(name, pattern)
}

func demoHash(s string) int {
	h := 0
	for _, c := range strings.ToUpper(s) {
		h = 31*h + int(c)
	}
	if h < 0 {
		h = -h
	}
	return h
}
