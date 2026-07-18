package decode

import (
	"strconv"
	"strings"
	"testing"
)

// --- Codepage -----------------------------------------------------------

func TestCodepageNormalization(t *testing.T) {
	tests := []struct {
		name string
		want *Charmap
	}{
		{"cp037", mustCM(t, "037")},
		{"CP037", mustCM(t, "037")},
		{"037", mustCM(t, "037")},
		{"IBM037", mustCM(t, "037")},
		{"IBM-037", mustCM(t, "037")},
		{"ibm-037", mustCM(t, "037")},
		{"1047", mustCM(t, "1047")},
		{"cp1047", mustCM(t, "1047")},
		{"IBM1047", mustCM(t, "1047")},
		{"1140", mustCM(t, "1140")},
		{"cp1140", mustCM(t, "1140")},
		{"ascii", mustCM(t, "latin1")},
		{"ASCII", mustCM(t, "latin1")},
		{"latin1", mustCM(t, "latin1")},
		{"LATIN1", mustCM(t, "latin1")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cm, err := Codepage(tc.name)
			if err != nil {
				t.Fatalf("Codepage(%q) returned error: %v", tc.name, err)
			}
			if cm != tc.want {
				t.Errorf("Codepage(%q) = %p, want %p", tc.name, cm, tc.want)
			}
		})
	}
}

func TestCodepageUnknown(t *testing.T) {
	for _, name := range []string{"bogus", "cp500", "cp1234", ""} {
		t.Run(name, func(t *testing.T) {
			cm, err := Codepage(name)
			if err == nil {
				t.Fatalf("Codepage(%q) = %v, want error", name, cm)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not mention input name %q", err.Error(), name)
			}
			// error should list supported names
			for _, supported := range []string{"037", "1047", "1140"} {
				if !strings.Contains(err.Error(), supported) {
					t.Errorf("error %q does not list supported name %q", err.Error(), supported)
				}
			}
		})
	}
}

// --- String ---------------------------------------------------------------

func TestString(t *testing.T) {
	cp037 := mustCM(t, "037")

	tests := []struct {
		name string
		b    []byte
		cm   *Charmap
		want string
	}{
		{
			name: "AB1 trailing space trimmed (cp037)",
			b:    []byte{0xC1, 0xC2, 0xF1, 0x40},
			cm:   cp037,
			want: "AB1",
		},
		{
			name: "trailing NUL trimmed (cp037)",
			b:    []byte{0xC1, 0xC2, 0x00, 0x00},
			cm:   cp037,
			want: "AB",
		},
		{
			name: "mixed trailing spaces and NULs",
			b:    []byte{0xC1, 0x40, 0x00, 0x40},
			cm:   cp037,
			want: "A",
		},
		{
			name: "no trimming needed",
			b:    []byte{0xC1, 0xC2, 0xC3},
			cm:   cp037,
			want: "ABC",
		},
		{
			name: "all spaces -> empty",
			b:    []byte{0x40, 0x40, 0x40},
			cm:   cp037,
			want: "",
		},
		{
			name: "ascii round trip",
			b:    []byte("HELLO   "),
			cm:   mustCM(t, "latin1"),
			want: "HELLO",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := String(tc.b, tc.cm)
			if got != tc.want {
				t.Errorf("String(% X) = %q, want %q", tc.b, got, tc.want)
			}
		})
	}
}

