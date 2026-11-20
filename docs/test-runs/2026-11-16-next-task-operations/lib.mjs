// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Shared helpers for the §7 browser walks: a context signed in as a named
// person through the IAP stand-in (env.sh start_iapsim), the daemon's API
// as the operator, and a results log.
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(
  path.join(process.env.ROOT || "/home/debian/work", "ui-ng", "package.json"),
);
export const { chromium } = require("@playwright/test");

export const daemon = process.env.DAEMON || "http://127.0.0.1:4280";
export const work = process.env.WORK_DIR || "/var/tmp/walk-ops";
export const code = fs
  .readFileSync(path.join(work, "data", "local-api-token"), "utf8")
  .trim();
export const out = process.env.OUT || path.join(work, "captures");
fs.mkdirSync(out, { recursive: true });

const seededFile = path.join(work, "seeded.env");
export const seeded = Object.fromEntries(
  (fs.existsSync(seededFile) ? fs.readFileSync(seededFile, "utf8") : "")
    .split("\n")
    .filter(Boolean)
    .map((l) => l.split("=")),
);

export const DESKTOP = { width: 1440, height: 900 };
export const PHONE = { width: 390, height: 844 };

const results = [];
export function note(item, ok, detail) {
  const line = `${ok ? "PASS" : "FAIL"} ${item}: ${detail}`;
  results.push(line);
  console.log(line);
}
export function info(item, detail) {
  const line = `INFO ${item}: ${detail}`;
  results.push(line);
  console.log(line);
}
export function flush(name) {
  fs.writeFileSync(path.join(out, `${name}.results.txt`), results.join("\n") + "\n");
}

export async function api(method, p, body) {
  const res = await fetch(`${daemon}${p}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const txt = await res.text();
  if (!res.ok) throw new Error(`${method} ${p}: ${res.status} ${txt}`);
  return txt ? JSON.parse(txt) : null;
}

export async function jwt(email) {
  const u = new URL("http://127.0.0.1:4302/sign");
  u.searchParams.set("email", email);
  u.searchParams.set("aud", "/projects/123/global/backendServices/456");
  u.searchParams.set("iss", "https://iap.test.local");
  u.searchParams.set("exp", "6h");
  return (await (await fetch(u)).text()).trim();
}

// as opens a context that every request of identifies as email.
export async function as(browser, email, viewport, colorScheme = "light") {
  const ctx = await browser.newContext({
    viewport,
    colorScheme,
    baseURL: `${daemon}/next/`,
    // NOIAP: a daemon with no IAP configured (the publishing-held
    // harness), where loopback is the operator.
    extraHTTPHeaders: process.env.NOIAP
      ? {}
      : { "x-goog-iap-jwt-assertion": await jwt(email) },
  });
  if (process.env.NOIAP) {
    // Signed in with the access code under the person's email.
    const r = await ctx.request.post(`${daemon}/api/auth/access-code`, {
      data: { code, name: email },
    });
    if (!r.ok()) throw new Error(`access-code sign-in: ${r.status()}`);
  }
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  page.errors = errors;
  return { ctx, page };
}

export async function shot(page, name) {
  await page.screenshot({ path: path.join(out, `${name}.png`), fullPage: false });
}

export const row = (page, id) =>
  page.locator(".task-row", { has: page.getByTestId(`open-${id}`) });
export const tick = (page, id) =>
  page.getByRole("checkbox", { name: `Select #${id}`, exact: true });

export async function accept(dialog, name) {
  const button = dialog.getByRole("button", { name, exact: true });
  await button.waitFor();
  const t = dialog.getByTestId("confirm-multi-repo");
  if (await t.count()) await t.check();
  await button.click();
}
