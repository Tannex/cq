// Package encode provides pure byte-level encoders for COBOL data.
package encode

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"unicode/utf8"

	"github.com/Tannex/cq/internal/decode"
)

// String encodes s in cm and pads it with spaces to width. Values that do
// not fit are rejected rather than truncated.
func String(s string, width int, cm *decode.Charmap) ([]byte, error) {
	if cm == nil {
		return nil, fmt.Errorf("encode: String: nil charmap")
	}
	if width < 0 {
		return nil, fmt.Errorf("encode: String: negative width %d", width)
	}
	if n := utf8.RuneCountInString(s); n > width {
		return nil, fmt.Errorf("encode: String: value is %d characters, field width is %d", n, width)
	}
	b, err := decode.EncodeString(s, width, cm)
	if err != nil {
		return nil, fmt.Errorf("encode: String: %w", err)
	}
	return b, nil
}

// Zoned encodes n as a zoned-decimal DISPLAY field using cm for digit bytes.
// Signed EBCDIC fields use canonical C/D overpunches.
func Zoned(n json.Number, digits, scale int, signed bool, cm *decode.Charmap) ([]byte, error) {
	negative, decimal, err := scaledDecimal(n, digits, scale, signed)
	if err != nil {
		return nil, fmt.Errorf("encode: Zoned: %w", err)
	}
	out, err := String(decimal, digits, cm)
	if err != nil {
		return nil, fmt.Errorf("encode: Zoned: %w", err)
	}
	if signed {
		if negative {
			out[len(out)-1] = 0xD0 | (decimal[len(decimal)-1] - '0')
		} else if cm.Name() != "latin1" {
			out[len(out)-1] = 0xC0 | (decimal[len(decimal)-1] - '0')
		}
	}
	return out, nil
}

// ZonedSeparate encodes n as zoned digits with a dedicated sign character.
// digits is the number of numeric characters and excludes the sign byte.
func ZonedSeparate(n json.Number, digits, scale int, signed, leading bool, cm *decode.Charmap) ([]byte, error) {
	if !signed {
		return Zoned(n, digits, scale, false, cm)
	}
	negative, decimal, err := scaledDecimal(n, digits, scale, true)
	if err != nil {
		return nil, fmt.Errorf("encode: ZonedSeparate: %w", err)
	}
	value, err := String(decimal, digits, cm)
	if err != nil {
		return nil, fmt.Errorf("encode: ZonedSeparate: %w", err)
	}
	sign := "+"
	if negative {
		sign = "-"
	}
	signByte, err := String(sign, 1, cm)
	if err != nil {
		return nil, fmt.Errorf("encode: ZonedSeparate: %w", err)
	}
	out := make([]byte, digits+1)
	if leading {
		out[0] = signByte[0]
		copy(out[1:], value)
	} else {
		copy(out, value)
		out[digits] = signByte[0]
	}
	return out, nil
}

// Packed encodes n as COMP-3. The result uses canonical C/D sign nibbles.
func Packed(n json.Number, digits, scale int, signed bool) ([]byte, error) {
	negative, decimal, err := scaledDecimal(n, digits, scale, signed)
	if err != nil {
		return nil, fmt.Errorf("encode: Packed: %w", err)
	}
	sign := byte(0x0C)
	if negative {
		sign = 0x0D
	}
	nibbles := make([]byte, 0, digits+2)
	if digits%2 == 0 {
		nibbles = append(nibbles, 0)
	}
	for i := range decimal {
		nibbles = append(nibbles, decimal[i]-'0')
	}
	nibbles = append(nibbles, sign)
	out := make([]byte, len(nibbles)/2)
	for i := range out {
		out[i] = nibbles[2*i]<<4 | nibbles[2*i+1]
	}
	return out, nil
}

// Binary encodes n as a 2, 4, or 8 byte big-endian COMP integer, applying
// scale as an implied decimal point and enforcing the PICTURE digit count.
func Binary(n json.Number, length, digits, scale int, signed bool) ([]byte, error) {
	if length != 2 && length != 4 && length != 8 {
		return nil, fmt.Errorf("encode: Binary: length must be 2, 4, or 8 bytes, got %d", length)
	}
	negative, decimal, err := scaledDecimal(n, digits, scale, signed)
	if err != nil {
		return nil, fmt.Errorf("encode: Binary: %w", err)
	}
	value := new(big.Int)
	value.SetString(decimal, 10)
	if negative {
		value.Neg(value)
	}
	bits := uint(length * 8)
	if signed {
		min := new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), bits-1))
		max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), bits-1), big.NewInt(1))
		if value.Cmp(min) < 0 || value.Cmp(max) > 0 {
			return nil, fmt.Errorf("encode: Binary: %s is outside signed %d-bit range", n, bits)
		}
		if value.Sign() < 0 {
			value.Add(value, new(big.Int).Lsh(big.NewInt(1), bits))
		}
	} else {
		max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), bits), big.NewInt(1))
		if value.Sign() < 0 || value.Cmp(max) > 0 {
			return nil, fmt.Errorf("encode: Binary: %s is outside unsigned %d-bit range", n, bits)
		}
	}
	out := make([]byte, length)
	value.FillBytes(out)
	return out, nil
}

