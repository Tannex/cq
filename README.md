# cq — jq for COBOL

`cq` parses a COBOL copybook and either prints the record layout (byte
offset and length of every field) or decodes fixed-length EBCDIC records —
for example a dataset streamed from z/OS through your Zowe config — into
JSON. A built-in jq (`-q`) covers most filtering; pipe into real `jq` for
anything else.

**Compa/z** (binary `compaz`) is the terminal browser: navigate z/OSMF data
sets and PDS/PDSE members, and view records raw or through the same copybook
parser, as a table or JSON. Press `?` inside it for the keymap.

```console
$ cq -c CUSTOMER.cpy                          # layout: offset/length of each field
$ cq -c CUSTOMER.cpy -d customer.bin           # decode records to a JSON array
$ cq --copybook-dsn "HQ.COPYLIB(CUSTOMER)" --data-dsn "HQ.CUSTOMER.DATA"
$ cq -q 'select(.BALANCE < 0)' -c CUSTOMER.cpy -d customer.bin
$ cq -where DTAR107-SALE -c DTAR107.cbl -d sales.bin
$ compaz --prefix 'PROD.CUSTOMER.*' -c CUSTOMER.cpy
```

## Install

On Windows, install from winget:

```console
$ winget install Tannex.cq
$ winget install Tannex.compaz
```

