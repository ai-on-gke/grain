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

// A repository's default hostd (model.RepoConfig.DefaultHostd) is set on
// its Agents tab: the box sits between Default capabilities and Prompt
// extension, inside the panel at desktop and phone widths, and a blur
// sends PUT .../settings with defaultHostd and the summary then names the
// stored selector.
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

test("A repository's default hostd is set on its Agents tab", async ({
  page,
}) => {
  test.setTimeout(90_000);
  // The demo is worker-scoped, and every task filed against acme/widgets
  // takes this default, so it comes off again whatever happens.
  try {
    for (const [viewport, selector, stored] of [
      [DESKTOP, "gpu=yes", "gpu=yes"],
      [PHONE, " build-runner ", "build-runner"],
    ]) {
      await page.setViewportSize(viewport);
      await page.goto("/repos/acme/widgets/settings/agents");
      const block = page.getByTestId("repo-default-hostd");
      const input = block.getByTestId("repo-default-hostd-input");
      await expect(input).toBeVisible();

      const panel = block.locator("xpath=..");
      const a = await block.boundingBox();
      const b = await panel.boundingBox();
      expect(a.x, "the block starts left of its panel").toBeGreaterThanOrEqual(
        b.x - 1,
      );
      expect(
        a.x + a.width,
        "the block runs past its panel",
      ).toBeLessThanOrEqual(b.x + b.width + 1);
      // Below the capabilities picker and above the prompt extension.
      const caps = await page
        .getByText("Default capabilities", { exact: true })
        .first()
        .boundingBox();
      const prompt = await page
        .getByText("Prompt extension", { exact: true })
        .first()
        .boundingBox();
      expect(a.y).toBeGreaterThan(caps.y);
      expect(a.y + a.height).toBeLessThanOrEqual(prompt.y + 1);
      const overflow = await page.evaluate(
        () =>
          document.documentElement.scrollWidth >
          document.documentElement.clientWidth + 1,
      );
      expect(overflow, "the page scrolls sideways").toBe(false);

      await input.fill(selector);
      const [put] = await Promise.all([
        page.waitForRequest(
          (r) =>
            r.method() === "PUT" &&
            r.url().endsWith("/api/repos/acme/widgets/settings"),
        ),
        input.blur(),
      ]);
      expect(put.postDataJSON()).toEqual({ defaultHostd: selector.trim() });
      await expect(input).toHaveValue(stored);
      await expect(
        block.getByTestId("repo-default-hostd-summary"),
      ).toContainText(
        `on hostd ${stored}, unless it or its template names another`,
      );
    }
  } finally {
    await page.request.put("/api/repos/acme/widgets/settings", {
      data: { defaultHostd: "" },
    });
  }
});