// Float encodes n as a 4 or 8 byte big-endian IEEE 754 value.
func Float(n json.Number, length int) ([]byte, error) {
	switch length {
	case 4:
		v, err := strconv.ParseFloat(n.String(), 32)
		if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			return nil, fmt.Errorf("encode: Float: invalid float32 %q", n)
		}
		out := make([]byte, 4)
		binary.BigEndian.PutUint32(out, math.Float32bits(float32(v)))
		return out, nil
	case 8:
		v, err := strconv.ParseFloat(n.String(), 64)
		if err != nil || math.IsInf(v, 0) || math.IsNaN(v) {
			return nil, fmt.Errorf("encode: Float: invalid float64 %q", n)
		}
		out := make([]byte, 8)
		binary.BigEndian.PutUint64(out, math.Float64bits(v))
		return out, nil
	default:
		return nil, fmt.Errorf("encode: Float: length must be 4 or 8 bytes, got %d", length)
	}
}

// scaledDecimal converts JSON decimal syntax directly to a zero-padded
// unscaled integer. It never passes through floating point.
func scaledDecimal(n json.Number, width, scale int, signed bool) (bool, string, error) {
	if width <= 0 {
		return false, "", fmt.Errorf("digit width must be positive, got %d", width)
	}
	if scale < 0 {
		return false, "", fmt.Errorf("scale must be non-negative, got %d", scale)
	}
	s := n.String()
	if s == "" {
		return false, "", fmt.Errorf("empty number")
	}
	i := 0
	negative := false
	if s[i] == '-' {
		negative = true
		i++
		if i == len(s) {
			return false, "", fmt.Errorf("invalid JSON number %q", s)
		}
	}
	intStart := i
	if s[i] == '0' {
		i++
		if i < len(s) && s[i] >= '0' && s[i] <= '9' {
			return false, "", fmt.Errorf("invalid JSON number %q", s)
		}
	} else if s[i] >= '1' && s[i] <= '9' {
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	} else {
		return false, "", fmt.Errorf("invalid JSON number %q", s)
	}
	intPart := s[intStart:i]
	fracPart := ""
	if i < len(s) && s[i] == '.' {
		i++
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if start == i {
			return false, "", fmt.Errorf("invalid JSON number %q", s)
		}
		fracPart = s[start:i]
	}
	exponent := 0
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		expNegative := false
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			expNegative = s[i] == '-'
			i++
		}
		start := i
		limit := width + len(fracPart) + 1
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			if exponent <= limit {
				exponent = exponent*10 + int(s[i]-'0')
			}
			i++
		}
		if start == i {
			return false, "", fmt.Errorf("invalid JSON number %q", s)
		}
		if exponent > limit {
			exponent = limit + 1
		}
		if expNegative {
			exponent = -exponent
		}
	}
	if i != len(s) {
		return false, "", fmt.Errorf("invalid JSON number %q", s)
	}

	coefficient := intPart + fracPart
	first := 0
	for first < len(coefficient) && coefficient[first] == '0' {
		first++
	}
	if first == len(coefficient) {
		return false, zeroes(width), nil
	}
	coefficient = coefficient[first:]
	shift := scale - len(fracPart) + exponent
	if shift < 0 {
		cut := -shift
		if cut >= len(coefficient) {
			return false, "", fmt.Errorf("%s has precision beyond scale %d", n, scale)
		}
		for _, c := range coefficient[len(coefficient)-cut:] {
			if c != '0' {
				return false, "", fmt.Errorf("%s has precision beyond scale %d", n, scale)
			}
		}
		coefficient = coefficient[:len(coefficient)-cut]
	} else if len(coefficient)+shift > width {
		return false, "", fmt.Errorf("%s exceeds %d digits", n, width)
	} else {
		coefficient += zeroes(shift)
	}
	if len(coefficient) > width {
		return false, "", fmt.Errorf("%s exceeds %d digits", n, width)
	}
	if negative && !signed {
		return false, "", fmt.Errorf("negative value %s for unsigned field", n)
	}
	return negative, zeroes(width-len(coefficient)) + coefficient, nil
}

func zeroes(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = '0'
	}
	return string(b)
}