Or download the archive for your platform from the
[latest release](https://github.com/Tannex/cq/releases/latest) — `cq_VERSION_OS_ARCH`
for the CLI, `compaz_VERSION_OS_ARCH` for the browser (Linux/macOS/Windows,
amd64/arm64). Extract and put the binary on your `PATH`.

Or install with Go, pinned to a
[release tag](https://github.com/Tannex/cq/releases):

```console
$ go install github.com/Tannex/cq@vX.Y.Z
$ go install github.com/Tannex/cq/cmd/compaz@vX.Y.Z
```

`go install …@main` gives the unstable development build — prefer tags.

## cq usage

```
cq [flags] (-c COPYBOOK | --copybook-dsn DSN[(MEMBER)])
           [-d DATA | --data-dsn DSN | DATA]
```

One copybook source (`-c` local file, or `--copybook-dsn` fetched through
Zowe) and optionally one data source (`-d` file/`-`/stdin, a trailing `DATA`
argument, or `--data-dsn` streamed in binary). Omit the data source for
layout output. Records are fixed-length, derived from the copybook; pass
`-lrecl` if physical records carry trailing padding.

| Flag | Default | Meaning |
| --- | --- | --- |
| `-c` | — | local copybook file |
| `--copybook-dsn` | — | copybook data set or PDS member, fetched through Zowe |
| `-d` | — | data file to decode (`-` for stdin) |
| `--data-dsn` | — | data set streamed in binary through Zowe |
| `-codepage` | Zowe profile `encoding`, else `cp037` | EBCDIC codepage (`cp037`, `cp277`, `cp1047`, `cp1140`, `cp1142`; `ascii`/`latin1` for testing) |
| `-format` | `auto` | copybook source format: `fixed` (cols 7–72), `free`, `auto` |
| `-record` | first | which 01-level record to decode |
| `-q` | — | jq expression (full jq via [gojq](https://github.com/itchyny/gojq)) |
| `-r` | off | with `-q`, print raw strings instead of JSON |
| `-where` | — | keep only records matching a level-88 condition; `!`/`not` negates; repeat to AND |
| `-max` | all | decode at most N records |
| `-pretty` | off | indent JSON output |
| `-fillers` | off | include FILLER fields |
| `-lrecl` | layout size | physical record length, when larger than the layout |
| `--verbose` | off | debug logging to stderr |
| `--version` | — | print version and exit |

**Layout:** one JSON object per 01-level record, `fields[]` with
`name`/`offset`/`length`/`kind` (`text`, `zoned`, `packed`, `binary`,
`float`, `edited`, `group`) and `occurs` on arrays. Level-88 names appear
under `conditions` with their VALUE/THRU literals.

**Decode:** one JSON object per record, copybook field order. Numeric
fields become JSON numbers (decimal point applied); `PIC X` trims trailing
spaces; `OCCURS` become arrays; groups nest.

**`-q`** runs per record while decoding (`.` is one record; results stream,
one per line — no buffering) or against the layout document in layout mode.
COBOL names with dashes need `.["CUST-NAME"]` (`-` is subtraction in jq).

**`-where`** filters on level-88 condition names straight from the
copybook, before any JSON is built — `-where DTAR107-SALE` instead of
remembering `TRANS-TYPE = 1`. Combines with `-q` (filter first, then query).
Not yet supported: conditions inside `OCCURS` tables.

## Compa/z

Shares `cq`'s Zowe session, TLS settings, and `cq/config.json` — no separate
config. Run `cq config` to edit it.

```console
$ compaz                                        # raw browser, default prefix
$ compaz --prefix 'PROD.CUSTOMER.*' -c CUSTOMER.cpy --format fixed
$ compaz --copybook-dsn 'HQ.COPYLIB(CUSTOMER)' --record CUSTOMER-RECORD
$ compaz --read-only                             # disable the editor entirely
```

A copybook is optional and can be loaded/changed at runtime (`c`). Every
z/OSMF list/read request is capped at `2×` the visible rows; cached rows
persist per screen so paging back never refetches. `e` opens a sequential
data set or PDS member for editing; nothing writes to the host until
`Ctrl-S`, which conditionally saves and fails cleanly on a stale copy
(`Ctrl-R` reloads). `--read-only` removes the editor and its write path
entirely — browsing itself never writes.

`J` from the data set screen opens the jobs view: list, drill into a job's
spool files, and read spool/JCL content — read-only, same session. z/OSMF's
jobs API has no pagination cursor, so the job and spool file lists are
single bounded snapshots rather than incrementally fetched like data sets.
Not yet supported: submit, cancel, purge, hold, release.

## Zowe setup

Both binaries connect to z/OSMF directly (no Node/npm) using your existing
Zowe team configuration and OS credential store — the same one Zowe CLI and
Zowe Explorer use. Global and project-level `zowe.config.json` /
`zowe.config.user.json`, profile precedence, `ZOWE_OPT_*` env overrides, and
secure credential storage (Keychain/Credential Manager/libsecret) all work
as expected.

HTTPS verifies the server certificate unless the profile sets
`rejectUnauthorized: false`. Not yet supported: Zowe V1 profiles, Imperative
credential-manager plugins, client-certificate auth.

`cq`/`compaz` are an independent project, not affiliated with or endorsed by
The Linux Foundation or Zowe. Zowe® is a registered trademark of The Linux
Foundation.

### Nested copybooks (`COPY MEMBER.`)

Resolved recursively through an ordered `dsnSearchPath` in `cq/config.json`
(`cq config` creates/opens it):

```json
{ "dsnSearchPath": ["HQL.CPY.SRC", "HQL.COB.SRC"] }
```

The same search runs for `--copybook-dsn` itself if z/OSMF 404s it. Lookups
are parallel (8 in flight) and cached per run; the first library in the list
that has the member wins. `REPLACING`/`OF`/`IN` clauses are rejected
explicitly.

### More examples

```console
# Peek at the first few records of a big dataset
$ cq -max 5 -pretty --copybook-dsn "HQ.COPYLIB(CUSTOMER)" --data-dsn "HQ.CUSTOMER.DATA"

# Download once, slice locally many times
$ zowe zos-files download data-set "HQ.CUSTOMER.DATA" --binary --file customer.bin
$ cq -r -q '.["CUST-NAME"]' -c CUSTOMER.cpy -d customer.bin
```

Don't pipe `zowe zos-files view data-set --binary` into `cq` — it can alter
packed/binary bytes. Use `--data-dsn`, or `download --binary --file` first.

## Supported COBOL

- Levels 01–49/77, FILLER, level-88, level-66 RENAMES (skipped)
- `PIC` X/A/9/S/V/P and numeric-edited pictures
- `USAGE` DISPLAY, COMP/COMP-4/COMP-5/BINARY, COMP-3/PACKED-DECIMAL,
  COMP-1/COMP-2 (IBM hex float not yet)
- `REDEFINES`, `SYNC`, `SIGN LEADING/TRAILING [SEPARATE]`
- `OCCURS`, including `OCCURS ... DEPENDING ON` as the trailing field
- Recursive `COPY MEMBER.` via Zowe DSN search paths
- Fixed and free source format, auto-detected

Not yet: multiple/non-trailing `OCCURS DEPENDING ON`, IBM hex float,
`USAGE POINTER/INDEX`, `PIC N`, RDW-prefixed (VB) files.

Codepages: cp037 (default), cp277, cp1047, cp1140, cp1142 — matched loosely
(`IBM-277`, `ibm277`, `277`). Others are a 256-entry table away; open an
issue.

## Test data

`testdata/copybooks/cb2xml/` are unmodified fixtures from
[cb2xml](https://github.com/bmTas/cb2xml) (LGPL-2.1), used to cross-check
field offsets. See `SOURCES.md` there for origins and license text.

## Releasing

```console
$ git tag -a v1.2.3 -m "v1.2.3"
$ git push origin v1.2.3
```

CI builds `cq`/`compaz` archives for all targets, generates one checksum
file, and publishes a GitHub release.
