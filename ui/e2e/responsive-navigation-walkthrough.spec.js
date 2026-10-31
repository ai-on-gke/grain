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
import { alertsBell, closeAlerts, openAlerts } from "./alerts.js";

test.setTimeout(90_000);

const DESKTOP = { width: 1440, height: 900 };
const TABLET = { width: 820, height: 1180 }; // iPad Air portrait
const TABLET_LANDSCAPE = { width: 1180, height: 820 };
const PHONE_PORTRAIT = { width: 390, height: 844 }; // iPhone 14
const PHONE_LANDSCAPE = { width: 844, height: 390 }; // iPhone 14 rotated

const seededBoards = [];
const seededTemplates = [];
const seededSuites = [];
const seededSchedules = [];
const seededTasks = [];

async function seedOne(page, path, data) {
  const res = await page.request.post(path, { data });
  expect(res.ok(), `POST ${path}: ${res.status()}`).toBe(true);
  return res.json();
}

test.afterEach(async ({ page }) => {
  while (seededBoards.length > 0) {
    await page.request.delete(`/api/boards/${seededBoards.pop()}`);
  }
  while (seededTemplates.length > 0) {
    await page.request.delete(`/api/templates/${seededTemplates.pop()}`);
  }
  while (seededSuites.length > 0) {
    await page.request.delete(`/api/suites/${seededSuites.pop()}`);
  }
  while (seededSchedules.length > 0) {
    await page.request.delete(`/api/schedules/${seededSchedules.pop()}`);
  }
  while (seededTasks.length > 0) {
    await page.request.delete(`/api/tasks/${seededTasks.pop()}`);
  }
});

async function assertNoHorizontalOverflow(page) {
  const overflows = await page.evaluate(() => {
    const docWidth = document.documentElement.clientWidth;
    const scrollWidth = document.documentElement.scrollWidth;
    return {
      docWidth,
      scrollWidth,
      hasOverflow: scrollWidth > docWidth + 1, // allow 1px rounding tolerance
    };
  });
  expect(
    overflows.hasOverflow,
    `Document scrollWidth (${overflows.scrollWidth}px) exceeds clientWidth (${overflows.docWidth}px)`,
  ).toBe(false);
}

