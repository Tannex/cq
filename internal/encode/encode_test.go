package encode

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"github.com/Tannex/cq/internal/decode"
)

func cp037(t *testing.T) *decode.Charmap {
	t.Helper()
	cm, err := decode.Codepage("037")
	if err != nil {
		t.Fatal(err)
	}
	return cm
}

func TestString(t *testing.T) {
	got, err := String("AB", 4, cp037(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0xC1, 0xC2, 0x40, 0x40}
	if !bytes.Equal(got, want) {
		t.Fatalf("String bytes = % X, want % X", got, want)
	}
	for _, tc := range []struct {
		name  string
		value string
		width int
		cm    *decode.Charmap
	}{
		{"overlong", "ABC", 2, cp037(t)},
		{"unencodable", "🙂", 1, cp037(t)},
		{"negative width", "", -1, cp037(t)},
		{"nil charmap", "", 1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := String(tc.value, tc.width, tc.cm); err == nil {
				t.Fatal("String returned nil error")
			}
		})
	}
}

func TestZoned(t *testing.T) {
	cm := cp037(t)
	tests := []struct {
		name   string
		n      json.Number
		digits int
		scale  int
		signed bool
		want   []byte
	}{
		{"positive signed", "12.30", 4, 2, true, []byte{0xF1, 0xF2, 0xF3, 0xC0}},
		{"negative signed", "-12.30", 4, 2, true, []byte{0xF1, 0xF2, 0xF3, 0xD0}},
		{"unsigned", "42", 3, 0, false, []byte{0xF0, 0xF4, 0xF2}},
		{"exact exponent", "123e-1", 4, 2, true, []byte{0xF1, 0xF2, 0xF3, 0xC0}},
		{"long exponent pair", "1000000000000e-12", 4, 0, false, []byte{0xF0, 0xF0, 0xF0, 0xF1}},
		{"negative zero", "-0.00", 3, 2, false, []byte{0xF0, 0xF0, 0xF0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Zoned(tc.n, tc.digits, tc.scale, tc.signed, cm)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("Zoned bytes = % X, want % X", got, tc.want)
			}
		})
	}
}

func TestZonedASCII(t *testing.T) {
	cm, err := decode.Codepage("latin1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := Zoned("42", 3, 0, false, cm)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte("042"); !bytes.Equal(got, want) {
		t.Fatalf("ASCII Zoned bytes = % X, want % X", got, want)
	}
	got, err = ZonedSeparate("-42", 3, 0, true, false, cm)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte("042-"); !bytes.Equal(got, want) {
		t.Fatalf("ASCII ZonedSeparate bytes = % X, want % X", got, want)
	}
}

func TestZonedSeparate(t *testing.T) {
	cm := cp037(t)
	leading, err := ZonedSeparate("-12.3", 4, 1, true, true, cm)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0x60, 0xF0, 0xF1, 0xF2, 0xF3}; !bytes.Equal(leading, want) {
		t.Fatalf("leading bytes = % X, want % X", leading, want)
	}
	trailing, err := ZonedSeparate("12.3", 4, 1, true, false, cm)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0xF0, 0xF1, 0xF2, 0xF3, 0x4E}; !bytes.Equal(trailing, want) {
		t.Fatalf("trailing bytes = % X, want % X", trailing, want)
	}
	unsigned, err := ZonedSeparate("12", 3, 0, false, true, cm)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0xF0, 0xF1, 0xF2}; !bytes.Equal(unsigned, want) {
		t.Fatalf("unsigned bytes = % X, want % X", unsigned, want)
	}
}

func TestPacked(t *testing.T) {
	tests := []struct {
		name   string
		n      json.Number
		digits int
		scale  int
		signed bool
		want   []byte
	}{
		{"odd positive", "123.45", 5, 2, true, []byte{0x12, 0x34, 0x5C}},
		{"odd negative", "-123.45", 5, 2, true, []byte{0x12, 0x34, 0x5D}},
		{"even leading pad unsigned", "12", 4, 0, false, []byte{0x00, 0x01, 0x2F}},
		{"even leading pad signed", "12", 4, 0, true, []byte{0x00, 0x01, 0x2C}},
		{"even picture pad nibble digit", "12345", 4, 0, false, []byte{0x12, 0x34, 0x5F}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Packed(tc.n, tc.digits, tc.scale, tc.signed)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("Packed bytes = % X, want % X", got, tc.want)
			}
		})
	}
}

