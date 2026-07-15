# cq — jq for COBOL

`cq` parses a COBOL copybook and either prints the record layout (byte
offset and length of every field) or decodes fixed-length EBCDIC records —
for example a binary dataset streamed straight from z/OS through the user's
Zowe configuration — into UTF-8 JSON. The
output is plain JSON on stdout; a built-in jq (`-q`) covers most filtering
and reshaping, and anything else can be piped into the real `jq`.

```console
$ cq -c CUSTOMER.cpy                         # layout: offset/length of each field
$ cq -c CUSTOMER.cpy -d customer.bin         # decode records to a JSON array
$ cq --copybook-dsn "HQ.COPYLIB(CUSTOMER)" --data-dsn "HQ.CUSTOMER.DATA"
$ cq -q 'select(.BALANCE < 0)' -c CUSTOMER.cpy -d customer.bin
$ cq -where DTAR107-SALE -c DTAR107.cbl -d sales.bin
```

## Install

```console
$ go install github.com/Tannex/cq@latest
```

## Usage

```
cq [flags] (-c COPYBOOK | --copybook-dsn DSN[(MEMBER)])
           [-d DATA | --data-dsn DSN | DATA]
```

Provide one copybook source: a local `-c COPYBOOK`, or `--copybook-dsn` to
fetch a data set or PDS member through Zowe (see the Zowe section). Data can
come from `-d DATA`, a trailing `DATA` argument, stdin via a trailing `-`, or
a binary Zowe stream selected by `--data-dsn`. Records are fixed-length,
derived from the copybook; use `-lrecl` if the physical records carry trailing
padding.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-c` | one copybook source required | local copybook file |
| `--copybook-dsn` | one copybook source required | data set or PDS member fetched as text through Zowe (z/OSMF) |
| `-d` | none | data file to decode (`-` for stdin); omit for layout output |
| `--data-dsn` | none | data set streamed in binary mode through Zowe (z/OSMF) |
| `-codepage` | `cp037` | EBCDIC codepage of the data (`cp037`, `cp277`, `cp1047`, `cp1140`, `cp1142`; `ascii`/`latin1` for testing) |
| `-format` | `auto` | copybook source format: `fixed` (cols 7–72), `free`, or `auto` |
| `--verbose` | off | write debug information (config, Zowe calls, timings) to stderr |
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

## Using Zowe

DSN sources are streamed directly from z/OSMF by cq's native Go transport.
There is no Node.js or npm setup. cq reads the same Zowe team configuration
and operating-system credential entry as Zowe CLI and Zowe Explorer, then:

```console
$ cq --copybook-dsn "HQ.COPYLIB(CUSTOMER)" --data-dsn "HQ.CUSTOMER.DATA"
```

A Zowe team configuration (from Zowe CLI or Zowe Explorer) must exist. cq
loads the global `zowe.config.json` and `zowe.config.user.json` from
`$ZOWE_CLI_HOME` (or `$HOME/.zowe`) and the nearest project pair found by
walking up from the current directory. It supports:

- global/project team and user layer precedence;
- base, nested, default, and `ZOWE_OPT_ZOSMF_PROFILE`-selected `zosmf`
  profiles;
- `ZOWE_OPT_HOST`, `PORT`, `BASE_PATH`, `PROTOCOL`, `USER`, `PASSWORD`,
  `TOKEN_TYPE`, `TOKEN_VALUE`, and `REJECT_UNAUTHORIZED` overrides;
- plain properties and Zowe's `secure_config_props` entry in macOS Keychain,
  Windows Credential Manager, or Secret Service/libsecret on Linux; and
- JSON-with-comments and trailing commas, as accepted by Zowe's config
  reader.

HTTPS connections always verify the server certificate. Profiles with
`rejectUnauthorized: false` are rejected; install the z/OSMF certificate
authority in the operating-system trust store instead.

cq does not currently load Zowe V1 profiles, arbitrary Imperative
credential-manager plug-ins, or client-certificate identities. Those
configurations must be migrated to a supported Zowe team configuration before
using DSN sources.

`cq` is an independent project and is not affiliated with or endorsed by The
Linux Foundation or the Zowe project. Zowe® is a registered trademark of The
Linux Foundation.

The copybook is fetched as text so z/OSMF converts its EBCDIC source, while
the data set is streamed in binary mode to preserve packed and binary fields
— records decode as bytes arrive, with no temporary file. Zowe
authentication, profiles, and connection settings continue to come from the
user's normal Zowe configuration.

### Nested copybooks

When a copybook contains `COPY MEMBER.`, cq resolves the member recursively
through the ordered libraries in `dsnSearchPath`. By default, `cq` reads
`config.json` from its platform user configuration directory:

- Linux: `$XDG_CONFIG_HOME/cq/config.json`, or `$HOME/.config/cq/config.json`
- macOS: `$HOME/Library/Application Support/cq/config.json`
- Windows: `%AppData%\cq\config.json`

Create that file with the ordered library search path:

```json
{
  "dsnSearchPath": [
    "HQL.CPY.SRC",
    "HQL.COB.SRC"
  ]
}
```

Run `cq config` to create this file when absent and open it in `$VISUAL`,
`$EDITOR`, or the platform text editor.

For `COPY ADDRESS.`, cq requests `HQL.CPY.SRC(ADDRESS)` and
`HQL.COB.SRC(ADDRESS)` in parallel and keeps the result from the earliest
library in the list that has the member, so the configured order still
decides which copy wins. Members named at the same nesting level are also
fetched in parallel, with at most eight Zowe requests in flight at once.
Resolved members (and failed lookups) are cached for the command, nested
`COPY` statements use the same search order, and cycles are reported with the
full member chain. The current scope supports plain `COPY MEMBER.`
statements; `REPLACING`, `OF`, and `IN` clauses are rejected explicitly.

Input containing a non-comment `PROCEDURE DIVISION` is rejected as `Not a
copybook` before its `COPY` statements are expanded.

Local files remain available:

```console
$ zowe zos-files download data-set "HQ.COPYLIB(CUSTOMER)" --file CUSTOMER.cpy
$ zowe zos-files download data-set "HQ.CUSTOMER.DATA" --binary --file customer.bin
$ cq -c CUSTOMER.cpy -d customer.bin
```

Everyday variations:

```console
# Peek at the first few records of a big dataset
$ cq -max 5 -pretty --copybook-dsn "HQ.COPYLIB(CUSTOMER)" \
    --data-dsn "HQ.CUSTOMER.DATA"