test.describe("Responsive Navigation Matrix & Shell Behavior", () => {
  test("Desktop (1440x900): traverses all primary views, boards, overlays, and deep-links with zero overflow", async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // 1. Inbox
    await page.getByRole("button", { name: /^Inbox/ }).click();
    await expect(page).toHaveURL(/\/inbox$/);
    await expect(page.getByRole("heading", { name: "Inbox" })).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // 2. Tasks
    await page.getByRole("button", { name: /^Tasks/ }).click();
    await expect(page).toHaveURL(/\/(?:\?.*)?$/);
    await expect(page.locator(".task-row").first()).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // 3. New task overlay
    await page.getByRole("button", { name: "+ New task" }).click();
    await expect(page).toHaveURL(/\/new$/);
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
    await assertNoHorizontalOverflow(page);
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/(?:\?.*)?$/);

    // 4. Task detail overlay
    await page.locator(".task-row").first().click();
    await expect(page).toHaveURL(/\/tasks\/\d+$/);
    await expect(page.locator(".detail-header")).toBeVisible();
    await assertNoHorizontalOverflow(page);
    await page.getByRole("button", { name: /Back/ }).first().click();
    await expect(page).toHaveURL(/\/(?:\?.*)?$/);

    // 5. Boards index & types
    const stamp = Date.now();
    const kanbanBoard = await seedOne(page, "/api/boards", {
      name: `Walkthrough Kanban ${stamp}`,
      kind: "kanban",
      query: "",
    });
    seededBoards.push(kanbanBoard.id);

    const timelineBoard = await seedOne(page, "/api/boards", {
      name: `Walkthrough Timeline ${stamp}`,
      kind: "gantt",
      query: "",
    });
    seededBoards.push(timelineBoard.id);

    const dashboardBoard = await seedOne(page, "/api/boards", {
      name: `Walkthrough Dashboard ${stamp}`,
      kind: "panels",
      query: "",
    });
    seededBoards.push(dashboardBoard.id);

    const reportBoard = await seedOne(page, "/api/boards", {
      name: `Walkthrough Report ${stamp}`,
      kind: "report",
      query: "",
    });
    seededBoards.push(reportBoard.id);

    await page.getByRole("button", { name: /^Boards/ }).click();
    await expect(page).toHaveURL(/\/boards$/);
    await expect(page.getByRole("heading", { name: "Boards" })).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // Open Kanban
    await page.locator(".template-name", { hasText: kanbanBoard.name }).click();
    await expect(page).toHaveURL(new RegExp(`/boards/${kanbanBoard.id}$`));
    await expect(page.locator(".board-column").first()).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // Open Timeline
    await page.getByRole("button", { name: /^Boards/ }).click();
    await page
      .locator(".template-name", { hasText: timelineBoard.name })
      .click();
    await expect(page).toHaveURL(new RegExp(`/boards/${timelineBoard.id}$`));
    await expect(page.locator(".gantt")).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // Open Dashboard
    await page.getByRole("button", { name: /^Boards/ }).click();
    await page
      .locator(".template-name", { hasText: dashboardBoard.name })
      .click();
    await expect(page).toHaveURL(new RegExp(`/boards/${dashboardBoard.id}$`));
    await expect(
      page.getByText(/This dashboard has no panels yet/i),
    ).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // Open Report
    await page.getByRole("button", { name: /^Boards/ }).click();
    await page.locator(".template-name", { hasText: reportBoard.name }).click();
    await expect(page).toHaveURL(new RegExp(`/boards/${reportBoard.id}$`));
    await expect(page.locator(".report-sections")).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // 6. Repos
    await page.getByRole("button", { name: /^Repos/ }).click();
    await expect(page).toHaveURL(/\/repos$/);
    await expect(page.getByRole("heading", { name: "Repos" })).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // Open Repo detail
    const repoRow = page.locator(".repo-list li").first();
    await expect(repoRow).toBeVisible();
    const repoName = await repoRow.locator(".repo-list-name").innerText();
    await repoRow.click();
    // The report board just left asked nothing of its tasks, and the one
    // narrowing every task view shares carries that over to this page's
    // list, written as the empty marker paths.js gives a cleared
    // narrowing.
    await expect(page).toHaveURL(
      new RegExp(`/repos/${repoName}(?:\\?f=none)?$`),
    );
    await expect(page.getByRole("heading", { name: repoName })).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // Open Releases subpage
    await page.getByRole("button", { name: "Releases", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/repos/${repoName}/releases$`));
    await assertNoHorizontalOverflow(page);

    // 7. CI
    await page.getByRole("button", { name: /^CI/ }).click();
    await expect(page).toHaveURL(/\/ci$/);
    await expect(page.locator(".ci-panel")).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // 7b. Memory, a Settings section now, by its old link
    await page.goto("/memory");
    await expect(page).toHaveURL(/\/settings\/memory$/);
    await expect(page.getByTestId("memory-page")).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // 8. Schedules, Templates, Suites
    const schedule = await seedOne(page, "/api/schedules", {
      title: `E2E Schedule ${stamp}`,
      repo: "acme/widgets",
      recurrence: { kind: "everyNHours", everyNHours: 24 },
    });
    seededSchedules.push(schedule.id);

    const template = await seedOne(page, "/api/templates", {
      name: `E2E Template ${stamp}`,
      title: "Run benchmarks",
    });
    seededTemplates.push(template.id);

    const suite = await seedOne(page, "/api/suites", {
      name: `E2E Suite ${stamp}`,
      templateIds: [template.id],
      mode: "count",
      count: 1,
    });
    seededSuites.push(suite.id);

    await page
      .locator("aside")
      .getByRole("button", { name: /^Processes/ })
      .click();
    await expect(page).toHaveURL(/\/processes(\?kind=schedule)?$/);
    await page
      .locator(".process-row-schedule", { hasText: schedule.title })
      .click();
    await expect(page).toHaveURL(
      new RegExp(`/processes/schedules/${schedule.id}$`),
    );
    await expect(
      page.getByRole("heading", { name: "Edit schedule" }),
    ).toBeVisible();
    await assertNoHorizontalOverflow(page);
    await page.locator(".overlay-pane-back button").click();
    await expect(page).toHaveURL(/\/processes(\?kind=schedule)?$/);

    await page
      .locator("aside")
      .getByRole("button", { name: /^Processes/ })
      .click();
    await expect(page).toHaveURL(/\/processes(\?kind=template)?$/);
    await page
      .locator(".process-row-template", { hasText: template.name })
      .click();
    await expect(page).toHaveURL(
      new RegExp(`/processes/templates/${template.id}$`),
    );
    await expect(
      page.getByRole("heading", { name: "Edit template" }),
    ).toBeVisible();
    await assertNoHorizontalOverflow(page);
    await page.locator(".overlay-pane-back button").click();
    await expect(page).toHaveURL(/\/processes(\?kind=template)?$/);

    await page
      .locator("aside")
      .getByRole("button", { name: /^Processes/ })
      .click();
    await expect(page).toHaveURL(/\/processes(\?kind=suite)?$/);
    await page.locator(".process-row-suite", { hasText: suite.name }).click();
    await expect(page).toHaveURL(new RegExp(`/processes/suites/${suite.id}$`));
    await expect(
      page.getByRole("heading", { name: "Edit suite" }),
    ).toBeVisible();
    await assertNoHorizontalOverflow(page);
    await page.locator(".overlay-pane-back button").click();
    await expect(page).toHaveURL(/\/processes(\?kind=suite)?$/);

    // 11. Settings: a view behind the gear (docs/design/settings-panel.md)
    await page.getByTestId("top-bar-settings").click();
    await expect(page).toHaveURL(/\/settings\/models$/);
    await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // 10. System: status and operations, behind the icon beside the gear
    await page.getByTestId("top-bar-system").click();
    await expect(page).toHaveURL(/\/system$/);
    await expect(
      page.getByRole("heading", { name: "System", exact: true }),
    ).toBeVisible();
    await expect(page.getByRole("tab", { name: "Sandboxes" })).toBeVisible();
    await expect(page.getByRole("tab", { name: "Caches" })).toBeVisible();
    await page.getByRole("tab", { name: "Sandboxes" }).click();
    await expect(page.locator(".sandbox-health-panel")).toBeVisible();
    await assertNoHorizontalOverflow(page);
    await page.getByRole("tab", { name: "Caches" }).click();
    await expect(page.getByRole("heading", { name: "Caches" })).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // 11. Metrics
    await page.getByRole("tab", { name: "Metrics" }).click();
    await expect(page).toHaveURL(/\/system\/metrics$/);
    await expect(page.getByRole("heading", { name: "Metrics" })).toBeVisible();
    await assertNoHorizontalOverflow(page);
  });

  test("Phone Portrait (390x844) & Landscape (844x390): Drawer navigation, full width overlays, and zero overflow", async ({
    page,
  }) => {
    await page.setViewportSize(PHONE_PORTRAIT);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // PhoneNav bar is visible
    const navButton = page.getByRole("button", { name: "Open navigation" });
    await expect(navButton).toBeVisible();

    // Open drawer and visit Inbox
    await navButton.click();
    await expect(page.getByRole("button", { name: /^Repos/ })).toBeVisible();
    // The open drawer is a modal dialog named Navigation, holding the
    // rail -- not a role="presentation" root whose name nothing reads
    // (2026-10-23 responsive walkthrough, bug 1).
    const drawer = page.getByRole("dialog", {
      name: "Navigation",
      exact: true,
    });
    await expect(drawer).toBeVisible();
    await expect(drawer).toHaveAttribute("aria-modal", "true");
    await expect(drawer.getByRole("button", { name: /^Inbox/ })).toBeVisible();
    await page.getByRole("button", { name: /^Inbox/ }).click();
    await expect(drawer).toHaveCount(0);
    await expect(page).toHaveURL(/\/inbox$/);
    await expect(page.getByRole("heading", { name: "Inbox" })).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // Open drawer and visit Repos
    await navButton.click();
    await expect(page.getByRole("button", { name: /^Repos/ })).toBeVisible();
    await page.getByRole("button", { name: /^Repos/ }).click();
    await expect(page).toHaveURL(/\/repos$/);
    await expect(page.getByRole("heading", { name: "Repos" })).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // Open task overlay on phone: occupies full width
    await navButton.click();
    await expect(page.getByRole("button", { name: /^Repos/ })).toBeVisible();
    await page.getByRole("button", { name: /^Tasks/ }).click();
    await page.locator(".task-row").first().click();
    await expect(page).toHaveURL(/\/tasks\/\d+$/);
    await expect(page.locator(".detail-layout")).toBeVisible();
    const paperBox = await page.locator(".MuiDialog-paper").boundingBox();
    expect(paperBox.width).toBe(PHONE_PORTRAIT.width); // 100% full screen on phone
    await assertNoHorizontalOverflow(page);

    // Close detail overlay via Back button
    await page.getByRole("button", { name: /Back/ }).first().click();
    await expect(page).toHaveURL(/\/(?:\?.*)?$/);

    // Test rotation to Phone Landscape (844x390)
    await page.setViewportSize(PHONE_LANDSCAPE);
    await expect(page.locator(".task-row").first()).toBeVisible();
    await assertNoHorizontalOverflow(page);
    // On short landscape phone, PhoneNav remains in place
    await expect(navButton).toBeVisible();

    // Open drawer in landscape phone
    await navButton.click();
    await expect(page.getByRole("button", { name: /^Repos/ })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(
      page.getByRole("button", { name: /^Repos/ }),
    ).not.toBeVisible();
    await assertNoHorizontalOverflow(page);
  });

  test("Tablet (820x1180) & dynamic orientation/resize: keeps rail beside content with a standing alert up", async ({
    page,
  }) => {
    // A real pipeline pause raises an alert, and is lifted again at the
    // end so the next spec on this worker's demo finds the pipeline running.
    const paused = await page.request.post("/api/pipeline-pause", {
      data: { reason: "Maintenance in progress" },
    });
    expect(paused.ok()).toBe(true);

    await page.setViewportSize(TABLET);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // The pause is behind the bell rather than a banner over the page
    // (grain/task-4008), and the bell is on the page's own header rail,
    // which is the top of the pane: nothing sits above it and the nav
    // rail beside it (grain/task-4915).
    await expect(alertsBell(page)).toHaveAttribute("data-severity", "warning");
    await expect(page.getByText(/Maintenance in progress/i)).toHaveCount(0);
    await expect(page.getByTestId("top-bar")).toHaveCount(0);
    const railBox = await page.getByTestId("page-header").boundingBox();
    const asideBox = await page.locator("aside").boundingBox();
    expect(Math.abs(railBox.y - asideBox.y)).toBeLessThanOrEqual(1);
    await expect(
      page.getByTestId("page-header").locator(".content-header"),
    ).toHaveCount(1);
    await assertNoHorizontalOverflow(page);

    // Rail is visible beside content
    const sidebar = page.locator("aside");
    await expect(sidebar).toBeVisible();
    const sidebarBox = await sidebar.boundingBox();
    expect(sidebarBox.width).toBe(232);

    // Detail overlay on tablet: occupies window width minus sidebar width (820 - 232 = 588)
    await page.locator(".task-row").first().click();
    await expect(page.locator(".detail-layout")).toBeVisible();
    const paperBox = await page.locator(".MuiDialog-paper").boundingBox();
    expect(Math.abs(paperBox.width - (820 - 232))).toBeLessThanOrEqual(2);
    await assertNoHorizontalOverflow(page);

    // Switch to Tablet Landscape (1180x820)
    await page.setViewportSize(TABLET_LANDSCAPE);
    await assertNoHorizontalOverflow(page);
    const widePaperBox = await page.locator(".MuiDialog-paper").boundingBox();
    expect(Math.abs(widePaperBox.width - (1180 - 232))).toBeLessThanOrEqual(2);

    await page.getByRole("button", { name: /Back/ }).first().click();
    await expect(page).toHaveURL(/\/(?:\?.*)?$/);
    await page.request.delete("/api/pipeline-pause");
  });

  test("Short viewport nested scrolling: rail scrolls independently and build stamp remains reachable", async ({
    page,
  }) => {
    // Set short viewport height
    await page.setViewportSize({ width: 1280, height: 500 });
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const aside = page.locator("aside");
    const isScrollable = await aside.evaluate(
      (el) => el.scrollHeight > el.clientHeight,
    );
    expect(isScrollable).toBe(true);

    // Scroll rail to bottom to view build stamp, the rail's foot now the
    // user tile is gone (docs/design/settings-panel.md §4.3)
    const buildStamp = aside.getByTestId("build-stamp");
    await buildStamp.scrollIntoViewIfNeeded();
    await expect(buildStamp).toBeVisible();
    const stampBox = await buildStamp.boundingBox();
    expect(stampBox.height).toBeGreaterThan(0);
    expect(stampBox.y + stampBox.height).toBeLessThanOrEqual(500);
    await expect(aside.getByTestId("user-tile")).toHaveCount(0);

    // System and Settings are reachable from the page's header rail
    await expect(page.getByTestId("top-bar-system")).toBeVisible();
    await expect(page.getByTestId("top-bar-settings")).toBeVisible();
  });

  test("Sticky action bars: BatchActionsBar reserves clearance for last row on phone and tablet", async ({
    page,
  }) => {
    await page.setViewportSize(PHONE_PORTRAIT);
    await page.goto("/");
    const rows = page.locator(".task-row");
    await expect(rows.first()).toBeVisible();

    // Select the first task to show the batch actions bar
    await rows.first().getByRole("checkbox").check();
    const batchBar = page
      .locator(".MuiPaper-root", { hasText: "1 selected" })
      .last();
    await expect(batchBar).toBeVisible();

    // Scroll main to the very bottom
    await page.locator("main").evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });
    const lastRowBox = await rows.last().boundingBox();
    const barBox = await batchBar.boundingBox();

    // The last row's bottom must not be hidden behind the sticky batch actions bar
    expect(lastRowBox.y + lastRowBox.height).toBeLessThanOrEqual(barBox.y + 1);
    await assertNoHorizontalOverflow(page);
  });

  test("Long names, Markdown, and tables: contain horizontal overflow within blocks", async ({
    page,
  }) => {
    const longTitle =
      "SupercalifragilisticexpialidociousLongUnbrokenIdentifierThatShouldWrapProperlyWithoutCausingHorizontalDocumentOverflowOrBrokenLayout1234567890";
    const longBody = `# Deep Heading
Here is a very wide markdown table:

| Column One | Column Two | Column Three | Column Four | Column Five | Column Six |
| :--- | :--- | :--- | :--- | :--- | :--- |
| Long cell content alpha beta gamma delta epsilon | Second long cell content omega | Third cell | Fourth cell with extra text | Fifth cell | Sixth cell |
| Row 2 cell 1 | Row 2 cell 2 | Row 2 cell 3 | Row 2 cell 4 | Row 2 cell 5 | Row 2 cell 6 |

And a wide code block:
\`\`\`go
func VeryLongFunctionNameWithLotsOfParameters(firstParam string, secondParam int, thirdParam map[string]interface{}) (result string, err error) {
    return fmt.Sprintf("formatted-%s-%d", firstParam, secondParam), nil
}
\`\`\`
`;

    const task = await seedOne(page, "/api/tasks", {
      title: longTitle,
      description: longBody,
    });
    seededTasks.push(task.id);

    // Test on Phone Portrait
    await page.setViewportSize(PHONE_PORTRAIT);
    await page.goto(`/tasks/${task.id}`);
    await expect(page.locator(".detail-header")).toBeVisible();

    // Check that pre/table has its own scroll and document has no horizontal overflow
    const pre = page.locator("pre").first();
    await expect(pre).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // Test on Tablet
    await page.setViewportSize(TABLET);
    await page.goto(`/tasks/${task.id}`);
    await expect(page.locator(".detail-header")).toBeVisible();
    await assertNoHorizontalOverflow(page);
  });
});

