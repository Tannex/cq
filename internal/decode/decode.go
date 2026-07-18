// Package decode provides pure byte-level decoders for COBOL/EBCDIC data.
//
// The functions in this package operate directly on raw field bytes (as
// they would appear in a fixed-width COBOL record) and convert them to
// Go strings, without any knowledge of copybooks, record layouts, or I/O.
// The package is intentionally self-contained: it depends only on the
// standard library and golang.org/x/text/encoding/charmap.
package decode

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/encoding/charmap"
)

// Charmap is a single-byte codepage: a byte-to-rune table with its
// reverse map for encoding. It replaces golang.org/x/text's *Charmap so
// that codepages that package does not ship (e.g. IBM-277) can be added
// as plain tables.
type Charmap struct {
	name string
	to   [256]rune
	from map[rune]byte
}

// Name returns the canonical name of the codepage, e.g. "cp277".
func (c *Charmap) Name() string { return c.name }

func newCharmap(name string, to [256]rune) *Charmap {
	c := &Charmap{name: name, to: to}
	c.from = make(map[rune]byte, 256)
	for b, r := range to {
		if _, dup := c.from[r]; !dup { // first mapping wins on duplicates
			c.from[r] = byte(b)
		}
	}
	return c
}

// fromXText builds a table by interrogating a golang.org/x/text charmap.
func fromXText(name string, cm *charmap.Charmap) *Charmap {
	var to [256]rune
	for i := 0; i < 256; i++ {
		to[i] = cm.DecodeByte(byte(i))
	}
	return newCharmap(name, to)
}

// codepages maps normalized codepage names to their table.
//
// 037, 1047 and 1140 come from golang.org/x/text/encoding/charmap; 277
// (Denmark/Norway) and 1142 (277 with the euro sign) are shipped as tables
// in tables.go because x/text does not provide them. Other z/OS code pages
// (500, 273, 285, 297, 870, ...) are intentionally not registered so that
// an unknown name produces a clear error rather than silently mapping to
// the wrong table; they can be added the same way as 277.
// latin1 is not true EBCDIC, but useful for testing with plain
// ASCII/Latin-1 fixtures instead of real mainframe extracts.
var latin1 = fromXText("latin1", charmap.ISO8859_1)

var codepages = map[string]*Charmap{
	"037":  fromXText("cp037", charmap.CodePage037),
	"1047": fromXText("cp1047", charmap.CodePage1047),
	"1140": fromXText("cp1140", charmap.CodePage1140),
	"277":  newCharmap("cp277", tableCP277),
	"1142": newCharmap("cp1142", tableCP1142),

	"ascii":  latin1,
	"latin1": latin1,
}

// Codepage maps a user-supplied name to a charmap. Names are matched
// case-insensitively, with optional "cp", "ibm" or "ibm-" prefixes
// stripped before lookup, e.g. "cp037", "037", "IBM037", "cp277",
// "IBM-277", "cp1047", "cp1140", "cp1142" all resolve. "ascii" and
// "latin1" map to ISO 8859-1, which is useful for exercising this package
// with plain-ASCII test fixtures.
//
// An unknown name returns an error listing the supported names.
func Codepage(name string) (*Charmap, error) {
	key := normalizeCodepageName(name)
	if cm, ok := codepages[key]; ok {
		return cm, nil
	}
	names := make([]string, 0, len(codepages))
	for n := range codepages {
		names = append(names, n)
	}
	sort.Strings(names)
	return nil, fmt.Errorf("decode: unknown codepage %q, supported: %s", name, strings.Join(names, ", "))
}

func normalizeCodepageName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.HasPrefix(n, "ibm-"):
		n = n[len("ibm-"):]
	case strings.HasPrefix(n, "ibm"):
		n = n[len("ibm"):]
	case strings.HasPrefix(n, "cp"):
		n = n[len("cp"):]
	}
	return n
}

// String decodes a fixed-width text field to UTF-8 using cm, then trims
// trailing spaces and NUL bytes. cm may not be nil.
func String(b []byte, cm *Charmap) string {
	end := len(b)
	for end > 0 {
		r := cm.to[b[end-1]]
		if r != ' ' && r != '\x00' {
			break
		}
		end--
	}
	var sb strings.Builder
	sb.Grow(end)
	for _, c := range b[:end] {
		sb.WriteRune(cm.to[c])
	}
	return sb.String()
}

