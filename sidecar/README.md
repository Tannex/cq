# cq Zowe sidecar (proof of concept)

The default cq transport runs one `zowe` CLI process per data set access:
each call pays Node.js startup plus a fresh TLS handshake, and binary
downloads go through a temporary file because the CLI cannot stream to
stdout. This sidecar replaces that with **one** long-lived Node process that:

- resolves the user's existing Zowe configuration — team config, secure
  credential store, tokens — through the official Zowe Node SDK
  (`ProfileInfo`, the same API the zowe CLI and Zowe Explorer use), so cq
  never reimplements or even sees auth;
- serves data set reads over the z/OSMF REST API on a warm HTTPS session,
  streaming bytes to cq as they arrive, so decoding starts before the
  download finishes and `-max`-style early exits cancel the transfer.

## Usage

```sh
cd sidecar && npm install
```

Set the sidecar command in cq's `config.json` so every run uses it:

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
certificates are not wired up in this proof of concept.

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

## Protocol

Newline-delimited JSON over stdin/stdout. cq sends requests:

```
{"id":1,"op":"view","dsn":"HQ.COPYLIB(CUSTOMER)"}   text read (copybooks)
{"id":2,"op":"download","dsn":"HQ.CUSTOMER.DATA"}   binary read (records)
{"id":2,"op":"cancel"}                              stop a transfer early
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
