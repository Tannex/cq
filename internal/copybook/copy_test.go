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