// DisplayString decodes a fixed-width text field for an operator display.
// Unlike String, decoded control characters are replaced with U+00B7 MIDDLE
// DOT, including trailing LOW-VALUE bytes. Trailing codepage
// spaces are still trimmed. This helper is intentionally separate so strict
// decoding keeps its existing semantics.
func DisplayString(b []byte, cm *Charmap) string {
	end := len(b)
	for end > 0 && cm.to[b[end-1]] == ' ' {
		end--
	}
	var sb strings.Builder
	sb.Grow(end)
	for _, c := range b[:end] {
		r := cm.to[c]
		if unicode.IsControl(r) {
			sb.WriteRune('·')
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// DisplayBytes decodes a complete raw record without trimming fixed-width
// padding. LOW-VALUE and decoded control characters are rendered visibly so a
// record can never inject terminal control sequences or line breaks.
func DisplayBytes(b []byte, cm *Charmap) string {
	if cm == nil {
		return strings.Repeat("·", len(b))
	}
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		r := cm.to[c]
		if c == 0 || unicode.IsControl(r) {
			sb.WriteRune('·')
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// EncodeString encodes a UTF-8 string to the given charmap, padding with
// the charmap's space character to width. The result is truncated if it
// is longer than width. It is intended as a test and round-trip helper.
// A rune with no mapping in the codepage is an error.
func EncodeString(s string, width int, cm *Charmap) ([]byte, error) {
	enc := make([]byte, 0, width)
	for _, r := range s {
		b, ok := cm.from[r]
		if !ok {
			return nil, fmt.Errorf("decode: EncodeString: %q cannot be encoded in %s", r, cm.name)
		}
		enc = append(enc, b)
	}
	if len(enc) >= width {
		return enc[:width], nil
	}
	padByte, ok := cm.from[' ']
	if !ok {
		padByte = ' '
	}
	out := make([]byte, width)
	copy(out, enc)
	for i := len(enc); i < width; i++ {
		out[i] = padByte
	}
	return out, nil
}

// Zoned decodes an EBCDIC zoned-decimal (USAGE DISPLAY numeric) field.
//
// Each byte holds a zone nibble (high) and a digit nibble (low). For
// EBCDIC data the zone is normally 0xF; the final byte's zone carries the
// sign when overpunched: 0xC or 0xF is positive, 0xD is negative (0xA and
// 0xE are also accepted as positive). If signed is false the value is
// always treated as positive, regardless of the sign nibble present.
//
// Plain ASCII zoned data is also tolerated: if the zone nibble of a byte
// is 0x3 (as in ASCII digit characters '0'-'9' = 0x30-0x39), digits are
// decoded accordingly; an ASCII positive overpunch is zone 0x3.
//
// scale is the number of implied digits after the decimal point (COBOL
// PICTURE V). A blank field (all bytes are space, 0x40 EBCDIC or 0x20
// ASCII) decodes to "0". Any other invalid digit or zone nibble is an
// error.
func Zoned(b []byte, scale int, signed bool) (string, error) {
	if len(b) == 0 {
		return "", fmt.Errorf("decode: Zoned: empty field")
	}

	if isBlank(b) {
		return "0", nil
	}

	digits := make([]byte, len(b))
	negative := false

	for i, c := range b {
		zone := c >> 4
		digit := c & 0x0F

		isLast := i == len(b)-1

		switch {
		case zone == 0xF:
			// standard unsigned EBCDIC zone
		case zone == 0x3:
			// ASCII digit zone
		case isLast && (zone == 0xC || zone == 0xA):
			// positive overpunch
		case isLast && zone == 0xD:
			if signed {
				negative = true
			}
		case isLast && zone == 0xE:
			// positive overpunch
		default:
			return "", fmt.Errorf("decode: Zoned: invalid zone nibble 0x%X at byte %d (0x%02X)", zone, i, c)
		}

		if digit > 9 {
			return "", fmt.Errorf("decode: Zoned: invalid digit nibble 0x%X at byte %d (0x%02X)", digit, i, c)
		}
		digits[i] = '0' + digit
	}

	return formatDecimal(digits, negative, scale), nil
}

func isBlank(b []byte) bool {
	for _, c := range b {
		if c != 0x40 && c != 0x20 {
			return false
		}
	}
	return true
}

// Packed decodes a COMP-3 packed-decimal field: two digits per byte, with
// the final byte holding one digit plus a sign nibble. Sign nibble 0xB or
// 0xD is negative; 0xA, 0xC, 0xE or 0xF is positive. scale is the number
// of implied digits after the decimal point. It is an error if any digit
// nibble is greater than 9 or the sign nibble is not one of the above.
func Packed(b []byte, scale int) (string, error) {
	if len(b) == 0 {
		return "", fmt.Errorf("decode: Packed: empty field")
	}

	digits := make([]byte, 0, len(b)*2-1)
	for i, c := range b[:len(b)-1] {
		hi, lo := c>>4, c&0x0F
		if hi > 9 {
			return "", fmt.Errorf("decode: Packed: invalid digit nibble 0x%X at byte %d (0x%02X)", hi, i, c)
		}
		if lo > 9 {
			return "", fmt.Errorf("decode: Packed: invalid digit nibble 0x%X at byte %d (0x%02X)", lo, i, c)
		}
		digits = append(digits, '0'+hi, '0'+lo)
	}

	i := len(b) - 1
	last := b[i]
	digit, sign := last>>4, last&0x0F
	if digit > 9 {
		return "", fmt.Errorf("decode: Packed: invalid digit nibble 0x%X at byte %d (0x%02X)", digit, i, last)
	}
	digits = append(digits, '0'+digit)
	var negative bool
	switch sign {
	case 0xB, 0xD:
		negative = true
	case 0xA, 0xC, 0xE, 0xF:
	default:
		return "", fmt.Errorf("decode: Packed: invalid sign nibble 0x%X at byte %d (0x%02X)", sign, i, last)
	}
	return formatDecimal(digits, negative, scale), nil
}

// Binary decodes a big-endian integer of len(b) in 1..8 bytes -
// two's-complement if signed, unsigned otherwise - then applies scale
// (implied decimal digits, COBOL PICTURE V) to the result.
func Binary(b []byte, signed bool, scale int) (string, error) {
	n := len(b)
	if n < 1 || n > 8 {
		return "", fmt.Errorf("decode: Binary: length must be 1..8 bytes, got %d", n)
	}

	var uval uint64
	for _, c := range b {
		uval = uval<<8 | uint64(c)
	}

	negative := false
	var digits string

	if signed {
		bits := uint(n * 8)
		signBit := uint64(1) << (bits - 1)
		if uval&signBit != 0 {
			negative = true
			// two's complement negate within n*8 bits
			mask := uint64(1)<<bits - 1
			if bits == 64 {
				mask = ^uint64(0)
			}
			uval = (^uval + 1) & mask
		}
	}
	digits = strconv.FormatUint(uval, 10)
	return formatDecimal([]byte(digits), negative, scale), nil
}

// Float decodes a big-endian IEEE 754 floating point value: len(b)==4
// decodes a float32, len(b)==8 decodes a float64. Any other length is an
// error.
//
// Note: traditional z/OS COMP-1/COMP-2 fields use IBM hexadecimal
// floating point, not IEEE 754. This function decodes IEEE 754, which is
// what modern COBOL compilers emit for FLOAT(IEEE) items (or data staged
// by tools that normalize to IEEE). IBM hex float support may be added
// later.
func Float(b []byte) (string, error) {
	switch len(b) {
	case 4:
		v := math.Float32frombits(binary.BigEndian.Uint32(b))
		return strconv.FormatFloat(float64(v), 'g', -1, 32), nil
	case 8:
		v := math.Float64frombits(binary.BigEndian.Uint64(b))
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	default:
		return "", fmt.Errorf("decode: Float: length must be 4 or 8 bytes, got %d", len(b))
	}
}

// formatDecimal builds the canonical decimal string from an ASCII digit
// byte slice, a sign, and a scale (number of fractional digits).
//
// Leading zeros are stripped but at least one integer digit is kept. If
// scale > 0 a decimal point is inserted with exactly scale fractional
// digits, left-padding with zeros if there are fewer digits than scale.
// A "-" prefix is emitted only when negative and the value is nonzero
// (negative zero is normalized to positive). The result is always a
// valid JSON number token.
func formatDecimal(digits []byte, negative bool, scale int) string {
	// Strip leading zeros from the digit run (but keep at least one).
	start := 0
	for start < len(digits)-1 && digits[start] == '0' {
		start++
	}
	digits = digits[start:]
	if len(digits) == 1 && digits[0] == '0' {
		negative = false
	}

	if scale < 0 {
		scale = 0
	}

	// Left-pad with zeros if we don't have enough digits for the scale
	// plus at least one integer digit.
	if padding := scale + 1 - len(digits); padding > 0 {
		padded := make([]byte, scale+1)
		for i := range padding {
			padded[i] = '0'
		}
		copy(padded[padding:], digits)
		digits = padded
	}

	var intPart, fracPart string
	if scale == 0 {
		intPart = string(digits)
		fracPart = ""
	} else {
		splitAt := len(digits) - scale
		intPart = string(digits[:splitAt])
		fracPart = string(digits[splitAt:])
	}

	var sb strings.Builder
	if negative {
		sb.WriteByte('-')
	}
	sb.WriteString(intPart)
	if scale > 0 {
		sb.WriteByte('.')
		sb.WriteString(fracPart)
	}
	return sb.String()
}
