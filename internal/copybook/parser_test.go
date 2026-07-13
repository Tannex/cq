package copybook

import (
	"strings"
	"testing"
)

// --- helpers -----------------------------------------------------------

func mustParse(t *testing.T, src string) []*Item {
	t.Helper()
	items, err := Parse(src, FormatAuto)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	return items
}

func findChild(t *testing.T, parent *Item, name string) *Item {
	t.Helper()
	for _, c := range parent.Children {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("child %q not found under %q", name, parent.Name)
	return nil
}

// --- 1. PICTURE parsing --------------------------------------------------

func TestPicSignedDecimal(t *testing.T) {
	src := "01 REC.\n  05 A PIC S9(4)V99.\n"
	items := mustParse(t, src)
	a := findChild(t, items[0], "A")
	pic := a.Pic
	if pic == nil {
		t.Fatal("expected a PICTURE")
	}
	if pic.Digits != 6 {
		t.Errorf("Digits = %d, want 6", pic.Digits)
	}
	if pic.Scale != 2 {
		t.Errorf("Scale = %d, want 2", pic.Scale)
	}
	if !pic.Signed {
		t.Error("Signed = false, want true")
	}
	if pic.Width != 6 {
		t.Errorf("Width = %d, want 6", pic.Width)
	}
	if pic.Category != CatNumeric {
		t.Errorf("Category = %v, want CatNumeric", pic.Category)
	}
}

func TestPicAlphanumeric(t *testing.T) {
	src := "01 REC.\n  05 B PIC X(10).\n"
	items := mustParse(t, src)
	b := findChild(t, items[0], "B")
	if b.Pic.Width != 10 {
		t.Errorf("Width = %d, want 10", b.Pic.Width)
	}
	if b.Pic.Digits != 0 {
		t.Errorf("Digits = %d, want 0", b.Pic.Digits)
	}
	if b.Pic.Category != CatAlphanumeric {
		t.Errorf("Category = %v, want CatAlphanumeric", b.Pic.Category)
	}
}

func TestPicUnsignedNumeric(t *testing.T) {
	src := "01 REC.\n  05 C PIC 9(3).\n"
	items := mustParse(t, src)
	c := findChild(t, items[0], "C")
	if c.Pic.Digits != 3 {
		t.Errorf("Digits = %d, want 3", c.Pic.Digits)
	}
	if c.Pic.Width != 3 {
		t.Errorf("Width = %d, want 3", c.Pic.Width)
	}
	if c.Pic.Signed {
		t.Error("Signed = true, want false")
	}
	if c.Pic.Category != CatNumeric {
		t.Errorf("Category = %v, want CatNumeric", c.Pic.Category)
	}
}

func TestPicNumericEdited(t *testing.T) {
	// ZZ,ZZ9.99- : 10 print positions, 3 explicit '9's plus 4 'Z's counted
	// as digit positions for informational purposes.
	src := "01 REC.\n  05 D PIC ZZ,ZZ9.99-.\n"
	items := mustParse(t, src)
	d := findChild(t, items[0], "D")
	if d.Pic.Category != CatNumericEdited {
		t.Errorf("Category = %v, want CatNumericEdited", d.Pic.Category)
	}
	if d.Pic.Width != 10 {
		t.Errorf("Width = %d, want 10", d.Pic.Width)
	}
	if d.Pic.Digits != 7 {
		t.Errorf("Digits = %d, want 7", d.Pic.Digits)
	}
}

func TestPicImpliedDecimalOnly(t *testing.T) {
	src := "01 REC.\n  05 E PIC V99.\n"
	items := mustParse(t, src)
	e := findChild(t, items[0], "E")
	if e.Pic.Digits != 2 {
		t.Errorf("Digits = %d, want 2", e.Pic.Digits)
	}
	if e.Pic.Scale != 2 {
		t.Errorf("Scale = %d, want 2", e.Pic.Scale)
	}
	if e.Pic.Width != 2 {
		t.Errorf("Width = %d, want 2", e.Pic.Width)
	}
}

func TestPicSignTrailingSeparate(t *testing.T) {
	src := "01 REC.\n  05 F PIC S9(5) SIGN TRAILING SEPARATE.\n"
	items := mustParse(t, src)
	f := findChild(t, items[0], "F")
	if f.Pic.Digits != 5 {
		t.Errorf("Digits = %d, want 5", f.Pic.Digits)
	}
	if !f.Pic.Signed {
		t.Error("Signed = false, want true")
	}
	if !f.SignSeparate {
		t.Error("SignSeparate = false, want true")
	}
	if f.SignLeading {
		t.Error("SignLeading = true, want false (TRAILING)")
	}
}

// --- 2. Level nesting, FILLER, level-88, level-66 -----------------------

func TestLevelNestingAndFiller(t *testing.T) {
	src := "01 REC.\n" +
		"  05 GRP.\n" +
		"    10 A PIC X(1).\n" +
		"    10 FILLER PIC X(2).\n" +
		"    10 PIC X(3).\n" + // implicit filler (no name at all)
		"  05 B PIC X(1).\n"
	items := mustParse(t, src)
	rec := items[0]
	if len(rec.Children) != 2 {
		t.Fatalf("REC children = %d, want 2", len(rec.Children))
	}
	grp := findChild(t, rec, "GRP")
	if len(grp.Children) != 3 {
		t.Fatalf("GRP children = %d, want 3", len(grp.Children))
	}
	if !grp.Children[1].Filler || grp.Children[1].Name != "FILLER" {
		t.Errorf("explicit FILLER: Filler=%v Name=%q", grp.Children[1].Filler, grp.Children[1].Name)
	}
	if !grp.Children[2].Filler || grp.Children[2].Name != "FILLER" {
		t.Errorf("implicit filler: Filler=%v Name=%q", grp.Children[2].Filler, grp.Children[2].Name)
	}
	if grp.Level != 5 {
		t.Errorf("GRP.Level = %d, want 5", grp.Level)
	}
	if grp.Children[0].Level != 10 {
		t.Errorf("A.Level = %d, want 10", grp.Children[0].Level)
	}
}

func TestLevel88ConditionsAttach(t *testing.T) {
	src := "01 REC.\n" +
		"  05 FLAG PIC 9(1).\n" +
		"    88 FLAG-YES VALUE 1.\n" +
		"    88 FLAG-NO VALUE 0, 9.\n" +
		"  05 OTHER PIC X(1).\n"
	items := mustParse(t, src)
	rec := items[0]
	if len(rec.Children) != 2 {
		t.Fatalf("REC children = %d, want 2 (88s must not become siblings)", len(rec.Children))
	}
	flag := findChild(t, rec, "FLAG")
	if len(flag.Conditions) != 2 {
		t.Fatalf("FLAG.Conditions = %d, want 2", len(flag.Conditions))
	}
	if flag.Conditions[0].Name != "FLAG-YES" || len(flag.Conditions[0].Values) != 1 || flag.Conditions[0].Values[0] != "1" {
		t.Errorf("FLAG-YES condition = %+v", flag.Conditions[0])
	}
	if flag.Conditions[1].Name != "FLAG-NO" {
		t.Errorf("FLAG-NO name = %q", flag.Conditions[1].Name)
	}
	if len(flag.Conditions[1].Values) != 2 || flag.Conditions[1].Values[0] != "0" || flag.Conditions[1].Values[1] != "9" {
		t.Errorf("FLAG-NO values = %+v, want [0 9]", flag.Conditions[1].Values)
	}
}

func TestLevel88NoPrecedingItem(t *testing.T) {
	src := "01 REC.\n  88 BAD VALUE 1.\n"
	_, err := Parse(src, FormatAuto)
	if err == nil {
		t.Fatal("expected error for level 88 with no preceding data item")
	}
}

func TestLevel66Renames(t *testing.T) {
	src := "01 REC.\n" +
		"  05 A PIC X(2).\n" +
		"  05 B PIC X(3).\n" +
		"  66 AB RENAMES A THRU B.\n" +
		"  05 C PIC X(1).\n"
	items := mustParse(t, src)
	rec := items[0]
	if len(rec.Children) != 3 {
		t.Fatalf("REC children = %d, want 3 (66 must be skipped entirely)", len(rec.Children))
	}
	for _, c := range rec.Children {
		if c.Name == "AB" {
			t.Errorf("level-66 RENAMES entry %q leaked into the tree", c.Name)
		}
	}
	if rec.Children[2].Name != "C" {
		t.Errorf("last child = %q, want C", rec.Children[2].Name)
	}
}

// --- 3. OCCURS / INDEXED BY ----------------------------------------------

func TestOccursFixed(t *testing.T) {
	src := "01 REC.\n  05 TBL PIC X(2) OCCURS 5 TIMES.\n"
	items := mustParse(t, src)
	tbl := findChild(t, items[0], "TBL")
	if tbl.Occurs != 5 {
		t.Errorf("Occurs = %d, want 5", tbl.Occurs)
	}
	if tbl.OccursMin != 0 {
		t.Errorf("OccursMin = %d, want 0", tbl.OccursMin)
	}
	if tbl.DependingOn != "" {
		t.Errorf("DependingOn = %q, want empty", tbl.DependingOn)
	}
}

func TestOccursDependingOn(t *testing.T) {
	src := "01 REC.\n" +
		"  05 CNT PIC 9(4).\n" +
		"  05 TBL PIC X(2) OCCURS 1 TO 10 TIMES DEPENDING ON CNT INDEXED BY IDX.\n"
	items := mustParse(t, src)
	tbl := findChild(t, items[0], "TBL")
	if tbl.Occurs != 10 {
		t.Errorf("Occurs = %d, want 10", tbl.Occurs)
	}
	if tbl.OccursMin != 1 {
		t.Errorf("OccursMin = %d, want 1", tbl.OccursMin)
	}
	if tbl.DependingOn != "CNT" {
		t.Errorf("DependingOn = %q, want CNT", tbl.DependingOn)
	}
}

func TestOccursIndexedByConsumed(t *testing.T) {
	// INDEXED BY with multiple index names must be fully consumed, leaving
	// the terminating period intact and not producing a parse error.
	src := "01 REC.\n  05 TBL PIC X(2) OCCURS 3 TIMES INDEXED BY IDX1 IDX2.\n  05 AFTER PIC X(1).\n"
	items := mustParse(t, src)
	if len(items[0].Children) != 2 {
		t.Fatalf("children = %d, want 2", len(items[0].Children))
	}
	after := findChild(t, items[0], "AFTER")
	if after.Pic == nil || after.Pic.Width != 1 {
		t.Errorf("AFTER not parsed correctly after INDEXED BY clause: %+v", after)
	}
}

// --- 4. REDEFINES / USAGE -------------------------------------------------

func TestRedefinesRecorded(t *testing.T) {
	src := "01 REC.\n" +
		"  05 A PIC X(4).\n" +
		"  05 B REDEFINES A PIC 9(4).\n"
	items := mustParse(t, src)
	b := findChild(t, items[0], "B")
	if b.Redefines != "A" {
		t.Errorf("Redefines = %q, want A", b.Redefines)
	}
}

func TestUsageVariants(t *testing.T) {
	cases := []struct {
		clause string
		want   Usage
	}{
		{"COMP", UsageBinary},
		{"COMP-3", UsagePacked},
		{"BINARY", UsageBinary},
		{"PACKED-DECIMAL", UsagePacked},
		{"USAGE IS COMP", UsageBinary},
		{"USAGE COMP-3", UsagePacked},
	}
	for _, c := range cases {
		src := "01 REC.\n  05 A PIC 9(4) " + c.clause + ".\n"
		items := mustParse(t, src)
		a := findChild(t, items[0], "A")
		if a.Usage != c.want {
			t.Errorf("clause %q: Usage = %v, want %v", c.clause, a.Usage, c.want)
		}
		if !a.UsageSet {
			t.Errorf("clause %q: UsageSet = false, want true", c.clause)
		}
	}
}

func TestUsageDefaultDisplay(t *testing.T) {
	src := "01 REC.\n  05 A PIC 9(4).\n"
	items := mustParse(t, src)
	a := findChild(t, items[0], "A")
	if a.Usage != UsageDisplay {
		t.Errorf("Usage = %v, want UsageDisplay", a.Usage)
	}
	if a.UsageSet {
		t.Error("UsageSet = true, want false (no explicit USAGE)")
	}
}

// --- 5. Fixed-format handling ---------------------------------------------

func TestFixedFormatColumns(t *testing.T) {
	// Line 1: normal fixed-format entry (sequence area + code).
	// Line 2: '*' in column 7 marks a full-line comment; must be ignored
	// even though it contains what looks like copybook code.
	// Line 3: a sequence-number-only line (no code) must not break
	// fixed-format detection nor produce a code line.
	// Line 4: code in columns 8-72, followed by garbage beyond column 72
	// that must be truncated and ignored.
	line1 := "000100 01 REC.                                                        "
	line2 := "000200*05 SHOULD-NOT-APPEAR PIC X(9).                                 "
	line3 := "000300"
	code := " 05 A PIC X(3)."
	line4 := "000400" + code
	for len(line4) < 72 {
		line4 += " "
	}
	line4 += "IGNOREDGARBAGEPASTCOL72"

	src := line1 + "\n" + line2 + "\n" + line3 + "\n" + line4 + "\n"
	items, err := Parse(src, FormatAuto)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	rec := items[0]
	if rec.Name != "REC" {
		t.Fatalf("root name = %q, want REC", rec.Name)
	}
	if len(rec.Children) != 1 {
		t.Fatalf("children = %d, want 1 (comment line and garbage past col 72 must not leak in)", len(rec.Children))
	}
	a := rec.Children[0]
	if a.Name != "A" || a.Pic == nil || a.Pic.Width != 3 {
		t.Errorf("child = %+v, want A PIC X(3)", a)
	}
	for _, c := range rec.Children {
		if strings.Contains(c.Name, "SHOULD-NOT-APPEAR") {
			t.Errorf("comment line leaked into parse tree: %q", c.Name)
		}
	}
}

func TestFixedFormatDetectionExplicit(t *testing.T) {
	// FormatFixed forced explicitly, same shape as auto-detected case.
	src := "000100 01 REC.                                                        \n" +
		"000200     05 A PIC X(2).                                            \n"
	items, err := Parse(src, FormatFixed)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if items[0].Name != "REC" {
		t.Fatalf("root = %q, want REC", items[0].Name)
	}
}

func TestFreeFormatDetection(t *testing.T) {
	// A line whose columns 1-6 contain non-digit, non-space content forces
	// free format, where the entire line is code.
	src := "01 REC.\n  05 A PIC X(2).\n"
	items, err := Parse(src, FormatAuto)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if items[0].Name != "REC" {
		t.Fatalf("root = %q, want REC", items[0].Name)
	}
}

// --- 6. Error cases ---------------------------------------------------------

func TestErrorBadLevelNumber(t *testing.T) {
	_, err := Parse("01 REC.\n  50 A PIC X(1).\n", FormatAuto)
	if err == nil {
		t.Fatal("expected error for invalid level number 50")
	}
	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("error type = %T, want *ParseError", err)
	}
	if pe.Line != 2 {
		t.Errorf("ParseError.Line = %d, want 2", pe.Line)
	}
}

func TestErrorPicUnclosedParen(t *testing.T) {
	_, err := Parse("01 REC.\n  05 A PIC X(3.\n", FormatAuto)
	if err == nil {
		t.Fatal("expected error for unclosed parenthesis in PICTURE")
	}
	if !strings.Contains(err.Error(), "unclosed parenthesis") {
		t.Errorf("error = %q, want mention of unclosed parenthesis", err.Error())
	}
}

func TestErrorUnexpectedToken(t *testing.T) {
	_, err := Parse("01 REC.\n  05 A PIC X(3) BOGUSCLAUSE.\n", FormatAuto)
	if err == nil {
		t.Fatal("expected error for unexpected token in entry")
	}
	if !strings.Contains(err.Error(), "unexpected token") {
		t.Errorf("error = %q, want mention of unexpected token", err.Error())
	}
}

func TestErrorEmptyCopybook(t *testing.T) {
	_, err := Parse("", FormatAuto)
	if err == nil {
		t.Fatal("expected error for a copybook with no data-description entries")
	}
}
