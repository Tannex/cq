package record

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestDecodeDisplayLocalizesInvalidScalars(t *testing.T) {
	d := mustDecoder(t, `01 R.
   05 PREFIX PIC X(3).
   05 ZONED-VALUE PIC 9(3).
   05 PACKED-VALUE PIC S9(3) COMP-3.
   05 SIGNED-VALUE PIC S9(2) SIGN TRAILING SEPARATE.
   05 AFTER PIC X(2).
`)
	rec := []byte{0xC1, 0x00, 0x00}
	rec = append(rec, 0xF1, 0xFA, 0xF3)
	rec = append(rec, 0x1A, 0x3C)
	rec = append(rec, zonedUnsigned("45")...)
	rec = append(rec, ebc(t, "X", 1)...)
	rec = append(rec, ebc(t, "OK", 2)...)

	decoded, err := d.DecodeDisplay(rec)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decoded.JSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"PREFIX":"A··","ZONED-VALUE":"1�3","PACKED-VALUE":"1�3","SIGNED-VALUE":"45�","AFTER":"OK"}`
	if string(got) != want {
		t.Fatalf("display JSON = %s, want %s", got, want)
	}
	if !json.Valid(got) {
		t.Fatalf("display JSON is invalid: %s", got)
	}
	if len(decoded.Diagnostics) != 4 {
		t.Fatalf("diagnostics = %#v, want four", decoded.Diagnostics)
	}
	checks := []struct {
		path   string
		offset int
		length int
		raw    string
	}{
		{path: "PREFIX", offset: 0, length: 3, raw: "C10000"},
		{path: "ZONED-VALUE", offset: 3, length: 3, raw: "F1FAF3"},
		{path: "PACKED-VALUE", offset: 6, length: 2, raw: "1A3C"},
		{path: "SIGNED-VALUE", offset: 8, length: 3},
	}
	for i, check := range checks {
		diagnostic := decoded.Diagnostics[i]
		if diagnostic.FieldPath != check.path || diagnostic.Offset != check.offset || diagnostic.Length != check.length {
			t.Errorf("diagnostic %d = %#v, want path=%s offset=%d length=%d", i, diagnostic, check.path, check.offset, check.length)
		}
		if check.raw != "" && diagnostic.RawHex != check.raw {
			t.Errorf("diagnostic %d raw = %q, want %q", i, diagnostic.RawHex, check.raw)
		}
		if diagnostic.Err == nil {
			t.Errorf("diagnostic %d has no original error", i)
		}
	}
	if !errors.Is(decoded.Diagnostics[0], ErrLowValue) {
		t.Fatalf("LOW-VALUE diagnostic does not wrap ErrLowValue: %v", decoded.Diagnostics[0])
	}
	if !strings.Contains(decoded.Diagnostics[1].Err.Error(), "invalid digit nibble") {
		t.Fatalf("zoned diagnostic lost decoder error: %v", decoded.Diagnostics[1].Err)
	}
	if after, ok := decoded.Value.Lookup("AFTER"); !ok || after != "OK" {
		t.Fatalf("independent field after invalid scalars was not decoded: %#v", after)
	}
}

func TestDecodeDisplayEditedLowValues(t *testing.T) {
	d := mustDecoder(t, `01 R.
   05 EDITED-VALUE PIC ZZ9.
`)
	if d.Rec.Children[0].Kind != layout.KindEdited {
		t.Fatalf("test field kind = %s, want edited", d.Rec.Children[0].Kind)
	}
	rec := []byte{0xF1, 0x00, 0x00}
	decoded, err := d.DecodeDisplay(rec)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := decoded.Value.Lookup("EDITED-VALUE")
	if value != "1··" || len(decoded.Diagnostics) != 1 || !errors.Is(decoded.Diagnostics[0], ErrLowValue) {
		t.Fatalf("edited LOW-VALUE result = %#v diagnostics=%#v", value, decoded.Diagnostics)
	}
}

func TestDecodeDisplayNestedDiagnosticPath(t *testing.T) {
	d := mustDecoder(t, custBook)
	rec := custRecord(t)
	rec[28] = 0xFA // ITEMS(2).ITEM-QTY, absolute offset 28

	decoded, err := d.DecodeDisplay(rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v, want one", decoded.Diagnostics)
	}
	diagnostic := decoded.Diagnostics[0]
	if diagnostic.FieldPath != "ITEMS[2].ITEM-QTY" || diagnostic.Offset != 28 || diagnostic.Length != 3 {
		t.Fatalf("nested diagnostic = %#v", diagnostic)
	}
	itemsValue, _ := decoded.Value.Lookup("ITEMS")
	items := itemsValue.(Array)
	second := items[1].(Object)
	qty, _ := second.Lookup("ITEM-QTY")
	if qty != "-�20" {
		t.Fatalf("localized nested value = %#v, want %q", qty, "-�20")
	}
}

func TestDecodeDisplayKeepsValidNumbersExact(t *testing.T) {
	d := mustDecoder(t, `01 R.
   05 BIG PIC 9(18)V99.
   05 AMOUNT PIC S9(5)V99 COMP-3.
