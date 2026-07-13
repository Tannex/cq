package record

import (
	"strings"
	"testing"
)

const txnBook = `01 TXN.
   05 TXN-TYPE PIC 9(2).
      88 TXN-SALE VALUE 1.
      88 TXN-VOID VALUE 4.
      88 TXN-SPECIAL VALUE 90 THRU 99.
   05 TXN-STATE PIC X(3).
      88 ST-NSW VALUE 'NSW'.
      88 ST-EMPTY VALUE SPACES.
   05 TXN-AMT PIC S9(3)V99 COMP-3.
      88 AMT-ZERO VALUE ZERO.
`

// txn builds one raw record.
func txn(t *testing.T, typ string, state string, amt string, negAmt bool) []byte {
	t.Helper()
	var b []byte
	b = append(b, zoned(typ, false)...)
	b = append(b, ebc(t, state, 3)...)
	b = append(b, packed(amt, negAmt)...)
	return b
}

func mustMatch(t *testing.T, d *Decoder, rec []byte, want bool) {
	t.Helper()
	got, err := d.Matches(rec)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("Matches = %v, want %v", got, want)
	}
}

func TestWhereEquality(t *testing.T) {
	d := mustDecoder(t, txnBook)
	if err := d.AddWhere("TXN-SALE"); err != nil {
		t.Fatal(err)
	}
	mustMatch(t, d, txn(t, "01", "NSW", "00100", false), true)
	mustMatch(t, d, txn(t, "04", "NSW", "00100", false), false)
}

func TestWhereNegation(t *testing.T) {
	for _, spec := range []string{"!TXN-SALE", "not TXN-SALE", "NOT txn-sale", " ! TXN-SALE"} {
		d := mustDecoder(t, txnBook)
		if err := d.AddWhere(spec); err != nil {
			t.Fatalf("%q: %v", spec, err)
		}
		got, err := d.Matches(txn(t, "01", "NSW", "00100", false))
		if err != nil {
			t.Fatal(err)
		}
		if got {
			t.Errorf("%q: sale record should be excluded", spec)
		}
		got, err = d.Matches(txn(t, "04", "NSW", "00100", false))
		if err != nil {
			t.Fatal(err)
		}
		if !got {
			t.Errorf("%q: void record should pass", spec)
		}
	}
}

func TestWhereThruRange(t *testing.T) {
	d := mustDecoder(t, txnBook)
	if err := d.AddWhere("TXN-SPECIAL"); err != nil {
		t.Fatal(err)
	}
	mustMatch(t, d, txn(t, "90", "NSW", "00100", false), true)
	mustMatch(t, d, txn(t, "95", "NSW", "00100", false), true)
	mustMatch(t, d, txn(t, "99", "NSW", "00100", false), true)
	mustMatch(t, d, txn(t, "89", "NSW", "00100", false), false)
}

func TestWhereText(t *testing.T) {
	d := mustDecoder(t, txnBook)
	if err := d.AddWhere("ST-NSW"); err != nil {
		t.Fatal(err)
	}
	mustMatch(t, d, txn(t, "01", "NSW", "00100", false), true)
	mustMatch(t, d, txn(t, "01", "VIC", "00100", false), false)

	d = mustDecoder(t, txnBook)
	if err := d.AddWhere("ST-EMPTY"); err != nil {
		t.Fatal(err)
	}
	mustMatch(t, d, txn(t, "01", "", "00100", false), true) // blanks
	mustMatch(t, d, txn(t, "01", "NSW", "00100", false), false)
}

func TestWhereZeroFigurative(t *testing.T) {
	d := mustDecoder(t, txnBook)
	if err := d.AddWhere("AMT-ZERO"); err != nil {
		t.Fatal(err)
	}
	mustMatch(t, d, txn(t, "01", "NSW", "00000", false), true)
	mustMatch(t, d, txn(t, "01", "NSW", "00000", true), true) // -0 is 0
	mustMatch(t, d, txn(t, "01", "NSW", "00001", false), false)
}

func TestWhereAnd(t *testing.T) {
	d := mustDecoder(t, txnBook)
	if err := d.AddWhere("TXN-SALE"); err != nil {
		t.Fatal(err)
	}
	if err := d.AddWhere("!ST-NSW"); err != nil {
		t.Fatal(err)
	}
	mustMatch(t, d, txn(t, "01", "VIC", "00100", false), true)
	mustMatch(t, d, txn(t, "01", "NSW", "00100", false), false)
	mustMatch(t, d, txn(t, "04", "VIC", "00100", false), false)
}

func TestWhereErrors(t *testing.T) {
	d := mustDecoder(t, txnBook)
	if err := d.AddWhere("NO-SUCH-COND"); err == nil || !strings.Contains(err.Error(), "no such") {
		t.Errorf("unknown name: %v", err)
	}
	if err := d.AddWhere(""); err == nil {
		t.Error("empty spec should fail")
	}

	occursBook := `01 R.
   05 SLOT OCCURS 3 TIMES.
      10 SLOT-TYPE PIC 9.
         88 SLOT-FULL VALUE 9.
`
	d = mustDecoder(t, occursBook)
	if err := d.AddWhere("SLOT-FULL"); err == nil || !strings.Contains(err.Error(), "OCCURS") {
		t.Errorf("occurs condition: %v", err)
	}

	dupBook := `01 R.
   05 A PIC 9.
      88 HOT VALUE 1.
   05 B PIC 9.
      88 HOT VALUE 2.
`
	d = mustDecoder(t, dupBook)
	if err := d.AddWhere("HOT"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("duplicate condition: %v", err)
	}
}
