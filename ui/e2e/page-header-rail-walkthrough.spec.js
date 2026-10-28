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

import { test, expect } from "./fixtures.js";

// The one header rail (src/components/PageHeader.jsx, grain/task-4915):
// the global top bar is gone, and every page in the main pane is headed
// by a single row holding its title, its own actions, and at the far end
// the alerts bell, System and the Settings gear.

async function boardPath(page, name) {
  const res = await page.request.get("/api/boards");
  expect(res.ok(), `GET /api/boards: ${res.status()}`).toBe(true);
  const board = (await res.json()).find((b) => b.name === name);
  expect(board, `no board called "${name}" in the demo store`).toBeTruthy();
  return `/boards/${board.id}`;
}

// Each page, by address, and what its rail's title reads.
const PAGES = [
  ["Tasks", "/", /^tasks$/],
  ["Inbox", "/inbox", /^inbox$/],
  ["a repo", "/repos/acme/widgets", /^repositories\/acme\/widgets$/],
  [
    "Releases",
    "/repos/acme/widgets/releases",
    /^repositories\/acme\/widgets\/releases$/,
  ],
  ["a board", null, /^boards\/Board$/],
  ["a session", "/session/director", /^sessions\//],
  ["Processes", "/processes", /^processes$/],
  ["Settings", "/settings", /^settings\/general$/],
  ["System", "/system", /^system\/health$/],
  ["Logs", "/system/logs", /^system\/logs$/],
];

// The pages with a count in or under their rail, and the pages with an
// action of their own in it.
const COUNTED_PAGES = [
  ["Tasks", "/"],
  ["Inbox", "/inbox"],
  ["Repos", "/repos"],
  ["Boards", "/boards"],
  ["a board", null],
  ["Sessions", "/sessions"],
  ["Processes", "/processes"],
  ["Processes' workflows", "/processes?kind=workflow"],
];
const ACTION_PAGES = [
  ["Repos", "/repos"],
  ["Boards", "/boards"],
  ["a board", null],
  ["Sessions", "/sessions"],
  ["Processes", "/processes"],
  ["Processes' templates", "/processes?kind=template"],
  ["Logs", "/system/logs"],
];

async function expectOneRail(page, title) {
  const rail = page.getByTestId("page-header");
  await expect(rail).toHaveCount(1);
  await expect(page.getByTestId("top-bar")).toHaveCount(0);
  await expect(
    rail.locator(".header-icon svg[data-glyph]").first(),
  ).toBeVisible();
  await expect(rail.getByTestId("page-header-title")).toHaveText(title);
  await expect(rail.getByTestId("top-bar-page")).toHaveAttribute(
    "aria-current",
    "page",
  );
  const globals = rail.getByTestId("page-header-globals");
  await expect(globals.getByTestId("alerts-button")).toBeVisible();
  await expect(globals.getByTestId("top-bar-system")).toBeVisible();
  await expect(globals.getByTestId("top-bar-settings")).toBeVisible();
  // One row: the rail is exactly as tall wherever it is.
  const box = await rail.boundingBox();
  expect(Math.round(box.height)).toBe(77);
  // At the top of the pane, level with the nav rail beside it.
  const aside = await page.locator("aside").boundingBox();
  expect(Math.abs(box.y - aside.y)).toBeLessThanOrEqual(1);
  return rail;
}

test.describe("the page header rail", () => {
  for (const [name, path, title] of PAGES) {
    test(`heads ${name} with one rail and the global buttons`, async ({
      page,
    }) => {
      await page.setViewportSize({ width: 1440, height: 900 });
      await page.goto(path ?? (await boardPath(page, "Board")));
      await expectOneRail(page, title);
    });
  }

  test("puts the task list's own header row in the rail", async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();
    const rail = await expectOneRail(page, /^tasks$/);
    await expect(rail.locator(".content-header .count")).toBeVisible();
    await expect(rail.getByPlaceholder(/Search/).first()).toBeVisible();
  });

  test("marks Settings and System pressed while their page shows", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/");
    await page.getByTestId("top-bar-settings").click();
    await expect(page).toHaveURL(/\/settings/);
    await expect(page.getByTestId("top-bar-settings")).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await page.getByTestId("top-bar-system").click();
    await expect(page).toHaveURL(/\/system/);
    await expect(page.getByTestId("top-bar-system")).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await expect(page.getByTestId("top-bar-settings")).toHaveAttribute(
      "aria-pressed",
      "false",
    );
  });

  // A narrow window: the rail stays one row, and what does not fit is
  // behind "more" rather than on a second row under it.
  test("keeps one row in a narrow window and puts the rest behind more", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 760, height: 800 });
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();
    const rail = await expectOneRail(page, /^tasks$/);
    const more = rail.getByTestId("page-header-more");
    await expect(more).toBeVisible();
    await expect(rail.getByTestId("top-bar-settings")).toBeInViewport();
    const width = await page.evaluate(
      () => document.documentElement.scrollWidth,
    );
    expect(width).toBeLessThanOrEqual(760);

    await more.click();
    await expect(more).toHaveAttribute("aria-expanded", "true");
    const slot = rail.getByTestId("page-header-actions");
    await expect(slot).toHaveClass(/page-header-slot-expanded/);
    // Every control the page had is reachable in the opened menu, and
    // still works: the search narrows the list.
    const search = slot.getByPlaceholder(/Search/).first();
    await expect(search).toBeVisible();
    // The rail itself stayed one row.
    expect(Math.round((await rail.boundingBox()).height)).toBe(77);
    await more.click();
    await expect(more).toHaveAttribute("aria-expanded", "false");
  });

  // A phone has no rail (PhoneNav's bar is the rail there); a window
  // crossing the breakpoint and back gets it back without remounting the
  // page under it -- the session's chat keeps its messages and nothing
  // throws (fixtures.js fails the test on an uncaught exception).
  test("drops the rail on a phone and brings it back without losing the page", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/session/director");
    await expectOneRail(page, /^sessions\//);
    await expect(page.locator(".cs-message").first()).toBeVisible();
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(page.getByTestId("page-header")).toHaveCount(0);
    await expect(page.getByTestId("phone-settings")).toBeVisible();
    await expect(page.locator(".console-session-header")).toBeVisible();
    await page.setViewportSize({ width: 1440, height: 900 });
    await expectOneRail(page, /^sessions\//);
    await expect(page.locator(".cs-message").first()).toBeVisible();
  });

  // Every count beside a label is level with it (grain/task-4980): the
  // rail's first line used to be only as tall as its tallest item, so a
  // count sat a different distance off the title's centre line on each
  // page -- 8px on Inbox, 22px on Repos. One badge everywhere, too: the
  // same size and tabular numerals on every page.
  for (const [name, path] of COUNTED_PAGES) {
    test(`centres every count on its label on ${name}`, async ({ page }) => {
      await page.setViewportSize({ width: 1440, height: 900 });
      await page.goto(path ?? (await boardPath(page, "Board")));
      await expect(page.getByTestId("page-header")).toHaveCount(1);
      await expect(page.locator(".count:visible").first()).toBeVisible();
      const counts = await page.evaluate(measureCounts);
      expect(counts.length).toBeGreaterThan(0);
      for (const c of counts) {
        expect(
          Math.abs(c.dy),
          `count "${c.text}" (${c.where}) is ${c.dy}px off its label "${c.label}"`,
        ).toBeLessThanOrEqual(1);
        expect(c.fontSize, `count "${c.text}" (${c.where})`).toBe("12px");
        expect(c.numerals, `count "${c.text}" (${c.where})`).toContain(
          "tabular-nums",
        );
      }
    });
  }

  // A page's own actions sit at the right-hand end of the rail's slot,
  // after its count, search and filters and just before the global
  // buttons -- not wherever the page's header happened to portal them,
  // which left "Add repo", "+ New" and "New board" floating mid-rail.
  for (const [name, path] of ACTION_PAGES) {
    test(`puts ${name}'s own actions at the end of the rail`, async ({
      page,
    }) => {
      await page.setViewportSize({ width: 1440, height: 900 });
      await page.goto(path ?? (await boardPath(page, "Board")));
      const rail = page.getByTestId("page-header");
      const slot = rail.getByTestId("page-header-actions");
      const actions = slot.getByTestId("page-header-page-actions").first();
      await expect(actions).toBeVisible();
      const layout = await page.evaluate(measureActions);
      expect(layout.actionsLeft).toBeGreaterThan(layout.titleRight);
      expect(layout.lastRight).toBeLessThan(layout.globalsLeft);
      // Flush with the end of the slot: nothing after them on the line
      // but a later list's header, and nothing ahead of them in the
      // page's header row to their right.
      expect(Math.abs(layout.slotRight - layout.lastRight)).toBeLessThanOrEqual(
        2,
      );
      expect(
        layout.beforeButRight,
        "items ahead of the page's actions drawn to the right of them",
      ).toEqual([]);
      // Level with the title, on the rail's one line.
      expect(Math.abs(layout.actionsMid - layout.titleMid)).toBeLessThanOrEqual(
        1,
      );
    });
  }
});