test.describe("Input Methods, Accessibility & Themes", () => {
  test("Keyboard-only navigation and Escape focus restoration", async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // Tab through to New task button and trigger with Enter
    await page.keyboard.press("Tab");
    const focused = page.locator(":focus");
    await expect(focused).toBeVisible();

    // Open New task form via keyboard shortcut / button
    await page.getByRole("button", { name: "+ New task" }).focus();
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/\/new$/);
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

    // Fill title via keyboard
    await page.keyboard.type("Keyboard filed task");

    // Press Escape to dismiss dialog and check URL
    await page.keyboard.press("Escape");
    await expect(page).toHaveURL(/\/(?:\?.*)?$/);

    // Drawer keyboard dismiss on phone
    await page.setViewportSize(PHONE_PORTRAIT);
    await page.goto("/");
    const navBtn = page.getByRole("button", { name: "Open navigation" });
    await navBtn.focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("button", { name: /^Repos/ })).toBeVisible();

    await page.keyboard.press("Escape");
    await expect(
      page.getByRole("button", { name: /^Repos/ }),
    ).not.toBeVisible();
    // Focus should be restored to the open navigation button
    await expect(navBtn).toBeFocused();
  });

  test("Touch and coarse-pointer target dimensions (>= 40px/44px)", async ({
    page,
  }) => {
    await page.setViewportSize(PHONE_PORTRAIT);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // Test PhoneNav buttons
    const menuBtn = page.getByRole("button", { name: "Open navigation" });
    const menuBox = await menuBtn.boundingBox();
    expect(menuBox.width).toBeGreaterThanOrEqual(40);
    expect(menuBox.height).toBeGreaterThanOrEqual(40);

    const addTaskBtn = page.getByRole("button", { name: "New task" });
    const addBox = await addTaskBtn.boundingBox();
    expect(addBox.width).toBeGreaterThanOrEqual(40);
    expect(addBox.height).toBeGreaterThanOrEqual(40);

    // Open drawer and test navigation items
    await menuBtn.click();
    const navItems = page.locator(".MuiDrawer-paper .MuiListItemButton-root");
    const count = await navItems.count();
    expect(count).toBeGreaterThan(0);
    for (let i = 0; i < Math.min(count, 5); i++) {
      const box = await navItems.nth(i).boundingBox();
      expect(box.height).toBeGreaterThanOrEqual(40);
    }
    await page.keyboard.press("Escape");
  });

  test("Themes and Reduced Motion rendering", async ({ page }) => {
    await page.emulateMedia({ colorScheme: "dark", reducedMotion: "reduce" });
    await page.setViewportSize(DESKTOP);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // Verify dark background applied
    const bgColor = await page.evaluate(() => {
      return window.getComputedStyle(document.body).backgroundColor;
    });
    // Dark theme should have a dark background (rgb values < 60)
    expect(bgColor).toMatch(/rgb\(\s*\d+\s*,\s*\d+\s*,\s*\d+\s*\)/);
    const [r, g, b] = bgColor.match(/\d+/g).map(Number);
    expect(r).toBeLessThan(60);
    expect(g).toBeLessThan(60);
    expect(b).toBeLessThan(60);

    await assertNoHorizontalOverflow(page);
  });
});

// grain/task-1630: the Timeline's Range and Lanes pickers sat side by
// side in a header that does not wrap, inside .gantt, which clips its
// overflow. On a 390px phone the Lanes picker ran 9px past the edge and
// at 320px 79px, cut off with nothing to scroll it into view.
test("A timeline board's pickers stay on a phone's screen and work", async ({
  page,
}) => {
  for (const viewport of [{ width: 320, height: 568 }, PHONE_PORTRAIT]) {
    await page.setViewportSize(viewport);
    await page.goto("/boards/board-2");
    const lanes = page.getByRole("combobox", { name: "Lanes" });
    await expect(lanes).toBeVisible();
    for (const name of ["Range", "Lanes"]) {
      const box = await page.getByRole("combobox", { name }).boundingBox();
      expect(box.x, `${name} at ${viewport.width}px`).toBeGreaterThanOrEqual(0);
      expect(
        box.x + box.width,
        `${name} at ${viewport.width}px`,
      ).toBeLessThanOrEqual(viewport.width);
    }
    await assertNoHorizontalOverflow(page);
  }

  await page.getByRole("combobox", { name: "Lanes" }).click();
  await page.getByRole("option", { name: "By state" }).click();
  await expect(page.getByRole("combobox", { name: "Lanes" })).toHaveText(
    "By state",
  );
});

// grain/task-1818: three rows in overlay panes never wrapped, so on a
// 320px phone each one pushed its last control past the pane's edge:
// a schedule's Cancel/Save beside Delete/Pause/Run now (Save ended at
// 361px), Admin's "Add system admin" field held at 260px with Add after
// it (349px; the field went with the admin list, task 4880, and Access's
// own row is what is held to it now), and the Logs toolbar, where Search was squeezed to 0px wide
// and Refresh ended at 325px. The pane scrolled sideways to reach them,
// which on a phone is nothing anyone finds.
test("Overlay action rows keep every control on a 320px phone", async ({
  page,
}) => {
  const cases = [
    {
      path: "/schedules/sched-1",
      controls: ["Delete", "Pause", "Run now", "Cancel", "Save"],
    },
    // Settings and System are views, not overlay panes
    // (docs/design/settings-panel.md §3.1): their rows are in the main
    // landmark.
    { path: "/settings/access", controls: ["Log out"], view: true },
    { path: "/system/logs", controls: ["Refresh"], view: true },
  ];
  for (const viewport of [{ width: 320, height: 568 }, PHONE_PORTRAIT]) {
    await page.setViewportSize(viewport);
    for (const { path, controls, view } of cases) {
      await page.goto(path);
      const pane = view ? page.getByRole("main") : page.getByRole("dialog");
      await expect(
        pane.getByRole("button", { name: controls.at(-1), exact: true }),
      ).toBeVisible();
      for (const name of controls) {
        const box = await pane
          .getByRole("button", { name, exact: true })
          .boundingBox();
        expect(
          box.x + box.width,
          `${name} on ${path} at ${viewport.width}px`,
        ).toBeLessThanOrEqual(viewport.width);
      }
      expect(
        await page
          .locator(view ? '[role="main"]' : ".overlay-pane")
          .evaluate((el) => el.scrollWidth - el.clientWidth),
        `${path} scrolls sideways at ${viewport.width}px`,
      ).toBeLessThanOrEqual(1);
      await assertNoHorizontalOverflow(page);
    }
    // The search field has to be wide enough to type into, not merely
    // present: at 320px it used to be laid out 0px wide.
    const search = page.getByRole("textbox", { name: "Search logs" });
    const box = await search.boundingBox();
    expect(box.width).toBeGreaterThan(120);
    expect(box.x + box.width).toBeLessThanOrEqual(viewport.width);
    await search.fill("no line says this");
    await expect(page.getByText("(no matching log lines)")).toBeVisible();
  }
});

