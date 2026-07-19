# cq — jq for COBOL

`cq` parses a COBOL copybook and either prints the record layout (byte
offset and length of every field) or decodes fixed-length EBCDIC records —
for example a binary dataset streamed straight from z/OS through the user's
Zowe configuration — into UTF-8 JSON. The output is plain JSON on stdout; a
built-in jq (`-q`) covers most filtering and reshaping, and anything else can
be piped into the real `jq`.

The separate **Compa/z** terminal browser (binary: `compaz`, formerly `cqt`)
navigates z/OSMF data sets, PDS/PDSE members, and bounded record windows. It can
display raw records or apply the same COBOL copybook parser as a table or
ordered JSON overlay.

```console
$ cq -c CUSTOMER.cpy                         # layout: offset/length of each field
$ cq -c CUSTOMER.cpy -d customer.bin         # decode records to a JSON array
$ cq --copybook-dsn "HQ.COPYLIB(CUSTOMER)" --data-dsn "HQ.CUSTOMER.DATA"
$ cq -q 'select(.BALANCE < 0)' -c CUSTOMER.cpy -d customer.bin
$ cq -where DTAR107-SALE -c DTAR107.cbl -d sales.bin
```

## Install

Download the archive for your platform from the
[latest GitHub release](https://github.com/Tannex/cq/releases/latest). Existing
`cq_VERSION_OS_ARCH` archives contain the `cq` CLI; separate
`compaz_VERSION_OS_ARCH` archives contain the terminal browser. Both are available
for Linux, macOS, and Windows on amd64 and arm64. Extract the archive you need
and place `cq`/`compaz` (or the corresponding `.exe` on Windows) on your `PATH`.
The attached `cq_VERSION_checksums.txt` covers both sets of release archives.

Alternatively, install either executable with Go, pinned to a
[release tag](https://github.com/Tannex/cq/releases) so you get a tested build:

```console
$ go install github.com/Tannex/cq@vX.Y.Z
$ go install github.com/Tannex/cq/cmd/compaz@vX.Y.Z
```

Confirm the installed versions with `cq --version` and `compaz --version`.
Building from `main` (`go install …@main`) gives the development version, which
may be unstable — prefer release binaries or tagged versions.

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
| `--version` | off | print the cq version and exit |
| `-codepage` | Zowe `encoding` for `--data-dsn`; otherwise `cp037` | EBCDIC codepage of the data (`cp037`, `cp277`, `cp1047`, `cp1140`, `cp1142`; `ascii`/`latin1` for testing); an explicit flag takes precedence |
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

## Compa/z — z/OSMF browser

> **Formerly `cqt`:** the terminal browser was renamed in this release. The
> binary is now `compaz`; flags, keys, and configuration are unchanged. Replace
> `cqt` with `compaz` in scripts and PATH installs.

The interface uses the [Ayu Dark](https://github.com/ayu-theme/ayu-colors)
color scheme (MIT license).

`compaz` uses the same Zowe team configuration, operating-system credential
entry, TLS settings, profile encoding, and `cq/config.json` file as `cq`. It
does not have a separate credential or application configuration. Run
`cq config` to create or edit the shared application config. Start `compaz` with
no copybook for a raw browser, or provide an optional display copybook:

```console
$ compaz
$ compaz --prefix 'IBMUSER.*'
$ compaz --prefix 'PROD.CUSTOMER.*' -c CUSTOMER.cpy --format fixed
$ compaz --copybook-dsn 'HQ.COPYLIB(CUSTOMER)' --record CUSTOMER-RECORD
```

```text
compaz [--prefix PREFIX]
    [-c COPYBOOK | --copybook COPYBOOK | --copybook-dsn DSN[(MEMBER)]]
    [--format auto|fixed|free] [--record NAME] [--codepage CODEPAGE]
    [--read-only]
```

A copybook is optional, but local and DSN copybook sources are mutually
exclusive. Codepage precedence is an explicit `--codepage`, the selected Zowe
profile's `encoding`, then `cp037`. The default data set search is
`<Zowe user>.*`; a token-only profile with no user opens with the prefix input
focused and sends no automatic query. Wildcards are never added implicitly:
spell out `*` or `%` for a prefix search, while a plain data set name looks up
exactly that catalog entry.

The data set screen keeps unsupported organizations visible. Enter opens PS,
PS-L (large-format sequential), or SEQ data sets directly as records and opens
PO/PDS or PO-E/PDSE data sets as a member list. Other DSORG values produce an
actionable warning and are not read. On the data set and member screens, `/`
edits the prefix or member filter; short literal member filters are expanded as
prefix patterns.

### Compa/z keys

| Key | Action |
| --- | --- |
| `↑`/`k`, `↓`/`j` | move the selected row |
| `PgUp`, `PgDn` | move by one visible page |
| `g`/`Home`, `G`/`End` | first or last row currently cached on this screen |
| `Enter` | open the selected data set/member, or accept focused input/dialog fields |
| `Esc` | return to the previous screen, or cancel focused input/dialog fields |
| `/` | edit the data set prefix or member filter |
| `e` | edit the selected sequential data set or member (explicit save only) |
| `r` | clear and refresh the current screen cache |
| `c` | open the copybook overlay dialog on the record screen |
| `x` | clear the active copybook overlay |
| `o` | toggle raw records and the active copybook overlay |
| `v` | toggle copybook table and pretty JSON views without refetching records |
| `d` | show or hide the selected record/field diagnostic |
| `:` | open the jq query popup on the record screen (needs a copybook overlay) |
| `F10` | scroll wide raw, table, or JSON data left |
| `F11` | scroll wide raw, table, or JSON data right |
| `?` | toggle expanded help |
| `q`, `Ctrl-C` | quit |

`F10` and `F11` are reserved exclusively for horizontal data movement and are
not reused in any mode. Global shortcuts are suppressed while a search input
or copybook dialog is focused; use `Tab`/`Shift-Tab` to move between dialog
fields.

### Bounded 2× requests and screen cache

For each terminal size, `compaz` calculates the data rows visible after the fixed
title, search/breadcrumb, table header, status/detail, and help lines. The
maximum size of **each individual z/OSMF request** is exactly
`2 × visible rows`:

- every data set list, member list, and record read is sent with that exact
  maximum item/count value;
- fetched rows are retained in memory for the lifetime of the current browse
  screen, so moving backward through previously visited data does not refetch;
- when the selection enters the final visible page of cached rows, `compaz`
  prefetches the next bounded request instead of waiting for the last row;
- opening a child screen retains its parent cache; `Esc` releases the child
  cache when returning to the parent. Prefix/member-filter changes and `r`
  clear and restart the active cache; and
- when the terminal is too small to have a positive row count, `compaz` cancels
  pending row work, retains already cached rows, displays a resize instruction,
  and dispatches no row fetch. Restoring the terminal shows the cache
  immediately.

A screen cache can therefore grow beyond `2 × visible rows`; the cap applies to
network transfers, not accumulated session memory. The status line reports both
the cached range and the current per-request fetch budget. Its spinner occupies
a permanently reserved cell, so loading transitions do not shift status text.

Record browsing uses only z/OSMF record ranges. Unlike the `cq --data-dsn`
streaming optimization, `compaz` never falls back to a whole-data-set download,
because doing so would violate the request cap. Copybook source files are
metadata for the display overlay and are not record-cache rows.

### Copybook and display modes

Raw mode is always available and preserves the fixed record/range gutter. Load
a copybook at startup with `-c`/`--copybook` or `--copybook-dsn`, or press `c`
to choose a local file or DSN, source format, and optional 01-level record.
Nested `COPY MEMBER.` statements use the ordered `dsnSearchPath` libraries in
the shared `cq/config.json`; this requires the active z/OSMF session even when
the top-level copybook is local. When the top-level copybook DSN itself is not
found (404), its member name — from `DSN(MEMBER)` or a bare member-sized
name — is searched through the same libraries, so mappings survive a copybook
moving to another library.

With an overlay active, `:` opens the jq query console over the records
screen; `ctrl+space` completes field names with the quoting jq needs (dashes
in COBOL names become `."CUST-TYPE"`). Queries stream through the bounded
record pages; when that paging runs longer than ten seconds, the whole data
set is downloaded once in a single record-mode request to a temporary file and
the search finishes from there. The download is reused for re-runs and deleted
when the console closes.

A valid overlay persists while browsing data sets and members. If a replacement
copybook fails to load or parse, the previous valid overlay remains active.
Press `o` for raw versus overlay, `v` for flattened table versus pretty ordered
JSON, and `x` to clear the overlay. OCCURS values remain compact JSON cells in
table mode. A structural row failure automatically falls back to that record's
raw display.

`cq` remains the strict batch/CLI decoder: malformed zoned, packed, or
separate-sign data stops decoding with an error, and its established text
semantics are unchanged. `compaz` uses an explicitly lenient display decoder so
the browser remains usable on imperfect operational data:

- every text or edited `0x00` LOW-VALUE byte, including trailing bytes, is shown
  as `·` and diagnosed;
- malformed zoned, packed, or separate-sign scalar cells become visible strings
  containing replacement markers, are prefixed with `!` in table view, and keep
  field path, byte offset/length, bounded raw hex, and the original error in the
  diagnostic view;
- valid numeric fields remain exact JSON numbers; and
- short records, impossible OCCURS DEPENDING ON counters, and other
  layout-determining failures remain row-level errors with a raw fallback.

### Edit mode

Press `e` on a sequential (PS) data set or a PDS member to open its text
content in an editor. Nothing is ever written implicitly: the buffer only
reaches the host through `Ctrl-S`, which validates every line against the data
set's record length before sending an `If-Match` conditional write. If the data
set changed on the host after it was fetched, the save fails cleanly, the
buffer is kept, and `Ctrl-R` reloads from the host (explicitly discarding the
buffer). Leaving the editor with unsaved changes requires a confirmation (`d`
discards, `Esc` keeps editing); a clean buffer closes immediately with `Esc`.
Load libraries and other undefined-format content are not editable.

Some terminals reserve `Ctrl-S` for flow control (XOFF); run `stty -ixon` to
free it. Start `compaz --read-only` to disable edit mode entirely and restore the
strictly read-only console guarantee. Browsing itself remains read-only: the
z/OSMF browser interface exposes only list/read/fetch methods, and the write
surface is a separate opt-in interface used exclusively by the editor. The
separate `cq` command and its existing behavior and installation path are
unchanged.

## Using Zowe

Both `cq` and `compaz` connect directly to z/OSMF through the shared native Go
transport; there is no Node.js or npm setup. They read the same Zowe team
configuration and operating-system credential entry as Zowe CLI and Zowe
Explorer. For example, `cq` can stream both sources directly:

```console
$ cq --copybook-dsn "HQ.COPYLIB(CUSTOMER)" --data-dsn "HQ.CUSTOMER.DATA"
```

A Zowe team configuration (from Zowe CLI or Zowe Explorer) must exist. Both
executables load the global `zowe.config.json` and `zowe.config.user.json` from
`$ZOWE_CLI_HOME` (or `$HOME/.zowe`) and the nearest project pair found by
walking up from the current directory. It supports:

- global/project team and user layer precedence;
- base, nested, default, and `ZOWE_OPT_ZOSMF_PROFILE`-selected `zosmf`
  profiles;
- `ZOWE_OPT_HOST`, `PORT`, `BASE_PATH`, `PROTOCOL`, `USER`, `PASSWORD`,
  `TOKEN_TYPE`, `TOKEN_VALUE`, `REJECT_UNAUTHORIZED`, and `ENCODING`
  overrides;
- plain properties and Zowe's `secure_config_props` entry in macOS Keychain,
  Windows Credential Manager, or Secret Service/libsecret on Linux; and
- JSON-with-comments and trailing commas, as accepted by Zowe's config
  reader.

HTTPS connections verify the server certificate by default. When a profile
explicitly sets `rejectUnauthorized: false`, both executables follow that
setting and disable certificate verification for their z/OSMF connection. This
permits man-in-the-middle attacks; prefer installing the z/OSMF certificate
authority in the operating-system trust store.

Neither executable currently loads Zowe V1 profiles, arbitrary Imperative
credential-manager plug-ins, or client-certificate identities. Those
configurations must be migrated to a supported Zowe team configuration before
using DSN sources or the browser.

`cq` and `compaz` are part of an independent project that is not affiliated with
or endorsed by The Linux Foundation or the Zowe project. Zowe® is a registered
trademark of The Linux Foundation.

The copybook is fetched as text so z/OSMF converts its EBCDIC source, while
the data set is streamed in binary mode to preserve packed and binary fields
— records decode as bytes arrive, with no temporary file. Zowe
authentication, profiles, and connection settings continue to come from the
user's normal Zowe configuration. For `--data-dsn`, the resolved `zosmf`
profile's `encoding` property selects the decoder codepage unless `-codepage`
is supplied; cq falls back to `cp037` when the property is absent.

### Nested copybooks

When a copybook contains `COPY MEMBER.`, cq resolves the member recursively
through the ordered libraries in `dsnSearchPath`. The same chain is tried for
`--copybook-dsn` itself when z/OSMF reports it missing (404), using the member
name from `DSN(MEMBER)` or a bare member-sized name. By default, `cq` reads
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

## Releasing

GitHub Actions tests every push to `main` and every pull request. To publish a
release, create and push a semantic-version tag from the commit to release:

```console
$ git tag -a v1.2.3 -m "v1.2.3"
$ git push origin v1.2.3
```

The release workflow reruns the tests, builds unchanged `cq_VERSION_OS_ARCH`
archives and separate `compaz_VERSION_OS_ARCH` archives for all supported targets,
embeds the same tag for both `cq --version` and `compaz --version`, generates one
SHA-256 checksum file covering both archive sets, creates release notes, and
publishes the files on the repository's GitHub Releases page.
