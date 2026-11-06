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

import { expect, test } from "./fixtures.js";

// "hostd" is an implementation detail: the pages that deal with hosts say
// host (and "the host agent" for the process on one), including the text
// the daemon itself sends them -- run locations, capability server
// reasons, health notes. ui/src/userFacingHost.test.js reads the UI's own
// strings; this reads what a person actually sees, rendered over a demo
// with a remote host dialed in. The binary, unit and config file names a
// user types are the only allowed spellings.
test.use({ demoArgs: ["-host-executor=hostd-remote"] });

const ALLOWED = [
  /\/run\/grain-hostd[\w./-]*/g,
  /\/usr\/local\/lib\/grain\/hostd[\w./-]*/g,
  /\/etc\/grain\/hostd\.(yaml|token)(\.default)?/g,
  /grain-hostd[\w.-]*/g,
  /-hostd-[a-z-]+/g,
  /GRAIN_HOSTD[A-Z_]*/g,
  /\/api\/hostds?[\w/{}.-]*/g,
  /\/hostd\/install\b/g,
];

function hostdLines(text) {
  return text.split("\n").filter((line) => {
    let rest = line;
    for (const re of ALLOWED) rest = rest.replace(re, "");
    return /\bhostds?\b/i.test(rest);
  });
}

// Each page, with text it shows once its own reads have landed.
for (const [path, shows] of [
  ["/settings/hostds", "cloudtop-alice"],
  ["/settings/models", "host agent"],
  ["/settings/capabilities", "Capability servers"],
  ["/system/health", "Health"],
]) {
  test(`${path} says host, not hostd`, async ({ page }) => {
    await page.goto(path);
    await expect(page.locator("body")).toContainText(shows);
    await page.waitForLoadState("networkidle");
    const text = await page.locator("body").innerText();
    expect(hostdLines(text)).toEqual([]);
  });
}

test("Settings names the section Hosts", async ({ page }) => {
  await page.goto("/settings/hostds");
  await expect(
    page.getByRole("heading", { name: "Hosts", exact: true }).first(),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Add host" })).toBeVisible();
});