// grain/task-1987: a repo page's state counts sat in one `.chips` strip
// that never wrapped anywhere but a phone. At a tablet's 820px, and at
// any desktop window narrower than about 1200px, the demo repo's eleven
// counts ran 1155px wide in a 588px column: Deferred, Closed and Blocked
// were past the column's edge, and the column scrolled sideways to show
// them.
test("A repo page's state counts wrap inside its column on a tablet", async ({
  page,
}) => {
  for (const viewport of [TABLET, { width: 1100, height: 800 }]) {
    await page.setViewportSize(viewport);
    await page.goto("/repos/acme/widgets");
    const chips = page.locator(".repo-page-header .chips .MuiChip-root");
    await expect(chips.first()).toBeVisible();
    expect(await chips.count()).toBeGreaterThanOrEqual(6);
    const column = page.locator(".main-column");
    const edge = await column.evaluate(
      (el) => el.getBoundingClientRect().right,
    );
    for (const chip of await chips.all()) {
      const box = await chip.boundingBox();
      expect(
        box.x + box.width,
        `"${await chip.textContent()}" at ${viewport.width}px`,
      ).toBeLessThanOrEqual(edge + 1);
    }
    expect(
      await column.evaluate((el) => el.scrollWidth - el.clientWidth),
      `the repo page scrolls sideways at ${viewport.width}px`,
    ).toBeLessThanOrEqual(1);
    await assertNoHorizontalOverflow(page);
  }
});

// grain/task-1987: a task's address names only the task, so Back or
// Forward onto a pane rebuilt the page behind it from "/tasks/N" alone
// -- the task list -- and the pane's own "← Back" then went to All
// tasks rather than the board, repo page or inbox it was opened over.
// Seen as: board → task → Back → Forward → "← Back" on /, and board →
// task 11 → its link to task 8 → Back → "← Back" on /.
test("Back and Forward onto a task pane keep the page it was opened over", async ({
  page,
}) => {
  await page.setViewportSize(PHONE_PORTRAIT);
  const back = () => page.getByRole("button", { name: /Back/ }).first();

  for (const start of ["/boards/board-1", "/inbox"]) {
    await page.goto(start);
    const link = page.locator("a[href^='/tasks/']").first();
    const href = await link.getAttribute("href");
    await link.click();
    await expect(page).toHaveURL(new RegExp(`${href}$`));
    await page.goBack();
    await expect(page).toHaveURL(new RegExp(`${start}$`));
    await page.goForward();
    await expect(page).toHaveURL(new RegExp(`${href}$`));
    await expect(page.getByRole("dialog")).toBeVisible();
    await back().click();
    await expect(page, `"← Back" after Forward from ${start}`).toHaveURL(
      new RegExp(`${start}$`),
    );
  }

  // One pane to another, then Back to the first: its "← Back" still
  // goes to the board underneath both.
  await page.goto("/boards/board-4");
  await page.getByRole("link", { name: "11", exact: true }).click();
  await expect(page).toHaveURL(/\/tasks\/11$/);
  await page.getByRole("dialog").locator(".provenance-toggle").click();
  await page.getByRole("dialog").locator("a[href='/tasks/8']").first().click();
  await expect(page).toHaveURL(/\/tasks\/8$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/tasks\/11$/);
  await back().click();
  await expect(page).toHaveURL(/\/boards\/board-4$/);
});

// grain/task-1987: the repo page's merge batch named its members with
// plain links (MergeBatchPanel had no onOpenTask there), so a click
// reloaded the whole app on /tasks/N and its "← Back" went to All tasks.
test("A repo page's merge batch opens its tasks in place", async ({ page }) => {
  await page.setViewportSize(TABLET);
  await page.goto("/repos/acme/widgets");
  const member = page.locator(".repo-page-body a[href^='/tasks/']").first();
  await expect(member).toBeVisible();
  const href = await member.getAttribute("href");
  await page.evaluate(() => {
    window.__sameDocument = true;
  });
  await member.click();
  await expect(page).toHaveURL(new RegExp(`${href}$`));
  await expect(page.getByRole("dialog")).toBeVisible();
  expect(
    await page.evaluate(() => window.__sameDocument === true),
    "the click reloaded the page",
  ).toBe(true);
  await page.getByRole("button", { name: /Back/ }).first().click();
  await expect(page).toHaveURL(/\/repos\/acme\/widgets$/);
  await assertNoHorizontalOverflow(page);
});

// grain/task-1987: an alert's message is `overflow: auto`, so one word
// longer than its line -- a pause reason with no spaces, the declaring
// file's path on a repo-declared schedule -- scrolled sideways inside
// the alert (2012px in 252px for a 199-character reason) instead of
// wrapping. Real pause, real declared schedule: nothing stubbed.
test("Banners and alerts wrap an unbroken word on a 320px phone", async ({
  page,
}) => {
  const reason = "rotating-the-signing-key-" + "x".repeat(150);
  const paused = await page.request.post("/api/pipeline-pause", {
    data: { reason },
  });
  expect(paused.ok()).toBe(true);
  try {
    await page.setViewportSize({ width: 320, height: 568 });
    for (const path of ["/", "/schedules/repo:schedule:acme:widgets:nightly"]) {
      await page.goto(path);
      // The pause is a row of the alerts panel (grain/task-4008), opened
      // from the task list; the schedule's page is an overlay the top
      // bar sits under, and its own alerts are what is asked of it.
      if (path === "/") {
        await openAlerts(page);
        await expect(page.getByText(reason.slice(0, 30))).toBeVisible();
      } else {
        // The first alert with words: every dialog's paper also holds
        // the disconnected strip (ModalStatusMirror), an empty, hidden
        // alert until the daemon is unreachable.
        await expect(
          page.locator(".MuiAlert-message").filter({ hasText: /\S/ }).first(),
        ).toBeVisible();
      }
      const sideways = await page
        .locator(".MuiAlert-message")
        .evaluateAll((els) =>
          els
            .filter((el) => el.scrollWidth > el.clientWidth + 1)
            .map((el) => `${el.scrollWidth}/${el.clientWidth}`),
        );
      expect(sideways, `alerts scrolling sideways on ${path}`).toEqual([]);
      await assertNoHorizontalOverflow(page);
      if (path === "/") await closeAlerts(page);
    }
  } finally {
    await page.request.delete("/api/pipeline-pause");
  }
});

// The same unbroken reason in a task's pane: a usage-limit pause names
// itself there too ("connection gemini-pro is paused until
// ... — <reason>"), once

// in the activity line and once under the actions, as plain text rather
// than an alert. Unwrapped, it widened the pane to the reason's length
// (1171px in a 389px pane on a phone) and the pane scrolled sideways.
// The pause is a provider's usage limit, which the demo cannot hit, so
// it is overlaid onto the real GET /api/config -- on every connection of
// the default team, so its queued task has nothing to rotate onto;
// everything else is the demo's own.
test("A task pane wraps an unbroken usage-limit pause reason", async ({
  page,
}) => {
  const reason = "provider-window-" + "x".repeat(180);
  await page.route("**/api/config", async (route) => {
    const response = await route.fetch();
    const json = await response.json();
    // Every connection of the default team is paused, and so are its runs
    // on no connection: other specs share this demo and may have given
    // the team another connection the task could rotate onto.
    const own = (json.agentBackends || []).filter(
      (b) => (b.team || "default") === "default",
    );
    json.agentPause = {
      paused: true,
      reason,
      pauses: [
        ...own.map((b) => ({
          connection: b.credentialBackendId || b.id,
          team: "default",
          framework: b.framework,
          reason,
        })),
        { team: "default", reason },
      ],
    };
    await route.fulfill({ response, json });
  });
  for (const size of [TABLET, PHONE_PORTRAIT, { width: 320, height: 568 }]) {
    await page.setViewportSize(size);
    // Task 2 is the demo's queued task: the activity line is a queued
    // task's, and the hint under the actions any open one's.
    await page.goto("/tasks/2");
    const pane = page.locator(".overlay-pane");
    await expect(pane.locator(".detail-framework-paused")).toContainText(
      reason.slice(0, 20),
    );
    await expect(
      pane.locator("p.hint", { hasText: "A run asked for now waits" }),
    ).toContainText(reason.slice(0, 20));
    const widths = await pane.evaluate((el) => [
      el.scrollWidth,
      el.clientWidth,
    ]);
    expect(
      widths[0],
      `pane ${widths[0]}/${widths[1]} at ${size.width}px`,
    ).toBeLessThanOrEqual(widths[1] + 1);
    await assertNoHorizontalOverflow(page);
  }
});

// grain/task-1630: the drawer closed on a tap but not on a keyboard
// pick. Its entries are ListItemButtons, which answer Enter and Space by
// calling onClick themselves rather than with a click event, so the page
// changed behind a drawer that stayed open and kept focus trapped in it.
test("A phone's drawer closes when an entry is chosen from the keyboard", async ({
  page,
}) => {
  await page.setViewportSize(PHONE_PORTRAIT);
  await page.goto("/");
  await expect(page.locator(".task-row").first()).toBeVisible();
  const menu = page.getByRole("button", { name: "Open navigation" });

  for (const [key, entry, url] of [
    ["Enter", /^Processes/, /\/processes$/],
    ["Space", /^CI/, /\/ci$/],
  ]) {
    await menu.focus();
    await page.keyboard.press("Enter");
    const drawer = page.locator(".MuiDrawer-modal");
    await expect(drawer).toBeVisible();
    await drawer.getByRole("button", { name: entry }).focus();
    await page.keyboard.press(key);

    await expect(page).toHaveURL(url);
    await expect(drawer).toHaveCount(0);
    await expect(menu).toBeFocused();
  }
  // And the page behind it is the one the keyboard reaches next.
  await page.keyboard.press("Tab");
  await expect(
    page.locator(".MuiDrawer-modal").getByRole("button"),
  ).toHaveCount(0);
});