// measureCounts runs in the page: every visible .count, the label it
// counts -- the nearest drawn element before it, or for the first count
// in the rail the rail's own title -- and how far apart their centre
// lines are.
function measureCounts() {
  const rail = document.querySelector("[data-testid=page-header]");
  const crumb = rail?.querySelector("[data-testid=top-bar-page]");
  const mid = (r) => r.top + r.height / 2;
  const drawn = (el) => {
    const r = el.getBoundingClientRect();
    return (
      r.width > 0 &&
      r.height > 0 &&
      getComputedStyle(el).visibility !== "hidden"
    );
  };
  const out = [];
  for (const el of document.querySelectorAll(".count")) {
    const r = el.getBoundingClientRect();
    if (!drawn(el) || r.bottom > window.innerHeight || r.top < 0) continue;
    // Clipped onto a wrapped line of the rail's slot: not on screen.
    if (rail?.contains(el) && r.top > rail.getBoundingClientRect().bottom) {
      continue;
    }
    let label = el.previousElementSibling;
    while (
      label &&
      (!drawn(label) || label.classList.contains("visually-hidden"))
    ) {
      label = label.previousElementSibling;
    }
    if (!label && rail?.contains(el)) label = crumb;
    if (!label) continue;
    const cs = getComputedStyle(el);
    out.push({
      text: el.textContent,
      where: rail?.contains(el) ? "rail" : el.parentElement.className,
      label: label.textContent.trim().slice(0, 40),
      dy: +(mid(r) - mid(label.getBoundingClientRect())).toFixed(1),
      fontSize: cs.fontSize,
      numerals: cs.fontVariantNumeric,
    });
  }
  return out;
}

