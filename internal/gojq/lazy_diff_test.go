package gojq_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"testing"
	"time"

	upstream "github.com/itchyny/gojq"

	"github.com/Tannex/cq/internal/gojq"
)

// The differential tests pin the fork to upstream gojq: every program must
// produce the same outputs and the same error on
//
//   - upstream gojq with native values,
//   - the fork with native values, and
//   - the fork with every object and array replaced by a lazy value.
//
// The lazy wrappers nest at every level, which is stricter than any real
// implementation needs to be: a native function that forgets to convert a
// lazy operand anywhere in a value tree shows up as a difference.

type testLazyObject struct{ members map[string]any }

func (o *testLazyObject) Get(key string) any    { return o.members[key] }
func (o *testLazyObject) Map() map[string]any   { return maps.Clone(o.members) }
func (o *testLazyObject) String() string        { return "testLazyObject" }
func (a *testLazyArray) Len() int               { return len(a.elements) }
func (a *testLazyArray) At(i int) any           { return a.elements[i] }
func (a *testLazyArray) Slice(lo, hi int) []any { return append([]any{}, a.elements[lo:hi]...) }

type testLazyArray struct{ elements []any }

// lazify replaces every container of a native value tree with a lazy one.
func lazify(v any) any {
	switch v := v.(type) {
	case map[string]any:
		members := make(map[string]any, len(v))
		for key, member := range v {
			members[key] = lazify(member)
		}
		return &testLazyObject{members}
	case []any:
		elements := make([]any, len(v))
		for i, element := range v {
			elements[i] = lazify(element)
		}
		return &testLazyArray{elements}
	default:
		return v
	}
}

// lazifyTop only wraps the outermost container and the containers directly
// inside it, the shape of a record set: a lazy array of lazy records whose
// members are native.
func lazifyTop(v any) any {
	switch v := v.(type) {
	case map[string]any:
		return &testLazyObject{maps.Clone(v)}
	case []any:
		elements := make([]any, len(v))
		for i, element := range v {
			if object, ok := element.(map[string]any); ok {
				element = &testLazyObject{maps.Clone(object)}
			}
			elements[i] = element
		}
		return &testLazyArray{elements}
	default:
		return v
	}
}

type diffCase struct {
	Name      string `json:"name"`
	Query     string `json:"query"`
	Input     string `json:"input"`
	NullInput bool   `json:"null_input"`
}

func decodeInputs(t *testing.T, tc diffCase) ([]any, bool) {
	t.Helper()
	if tc.NullInput {
		return []any{nil}, true
	}
	var inputs []any
	decoder := json.NewDecoder(strings.NewReader(tc.Input))
	decoder.UseNumber()
	for {
		var v any
		if err := decoder.Decode(&v); err != nil {
			if errors.Is(err, io.EOF) {
				return inputs, true
			}
			return nil, false // not plain JSON; exercised by upstream's CLI tests only
		}
		inputs = append(inputs, v)
	}
}

const diffOutputLimit = 2000

// runUpstream renders the outputs of a program, one per line, ending with an
// error line when the program fails. A panic is part of the result too, so
// the fork is held to upstream's behavior even there.
func runUpstream(query string, input any) (result string) {
	defer func() {
		if r := recover(); r != nil {
			result += fmt.Sprintf("panic: %v\n", r)
		}
	}()
	parsed, err := upstream.Parse(query)
	if err != nil {
		return "parse error: " + err.Error() + "\n"
	}
	code, err := upstream.Compile(parsed)
	if err != nil {
		return "compile error: " + err.Error() + "\n"
	}
	var out strings.Builder
	iter := code.Run(input)
	for n := 0; n < diffOutputLimit; n++ {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := v.(error); ok {
			out.WriteString("error: " + err.Error() + "\n")
			break
		}
		encoded, err := upstream.Marshal(v)
		if err != nil {
			out.WriteString("marshal error: " + err.Error() + "\n")
			break
		}
		out.Write(encoded)
		out.WriteByte('\n')
	}
	return out.String()
}

func runFork(query string, input any) (result string) {
	var out strings.Builder
	defer func() {
		if r := recover(); r != nil {
			result = out.String() + fmt.Sprintf("panic: %v\n", r)
		}
	}()
	parsed, err := gojq.Parse(query)
	if err != nil {
		return "parse error: " + err.Error() + "\n"
	}
	code, err := gojq.Compile(parsed)
	if err != nil {
		return "compile error: " + err.Error() + "\n"
	}
	iter := code.Run(input)
	for n := 0; n < diffOutputLimit; n++ {
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
			out.WriteString("marshal error: " + err.Error() + "\n")
			break
		}
		out.Write(encoded)
		out.WriteByte('\n')
	}
	return out.String()
}