func TestDisplayStringSuppressesControls(t *testing.T) {
	cp037 := mustCM(t, "037")
	latin1 := mustCM(t, "latin1")
	tests := []struct {
		name string
		raw  []byte
		cm   *Charmap
		want string
	}{
		{name: "embedded and trailing low values", raw: []byte{0xC1, 0x00, 0xC2, 0x00}, cm: cp037, want: "A·B·"},
		{name: "all low values", raw: []byte{0x00, 0x00, 0x00}, cm: cp037, want: "···"},
		{name: "spaces after low value are trimmed", raw: []byte{0xC1, 0x00, 0x40, 0x40}, cm: cp037, want: "A·"},
		{name: "C0 and C1 controls", raw: []byte{'A', '\n', 0x1b, 0x7f, 0x85, 'B'}, cm: latin1, want: "A····B"},
		{name: "ordinary printable text remains unchanged", raw: []byte{'A', 0xa3, 'B', ' '}, cm: latin1, want: "A£B"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DisplayString(tc.raw, tc.cm); got != tc.want {
				t.Fatalf("DisplayString(% X) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}

	// The strict helper deliberately keeps its established trimming behavior.
	if got := String([]byte{0xC1, 0x00, 0x00}, cp037); got != "A" {
		t.Fatalf("strict String regression: got %q, want A", got)
	}
}

func TestDisplayBytesPreservesWidthAndSuppressesControls(t *testing.T) {
	latin1 := mustCM(t, "latin1")
	if got := DisplayBytes([]byte{'A', 0x00, '\n', ' ', 'B'}, latin1); got != "A·· B" {
		t.Fatalf("DisplayBytes = %q, want visible fixed-width output", got)
	}
	if got := DisplayBytes([]byte{0x00, 0x00}, nil); got != "··" {
		t.Fatalf("DisplayBytes nil charmap = %q", got)
	}
}

// --- EncodeString -----------------------------------------------------------

func TestEncodeString(t *testing.T) {
	cp037 := mustCM(t, "037")

	t.Run("pads with cp037 space", func(t *testing.T) {
		got, err := EncodeString("AB1", 4, cp037)
		if err != nil {
			t.Fatalf("EncodeString: %v", err)
		}
		want := []byte{0xC1, 0xC2, 0xF1, 0x40}
		if string(got) != string(want) {
			t.Errorf("EncodeString = % X, want % X", got, want)
		}
	})

	t.Run("truncates when longer than width", func(t *testing.T) {
		got, err := EncodeString("HELLO", 3, cp037)
		if err != nil {
			t.Fatalf("EncodeString: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("len(got) = %d, want 3", len(got))
		}
		// round trip via String should give "HEL"
		if s := String(got, cp037); s != "HEL" {
			t.Errorf("round trip = %q, want %q", s, "HEL")
		}
	})

	t.Run("exact width, no padding", func(t *testing.T) {
		got, err := EncodeString("ABCD", 4, cp037)
		if err != nil {
			t.Fatalf("EncodeString: %v", err)
		}
		if len(got) != 4 {
			t.Fatalf("len(got) = %d, want 4", len(got))
		}
	})

	t.Run("round trip AB1 through cp037", func(t *testing.T) {
		enc, err := EncodeString("AB1", 4, cp037)
		if err != nil {
			t.Fatalf("EncodeString: %v", err)
		}
		if String(enc, cp037) != "AB1" {
			t.Errorf("round trip = %q, want %q", String(enc, cp037), "AB1")
		}
	})

	t.Run("ascii padding", func(t *testing.T) {
		got, err := EncodeString("HI", 5, mustCM(t, "latin1"))
		if err != nil {
			t.Fatalf("EncodeString: %v", err)
		}
		if string(got) != "HI   " {
			t.Errorf("EncodeString = %q, want %q", got, "HI   ")
		}
	})
}

// --- Zoned ------------------------------------------------------------------

func TestZoned(t *testing.T) {
	tests := []struct {
		name    string
		b       []byte
		scale   int
		signed  bool
		want    string
		wantErr bool
	}{
		{
			name:   "positive unsigned zone (all 0xF)",
			b:      []byte{0xF1, 0xF2, 0xF3},
			scale:  0,
			signed: false,
			want:   "123",
		},
		{
			name:   "positive overpunch 0xC on last byte, signed",
			b:      []byte{0xF1, 0xF2, 0xC3},
			scale:  0,
			signed: true,
			want:   "123",
		},
		{
			name:   "negative overpunch 0xD on last byte, signed",
			b:      []byte{0xF1, 0xF2, 0xD3},
			scale:  0,
			signed: true,
			want:   "-123",
		},
		{
			name:   "negative overpunch but unsigned flag -> positive",
			b:      []byte{0xF1, 0xF2, 0xD3},
			scale:  0,
			signed: false,
			want:   "123",
		},
		{
			name:   "scale applied",
			b:      []byte{0xF1, 0xF2, 0xF3, 0xF4},
			scale:  2,
			signed: false,
			want:   "12.34",
		},
		{
			name:   "negative with scale",
			b:      []byte{0xF1, 0xF2, 0xF3, 0xD4},
			scale:  2,
			signed: true,
			want:   "-12.34",
		},
		{
			name:   "negative zero normalizes to positive",
			b:      []byte{0xF0, 0xD0},
			scale:  0,
			signed: true,
			want:   "0",
		},
		{
			name:   "blank field all EBCDIC spaces -> 0",
			b:      []byte{0x40, 0x40, 0x40},
			scale:  0,
			signed: true,
			want:   "0",
		},
		{
			name:   "blank field all ASCII spaces -> 0",
			b:      []byte{0x20, 0x20, 0x20},
			scale:  0,
			signed: true,
			want:   "0",
		},
		{
			name:   "ASCII zoned positive",
			b:      []byte("123"),
			scale:  0,
			signed: false,
			want:   "123",
		},
		{
			name:   "ASCII zoned with scale",
			b:      []byte("1234"),
			scale:  2,
			signed: false,
			want:   "12.34",
		},
		{
			name:   "scale larger than digit count pads with leading zeros",
			b:      []byte{0xF5},
			scale:  3,
			signed: false,
			want:   "0.005",
		},
		{
			name:    "invalid zone nibble on non-last byte",
			b:       []byte{0x51, 0xF2},
			scale:   0,
			signed:  false,
			wantErr: true,
		},
		{
			name:    "invalid digit nibble",
			b:       []byte{0xFA},
			scale:   0,
			signed:  false,
			wantErr: true,
		},
		{
			name:    "empty field",
			b:       []byte{},
			scale:   0,
			signed:  false,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Zoned(tc.b, tc.scale, tc.signed)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Zoned(% X) = %q, want error", tc.b, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Zoned(% X) unexpected error: %v", tc.b, err)
			}
			if got != tc.want {
				t.Errorf("Zoned(% X, scale=%d, signed=%v) = %q, want %q", tc.b, tc.scale, tc.signed, got, tc.want)
			}
			assertValidJSONNumber(t, got)
		})
	}
}

// --- Packed -----------------------------------------------------------------

func TestPacked(t *testing.T) {
	// 123 packed = nibbles 1,2,3,C (positive sign) = bytes 0x12, 0x3C.
	tests := []struct {
		name    string
		b       []byte
		scale   int
		want    string
		wantErr bool
	}{
		{
			name: "single byte positive +5 (0x5C)",
			b:    []byte{0x5C},
			want: "5",
		},
		{
			name: "single byte positive +5 (0x5F)",
			b:    []byte{0x5F},
			want: "5",
		},
		{
			name: "single byte negative -5 (0x5D)",
			b:    []byte{0x5D},
			want: "-5",
		},
		{
			name: "single byte negative -5 (0x5B)",
			b:    []byte{0x5B},
			want: "-5",
		},
		{
			name: "positive multi-byte 123",
			b:    []byte{0x12, 0x3C},
			want: "123",
		},
		{
			name: "negative multi-byte -123",
			b:    []byte{0x12, 0x3D},
			want: "-123",
		},
		{
			name:  "scale applied 12.34",
			b:     []byte{0x01, 0x23, 0x4C},
			scale: 2,
			want:  "12.34",
		},
		{
			name:  "negative with scale -12.34",
			b:     []byte{0x01, 0x23, 0x4D},
			scale: 2,
			want:  "-12.34",
		},
		{
			name: "negative zero normalizes to positive",
			b:    []byte{0x0D},
			want: "0",
		},
		{
			name:    "invalid digit nibble in leading byte",
			b:       []byte{0xAB, 0xCC},
			wantErr: true,
		},
		{
			name:    "invalid digit nibble in final byte high nibble",
			b:       []byte{0xAC},
			wantErr: true,
		},
		{
			name:    "invalid sign nibble",
			b:       []byte{0x12, 0x39}, // sign nibble 9 invalid
			wantErr: true,
		},
		{
			name:    "empty field",
			b:       []byte{},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Packed(tc.b, tc.scale)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Packed(% X) = %q, want error", tc.b, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Packed(% X) unexpected error: %v", tc.b, err)
			}
			if got != tc.want {
				t.Errorf("Packed(% X, scale=%d) = %q, want %q", tc.b, tc.scale, got, tc.want)
			}
			assertValidJSONNumber(t, got)
		})
	}
}

