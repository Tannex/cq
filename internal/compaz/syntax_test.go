package compaz

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Tannex/cq/internal/zosmf"
)

var amberSGR = fgSGR(ayu.fn)

var jclSample = []string{
	"//NIGHTLY  JOB (ACCT01),'NIGHTLY BATCH',CLASS=A",
	"//* REFRESH THE CUSTOMER MASTER",
	"//STEP01   EXEC PGM=SORT",
	"//SORTIN   DD DSN=DEMO.CUSTOMER.MASTER,DISP=SHR",
	"//SYSIN    DD *",
	"  SORT FIELDS=(1,6,CH,A)",
	"/*",
}

var cobolSample = []string{
	"000100 IDENTIFICATION DIVISION.",
	"000200 PROGRAM-ID. DEMOPGM.",
	"000300* COMMENT LINE.",
	"000400 DATA DIVISION.",
	"000500 WORKING-STORAGE SECTION.",
	"000600 01  WS-REC.",
	"000700     05  WS-ID    PIC 9(6).",
	"000800     05  WS-NAME  PIC X(20).",
	"000900 PROCEDURE DIVISION.",
	"001000     MOVE 'HI' TO WS-NAME.",
}

func TestDetectSourceKind(t *testing.T) {
	if got := detectSourceKind(jclSample); got != sourceJCL {
		t.Fatalf("JCL sample detected as %v", got)
	}
	if got := detectSourceKind(cobolSample); got != sourceCOBOL {
		t.Fatalf("COBOL sample detected as %v", got)
	}
	// Flat data files often lead with numeric ids in columns 1-6; a numeric
	// sequence area alone must not flip the verdict to COBOL.
	var data []string
	for i := 1; i <= 20; i++ {
		data = append(data, fmt.Sprintf("%06dALICE JOHNSON       %9.2f2026-01-02", i, 100.0+float64(i)))
	}
	if got := detectSourceKind(data); got != sourcePlain {
		t.Fatalf("flat data detected as %v", got)
	}
	// A stray pair of //-style lines in otherwise plain text stays plain.
	mixed := append([]string{"// note", "// another"}, data...)
	if got := detectSourceKind(mixed); got != sourcePlain {
		t.Fatalf("mostly-plain text detected as %v", got)
	}
	if got := detectSourceKind(nil); got != sourcePlain {
		t.Fatalf("empty input detected as %v", got)
	}
}

func TestHighlightJCLTokenClasses(t *testing.T) {
	for _, line := range jclSample {
		if got := ansi.Strip(highlightSourceLine(line, sourceJCL)); got != line {
			t.Fatalf("highlighting changed content: %q -> %q", line, got)
		}
	}
	comment := highlightSourceLine("//* REMARK", sourceJCL)
	if !strings.Contains(comment, mutedSGR) {
		t.Fatalf("comment not muted: %q", comment)
	}
	statement := highlightSourceLine("//STEP01   EXEC PGM=SORT,COND=(0,NE)", sourceJCL)
	if !strings.Contains(statement, cyanSGR) {
		t.Fatalf("statement name not tinted: %q", statement)
	}
	if !strings.Contains(statement, amberSGR) {
		t.Fatalf("operation not tinted: %q", statement)
	}
	if !strings.Contains(statement, greenSGR) {
		t.Fatalf("parameter keys not tinted: %q", statement)
	}
	literal := highlightSourceLine("//J1 JOB (A),'NIGHTLY'", sourceJCL)
	if !strings.Contains(literal, greenSGR) {
		t.Fatalf("quoted literal not tinted: %q", literal)
	}
	if instream := highlightSourceLine("  SORT FIELDS=(1,6,CH,A)", sourceJCL); instream != "  SORT FIELDS=(1,6,CH,A)" {
		t.Fatalf("in-stream data must stay plain: %q", instream)
	}
	// Continuation lines have no operation; their parameters still tint.
	continuation := highlightSourceLine("//            SPACE=(TRK,(5,5))", sourceJCL)
	if !strings.Contains(continuation, greenSGR) || strings.Contains(continuation, amberSGR) {
		t.Fatalf("continuation styled wrong: %q", continuation)
	}
}

