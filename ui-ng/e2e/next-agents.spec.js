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
import fs from "node:fs";
import path from "node:path";
import { test, expect } from "./fixtures.js";

// /next/agents over a real `grain demo`, which seeds one running task:
// the card and list row the inspector shows look selected (computed
// style, not a class name), they say they are clickable, every inspector
// tab switches its panel, the CPU cell says why there is no reading
// rather than printing a dash, and axe finds nothing in light and dark at
// a desktop and a phone width. jsdom applies no CSS, so these are the
// checks the unit specs cannot make.

// A named actor, so no screenshot carries the host account's user name
// (`grain demo` falls back to it for the task owner without -as).
test.use({ demoArgs: ["-as", "demo-operator"] });

async function apiJson(baseURL, code, p) {
  const res = await fetch(`${baseURL}${p}`, {
    headers: { Authorization: `Bearer ${code}` },
  });
  if (!res.ok) throw new Error(`GET ${p}: ${res.status} ${await res.text()}`);
  return res.json();
}

// shotsDir is where the screenshots go: GRAIN_E2E_SHOTS when a reviewer
// wants them somewhere stable, else this test's own output directory.
function shotsDir() {
  const dir = process.env.GRAIN_E2E_SHOTS || test.info().outputPath("shots");
  fs.mkdirSync(dir, { recursive: true });
  return dir;
}

// rgbOf resolves a CSS colour expression (a token) to the computed
// rgb() string the browser compares against.
async function rgbOf(page, expr) {
  return page.evaluate((e) => {
    const probe = document.createElement("span");
    probe.style.color = e;
    document.body.appendChild(probe);
    const c = getComputedStyle(probe).color;
    probe.remove();
    return c;
  }, expr);
}

async function styleOf(locator) {
  return locator.evaluate((el) => {
    const cs = getComputedStyle(el);
    return {
      cursor: cs.cursor,
      background: cs.backgroundColor,
      border: cs.borderTopColor,
      shadow: cs.boxShadow,
      outlineStyle: cs.outlineStyle,
      outlineColor: cs.outlineColor,
    };
  });
}

async function setTheme(page, mode) {
  await page.evaluate((m) => {
    localStorage.setItem("grain.themeMode", m);
    document.documentElement.setAttribute("data-theme", m);
  }, mode);
  await expect(page.locator("html")).toHaveAttribute("data-theme", mode);
}

async function axeClean(page) {
  const r = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .analyze();
  expect(r.violations).toEqual([]);
}