// measureActions runs in the page: where the rail's title, slot, global
// buttons and the page's own actions are, and which of the slot's items
// that come before the first actions group in the page's order are
// drawn to its right on the rail's first line.
function measureActions() {
  const rail = document.querySelector("[data-testid=page-header]");
  const slot = rail.querySelector("[data-testid=page-header-actions]");
  const title = rail.querySelector("[data-testid=page-header-title]");
  const crumb = rail.querySelector("[data-testid=top-bar-page]");
  const globals = rail.querySelector("[data-testid=page-header-globals]");
  const groups = [
    ...slot.querySelectorAll("[data-testid=page-header-page-actions]"),
  ];
  const first = groups[0];
  const fr = first.getBoundingClientRect();
  const slotBox = slot.getBoundingClientRect();
  const items = [];
  const walk = (node) => {
    for (const c of node.children) {
      if (getComputedStyle(c).display === "contents") walk(c);
      else items.push(c);
    }
  };
  walk(slot);
  const onLine = items.filter((c) => {
    const r = c.getBoundingClientRect();
    return r.width > 0 && r.top < slotBox.bottom;
  });
  const beforeButRight = onLine
    .filter(
      (c) =>
        first.compareDocumentPosition(c) & Node.DOCUMENT_POSITION_PRECEDING &&
        c.getBoundingClientRect().right > fr.left + 0.5,
    )
    .map((c) => c.textContent.trim().slice(0, 30) || c.className);
  const cr = crumb.getBoundingClientRect();
  return {
    titleRight: title.getBoundingClientRect().right,
    titleMid: cr.top + cr.height / 2,
    globalsLeft: globals.getBoundingClientRect().left,
    slotRight: slotBox.right,
    actionsLeft: fr.left,
    actionsMid: fr.top + fr.height / 2,
    lastRight: Math.max(...onLine.map((c) => c.getBoundingClientRect().right)),
    beforeButRight,
  };
}