func TestBinary(t *testing.T) {
	tests := []struct {
		name   string
		n      json.Number
		length int
		scale  int
		signed bool
		want   []byte
	}{
		{"signed positive", "32767", 2, 0, true, []byte{0x7F, 0xFF}},
		{"signed negative", "-32768", 2, 0, true, []byte{0x80, 0x00}},
		{"unsigned maximum", "65535", 2, 0, false, []byte{0xFF, 0xFF}},
		{"scaled exponent", "123e-2", 2, 2, true, []byte{0x00, 0x7B}},
		{"signed 32 bit", "-1", 4, 0, true, []byte{0xFF, 0xFF, 0xFF, 0xFF}},
		{"signed 64 bit minimum", "-9223372036854775808", 8, 0, true, []byte{0x80, 0, 0, 0, 0, 0, 0, 0}},
		{"unsigned 64 bit maximum", "18446744073709551615", 8, 0, false, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Binary(tc.n, tc.length, tc.scale, tc.signed)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("Binary bytes = % X, want % X", got, tc.want)
			}
		})
	}
}

func TestFloat(t *testing.T) {
	got32, err := Float("1.5", 4)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0x3F, 0xC0, 0, 0}; !bytes.Equal(got32, want) {
		t.Fatalf("Float32 bytes = % X, want % X", got32, want)
	}
	got64, err := Float("-1.5", 8)
	if err != nil {
		t.Fatal(err)
	}
	wantBits := math.Float64bits(-1.5)
	want64 := []byte{byte(wantBits >> 56), byte(wantBits >> 48), byte(wantBits >> 40), byte(wantBits >> 32), byte(wantBits >> 24), byte(wantBits >> 16), byte(wantBits >> 8), byte(wantBits)}
	if !bytes.Equal(got64, want64) {
		t.Fatalf("Float64 bytes = % X, want % X", got64, want64)
	}
}

func TestNumericErrors(t *testing.T) {
	tests := []struct {
		name string
		fn   func() error
	}{
		{"precision loss", func() error { _, err := Zoned("1.234", 4, 2, true, cp037(t)); return err }},
		{"huge negative exponent", func() error { _, err := Zoned("10000000e-100", 5, 0, false, cp037(t)); return err }},
		{"huge positive exponent", func() error { _, err := Zoned("1e100000", 5, 0, false, cp037(t)); return err }},
		{"zoned overflow", func() error { _, err := Zoned("1000", 3, 0, true, cp037(t)); return err }},
		{"unsigned negative", func() error { _, err := Packed("-1", 3, 0, false); return err }},
		{"invalid JSON number", func() error { _, err := Packed("01", 3, 0, true); return err }},
		{"packed odd picture overflow", func() error { _, err := Packed("123456", 5, 0, true); return err }},
		{"packed even storage overflow", func() error { _, err := Packed("123456", 4, 0, true); return err }},
		{"binary signed overflow", func() error { _, err := Binary("32768", 2, 0, true); return err }},
		{"binary unsigned overflow", func() error { _, err := Binary("65536", 2, 0, false); return err }},
		{"binary negative unsigned", func() error { _, err := Binary("-1", 2, 0, false); return err }},
		{"binary bad length", func() error { _, err := Binary("1", 3, 0, true); return err }},
		{"float overflow", func() error { _, err := Float("1e100", 4); return err }},
		{"float bad token", func() error { _, err := Float("NaN", 8); return err }},
		{"float bad length", func() error { _, err := Float("1", 2); return err }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err == nil {
				t.Fatal("got nil error")
			}
		})
	}
}
