// Package decode provides pure byte-level decoders for COBOL/EBCDIC data.
//
// The functions in this package operate directly on raw field bytes (as
// they would appear in a fixed-width COBOL record) and convert them to
// Go strings, without any knowledge of copybooks, record layouts, or I/O.
// The package is intentionally self-contained: it depends only on the
// standard library and golang.org/x/text/encoding/charmap.
package decode

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/charmap"
)

// codepages maps normalized codepage names to their charmap.
//
// golang.org/x/text/encoding/charmap only ships a small subset of the IBM
// EBCDIC code pages as prebuilt tables: CodePage037, CodePage1047 and
// CodePage1140. Common z/OS code pages such as 500, 273, 285, 297, 870 and
// 1141-1149 are not provided by that package and therefore cannot be
// supported here without shipping custom translation tables. Names for
// those code pages are intentionally not registered so that an unknown
// name produces a clear error rather than silently mapping to the wrong
// table.
var codepages = map[string]*charmap.Charmap{
	"037":  charmap.CodePage037,
	"1047": charmap.CodePage1047,
	"1140": charmap.CodePage1140,

	// Not true EBCDIC, but useful for testing with plain ASCII/Latin-1
	// fixtures instead of real mainframe extracts.
	"ascii":  charmap.ISO8859_1,
	"latin1": charmap.ISO8859_1,
}

// Codepage maps a user-supplied name to a charmap. Names are matched
// case-insensitively, with optional "cp", "ibm" or "ibm-" prefixes
// stripped before lookup, e.g. "cp037", "037", "IBM037", "cp1047",
// "IBM-1047", "cp1140" all resolve. "ascii" and "latin1" map to
// charmap.ISO8859_1, which is useful for exercising this package with
// plain-ASCII test fixtures.
//
// Only the EBCDIC code pages actually shipped by
// golang.org/x/text/encoding/charmap are supported: 037, 1047 and 1140.
// An unknown name returns an error listing the supported names.
func Codepage(name string) (*charmap.Charmap, error) {
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
func String(b []byte, cm *charmap.Charmap) string {
	s, err := cm.NewDecoder().Bytes(b)
	if err != nil {
		// charmap decoders do not return errors for byte-oriented
		// Charmap tables (unmapped bytes become U+FFFD), but guard
		// against future/alternate encodings defensively.
		s = b
	}
	return strings.TrimRight(string(s), " \x00")
}

// EncodeString encodes a UTF-8 string to the given charmap, padding with
// the charmap's space character (encode " ") to width. The result is
// truncated if it is longer than width. It is intended as a test and
// round-trip helper.
func EncodeString(s string, width int, cm *charmap.Charmap) ([]byte, error) {
	enc, err := cm.NewEncoder().Bytes([]byte(s))
	if err != nil {
		return nil, fmt.Errorf("decode: EncodeString: %w", err)
	}
	if len(enc) >= width {
		return enc[:width], nil
	}
	sp, err := cm.NewEncoder().Bytes([]byte(" "))
	if err != nil {
		return nil, fmt.Errorf("decode: EncodeString: encode pad space: %w", err)
	}
	padByte := byte(' ')
	if len(sp) == 1 {
		padByte = sp[0]
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

	digits := make([]byte, 0, len(b)*2)
	for i, c := range b {
		hi := c >> 4
		lo := c & 0x0F

		if i == len(b)-1 {
			if hi > 9 {
				return "", fmt.Errorf("decode: Packed: invalid digit nibble 0x%X at byte %d (0x%02X)", hi, i, c)
			}
			digits = append(digits, '0'+hi)

			var negative bool
			switch lo {
			case 0xB, 0xD:
				negative = true
			case 0xA, 0xC, 0xE, 0xF:
				negative = false
			default:
				return "", fmt.Errorf("decode: Packed: invalid sign nibble 0x%X at byte %d (0x%02X)", lo, i, c)
			}
			return formatDecimal(digits, negative, scale), nil
		}

		if hi > 9 {
			return "", fmt.Errorf("decode: Packed: invalid digit nibble 0x%X at byte %d (0x%02X)", hi, i, c)
		}
		if lo > 9 {
			return "", fmt.Errorf("decode: Packed: invalid digit nibble 0x%X at byte %d (0x%02X)", lo, i, c)
		}
		digits = append(digits, '0'+hi, '0'+lo)
	}

	// unreachable: loop always returns on the last byte
	return "", fmt.Errorf("decode: Packed: internal error")
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

	if uval == 0 {
		negative = false
	}

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
		bits := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
		v := math.Float32frombits(bits)
		return strconv.FormatFloat(float64(v), 'g', -1, 32), nil
	case 8:
		bits := uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
			uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
		v := math.Float64frombits(bits)
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

	if scale < 0 {
		scale = 0
	}

	// Left-pad with zeros if we don't have enough digits for the scale
	// plus at least one integer digit.
	for len(digits) < scale+1 {
		digits = append([]byte{'0'}, digits...)
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

	// Determine if the value is zero (all digits are '0').
	isZero := true
	for _, d := range digits {
		if d != '0' {
			isZero = false
			break
		}
	}
	if isZero {
		negative = false
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
