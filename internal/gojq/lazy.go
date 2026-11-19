package gojq

// This file is the core of the cq fork of gojq: values that are produced on
// demand from data the caller keeps in its own representation.
//
// Upstream gojq only understands a closed set of Go types and callers have to
// build map[string]any / []any trees before running a query. For a data set of
// millions of records that tree dwarfs the raw data and building it dominates
// every query. A LazyObject or LazyArray may be used anywhere an object or an
// array is accepted instead: the interpreter reads only the members a query
// touches, and converts to the native containers (Map, Slice) at any site that
// has no lazy-aware path. Query results are therefore identical to running
// the same query on the fully materialized value.
//
// Implementations must be pointer types (the interpreter compares values by
// identity while tracking paths) and must be immutable while a query runs.

// LazyObject is a read-only object whose members are produced on demand.
type LazyObject interface {
	// Get returns the member named key, or nil when there is no such member
	// (indexing a missing key yields null in jq).
	Get(key string) any
	// Map materializes the object. The result must not alias mutable state
	// of the receiver; members may themselves be lazy values.
	Map() map[string]any
}

// LazyArray is a read-only array whose elements are produced on demand.
type LazyArray interface {
	// Len returns the number of elements.
	Len() int
	// At returns element i, where 0 <= i < Len().
	At(i int) any
	// Slice materializes the elements start <= i < end, where
	// 0 <= start <= end <= Len(). Elements may themselves be lazy values.
	Slice(start, end int) []any
}

// LazyComparer may be implemented by a lazy value to order itself against
// another value without materializing either side. It returns ok == false to
// defer to the generic comparison. When ok is true the result must equal
// Compare applied to the materialized values.
type LazyComparer interface {
	CompareLazy(other any) (result int, ok bool)
}

// Materialize converts a lazy value to gojq's native container for its type
// without descending into its members; any other value is returned as is.
func Materialize(v any) any {
	switch v := v.(type) {
	case LazyObject:
		return v.Map()
	case LazyArray:
		return v.Slice(0, v.Len())
	default:
		return v
	}
}

// unlazy is the short internal spelling of Materialize.
func unlazy(v any) any {
	switch v := v.(type) {
	case LazyObject:
		return v.Map()
	case LazyArray:
		return v.Slice(0, v.Len())
	default:
		return v
	}
}

// unlazyArray materializes a lazy array and leaves everything else, lazy
// objects included, untouched.
func unlazyArray(v any) any {
	if a, ok := v.(LazyArray); ok {
		return a.Slice(0, a.Len())
	}
	return v
}

// lazyArrayIter iterates a lazy array without materializing it.
type lazyArrayIter struct {
	array LazyArray
	index int
}

func (iter *lazyArrayIter) Next() (any, bool) {
	if iter.index >= iter.array.Len() {
		return nil, false
	}
	v := iter.array.At(iter.index)
	iter.index++
	return v, true
}

// lazyPolicy says which operands of a native function are materialized
// before the function runs.
type lazyPolicy int

const (
	// lazyMaterialize converts the input and every argument (one level).
	// It is the default, which keeps every function correct without having
	// to know about lazy values.
	lazyMaterialize lazyPolicy = iota
	// lazyArgs converts the arguments only: the function either ignores
	// its input or handles lazy inputs itself.
	lazyArgs
	// lazyRaw converts nothing: the function handles lazy values itself.
	lazyRaw
)

// lazyPolicies lists the native functions that do not need the default
// conversion. Getting an entry wrong in the cautious direction only costs
// time; a function listed here must really cope with lazy operands.
var lazyPolicies = map[string]lazyPolicy{
	// Lazy-aware functions.
	"length":     lazyRaw,
	"type":       lazyRaw,
	"tojson":     lazyRaw,
	"tostring":   lazyRaw,
	"_index":     lazyRaw,
	"_slice":     lazyRaw,
	"error":      lazyRaw,
	"halt_error": lazyRaw,

	// Binary operators ignore their input and convert their operands in
	// binopTypeSwitch, after a LazyComparer had its chance.
	"_add":         lazyRaw,
	"_subtract":    lazyRaw,
	"_multiply":    lazyRaw,
	"_divide":      lazyRaw,
	"_modulo":      lazyRaw,
	"_alternative": lazyRaw,
	"_equal":       lazyRaw,
	"_notequal":    lazyRaw,
	"_greater":     lazyRaw,
	"_less":        lazyRaw,
	"_greatereq":   lazyRaw,
	"_lesseq":      lazyRaw,

	// Path functions convert each level they visit themselves.
	"getpath":  lazyArgs,
	"setpath":  lazyArgs,
	"delpaths": lazyArgs,

	// Functions that ignore their input.
	"_range":     lazyArgs,
	"now":        lazyArgs,
	"infinite":   lazyArgs,
	"nan":        lazyArgs,
	"halt":       lazyArgs,
	"atan2":      lazyArgs,
	"copysign":   lazyArgs,
	"drem":       lazyArgs,
	"fdim":       lazyArgs,
	"fmax":       lazyArgs,
	"fmin":       lazyArgs,
	"fmod":       lazyArgs,
	"hypot":      lazyArgs,
	"jn":         lazyArgs,
	"nextafter":  lazyArgs,
	"nexttoward": lazyArgs,
	"remainder":  lazyArgs,
	"ldexp":      lazyArgs,
	"scalb":      lazyArgs,
	"scalbln":    lazyArgs,
	"yn":         lazyArgs,
	"pow":        lazyArgs,
	"fma":        lazyArgs,
}

// withLazyPolicy wraps a native callback so that it only ever sees the
// operand shapes its policy promises. Arguments are converted in place: the
// interpreter reads them back after the call while tracking paths.
func withLazyPolicy(policy lazyPolicy, callback func(any, []any) any) func(any, []any) any {
	switch {
	case callback == nil, policy == lazyRaw:
		return callback
	case policy == lazyArgs:
		return func(v any, args []any) any {
			for i, arg := range args {
				args[i] = unlazy(arg)
			}
			return callback(v, args)
		}
	default:
		return func(v any, args []any) any {
			for i, arg := range args {
				args[i] = unlazy(arg)
			}
			return callback(unlazy(v), args)
		}
	}
}
