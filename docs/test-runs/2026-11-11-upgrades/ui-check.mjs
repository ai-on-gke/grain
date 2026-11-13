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

// ui-check.mjs walks Settings -> Upgrade on a running daemon built with a
// test-only notice and a second edition (docs/test-runs/2026-11-11-upgrades.md,
// item 4) at 1440x900 and 390x844, light and dark: the registered notice
// is drawn as a warning, the ordinary image chips carry only this
// edition's repositories, "Switch edition..." opens a chooser naming the
// losses, picking another edition's repository and pressing Upgrade asks
// again in a dialog, and nothing overflows the viewport sideways. It
// screenshots each state into $OUT.
//
//   cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ../docs/test-runs/2026-11-11-upgrades/ui-check.mjs <url> <access-code> <out>
import { chromium } from "@playwright/test";
import { signIn } from "../../../ui/e2e/accesscode.js";

const [url, code, out = "/tmp/shots/check"] = process.argv.slice(2);
const viewports = { desktop: { width: 1440, height: 900 }, phone: { width: 390, height: 844 } };
const { cookies } = await signIn(url, code);
const browser = await chromium.launch();
let failures = 0;
const check = (cond, what) => {
  console.log(`${cond ? "ok  " : "FAIL"} ${what}`);
  if (!cond) failures++;
};
const noSidewaysOverflow = async (page, label) => {
  const { sw, iw } = await page.evaluate(() => ({
    sw: document.documentElement.scrollWidth,
    iw: window.innerWidth,
  }));
  check(sw <= iw, `${label}: no sideways overflow (scrollWidth ${sw} <= ${iw})`);
};
const inViewport = async (page, locator, label) => {
  const box = await locator.boundingBox();
  const vw = page.viewportSize().width;
  check(box && box.x >= 0 && box.x + box.width <= vw + 0.5, `${label}: within the viewport width (${box && Math.round(box.x)}..${box && Math.round(box.x + box.width)} of ${vw})`);
};
for (const [width, viewport] of Object.entries(viewports)) {
  for (const theme of ["light", "dark"]) {
    const tag = `${width}-${theme}`;
    const ctx = await browser.newContext({ viewport, colorScheme: theme });
    await ctx.addCookies(cookies);
    const page = await ctx.newPage();
    await page.goto(`${url}/settings/upgrade`);
    const notice = page.getByText("Walkthrough test notice", { exact: false });
    await notice.waitFor({ timeout: 20000 });
    const alert = page.locator(".MuiAlert-root", { has: notice });
    check(await alert.evaluate((el) => el.className.includes("Warning")), `${tag}: the registered notice is a warning`);
    check(await alert.getByText("grain upgrade start latest").count() > 0, `${tag}: the notice shows its command`);
    await inViewport(page, alert, `${tag}: notice`);
    const chipText = (await page.locator(".MuiChip-label").allTextContents()).join(" | ");
    check(!chipText.includes("acme-grain") && !chipText.includes("mirror.example"), `${tag}: ordinary chips carry no other edition's repository (${chipText})`);
    await noSidewaysOverflow(page, `${tag}: pane`);
    await page.screenshot({ path: `${out}/pane-${tag}.png`, fullPage: true });

    await page.locator(".upgrade-switch-edition-open").click();
    const chooser = page.locator(".upgrade-switch-edition");
    await chooser.waitFor();
    check(await chooser.locator(".upgrade-switch-losses li").count() >= 1, `${tag}: the chooser names what a switch loses`);
    const repos = chooser.locator(".upgrade-switch-edition-repo");
    check(await repos.count() === 3, `${tag}: the chooser offers the acme edition's three repositories`);
    await inViewport(page, chooser, `${tag}: chooser`);
    await noSidewaysOverflow(page, `${tag}: chooser open`);
    await chooser.screenshot({ path: `${out}/chooser-${tag}.png` });

    await repos.filter({ hasText: "registry.example/acme/acme-grain" }).click();
    check(await page.locator(".upgrade-will-switch").count() === 1, `${tag}: the pane says the upgrade will switch edition`);
    await page.getByRole("button", { name: "Switch edition and upgrade" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.waitFor();
    const title = await dialog.locator("#confirm-edition-switch-title").textContent();
    check(/Switch edition from oss to acme\?/.test(title || ""), `${tag}: the dialog says from which edition to which (${title})`);
    check(await dialog.locator(".upgrade-switch-losses li").count() >= 1, `${tag}: the dialog names the losses`);
    await inViewport(page, dialog, `${tag}: dialog`);
    await page.screenshot({ path: `${out}/dialog-${tag}.png` });
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await ctx.close();
  }
}
await browser.close();
console.log(failures ? `${failures} check(s) failed` : "all checks passed");
process.exit(failures ? 1 : 0);
