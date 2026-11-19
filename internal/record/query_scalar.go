package record

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/Tannex/cq/internal/decode"
	"github.com/Tannex/cq/internal/layout"
)

// Sorting and deduplicating whole records compares one differing member per
// record pair, millions of times. Decoding both sides into fresh strings and
// json.Numbers, which the jq engine then parses again, dominates that work;
// CompareScalar orders the raw bytes instead, for the field kinds where it can
// reproduce the engine's order exactly.
//
// The engine's order (gojq Compare) is: numbers before strings; numbers by
// value after parsing the token, integers exactly and tokens with a fraction
// as float64; strings by UTF-8 bytes, which is code point order.
//
// Reading one field is the other hot path, and the same digit readers let
// queryScalar build a number token with one allocation where decode.Zoned,
// decode.Packed and decode.Binary take several.

// scalarOrder is how CompareScalar orders a scalar field, and which reader
// queryScalar uses.
type scalarOrder uint8

const (
	orderDecode        scalarOrder = iota // no shortcut: the caller decodes and compares
	orderText                             // DisplayString
	orderZoned                            // zoned decimal with an overpunched or no sign
	orderZonedSeparate                    // zoned decimal with a separate sign character
	orderPacked                           // packed decimal
	orderBinary                           // big-endian binary integer
)

// scalarOrderOf picks the shortcut for a field. Floating point decodes to the
// shortest token that round-trips, which only parsing reproduces, so it
// keeps decoding.
func scalarOrderOf(f *layout.Field) scalarOrder {
	switch f.Kind {
	case layout.KindText, layout.KindEdited:
		return orderText
	case layout.KindZoned:
		if f.SignSeparate {
			return orderZonedSeparate
		}
		return orderZoned
	case layout.KindPacked:
		return orderPacked
	case layout.KindBinary:
		return orderBinary
	}
	return orderDecode
}

// numberState classifies one side of a numeric comparison.
type numberState uint8

const (
	numberValid   numberState = iota // decodes to the number read
	numberInvalid                    // decodes to a placeholder string
	numberUnknown                    // outside what the shortcut reads; decode it
)

// maxDigits bounds the digits a numeric shortcut reads into its buffer.
const maxDigits = 64

// pow10 holds the powers of ten a float64 represents exactly.
var pow10 = [...]float64{1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10, 1e11,
	1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19, 1e20, 1e21, 1e22}

// CompareScalar orders one occurrence of a scalar member taken from two
// checked records, given the raw bytes of each, exactly as the jq engine
// orders the values Element decodes from them. ok is false when the field
// kind or the values have no shortcut; the caller then decodes both.
func (v *QueryView) CompareScalar(s *Shape, a, b []byte) (result int, ok bool) {
	switch s.kind {
	case orderText:
		return decode.CompareDisplay(a, b, v.d.CM), true
	case orderBinary:
		magA, negA, stateA := binaryValue(s.Field, a)
		magB, negB, stateB := binaryValue(s.Field, b)
		if stateA != numberValid || stateB != numberValid {
			return compareStates(stateA, stateB)
		}
		if scale := max(s.Field.Scale, 0); scale > 0 {
			return compareFloats(magA, negA, magB, negB, scale)
		}
		return compareSigned(negA, negB, cmpUint(magA, magB)), true
	case orderZoned, orderZonedSeparate, orderPacked:
		var bufA, bufB [maxDigits]byte
		digitsA, negA, _, stateA := v.d.decimalDigits(s, a, bufA[:0])
		digitsB, negB, _, stateB := v.d.decimalDigits(s, b, bufB[:0])
		if stateA != numberValid || stateB != numberValid {
			return compareStates(stateA, stateB)
		}
		if scale := max(s.Field.Scale, 0); scale > 0 {
			magA, okA := mantissa(digitsA)
			magB, okB := mantissa(digitsB)
			if !okA || !okB {
				return 0, false
			}
			return compareFloats(magA, negA, magB, negB, scale)
		}
		// Integer tokens compare exactly whatever their size (the engine
		// uses int or big.Int), and both sides have as many digits as the
		// field holds, leading zeros included.
		return compareSigned(negA, negB, bytes.Compare(digitsA, digitsB)), true
	}
	return 0, false
}