// nondeterministic reports programs whose output legitimately differs
// between two runs.
func nondeterministic(query string) bool {
	for _, word := range []string{"now", "localtime", "mktime", "strflocaltime", "input", "$ENV", "env", "$__loc__", "debug", "stderr"} {
		if strings.Contains(query, word) {
			return true
		}
	}
	return false
}

func checkDifferential(t *testing.T, query string, input any) {
	t.Helper()
	want := runUpstream(query, input)
	if strings.Contains(want, "panic: ") {
		return // upstream itself gives up on this program
	}
	if got := runFork(query, input); got != want {
		t.Errorf("fork differs from upstream on native input\nquery: %s\ninput: %s\n--- upstream\n%s--- fork\n%s", query, previewJSON(input), want, got)
		return
	}
	if got := runFork(query, lazifyTop(input)); got != want {
		t.Errorf("fork differs on record-shaped lazy input\nquery: %s\ninput: %s\n--- upstream\n%s--- fork (lazy)\n%s", query, previewJSON(input), want, got)
	}
	if got := runFork(query, lazify(input)); got != want {
		t.Errorf("fork differs on fully lazy input\nquery: %s\ninput: %s\n--- upstream\n%s--- fork (lazy)\n%s", query, previewJSON(input), want, got)
	}
}

func previewJSON(v any) string {
	encoded, _ := json.Marshal(v)
	if len(encoded) > 300 {
		encoded = append(encoded[:300], "..."...)
	}
	return string(encoded)
}

// TestLazyDifferentialUpstreamCorpus replays the programs of upstream's CLI
// test suite (testdata/upstream_cli_cases.json, extracted from cli/test.yaml
// of the forked release).
func TestLazyDifferentialUpstreamCorpus(t *testing.T) {
	raw, err := os.ReadFile("testdata/upstream_cli_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []diffCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	ran := 0
	for _, tc := range cases {
		if nondeterministic(tc.Query) {
			continue
		}
		inputs, ok := decodeInputs(t, tc)
		if !ok {
			continue
		}
		for _, input := range inputs {
			ran++
			start := time.Now()
			checkDifferential(t, tc.Query, input)
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				t.Logf("slow corpus case %q (%s): %s", tc.Name, elapsed.Round(time.Millisecond), tc.Query)
			}
		}
	}
	if ran < 500 {
		t.Fatalf("only %d corpus programs ran; the corpus did not load as expected", ran)
	}
}

// diffDocuments are inputs every program of TestLazyDifferentialPrograms runs
// against: record sets (arrays of flat and nested objects, duplicates
// included), single records, and awkward shapes.
var diffDocuments = []string{
	`[{"ID":1,"NAME":"ANNA","REGION":"R1","AMOUNT":10.50,"BIG":12345678901234567890,"TAGS":["a","b"],"CUSTOMER":{"CITY":"Oslo","TIER":2},"ITEMS":[{"CODE":"X","QTY":1},{"CODE":"Y","QTY":2}]},
	  {"ID":2,"NAME":"BOB","REGION":"R2","AMOUNT":7,"BIG":3,"TAGS":[],"CUSTOMER":{"CITY":"Bergen","TIER":1},"ITEMS":[{"CODE":"Y","QTY":5}]},
	  {"ID":3,"NAME":"anna","REGION":"R1","AMOUNT":10.5,"BIG":12345678901234567891,"TAGS":["b"],"CUSTOMER":{"CITY":"Oslo","TIER":2},"ITEMS":[]},
	  {"ID":2,"NAME":"BOB","REGION":"R2","AMOUNT":7,"BIG":3,"TAGS":[],"CUSTOMER":{"CITY":"Bergen","TIER":1},"ITEMS":[{"CODE":"Y","QTY":5}]},
	  {"ID":5,"NAME":"","REGION":"R3","AMOUNT":-0.25,"BIG":0,"TAGS":["c","a"],"CUSTOMER":{"CITY":"","TIER":0},"ITEMS":[{"CODE":"Z","QTY":0}]}]`,
	`[]`,
	`[{}]`,
	`[{"a":1},{"a":null},{"b":2},{"a":[1,2]},{"a":{"a":1}}]`,
	`[[{"a":1},{"a":2}],[{"a":3}],[]]`,
	`{"ID":1,"NAME":"ANNA","REGION":"R1","AMOUNT":10.50,"CUSTOMER":{"CITY":"Oslo","TIER":2},"ITEMS":[{"CODE":"X","QTY":1}]}`,
	`{"a":[{"b":1},{"b":2}],"c":{"d":{"e":[1,{"f":2}]}}}`,
	`[1,"a",null,true,[1],{"a":1},[{"a":1}],{"a":[1]}]`,
	`null`,
}