// grain/task-1630: an edited schedule asked before Escape or "← Back"
// threw the edit away, but the browser's Back -- on a phone, the back
// gesture -- closed it without a word.
test("Browser Back asks before it drops an edited schedule on a phone", async ({
  page,
}) => {
  const schedule = await seedOne(page, "/api/schedules", {
    title: "Back-guarded schedule",
    repo: "acme/widgets",
    recurrence: { kind: "everyNHours", everyNHours: 24 },
    enabled: true,
  });
  seededSchedules.push(schedule.id);
  await page.setViewportSize(PHONE_PORTRAIT);
  await page.goto("/schedules");
  await page.getByRole("link", { name: "Back-guarded schedule" }).click();
  await expect(page).toHaveURL(
    new RegExp(`/processes/schedules/${schedule.id}$`),
  );
  const title = page.getByRole("textbox", { name: "Title" });
  await title.fill("Back-guarded schedule, edited");

  const asked = [];
  page.once("dialog", (d) => {
    asked.push(d.message());
    d.dismiss();
  });
  await page.goBack();
  await expect.poll(() => asked.length).toBe(1);
  expect(asked[0]).toBe(
    "You have unsaved changes here. Leave and discard them?",
  );
  await expect(page).toHaveURL(
    new RegExp(`/processes/schedules/${schedule.id}$`),
  );
  await expect(title).toHaveValue("Back-guarded schedule, edited");

  page.once("dialog", (d) => d.accept());
  await page.goBack();
  await expect(page).toHaveURL(/\/processes(\?kind=schedule)?$/);
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.reload();
  await expect(
    page.getByRole("link", { name: "Back-guarded schedule" }),
  ).toBeVisible();
});

