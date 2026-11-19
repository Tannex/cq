package gojq_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	upstream "github.com/itchyny/gojq"

	"github.com/Tannex/cq/internal/gojq"
)

// The sweep applies every builtin, at every arity, to lazy values in several
// positions: as the input, as each argument, and nested one level inside a
// native array and a native object the program builds itself. Upstream gojq
// on the native value is the reference.

// sweepRun evaluates with a deadline so that generators such as repeat or
// range with a huge bound cannot hang the sweep; such programs are dropped.
func sweepRun(run func(ctx context.Context) (string, bool)) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return run(ctx)
}

func sweepUpstream(query string, input any) (string, bool) {
	return sweepRun(func(ctx context.Context) (result string, ok bool) {
		defer func() {
			if r := recover(); r != nil {
				result, ok = "", false
			}
		}()
		parsed, err := upstream.Parse(query)
		if err != nil {
			return "", false
		}
		code, err := upstream.Compile(parsed)
		if err != nil {
			return "", false
		}
		var out strings.Builder
		iter := code.RunWithContext(ctx, input)
		for n := 0; n < 200; n++ {
			v, ok := iter.Next()
			if !ok {
				break
			}
			if err, ok := v.(error); ok {
				if ctx.Err() != nil {
					return "", false
				}
				out.WriteString("error: " + err.Error() + "\n")
				break
			}
			encoded, err := upstream.Marshal(v)
			if err != nil {
				out.WriteString("marshal error\n")
				break
			}
			out.Write(encoded)
			out.WriteByte('\n')
		}
		return out.String(), true
	})
}

func sweepFork(query string, input any) string {
	result, _ := sweepRun(func(ctx context.Context) (result string, ok bool) {
		var out strings.Builder
		defer func() {
			if r := recover(); r != nil {
				result = out.String() + fmt.Sprintf("panic: %v\n", r)
			}
		}()
		parsed, err := gojq.Parse(query)
		if err != nil {
			return "parse error", true
		}
		code, err := gojq.Compile(parsed)
		if err != nil {
			return "compile error", true
		}
		iter := code.RunWithContext(ctx, input)
		for n := 0; n < 200; n++ {
			v, ok := iter.Next()
			if !ok {
				break
			}
			if err, ok := v.(error); ok {
				out.WriteString("error: " + err.Error() + "\n")
				break
			}
			encoded, err := gojq.Marshal(v)
			if err != nil {
				out.WriteString("marshal error\n")
				break
			}
			out.Write(encoded)
			out.WriteByte('\n')
		}
		return out.String(), true
	})
	return result
}

// comparingObject is a lazy object that also orders itself (LazyComparer)
// the way the record set does: by delegating to Compare on its members.
type comparingObject struct{ testLazyObject }

func (o *comparingObject) CompareLazy(other any) (int, bool) {
	if p, ok := other.(*comparingObject); ok {
		return gojq.Compare(o.Map(), p.Map()), true
	}
	return 0, false
}

func lazifyComparing(v any) any {
	switch v := v.(type) {
	case map[string]any:
		members := make(map[string]any, len(v))
		for key, member := range v {
			members[key] = lazifyComparing(member)
		}
		return &comparingObject{testLazyObject{members}}
	case []any:
		elements := make([]any, len(v))
		for i, element := range v {
			elements[i] = lazifyComparing(element)
		}
		return &testLazyArray{elements}
	default:
		return v
	}
}

var sweepSkip = map[string]bool{
	"input": true, "inputs": true, "debug": true, "stderr": true, "input_filename": true,
	"now": true, "localtime": true, "mktime": true, "strflocaltime": true, "env": true,
	"builtins": true, "input_line_number": true, "halt": true, "repeat": true,
	"$__loc__": true, "get_search_list": true, "modulemeta": true, "get_prog_origin": true,
	"get_jq_origin": true, "splits": false,
}

