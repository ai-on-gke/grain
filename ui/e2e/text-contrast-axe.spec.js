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

import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "./fixtures.js";

// docs/test-runs/2026-11-03-ui-fixes.md §2.2 measured text contrast with
// axe-core over every page the plan names and found two light-theme
// misses: the rail's build stamp (text.secondary at 75% opacity, 4.27:1,
// on every page -- F1) and a report's thin percentiles (text.disabled,
// 2.64:1 -- F2). A later CI run found a third, the Inbox's outlined
// error button (4.22:1 -- F6). textContrast.test.js checks the palette's tokens; this
// checks what a browser actually draws with them, so a token dimmed
// again by opacity or swapped for MUI's disabled grey fails here.

// violations runs axe's contrast rule over only the nodes the fixes were
// about. A whole page on a shared worker's store carries whatever other
// specs filed, and their chips and buttons are the report's follow-ups,
// not this spec's to hold.
async function violations(page, selectors) {
  let builder = new AxeBuilder({ page }).withRules(["color-contrast"]);
  for (const selector of selectors) builder = builder.include(selector);
  const result = await builder.analyze();
  return result.violations.flatMap((v) =>
    v.nodes.map((n) => ({
      target: n.target.join(" "),
      ratio: n.any[0]?.data?.contrastRatio,
      fg: n.any[0]?.data?.fgColor,
      bg: n.any[0]?.data?.bgColor,
      html: n.html.slice(0, 120),
    })),
  );
}

for (const scheme of ["light", "dark"]) {
  test(`the inbox, a report and Preferences hold 4.5:1 text in ${scheme}`, async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.emulateMedia({ colorScheme: scheme });
    await page.addInitScript(
      (t) => localStorage.setItem("grain.themeMode", t),
      scheme,
    );
    const boards = await (await page.request.get("/api/boards")).json();
    const report = boards.find((b) => b.kind === "report");
    expect(report, "grain demo seeds a report board").toBeTruthy();
    for (const path of [
      "/inbox",
      `/boards/${report.id}`,
      "/settings/preferences",
    ]) {
      await page.goto(path);
      await expect(page.getByTestId("build-stamp")).toBeVisible();
      if (path.startsWith("/boards/")) {
        // The thin-percentile cells are what F2 was about.
        await expect(
          page.locator("td span[aria-label^='Only ']").first(),
        ).toBeVisible();
      }
      // Let theme and MUI transitions settle before reading colours.
      await page.waitForTimeout(600);
      const selectors = ["[data-testid=build-stamp]"];
      if (path.startsWith("/boards/")) {
        selectors.push("td span[aria-label^='Only ']");
      }
      if (path === "/inbox") {
        // A proposal's outlined "decline" writes its label in
        // error.main, which was 4.22:1 on the canvas (F6). axe skips
        // what the inbox's own scroller has below the fold, so bring
        // one into view when a row carries it.
        selectors.push(".inbox-actions");
        const decline = page.locator(".inbox-actions .MuiButton-outlinedError");
        if (await decline.count()) {
          await decline.first().scrollIntoViewIfNeeded();
        }
      }
      expect(await violations(page, selectors), `${path} in ${scheme}`).toEqual(
        [],
      );
    }
  });
}
