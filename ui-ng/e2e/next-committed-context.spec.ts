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

// Committed context (#committed-context) on /next/task/<id>:
//   - Seeded task ("Add a dark mode toggle") renders its target-repo badge,
//     pinned @<rev> link, path, and manifest files in the desktop Details
//     sidebar and on the Details tab at desktop, phone, and dark themes.
//   - Cold load and in-page hashchange to #committed-context open Details,
//     scroll #committed-context into the viewport, and focus it.
//   - Read-only repo entries render `<repo>:<path>` with a Read-only repo
//     badge and drop unsafe source_url protocols without horizontal overflow.

import AxeBuilder from "@axe-core/playwright";
import type { Locator, Page } from "@playwright/test";
import { expect, test } from "./fixtures";

const SEEDED_TITLE = "Add a dark mode toggle";

interface TaskSummary {
  id: string;
  title: string;
  context?: unknown[];
}

async function findSeededTasks(page: Page): Promise<{
  withContext: TaskSummary;
  withoutContext: TaskSummary;
}> {
  const res = await page.request.get("/api/tasks");
  expect(res.ok()).toBeTruthy();
  const payload = await res.json();
  const list = (
    Array.isArray(payload) ? payload : (payload.tasks ?? [])
  ) as TaskSummary[];
  const withContext = list.find((t) => t.title === SEEDED_TITLE);
  expect(withContext, `seeded "${SEEDED_TITLE}" task`).toBeTruthy();
  const withoutContext = list.find(
    (t) => t.id !== withContext!.id && (!t.context || t.context.length === 0),
  );
  expect(
    withoutContext,
    "a seeded task without committed context",
  ).toBeTruthy();
  return { withContext: withContext!, withoutContext: withoutContext! };
}

async function expectSectionLayoutAndA11y(
  page: Page,
  section: Locator,
): Promise<void> {
  await expect(section).toBeVisible();
  const vp = page.viewportSize()!;

  const docOverflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(docOverflow, "page must not scroll sideways").toBeLessThanOrEqual(0);

  const box = (await section.boundingBox())!;
  expect(box).not.toBeNull();
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(vp.width + 1);

  const sectionOverflow = await section.evaluate(
    (el) => el.scrollWidth - el.clientWidth,
  );
  expect(
    sectionOverflow,
    "section must not overflow horizontally",
  ).toBeLessThanOrEqual(1);

  for (const item of await section
    .getByTestId("committed-context-item")
    .all()) {
    const itemBox = (await item.boundingBox())!;
    expect(itemBox).not.toBeNull();
    expect(itemBox.x).toBeGreaterThanOrEqual(box.x - 1);
    expect(itemBox.x + itemBox.width).toBeLessThanOrEqual(
      box.x + box.width + 1,
    );

    // Badge and @rev link sit vertically centred on the same header row when
    // they did not wrap onto separate lines.
    const badge = item.locator(".tcc-badge");
    const sha = item.locator(".tcc-sha");
    if ((await badge.count()) > 0 && (await sha.count()) > 0) {
      const bBox = (await badge.boundingBox())!;
      const sBox = (await sha.boundingBox())!;
      if (Math.abs(bBox.y - sBox.y) < bBox.height) {
        const bMid = bBox.y + bBox.height / 2;
        const sMid = sBox.y + sBox.height / 2;
        expect(
          Math.abs(bMid - sMid),
          "badge and @rev vertically centred on header row",
        ).toBeLessThanOrEqual(2);
      }
    }
  }

  const axe = await new AxeBuilder({ page })
    .include("#committed-context")
    .analyze();
  const bad = axe.violations.filter((v) =>
    ["serious", "critical"].includes(v.impact ?? ""),
  );
  expect(
    bad.map((v) => `${v.id}: ${v.help}`),
    "no serious/critical axe or contrast violations in #committed-context",
  ).toEqual([]);
}

async function expectRevealedInViewport(
  page: Page,
  section: Locator,
): Promise<void> {
  await expect(section).toBeVisible();
  await expect(section).toBeFocused();
  const vp = page.viewportSize()!;
  await expect
    .poll(async () => {
      const a = await section.boundingBox();
      await page.waitForTimeout(80);
      const b = await section.boundingBox();
      return Boolean(a && b && Math.abs(a.y - b.y) < 1);
    })
    .toBe(true);
  const box = (await section.boundingBox())!;
  expect(box.y, "#committed-context top is in view").toBeGreaterThanOrEqual(0);
  expect(box.y, "#committed-context top is above viewport bottom").toBeLessThan(
    vp.height,
  );
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(vp.width + 1);
}

