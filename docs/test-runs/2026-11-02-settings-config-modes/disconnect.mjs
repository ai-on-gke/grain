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

// Stops and restarts a live grain daemon under an open page and times
// the disconnected banner: how long after the kill it appears, and how
// long after the daemon answers again it clears
// (../2026-11-02-settings-config-modes.md, "What is NOT proved" 3).
//
//   [SIGNAL=KILL] node disconnect.mjs <base-url> <bearer-token> <daemon-pattern> <start-script> <path>...
//
// <daemon-pattern> is what `pkill -f` finds the daemon by.
import { chromium } from "playwright";
import { execSync } from "node:child_process";

const [BASE, TOK, PATTERN, START, ...paths] = process.argv.slice(2);
const answers = async () => {
  try {
    const r = await fetch(`${BASE}/api/config`, {
      headers: { Authorization: `Bearer ${TOK}` },
    });
    return r.ok;
  } catch {
    return false;
  }
};

const browser = await chromium.launch();
const ctx = await browser.newContext({
  extraHTTPHeaders: { Authorization: `Bearer ${TOK}` },
  viewport: { width: 1440, height: 900 },
});
for (const path of paths.length ? paths : ["/"]) {
  const page = await ctx.newPage();
  await page.goto(BASE + path);
  await page.waitForTimeout(3000);
  const banner = page.getByTestId("disconnected-banner");
  const before = await banner.isVisible();
  const killedAt = Date.now();
  execSync(
    `pkill -${process.env.SIGNAL || "TERM"} -f '${PATTERN}'; while pgrep -f '${PATTERN}' >/dev/null; do sleep 0.1; done`,
  );
  const killWait = Date.now() - killedAt;
  await banner.waitFor({ state: "visible", timeout: 120000 });
  const appearedMs = Date.now() - killedAt;
  const bannerText = (await banner.innerText()).replace(/\s+/g, " ").trim();
  await page.waitForTimeout(5000);
  execSync(START, { stdio: "ignore" });
  while (!(await answers())) await new Promise((r) => setTimeout(r, 100));
  const answeredAt = Date.now();
  await banner.waitFor({ state: "hidden", timeout: 120000 });
  const clearedMs = Date.now() - answeredAt;
  console.log(
    JSON.stringify({
      path,
      bannerBefore: before,
      killWaitMs: killWait,
      appearedMs,
      bannerText,
      clearedAfterAnswerMs: clearedMs,
    }),
  );
  await page.close();
}
await browser.close();
