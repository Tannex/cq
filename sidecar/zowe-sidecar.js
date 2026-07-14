#!/usr/bin/env node
// cq Zowe sidecar.
//
// Resolves the user's existing Zowe configuration — team config, secure
// credential store, tokens — through the official Zowe Node SDK (the same
// ProfileInfo API the zowe CLI and Zowe Explorer use), then serves data set
// reads over the z/OSMF REST API, streaming them to cq as newline-delimited
// JSON on stdout. See ../sidecar.go for the protocol.
//
// cq never sees credentials: they stay in this process, exactly where the
// zowe CLI would keep them.
"use strict";

const https = require("https");
const readline = require("readline");

function send(msg) {
  process.stdout.write(JSON.stringify(msg) + "\n");
}

async function resolveSession() {
  const { ProfileInfo, ProfileCredentials } = require("@zowe/imperative");
  // Standalone imperative has no secure credential manager wired up; give it
  // the same keyring the zowe CLI uses (macOS Keychain, Windows Credential
  // Manager, libsecret) so configs with secure fields resolve. When the
  // native module is unavailable, plain-text configs still work.
  let credMgrOverride;
  let keyringError;
  try {
    const { keyring } = require("@zowe/secrets-for-zowe-sdk");
    credMgrOverride = ProfileCredentials.defaultCredMgrWithKeytar(() => keyring);
  } catch (err) {
    keyringError = err;
  }
  const profInfo = new ProfileInfo("zowe", { credMgrOverride });
  try {
    await profInfo.readProfilesFromDisk();
  } catch (err) {
    if (err && err.errorCode === "LoadCredMgrFailed") {
      const cause = (err.causeErrors && err.causeErrors.message) || err.message;
      const hint = keyringError
        ? `the @zowe/secrets-for-zowe-sdk keyring failed to load (${keyringError.message})`
        : cause;
      throw new Error(
        "cannot open the secure credential store your Zowe configuration uses: " +
        hint +
        "; reinstall the sidecar dependencies (npm install) or store the " +
        "profile's credentials as plain properties to bypass the store",
      );
    }
    throw err;
  }
  const prof = profInfo.getDefaultProfile("zosmf");
  if (!prof) {
    throw new Error(
      "no default zosmf profile found in the Zowe configuration; " +
      "run 'zowe config init' or check ~/.zowe/zowe.config.json",
    );
  }
  const merged = profInfo.mergeArgsForProfile(prof, { getSecureVals: true });
  const arg = (name) => {
    const found = merged.knownArgs.find((a) => a.argName === name);
    return found ? found.argValue : undefined;
  };
  const session = {
    profile: prof.profName,
    host: arg("host"),
    port: arg("port") || 443,
    basePath: String(arg("basePath") || "").replace(/\/+$/, ""),
    user: arg("user"),
    password: arg("password"),
    tokenType: arg("tokenType"),
    tokenValue: arg("tokenValue"),
    rejectUnauthorized: arg("rejectUnauthorized") !== false,
  };
  if (!session.host) {
    throw new Error(`zosmf profile ${prof.profName} has no host`);
  }
  if (!session.tokenValue && !session.user) {
    throw new Error(
      `zosmf profile ${prof.profName} has neither a token nor a user; ` +
      "log in with 'zowe auth login' or add credentials to the profile",
    );
  }
  return session;
}

function openRequest(session, dsn, opts) {
  const headers = {
    "X-CSRF-ZOSMF-HEADER": "",
    "X-IBM-Migrated-Recall": "error",
    "X-IBM-Data-Type": opts.dataType,
  };
  if (opts.range) {
    headers["X-IBM-Record-Range"] = opts.range;
  }
  if (session.tokenValue) {
    const cookie = session.tokenType || "apimlAuthenticationToken";
    headers.Cookie = `${cookie}=${session.tokenValue}`;
  } else {
    const basic = Buffer.from(`${session.user}:${session.password}`);
    headers.Authorization = "Basic " + basic.toString("base64");
  }
  return https.request({
    host: session.host,
    port: session.port,
    path: `${session.basePath}/zosmf/restfiles/ds/${encodeURIComponent(dsn)}`,
    method: "GET",
    headers,
    rejectUnauthorized: session.rejectUnauthorized,
  });
}

function zosmfError(dsn, statusCode, body) {
  let message = body;
  try {
    const parsed = JSON.parse(body);
    if (parsed && parsed.message) {
      message = parsed.message;
    }
  } catch {
    // keep the raw body
  }
  message = String(message || "").trim() || `HTTP ${statusCode}`;
  return `z/OSMF ${statusCode} for ${dsn}: ${message}`;
}

