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

// Which grain hostd host work runs on is a model connection's (Settings
// -> Models -> the connection -> Run on host) or a capability server's, never
// a task's (docs/host-placement.md). The connection's picker lists the
// registry -- each hostd with whether it is connected and its owner, and
// no revoked one -- sits inside the connection's form at desktop and
// phone widths, and offers a fallback list only once a hostd is picked.
// No repository, template or New Task form offers a hostd, and the API
// refuses one.
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

test("a connection picks its hostd; a task has none", async ({ page }) => {
  test.setTimeout(90_000);
  // Leaving an edited connection unsaved asks first; the test says yes.
  page.on("dialog", (d) => d.accept());
  for (const viewport of [DESKTOP, PHONE]) {
    await page.setViewportSize(viewport);
    await page.goto("/settings/models");
    await page
      .getByRole("button", { name: "Edit", exact: true })
      .first()
      .click();
    const field = page.getByTestId("connection-hostd");
    const picker = field.getByRole("combobox", { name: "Run on host" });
    await expect(picker).toBeVisible();
    await expect(field.getByTestId("connection-hostd-fallback")).toHaveCount(0);

    const form = field.locator("xpath=ancestor::form[1]");
    const a = await field.boundingBox();
    const b = await form.boundingBox();
    expect(a.x, "the picker starts left of its form").toBeGreaterThanOrEqual(
      b.x - 1,
    );
    expect(a.x + a.width, "the picker runs past its form").toBeLessThanOrEqual(
      b.x + b.width + 1,
    );
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow, "the page scrolls sideways").toBe(false);

    await picker.click();
    const options = page.getByRole("listbox");
    await expect(
      options.getByRole("option", { name: /^host cloudtop-alice/ }),
    ).toContainText("owner alice@example.com");
    await expect(
      options.getByRole("option", { name: /^host old-laptop/ }),
    ).toHaveCount(0);
    await options.getByRole("option", { name: /^host build-runner/ }).click();
    await expect(field.getByTestId("connection-hostd-fallback")).toBeVisible();
    await expect(field.getByTestId("connection-hostd-fallback")).toHaveValue(
      "",
    );
    await page.keyboard.press("Escape");
  }

  await page.setViewportSize(DESKTOP);
  await page.goto("/repos/acme/widgets/settings/agents");
  await expect(
    page.getByText("Prompt extension", { exact: true }).first(),
  ).toBeVisible();
  await expect(page.getByTestId("repo-default-hostd")).toHaveCount(0);

  const refused = await page.request.post("/api/tasks", {
    data: { title: "elsewhere", repo: "acme/widgets", hostd: "build-runner" },
  });
  expect(refused.status()).toBe(400);
  expect(await refused.text()).toContain(
    "The host is set on the model connection or capability server, not the task",
  );
});