// compareStates orders two sides of which at least one is not a number to
// compare: a number orders before a placeholder string, and two placeholder
// strings, or a side the shortcut cannot read, need decoding.
func compareStates(a, b numberState) (int, bool) {
	switch {
	case a == numberUnknown || b == numberUnknown, a == b:
		return 0, false
	case a == numberValid:
		return -1, true
	default:
		return 1, true
	}
}

// compareSigned orders two numbers from their signs and the order of their
// magnitudes. Zero is never negative: the decoders normalize -0.
func compareSigned(negA, negB bool, magnitudes int) int {
	switch {
	case negA && !negB:
		return -1
	case !negA && negB:
		return 1
	case negA:
		return -magnitudes
	default:
		return magnitudes
	}
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// compareFloats orders two decimals of scale digits, mag/10^scale, as the
// engine does: as the float64 that parsing their tokens yields. A mantissa
// below 2^53 and a power of ten up to 1e22 are exact in float64, so the
// quotient is the correctly rounded value of the decimal — the one value
// strconv.ParseFloat returns. Rounding can make distinct decimals equal,
// which is why the floats, not the decimals, are compared.
func compareFloats(magA uint64, negA bool, magB uint64, negB bool, scale int) (int, bool) {
	const exact = 1 << 53
	if scale >= len(pow10) || magA >= exact || magB >= exact {
		return 0, false
	}
	fa, fb := float64(magA)/pow10[scale], float64(magB)/pow10[scale]
	if negA {
		fa = -fa
	}
	if negB {
		fb = -fb
	}
	switch {
	case fa < fb:
		return -1, true
	case fa == fb:
		return 0, true
	default:
		return 1, true
	}
}

// mantissa returns the integer the digits spell, if it fits in a uint64.
func mantissa(digits []byte) (uint64, bool) {
	for len(digits) > 0 && digits[0] == 0 {
		digits = digits[1:]
	}
	if len(digits) > 19 {
		return 0, false
	}
	var m uint64
	for _, digit := range digits {
		m = m*10 + uint64(digit)
	}
	return m, true
}

// decimalDigits validates a zoned or packed field as decode.Zoned,
// decode.Packed and the separate-sign rules in Decoder.scalar and
// queryScalar do, and returns its digit values (0-9) with leading zeros, so
// two values of one field have equally many. blank reports a zoned field of
// spaces, which decodes to "0" whatever its scale; as a number that equals
// the zero its digits spell.
func (d *Decoder) decimalDigits(s *Shape, raw []byte, buf []byte) (digits []byte, negative, blank bool, state numberState) {
	f := s.Field
	switch s.kind {
	case orderZoned:
		return zonedDigits(raw, f.Signed, buf)
	case orderZonedSeparate:
		if len(raw) < 2 {
			return nil, false, false, numberInvalid
		}
		sign := separateSignByte(f, raw)
		if sign == 0 {
			return nil, false, false, numberInvalid // LOW-VALUE sign, see queryScalar
		}
		body := raw[1:]
		if !f.SignLeading {
			body = raw[:len(raw)-1]
		}
		digits, _, blank, state = zonedDigits(body, false, buf)
		if state != numberValid {
			return nil, false, false, state
		}
		// decode.String trims a space or NUL, leaving no sign.
		switch d.CM.Rune(sign) {
		case '-':
			negative = !allZero(digits)
		case '+', ' ', 0:
		default:
			return nil, false, false, numberInvalid
		}
		return digits, negative, blank, numberValid
	default:
		digits, negative, state = packedDigits(raw, buf)
		return digits, negative, false, state
	}
}

func zonedDigits(raw []byte, signed bool, buf []byte) (digits []byte, negative, blank bool, state numberState) {
	if len(raw) == 0 {
		return nil, false, false, numberInvalid
	}
	if len(raw) > maxDigits {
		return nil, false, false, numberUnknown
	}
	blank = true
	for _, c := range raw {
		if c != 0x40 && c != 0x20 {
			blank = false
			break
		}
	}
	if blank {
		for range raw {
			buf = append(buf, 0)
		}
		return buf, false, true, numberValid
	}
	for i, c := range raw {
		zone, digit := c>>4, c&0x0F
		last := i == len(raw)-1
		switch {
		case zone == 0xF, zone == 0x3:
		case last && (zone == 0xC || zone == 0xA || zone == 0xE):
		case last && zone == 0xD:
			negative = signed
		default:
			return nil, false, false, numberInvalid
		}
		if digit > 9 {
			return nil, false, false, numberInvalid
		}
		buf = append(buf, digit)
	}
	return buf, negative && !allZero(buf), false, numberValid
}

// decimalToken formats digit values as formatDecimal in package decode
// does: leading zeros dropped but one integer digit kept, scale fraction
// digits, and a sign only on a nonzero value, which negative already
// implies. It allocates the token only.
func decimalToken(digits []byte, negative bool, scale int) json.Number {
	for len(digits) > 1 && digits[0] == 0 {
		digits = digits[1:]
	}
	scale = max(scale, 0)
	width := max(len(digits), scale+1) // integer digits padded to one
	var buf [2 * maxDigits]byte
	token := buf[:0]
	if width+2 > len(buf) {
		token = make([]byte, 0, width+2)
	}
	if negative {
		token = append(token, '-')
	}
	for i := range width {
		if scale > 0 && i == width-scale {
			token = append(token, '.')
		}
		digit := byte(0)
		if j := i - (width - len(digits)); j >= 0 {
			digit = digits[j]
		}
		token = append(token, '0'+digit)
	}
	return json.Number(token)
}

// numberToken decodes a zoned, packed or binary field to the value
// queryScalar returns, if the readers above cover it.
func (d *Decoder) numberToken(s *Shape, raw []byte) (value any, ok bool) {
	switch s.kind {
	case orderZoned, orderZonedSeparate, orderPacked:
		var buf [maxDigits]byte
		digits, negative, blank, state := d.decimalDigits(s, raw, buf[:0])
		switch {
		case state == numberInvalid:
			return d.invalidScalarDisplay(s.Field, raw), true
		case state != numberValid:
			return nil, false
		case blank:
			return json.Number("0"), true
		default:
			return decimalToken(digits, negative, s.Field.Scale), true
		}
	case orderBinary:
		magnitude, negative, state := binaryValue(s.Field, raw)
		if state != numberValid {
			return d.invalidScalarDisplay(s.Field, raw), true
		}
		var buf [20]byte
		digits := strconv.AppendUint(buf[:0], magnitude, 10)
		for i := range digits {
			digits[i] -= '0'
		}
		return decimalToken(digits, negative, s.Field.Scale), true
	}
	return nil, false
}

func packedDigits(raw []byte, buf []byte) ([]byte, bool, numberState) {
	if len(raw) == 0 {
		return nil, false, numberInvalid
	}
	if 2*len(raw)-1 > maxDigits {
		return nil, false, numberUnknown
	}
	for _, c := range raw[:len(raw)-1] {
		hi, lo := c>>4, c&0x0F
		if hi > 9 || lo > 9 {
			return nil, false, numberInvalid
		}
		buf = append(buf, hi, lo)
	}
	last := raw[len(raw)-1]
	digit, sign := last>>4, last&0x0F
	if digit > 9 {
		return nil, false, numberInvalid
	}
	buf = append(buf, digit)
	negative := false
	switch sign {
	case 0xB, 0xD:
		negative = true
	case 0xA, 0xC, 0xE, 0xF:
	default:
		return nil, false, numberInvalid
	}
	return buf, negative && !allZero(buf), numberValid
}

// binaryValue reads a binary field as decode.Binary does: its magnitude and
// sign.
func binaryValue(f *layout.Field, raw []byte) (magnitude uint64, negative bool, state numberState) {
	n := len(raw)
	if n < 1 || n > 8 {
		return 0, false, numberInvalid
	}
	var u uint64
	for _, c := range raw {
		u = u<<8 | uint64(c)
	}
	if f.Signed {
		bits := uint(n * 8)
		if u&(1<<(bits-1)) != 0 {
			mask := uint64(1)<<bits - 1
			if bits == 64 {
				mask = ^uint64(0)
			}
			return (^u + 1) & mask, true, numberValid
		}
	}
	return u, false, numberValid
}

func allZero(digits []byte) bool {
	for _, digit := range digits {
		if digit != 0 {
			return false
		}
	}
	return true
}
