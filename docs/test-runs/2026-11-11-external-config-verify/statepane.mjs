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

// statepane.mjs: §11 item 4. Opens Settings -> Config Repo on the live
// walkthrough daemon (URL, CODE) at 1440x900 and 390x844, light and dark,
// and checks the require_verified notice: it names the held commit
// (HELD, its full SHA), its Accept button is visible and inside the
// viewport, and the page does not scroll sideways. Copied into ui/e2e so
// playwright and accesscode.js resolve:
//
//	cp statepane.mjs ../../../ui/e2e/statepane.tmp.mjs && cd ../../../ui &&
//	  URL=http://127.0.0.1:4292 CODE=... HELD=<sha> node e2e/statepane.tmp.mjs
import { chromium } from "@playwright/test";
import { signIn } from "./accesscode.js";

const url = process.env.URL;
const held = process.env.HELD;
const { cookies } = await signIn(url, process.env.CODE);
const browser = await chromium.launch();
let failed = false;
for (const [width, height] of [
  [1440, 900],
  [390, 844],
]) {
  for (const theme of ["light", "dark"]) {
    const ctx = await browser.newContext({
      viewport: { width, height },
      colorScheme: theme,
    });
    await ctx.addCookies(cookies);
    await ctx.addInitScript(
      ([k, v]) => localStorage.setItem(k, v),
      ["grain.themeMode", theme],
    );
    const tab = await ctx.newPage();
    await tab.goto(new URL("/settings/config-repo", url).href);
    const notice = tab.getByText(`Holding ${held.slice(0, 12)}`);
    await notice.first().waitFor({ timeout: 15_000 });
    const accept = tab.getByRole("button", { name: "Accept", exact: true });
    await accept.scrollIntoViewIfNeeded();
    const box = await accept.boundingBox();
    const overflow = await tab.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    const ok = box && box.x >= 0 && box.x + box.width <= width && overflow <= 0;
    failed ||= !ok;
    console.log(
      `${width}x${height} ${theme}: notice shown, Accept at x=${Math.round(box?.x)}..${Math.round(
        (box?.x ?? 0) + (box?.width ?? 0),
      )}, horizontal overflow ${overflow}px -> ${ok ? "ok" : "FAIL"}`,
    );
    await ctx.close();
  }
}
// CLICK=1: press Accept as the signed-in operator and wait for the notice
// to go once the next sync applies the commit.
if (process.env.CLICK) {
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 },
  });
  await ctx.addCookies(cookies);
  const tab = await ctx.newPage();
  await tab.goto(new URL("/settings/config-repo", url).href);
  await tab.getByRole("button", { name: "Accept", exact: true }).click();
  await tab
    .getByText(`Holding ${held.slice(0, 12)}`)
    .first()
    .waitFor({ state: "detached", timeout: 30_000 });
  console.log("Accept clicked: the notice is gone");
  await ctx.close();
}
await browser.close();
process.exit(failed ? 1 : 0);
