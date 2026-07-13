package copybook

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseWithNestedCopies(t *testing.T) {
	members := map[string]string{
		"ADDRESS":  "05 ADDRESS.\n   COPY POSTCODE.\n",
		"POSTCODE": "10 POSTCODE PIC X(4).\n",
	}
	resolve := func(name string) (string, error) {
		src, ok := members[name]
		if !ok {
			return "", fmt.Errorf("member not found")
		}
		return src, nil
	}

	items, err := ParseWithCopies("01 CUSTOMER.\n   COPY ADDRESS.\n", FormatAuto, resolve)
	if err != nil {
		t.Fatalf("ParseWithCopies() error = %v", err)
	}
	address := items[0].Children[0]
	if address.Name != "ADDRESS" || len(address.Children) != 1 || address.Children[0].Name != "POSTCODE" {
		t.Fatalf("expanded tree = %+v, want CUSTOMER > ADDRESS > POSTCODE", items[0])
	}
}

func TestParseWithFixedFormatCopy(t *testing.T) {
	resolve := func(name string) (string, error) {
		if name != "FIELD" {
			return "", fmt.Errorf("unexpected member %s", name)
		}
		return "000100     05 VALUE-FIELD PIC X(2).\n", nil
	}
	src := "000100 01 RECORD.\n000200     COPY FIELD.\n"

	items, err := ParseWithCopies(src, FormatFixed, resolve)
	if err != nil {
		t.Fatalf("ParseWithCopies() error = %v", err)
	}
	if got := items[0].Children[0].Name; got != "VALUE-FIELD" {
		t.Fatalf("expanded child = %q, want VALUE-FIELD", got)
	}
}

func TestParseWithCopiesDetectsCycle(t *testing.T) {
	members := map[string]string{
		"A": "COPY B.\n",
		"B": "COPY A.\n",
	}
	resolve := func(name string) (string, error) { return members[name], nil }

	_, err := ParseWithCopies("01 RECORD.\n COPY A.\n", FormatAuto, resolve)
	if err == nil || !strings.Contains(err.Error(), "COPY cycle: A -> B -> A") {
		t.Fatalf("ParseWithCopies() error = %v, want cycle", err)
	}
}

func TestParseWithCopiesRejectsClauses(t *testing.T) {
	resolve := func(name string) (string, error) { return "", nil }

	_, err := ParseWithCopies("01 RECORD.\n COPY A REPLACING ==X== BY ==Y==.\n", FormatAuto, resolve)
	if err == nil || !strings.Contains(err.Error(), "unsupported clauses") {
		t.Fatalf("ParseWithCopies() error = %v, want unsupported clause", err)
	}
}

func TestParseWithCopiesRejectsProcedureDivisionBeforeExpansion(t *testing.T) {
	resolveCalls := 0
	resolve := func(name string) (string, error) {
		resolveCalls++
		return "05 FIELD PIC X(1).", nil
	}
	src := `IDENTIFICATION DIVISION.
PROGRAM-ID. EXAMPLE.
DATA DIVISION.
WORKING-STORAGE SECTION.
01 RECORD.
   COPY FIELD.
PROCEDURE DIVISION.
   GOBACK.
`

	_, err := ParseWithCopies(src, FormatFree, resolve)
	if err == nil || !strings.Contains(err.Error(), "Not a copybook") {
		t.Fatalf("ParseWithCopies() error = %v, want Not a copybook", err)
	}
	if resolveCalls != 0 {
		t.Fatalf("resolver called %d times, want 0", resolveCalls)
	}
}

func TestParseWithCopiesIgnoresCommentedProcedureDivision(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		format Format
	}{
		{
			name: "free",
			src: `01 RECORD.
*> PROCEDURE DIVISION.
   05 FIELD PIC X(1). *> PROCEDURE DIVISION.
`,
			format: FormatFree,
		},
		{
			name: "fixed",
			src: "000100 01 RECORD.\n" +
				"000200*PROCEDURE DIVISION.\n" +
				"000300     05 FIELD PIC X(1).\n",
			format: FormatFixed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, err := ParseWithCopies(tt.src, tt.format, nil)
			if err != nil {
				t.Fatalf("ParseWithCopies() error = %v", err)
			}
			if got := items[0].Children[0].Name; got != "FIELD" {
				t.Fatalf("child name = %q, want FIELD", got)
			}
		})
	}
}

func TestParseWithCopiesAllowsProcedureDivisionLiteral(t *testing.T) {
	src := "01 RECORD.\n 05 TEXT PIC X(18) VALUE 'PROCEDURE DIVISION'.\n"

	if _, err := ParseWithCopies(src, FormatFree, nil); err != nil {
		t.Fatalf("ParseWithCopies() error = %v", err)
	}
}
