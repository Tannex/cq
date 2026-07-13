# cq — jq for COBOL

`cq` parses a COBOL copybook and either prints the record layout (byte
offset and length of every field) or decodes fixed-length EBCDIC records —
for example a binary dataset downloaded with Zowe CLI — into a UTF-8 JSON
array. The output is plain JSON on stdout, made to be piped into `jq`.

```console
$ cq CUSTOMER.cpy                     # layout: offset/length of each field
$ cq CUSTOMER.cpy customer.bin        # decode records to a JSON array
$ cq -q 'select(.BALANCE < 0)' CUSTOMER.cpy customer.bin   # built-in jq
$ zowe zos-files download ds "HQ.CUSTOMER.DATA" --binary --file - \
    | cq CUSTOMER.cpy - | jq '.[] | select(.BALANCE < 0)'
```

## Install

```console
$ go install github.com/Tannex/cq@latest
```

## Usage

```
cq [flags] COPYBOOK [DATA]
```

With only a `COPYBOOK`, cq prints the layout. With `DATA` (a file, or `-`
for stdin), it decodes the records. Records are fixed-length, derived from
the copybook; use `-lrecl` if the physical records carry trailing padding.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-codepage` | `cp037` | EBCDIC codepage of the data (`cp037`, `cp1047`, `cp1140`; `ascii`/`latin1` for testing) |
| `-format` | `auto` | copybook source format: `fixed` (cols 7–72), `free`, or `auto` |
| `-record` | first | which 01-level record to decode when the copybook has several |
| `-pretty` | off | indent JSON output |
| `-fillers` | off | include FILLER fields in decoded output |
| `-max` | all | decode at most N records |
| `-lrecl` | layout | physical record length when it exceeds the layout |
| `-q` | none | jq expression (full jq language via [gojq](https://github.com/itchyny/gojq)) |
| `-r` | off | with `-q`, print string results raw instead of JSON-quoted |

### Layout output

```console
$ cq CUSTOMER.cpy | jq '.[0].fields[] | {name, offset, length, kind}'
{"name":"CUST-NO","offset":0,"length":5,"kind":"zoned"}
{"name":"CUST-NAME","offset":5,"length":10,"kind":"text"}
{"name":"BALANCE","offset":15,"length":4,"kind":"packed"}
```

Offsets are 0-based. `length` is the size of one element; array fields also
carry `occurs`. Kinds: `text`, `zoned`, `packed` (COMP-3), `binary` (COMP),
`float` (COMP-1/2), `edited`, `group`.

### Decode output

One JSON object per record, fields in copybook order. Numbers (zoned,
packed, binary) become JSON numbers with the implied decimal point applied;
`PIC X` fields become strings with trailing spaces trimmed; `OCCURS` become
arrays; groups become nested objects.

### Queries (`-q`)

`-q` embeds the full jq language, so no external `jq` is needed. When
decoding, the expression runs **per record** (`.` is one record object) and
the output is a stream of results, one per line, instead of a wrapped
array — so `select()` filters millions of records without buffering:

```console
$ cq -q 'select(.BALANCE < 0)' CUSTOMER.cpy customer.bin
$ cq -r -q '.["CUST-NAME"]' CUSTOMER.cpy customer.bin
ALICE
BOB
```

In layout mode the expression runs against the layout document:

```console
$ cq -r -q '.[0].fields[] | "\(.offset)\t\(.length)\t\(.name)"' CUSTOMER.cpy
0	5	CUST-NO
5	10	CUST-NAME
```

Note that COBOL names need `.["CUST-NAME"]` (or `."CUST-NAME"`) syntax,
since `-` is subtraction in jq. `halt` and `halt_error` work; query results
print object keys in sorted order (plain decode output keeps copybook
order).

## Supported COBOL

- Levels 01–49 and 77, FILLER, level-88 (reported in the layout as
  `conditions`), level-66 RENAMES (skipped)
- `PIC` X/A/9/S/V/P and numeric-edited pictures (decoded as text)
- `USAGE` DISPLAY, COMP/COMP-4/COMP-5/BINARY, COMP-3/PACKED-DECIMAL,
  COMP-1/COMP-2 (decoded as big-endian IEEE; IBM hex float not yet)
- `REDEFINES` (all views are decoded), `SYNC` alignment,
  `SIGN LEADING/TRAILING [SEPARATE]`
- `OCCURS`, including `OCCURS ... DEPENDING ON` when the variable table is
  the trailing storage of the record
- Fixed (columns 7–72) and free source formats, auto-detected; sequence
  numbers and comment lines handled

Not yet: multiple `OCCURS DEPENDING ON` per record or ODO followed by other
fields, IBM hexadecimal floating point, `USAGE POINTER/INDEX`, national
(PIC N) widths, RDW-prefixed (VB) files, codepages beyond those shipped by
`golang.org/x/text` (cp037, cp1047, cp1140).

## Test data

Copybooks under `testdata/copybooks/cb2xml/` are borrowed from the
[cb2xml](https://github.com/bmTas/cb2xml) project (LGPL) as test fixtures;
its expected-XML outputs are used to cross-check field offsets. See
`testdata/copybooks/cb2xml/SOURCES.md`.