func sweepBuiltins(t *testing.T) [][2]any {
	out := runFork(`builtins`, nil)
	var names []string
	if err := json.Unmarshal([]byte(out), &names); err != nil {
		t.Fatalf("builtins: %v (%s)", err, out)
	}
	// internal functions are callable too
	for _, extra := range []string{"_index/2", "_slice/3", "_captures/0", "_min_by/1", "_max_by/1", "_sort_by/1",
		"_group_by/1", "_unique_by/1", "_plus/0", "_negate/0", "_add/2", "_subtract/2", "_equal/2", "_less/2",
		"_match/3", "_range/3", "getpath/1", "setpath/2", "delpaths/1", "tojson/0", "_modify/2", "_last/1"} {
		names = append(names, extra)
	}
	sort.Strings(names)
	var fns [][2]any
	for _, n := range names {
		i := strings.LastIndexByte(n, '/')
		var arity int
		fmt.Sscan(n[i+1:], &arity)
		if sweepSkip[n[:i]] {
			continue
		}
		fns = append(fns, [2]any{n[:i], arity})
	}
	return fns
}

var sweepDocuments = []string{
	`[{"a":1,"name":"n","string":"s","start":1,"end":2,"key":"k","value":3},{"a":"x","b":[1,{"c":2}]},[3,1,2],"str",2,null]`,
	`{"start":1,"end":3,"a":[1,2,3,4],"b":{"c":{"start":0,"end":1}},"name":"x","string":"y","key":"k","value":[1]}`,
	`[[1,2],[3,4],[{"a":1},{"a":1}],[{"start":0,"end":1}]]`,
	`[{"start":0,"end":1},10,20,30]`,
}

var sweepArgs = []string{`.`, `.[0]`, `.a`, `1`, `"a"`, `[.]`, `{a: .}`, `[.[0]]`, `null`, `.b`}

// sweepWraps put the lazy input into different positions.
var sweepWraps = []string{`%s`, `[.] | %s`, `{a: .} | %s`, `[.[0], .[1]] | %s`, `(.[0] // .) | %s`, `[.[]?] | %s`}

func TestLazySweepBuiltins(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	// The full sweep takes minutes (over half an hour with -race); by
	// default every 16th program runs. CQ_LAZY_SWEEP=1 runs all of them.
	stride, offset := 16, 0
	if os.Getenv("CQ_LAZY_SWEEP") != "" {
		stride, offset = 1, 0
	}
	seq := 0
	fns := sweepBuiltins(t)
	var documents []any
	for _, raw := range sweepDocuments {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var v any
		if err := decoder.Decode(&v); err != nil {
			t.Fatal(err)
		}
		documents = append(documents, v)
	}
	failures := 0
	reported := map[string]int{}
	ran := 0
	for _, fn := range fns {
		name, arity := fn[0].(string), fn[1].(int)
		var calls []string
		switch arity {
		case 0:
			calls = []string{name}
		case 1:
			for _, a := range sweepArgs {
				calls = append(calls, fmt.Sprintf("%s(%s)", name, a))
			}
		case 2:
			for _, a := range sweepArgs {
				for _, b := range []string{`.`, `.[0]`, `1`, `"a"`, `[.[0]]`, `null`} {
					calls = append(calls, fmt.Sprintf("%s(%s; %s)", name, a, b))
				}
			}
		default:
			for _, a := range []string{`.`, `.[0]`, `1`, `"a"`} {
				for _, b := range []string{`.`, `.[0]`, `1`, `null`} {
					args := []string{a, b}
					for len(args) < arity {
						args = append(args, `null`)
					}
					calls = append(calls, fmt.Sprintf("%s(%s)", name, strings.Join(args, "; ")))
				}
			}
		}
		for _, call := range calls {
			for _, wrap := range sweepWraps {
				query := fmt.Sprintf(wrap, call)
				for _, document := range documents {
					if seq++; seq%stride != offset {
						continue
					}
					want, ok := sweepUpstream(query, document)
					if !ok {
						continue
					}
					ran++
					for kind, lazy := range map[string]any{
						"native": document, "top": lazifyTop(document), "full": lazify(document), "comparing": lazifyComparing(document),
					} {
						if got := sweepFork(query, lazy); got != want {
							failures++
							if reported[name]++; reported[name] <= 3 {
								t.Errorf("%s input differs\nquery: %s\ninput: %s\n--- upstream\n%s--- fork\n%s", kind, query, previewJSON(document), want, got)
							}
						}
					}
				}
			}
		}
	}
	t.Logf("%d programs compared, %d failures", ran, failures)
}

