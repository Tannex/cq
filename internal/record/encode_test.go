package record

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Tannex/cq/internal/copybook"
	"github.com/Tannex/cq/internal/layout"
)

func mustEncoder(t *testing.T, src string) *Encoder {
	t.Helper()
	items, err := copybook.Parse(src, copybook.FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := layout.Build(items)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEncoder(recs[0], cp037(t))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestEncodeJSONRoundTrip(t *testing.T) {
	d := mustDecoder(t, custBook)
	want, err := d.Decode(custRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	e := mustEncoder(t, custBook)

	var binary bytes.Buffer
	if err := EncodeJSON(&binary, e, bytes.NewReader(want)); err != nil {
		t.Fatal(err)
	}
	got, err := d.Decode(binary.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round trip:\n got %s\nwant %s", got, want)
	}
}

func TestEncodeJSONArray(t *testing.T) {
	e := mustEncoder(t, `01 R.
  05 A PIC X(2).
  05 N PIC S9(3) COMP-3.
`)
	var got bytes.Buffer
	err := EncodeJSON(&got, e, strings.NewReader(`[{"A":"AB","N":-12},{"A":"CD","N":7}]`))
	if err != nil {
		t.Fatal(err)
	}
	want := append(append(ebc(t, "AB", 2), packed("012", true)...), ebc(t, "CD", 2)...)
	want = append(want, packed("007", false)...)
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("encoded = % X, want % X", got.Bytes(), want)
	}
}

func TestEncodeODO(t *testing.T) {
	e := mustEncoder(t, odoBook)
	var got bytes.Buffer
	err := EncodeJSON(&got, e, strings.NewReader(`[
      {"ITEM-COUNT":2,"ITEM":["AAAA","BBBB"]},
      {"ITEM-COUNT":1,"ITEM":["CCCC"]}
    ]`))
	if err != nil {
		t.Fatal(err)
	}
	want := append(zonedUnsigned("002"), ebc(t, "AAAA", 4)...)
	want = append(want, ebc(t, "BBBB", 4)...)
	want = append(want, zonedUnsigned("001")...)
	want = append(want, ebc(t, "CCCC", 4)...)
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("encoded = % X, want % X", got.Bytes(), want)
	}
}

func TestEncodeDefaultsFillerAndLrecl(t *testing.T) {
	e := mustEncoder(t, `01 R.
  05 A PIC X(2).
  05 FILLER PIC 9(2).
`)
	e.Lrecl = 6
	var got bytes.Buffer
	if err := EncodeJSON(&got, e, strings.NewReader(`{"A":"X"}`)); err != nil {
		t.Fatal(err)
	}
	want := append(ebc(t, "X", 2), zonedUnsigned("00")...)
	want = append(want, ebc(t, "", 2)...)
	if !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("encoded = % X, want % X", got.Bytes(), want)
	}
}

func TestEncodeJSONPreservesFillers(t *testing.T) {
	const book = `01 R.
  05 A PIC X(2).
  05 FILLER PIC X(2).
  05 N PIC 9(2).
  05 FILLER PIC 9(2).
`
	record := append(ebc(t, "AB", 2), ebc(t, "XY", 2)...)
	record = append(record, zonedUnsigned("12")...)
	record = append(record, zonedUnsigned("34")...)

	d := mustDecoder(t, book)
	d.IncludeFillers = true
	decoded, err := d.Decode(record)
	if err != nil {
		t.Fatal(err)
	}
	e := mustEncoder(t, book)
	var got bytes.Buffer
	if err := EncodeJSON(&got, e, bytes.NewReader(decoded)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), record) {
		t.Fatalf("re-encoded = % X, want original % X", got.Bytes(), record)
	}
}

func TestEncodeBinaryAcceptsFullStorageRange(t *testing.T) {
	// Stored COMP values routinely exceed the PICTURE digit count; what
	// decodes must re-encode.
	const book = `01 R.
  05 N PIC S9(4) COMP.
`
	record := []byte{0x7F, 0xFF}
	d := mustDecoder(t, book)
	decoded, err := d.Decode(record)
	if err != nil {
		t.Fatal(err)
	}
	e := mustEncoder(t, book)
	var got bytes.Buffer
	if err := EncodeJSON(&got, e, bytes.NewReader(decoded)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), record) {
		t.Fatalf("re-encoded = % X, want original % X", got.Bytes(), record)
	}
}

func TestEncodePackedAcceptsPadNibbleDigit(t *testing.T) {
	// An even-digit COMP-3 picture leaves a pad nibble that real data can
	// use; what decodes must re-encode.
	const book = `01 R.
  05 N PIC 9(4) COMP-3.
`
	record := []byte{0x12, 0x34, 0x5F}
	d := mustDecoder(t, book)
	decoded, err := d.Decode(record)
	if err != nil {
		t.Fatal(err)
	}
	e := mustEncoder(t, book)
	var got bytes.Buffer
	if err := EncodeJSON(&got, e, bytes.NewReader(decoded)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), record) {
		t.Fatalf("re-encoded = % X, want original % X", got.Bytes(), record)
	}
}

