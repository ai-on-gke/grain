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

// A connection's credentials are Stored or Ambient (Settings -> Models ->
// the connection -> Authentication -> Credentials; docs/host-placement.md).
// Ambient is the CLI's own login on one host, so choosing it asks for that
// host before anything is saved, and the host picker says which agent CLI
// logins each host reports (GET /api/hostds' logins, from the host's
// hello). The demo's hosts do not dial in, so the registry is answered
// with logins added; nothing here saves, so the shared demo server is
// left as it was. (The API's refusal of an ambient connection with no
// host is pkg/ui's and pkg/model's Go tests'.)
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

test("ambient credentials ask for the host whose logins the picker shows", async ({
  page,
}) => {
  test.setTimeout(90_000);
  page.on("dialog", (d) => d.accept());
  await page.route("**/api/hostds", async (route) => {
    const response = await route.fetch();
    const rows = await response.json();
    for (const h of rows) {
      if (h.name === "cloudtop-alice") {
        h.connection = { connected: true, principal: "alice", host: "ws-1" };
        h.logins = ["antigravity", "codex"];
        h.loginsKnown = true;
      }
      if (h.name === "build-runner") {
        h.connection = { connected: true, host: "runner-7" };
        h.logins = [];
        h.loginsKnown = true;
      }
    }
    await route.fulfill({ response, json: rows });
  });

  for (const viewport of [DESKTOP, PHONE]) {
    await page.setViewportSize(viewport);
    await page.goto("/settings/models");
    await page
      .getByRole("button", { name: "Edit", exact: true })
      .first()
      .click();
    const credentials = page.getByTestId("connection-credentials");
    await expect(credentials).toBeVisible();
    await expect(
      credentials.getByRole("radio", { name: "Stored" }),
    ).toBeVisible();
    const ambient = credentials.getByRole("radio", { name: "Ambient" });
    if (!(await ambient.isChecked())) await ambient.check();
    await expect(ambient).toBeChecked();

    const field = page.getByTestId("credentials-hostd");
    await expect(field).toBeVisible();
    // Both sit inside the connection's pane, one above the other, and
    // the page does not scroll sideways.
    const form = page.getByRole("dialog").last();
    const f = await form.boundingBox();
    const c = await credentials.boundingBox();
    const p = await field.boundingBox();
    for (const [name, box] of [
      ["the credentials choice", c],
      ["the host picker", p],
    ]) {
      expect(box.x, `${name} starts left of its pane`).toBeGreaterThanOrEqual(
        f.x - 1,
      );
      expect(
        box.x + box.width,
        `${name} runs past its pane`,
      ).toBeLessThanOrEqual(f.x + f.width + 1);
    }
    expect(p.y, "the host picker is below the choice").toBeGreaterThan(
      c.y + c.height - 1,
    );
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow, "the page scrolls sideways").toBe(false);

    await field.getByRole("combobox").click();
    const options = page.getByRole("listbox");
    await expect(
      options.getByRole("option", { name: /^host cloudtop-alice/ }),
    ).toContainText("logins: antigravity, codex");
    await expect(
      options.getByRole("option", { name: /^host build-runner/ }),
    ).toContainText("no CLI logins");
    await page.keyboard.press("Escape");
  }
});
