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

// Task detail and the Processes schedule and template inspectors show what
// the agent is told in a Prompt pane, rendered as markdown, and the facts
// about it in a separate Attributes / Configuration pane below. The two
// panes must not overlap, and no key or value may run out of its pane, at
// a desktop split and on a phone. The prompts are laid over the demo's
// answers with page.route, so the worker's shared store is not touched.

import { test, expect } from "./fixtures.js";

const PROMPT = [
  "## Goal",
  "",
  "Audit **every** dependency in `go.mod` for known CVEs.",
  "",
  "- Run `govulncheck ./...`",
  "- File one follow-up per finding",
  "",
  "```bash",
  "govulncheck ./... | tee vuln.txt",
  "```",
].join("\n");

async function withPrompt(page, pattern, pick) {
  await page.route(pattern, async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const res = await route.fetch();
    const text = await res.text();
    if (!text) return route.fulfill({ response: res });
    const body = JSON.parse(text);
    for (const item of pick(body)) item.description = PROMPT;
    await route.fulfill({ response: res, json: body });
  });
}

async function expectPanes(page, promptId, attrsId, width) {
  // The full task page draws its details twice (tab and aside) and shows
  // one of them for the width.
  const prompt = page.getByTestId(promptId).filter({ visible: true });
  const attrs = page.getByTestId(attrsId).filter({ visible: true });
  await expect(prompt).toBeVisible();
  await expect(prompt.locator(".g-pane-title")).toHaveText("Prompt");
  // Rendered markdown, not a raw text well.
  await expect(prompt.locator(".g-pane-prompt h2")).toHaveText("Goal");
  await expect(prompt.locator(".g-pane-prompt strong")).toHaveText("every");
  await expect(prompt.locator(".g-pane-prompt li")).toHaveCount(2);
  await expect(prompt.locator(".g-pane-prompt pre")).toContainText(
    "govulncheck",
  );
  await expect(prompt).not.toContainText("**every**");

  await attrs.scrollIntoViewIfNeeded();
  const p = await prompt.boundingBox();
  const a = await attrs.boundingBox();
  expect(a.y, "the attributes pane starts below the prompt").toBeGreaterThan(
    p.y + p.height,
  );
  for (const box of [p, a]) {
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width + 0.5);
  }
  // Every key and value stays inside the pane, and a value never sits on
  // its key.
  const cells = await attrs.locator("dt, dd").evaluateAll((els) =>
    els.map((e) => {
      const r = e.getBoundingClientRect();
      return { tag: e.tagName, l: r.left, r: r.right, t: r.top, b: r.bottom };
    }),
  );
  expect(cells.length).toBeGreaterThan(3);
  for (const c of cells) {
    expect(c.l).toBeGreaterThanOrEqual(a.x - 0.5);
    expect(c.r).toBeLessThanOrEqual(a.x + a.width + 0.5);
  }
  for (let i = 0; i + 1 < cells.length; i++) {
    const [k, v] = [cells[i], cells[i + 1]];
    if (k.tag !== "DT" || v.tag !== "DD") continue;
    const sideBySide = v.l >= k.r - 0.5;
    const stacked = v.t >= k.b - 0.5;
    expect(sideBySide || stacked, "a value overlaps its key").toBe(true);
  }
}

