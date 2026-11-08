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

// Renders the live walkthrough daemon's Settings -> Hosts and Settings ->
// Models at 1440x900 and 390x844, light and dark, and reports for each
// whether the page scrolls sideways and whether any visible text still
// says "hostd" outside a path. Run from ui/ (for @playwright/test):
//
//   GRAIN_URL=http://127.0.0.1:4280 GRAIN_CODE=$(cat /tmp/walk/data/local-api-token) \
//     OUT=/tmp/walk/shots node ../docs/test-runs/2026-11-07-hosts-enrollment-placement/shoot.mjs
import { chromium } from "@playwright/test";
import fs from "node:fs/promises";

const url = process.env.GRAIN_URL || "http://127.0.0.1:4280";
const code = process.env.GRAIN_CODE;
const out = process.env.OUT || "/tmp/walk/shots";
await fs.mkdir(out, { recursive: true });

const res = await fetch(`${url}/api/auth/access-code`, {
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify({ code }),
});
if (!res.ok) throw new Error(`sign-in: ${res.status}`);
const cookies = res.headers.getSetCookie().map((h) => {
  const [pair] = h.split(";");
  const at = pair.indexOf("=");
  return {
    name: pair.slice(0, at).trim(),
    value: pair.slice(at + 1).trim(),
    domain: new URL(url).hostname,
    path: "/",
  };
});

const browser = await chromium.launch();
const pages = [
  ["hosts", "/settings/hostds"],
  ["models", "/settings/models"],
  ["capabilities", "/settings/capabilities"],
];
const sizes = [
  ["1440", { width: 1440, height: 900 }],
  ["390", { width: 390, height: 844 }],
];
let bad = 0;
for (const scheme of ["light", "dark"]) {
  for (const [sname, viewport] of sizes) {
    const ctx = await browser.newContext({ viewport, colorScheme: scheme });
    await ctx.addCookies(cookies);
    const page = await ctx.newPage();
    for (const [pname, path] of pages) {
      await page.goto(url + path, { waitUntil: "load" });
      // The UI keeps a live event stream open, so the network never idles.
      await page.waitForTimeout(2500);
      const m = await page.evaluate(() => ({
        overflow:
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
        // A path or URL (/hostd/install, hostd.sock, hostd.yaml) is not
        // wording; the word on its own is.
        hostd: (
          document.body.innerText.match(/(?<![/\w.-])hostds?(?![\w./-])/gi) ||
          []
        ).length,
      }));
      const file = `${out}/${pname}-${sname}-${scheme}.png`;
      await page.screenshot({ path: file, fullPage: true });
      const ok = m.overflow <= 0 && m.hostd === 0;
      if (!ok) bad++;
      console.log(
        `${ok ? "ok " : "BAD"} ${pname} ${sname} ${scheme}: horizontal overflow ${m.overflow}px, "hostd" in visible text ${m.hostd}x -> ${file}`,
      );
    }
    await ctx.close();
  }
}
await browser.close();
process.exit(bad ? 1 : 0);