func TestEncodeJSONRejectsPartialFillers(t *testing.T) {
	e := mustEncoder(t, `01 R.
  05 A PIC X(2).
  05 FILLER PIC X(2).
  05 FILLER PIC 9(2).
`)
	var out bytes.Buffer
	err := EncodeJSON(&out, e, strings.NewReader(`{"A":"AB","FILLER":"XY"}`))
	if err == nil || !strings.Contains(err.Error(), "got 1 FILLER values, want 2") {
		t.Fatalf("error = %v, want FILLER count mismatch", err)
	}
}

func TestEncodeJSONRejectsFillerWithoutFillerFields(t *testing.T) {
	e := mustEncoder(t, `01 R.
  05 A PIC X(2).
`)
	var out bytes.Buffer
	err := EncodeJSON(&out, e, strings.NewReader(`{"A":"AB","FILLER":"XY"}`))
	if err == nil || !strings.Contains(err.Error(), `unknown field "FILLER"`) {
		t.Fatalf("error = %v, want unknown FILLER", err)
	}
}

func TestEncodeUsesPrimaryRedefinesView(t *testing.T) {
	e := mustEncoder(t, `01 R.
  05 BASE PIC X(2).
  05 ALT REDEFINES BASE PIC 9(2).
  05 ALT-2 REDEFINES ALT PIC X(2).
`)
	var got bytes.Buffer
	if err := EncodeJSON(&got, e, strings.NewReader(`{"BASE":"AB","ALT":12,"ALT-2":"CD"}`)); err != nil {
		t.Fatal(err)
	}
	if want := ebc(t, "AB", 2); !bytes.Equal(got.Bytes(), want) {
		t.Fatalf("encoded = % X, want primary bytes % X", got.Bytes(), want)
	}
}

func TestNewEncoderRejectsLongerRedefines(t *testing.T) {
	items, err := copybook.Parse(`01 R.
  05 BASE PIC X(2).
  05 ALT REDEFINES BASE PIC X(3).
`, copybook.FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := layout.Build(items)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewEncoder(recs[0], cp037(t))
	if err == nil || !strings.Contains(err.Error(), "longer than primary") {
		t.Fatalf("NewEncoder() error = %v, want longer redefine error", err)
	}
}

func TestNewEncoderRejectsODOInsideRedefines(t *testing.T) {
	items, err := copybook.Parse(`01 R.
  05 COUNT PIC 9.
  05 BASE PIC X(2).
  05 ALT REDEFINES BASE.
    10 ITEM OCCURS 0 TO 2 DEPENDING ON COUNT PIC X.
`, copybook.FormatAuto)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := layout.Build(items)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewEncoder(recs[0], cp037(t))
	if err == nil || !strings.Contains(err.Error(), "must be on the primary") {
		t.Fatalf("NewEncoder() error = %v, want primary-path ODO error", err)
	}
}

func TestEncodeJSONStrictErrors(t *testing.T) {
	e := mustEncoder(t, `01 R.
  05 A PIC X(2).
  05 N PIC 9(2).
`)
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"missing", `{"A":"AB"}`, "missing field"},
		{"unknown", `{"A":"AB","N":1,"X":2}`, "unknown field"},
		{"null", `{"A":null,"N":1}`, "null is not allowed"},
		{"wrong type", `{"A":2,"N":1}`, "want JSON string"},
		{"duplicate", `{"A":"AB","A":"CD","N":1}`, "record 1: duplicate JSON field"},
		{"trailing", `{"A":"AB","N":1}{}`, "unexpected JSON value"},
		{"array scalar", `[1]`, "want JSON object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := EncodeJSON(&out, e, strings.NewReader(tt.input))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestEncodeJSONRejectsODOCountMismatch(t *testing.T) {
	e := mustEncoder(t, odoBook)
	var out bytes.Buffer
	err := EncodeJSON(&out, e, strings.NewReader(`{"ITEM-COUNT":2,"ITEM":["AAAA"]}`))
	if err == nil || !strings.Contains(err.Error(), "got 1 elements, want 2") {
		t.Fatalf("error = %v, want ODO count mismatch", err)
	}
}

func TestEncodeJSONRejectsShortWrite(t *testing.T) {
	e := mustEncoder(t, `01 R.
  05 A PIC X(2).
`)
	err := EncodeJSON(shortWriter{}, e, strings.NewReader(`{"A":"AB"}`))
	if err == nil || !strings.Contains(err.Error(), "short write") {
		t.Fatalf("error = %v, want short write", err)
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
