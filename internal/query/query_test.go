package query

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/itchyny/gojq"
)

func run(t *testing.T, expr, input string) []string {
	t.Helper()
	q, err := Compile(expr)
	if err != nil {
		t.Fatal(err)
	}
	v, err := FromJSON([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	err = q.Run(v, func(r any) error {
		b, err := gojq.Marshal(r)
		if err != nil {
			return err
		}
		out = append(out, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFieldAccess(t *testing.T) {
	got := run(t, `.NAME`, `{"NAME":"ALICE","N":1}`)
	if len(got) != 1 || got[0] != `"ALICE"` {
		t.Errorf("got %v", got)
	}
}

func TestSelectAndArithmetic(t *testing.T) {
	got := run(t, `select(.BAL < 0) | .BAL * 2`, `{"BAL":-123.45}`)
	if len(got) != 1 || got[0] != `-246.9` {
		t.Errorf("got %v", got)
	}
	got = run(t, `select(.BAL < 0)`, `{"BAL":10}`)
	if len(got) != 0 {
		t.Errorf("want no results, got %v", got)
	}
}

func TestPrecisionPreserved(t *testing.T) {
	// 18-digit integers survive FromJSON (json.Number) and identity queries.
	got := run(t, `.N`, `{"N":123456789012345678}`)
	if len(got) != 1 || got[0] != `123456789012345678` {
		t.Errorf("got %v", got)
	}
}

func TestMultipleResults(t *testing.T) {
	got := run(t, `.A[]`, `{"A":[1,2,3]}`)
	if len(got) != 3 || got[0] != `1` || got[2] != `3` {
		t.Errorf("got %v", got)
	}
}

func TestCompileError(t *testing.T) {
	if _, err := Compile(`.foo | `); err == nil {
		t.Fatal("want parse error")
	}
}

func TestRuntimeError(t *testing.T) {
	q, err := Compile(`.A.B`)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := FromJSON([]byte(`{"A":"text"}`))
	err = q.Run(v, func(any) error { return nil })
	if err == nil {
		t.Fatal("want runtime error indexing a string")
	}
	var h *Halt
	if errors.As(err, &h) {
		t.Fatalf("plain error reported as halt: %v", err)
	}
}

func TestHalt(t *testing.T) {
	q, err := Compile(`halt`)
	if err != nil {
		t.Fatal(err)
	}
	err = q.Run(map[string]any{}, func(any) error { return nil })
	var h *Halt
	if !errors.As(err, &h) || h.Value != nil {
		t.Fatalf("want clean halt, got %v", err)
	}

	q, err = Compile(`"stop here" | halt_error`)
	if err != nil {
		t.Fatal(err)
	}
	err = q.Run(map[string]any{}, func(any) error { return nil })
	if !errors.As(err, &h) || h.Error() != "stop here" {
		t.Fatalf("want halt_error payload, got %v", err)
	}
}

func TestFromJSONKeepsNumbers(t *testing.T) {
	v, err := FromJSON([]byte(`{"N":-0.10}`))
	if err != nil {
		t.Fatal(err)
	}
	n := v.(map[string]any)["N"]
	if num, ok := n.(json.Number); !ok || string(num) != "-0.10" {
		t.Errorf("got %T %v", n, n)
	}
}