test.describe("Live Interaction Surfaces & Sessions", () => {
  test("Standing chat session (Director): messaging, markdown, disconnect recovery, and session switching", async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);

    // The demo seeds the Director; a second standing session is created
    // through the real POST /api/console-sessions, which is what the rail's
    // own "new session" does. A second copy of this file on the same demo
    // finds it already there.
    const created = await page.request.post("/api/console-sessions", {
      data: { id: "operations", title: "Operations" },
    });
    if (!created.ok()) {
      const listed = await (
        await page.request.get("/api/console-sessions")
      ).json();
      expect(listed.sessions.map((session) => session.id)).toContain(
        "operations",
      );
    }

    await page.goto("/session/director");
    await expect(page.getByRole("heading", { name: "Director" })).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // Verify chat composer is visible
    const input = page.locator(".cs-message-input__content-editor");
    await expect(input).toBeVisible();

    // Send a message with multiline Markdown, bold text, and code snippet
    const msgText = "**Hello Director**\nPlease inspect the cluster health.";
    await input.fill(msgText);
    await page.keyboard.press("Enter");

    // Message appears in the chat transcript
    const userMsg = page.locator(".cs-message--outgoing").last();
    await expect(userMsg).toBeVisible();
    await expect(userMsg.getByText("Hello Director")).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // Direct link to secondary session
    await page.getByRole("button", { name: /^Sessions/ }).click();
    await page.getByText("Operations").click();
    await expect(page).toHaveURL(/\/session\/operations$/);
    await expect(
      page.getByRole("heading", { name: "Operations" }),
    ).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // Hard reload preserves session URL and state
    await page.reload();
    await expect(page).toHaveURL(/\/session\/operations$/);
    await expect(
      page.getByRole("heading", { name: "Operations" }),
    ).toBeVisible();
    await assertNoHorizontalOverflow(page);

    // Browser Back returns through Sessions list to Director
    await page.goBack();
    await expect(page).toHaveURL(/\/sessions$/);
    await page.goBack();
    await expect(page).toHaveURL(/\/session\/director$/);
    await expect(page.getByRole("heading", { name: "Director" })).toBeVisible();

    // Browser Forward returns to Operations
    await page.goForward();
    await page.goForward();
    await expect(page).toHaveURL(/\/session\/operations$/);
    await expect(
      page.getByRole("heading", { name: "Operations" }),
    ).toBeVisible();
  });

  // A chat opened at a desktop's size started 4px short of its newest
  // message, and so never followed one. The library's message list takes
  // its height when it mounts, a frame before the composer's scroll
  // wrapper gets the class that sets its line-height; when the composer
  // then shrank by those 4px, the list's own resize handler scrolled up
  // by them, and its "pinned to the bottom" test is exact to a pixel.
  // Every reply and every message sent after that landed below the fold.
  // `toBeVisible` cannot see that -- the message is in the DOM, only
  // scrolled out of its list -- so this measures the list itself.
  // Unstubbed: the demo's Director and real sends.
  test("A session's chat stays pinned to its newest message on a desktop", async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);
    await page.goto("/session/director");
    const list = page.locator(".cs-message-list__scroll-wrapper");
    await expect(list).toBeVisible();
    const unscrolled = () =>
      list.evaluate((el) => el.scrollHeight - el.scrollTop - el.clientHeight);
    const newestInView = () =>
      list.evaluate((el) => {
        const sent = el.querySelectorAll(".cs-message--outgoing");
        const last = sent[sent.length - 1].getBoundingClientRect();
        return last.bottom <= el.getBoundingClientRect().bottom + 1;
      });
    // Only meaningful when the conversation is taller than its list,
    // which the demo's seeded Director already is at 1440x900.
    expect(await list.evaluate((el) => el.scrollHeight > el.clientHeight)).toBe(
      true,
    );
    await page.waitForTimeout(500);
    expect(await unscrolled()).toBeLessThanOrEqual(1);

    const input = page.locator(".cs-message-input__content-editor");
    for (const n of [1, 2]) {
      await input.fill(`Pinned check ${n}`);
      await page.keyboard.press("Enter");
      await expect(page.locator(".cs-message--outgoing").last()).toContainText(
        `Pinned check ${n}`,
      );
      await expect.poll(newestInView).toBe(true);
      await expect.poll(unscrolled).toBeLessThanOrEqual(1);
    }
  });

  // grain/task-1360: a link to a session by id used to fall back to the
  // first session there was, so a deep link to one since deleted (or
  // mistyped) drew the Director's chat under the other session's URL --
  // and the next message went to an agent nobody picked. Unstubbed: the
  // demo's own Director, and a session created and deleted through the
  // real UI and API.
  test("Deep link to a deleted or unknown session never opens another session's chat", async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);
    await page.goto("/session/does-not-exist");
    await expect(page).toHaveURL(/\/session\/does-not-exist$/);
    await expect(page.getByRole("alert")).toContainText(
      /no session .does-not-exist. here/i,
    );
    await expect(page.locator(".console-session-page")).toHaveCount(0);
    await expect(page.locator(".cs-message-input")).toHaveCount(0);

    // Keyboard-only: the list's rows are buttons, so a session opens from
    // here without a pointer.
    const director = page.getByRole("button", {
      name: "Open session Director",
    });
    await director.focus();
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/\/session\/director$/);
    await expect(page.locator(".console-session-title")).toHaveText("Director");

    // Create one through the rail -- a chat that opens empty and becomes
    // a session on its first message, named from it -- delete it with
    // the confirmation accepted, then go Back onto its URL.
    await page.getByRole("button", { name: "Create options" }).click();
    await page.getByRole("menuitem", { name: "New session" }).click();
    await expect(page).toHaveURL(/\/session\/new$/);
    await expect(page.locator(".console-session-title")).toHaveText("New chat");
    const newBox = page.locator(".cs-message-input__content-editor");
    await newBox.click();
    await page.keyboard.type("Night watch");
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/\/session\/night-watch$/);
    await expect(page.locator(".console-session-title")).toHaveText(
      "Night watch",
    );
    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Delete session" }).click();
    await expect(page.locator(".console-session-title")).toHaveText("Director");
    await page.goto("/session/night-watch");
    await expect(page.getByRole("alert")).toContainText(
      /no session .night-watch. here/i,
    );
    await expect(page.locator(".cs-message-input")).toHaveCount(0);
    await assertNoHorizontalOverflow(page);
  });

  // grain/task-1360: the composer was cleared before the comment was
  // posted and never refilled, so a message sent while the connection was
  // down vanished with the "Failed to fetch" banner. The only stub is the
  // network itself (context.setOffline); the server, the session and its
  // task are the demo's own.
  test("A message sent while offline stays in the composer and sends once back online", async ({
    page,
    context,
  }) => {
    await page.setViewportSize(PHONE_PORTRAIT);
    await page.goto("/session/director");
    const box = page.locator(".cs-message-input__content-editor");
    await expect(box).toBeVisible();

    const text = `offline walkthrough ${Date.now()}`;
    await context.setOffline(true);
    await box.click();
    await page.keyboard.type(text);
    await page.keyboard.press("Enter");
    // The friendly sentence, with each engine's own words for a fetch
    // that never left kept in brackets after it: Chromium's "Failed to
    // fetch", WebKit's "Load failed". Or, if a poll found grain out of
    // reach before the Enter did, the standing banner instead -- which
    // is what keeps the toast away then (grain/task-2009).
    await expect(
      page
        .getByRole("alert")
        .filter({
          hasText: /cannot be reached.*\((Failed to fetch|Load failed)\)/,
        })
        .or(page.getByTestId("disconnected-banner")),
    ).not.toHaveCount(0);
    await expect(box).toHaveText(text);

    await context.setOffline(false);
    await page.getByRole("button", { name: "Send", exact: true }).click();
    await expect(box).toHaveText("");
    await expect(
      page.locator(".cs-message--outgoing").filter({ hasText: text }),
    ).toHaveCount(1);
    await page.reload();
    await expect(
      page.locator(".cs-message--outgoing").filter({ hasText: text }),
    ).toHaveCount(1);
    await assertNoHorizontalOverflow(page);
  });

  // grain/task-2009: with the disconnected banner already up, a send
  // that failed for the same reason also raised the bottom-centre error
  // toast, which sat over Send, "Attach files" and "Delete session" on
  // a tablet and over the session footer on a phone -- saying again what
  // the banner said. The only stub is the network (context.setOffline).
  for (const viewport of [PHONE_PORTRAIT, TABLET]) {
    test(`Sending while grain is out of reach leaves a session's controls uncovered at ${viewport.width}x${viewport.height}`, async ({
      page,
      context,
    }) => {
      await page.setViewportSize(viewport);
      await page.goto("/session/director");
      const box = page.locator(".cs-message-input__content-editor");
      await expect(box).toBeVisible();

      await context.setOffline(true);
      await expect(page.getByTestId("disconnected-banner")).toBeVisible({
        timeout: 20_000,
      });
      const text = `unreachable send ${Date.now()}`;
      await box.click();
      await page.keyboard.type(text);
      await page.keyboard.press("Enter");
      await expect(box).toHaveText(text);

      // Long enough for the failed send to have come back and for a
      // toast to have been drawn, had one been raised.
      await page.waitForTimeout(1000);
      for (const control of [
        page.getByRole("button", { name: "Send", exact: true }),
        page.getByRole("button", { name: "Delete session" }),
      ]) {
        await control.scrollIntoViewIfNeeded();
        const covered = await control.evaluate((el) => {
          const r = el.getBoundingClientRect();
          const hit = document.elementFromPoint(
            r.x + r.width / 2,
            r.y + r.height / 2,
          );
          return hit && !el.contains(hit) ? hit.outerHTML.slice(0, 120) : "";
        });
        expect(covered, `${await control.getAttribute("aria-label")}`).toBe("");
      }
      // Also read once, not retried: the toast hides itself after five
      // seconds, so a retrying assertion would pass by outwaiting it.
      expect(await page.locator(".MuiSnackbar-root").count()).toBe(0);

      await context.setOffline(false);
      await expect(page.getByTestId("disconnected-banner")).toHaveCount(0, {
        timeout: 45_000,
      });
      await page.getByRole("button", { name: "Send", exact: true }).click();
      await expect(box).toHaveText("");
    });
  }

  // grain/task-1854: with the network gone, the session poll and the
  // shell's own poll failed every three seconds into the console and
  // nowhere else, so a session that said "running" could have stopped
  // updating with nothing on screen to say so. The only stub is the
  // network itself (context.setOffline), or, for the gateway case, the
  // one session read answering 503. The banner itself is task-26's
  // DisconnectedBanner, which replaced task-1854's ReconnectingBanner.
  const reconnecting = (page) => page.getByTestId("disconnected-banner");
  const RECONNECT_WAIT = { timeout: 20_000 };
  // Longer on the way back: while grain stays out of reach the main poll
  // backs off to one try in ten ticks (App.jsx's pollBackoff), so the
  // first tick that gets through can be half a minute after the network
  // returns -- the same allowance disconnection.spec.js gives it.
  const RECOVER_WAIT = { timeout: 45_000 };

  for (const [where, path, ready] of [
    ["a session", "/session/director", ".cs-message-input__content-editor"],
    ["the task list", "/", ".task-row"],
  ]) {
    test(`Going offline on ${where} says grain is out of reach, once, and clears without a reload`, async ({
      page,
      context,
    }) => {
      await page.setViewportSize(PHONE_PORTRAIT);
      await page.goto(path);
      await expect(page.locator(ready).first()).toBeVisible();
      await expect(reconnecting(page)).toHaveCount(0);
      await page.evaluate(() => {
        window.__noReload = true;
      });

      await context.setOffline(true);
      await expect(reconnecting(page)).toBeVisible(RECONNECT_WAIT);
      await expect(reconnecting(page)).toHaveAttribute("role", "status");
      await expect(reconnecting(page)).toContainText(
        "This deployment cannot be reached",
      );
      // More failed ticks go by: still the one, and no alert per tick.
      await page.waitForTimeout(7_000);
      await expect(reconnecting(page)).toHaveCount(1);
      await expect(page.getByRole("alert")).toHaveCount(0);
      await assertNoHorizontalOverflow(page);

      await context.setOffline(false);
      await expect(reconnecting(page)).toHaveCount(0, RECOVER_WAIT);
      expect(await page.evaluate(() => window.__noReload)).toBe(true);
    });
  }

  test("A session read answering 503 says grain is out of reach until it answers again", async ({
    page,
  }) => {
    await page.setViewportSize(PHONE_PORTRAIT);
    await page.goto("/session/director");
    await expect(
      page.locator(".cs-message-input__content-editor"),
    ).toBeVisible();

    const sessionRead = /\/api\/tasks\/\d+$/;
    await page.route(sessionRead, (route) =>
      route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({ error: "upstream unavailable" }),
      }),
    );
    await expect(reconnecting(page)).toBeVisible(RECONNECT_WAIT);
    await page.unroute(sessionRead);
    await expect(reconnecting(page)).toHaveCount(0, RECOVER_WAIT);
  });

  // The session composer exposes role="textbox" and name="Message" for screen
  // readers, hides the underlying implementation-detail task link and count,
  // and redirects direct /tasks/{id} navigation for a session's backing task
  // to the session chat rather than opening a task detail dialog.
  test("A session's backing task redirects to the session chat without exposing task details", async ({
    page,
  }) => {
    await page.setViewportSize(TABLET);
    await page.goto("/session/director");
    const box = page.getByRole("textbox", { name: "Message" });
    await expect(box).toBeVisible();
    await box.click();
    await page.keyboard.type("draft kept across the detail");
    await expect(box).toHaveText("draft kept across the detail");

    await expect(
      page.getByRole("link", { name: "Open the task behind this session" }),
    ).toHaveCount(0);
    await expect(page.locator(".console-session-header .count")).toHaveCount(0);

    const res = await page.request.get("/api/console-sessions");
    const { sessions } = await res.json();
    const director = sessions.find((s) => s.id === "director");
    expect(director?.taskId).toBeTruthy();

    await page.goto(`/tasks/${director.taskId}`);
    await expect(page).toHaveURL(/\/session\/director$/);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "Director" })).toBeVisible();
    await assertNoHorizontalOverflow(page);
  });

  test("Session manager error state (HTTP 503) and graceful recovery", async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);
    let failOnce = true;

    // The one fault injected into grain's own API in this file: the first
    // GET /api/console-sessions answers 503, as a daemon still starting its
    // session manager does, and every request after it reaches the real
    // handler -- so the recovery is onto the demo's seeded Director.
    await page.route("**/api/console-sessions", async (route) => {
      if (failOnce && route.request().method() === "GET") {
        failOnce = false;
        await route.fulfill({
          status: 503,
          body: JSON.stringify({ error: "Session daemon starting" }),
        });
      } else {
        await route.fallback();
      }
    });

    await page.goto("/session/director");
    // Recovers gracefully on subsequent poll / retry
    await expect(page.getByRole("heading", { name: "Director" })).toBeVisible();
    await assertNoHorizontalOverflow(page);
  });
});

// 2026-10-07 walkthrough, bug 1: a second session given a title the rail
// already holds is filed as "<slug>-2", but the create handler opened the
// first session carrying that title -- so the reader landed on the older
// session's pane, and its "Delete session" deleted the older one. Every
// step is a real gesture against the real server; nothing is stubbed.
// A session is started from its first message now, and named from it
// (model.ConsoleSessionTitleFromMessage), so two chats opened with the
// same words are the same case.
test("Creating a session with a title already in use opens the new one", async ({
  page,
}) => {
  await page.setViewportSize(DESKTOP);
  const title = `Night shift ${Date.now()}`;
  const create = async () => {
    await page.goto("/sessions");
    await page.getByRole("button", { name: "+ New session" }).first().click();
    await expect(page).toHaveURL(/\/session\/new$/);
    const box = page.locator(".cs-message-input__content-editor");
    await box.click();
    await page.keyboard.type(title);
    await page.keyboard.press("Enter");
    await expect(page).not.toHaveURL(/\/session\/new$/);
  };
  const ids = async () => {
    const res = await page.request.get("/api/console-sessions");
    return (await res.json()).sessions
      .filter((s) => s.title === title)
      .map((s) => s.id);
  };
  try {
    await create();
    const [first] = await ids();
    await expect(page).toHaveURL(new RegExp(`/session/${first}$`));
    await expect(page.getByTestId("page-header-title")).toHaveText(
      `sessions/${title}`,
    );
    await expect(page.getByTestId("top-bar-page")).toHaveText(title);

    // Returning to /sessions immediately shows the new session's backing
    // task status. The first message queued the session's first exchange,
    // and the demo dispatches nothing, so it reads "Starting" -- the
    // point being that the row has a status at all without a poll.
    await page
      .getByRole("complementary")
      .getByRole("button", { name: /^Sessions/ })
      .click();
    await expect(page).toHaveURL(/\/sessions$/);
    await expect(
      page
        .getByRole("listitem")
        .filter({
          has: page.getByRole("button", { name: `Open session ${title}` }),
        })
        .first(),
    ).toContainText("Starting");

    await create();
    await expect.poll(async () => (await ids()).length).toBe(2);
    const second = (await ids()).find((id) => id !== first);
    await expect(page).toHaveURL(new RegExp(`/session/${second}$`));
    await expect(page.getByTestId("page-header-title")).toHaveText(
      `sessions/${title}`,
    );

    // The pane's delete is the new session's, and the older one survives.
    page.once("dialog", (d) => d.accept());
    await page.getByRole("button", { name: "Delete session" }).click();
    await expect.poll(ids).toEqual([first]);
  } finally {
    for (const id of await ids()) {
      await page.request.delete(`/api/console-sessions/${id}`);
    }
  }
});

