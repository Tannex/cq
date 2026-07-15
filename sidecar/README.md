# cq Zowe sidecar

cq's transport to z/OS: one long-lived Node process per cq run that

- resolves the user's existing Zowe configuration — team config, secure
  credential store, tokens — through the official Zowe Node SDK
  (`ProfileInfo`, the same API the Zowe CLI and Zowe Explorer use), so cq
  never reimplements or even sees auth;
- serves data set reads over the z/OSMF REST API on a warm HTTPS session,
  streaming bytes to cq as they arrive, so decoding starts before the
  transfer finishes and `-max`-style early exits cancel it.

## Setup

```sh
cq init
```

This requires Node.js 20.9.0 or newer and npm. It extracts the sidecar into
cq's user configuration directory, runs `npm ci`, and updates `config.json`.
Run it again to update or repair the installation. To run the sidecar from
somewhere else instead, name the command in cq's `config.json`:

```json
{
  "dsnSearchPath": ["HQL.CPY.SRC", "HQL.COB.SRC"],
  "sidecar": "node /path/to/sidecar/zowe-sidecar.js"
}
```

Then run cq normally, for example:

```sh
cq --copybook-dsn "HQ.COPYLIB(CUSTOMER)" --data-dsn "HQ.CUSTOMER.DATA"
```

The sidecar uses the default `zosmf` profile from the Zowe configuration.
Basic auth (user/password, including values from the secure credential
store) and token auth (`zowe auth login apiml`) are supported; client
certificates are not wired up yet.

## Secure credential store

Profiles whose credentials live in the OS secure store (the zowe CLI
default: `"secure": ["user", "password"]` in the team config) need
`@zowe/secrets-for-zowe-sdk`, which `npm install` in this directory brings
in. Without it, `ProfileInfo` fails with `Failed to initialize secure
credential manager` as soon as the config contains secure fields — configs
with plain-text properties are unaffected. The sidecar wires up the same
keyring the zowe CLI itself uses: macOS Keychain, Windows Credential
Manager, or libsecret on Linux.

On macOS the first read may pop a Keychain prompt asking to allow `node`
access to the "Zowe" item — that is the Keychain protecting the zowe CLI's
stored secrets; choose "Always Allow" to stop it recurring.

## Bounded reads for -max

When cq runs with `-max N` (and no `-where` filter), the download request
carries `records`/`reclen` and the sidecar asks z/OSMF for just those
records with `X-IBM-Record-Range: 0,N` — record offsets are 0-based per the
z/OSMF REST documentation — in record mode, where "each logical record is
preceded by the 4-byte big endian record length". Every record's length
prefix is checked against the record length cq expects; on any surprise — a
different record length, a response ending inside a record, or an HTTP
error on the ranged request (z/OSMF returns an exception when the range
matches no records, e.g. an empty data set) — the sidecar restarts the
transfer as a plain unranged binary stream and skips the bytes already
delivered, so the bound can only ever save work, never change output.

## Protocol

Newline-delimited JSON over stdin/stdout. cq sends requests:

```
{"id":1,"op":"view","dsn":"HQ.COPYLIB(CUSTOMER)"}                          text read (copybooks)
{"id":2,"op":"download","dsn":"HQ.CUSTOMER.DATA","records":10,"reclen":80} binary read (records; bound optional)
{"id":2,"op":"cancel"}                                                     stop a transfer early
```

The sidecar answers with one `{"ready":true}` frame at startup (or
`{"error":"..."}` if the Zowe configuration cannot be resolved), then per
request: zero or more `{"id":N,"data":"<base64 chunk>"}` frames followed by
`{"id":N,"end":true}` or `{"id":N,"error":"..."}`. Frames for different
requests may interleave — cq's copybook resolver issues concurrent probes
over the single connection. Chunks are base64 so the stream survives any
stdio mangling cross-platform; the ~33% inflation is negligible next to the
network hop.

Anything the sidecar writes to stderr shows up in `cq --verbose` output
prefixed with `sidecar:`.