// --- Binary -------------------------------------------------------------

func TestBinary(t *testing.T) {
	tests := []struct {
		name    string
		b       []byte
		signed  bool
		scale   int
		want    string
		wantErr bool
	}{
		{
			name:   "1 byte unsigned max 255",
			b:      []byte{0xFF},
			signed: false,
			want:   "255",
		},
		{
			name:   "1 byte signed -1",
			b:      []byte{0xFF},
			signed: true,
			want:   "-1",
		},
		{
			name:   "1 byte signed min -128",
			b:      []byte{0x80},
			signed: true,
			want:   "-128",
		},
		{
			name:   "2 byte unsigned max 65535",
			b:      []byte{0xFF, 0xFF},
			signed: false,
			want:   "65535",
		},
		{
			name:   "2 byte signed -1",
			b:      []byte{0xFF, 0xFF},
			signed: true,
			want:   "-1",
		},
		{
			name:   "2 byte signed min -32768",
			b:      []byte{0x80, 0x00},
			signed: true,
			want:   "-32768",
		},
		{
			name:   "4 byte unsigned max 4294967295",
			b:      []byte{0xFF, 0xFF, 0xFF, 0xFF},
			signed: false,
			want:   "4294967295",
		},
		{
			name:   "4 byte signed -1",
			b:      []byte{0xFF, 0xFF, 0xFF, 0xFF},
			signed: true,
			want:   "-1",
		},
		{
			name:   "4 byte signed min",
			b:      []byte{0x80, 0x00, 0x00, 0x00},
			signed: true,
			want:   "-2147483648",
		},
		{
			name:   "8 byte unsigned max",
			b:      []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
			signed: false,
			want:   "18446744073709551615",
		},
		{
			name:   "8 byte signed -1",
			b:      []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
			signed: true,
			want:   "-1",
		},
		{
			name:   "8 byte signed min",
			b:      []byte{0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
			signed: true,
			want:   "-9223372036854775808",
		},
		{
			name:   "4 byte positive value 1000",
			b:      []byte{0x00, 0x00, 0x03, 0xE8},
			signed: true,
			want:   "1000",
		},
		{
			name:   "scale on binary value",
			b:      []byte{0x00, 0x00, 0x03, 0xE8}, // 1000
			signed: true,
			scale:  2,
			want:   "10.00",
		},
		{
			name:   "scale on negative binary value",
			b:      []byte{0xFF, 0xFF, 0xFC, 0x18}, // -1000
			signed: true,
			scale:  2,
			want:   "-10.00",
		},
		{
			name:   "zero unsigned",
			b:      []byte{0x00, 0x00},
			signed: false,
			want:   "0",
		},
		{
			name:   "zero signed",
			b:      []byte{0x00, 0x00},
			signed: true,
			want:   "0",
		},
		{
			name:    "invalid length 0",
			b:       []byte{},
			wantErr: true,
		},
		{
			name:    "invalid length 9",
			b:       make([]byte, 9),
			wantErr: true,
		},
		{
			name:   "3 byte unusual but allowed length",
			b:      []byte{0x00, 0x01, 0x00},
			signed: false,
			want:   "256",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Binary(tc.b, tc.signed, tc.scale)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Binary(% X) = %q, want error", tc.b, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Binary(% X) unexpected error: %v", tc.b, err)
			}
			if got != tc.want {
				t.Errorf("Binary(% X, signed=%v, scale=%d) = %q, want %q", tc.b, tc.signed, tc.scale, got, tc.want)
			}
			assertValidJSONNumber(t, got)
		})
	}
}

