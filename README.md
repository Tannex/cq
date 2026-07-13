# cq — jq for COBOL

`cq` parses a COBOL copybook and either prints the record layout (byte
offset and length of every field) or decodes fixed-length EBCDIC records —
for example a binary dataset fetched with Zowe CLI — into UTF-8 JSON. The
output is plain JSON on stdout; a built-in jq (`-q`) covers most filtering
and reshaping, and anything else can be piped into the real `jq`.

```console
$ cq -c CUSTOMER.cpy                         # layout: offset/length of each field
$ cq -c CUSTOMER.cpy -d customer.bin         # decode records to a JSON array
$ cq -q 'select(.BALANCE < 0)' -c CUSTOMER.cpy -d customer.bin
$ cq -where DTAR107-SALE -c DTAR107.cbl -d sales.bin
$ zowe zos-files view data-set "HQ.CUSTOMER.DATA" --binary \
    | cq -c CUSTOMER.cpy -
```

## Install

```console
$ go install github.com/Tannex/cq@latest
```

## Usage

```
cq [flags] -c COPYBOOK [-d DATA]
cq [flags] -c COPYBOOK [DATA]
```

With only `-c COPYBOOK`, cq prints the layout. Use `-d DATA` for a named data
file, or one trailing `DATA` argument to decode records. A trailing `-` is the
canonical stdin form. Records are fixed-length, derived from the copybook; use
`-lrecl` if the physical records carry trailing padding.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-c` | required | copybook file |
| `-d` | none | data file to decode (`-` for stdin); omit for layout output |
| `-codepage` | `cp037` | EBCDIC codepage of the data (`cp037`, `cp277`, `cp1047`, `cp1140`, `cp1142`; `ascii`/`latin1` for testing) |
| `-format` | `auto` | copybook source format: `fixed` (cols 7–72), `free`, or `auto` |
| `-record` | first | which 01-level record to decode when the copybook has several |
| `-pretty` | off | indent JSON output |
| `-fillers` | off | include FILLER fields in decoded output |
| `-max` | all | decode at most N records |
| `-lrecl` | layout size | physical record length when it exceeds the layout |
| `-q` | none | jq expression (full jq language via [gojq](https://github.com/itchyny/gojq)) |
| `-r` | off | with `-q`, print string results raw instead of JSON-quoted |
| `-where` | none | keep only records satisfying a level-88 condition; `!`/`not ` negates; repeat to AND |

### Layout output

```console
$ cq -c CUSTOMER.cpy | jq '.[0].fields[] | {name, offset, length, kind}'
{"name":"CUST-NO","offset":0,"length":5,"kind":"zoned"}
{"name":"CUST-NAME","offset":5,"length":10,"kind":"text"}
{"name":"BALANCE","offset":15,"length":4,"kind":"packed"}
```

Offsets are 0-based. `length` is the size of one element; array fields also
carry `occurs`. Kinds: `text`, `zoned`, `packed` (COMP-3), `binary` (COMP),
`float` (COMP-1/2), `edited`, `group`. Level-88 condition names appear on
their field under `conditions`, with their VALUE literals and THRU ranges.

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
$ cq -q 'select(.BALANCE < 0)' -c CUSTOMER.cpy -d customer.bin
$ cq -r -q '.["CUST-NAME"]' -c CUSTOMER.cpy -d customer.bin
ALICE
BOB
```

In layout mode the expression runs against the layout document:

```console
$ cq -r -q '.[0].fields[] | "\(.offset)\t\(.length)\t\(.name)"' -c CUSTOMER.cpy
0	5	CUST-NO
5	10	CUST-NAME
```

Note that COBOL names need `.["CUST-NAME"]` (or `."CUST-NAME"`) syntax,
since `-` is subtraction in jq. `halt` and `halt_error` work; query results
print object keys in sorted order (plain decode output keeps copybook
order).

### Level-88 filters (`-where`)

Copybooks already define their business vocabulary as level-88 condition
names — `cq` lets you filter by them directly, so nobody has to remember
that a sale is `TRANS-TYPE = 1`:

```console
$ cq -where DTAR107-SALE -c DTAR107.cbl -d sales.bin      # 88 ... VALUE 1
$ cq -where 'not DTAR107-VOID' -c DTAR107.cbl -d sales.bin
$ cq -where NSW-POSTCODE -c VENDOR.cbl -d vendors.bin     # VALUE 2000 THRU 2999
```

Negate with a leading `!` or `not `; repeat the flag to AND conditions.
`VALUE a THRU b` ranges and the figurative constants ZERO/SPACES/QUOTE are
supported; matching is exact for numeric fields (full packed-decimal
precision) and trailing-space-insensitive for text. Filtering happens on
the raw bytes before any JSON is built, and combines with `-q` (filter
first, query after). Conditions inside `OCCURS` tables aren't supported
yet.

## Piping with Zowe CLI

`cq` is built to sit at the end of a Zowe pipe. Fetch the copybook once —
as **text**, so Zowe converts the EBCDIC source to UTF-8 — then stream the
data as **binary**, so the bytes arrive untouched and `cq` does the
decoding:

```console
$ zowe zos-files download data-set "HQ.COPYLIB(CUSTOMER)" --file CUSTOMER.cpy
$ zowe zos-files view data-set "HQ.CUSTOMER.DATA" --binary | cq -c CUSTOMER.cpy -
```

Everyday variations:

```console
# Peek at the first few records of a big dataset
$ zowe zos-files view data-set "HQ.CUSTOMER.DATA" --binary \
    | cq -max 5 -pretty -c CUSTOMER.cpy -

# Pull only the sales out of a transaction file
$ zowe zos-files view data-set "PROD.DAILY.TXNS" --binary \
    | cq -where DTAR107-SALE -c DTAR107.cbl -

# Sum an amount field across the whole dataset
$ zowe zos-files view data-set "PROD.DAILY.TXNS" --binary \
    | cq -q '.["DTAR107-AMOUNT"]' -c DTAR107.cbl - | jq -s add

# Download once, slice locally many times
$ zowe zos-files download data-set "HQ.CUSTOMER.DATA" --binary --file customer.bin
$ cq -r -q '.["CUST-NAME"]' -c CUSTOMER.cpy -d customer.bin
```

Two things to keep straight: always use `--binary` for the data (without it
Zowe converts EBCDIC to ASCII and inserts newlines, corrupting packed and
binary fields), and if the dataset's LRECL is larger than the copybook
layout (padded FB records), pass `-lrecl` with the dataset's record length.

## Supported COBOL

- Levels 01–49 and 77, FILLER, level-88 (listed in the layout and usable
  as `-where` filters), level-66 RENAMES (skipped)
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
(PIC N) widths, RDW-prefixed (VB) files.

Codepages: cp037 (default), cp277 (Denmark/Norway), cp1047, cp1140, and
cp1142 (277 with euro). Names are matched loosely — `IBM-277`, `ibm277`,
`cp277` and `277` all work. Other EBCDIC pages are a 256-entry table away;
open an issue.

## Test data

Copybooks and expected XML under `testdata/copybooks/cb2xml/` are unmodified
fixtures from the [cb2xml](https://github.com/bmTas/cb2xml) project under
LGPL-2.1. They are used to cross-check field offsets; the pinned source
revision, per-file origins, and complete license text are recorded in
`testdata/copybooks/cb2xml/SOURCES.md`.