`)
	rec := append(zonedUnsigned("12345678901234567890"), packed("0012300", false)...)
	decoded, err := d.DecodeDisplay(rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", decoded.Diagnostics)
	}
	big, _ := decoded.Value.Lookup("BIG")
	amount, _ := decoded.Value.Lookup("AMOUNT")
	if big != json.Number("123456789012345678.90") || amount != json.Number("123.00") {
		t.Fatalf("numbers lost exact representation: BIG=%#v AMOUNT=%#v", big, amount)
	}
	got, err := decoded.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"BIG":123456789012345678.90,"AMOUNT":123.00}`; string(got) != want {
		t.Fatalf("JSON = %s, want %s", got, want)
	}
}

func TestDecodeDisplayStructuralErrorsRemainFatal(t *testing.T) {
	t.Run("short fixed record including skipped filler", func(t *testing.T) {
		d := mustDecoder(t, `01 R.
   05 A PIC X(2).
   05 FILLER PIC X(3).
`)
		rec := ebc(t, "OK", 2)

		// This is existing strict behavior: a skipped trailing filler is not read.
		strict, err := d.Decode(rec)
		if err != nil || string(strict) != `{"A":"OK"}` {
			t.Fatalf("strict regression: JSON=%s error=%v", strict, err)
		}
		if _, err := d.DecodeDisplay(rec); err == nil || !strings.Contains(err.Error(), "record R is short") {
			t.Fatalf("want hard short-record error, got %v", err)
		}
	})

	t.Run("invalid ODO counter", func(t *testing.T) {
		d := mustDecoder(t, odoBook)
		rec := append([]byte{0xF0, 0xFA, 0xF2}, ebc(t, "AAAA", 4)...)
		if _, err := d.DecodeDisplay(rec); err == nil || !strings.Contains(err.Error(), "decoding DEPENDING ON") {
			t.Fatalf("want hard ODO decode error, got %v", err)
		}
	})

	t.Run("ODO below declared minimum", func(t *testing.T) {
		d := mustDecoder(t, odoBook)
		if _, err := d.DecodeDisplay(zoned("000", false)); err == nil || !strings.Contains(err.Error(), "outside 1..5") {
			t.Fatalf("want hard ODO minimum error, got %v", err)
		}
	})

	t.Run("short ODO tail", func(t *testing.T) {
		d := mustDecoder(t, odoBook)
		rec := append(zoned("002", false), ebc(t, "AAAA", 4)...)
		if _, err := d.DecodeDisplay(rec); err == nil || !strings.Contains(err.Error(), "requires 11 bytes") {
			t.Fatalf("want hard ODO short-record error, got %v", err)
		}
	})
}

func TestDecodeDisplayBoundsDiagnosticRawHex(t *testing.T) {
	d := mustDecoder(t, `01 R.
   05 N PIC 9(40).
`)
	rec := bytes.Repeat([]byte{0xFA}, 40)
	decoded, err := d.DecodeDisplay(rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v", decoded.Diagnostics)
	}
	raw := decoded.Diagnostics[0].RawHex
	if len(raw) != diagnosticRawByteLimit*2+3 || !strings.HasSuffix(raw, "...") {
		t.Fatalf("bounded raw hex = %q (length %d)", raw, len(raw))
	}
}

func TestNewDecoderStrictValidationRegression(t *testing.T) {
	items, err := copybook.Parse(`01 R.
   05 N PIC 9.
   05 T OCCURS 1 TO 2 TIMES DEPENDING ON N PIC X.
   05 AFTER PIC X.
`, copybook.FormatFree)
	if err != nil {
		t.Fatal(err)
	}
	records, err := layout.Build(items)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewDecoder(records[0], cp037(t))
	want := "record R: OCCURS DEPENDING ON table T is not at the end of the record; only trailing ODO is supported"
	if err == nil || err.Error() != want {
		t.Fatalf("NewDecoder error = %v, want %q", err, want)
	}
}

func TestStrictDecodeErrorAndLowValueRegression(t *testing.T) {
	t.Run("invalid zoned error", func(t *testing.T) {
		d := mustDecoder(t, `01 R.
   05 N PIC 9(2).
   05 AFTER PIC X(1).
`)
		_, err := d.Decode([]byte{0xFA, 0xF1, 0xC1})
		want := "field N (offset 0): decode: Zoned: invalid digit nibble 0xA at byte 0 (0xFA)"
		if err == nil || err.Error() != want {
			t.Fatalf("strict error = %v, want %q", err, want)
		}
	})

	t.Run("trailing low values remain trimmed", func(t *testing.T) {
		d := mustDecoder(t, `01 R.
   05 TEXT-VALUE PIC X(3).
`)
		got, err := d.Decode([]byte{0xC1, 0x00, 0x00})
		if err != nil || string(got) != `{"TEXT-VALUE":"A"}` {
			t.Fatalf("strict JSON=%s error=%v", got, err)
		}
	})
}

func cp037(t *testing.T) *decode.Charmap {
	t.Helper()
	cm, err := decode.Codepage("037")
	if err != nil {
		t.Fatal(err)
	}
	return cm
}