for (const [label, width] of [
  ["desktop", 1280],
  ["phone", 390],
]) {
  // The desktop project runs both widths; the phone project only the
  // phone one.
  const tag = width < 1080 ? ["@phone"] : [];
  test(
    `task detail splits prompt from attributes (${label})`,
    { tag },
    async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await withPrompt(page, /\/api\/tasks\/3(\?|$)/, (t) => [t.task ?? t]);
      await page.goto("task/3");
      if (width < 1080) await page.getByTestId("task-tab-details").click();
      await expectPanes(
        page,
        "task-prompt-pane",
        "task-attributes-pane",
        width,
      );
      const attrs = page
        .getByTestId("task-attributes-pane")
        .filter({ visible: true });
      await expect(attrs.locator(".g-pane-title")).toHaveText("Attributes");
      await expect(attrs.locator("dt").first()).toHaveText("Priority");
    },
  );

  test(
    `the tasks inspector pane splits prompt from attributes (${label})`,
    { tag },
    async ({ page }) => {
      // A phone shows the list over the pane; the full page covers it.
      test.skip(width < 1080, "desktop split only");
      await page.setViewportSize({ width, height: 900 });
      await withPrompt(page, /\/api\/tasks\/3(\?|$)/, (t) => [t.task ?? t]);
      await page.goto("tasks?task=3");
      // The inspector pane puts details behind its own tab.
      await page.getByTestId("task-tab-details").click();
      await expectPanes(
        page,
        "task-prompt-pane",
        "task-attributes-pane",
        width,
      );
    },
  );

  test(
    `schedule inspector renders its prompt as markdown (${label})`,
    { tag },
    async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await withPrompt(page, /\/api\/schedules(\?|$)/, (l) => l);
      await page.goto("processes/schedules");
      const row = page.locator('[data-testid^="sched-row-"]').first();
      await row.click();
      await expectPanes(page, "sched-prompt-pane", "sched-config-pane", width);
      await expect(
        page.getByTestId("sched-config-pane").locator(".g-pane-title"),
      ).toHaveText("Configuration");
    },
  );

  test(
    `template inspector renders its prompt as markdown (${label})`,
    { tag },
    async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await withPrompt(page, /\/api\/templates(\?|$)/, (l) => l);
      await page.goto("processes/templates");
      const row = page.locator('[data-testid^="tmpl-row-"]').first();
      await row.click();
      await expectPanes(page, "tmpl-prompt-pane", "tmpl-config-pane", width);
    },
  );
}

// A pane under 260px stacks each key over its value. Opening Processes
// first and Tasks after, in one session, loads detail-panes.css before the
// task detail's own stylesheet -- the order in which a rule there would win
// over .g-attrs if they ever shared a selector again.
test("a narrow attributes pane stacks keys over values after Processes", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto("processes/schedules");
  await page.locator('[data-testid^="sched-row-"]').first().click();
  await expect(page.getByTestId("sched-config-pane")).toBeVisible();

  const nav = page.getByRole("navigation", { name: "Primary destinations" });
  await nav.getByRole("link", { name: "Tasks", exact: true }).click();
  await page.locator('[data-testid^="open-"]').first().click();
  await page.getByTestId("task-tab-details").click();
  const attrs = page
    .getByTestId("task-attributes-pane")
    .filter({ visible: true });
  await expect(attrs).toBeVisible();

  // Narrow the pane itself to a 240px content box: what a 320px inspector
  // split leaves it once the tab body and pane padding are taken off.
  await attrs.evaluate((el) => {
    el.style.boxSizing = "content-box";
    el.style.width = "240px";
  });
  const dl = attrs.locator("dl.g-attrs");
  await expect
    .poll(() =>
      dl.evaluate(
        (el) => getComputedStyle(el).gridTemplateColumns.split(" ").length,
      ),
    )
    .toBe(1);
  const cells = await dl.locator("dt, dd").evaluateAll((els) =>
    els.map((e) => {
      const r = e.getBoundingClientRect();
      return { tag: e.tagName, t: r.top, b: r.bottom, l: r.left };
    }),
  );
  expect(cells.length).toBeGreaterThan(3);
  for (let i = 0; i + 1 < cells.length; i++) {
    const [k, v] = [cells[i], cells[i + 1]];
    if (k.tag !== "DT" || v.tag !== "DD") continue;
    expect(v.t, "the value sits under its key").toBeGreaterThanOrEqual(
      k.b - 0.5,
    );
    expect(Math.abs(v.l - k.l), "key and value share a left edge").toBeLessThan(
      1,
    );
  }
});