// diffPrograms concentrates on what a record query does: field access,
// filters, projections, aggregation, sorting and grouping, updates, paths,
// formatting, and the error cases of each.
var diffPrograms = []string{
	// identity, iteration, indexing, slicing
	`.`, `.[]`, `.[]?`, `.[0]`, `.[-1]`, `.[1]`, `.[99]`, `.[-99]`, `.[1:3]`, `.[:2]`, `.[-2:]`, `.[2:1]`, `.[1.5:3.5]`,
	`.[:1][0]`, `.[0:2] | length`, `first`, `last`, `nth(1)`, `first(.[])`, `limit(2; .[])`, `[limit(2; .[])]`, `until(length < 2; .[1:])`,
	`.[0].NAME`, `.[0]["NAME"]`, `.[0].MISSING`, `.[0].CUSTOMER.CITY`, `.[0].ITEMS[0].CODE`, `.[0].ITEMS[].QTY`, `.[].NAME`, `.[]?.NAME`,
	`.NAME`, `.["NAME"]`, `.NAME?`, `.[0]?`, `."NAME"?`, `.[] | .ID?`, `.[.[0]]`, `.[[]]`, `.[null]`, `.["a","b"]?`, `.[0][0]`, `.[0][]`,
	`.a`, `.a[]`, `.a[0].b`, `.c.d.e[1].f`, `.[] | .a`, `.[] | .a?`, `..`, `[..] | length`, `.. | numbers`, `[.. | scalars]`,
	// filters and projections
	`.[] | select(.REGION == "R1")`, `.[] | select(.REGION == "R1") | .ID`, `.[] | select(.AMOUNT > 8) | {ID, NAME}`,
	`.[] | select(.ID == 2)`, `.[] | select(.NAME | test("^A"; "i"))`, `.[] | select(.CUSTOMER.TIER >= 2 and .ITEMS[0].QTY == 1) | .ID`,
	`.[] | select(.TAGS | index("a")) | .NAME`, `.[] | select(.NAME != "") | [.ID, .NAME]`, `.[] | {id: .ID, city: .CUSTOMER.CITY}`,
	`.[] | {ID, missing: .MISSING, ("k" + (.ID | tostring)): .REGION}`, `.[] | [.ID, .AMOUNT * 2, .AMOUNT + 1, -.AMOUNT, .AMOUNT / 2, .ID % 2]`,
	`.[] | .NAME + "!"`, `.[] | "\(.ID): \(.NAME)"`, `.[] | "\(.)"`, `.[] | @json`, `.[] | [.ID, .NAME] | @csv`, `.[] | [.ID, .NAME] | @tsv`,
	`.[] | @text`, `.[] | @html`, `.[] | [.] | @csv`, `.[] | @sh`, `.[] | tojson`, `.[] | tostring`, `.[] | tojson | fromjson`, `.[] | ascii_downcase?`,
	`.[] | .NAME | ascii_downcase`, `.[] | .NAME | length`, `.[] | .BIG`, `.[] | .BIG + 1`, `.[] | .BIG == 12345678901234567890`, `.[] | .AMOUNT == 10.5`,
	`.[] | if .REGION == "R1" then .NAME else empty end`, `.[] | .MISSING // "dflt"`, `.[] | (.NAME // .ID)`, `.[] | try error(.) catch .ID`,
	`.[] | try error catch .`, `.[] | error(.)`, `.[] | error`, `.[0] | error`, `.[] | not`, `.[] | [.] | first`, `.[] | [., .] | unique | length`,
	`map(select(.REGION == "R1"))`, `map(.NAME)`, `map(.CUSTOMER)`, `map(.ITEMS[])`, `map(.ITEMS | length)`, `map(has("ID"))`, `map(keys)`,
	`map(keys_unsorted)`, `map(length)`, `map(type)`, `map(to_entries)`, `map(with_entries(.value |= tostring))`, `map(del(.ITEMS))`,
	`map(del(.CUSTOMER.CITY))`, `map(.NAME |= ascii_downcase)`, `map(.CUSTOMER.TIER += 1)`, `map(.NEW = .ID * 2)`, `map(. + {X: 1})`,
	`map(. * {CUSTOMER: {EXTRA: true}})`, `map({a: .} * {a: {X: 1}})`, `map(.ITEMS |= map(.QTY))`, `map(pick(.ID, .CUSTOMER.CITY))`,
	`map(to_entries | from_entries)`, `map(.TAGS | join(","))`, `map([.ID, .NAME] | join("-"))`, `map(tostream)`, `[.[] | tostream]`,
	`map([paths])`, `map([leaf_paths])`, `map(path(..))`, `[paths]`, `[paths(type == "number")]`, `[leaf_paths] | length`,
	`map(getpath(["CUSTOMER", "CITY"]))`, `getpath([0, "NAME"])`, `getpath([0, "ITEMS", 0])`, `getpath(["a"])?`, `getpath([0, 0])`,
	`[getpath([0], [1], [9])]`, `setpath([0, "NAME"]; "x") | .[0]`, `setpath([0]; 1)`, `setpath([0, "CUSTOMER", "CITY"]; null) | .[0].CUSTOMER`,
	`delpaths([[0], [1, "NAME"]])`, `del(.[0])`, `del(.[0].NAME)`, `del(.[].ITEMS)`, `del(.[0, 2])`, `del(.[1:])`, `del(.[] | select(.ID == 2))`,
	`to_entries`, `to_entries | map(.key)`, `with_entries(.value |= type)?`, `map_values(type)`, `map_values(empty)`, `.[0] |= {a: 1}`,
	`.[0].NAME |= "x"`, `.[].NAME |= ascii_downcase`, `.[].CUSTOMER.TIER |= . + 1`, `.[0] = 1`, `.[1:3] = ["x"]`, `.[] += {X: 1}`, `. += [1]`,
	`.[0] |= empty`, `(.[] | select(.ID == 2) | .NAME) |= "two"`, `.[] |= (.ID // null)`, `. |= map(.ID)`, `.. |= (numbers |= . + 1)?`,
	`walk(if type == "number" then . + 1 else . end)`, `walk(if type == "object" then del(.ID) else . end)`, `[.[] | walk(strings |= ascii_downcase)]`,
	// aggregation
	`length`, `map(.AMOUNT) | add`, `[.[].AMOUNT] | add`, `add`, `add(.[].AMOUNT)`, `map(.TAGS) | add`, `map(.CUSTOMER) | add`, `map(.NAME) | add`,
	`[.[] | .ITEMS | length] | add`, `map(.AMOUNT) | min`, `map(.AMOUNT) | max`, `min`, `max`, `min_by(.AMOUNT)`, `max_by(.AMOUNT)`,
	`min_by(.NAME)`, `max_by(.CUSTOMER)`, `min_by(.MISSING)`, `any`, `all`, `any(.[]; .ID == 2)`, `all(.[]; .ID > 0)`, `map(.ID == 2) | any`,
	`[.[] | .ID] | index(2)`, `index(.[1])`, `indices(.[1])`, `rindex(.[1])`, `inside(.)`, `contains(.)`, `contains([{"ID": 2}])`,
	`.[0] | contains({CUSTOMER: {CITY: "Os"}})`, `map(contains({REGION: "R1"}))`, `map(inside({ID: 1}))`, `map(has("NAME"))`, `has(0)`, `has("a")?`,
	`map(in({"1": true})?)`, `[.[] | .ID as $id | $id * 2]`, `. as $all | .[] | [.ID, ($all | length)]`, `. as [$first, $second] | [$first.ID, $second.ID]`,
	`.[0] as {ID: $id, CUSTOMER: {CITY: $city}} | [$id, $city]`, `.[] as {ID: $id} ?// [$id] | $id`, `. as [$a] | $a`, `.[0] as $r | $r.NAME`,
	`reduce .[] as $r (0; . + $r.AMOUNT)`, `reduce .[] as $r ({}; .[$r.REGION] += 1)`, `reduce .[] as $r ([]; . + [$r.ID])`,
	`reduce .[] as $r (null; $r)`, `foreach .[] as $r (0; . + 1; [., $r.ID])`, `foreach .[] as $r (0; . + $r.AMOUNT)`, `[foreach .[] as $r ({}; $r; .NAME)]`,
	`[.[] | .REGION] | unique`, `map(.REGION) | unique | length`, `unique`, `unique | length`, `unique_by(.REGION)`, `unique_by(.CUSTOMER)`,
	`unique_by(.MISSING)`, `[.[].TAGS] | unique`, `group_by(.REGION)`, `group_by(.REGION) | map({region: .[0].REGION, count: length})`,
	`group_by(.CUSTOMER.CITY) | map(length)`, `group_by(.ID) | map(map(.NAME))`, `group_by(.MISSING) | length`, `group_by(.ITEMS)`, `group_by(.)`,
	`sort`, `sort | map(.ID)`, `sort_by(.AMOUNT)`, `sort_by(.AMOUNT) | map(.ID)`, `sort_by(.NAME) | map(.NAME)`, `sort_by(.REGION, .ID) | map(.ID)`,
	`sort_by(.CUSTOMER) | map(.ID)`, `sort_by(.BIG) | map(.BIG)`, `sort_by(-.AMOUNT) | .[0].ID`, `sort_by(.TAGS) | map(.TAGS)`, `sort_by(.) | map(.ID)`,
	`sort_by(.MISSING) | map(.ID)`, `[.[] | .AMOUNT] | sort`, `reverse`, `reverse | map(.ID)`, `map(.ID) | reverse`, `bsearch(.[1])`, `bsearch(0)`,
	`sort | bsearch({"ID": 2})`, `flatten`, `flatten(1)`, `flatten(0)`, `[., .] | flatten | length`, `[., .] | flatten(1) | length`, `[.[0], [.[1]]] | flatten`,
	`transpose`, `[., .] | transpose | length`, `[.[0:2], .[2:3]] | transpose`, `map([.ID, .NAME]) | transpose`, `combinations`, `[.[0:2], .[2:3]] | [combinations] | length`,
	`tojson | fromjson | length`, `tostring | length`, `@json | length`, `[.[] | tojson] | join("\n") | length`, `keys`, `keys_unsorted`, `map(keys) | add | unique`,
	`type`, `map(type)`, `.[0] | type`, `arrays | length`, `objects | length`, `iterables | length`, `scalars`, `values | length`, `nulls`,
	`[.[] | objects] | length`, `[.[] | arrays]`, `[.[] | scalars]`, `isvalid(.[0])`, `[.[] | numbers]`, `toarray | length`, `.[0] | toarray | length`,
	`@csv`, `@tsv`, `@sh`, `@html`, `@uri`, `@base64`, `@text | length`, `ascii_downcase`, `ltrimstr("a")`, `startswith("a")`, `test("a")`,
	`split(",")`, `join(",")`, `map(tostring) | join(",") | length`, `implode`, `explode`, `tonumber`, `abs`, `floor`, `sqrt`, `pow(.; 2)`, `pow(2; 3)`,
	`[.[] | pow(.ID; 2)?]`, `[.[] | atan2(.ID; 1)? | floor]`, `-(.)`, `+.`, `. + .`, `. - .`, `. * 2`, `. / .`, `. % 2`, `. + null`, `null + .`, `. + 1`,
	`.[0] + .[1]`, `.[0] * .[1]`, `.[0] - .[1]`, `.[0] / .[1]`, `[.[0]] - [.[0]]`, `. - [.[0]]`, `. - .[1:]`, `[.[0], .[1]] - [.[1]]`, `.[0] == .[0]`,
	`.[1] == .[3]`, `.[0] == .[1]`, `.[0] < .[1]`, `.[1] <= .[3]`, `.[0] != .[2]`, `. == .`, `. < .`, `. == [.[]]`, `[.[]] == .`, `.[0] == (.[0] | tojson | fromjson)`,
	`.[0] < 1`, `.[0] > "a"`, `.[0] > [.[0]]`, `. > .[0]`, `. < {}`, `[.[0], 1, "a", null, [], {}] | sort | map(type)`, `[.[] | . == null]`,
	`.[0] // 1`, `(.[5] // .[0]) | type`, `[.[] | (.MISSING // empty)]`, `first(.[] | select(.ID == 2)) == .[1]`, `[.[] | select(. == null)]`,
	`splits("a")`, `sub("a"; "b")`, `ascii`, `@base64d`, `input_line_number`, `$__prog_args?`, `halt_error`, `.[0] | halt_error`, `{a: .} | halt_error`,
	`try halt_error catch .`, `error(null)`, `try error("x") catch .`, `try error({a: .[0]}) catch .a.ID`, `[.[] | try error(.) catch .] | length`,
	`label $out | .[] | if .ID == 2 then break $out else .ID end`, `[limit(1; .[] | select(.ID == 2))] | length`, `first(.[] | select(.ID > 100))`,
	`isempty(.[])`, `isempty(.[] | select(.ID > 100))`, `[range(length)]`, `[range(0; length; 2)] | map(tostring)`, `[.[range(length)]] | length`,
	`def f: .[] | select(.ID == 2); [f] | length`, `def f(g): [.[] | g]; f(.ID)`, `def r: if length > 1 then .[1:] | r else . end; r | length`,
	`[recurse(if type == "array" then .[1:] | select(length > 0) else empty end)] | length`, `[recurse] | length`, `[recurse(.[]?; . != null)] | length`,
	`env | type`, `[splits("x")?]`, `ltrimstr(.[0])`, `tojson | test("ANNA")`, `[.[] | .NAME | select(. != null) | test("N")?]`, `getpath(["x"])?`,
	`[.[] | objects | .CUSTOMER | objects | .CITY]`, `[.[] | .ITEMS? | arrays | .[] | .CODE]`, `[.[] | .ITEMS? | arrays | map(.QTY) | add]`,
	`map(.ITEMS? // [] | map(.CODE)) | add`, `[.[] | .TAGS? | arrays | sort]`, `[.[] | .TAGS? | arrays | unique | length]`, `map(.TAGS? | arrays | contains(["a"]))`,
	`INDEX(.ID)`, `INDEX(.[]; .REGION) | keys`, `[IN(.[]; .[0], .[1])]`, `[.[] | IN(.ID; 1, 2)]`, `GROUP_BY(.REGION)?`, `[.[] | ascii?]`, `tojson | length`,
	`[.[] | objects | with_entries(select(.value | type == "string"))]`, `[.[] | objects | to_entries[] | select(.key == "ID") | .value]`,
	`[.[] | objects | del(.[keys[0]])] | length`, `[.[] | objects | .[keys[0]?]?]`, `map(objects | . as $o | keys | map($o[.]) | length)`,
	`[.[] | objects | [.[]] | length]`, `[.[] | objects | map_values(type)]`, `[.[] | objects | any]`, `[.[] | objects | all]`, `[.[] | objects | add?]`,
	`[.[] | objects | min]`, `[.[] | objects | [.[]] | sort | length]`, `[.[] | objects | tostream] | length`, `[.[] | objects | [paths] | length]`,
	`[.[] | objects | flatten?]`, `[.[] | objects | reverse?]`, `[.[] | objects | sort?]`, `[.[] | objects | unique?]`, `[.[] | objects | join(",")?]`,
	`[.[] | objects | transpose?]`, `[.[] | objects | keys | length]`, `[.[] | objects | has("ID")]`, `[.[] | objects | length]`, `[.[] | objects | first?]`,
	`[.[] | objects | .[0]?]`, `[.[] | objects | .[1:]?]`, `[.[] | objects | .[]? | type]`, `[.[] | objects | splits("a")?]`, `[.[] | objects | tonumber?]`,
	`[.[] | objects | @csv?]`, `[.[] | objects | implode?]`, `[.[] | objects | ltrimstr("a") | type]`, `[.[] | objects | indices(1)?]`, `[.[] | objects | inside({})?]`,
	`[.[] | objects | bsearch(1)?]`, `[.[] | objects | min_by(.)?]`, `[.[] | objects | group_by(.)?]`, `[.[] | objects | getpath(["CUSTOMER","CITY"])?]`,
	`[.[] | objects | setpath(["ID"]; 0) | .ID]`, `[.[] | objects | delpaths([["ID"]]) | has("ID")]`, `[.[] | objects | to_entries | length]`,
	`[.[] | objects | . * .] | length`, `[.[] | objects | . + .] | length`, `[.[] | objects | (. - .)?]`, `[.[] | objects | (. / .)?]`, `[.[] | objects | (. % .)?]`,
	`[.[] | objects | -(.)?]`, `[.[] | objects | abs?]`, `[.[] | objects | floor?]`, `[.[] | objects | tojson | length]`, `[.[] | objects | ascii_downcase?]`,
	`[.[] | objects | test("a")?]`, `[.[] | objects | startswith("a")?]`, `[.[] | objects | split(",")?]`, `[.[] | objects | explode?]`, `[.[] | objects | utf8bytelength?]`,
	`[.[] | objects | toarray | length]`, `[.[] | objects | have_literal_numbers]`, `[.[] | objects | trim?]`, `[.[] | objects | mktime?]`, `[.[] | objects | strftime("%Y")?]`,
	`[.[] | objects | gmtime?]`, `[.[] | objects | todate?]`, `[.[] | objects | fromjson?]`, `[.[] | objects | tojson | fromjson | length]`, `[.[] | objects | getpath([])] | length`,
	`[.[] | objects | range(.)?]`, `[.[] | objects | limit(.; 1)?]`, `[.[] | objects | [limit(1; .[])] | length]`, `[.[] | objects | isvalid(.ID)]`, `[.[] | objects | error?]`,
	// error messages must match too
	`.[0] | .[0]`, `.[0] | .[1:]`, `.[0] | keys | .[99]`, `.NAME | .x`, `.[] | .NAME.x`, `.[0] | implode`, `.[0] | join(",")`, `.[0] | test("a")`, `.[0] | tonumber`,
	`.[0] | ascii_downcase`, `.[0] | ltrimstr(1)`, `.[0] | splits("a")`, `.[0] | flatten`, `.[0] | transpose`, `.[0] | reverse`, `.[0] | sort`, `.[0] | unique`,
	`.[0] | min_by(.)`, `.[0] | group_by(.)`, `.[0] | @csv`, `.[0] | @tsv`, `.[0] | @sh`, `.[0] | -(.)`, `.[0] - 1`, `.[0] / 1`, `.[0] % 1`, `.[0] * 2`, `1 - .[0]`, `.[0] + 1`,
	`.[0] | has(0)`, `.[0] | .[0:1]`, `.[0] | getpath([0])`, `.[0] | setpath([0]; 1)`, `.[0] | delpaths([[0]])`, `.[0] | bsearch(1)`, `.[0] | indices(1)`,
	`.[0] | index("a")`, `.[0] | contains("a")`, `.[0] | inside("a")`, `.[0] | startswith("a")`, `.[0] | split(",")`, `.[0] | explode`, `.[0] | abs`, `.[0] | floor`,
	`.[0] | pow(.; 2)`, `.[0] | tojson | tonumber`, `.[0] | range(.)`, `.[0] | error`, `.[0] | error(.)`, `.[0] | {(.): 1}`, `.[0] | .[.]`, `.[0] | .[[1]]`,
	`.[0] | has(.)`, `.[0] | mktime`, `.[0] | strftime("%Y")`, `.[0] | todate`, `.[0] | fromjson`, `.[0] | utf8bytelength`, `.[0] | trim`, `.[0] | add`, `.[0] | any`,
	`.[0] | all`, `.[0] | min`, `.[0] | max`, `.[0] | first`, `.[0] | last`, `.[0] | nth(0)`, `.[0] | to_entries | length`, `.[0] | with_entries(.) | length`,
	`.[0] | path(..) | length`, `.[0] | [paths] | length`, `.[0] | tostream | length`, `.[0] | limit(1; .[])`, `.[0] | input`, `.[0] | $__loc__`, `.[0] | ascii`,
	`path(.[0].NAME)`, `path(.[].NAME)`, `[path(..)] | length`, `path(.[0] | select(.ID == 1))`, `path(first(.[] | select(.ID == 2)))`, `[path(.[] | select(.ID == 2))]`,
	`path(.[0] | tojson)`, `path(.[1:][0])`, `path(.[1:3])`, `[path(.[] | .ITEMS[]?)]`, `path(getpath([0, "NAME"]))`, `[paths(objects)] | length`,
	`try path(.[0] | {a: 1} | .a) catch .`, `try path(.[] | tojson) catch .`, `path(.[0] // 1)`, `path(if .[0] then .[1] else .[2] end)`, `[path(.[]?)]`,
	`del(.[0] | select(.ID == 1))`, `del(..)`, `to_entries[0].value == .[0]`, `[.[] | . as $r | path(..) | length] | add`, `[.[] | [paths] | length]`,
	`getpath(path(.[0])) == .[0]`, `[.[0] | getpath(paths)] | length`, `reduce path(.[]) as $p (.; setpath($p; 1))`, `[splits(", ")?]`,
	`[.[] | . as [$a] ?// $a | $a | type]`, `[.[] | . as {a: $x} ?// [$x] | $x]`, `.[] as [$a, $b] | {a: $a, b: $b}`,
	`. as {a: $x} | $x`, `.[] as {$ID} | $ID`, `[.[] | .[]?] | length`, `[.[] | .[]? | .[]?] | length`, `[.[][]?] | length`, `.[0][]?`, `[.[]?[]?] | length`,
	`$ENV | type`, `@json "x\(.[0])y" | length`, `@html "\(.[0])" | length`, `@uri "\(.[0].NAME?)"`, `@sh "\(.[0].NAME?)"`, `@base64 "\(.[0].NAME?)"`,
	`"\(.[0]) and \(.[1])" | length`, `[.[] | tostring | length]`, `[.[] | tojson | length]`, `[.[] | @text | length]`, `tojson == (map(.) | tojson)`,
	`[.[] | objects | keys_unsorted | sort] == [.[] | objects | keys]`, `map(. == (tojson | fromjson))`, `[.[] | . == .]`, `[.[] | . < .]`, `unique == (map(.) | unique)`,
	`sort == (map(.) | sort)`, `group_by(.REGION) == (map(.) | group_by(.REGION))`, `(min, max) == (map(.) | (min, max))`, `add == (map(.) | add)`,
}