// Programs the sweep found, kept as fixed regression cases.
func TestLazyReviewFindings(t *testing.T) {
	for _, tc := range []struct{ query, input string }{
		// delpaths: a path that is itself a lazy array
		{`delpaths([.[0]])`, `[[1], [5, 6]]`},
		{`delpaths(.)`, `[[0], [1]]`},
		{`[.[0], .[1]] | delpaths(.)`, `[[0], [1, 0]]`},
		// a slice path element that is a lazy object: setpath, delpaths,
		// and paths recorded by path tracking from a lazy index (_index)
		{`setpath([.[0]]; ["x"])`, `[{"start": 1, "end": 3}, 10, 20, 30]`},
		{`delpaths([[.[0]]])`, `[{"start": 1, "end": 3}, 10, 20, 30]`},
		{`del(.[.[0]])`, `[{"start": 1, "end": 3}, 10, 20, 30]`},
		{`.[.[0]] |= ["y"]`, `[{"start": 1, "end": 3}, 10, 20, 30]`},
		{`.[.[0]] = ["y"]`, `[{"start": 1, "end": 3}, 10, 20, 30]`},
		{`delpaths([path(.[.[0]])])`, `[{"start": 0, "end": 1}, 10]`},
		{`path(getpath([.[0]])) as $p | delpaths([$p])`, `[{"start": 1, "end": 2}, 10, 20]`},
		// _captures with lazy capture objects
		{`_captures`, `[{"name": "a", "string": "x"}, {"name": "b", "string": "y"}]`},
		{`[.[0], .[1]] | _captures`, `[{"name": "a", "string": "x"}, {"name": "b", "string": "y"}]`},
	} {
		decoder := json.NewDecoder(strings.NewReader(tc.input))
		decoder.UseNumber()
		var v any
		if err := decoder.Decode(&v); err != nil {
			t.Fatal(err)
		}
		checkDifferential(t, tc.query, v)
		if got, want := runFork(tc.query, lazifyComparing(v)), runUpstream(tc.query, v); got != want {
			t.Errorf("comparing lazy input differs: %s: %s, want %s", tc.query, got, want)
		}
	}
}

// One compiled program runs on several goroutines at once (the record set's
// chunk workers). del over a constant object must not write to it: before
// the fix this was a fatal "concurrent map writes".
func TestCompiledCodeConcurrentDelOnConstants(t *testing.T) {
	for _, program := range []string{
		`{a: {b: 1, c: [1, {d: 2}]}} | del(.x)`,
		`[{b: 1}, [2]] | del(.[5])`,
		`{a: {b: 1}} | del(.a.x)`,
	} {
		parsed, err := gojq.Parse(program)
		if err != nil {
			t.Fatal(err)
		}
		code, err := gojq.Compile(parsed)
		if err != nil {
			t.Fatal(err)
		}
		want := runFork(program, nil)
		done := make(chan string)
		for range 8 {
			go func() {
				var got string
				for range 3000 {
					v, _ := code.Run(nil).Next()
					encoded, _ := gojq.Marshal(v)
					got = string(encoded) + "\n"
				}
				done <- got
			}()
		}
		for range 8 {
			if got := <-done; got != want {
				t.Errorf("%s: %s, want %s", program, got, want)
			}
		}
	}
}