// 2026-10-07 walkthrough, bug 2: the timeline's "now" line sits at 100%
// of the track while a task is still running, and its 2px reached past
// the lanes, giving them a sideways scrollbar that scrolled two pixels.
test("A timeline's now line gives its lanes no sideways scroll", async ({
  page,
}) => {
  for (const size of [DESKTOP, PHONE_PORTRAIT]) {
    await page.setViewportSize(size);
    await page.goto("/boards");
    await page
      .locator(".template-row", { hasText: "Timeline" })
      .first()
      .click();
    await expect(page.locator(".gantt-now")).toHaveCount(1);
    const lanes = await page
      .locator(".gantt-lanes")
      .evaluate((el) => ({ sw: el.scrollWidth, cw: el.clientWidth }));
    expect(
      lanes.sw,
      `lanes scroll ${lanes.sw}/${lanes.cw} at ${size.width}px`,
    ).toBeLessThanOrEqual(lanes.cw);
    await assertNoHorizontalOverflow(page);
  }
});

// 2026-10-12 walkthrough: Metrics -> Release notes lists grain's versions
// and opens a version's notes under the whole list, which on a phone and
// on a short desktop window starts below the fold -- a tap changed
// nothing on screen -- and the version's button never said it was open.
// The notes are the real ones this build embeds (pkg/releasenotes).
test("A grain version's release notes open on screen and say they are open", async ({
  page,
}) => {
  for (const size of [PHONE_PORTRAIT, DESKTOP]) {
    await page.setViewportSize(size);
    await page.goto("/");
    await expect(
      page.getByRole("heading", { name: "All tasks" }),
    ).toBeVisible();
    await page.goto("/system/metrics");
    await expect(page).toHaveURL(/\/system\/metrics$/);

    const versions = page.getByRole("list", { name: "grain versions" });
    const newest = versions.getByRole("button").first();
    await expect(newest).toHaveAttribute("aria-expanded", "false");
    const version = (await newest.innerText()).split(/\s/)[0];
    await newest.click();

    const notes = page.getByRole("region", {
      name: `${version} release notes`,
    });
    await expect(notes).toBeInViewport();
    await expect(newest).toHaveAttribute("aria-expanded", "true");
    await expect(newest).toHaveAttribute(
      "aria-controls",
      await notes.getAttribute("id"),
    );
    await assertNoHorizontalOverflow(page);

    // And it closes from the keyboard, saying so.
    await newest.focus();
    await page.keyboard.press("Enter");
    await expect(newest).toHaveAttribute("aria-expanded", "false");
    await expect(notes).toHaveCount(0);
  }
});

// Boards, Sessions, a session's chat, Memory, the team home page and a
// report board rendered no main landmark at all, while Tasks, Inbox and
// CI each had one -- so a screen reader's "jump to main content" found
// nothing on exactly the pages reached from the rail beside those.
// Reached through the rail itself (the drawer on a phone), each page must
// have exactly one main, and it must hold the page's own heading.
test("Every page the rail opens has one main landmark holding its heading", async ({
  page,
}) => {
  const pages = [
    { entry: /^Boards/, url: /\/boards$/, heading: "Boards" },
    { entry: /^Sessions/, url: /\/sessions$/, heading: "Sessions" },
    { entry: /^Inbox/, url: /\/inbox$/, heading: "Inbox" },
    { entry: /^CI/, url: /\/ci$/, heading: "CI" },
  ];
  for (const size of [PHONE_PORTRAIT, DESKTOP]) {
    await page.setViewportSize(size);
    await page.goto("/");
    await expect(
      page.getByRole("heading", { name: "All tasks" }),
    ).toBeVisible();
    const rail = page.getByRole("complementary").getByRole("navigation");
    for (const { entry, url, heading } of pages) {
      if (size.width < 700) {
        await page.getByRole("button", { name: "Open navigation" }).click();
      }
      await rail.getByRole("button", { name: entry }).first().click();
      await expect(page).toHaveURL(url);
      await expect(page.getByRole("main")).toHaveCount(1);
      await expect(
        page.getByRole("main").getByRole("heading", { name: heading }).first(),
      ).toBeVisible();
    }

    // Settings → Memory (a section, not a rail entry), by its old link.
    await page.goto("/memory");
    await expect(page).toHaveURL(/\/settings\/memory$/);
    await expect(page.getByRole("main")).toHaveCount(1);
    await expect(
      page.getByRole("main").getByRole("heading", { name: "Memory" }).first(),
    ).toBeVisible();

    // A standing session's chat, by its deep link, and each saved board.

    await page.goto("/session/director");
    await expect(page.getByRole("main")).toHaveCount(1);
    await expect(
      page.getByRole("main").getByRole("heading", { name: "Director" }),
    ).toBeVisible();

    // Every seeded board kind (pkg/demo): the board's page is its one
    // main, holding the board's own name, chips and description -- the
    // Kanban, timeline and tree panes under it gain no second main of
    // their own (grain/task-1253; axe's `region` rule failed on the
    // header of all three before).
    await page.goto("/boards");
    for (const { name, description } of [
      { name: "Board", description: "Every task, in columns of states" },
      { name: "Timeline", description: "Every task as a bar against a clock" },
      { name: "Report", description: "How this deployment is going, measured" },
      { name: "Tree", description: "Every task, under the task that filed it" },
    ]) {
      await page.getByRole("link", { name, exact: true }).click();
      await expect(page.getByRole("main")).toHaveCount(1);
      const main = page.getByRole("main");
      await expect(
        main.getByRole("heading", { name, exact: true }).first(),
      ).toBeVisible();
      await expect(main.getByText(description, { exact: true })).toBeVisible();
      await expect(
        main.getByRole("button", { name: "← Boards" }),
      ).toBeVisible();
      await assertNoHorizontalOverflow(page);
      await main.getByRole("button", { name: "← Boards" }).click();
      await expect(page).toHaveURL(/\/boards$/);
    }
  }
});

// A title is whatever somebody typed, and one unbroken run in it -- an
// identifier, a URL -- set the min-content width of the task pane's
// heading and of a session's "Edit <title> capabilities" button. On a
// 320px phone the task pane then scrolled sideways (1177px of content in
// a 319px pane) and the session's label ran off the screen; the page
// itself never overflowed, so a document-width check alone missed both.
test("A long unbroken title wraps in the task pane and a session's actions on a 320px phone", async ({
  page,
}) => {
  const word = "Unbroken" + "Identifier".repeat(12);
  const task = await seedOne(page, "/api/tasks", {
    title: `Walkthrough ${word} task`,
    description: "A title with one long word in it.",
    repo: "acme/widgets",
  });
  seededTasks.push(task.id);
  const sessionId = `longword-${test.info().project.name}`;
  await seedOne(page, "/api/console-sessions", {
    id: sessionId,
    title: word,
  });

  try {
    await page.setViewportSize({ width: 320, height: 568 });
    await page.goto(`/tasks/${task.id}`);
    const pane = page.getByRole("dialog", {
      name: new RegExp(`Task ${task.id}`),
    });
    await expect(pane.getByRole("heading", { level: 2 }).first()).toContainText(
      word,
    );
    const paneWidths = await page
      .locator(".overlay-pane")
      .first()
      .evaluate((el) => ({ sw: el.scrollWidth, cw: el.clientWidth }));
    expect(
      paneWidths.sw,
      `task pane scrolls sideways: ${paneWidths.sw} > ${paneWidths.cw}`,
    ).toBeLessThanOrEqual(paneWidths.cw + 1);
    await assertNoHorizontalOverflow(page);

    await page.goto(`/session/${sessionId}`);
    const toggle = page.getByRole("button", {
      name: `Edit ${word} capabilities`,
    });
    await expect(toggle).toBeVisible();
    const label = await toggle.evaluate((el) => ({
      sw: el.scrollWidth,
      cw: el.clientWidth,
      right: el.getBoundingClientRect().right,
    }));
    expect(
      label.sw,
      `button label overflows: ${label.sw} > ${label.cw}`,
    ).toBeLessThanOrEqual(label.cw + 1);
    expect(label.right).toBeLessThanOrEqual(320);
    await assertNoHorizontalOverflow(page);
  } finally {
    await page.request.delete(`/api/console-sessions/${sessionId}`);
  }
});

