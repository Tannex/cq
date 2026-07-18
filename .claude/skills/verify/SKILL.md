---
name: verify
description: Drive cq and cqt through their real terminal surfaces with an isolated z/OSMF fixture.
---

# Verify cq/cqt runtime changes

1. Build temporary binaries outside the repo:
   `go build -o /tmp/cq-verify .` and `go build -o /tmp/cqt-verify ./cmd/cqt`.
2. Run a deterministic HTTP fixture that implements dataset/member listing, text copybooks, and four-byte length-prefixed `X-IBM-Data-Type: record` responses. Log `X-IBM-Max-Items`, `X-IBM-Record-Range`, and data type for every request.
3. Point an isolated `ZOWE_CLI_HOME/zowe.config.json` at the fixture and run `cqt` in an isolated tmux server (`tmux -L <name>`) so panes can be resized, driven, and captured.
4. Exercise prefix listing, PS and PO/PO-E navigation, local and DSN copybooks, raw/table/JSON modes, F10/F11, focused input, LOW-VALUE diagnostics, and resize shrink/grow. Add response delay when checking the fixed-width loading spinner, selected-row visibility, one-page-ahead prefetch, backward cache reuse, child-cache release on Back, and parent-cache retention. Confirm every request uses the current exact 2× budget and no record request uses binary/full-download mode.
5. Run `cq` itself with valid and malformed local records to confirm strict decoding is unchanged.
6. The mock at `/home/henrik/dev/zosmf` is useful for catalog/member smoke checks, but its content handler ignores record mode/ranges and returns unframed text; use the deterministic fixture as authoritative for bounded record paging.

Capture representative tmux panes and the fixture request log inline in the verification report. Do not substitute unit tests for the terminal run.