function serve(session) {
  const inflight = new Map(); // id -> cancel function
  let draining = false; // stdin closed: exit once inflight requests settle
  const maybeExit = () => {
    if (draining && inflight.size === 0) {
      process.exit(0);
    }
  };
  const rl = readline.createInterface({ input: process.stdin, terminal: false });

  rl.on("line", (line) => {
    if (!line.trim()) {
      return;
    }
    let req;
    try {
      req = JSON.parse(line);
    } catch {
      send({ error: `bad request line: ${line}` });
      return;
    }
    if (req.op === "cancel") {
      const cancel = inflight.get(req.id);
      if (cancel) {
        cancel();
      }
      return;
    }
    if (req.op !== "view" && req.op !== "download") {
      send({ id: req.id, error: `unknown op ${JSON.stringify(req.op)}` });
      return;
    }

    // done makes the terminal frame single-shot: whichever of end, error,
    // or cancel happens first wins, and everything after it stays silent.
    let done = false;
    const settle = (frame) => {
      if (done) {
        return;
      }
      done = true;
      inflight.delete(req.id);
      if (frame) {
        send({ id: req.id, ...frame });
      }
      maybeExit();
    };

    // sentBytes tracks what already reached cq, so a fallback transfer can
    // skip exactly that prefix and continue the stream seamlessly.
    let sentBytes = 0;
    const sendData = (buf) => {
      if (!done && buf.length > 0) {
        sentBytes += buf.length;
        send({ id: req.id, data: buf.toString("base64") });
      }
    };

    // startAttempt issues one HTTP request for this cq request. Each attempt
    // has its own guard so a superseded attempt (record-range fallback) goes
    // quiet instead of settling the request.
    const startAttempt = (opts, onOK) => {
      const attempt = { superseded: false };
      const httpReq = openRequest(session, req.dsn, opts);
      inflight.set(req.id, () => {
        settle(null); // canceled by cq: no terminal frame expected
        httpReq.destroy();
      });
      httpReq.on("response", (res) => {
        if (res.statusCode !== 200) {
          if (opts.range) {
            // Range or record mode unsupported here: retry unranged.
            attempt.superseded = true;
            res.destroy();
            process.stderr.write(`zowe-sidecar: ${req.dsn}: range request got HTTP ${res.statusCode}; retrying without record range\n`);
            streamPlain();
            return;
          }
          let body = "";
          res.setEncoding("utf8");
          res.on("data", (chunk) => (body += chunk));
          res.on("end", () => settle({ error: zosmfError(req.dsn, res.statusCode, body) }));
          res.on("error", () => settle({ error: zosmfError(req.dsn, res.statusCode, body) }));
          return;
        }
        onOK(res, attempt);
      });
      httpReq.on("error", (err) => {
        if (!attempt.superseded) {
          settle({ error: `data set ${req.dsn}: ${err.message}` });
        }
      });
      httpReq.end();
      return attempt;
    };

    // streamPlain transfers the whole data set (text for copybooks, binary
    // for records), skipping any prefix a ranged attempt already delivered.
    const streamPlain = () => {
      let toSkip = sentBytes;
      startAttempt({ dataType: req.op === "download" ? "binary" : "text" }, (res, attempt) => {
        res.on("data", (chunk) => {
          if (toSkip > 0) {
            const n = Math.min(toSkip, chunk.length);
            toSkip -= n;
            chunk = chunk.subarray(n);
          }
          sendData(chunk);
        });
        res.on("end", () => settle({ end: true }));
        res.on("error", (err) => {
          if (!attempt.superseded) {
            settle({ error: `data set ${req.dsn}: ${err.message}` });
          }
        });
      });
    };

    // streamRanged asks z/OSMF for just the first req.records records, in
    // record mode so each record arrives as a 4-byte big-endian length
    // prefix plus payload. Every record is checked against the record length
    // cq expects; any surprise (different length, partial trailer, HTTP
    // error) falls back to a full plain transfer, so the hint can only ever
    // save work, never change output.
    const streamRanged = () => {
      startAttempt({ dataType: "record", range: `0,${req.records}` }, (res, attempt) => {
        let buf = Buffer.alloc(0);
        const fallBack = (reason) => {
          if (attempt.superseded || done) {
            return;
          }
          attempt.superseded = true;
          res.destroy();
          process.stderr.write(`zowe-sidecar: ${req.dsn}: ${reason}; retrying without record range\n`);
          streamPlain();
        };
        res.on("data", (chunk) => {
          if (done || attempt.superseded) {
            return;
          }
          buf = buf.length === 0 ? chunk : Buffer.concat([buf, chunk]);
          while (buf.length >= 4) {
            const len = buf.readUInt32BE(0);
            if (len !== req.reclen) {
              fallBack(`data set record is ${len} bytes, cq record is ${req.reclen}`);
              return;
            }
            if (buf.length < 4 + len) {
              break;
            }
            sendData(buf.subarray(4, 4 + len));
            buf = buf.subarray(4 + len);
          }
        });
        res.on("end", () => {
          if (done || attempt.superseded) {
            return;
          }
          if (buf.length !== 0) {
            fallBack("ranged response ended inside a record");
            return;
          }
          settle({ end: true });
        });
        res.on("error", (err) => {
          if (!attempt.superseded) {
            settle({ error: `data set ${req.dsn}: ${err.message}` });
          }
        });
      });
    };

    if (req.op === "download" && Number.isInteger(req.records) && req.records > 0 && Number.isInteger(req.reclen) && req.reclen > 0) {
      streamRanged();
    } else {
      streamPlain();
    }
  });

  rl.on("close", () => {
    draining = true;
    maybeExit();
  });
}

async function main() {
  let session;
  try {
    session = await resolveSession();
  } catch (err) {
    send({ error: String((err && err.message) || err) });
    process.exit(1);
  }
  process.stderr.write(`zowe-sidecar: profile ${session.profile}, host ${session.host}:${session.port}\n`);
  send({ ready: true });
  serve(session);
}

main();