# Pull only the sales out of a transaction file
$ cq -where DTAR107-SALE -c DTAR107.cbl --data-dsn "PROD.DAILY.TXNS"

# Sum an amount field across the whole dataset
$ cq -q '.["DTAR107-AMOUNT"]' -c DTAR107.cbl \
    --data-dsn "PROD.DAILY.TXNS" | jq -s add

# Download once, slice locally many times
$ zowe zos-files download data-set "HQ.CUSTOMER.DATA" --binary --file customer.bin
$ cq -r -q '.["CUST-NAME"]' -c CUSTOMER.cpy -d customer.bin
```

Do not pipe `zowe zos-files view data-set --binary` into `cq`: the view path can
alter packed and binary bytes. Use `--data-dsn` or `zowe zos-files download
data-set --binary --file` instead. For a manual POSIX pipeline, download to
`/dev/stdout` and enable `pipefail`, because `cq` cannot observe an upstream
process failure through stdin:

```console
$ set -o pipefail
$ zowe zos-files download data-set "HQ.CUSTOMER.DATA" --binary \
    --file /dev/stdout --overwrite \
    | cq -c CUSTOMER.cpy -
```

If the dataset's LRECL is larger than the copybook layout (padded FB records),
pass `-lrecl` with the dataset's record length. With `-max N` (and no
`-where`), a `--data-dsn` transfer is bounded server-side with a z/OSMF
record range, so peeking at a huge dataset moves only the records asked for;
the native transport verifies the dataset's record length matches the layout
and transparently falls back to a full streamed transfer (canceled once
`-max` records have decoded) when it does not.

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
- Recursive `COPY MEMBER.` expansion through configured Zowe DSN search paths
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