test("Agents: selected card and row look selected, are clickable, and every inspector tab switches", async ({
  page,
  demoServer,
  accessCode,
}) => {
  test.setTimeout(120_000);
  const tasks = await apiJson(demoServer, accessCode, "/api/tasks");
  const list = Array.isArray(tasks) ? tasks : tasks.tasks;
  const runningTask = list.find(
    (t) => t.state === "running" && !t.consoleSession,
  );
  const other = list.find((t) => t.state !== "running");
  expect(runningTask, "grain demo seeds a running task").toBeTruthy();
  expect(other, "grain demo seeds a task that is not running").toBeTruthy();
  const id = runningTask.id;
  const dir = shotsDir();

  await page.addInitScript(() =>
    localStorage.setItem("grain.themeMode", "light"),
  );
  await page.setViewportSize({ width: 1440, height: 900 });

  // Another task pinned in the pane: the running card is not selected.
  await page.goto(`/next/agents?task=${other.id}`);
  const card = page.getByTestId(`agent-${id}`);
  await expect(card).toBeVisible();
  await expect(page.getByTestId("pane-moved")).toBeVisible();
  await expect(card).not.toHaveClass(/is-selected/);
  await expect(card).not.toHaveAttribute("aria-current", /.*/);
  const before = await styleOf(card);
  expect(before.cursor).toBe("pointer");
  await page.screenshot({
    path: path.join(dir, "agents-1440-light-unselected.png"),
  });

  // Click the card body: the inspector follows, and the card shows it.
  await card.locator(".agent-activity").click();
  await expect(page).toHaveURL(new RegExp(`[?&]task=${id}(&|$)`));
  await expect(card).toHaveClass(/is-selected/);
  await expect(card).toHaveAttribute("aria-current", "true");
  await expect(page.getByTestId("agents-pane")).toContainText(
    `Inspecting #${id}`,
  );
  await page.mouse.move(0, 0);
  // Let the 220ms background/border transition finish before reading.
  await page.waitForTimeout(400);
  const primary = await rgbOf(page, "var(--md-sys-color-primary)");
  const container = await rgbOf(page, "var(--md-sys-color-surface-container)");
  const sel = await styleOf(card);
  expect(sel.background).toBe(container);
  expect(sel.background).not.toBe(before.background);
  expect(sel.border).toBe(primary);
  expect(sel.shadow).toContain("inset");
  expect(sel.shadow).toContain(primary);
  expect(sel.cursor).toBe("pointer");

  // Keyboard: a focus ring on the card itself.
  await card.focus();
  await page.keyboard.press("Shift+Tab");
  await page.keyboard.press("Tab");
  await expect(card).toBeFocused();
  const focused = await styleOf(card);
  expect(focused.outlineStyle).toBe("solid");
  expect(focused.outlineColor).toBe(primary);

  // CPU: the demo's run has no sandbox, so the card says so in words.
  const cpu = card.getByTestId("agent-cpu");
  await expect(cpu.locator(".cpu-none")).toHaveText("not sampled");
  await expect(cpu).not.toContainText("—");
  await expect(cpu.locator(".sr-only")).toContainText("Not sampled:");
  await page.screenshot({
    path: path.join(dir, "agents-1440-light-selected.png"),
  });

  // Every inspector tab switches its panel.
  const pane = page.getByTestId("agents-pane");
  const tabs = pane.locator(".task-tabs .tab-link");
  const ids = await tabs.evaluateAll((els) =>
    els
      .filter((e) => getComputedStyle(e).display !== "none")
      .map((e) => e.dataset.testid.replace("task-tab-", "")),
  );
  expect(ids).toEqual(["conversation", "runs", "changes", "tree", "details"]);
  const panel = {
    conversation: () =>
      expect(pane.locator(".task-tab-body h2").first()).toHaveText(
        "Conversation",
      ),
    runs: () =>
      expect(pane.locator(".task-tab-body h2").first()).toHaveText("Runs"),
    changes: () =>
      expect(pane.locator(".task-tab-body h2").first()).toHaveText(
        "Changes and CI",
      ),
    tree: () =>
      expect(pane.locator(".task-tab-body h2").first()).toHaveText("Work tree"),
    details: () => expect(pane.getByTestId("task-details")).toBeVisible(),
  };
  for (const tab of [
    ...ids.filter((t) => t !== "conversation"),
    "conversation",
  ]) {
    await pane.getByTestId(`task-tab-${tab}`).click();
    await expect(page).toHaveURL(new RegExp(`[?&]pane=${tab}(&|$)`));
    await expect(pane.getByTestId(`task-tab-${tab}`)).toHaveAttribute(
      "aria-current",
      "true",
    );
    await expect(pane.locator(".task-tabs [aria-current]")).toHaveCount(1);
    await panel[tab]();
    if (tab === "runs") {
      await page.screenshot({
        path: path.join(dir, "agents-1440-light-tab-runs.png"),
      });
    }
  }

  await axeClean(page);

  // List view: the row shows the same selection and is clickable.
  await page.getByTestId("layout-list").click();
  const row = page.getByTestId(`agent-row-${id}`);
  await expect(row).toHaveClass(/is-selected/);
  await expect(row).toHaveAttribute("aria-current", "true");
  const rowCell = await styleOf(row.locator("td").nth(1));
  expect(rowCell.background).toBe(container);
  expect((await styleOf(row)).cursor).toBe("pointer");
  expect((await styleOf(row.locator("td").first())).shadow).toContain(primary);
  await expect(row.locator("td").nth(6)).toHaveText("not sampled");
  await page.screenshot({ path: path.join(dir, "agents-1440-light-list.png") });
  await page.getByTestId("layout-cards").click();

  // Dark: the selection reads against the darker cards too.
  await setTheme(page, "dark");
  await page.waitForTimeout(400);
  const darkPrimary = await rgbOf(page, "var(--md-sys-color-primary)");
  const darkHigh = await rgbOf(
    page,
    "var(--md-sys-color-surface-container-high)",
  );
  const darkSel = await styleOf(card);
  expect(darkSel.background).toBe(darkHigh);
  expect(darkSel.border).toBe(darkPrimary);
  expect(darkSel.shadow).toContain(darkPrimary);
  await page.screenshot({
    path: path.join(dir, "agents-1440-dark-selected.png"),
  });
  await axeClean(page);

  // Phone: one column, no pane; cards still say they are clickable and clear the bottom bar + FAB.
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`/next/agents`);
  await expect(card).toBeVisible();
  expect((await styleOf(card)).cursor).toBe("pointer");
  const mobilePadBottom = await page
    .locator(".agents-body")
    .evaluate((el) => parseFloat(getComputedStyle(el).paddingBottom));
  expect(mobilePadBottom).toBeGreaterThanOrEqual(100);
  await expect(page.getByTestId("agents-pane")).toHaveCount(0);
  await page.screenshot({ path: path.join(dir, "agents-390-dark.png") });
  await axeClean(page);
  await setTheme(page, "light");
  await page.screenshot({ path: path.join(dir, "agents-390-light.png") });
  await axeClean(page);
});
