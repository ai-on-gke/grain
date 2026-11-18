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

// Shared Playwright helpers for the 2026-11-18 §8 /next/ fixes walk
// (docs/test-runs/2026-11-18-next-fixes.md). Run from ui/ so its
// node_modules supplies playwright:
//   NODE_PATH=ui/node_modules node docs/test-runs/2026-11-18-next-fixes/<walk>.mjs
import { createRequire } from "node:module";
import path from "node:path";

const require = createRequire(path.resolve("ui/package.json"));
export const { chromium } = require("playwright");

// session signs in to base with the access code and returns a browser
// context holding the session cookie, in theme at width.
export async function session(browser, base, code, { width = 1440, height = 900, theme = "light", name = "alice@example.com" } = {}) {
  const res = await fetch(`${base}/api/auth/access-code`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ code, name }),
  });
  if (!res.ok) throw new Error(`sign-in: ${res.status} ${await res.text()}`);
  const { hostname } = new URL(base);
  const cookies = res.headers.getSetCookie().map((h) => {
    const [pair] = h.split(";");
    const at = pair.indexOf("=");
    return { name: pair.slice(0, at).trim(), value: pair.slice(at + 1).trim(), domain: hostname, path: "/", httpOnly: true, secure: false, sameSite: "Strict" };
  });
  const ctx = await browser.newContext({ viewport: { width, height }, colorScheme: theme, storageState: { cookies, origins: [] } });
  await ctx.addInitScript((t) => localStorage.setItem("grain.themeMode", t), theme);
  return ctx;
}

// watch records console errors, page errors and 4xx/5xx API answers.
export function watch(page) {
  const log = { console: [], pageErrors: [], http: [] };
  page.on("console", (m) => m.type() === "error" && log.console.push(m.text()));
  page.on("pageerror", (e) => log.pageErrors.push(String(e)));
  page.on("response", (r) => r.status() >= 400 && log.http.push(`${r.status()} ${r.request().method()} ${new URL(r.url()).pathname}${new URL(r.url()).search}`));
  return log;
}