// --- Float --------------------------------------------------------------

func TestFloat(t *testing.T) {
	tests := []struct {
		name    string
		b       []byte
		want    string
		wantErr bool
	}{
		{
			name: "float32 1.5",
			b:    []byte{0x3F, 0xC0, 0x00, 0x00},
			want: strconv.FormatFloat(1.5, 'g', -1, 32),
		},
		{
			name: "float32 zero",
			b:    []byte{0x00, 0x00, 0x00, 0x00},
			want: strconv.FormatFloat(0, 'g', -1, 32),
		},
		{
			name: "float32 negative",
			b:    []byte{0xBF, 0xC0, 0x00, 0x00},
			want: strconv.FormatFloat(-1.5, 'g', -1, 32),
		},
		{
			name: "float64 1.5",
			b:    []byte{0x3F, 0xF8, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
			want: strconv.FormatFloat(1.5, 'g', -1, 64),
		},
		{
			name: "float64 pi approx",
			b:    []byte{0x40, 0x09, 0x21, 0xFB, 0x54, 0x44, 0x2D, 0x18},
			want: strconv.FormatFloat(3.141592653589793, 'g', -1, 64),
		},
		{
			name:    "invalid length",
			b:       []byte{0x00, 0x00, 0x00},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Float(tc.b)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Float(% X) = %q, want error", tc.b, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Float(% X) unexpected error: %v", tc.b, err)
			}
			if got != tc.want {
				t.Errorf("Float(% X) = %q, want %q", tc.b, got, tc.want)
			}
		})
	}
}