test(
  "renders seeded committed context in desktop aside and Details tab",
  { tag: ["@phone", "@dark"] },
  async ({ page, pageErrors }) => {
    await page.goto("/next/");
    const { withContext, withoutContext } = await findSeededTasks(page);
    const vp = page.viewportSize()!;

    await page.goto(`/next/task/${withContext.id}`);
    const section = page.getByTestId("committed-context");

    if (vp.width >= 1080) {
      // Desktop shows Committed context right in the Details sidebar on the
      // default Conversation tab.
      await section.scrollIntoViewIfNeeded();
      await expectSectionLayoutAndA11y(page, section);
    }

    // Details tab always renders #committed-context at every viewport.
    await page.getByTestId("task-tab-details").click();
    await section.scrollIntoViewIfNeeded();
    await expectSectionLayoutAndA11y(page, section);

    await expect(
      section.getByRole("heading", { name: "Committed context" }),
    ).toBeVisible();
    await expect(section).toContainText("Target repo");
    await expect(section).toContainText("design/ui/theme-toggle");
    await expect(section).toContainText("Theme Switcher Spec");
    await expect(section).toContainText(
      "Approved product brief, contrast tokens, and settings mock",
    );
    await expect(section).toContainText("spec.md");
    await expect(section).toContainText("settings-mock.html");

    const shaLink = section.getByRole("link", { name: "@9f4c2b81e7" });
    await expect(shaLink).toHaveAttribute(
      "href",
      /\/tree\/9f4c2b81e7a03d5f6c8912a4b0e3f7d81c2e5a90\/design\/ui\/theme-toggle$/,
    );

    const sourceLink = section.getByRole("link", {
      name: /Source for spec\.md/i,
    });
    await expect(sourceLink).toHaveAttribute(
      "href",
      "https://example.com/specs/theme-toggle",
    );

    // Tasks without committed context omit #committed-context cleanly.
    await page.goto(`/next/task/${withoutContext.id}?tab=details`);
    await expect(page.getByTestId("task-attributes-pane")).toBeVisible();
    await expect(page.getByTestId("committed-context")).toHaveCount(0);
    expect(pageErrors).toEqual([]);
  },
);

test(
  "deep-linking #committed-context opens Details, scrolls into view, and focuses the pane",
  { tag: ["@phone", "@dark"] },
  async ({ page, pageErrors }) => {
    await page.goto("/next/");
    const { withContext, withoutContext } = await findSeededTasks(page);

    // Cold load directly to /next/task/<id>#committed-context.
    await page.goto(`/next/task/${withContext.id}#committed-context`);
    await expect(page.getByTestId("task-tab-details")).toHaveAttribute(
      "aria-current",
      "page",
    );
    const section = page.getByTestId("committed-context");
    await expectRevealedInViewport(page, section);
    await expectSectionLayoutAndA11y(page, section);

    // Clicking another tab clears the forced Details tab cleanly.
    await page.getByTestId("task-tab-runs").click();
    await expect(page.getByTestId("task-tab-runs")).toHaveAttribute(
      "aria-current",
      "page",
    );

    // In-page hashchange back to #committed-context re-opens Details and reveals it.
    await page.evaluate(() => {
      window.location.hash = "committed-context";
    });
    await expect(page.getByTestId("task-tab-details")).toHaveAttribute(
      "aria-current",
      "page",
    );
    await expectRevealedInViewport(page, section);

    // Tasks pane link: opens the pane on Details on desktop, or redirects to
    // the task's own page on phone.
    await page.goto(`/next/tasks?task=${withContext.id}#committed-context`);
    if (page.viewportSize()!.width <= 720) {
      await expect(page).toHaveURL(
        new RegExp(`/next/task/${withContext.id}\\?.*#committed-context$`),
      );
    }
    await expectRevealedInViewport(page, page.getByTestId("committed-context"));

    // Deep-linking a task with no committed context does not error.
    await page.goto(`/next/task/${withoutContext.id}#committed-context`);
    await expect(page.getByTestId("task-tab-conversation")).toHaveAttribute(
      "aria-current",
      "page",
    );
    await expect(page.getByTestId("committed-context")).toHaveCount(0);
    expect(pageErrors).toEqual([]);
  },
);

test(
  "renders read-only repo placement and drops unsafe source_url links cleanly",
  { tag: ["@phone", "@dark"] },
  async ({ page, pageErrors }) => {
    await page.goto("/next/");
    const { withContext } = await findSeededTasks(page);

    await page.route(
      new RegExp(`/api/tasks/${withContext.id}(\\?.*)?$`),
      async (route) => {
        if (route.request().method() !== "GET") return route.fallback();
        const headers = { ...route.request().headers() };
        delete headers["if-none-match"];
        const response = await route.fetch({ headers });
        const body = await response.json();
        const nextContext = [
          ...(body.context || []),
          {
            repo: "acme/product-specs",
            path: "design/plc/042-committed-context-architecture",
            rev: "4d71a90cc3e8102b5f649a12d83c7e09f1b42a61",
            placement: "read",
            manifest: {
              title: "Architecture ADR",
              files: [
                {
                  path: "adr-042.md",
                  kind: "design",
                  title: "Committed context architecture",
                  source_url: "javascript:alert(1)",
                },
              ],
            },
          },
        ];
        await route.fulfill({
          response,
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ ...body, context: nextContext }),
        });
      },
    );

    await page.goto(`/next/task/${withContext.id}#committed-context`);
    const section = page.getByTestId("committed-context");
    await expectRevealedInViewport(page, section);
    await expectSectionLayoutAndA11y(page, section);

    await expect(section.getByText("Target repo")).toBeVisible();
    await expect(section.getByText("Read-only repo")).toBeVisible();
    await expect(
      section.getByText(
        "acme/product-specs:design/plc/042-committed-context-architecture",
      ),
    ).toBeVisible();

    const readSha = section.getByRole("link", { name: "@4d71a90cc3" });
    await expect(readSha).toHaveAttribute(
      "href",
      "https://github.com/acme/product-specs/tree/4d71a90cc3e8102b5f649a12d83c7e09f1b42a61/design/plc/042-committed-context-architecture",
    );

    // Unsafe javascript: source_url on adr-042.md must not render as a link.
    await expect(section.getByText("adr-042.md")).toBeVisible();
    await expect(
      section.getByRole("link", { name: /Source for adr-042\.md/i }),
    ).toHaveCount(0);
    expect(pageErrors).toEqual([]);
  },
);
