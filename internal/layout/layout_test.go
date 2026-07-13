package layout

import (
	"encoding/xml"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/Tannex/cq/internal/copybook"
)

// --- helpers ---------------------------------------------------------------

func mustBuild(t *testing.T, src string) []*Record {
	t.Helper()
	items, err := copybook.Parse(src, copybook.FormatAuto)
	if err != nil {
		t.Fatalf("copybook.Parse failed: %v", err)
	}
	recs, err := Build(items)
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	return recs
}

func child(t *testing.T, f *Field, name string) *Field {
	t.Helper()
	for _, c := range f.Children {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("child %q not found under %q (children: %v)", name, f.Name, childNames(f))
	return nil
}

func childNames(f *Field) []string {
	var names []string
	for _, c := range f.Children {
		names = append(names, c.Name)
	}
	return names
}

// --- 1. Hand-computed layout -------------------------------------------------

func TestLayoutBasicOffsetsAndLengths(t *testing.T) {
	src := "01 REC.\n" +
		"  05 A PIC X(5).\n" +
		"  05 B PIC S9(4) COMP.\n" +
		"  05 C PIC S9(7)V99 COMP-3.\n" +
		"  05 D PIC 9(3).\n"
	recs := mustBuild(t, src)
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	rec := recs[0]

	a := child(t, rec.Field, "A")
	if a.Offset != 0 || a.Length != 5 || a.Kind != KindText {
		t.Errorf("A = offset=%d length=%d kind=%s, want offset=0 length=5 kind=text", a.Offset, a.Length, a.Kind)
	}

	// S9(4) COMP: digits<=4 -> 2 bytes.
	b := child(t, rec.Field, "B")
	if b.Offset != 5 || b.Length != 2 || b.Kind != KindBinary {
		t.Errorf("B = offset=%d length=%d kind=%s, want offset=5 length=2 kind=binary", b.Offset, b.Length, b.Kind)
	}

	// S9(7)V99 COMP-3: digits=9 -> 9/2+1 = 5 bytes.
	c := child(t, rec.Field, "C")
	if c.Offset != 7 || c.Length != 5 || c.Kind != KindPacked {
		t.Errorf("C = offset=%d length=%d kind=%s, want offset=7 length=5 kind=packed", c.Offset, c.Length, c.Kind)
	}
	if c.Digits != 9 || c.Scale != 2 {
		t.Errorf("C digits/scale = %d/%d, want 9/2", c.Digits, c.Scale)
	}

	// 9(3) DISPLAY: zoned decimal, 3 bytes.
	d := child(t, rec.Field, "D")
	if d.Offset != 12 || d.Length != 3 || d.Kind != KindZoned {
		t.Errorf("D = offset=%d length=%d kind=%s, want offset=12 length=3 kind=zoned", d.Offset, d.Length, d.Kind)
	}

	wantMax := 15
	if rec.MaxLength != wantMax {
		t.Errorf("MaxLength = %d, want %d", rec.MaxLength, wantMax)
	}
	if rec.MinLength != rec.MaxLength {
		t.Errorf("MinLength = %d, want == MaxLength (%d) for a fixed record", rec.MinLength, rec.MaxLength)
	}
	if rec.Variable() {
		t.Error("Variable() = true, want false for a fixed-length record")
	}
}

// --- 2. REDEFINES ------------------------------------------------------------

func TestLayoutRedefinesSharesOffset(t *testing.T) {
	src := "01 REC.\n" +
		"  05 A PIC X(4).\n" +
		"  05 B REDEFINES A PIC 9(4).\n" +
		"  05 C PIC X(2).\n"
	recs := mustBuild(t, src)
	rec := recs[0]

	a := child(t, rec.Field, "A")
	b := child(t, rec.Field, "B")
	c := child(t, rec.Field, "C")

	if a.Offset != 0 || a.Length != 4 {
		t.Errorf("A = offset=%d length=%d, want offset=0 length=4", a.Offset, a.Length)
	}
	if b.Offset != a.Offset {
		t.Errorf("B.Offset = %d, want == A.Offset (%d)", b.Offset, a.Offset)
	}
	if b.Length != 4 {
		t.Errorf("B.Length = %d, want 4", b.Length)
	}
	// C must follow A's extent (4 bytes), not extend because of B.
	if c.Offset != 4 {
		t.Errorf("C.Offset = %d, want 4", c.Offset)
	}
}

func TestLayoutRedefinesLongerThanTargetExtendsGroup(t *testing.T) {
	// B REDEFINES A but is longer than A; the following field C must start
	// after B's extent, not A's.
	src := "01 REC.\n" +
		"  05 A PIC X(3).\n" +
		"  05 B REDEFINES A PIC X(5).\n" +
		"  05 C PIC X(2).\n"
	recs := mustBuild(t, src)
	rec := recs[0]

	a := child(t, rec.Field, "A")
	b := child(t, rec.Field, "B")
	c := child(t, rec.Field, "C")

	if a.Offset != 0 || a.Length != 3 {
		t.Errorf("A = offset=%d length=%d, want offset=0 length=3", a.Offset, a.Length)
	}
	if b.Offset != 0 || b.Length != 5 {
		t.Errorf("B = offset=%d length=%d, want offset=0 length=5", b.Offset, b.Length)
	}
	if c.Offset != 5 {
		t.Errorf("C.Offset = %d, want 5 (after the longer redefinition)", c.Offset)
	}
	if rec.Length != 7 {
		t.Errorf("record length = %d, want 7", rec.Length)
	}
}

// --- 3. OCCURS ---------------------------------------------------------------

func TestLayoutOccursElementaryLengthVsTotal(t *testing.T) {
	src := "01 REC.\n" +
		"  05 TBL PIC X(4) OCCURS 3 TIMES.\n" +
		"  05 AFTER PIC X(1).\n"
	recs := mustBuild(t, src)
	rec := recs[0]

	tbl := child(t, rec.Field, "TBL")
	if tbl.Length != 4 {
		t.Errorf("TBL.Length (one element) = %d, want 4", tbl.Length)
	}
	if tbl.Occurs != 3 {
		t.Errorf("TBL.Occurs = %d, want 3", tbl.Occurs)
	}
	if tbl.total() != 12 {
		t.Errorf("TBL total() = %d, want 12", tbl.total())
	}
	after := child(t, rec.Field, "AFTER")
	if after.Offset != 12 {
		t.Errorf("AFTER.Offset = %d, want 12 (after the full 3-element table)", after.Offset)
	}
}

func TestLayoutOccursGroupWithChildren(t *testing.T) {
	// Children's offsets inside a repeated group are relative to the first
	// element only; the group's own Length covers one element.
	src := "01 REC.\n" +
		"  05 TBL OCCURS 3 TIMES.\n" +
		"    10 X PIC X(2).\n" +
		"    10 Y PIC X(3).\n"
	recs := mustBuild(t, src)
	rec := recs[0]

	tbl := child(t, rec.Field, "TBL")
	if tbl.Kind != KindGroup {
		t.Fatalf("TBL.Kind = %s, want group", tbl.Kind)
	}
	if tbl.Length != 5 {
		t.Errorf("TBL.Length (one element) = %d, want 5", tbl.Length)
	}
	if tbl.Occurs != 3 {
		t.Errorf("TBL.Occurs = %d, want 3", tbl.Occurs)
	}

	x := child(t, tbl, "X")
	y := child(t, tbl, "Y")
	if x.Offset != 0 || x.Length != 2 {
		t.Errorf("X = offset=%d length=%d, want offset=0 length=2", x.Offset, x.Length)
	}
	if y.Offset != 2 || y.Length != 3 {
		t.Errorf("Y = offset=%d length=%d, want offset=2 length=3", y.Offset, y.Length)
	}

	if rec.Length != 15 {
		t.Errorf("record length = %d, want 15 (5 * 3 occurrences)", rec.Length)
	}
}

// --- 4. SYNC alignment --------------------------------------------------------

func TestLayoutSyncAlignment2Byte(t *testing.T) {
	// A is 3 bytes (offset 0-2, end=3). B is a 2-byte COMP field with SYNC;
	// 3 % 2 == 1, so B is padded up to the next 2-byte boundary: offset 4.
	src := "01 REC.\n" +
		"  05 A PIC X(3).\n" +
		"  05 B PIC S9(4) COMP SYNC.\n"
	recs := mustBuild(t, src)
	rec := recs[0]

	a := child(t, rec.Field, "A")
	b := child(t, rec.Field, "B")
	if a.Offset != 0 || a.Length != 3 {
		t.Errorf("A = offset=%d length=%d, want offset=0 length=3", a.Offset, a.Length)
	}
	if b.Offset != 4 {
		t.Errorf("B.Offset = %d, want 4 (2-byte SYNC alignment past slack byte 3)", b.Offset)
	}
	if b.Length != 2 {
		t.Errorf("B.Length = %d, want 2", b.Length)
	}
	if rec.Length != 6 {
		t.Errorf("record length = %d, want 6 (4 + 2)", rec.Length)
	}
}

func TestLayoutSyncAlignment4Byte(t *testing.T) {
	// A is 1 byte (offset 0, end=1). B is a 4-byte COMP field (digits<=9)
	// with SYNC; 1 % 4 == 1, so B is padded up to the next 4-byte boundary:
	// offset 4.
	src := "01 REC.\n" +
		"  05 A PIC X(1).\n" +
		"  05 B PIC S9(9) COMP SYNC.\n"
	recs := mustBuild(t, src)
	rec := recs[0]

	a := child(t, rec.Field, "A")
	b := child(t, rec.Field, "B")
	if a.Offset != 0 || a.Length != 1 {
		t.Errorf("A = offset=%d length=%d, want offset=0 length=1", a.Offset, a.Length)
	}
	if b.Offset != 4 {
		t.Errorf("B.Offset = %d, want 4 (4-byte SYNC alignment)", b.Offset)
	}
	if b.Length != 4 {
		t.Errorf("B.Length = %d, want 4", b.Length)
	}
	if rec.Length != 8 {
		t.Errorf("record length = %d, want 8 (4 + 4)", rec.Length)
	}
}

// --- 5. ODO record -------------------------------------------------------------

func TestLayoutOdoMinMaxAndVariable(t *testing.T) {
	src := "01 REC.\n" +
		"  05 CNT PIC 9(3).\n" +
		"  05 TBL PIC X(2) OCCURS 1 TO 5 TIMES DEPENDING ON CNT.\n"
	recs := mustBuild(t, src)
	rec := recs[0]

	// CNT: 3 bytes. TBL element: 2 bytes, occurs 1..5.
	// MaxLength = 3 + 2*5 = 13. MinLength = 13 - (5-1)*2 = 5.
	if rec.MaxLength != 13 {
		t.Errorf("MaxLength = %d, want 13", rec.MaxLength)
	}
	if rec.MinLength != 5 {
		t.Errorf("MinLength = %d, want 5", rec.MinLength)
	}
	if !rec.Variable() {
		t.Error("Variable() = false, want true for an OCCURS DEPENDING ON record")
	}
}

func TestLayoutNonOdoIsNotVariable(t *testing.T) {
	src := "01 REC.\n  05 A PIC X(4).\n  05 B PIC 9(2).\n"
	recs := mustBuild(t, src)
	rec := recs[0]
	if rec.Variable() {
		t.Error("Variable() = true, want false (no OCCURS DEPENDING ON present)")
	}
	if rec.MinLength != rec.MaxLength {
		t.Errorf("MinLength (%d) != MaxLength (%d) for a fixed record", rec.MinLength, rec.MaxLength)
	}
}

// --- Walk / FindByName sanity ---------------------------------------------

func TestWalkAndFindByName(t *testing.T) {
	src := "01 REC.\n" +
		"  05 GRP.\n" +
		"    10 A PIC X(1).\n" +
		"    10 B PIC X(2).\n"
	recs := mustBuild(t, src)
	rec := recs[0]

	var names []string
	Walk(rec.Field, func(f *Field) { names = append(names, f.Name) })
	want := []string{"RECORD", "GRP", "A", "B"}
	if len(names) != len(want) {
		t.Fatalf("Walk visited %v, want %v", names, want)
	}
	for i := range want {
		// The root record synthetic name may differ; only check the
		// deterministic parts for this hand-written copybook (single 01
		// item; Build should not wrap it).
		if i == 0 {
			continue
		}
		if names[i] != want[i] {
			t.Errorf("Walk[%d] = %q, want %q", i, names[i], want[i])
		}
	}

	found := FindByName(rec.Field, "B")
	if found == nil || found.Name != "B" || found.Offset != 1 {
		t.Errorf("FindByName(B) = %+v, want offset=1", found)
	}

	notFound := FindByName(rec.Field, "NOPE")
	if notFound != nil {
		t.Errorf("FindByName(NOPE) = %+v, want nil", notFound)
	}
}

// --- 6. Golden cross-check against cb2xml expected XML ------------------------

// xmlItem mirrors the subset of cb2xml's <item> element this test needs.
type xmlItem struct {
	Level         string    `xml:"level,attr"`
	Name          string    `xml:"name,attr"`
	Position      string    `xml:"position,attr"`
	StorageLength string    `xml:"storage-length,attr"`
	Items         []xmlItem `xml:"item"`
}

type xmlCopybook struct {
	Items []xmlItem `xml:"item"`
}

// flatXMLItem is one item flattened into document order, alongside its
// resolved position/length (skipping items without a position attribute,
// per cb2xml semantics for group headers that carry no storage of their
// own... though in practice DTAR107/DTAR119 have positions on every item).
type flatItem struct {
	name   string
	pos    int
	length int
}

func flattenXML(items []xmlItem, out *[]flatItem) {
	for _, it := range items {
		if it.Position == "" {
			flattenXML(it.Items, out)
			continue
		}
		pos, err := strconv.Atoi(it.Position)
		if err != nil {
			continue
		}
		sl, err := strconv.Atoi(it.StorageLength)
		if err != nil {
			continue
		}
		*out = append(*out, flatItem{name: it.Name, pos: pos, length: sl})
		flattenXML(it.Items, out)
	}
}

func flattenLayout(f *Field, out *[]flatItem) {
	*out = append(*out, flatItem{name: f.Name, pos: f.Offset + 1, length: f.Length})
	for _, c := range f.Children {
		flattenLayout(c, out)
	}
}

func TestLayoutGoldenCb2xml(t *testing.T) {
	cases := []string{"DTAR107", "DTAR119"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			cbPath := "../../testdata/copybooks/cb2xml/" + name + ".cbl"
			xmlPath := "../../testdata/copybooks/cb2xml/expected-xml/" + name + ".cbl.xml"

			src, err := os.ReadFile(cbPath)
			if err != nil {
				t.Fatalf("reading copybook: %v", err)
			}
			items, err := copybook.Parse(string(src), copybook.FormatAuto)
			if err != nil {
				t.Fatalf("copybook.Parse: %v", err)
			}
			recs, err := Build(items)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if len(recs) != 1 {
				t.Fatalf("records = %d, want 1", len(recs))
			}
			rec := recs[0]
			// DTAR107/DTAR119 start their entries at level 03 (no 01), so
			// Build wraps them in a synthetic "RECORD" root; the XML items
			// then correspond to the wrapper's children.
			if rec.Name != "RECORD" {
				t.Fatalf("root name = %q, want synthetic RECORD wrapper", rec.Name)
			}

			xb, err := os.ReadFile(xmlPath)
			if err != nil {
				t.Fatalf("reading expected xml: %v", err)
			}
			var cb xmlCopybook
			if err := xml.Unmarshal(xb, &cb); err != nil {
				t.Fatalf("unmarshalling expected xml: %v", err)
			}

			var xmlFlat []flatItem
			flattenXML(cb.Items, &xmlFlat)

			var layoutFlat []flatItem
			for _, c := range rec.Children {
				flattenLayout(c, &layoutFlat)
			}

			if len(xmlFlat) != len(layoutFlat) {
				t.Fatalf("item count mismatch: xml=%d layout=%d\nxml=%v\nlayout=%v",
					len(xmlFlat), len(layoutFlat), xmlFlat, layoutFlat)
			}
			for i := range xmlFlat {
				xi, li := xmlFlat[i], layoutFlat[i]
				if !strings.EqualFold(xi.name, li.name) {
					t.Errorf("item %d: name = %q, want %q (case-insensitive)", i, li.name, xi.name)
				}
				if li.pos != xi.pos {
					t.Errorf("item %d (%s): Offset+1 = %d, want position %d", i, xi.name, li.pos, xi.pos)
				}
				if li.length != xi.length {
					t.Errorf("item %d (%s): Length = %d, want storage-length %d", i, xi.name, li.length, xi.length)
				}
			}
		})
	}
}