// Every @format, applied to lazy values directly, nested, and through string
// interpolation.
func TestLazyFormats(t *testing.T) {
	var documents []any
	for _, raw := range append(append([]string{}, sweepDocuments...), `["a,b", 1, null, true, "x\ty"]`, `[["a"], {"b": 1}]`) {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var v any
		if err := decoder.Decode(&v); err != nil {
			t.Fatal(err)
		}
		documents = append(documents, v)
	}
	for _, format := range []string{"text", "json", "html", "uri", "csv", "tsv", "sh", "base64", "base64d", "base32", "base32d"} {
		for _, program := range []string{`@%s`, `.[0] | @%s`, `[.[0]] | @%s`, `[.[1]] | @%s`, `@%s "<\(.)>"`, `@%s "\(.[0])\(.[1])"`, `format("%s")`, `map(@%s?)`, `[.[]? | @%s?]`} {
			query := strings.ReplaceAll(program, "%s", format)
			for _, document := range documents {
				checkDifferential(t, query, document)
				if got, want := runFork(query, lazifyComparing(document)), runUpstream(query, document); got != want {
					t.Errorf("comparing lazy input differs: %s: %s, want %s", query, got, want)
				}
			}
		}
	}
}

// Path tracking over lazy values: every path expression under every
// path-consuming construct.
func TestLazyPathSweep(t *testing.T) {
	var documents []any
	for _, raw := range append(append([]string{}, sweepDocuments...), diffDocuments[0], diffDocuments[6]) {
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var v any
		if err := decoder.Decode(&v); err != nil {
			t.Fatal(err)
		}
		documents = append(documents, v)
	}
	paths := []string{`.`, `.a`, `.[0]`, `.[-1]`, `.[1:]`, `.[:1][]`, `.[1:][0]`, `.[]`, `.[]?`, `..`, `.[0].a`, `.[0][0]`, `first(.[])`,
		`last(.[])`, `limit(2; .[])`, `.[.[0]]`, `.[.[0]?]?`, `getpath(["a"])`, `getpath([0, "a"])`, `getpath([.[0]])?`, `select(.)`,
		`if .[0] then .[0] else .a end`, `.a // .[0]`, `(.a, .[0])`, `recurse`, `.[0:0]`, `.[] | select(type == "object") | .a`,
		`.[]? | .[]?`, `to_entries?`, `.[0] as $x | .[1]`, `reduce 1 as $x (.; .[0])`, `label $l | .[] | ., break $l`, `getpath(paths)`,
		`.c.d.e[1].f`, `.b.c`, `.[2:4]`, `.[1:3][0]`}
	constructs := []string{`[path(%s)]`, `try ([path(%s)]) catch .`, `del(%s)`, `try ((%s) |= 7) catch .`, `try ((%s) = 7) catch .`,
		`try ((%s) += 1) catch .`, `try ((%s) |= empty) catch .`, `[paths(%s)]?`, `try ([path(%s)] as $p | delpaths($p)) catch .`,
		`try (reduce path(%s) as $p (.; setpath($p; 1))) catch .`, `try ([path(%s)] | map(. as $p | $p)) catch .`, `try pick(%s) catch .`,
		`[getpath(path(%s))?]`, `try ([path(%s)] | sort) catch .`, `try ([path(%s)] | tojson) catch .`}
	for _, p := range paths {
		for _, c := range constructs {
			query := strings.ReplaceAll(c, "%s", p)
			for _, document := range documents {
				checkDifferential(t, query, document)
				if got, want := runFork(query, lazifyComparing(document)), runUpstream(query, document); got != want && !strings.Contains(want, "panic") {
					t.Errorf("comparing lazy input differs: %s: %s, want %s", query, got, want)
				}
			}
		}
	}
}

// An error message previews 30 bytes of the offending value. When that value
// is the whole input array (`.NAME` instead of `.[].NAME`), the preview must
// not materialize the array: for a data set of millions of records that was
// one allocation per record just to print the first one.
func TestPreviewDoesNotMaterializeLazyArray(t *testing.T) {
	elements := make([]any, 200_000)
	for i := range elements {
		elements[i] = i
	}
	set := &countingArray{testLazyArray{elements}, 0}
	want := runUpstream(`.NAME`, elements)
	if got := runFork(`.NAME`, set); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if set.slices != 0 {
		t.Errorf("the error preview materialized the array %d times", set.slices)
	}
}
