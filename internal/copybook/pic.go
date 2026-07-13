package copybook

import (
	"fmt"
	"strconv"
	"strings"
)

// expandPic expands repeat factors: "9(3)V99" -> "999V99".
// CR and DB are kept as single symbols 'C' and 'D' occupying two positions.
func expandPic(raw string) (string, error) {
	up := strings.ToUpper(raw)
	var out []byte
	i := 0
	for i < len(up) {
		c := up[i]
		if c == '(' {
			if len(out) == 0 {
				return "", fmt.Errorf("picture %q: repeat with no preceding symbol", raw)
			}
			j := strings.IndexByte(up[i:], ')')
			if j < 0 {
				return "", fmt.Errorf("picture %q: unclosed parenthesis", raw)
			}
			n, err := strconv.Atoi(up[i+1 : i+j])
			if err != nil || n < 1 {
				return "", fmt.Errorf("picture %q: bad repeat count %q", raw, up[i+1:i+j])
			}
			sym := out[len(out)-1]
			for k := 1; k < n; k++ {
				out = append(out, sym)
			}
			i += j + 1
			continue
		}
		if c == 'C' && i+1 < len(up) && up[i+1] == 'R' {
			out = append(out, 'C')
			i += 2
			continue
		}
		if c == 'D' && i+1 < len(up) && up[i+1] == 'B' {
			out = append(out, 'D')
			i += 2
			continue
		}
		out = append(out, c)
		i++
	}
	return string(out), nil
}

// parsePic analyses a PICTURE string.
func parsePic(raw string) (*Picture, error) {
	exp, err := expandPic(raw)
	if err != nil {
		return nil, err
	}
	p := &Picture{Raw: raw}
	seenV := false
	counts := map[byte]int{}
	for i := 0; i < len(exp); i++ {
		c := exp[i]
		counts[c]++
		switch c {
		case 'S':
			if i != 0 {
				return nil, fmt.Errorf("picture %q: S must be leading", raw)
			}
			p.Signed = true
		case 'V':
			if seenV {
				return nil, fmt.Errorf("picture %q: multiple V", raw)
			}
			seenV = true
		case '9':
			p.Digits++
			if seenV {
				p.Scale++
			}
		case 'P':
			// Scaling positions: after V (or after digits) they extend the
			// fraction; before any 9 they scale the integer part negatively.
			if seenV || counts['9'] > 0 {
				p.Scale++
			} else {
				p.Scale--
			}
		case 'X', 'A', 'Z', '*', 'B', '0', '/', '.', ',', '+', '-', '$', 'C', 'D', 'E', 'N', 'G':
			// width handled below
		default:
			return nil, fmt.Errorf("picture %q: unsupported symbol %q", raw, string(c))
		}
	}

	// Storage width for USAGE DISPLAY: every position except S, V, P.
	for i := 0; i < len(exp); i++ {
		switch exp[i] {
		case 'S', 'V', 'P':
		case 'C', 'D': // CR / DB
			p.Width += 2
		default:
			p.Width++
		}
	}

	editing := counts['Z'] + counts['*'] + counts['$'] + counts['+'] + counts['-'] +
		counts['C'] + counts['D'] + counts['.'] + counts['/'] + counts[',']
	switch {
	case counts['X'] > 0 || counts['A'] > 0 || counts['N'] > 0 || counts['G'] > 0:
		p.Category = CatAlphanumeric
	case editing > 0 || (counts['B'] > 0 || counts['0'] > 0) && counts['9'] > 0:
		p.Category = CatNumericEdited
		// Edited numerics are decoded as text; digit info only informational.
		p.Digits += counts['Z'] + counts['*']
	case counts['9'] > 0:
		p.Category = CatNumeric
	default:
		return nil, fmt.Errorf("picture %q: no data positions", raw)
	}
	return p, nil
}
