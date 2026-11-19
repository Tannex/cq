# gojq, forked for cq

This directory is a fork of [gojq](https://github.com/itchyny/gojq) v0.12.19
(MIT, see `LICENSE`). It exists so the jq console can query a data set
without first turning every record into Go maps: the interpreter accepts
values that are decoded on demand from data the caller keeps in its own form.

Queries behave exactly as in upstream gojq. That is a tested property, not an
intention: see "Keeping it honest" below.

## What is different from upstream

Changes are marked in the source with `// lazy:` or `// cq:` comments.

- **Lazy values** (`lazy.go`). `LazyObject` and `LazyArray` may be used
  wherever an object or array is accepted. Indexing, iterating, slicing,
  `length`, `type`, comparison (`LazyComparer`) and encoding work on them
  directly. Every other native function receives the ordinary
  `map[string]any` / `[]any` for its operands, converted one level at a time
  (`lazyPolicies`), so no function had to be taught what a lazy value is to
  stay correct. Functions that walk nested values (`add`, `flatten`,
  `transpose`, `getpath`/`setpath`/`delpaths`, deep merge, the row formats)
  convert at each level they visit.
- **Sorting** (`func.go`, `sortOrder`). `sort`, `sort_by`, `group_by`,
  `unique` and `unique_by` prepare their keys once, compute the order as a
  permutation with a stable merge sort that uses every core, and compare the
  usual key shapes directly. Upstream allocates an item per element and
  re-parses number tokens on every comparison.
- **Cancellation** (`execute.go`). The context is polled on entry and every
  256 instructions instead of on every instruction.
- **Concurrent runs of one compiled query** (`func.go`, `deleteEmpty`). cq
  evaluates one `Code` on several goroutines. Upstream's `del`/`delpaths`
  wrote back into every container it walked, including constants shared by
  all runs of the compiled code, which is a fatal concurrent map write; the
  fork writes only the containers the deletion copied.

One behavior can differ from upstream: `sort_by` and friends with several
`nan` keys. jq orders `nan` below everything including itself, so no sort
order is defined among them; the two sort algorithms settle it differently.

## Keeping it honest

- The upstream tests are kept and run against the fork.
- `lazy_diff_test.go` runs over a thousand programs — upstream's CLI test corpus
  (`testdata/upstream_cli_cases.json`, extracted from `cli/test.yaml`) and a
  list aimed at record queries — through upstream gojq and through the fork,
  on native input and on input where every object and array is lazy, and
  requires identical output and identical errors. The upstream module stays
  in `go.mod` for this purpose only.
- `lazy_sweep_test.go` applies every builtin, at every arity, to lazy values
  as input, as arguments, and nested inside containers the program builds,
  again against upstream. A sample runs by default; `CQ_LAZY_SWEEP=1` runs
  all of it (about 86,000 programs).
- `internal/recordset` repeats the comparison with real record sets, running
  each query sequentially and on several workers.

## Updating from upstream

Copy the new release's root `*.go` files, `builtin.jq` and `LICENSE` over
this directory, re-apply the marked changes, bump the upstream version in
`go.mod`, regenerate `testdata/upstream_cli_cases.json` from the release's
`cli/test.yaml`, and run the tests. A native function added upstream is
correct without further work, because unlisted functions get materialized
operands; list it in `lazyPolicies` only if it should avoid that cost.
