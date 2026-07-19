package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Tannex/cq/internal/cqt"
	"github.com/Tannex/cq/internal/dsnmap"
	"github.com/Tannex/cq/internal/zosmf"
)

// demoBrowser is a deterministic, in-memory zosmf.Browser implementation used
// by the hidden --demo flag. It requires no z/OSMF connection and produces
// plausible data sets, PDS members, and records for visual inspection.
type demoBrowser struct{}

func loadDemoSession(ctx context.Context, profile string) (cqt.Session, error) {
	if err := ctx.Err(); err != nil {
		return cqt.Session{}, err
	}
	user := "DEMOUSER"
	if profile != "" {
		user = strings.ToUpper(profile) + "USR"
	}
	return cqt.Session{
		Browser:  &demoBrowser{},
		User:     user,
		Encoding: "latin1",
	}, nil
}

// listDemoProfiles exposes multiple fake profiles so the tab bar and
// per-profile workspace behavior can be demoed offline.
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

func demoName(index int) string {
	name := demoNames[index%len(demoNames)]
	if len(name) >= 20 {
		return name[:20]
	}
	return name + strings.Repeat(" ", 20-len(name))
}

const demoCopybook = `       01  CUSTOMER-REC.
           05  CUST-ID      PIC X(6).
           05  CUST-NAME    PIC X(20).
           05  CUST-AMOUNT  PIC X(9).
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