// Found in the 2026-10-15 walkthrough: the panes the rail and the top bar
// open -- System, Metrics, Settings, Team settings, a schedule -- were
// announced as a bare "dialog" (Overlay's Dialog points aria-labelledby at
// a title id nothing renders, and these callers passed no `label`), and
// the rail said which page was open only in paint, with no aria-current.
test("Every pane the shell opens is named, and the rail says which page is open", async ({
  page,
}) => {
  for (const size of [PHONE_PORTRAIT, DESKTOP]) {
    await page.setViewportSize(size);
    await page.goto("/");
    await expect(
      page.getByRole("heading", { name: "All tasks" }),
    ).toBeVisible();
    const openNav = async () => {
      if (size.width < 700) {
        await page.getByRole("button", { name: "Open navigation" }).click();
      }
    };
    const rail = page.getByRole("complementary");

    for (const { entry, url } of [
      { entry: /^Boards/, url: /\/boards$/ },
      { entry: /^Processes/, url: /\/processes$/ },
    ]) {
      await openNav();
      await rail.getByRole("button", { name: entry }).first().click();
      await expect(page).toHaveURL(url);
      await openNav();
      await expect(
        rail.locator('[aria-current="page"]'),
        "exactly one rail entry is the open page",
      ).toHaveCount(1);
      await expect(
        rail.getByRole("button", { name: entry }).first(),
      ).toHaveAttribute("aria-current", "page");
      if (size.width < 700) await page.keyboard.press("Escape");
    }

    // A schedule's pane, from the list.
    await page
      .getByRole("main")
      .getByRole("listitem")
      .getByRole("link")
      .first()
      .click();
    await expect(page.getByRole("dialog", { name: /schedule/i })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("dialog")).toHaveCount(0);

    // Settings and System are views rather than dialogs now
    // (docs/design/settings-panel.md §3.1): each has one main landmark,
    // named by its heading, and the button that opened it says so.
    for (const { open, name } of [
      {
        open: () =>
          size.width < 700
            ? page.getByTestId("phone-settings").click()
            : page.getByTestId("top-bar-settings").click(),
        name: "Settings",
      },
      {
        open: () => page.goto("/system"),
        name: "System",
      },
    ]) {
      await open();
      await expect(
        page.getByRole("heading", { name, exact: true }),
      ).toBeVisible();
      await expect(page.getByRole("main")).toHaveCount(1);
      await expect(page.getByRole("dialog")).toHaveCount(0);
    }
  }
});

// Found in the 2026-10-15 walkthrough: hard-loading a session's deep link
// while GET /api/console-sessions answered 503 (a daemon still starting
// its session manager -- the one boundary stubbed here) told the reader
// the session "may have been deleted; pick another below" for as long as
// the outage lasted, though nothing had been read to say so.
test("A session's deep link never says it was deleted while the session list cannot be read", async ({
  page,
}) => {
  await page.setViewportSize(PHONE_PORTRAIT);
  let failing = true;
  await page.route("**/api/console-sessions", async (route) => {
    if (failing && route.request().method() === "GET") {
      await route.fulfill({
        status: 503,
        contentType: "application/json",
        body: JSON.stringify({ error: "session manager starting" }),
      });
    } else {
      await route.fallback();
    }
  });

  const failed = page.waitForResponse(
    (r) => r.url().endsWith("/api/console-sessions") && r.status() === 503,
  );
  await page.goto("/session/director");
  await failed;
  // Past the shell's first load, with the list still unread.
  await expect(page.getByRole("heading", { name: "Sessions" })).toBeVisible();
  await page.waitForTimeout(1500);
  await expect(page.getByText(/There is no session/)).toHaveCount(0);

  failing = false;
  await expect(page.getByRole("heading", { name: "Director" })).toBeVisible({
    timeout: 20_000,
  });
  await expect(page).toHaveURL(/\/session\/director$/);
  await assertNoHorizontalOverflow(page);
});

// A new session opens as an empty chat rather than a form (grain task 12,
// NewSessionPage.jsx): the composer is on screen and focused, nothing has
// been created by opening it, and its one control besides Send -- the
// model picker -- sits in the composer's own row at both widths without
// pushing the page sideways. Real server, real browser, nothing stubbed.
test("A new session opens as an empty chat with its controls in one row, and creates nothing until something is said", async ({
  page,
}) => {
  const listed = async () => {
    const res = await page.request.get("/api/console-sessions");
    return (await res.json()).sessions.map((s) => s.id);
  };
  for (const size of [DESKTOP, PHONE_PORTRAIT]) {
    await page.setViewportSize(size);
    const before = await listed();
    await page.goto("/sessions");
    await page.getByRole("button", { name: "+ New session" }).first().click();
    await expect(page).toHaveURL(/\/session\/new$/);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(page.locator(".console-session-title")).toHaveText("New chat");

    const composer = page.getByRole("textbox", { name: "Message" });
    await expect(composer).toBeVisible();
    await expect(composer).toBeFocused();
    const send = page.getByRole("button", { name: "Send" });
    await expect(send).toBeDisabled();

    // The model picker shares the Send button's row: its box overlaps
    // Send's vertically, and ends inside the viewport.
    const sendBox = await send.boundingBox();
    const modelBox = await page.locator(".new-session-model").boundingBox();
    expect(modelBox).not.toBeNull();
    expect(modelBox.y).toBeLessThan(sendBox.y + sendBox.height);
    expect(modelBox.y + modelBox.height).toBeGreaterThan(sendBox.y);
    expect(modelBox.x + modelBox.width).toBeLessThanOrEqual(size.width);
    await assertNoHorizontalOverflow(page);

    // Opening the chat made no session.
    expect(await listed()).toEqual(before);
  }
});

// On a phone the rail lives in PhoneNav's drawer, which closes -- and
// unmounts the rail -- on the tap that asks for Customize. While the
// overlay was the rail's own state it went with the drawer and never
// appeared (2026-10-23 responsive walkthrough, bug 2). Pinning from a
// phone has to open the overlay, save, and show the pin in the drawer
// after a reload.
test("Customize sidebar opens from the phone drawer and its pin persists", async ({
  page,
}) => {
  await page.setViewportSize(PHONE_PORTRAIT);
  await page.goto("/sessions");
  const menu = page.getByRole("button", { name: "Open navigation" });
  await menu.click();
  const drawer = page.getByRole("dialog", { name: "Navigation", exact: true });
  const customize = drawer.getByRole("button", { name: "Customize sidebar" });
  await customize.click();
  // The drawer stays open under the overlay (it opens a dialog rather
  // than going anywhere), so closing Customize lands back in the rail.
  await expect(page.locator(".MuiDrawer-paper")).toBeAttached();
  const overlay = page.getByRole("dialog", {
    name: "Customize sidebar",
  });
  await expect(overlay).toBeVisible();
  await assertNoHorizontalOverflow(page);

  try {
    await overlay.getByLabel("Type").click();
    await page.getByRole("option", { name: "Session" }).click();
    await overlay.locator("#add-item-select").click();
    await page.getByRole("option", { name: "Director" }).click();
    await overlay.getByRole("button", { name: "Pin item" }).click();
    await overlay.getByRole("button", { name: "Save" }).click();
    await expect(overlay).toHaveCount(0);
    // Save hands focus back to Customize in the drawer it was opened
    // from, and the pin is there at once.
    await expect(drawer).toBeVisible();
    await expect(customize).toBeFocused();
    await expect(
      drawer.getByRole("button", { name: /^Director/ }),
    ).toBeVisible();

    await page.reload();
    await menu.click();
    await expect(
      page
        .getByRole("dialog", { name: "Navigation", exact: true })
        .getByRole("button", { name: /^Director/ }),
    ).toBeVisible();
  } finally {
    // Pins live in this browser's localStorage, which no other test
    // shares, but leave the page as a later step in this test found it.
    await page.evaluate(() => localStorage.removeItem("grain.pinned"));
  }
});

// The rail's Create options menu is anchored to its button inside the
// phone drawer. Closing the drawer on the tap that opened the menu took
// the anchor away and the menu never appeared, so New session and New
// board were out of reach on a phone (2026-10-23 responsive walkthrough,
// bug 3). A pick from the menu still closes the drawer.
test("The create menu opens from the phone drawer and a pick closes it", async ({
  page,
}) => {
  await page.setViewportSize(PHONE_PORTRAIT);
  await page.goto("/");
  await expect(page.locator(".task-row").first()).toBeVisible();
  await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByRole("button", { name: "Create options" }).click();
  const pick = page.getByRole("menuitem", { name: "New session" });
  await expect(pick).toBeVisible();
  await expect(page.getByRole("menuitem", { name: "New board" })).toBeVisible();
  await pick.click();
  // main opens a new session as an empty chat at /session/new rather
  // than a New session dialog (cdc9ffa35).
  await expect(page).toHaveURL(/\/session\/new$/);
  await expect(page.locator(".console-session-title")).toHaveText("New chat");
  await expect(
    page.getByRole("dialog", { name: "Navigation", exact: true }),
  ).toHaveCount(0);
  await assertNoHorizontalOverflow(page);
  await expect(page.getByRole("dialog")).toHaveCount(0);
});