func TestHighlightCOBOLTokenClasses(t *testing.T) {
	for _, line := range cobolSample {
		if got := ansi.Strip(highlightSourceLine(line, sourceCOBOL)); got != line {
			t.Fatalf("highlighting changed content: %q -> %q", line, got)
		}
	}
	comment := highlightSourceLine("000300* COMMENT LINE.", sourceCOBOL)
	if !strings.Contains(comment, mutedSGR) || strings.Contains(comment, cyanSGR) {
		t.Fatalf("comment line should be muted only: %q", comment)
	}
	sequence := highlightSourceLine("000100 IDENTIFICATION DIVISION.", sourceCOBOL)
	if !strings.Contains(sequence, mutedSGR) {
		t.Fatalf("sequence area not muted: %q", sequence)
	}
	if !strings.Contains(sequence, cyanSGR) {
		t.Fatalf("division keywords not tinted: %q", sequence)
	}
	pic := highlightSourceLine("000700     05  WS-ID    PIC S9(7)V99 COMP-3.", sourceCOBOL)
	if !strings.Contains(pic, amberSGR) {
		t.Fatalf("PIC clause not tinted: %q", pic)
	}
	if picture := ansi.Strip(pic); picture != "000700     05  WS-ID    PIC S9(7)V99 COMP-3." {
		t.Fatalf("PIC line content changed: %q", picture)
	}
	literal := highlightSourceLine("001000     MOVE 'HI THERE' TO WS-NAME.", sourceCOBOL)
	if !strings.Contains(literal, greenSGR) {
		t.Fatalf("literal not tinted: %q", literal)
	}
	if !strings.Contains(literal, cyanSGR) {
		t.Fatalf("verbs not tinted: %q", literal)
	}
}

func syntaxRecords(lines []string) []recordRow {
	rows := make([]recordRow, len(lines))
	for i, line := range lines {
		rows[i] = recordRow{Record: zosmf.Record{Number: int64(i + 1), Data: []byte(line)}}
	}
	return rows
}

func applySyntaxRecords(model *Model, lines []string) {
	model.records = syntaxRecords(lines)
	keys := make([]string, len(lines))
	for i := range keys {
		keys[i] = strconv.Itoa(i + 1)
	}
	model.recordPage.reset(model.visible, model.budget)
	model.recordPage.apply(keys, false, model.recordPage.initialPlan(0))
}

func TestRecordSyntaxDetectsOncePerPage(t *testing.T) {
	model := recordViewModel(t)
	applySyntaxRecords(model, jclSample)
	if got := model.recordSyntax(); got != sourceJCL {
		t.Fatalf("detected %v, want JCL", got)
	}
	// Same cache size: the verdict is served from the cache, not re-derived —
	// swapping record contents without changing the count must not re-detect.
	for i := range model.records {
		model.records[i].Record.Data = []byte("PLAIN DATA WITHOUT MARKUP")
	}
	if got := model.recordSyntax(); got != sourceJCL {
		t.Fatalf("cached verdict was re-derived: %v", got)
	}
	// A new page (different cache size) refreshes the verdict.
	model.records = append(model.records, syntaxRecords([]string{"MORE PLAIN DATA"})...)
	if got := model.recordSyntax(); got != sourcePlain {
		t.Fatalf("verdict not refreshed on new page: %v", got)
	}
}

func TestRawRecordViewHighlightsDetectedSource(t *testing.T) {
	model := recordViewModel(t)
	model.recordMode = ModeRaw
	applySyntaxRecords(model, jclSample)
	view := model.rawRecordView()
	if !strings.Contains(view, mutedSGR) {
		t.Fatalf("JCL comment line not muted in raw view:\n%q", view)
	}
	stripped := ansi.Strip(view)
	for _, line := range jclSample {
		if !strings.Contains(stripped, strings.TrimRight(line, " ")) {
			t.Fatalf("raw view lost line %q:\n%s", line, stripped)
		}
	}
	// The selected row stays untinted so the cursor style renders uniformly:
	// its line must not contain the statement-name tint.
	selectedLine := ""
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(ansi.Strip(line), "> 00000001") {
			selectedLine = line
		}
	}
	if selectedLine == "" {
		t.Fatalf("selected row not found:\n%s", stripped)
	}
	if strings.Contains(selectedLine, cyanSGR) {
		t.Fatalf("selected row carries syntax tint: %q", selectedLine)
	}
}

func TestRawRecordViewPlainContentUnchanged(t *testing.T) {
	model := recordViewModel(t)
	model.recordMode = ModeRaw
	before := model.rawRecordView()
	if model.recordSyntax() != sourcePlain {
		t.Fatalf("tiny plain fixture misdetected")
	}
	if after := model.rawRecordView(); after != before {
		t.Fatalf("plain rendering changed")
	}
}
