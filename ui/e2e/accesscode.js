import { randomBytes } from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";

// Every grain daemon is single-tenant: every caller, loopback included,
// shows its access code (docs/access-code.md). global-setup.js makes one
// code for the whole run, and every server the suite starts serves it --
// each worker's demo (fixtures.js), the daemons and demos specs start of
// their own (realdaemon.js and the specs that spawn one), and the Go
// fixture servers (GRAIN_E2E_ACCESS_CODE). An access session is signed
// with the code alone and its cookie belongs to 127.0.0.1 whatever the
// port, so the one a worker signs in for is good on every one of them.

const BASE32 = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

// newAccessCode is a code shaped like the ones grain makes itself
// (pkg/ui newAccessCode): 20 random bytes in base32, in groups of four.
export function newAccessCode() {
  const bytes = randomBytes(20);
  let bits = 0;
  let value = 0;
  let out = "";
  for (const b of bytes) {
    value = (value << 8) | b;
    bits += 8;
    while (bits >= 5) {
      out += BASE32[(value >>> (bits - 5)) & 31];
      bits -= 5;
    }
  }
  return out.match(/.{1,4}/g).join("-");
}

// accessCode is the run's access code.
export function accessCode() {
  const code = process.env.GRAIN_E2E_ACCESS_CODE;
  if (!code) {
    throw new Error(
      "GRAIN_E2E_ACCESS_CODE is unset: e2e/global-setup.js makes it, so this " +
        "suite has to run through playwright.config.js",
    );
  }
  return code;
}

// accessHeaders is what a caller outside the browser sends: the code as
// a bearer token, as the grain CLI sends it.
export function accessHeaders() {
  return { Authorization: `Bearer ${accessCode()}` };
}

// writeAccessCode puts the run's code in file -- a daemon's
// -data-dir/local-api-token, or a demo's -access-code-file -- before the
// server starts, which then keeps it rather than making its own.
export async function writeAccessCode(file) {
  await fs.mkdir(path.dirname(file), { recursive: true });
  await fs.writeFile(file, `${accessCode()}\n`, { mode: 0o600 });
}

// signIn posts code to url's sign-in route and resolves to a Playwright
// storage state holding the session cookie it sets.
export async function signIn(url, code = accessCode()) {
  const res = await fetch(`${url}/api/auth/access-code`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ code }),
  });
  if (!res.ok) {
    throw new Error(
      `POST ${url}/api/auth/access-code: ${res.status} ${await res.text()}`,
    );
  }
  const { hostname } = new URL(url);
  const cookies = res.headers.getSetCookie().map((header) => {
    const [pair] = header.split(";");
    const at = pair.indexOf("=");
    return {
      name: pair.slice(0, at).trim(),
      value: pair.slice(at + 1).trim(),
      domain: hostname,
      path: "/",
      expires: -1,
      httpOnly: true,
      secure: false,
      sameSite: "Strict",
    };
  });
  if (cookies.length === 0) {
    throw new Error(`POST ${url}/api/auth/access-code set no cookie`);
  }
  return { cookies, origins: [] };
}

// signInContext signs a browser context in to url with the run's code,
// for a spec that drives a server of its own with Playwright's own
// `test` rather than fixtures.js's: every page and `context.request` of
// it then carries the session.
export async function signInContext(context, url) {
  await context.addCookies((await signIn(url)).cookies);
}
