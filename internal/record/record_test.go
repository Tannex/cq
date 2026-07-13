package record

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/Tannex/cq/internal/copybook"
	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/layout"
)

func mustDecoder(t *testing.T, src string) *Decoder {
	t.Helper()
	items, err := copybook.Parse(src, copybook.FormatFree)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := layout.Build(items)
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDecoder(recs[0], cp037(t))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// ebc encodes text as cp037, padded to width.
func ebc(t *testing.T, s string, width int) []byte {
	t.Helper()
	b, err := decode.EncodeString(s, width, cp037(t))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// zoned encodes digits as EBCDIC zoned decimal with overpunched sign.
func zoned(digits string, negative bool) []byte {
	b := make([]byte, len(digits))
	for i := 0; i < len(digits); i++ {
		b[i] = 0xF0 | (digits[i] - '0')
	}
	last := len(b) - 1
	if negative {
		b[last] = 0xD0 | (digits[last] - '0')
	} else {
		b[last] = 0xC0 | (digits[last] - '0')
	}
	return b
}

// packed encodes digits as COMP-3.
func packed(digits string, negative bool) []byte {
	sign := byte(0xC)
	if negative {
		sign = 0xD
	}
	nib := make([]byte, 0, len(digits)+2)
	if len(digits)%2 == 0 {
		nib = append(nib, 0)
	}
	for i := 0; i < len(digits); i++ {
		nib = append(nib, digits[i]-'0')
	}
	nib = append(nib, sign)
	out := make([]byte, len(nib)/2)
	for i := range out {
		out[i] = nib[2*i]<<4 | nib[2*i+1]
	}
	return out
}

const custBook = `01 CUST-REC.
   05 CUST-NO      PIC 9(5).
   05 CUST-NAME    PIC X(10).
   05 BALANCE      PIC S9(5)V99 COMP-3.
   05 TXN-COUNT    PIC S9(4) COMP.
   05 ITEMS OCCURS 2 TIMES.
      10 ITEM-CODE PIC X(2).
      10 ITEM-QTY  PIC S9(3).
`

func custRecord(t *testing.T) []byte {
	var b []byte
	b = append(b, zoned("00042", false)...)   // CUST-NO 42 (sign zone C is fine unsigned)
	b = append(b, ebc(t, "ALICE", 10)...)     // CUST-NAME
	b = append(b, packed("0012345", true)...) // BALANCE -123.45
	b = append(b, 0x00, 0x07)                 // TXN-COUNT 7
	b = append(b, ebc(t, "AB", 2)...)         // ITEMS(1)
	b = append(b, zoned("003", false)...)     //
	b = append(b, ebc(t, "CD", 2)...)         // ITEMS(2)
	b = append(b, zoned("120", true)...)      //
	return b
}

func TestDecodeRecord(t *testing.T) {
	d := mustDecoder(t, custBook)
	if d.Rec.MaxLength != 5+10+4+2+2*(2+3) {
		t.Fatalf("record length = %d", d.Rec.MaxLength)
	}
	js, err := d.Decode(custRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"CUST-NO":42,"CUST-NAME":"ALICE","BALANCE":-123.45,"TXN-COUNT":7,` +
		`"ITEMS":[{"ITEM-CODE":"AB","ITEM-QTY":3},{"ITEM-CODE":"CD","ITEM-QTY":-120}]}`
	if string(js) != want {
		t.Errorf("decoded:\n%s\nwant:\n%s", js, want)
	}
}

func TestNextFixedRecords(t *testing.T) {
	d := mustDecoder(t, custBook)
	one := custRecord(t)
	in := bytes.NewReader(append(append([]byte{}, one...), one...))
	for i := 0; i < 2; i++ {
		got, err := d.Next(in)
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
		if !bytes.Equal(got, one) {
			t.Fatalf("record %d bytes differ", i)
		}
	}
	if _, err := d.Next(in); err != io.EOF {
		t.Fatalf("want io.EOF, got %v", err)
	}
}

func TestNextTruncated(t *testing.T) {
	d := mustDecoder(t, custBook)
	if _, err := d.Next(bytes.NewReader(custRecord(t)[:10])); err == nil || err == io.EOF {
		t.Fatalf("want truncation error, got %v", err)
	}
}

const odoBook = `01 ODO-REC.
   05 ITEM-COUNT PIC 9(3).
   05 ITEM OCCURS 1 TO 5 TIMES DEPENDING ON ITEM-COUNT PIC X(4).
`

func TestDecodeODO(t *testing.T) {
	d := mustDecoder(t, odoBook)
	if !d.Rec.Variable() || d.Rec.MinLength != 3+4 || d.Rec.MaxLength != 3+20 {
		t.Fatalf("min/max = %d/%d variable=%v", d.Rec.MinLength, d.Rec.MaxLength, d.Rec.Variable())
	}
	var in bytes.Buffer
	in.Write(zoned("002", false))
	in.Write(ebc(t, "AAAA", 4))
	in.Write(ebc(t, "BBBB", 4))
	in.Write(zoned("001", false))
	in.Write(ebc(t, "CCCC", 4))

	raw, err := d.Next(&in)
	if err != nil {
		t.Fatal(err)
	}
	js, err := d.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"ITEM-COUNT":2,"ITEM":["AAAA","BBBB"]}`; string(js) != want {
		t.Errorf("got %s want %s", js, want)
	}

	raw, err = d.Next(&in)
	if err != nil {
		t.Fatal(err)
	}
	js, err = d.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"ITEM-COUNT":1,"ITEM":["CCCC"]}`; string(js) != want {
		t.Errorf("got %s want %s", js, want)
	}
	if _, err := d.Next(&in); err != io.EOF {
		t.Fatalf("want io.EOF, got %v", err)
	}
}

func TestODOCountOutOfRange(t *testing.T) {
	d := mustDecoder(t, odoBook)
	var in bytes.Buffer
	in.Write(zoned("009", false))
	if _, err := d.Next(&in); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("want out-of-range error, got %v", err)
	}
}

func TestFillersSkippedAndIncluded(t *testing.T) {
	src := `01 R.
   05 A PIC X(2).
   05 FILLER PIC X(3).
   05 B PIC 9(2).
`
	d := mustDecoder(t, src)
	rec := append(append(ebc(t, "HI", 2), ebc(t, "", 3)...), zoned("07", false)...)
	js, err := d.Decode(rec)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"A":"HI","B":7}`; string(js) != want {
		t.Errorf("got %s want %s", js, want)
	}
	d.IncludeFillers = true
	js, err = d.Decode(rec)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"A":"HI","FILLER":"","B":7}`; string(js) != want {
		t.Errorf("got %s want %s", js, want)
	}
}

func TestSeparateSign(t *testing.T) {
	src := `01 R.
   05 N PIC S9(3) SIGN TRAILING SEPARATE.
`
	d := mustDecoder(t, src)
	rec := append(zonedUnsigned("045"), ebc(t, "-", 1)...)
	js, err := d.Decode(rec)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"N":-45}`; string(js) != want {
		t.Errorf("got %s want %s", js, want)
	}
}

func zonedUnsigned(digits string) []byte {
	b := make([]byte, len(digits))
	for i := 0; i < len(digits); i++ {
		b[i] = 0xF0 | (digits[i] - '0')
	}
	return b
}

func cp037(t *testing.T) *decode.Charmap {
	t.Helper()
	cm, err := decode.Codepage("037")
	if err != nil {
		t.Fatal(err)
	}
	return cm
}