// TestLazyDifferentialPrograms runs every program against every document.
func TestLazyDifferentialPrograms(t *testing.T) {
	var documents []any
	for _, raw := range diffDocuments {
		decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
		decoder.UseNumber()
		var v any
		if err := decoder.Decode(&v); err != nil {
			t.Fatalf("bad test document %s: %v", raw, err)
		}
		documents = append(documents, v)
	}
	for _, program := range diffPrograms {
		if nondeterministic(program) {
			// env, input and $__loc__ behave the same for any input shape;
			// they stay in the list for the lazy-vs-native check below.
			for _, document := range documents {
				if native, lazy := runFork(program, document), runFork(program, lazify(document)); native != lazy {
					t.Errorf("fork differs between native and lazy input\nquery: %s\ninput: %s\n--- native\n%s--- lazy\n%s", program, previewJSON(document), native, lazy)
				}
			}
			continue
		}
		for _, document := range documents {
			checkDifferential(t, program, document)
		}
	}
}

// TestLazyValuesAreNotMaterializedByFieldAccess guards the point of the fork:
// reading a member or iterating must not convert the value.
func TestLazyValuesAreNotMaterializedByFieldAccess(t *testing.T) {
	record := &countingObject{testLazyObject{map[string]any{"ID": 1, "NAME": "ANNA", "CUSTOMER": map[string]any{"TIER": 2}}}, 0}
	set := &countingArray{testLazyArray{[]any{record, record, record}}, 0}
	for _, program := range []string{
		`.[] | select(.ID == 1) | .NAME`,
		`.[] | select(.CUSTOMER.TIER >= 2 and (.NAME | test("^A"))) | {ID, NAME}`,
		`map(.ID) | add`,
		`[.[] | .NAME] | unique`,
		`length`,
		`.[1].NAME`,
		`first(.[] | select(.ID == 1)) | .ID`,
		`reduce .[] as $r (0; . + $r.ID)`,
		`[.[] | pow(.ID; 2)]`,
		`[limit(2; .[])] | length`,
		`.[] | .MISSING // .ID`,
		`[.[] | type] | length`,
		`group_by(.ID) | map(length)`,
		`sort_by(.ID) | map(.NAME)`,
		`unique_by(.NAME) | length`,
		`min_by(.ID).NAME`,
		`[.[] | getpath(["CUSTOMER", "TIER"])]`,
	} {
		record.maps, set.slices = 0, 0
		if got := runFork(program, set); strings.Contains(got, "error") {
			t.Fatalf("%s: %s", program, got)
		}
		if record.maps != 0 {
			t.Errorf("%s: materialized a record %d times", program, record.maps)
		}
		if strings.HasPrefix(program, ".[]") || strings.HasPrefix(program, "map(") || strings.HasPrefix(program, "[") ||
			strings.HasPrefix(program, "length") || strings.HasPrefix(program, "first") || strings.HasPrefix(program, "reduce") || strings.HasPrefix(program, ".[1]") {
			if set.slices != 0 {
				t.Errorf("%s: materialized the array %d times", program, set.slices)
			}
		}
	}
}

type countingObject struct {
	testLazyObject
	maps int
}

func (o *countingObject) Map() map[string]any { o.maps++; return o.testLazyObject.Map() }

type countingArray struct {
	testLazyArray
	slices int
}

func (a *countingArray) Slice(lo, hi int) []any { a.slices++; return a.testLazyArray.Slice(lo, hi) }