// --- helpers --------------------------------------------------------------

// assertValidJSONNumber checks that s is a syntactically valid JSON number
// token, per the package contract for formatDecimal output.
func assertValidJSONNumber(t *testing.T, s string) {
	t.Helper()
	if s == "" {
		t.Fatalf("empty string is not a valid JSON number")
	}
	rest := s
	if strings.HasPrefix(rest, "-") {
		rest = rest[1:]
	}
	if rest == "" {
		t.Fatalf("%q is not a valid JSON number", s)
	}
	parts := strings.SplitN(rest, ".", 2)
	intPart := parts[0]
	if intPart == "" {
		t.Fatalf("%q is not a valid JSON number: missing integer part", s)
	}
	if len(intPart) > 1 && intPart[0] == '0' {
		t.Fatalf("%q is not a valid JSON number: leading zero in integer part", s)
	}
	for _, c := range intPart {
		if c < '0' || c > '9' {
			t.Fatalf("%q is not a valid JSON number: non-digit in integer part", s)
		}
	}
	if len(parts) == 2 {
		frac := parts[1]
		if frac == "" {
			t.Fatalf("%q is not a valid JSON number: empty fractional part", s)
		}
		for _, c := range frac {
			if c < '0' || c > '9' {
				t.Fatalf("%q is not a valid JSON number: non-digit in fractional part", s)
			}
		}
	}
	// Also verify strconv can parse it as a float, as a sanity backstop.
	if _, err := strconv.ParseFloat(s, 64); err != nil {
		t.Fatalf("%q failed strconv.ParseFloat: %v", s, err)
	}
}

func mustCM(t *testing.T, name string) *Charmap {
	t.Helper()
	cm, err := Codepage(name)
	if err != nil {
		t.Fatal(err)
	}
	return cm
}

func TestCP277Danish(t *testing.T) {
	cm := mustCM(t, "cp277")
	// Æ Ø Å live where cp037 keeps # @ $; verified against glibc iconv.
	if got := String([]byte{0x7B, 0x7C, 0x5B, 0xC0, 0x6A, 0xD0}, cm); got != "ÆØÅæøå" {
		t.Errorf("String = %q, want ÆØÅæøå", got)
	}
	enc, err := EncodeString("ÅRHUS", 8, cm)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x5B, 0xD9, 0xC8, 0xE4, 0xE2, 0x40, 0x40, 0x40}
	if string(enc) != string(want) {
		t.Errorf("EncodeString(ÅRHUS) = % X, want % X", enc, want)
	}
	if got := String(enc, cm); got != "ÅRHUS" {
		t.Errorf("round trip = %q", got)
	}

	// cp1142 is cp277 with the euro at 0x5A (replacing ¤).
	cm1142 := mustCM(t, "IBM-1142")
	if got := String([]byte{0x5A}, cm1142); got != "€" {
		t.Errorf("cp1142 0x5A = %q, want €", got)
	}
	if got := String([]byte{0x5A}, cm); got != "¤" {
		t.Errorf("cp277 0x5A = %q, want ¤", got)
	}
}
