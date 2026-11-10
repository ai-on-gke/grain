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

// docs/test-plans/2026-11-10.md §5 item 2 (Copy link over plain HTTP)
// and item 1's malformed fragments, each on a fresh page load, against a
// demo served on a non-loopback address so the page is not a secure
// context. Copy it into ui/e2e/ (it imports ./accesscode.js), then from
// ui/, with a `grain demo -github-sim -fake-ci -addr 0.0.0.0:4272` up:
//   node e2e/walk-insecure.mjs http://<non-loopback address>:4272 <code>
import { chromium } from "@playwright/test";
import { signIn } from "./accesscode.js";

const [url, code] = process.argv.slice(2);
const state = await signIn(url, code);
const browser = await chromium.launch();
const context = await browser.newContext({ storageState: state });
const page = await context.newPage();
const errors = [];
page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
page.on("pageerror", (e) => errors.push(String(e)));

const api = context.request;
const t = await (await api.get(`${url}/api/tasks/4`)).json();
const cid = t.comments[0].id;

await page.goto(`${url}/tasks/4#comment=${cid}`);
const facts = await page.evaluate(() => ({
  isSecureContext: window.isSecureContext,
  hasClipboard: Boolean(navigator.clipboard?.writeText),
  origin: window.location.origin,
}));
// What the fallback copies: the textarea it selects for execCommand.
await page.evaluate(() => {
  window.__copied = null;
  const orig = document.execCommand.bind(document);
  document.execCommand = (...a) => {
    const v = document.activeElement?.tagName;
    const ok = orig(...a);
    window.__exec = { ok, v };
    return ok;
  };
  window.addEventListener("copy", (e) => {
    const a = document.activeElement;
    window.__copied = {
      active: a?.tagName,
      // What the fixed fallback writes through the event itself.
      clipboardData: e.clipboardData?.getData("text/plain") || "",
      selected:
        a && "value" in a && a.selectionEnd > a.selectionStart
          ? a.value.slice(a.selectionStart, a.selectionEnd)
          : document.getSelection()?.toString() || "",
    };
  });
});
const entry = page.locator(`[data-comment-id="${cid}"]`);
await entry.getByRole("button", { name: "Copy link" }).click();
await page.waitForTimeout(300);
const copied = await page.evaluate(() => ({
  event: window.__copied,
  exec: window.__exec || null,
}));
const label = await entry
  .locator(".copy-button")
  .first()
  .getAttribute("aria-label");

const malformed = {};
for (const h of [
  "#comment=abc",
  "#comment=",
  "#comment=12abc",
  "#comment=-1",
  "#comment=%31",
  "#Comment=1",
]) {
  await page.goto("about:blank");
  await page.goto(`${url}/tasks/4${h}`);
  await page.waitForTimeout(2500);
  malformed[h] = {
    notice: await page.getByTestId("comment-link-notice").allInnerTexts(),
    highlighted: await page.locator(".comment-highlight").count(),
  };
}

console.log(
  JSON.stringify({ facts, copied, label, malformed, errors }, null, 1),
);
await browser.close();
