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

// A Director turn's live steps (shared/live-steps): one described line per
// tool call, the newest one fully above the composer, decoded on expand.
// The demo's Director is idle, so the browser is told it is mid-turn: its
// task reads as running with an open attempt, and that attempt's transcript
// is what pkg/agent/claude/transcript.go writes, Go's & escapes and all.

import { test, expect } from "./fixtures.js";

const TRANSCRIPT = [
  "Looking at what the hosts report before changing anything.",
  "",
  '> run_command({"command":"cd /tmp \\u0026\\u0026 grain api GET /api/hosts","description":"listing the deployment\'s hosts and their states"})',
  "exit=0",
  "stdout:",
  '{"name":"host-a","state":"ready"}',
  "",
  "stderr:",
  "",
  "",
  "Searching the source for where draining is decided.",
  "",
  '> run_command({"command":"grain grep -i \\"hostd\\" src://pkg/orchestrator --max 10000 \\u0026\\u0026 grain grep -i \\"drain\\" src://pkg/runhost --max 10000 | head -50"})',
  ...Array.from(
    { length: 30 },
    (_, i) => `pkg/orchestrator/hosts.go:${100 + i}: drain check ${i}`,
  ),
  "",
  '> grain_get_task({"id":"5606","view":"summary"})',
].join("\n");

// fakeLiveTurn makes the Director's task read as running in this page.
async function fakeLiveTurn(page) {
  let dirTid = "";
  await page.route("**/api/console-sessions", async (route) => {
    const r = await route.fetch();
    const j = await r.json();
    dirTid = (j.sessions || []).find((s) => s.id === "director")?.taskId || "";
    await route.fulfill({ response: r, json: j });
  });
  const fresh = (route) =>
    route.fetch({
      headers: { ...route.request().headers(), "if-none-match": "" },
    });
  await page.route(/\/api\/tasks(\?.*)?$/, async (route) => {
    const r = await fresh(route);
    let j = await r.json().catch(() => null);
    if (Array.isArray(j)) {
      j = j.map((t) =>
        t.id === dirTid
          ? { ...t, state: "running", activity: "Running grain_get_task" }
          : t,
      );
    }
    await route.fulfill({ json: j, headers: { etag: `"${Math.random()}"` } });
  });
  await page.route(/\/api\/tasks\/[^/]+\/attempts\/\d+\/transcript/, (route) =>
    route.fulfill({ json: { transcript: TRANSCRIPT } }),
  );
  await page.route(/\/api\/tasks\/[^/?]+(\?.*)?$/, async (route) => {
    const id = new URL(route.request().url()).pathname.split("/").pop();
    if (!dirTid || id !== dirTid) return route.fallback();
    const r = await fresh(route);
    const j = await r.json();
    j.state = "running";
    j.attempts = [
      ...(j.attempts || []),
      { number: 99, startedAt: new Date().toISOString() },
    ];
    await route.fulfill({ json: j, headers: { etag: `"${Math.random()}"` } });
  });
}

// The newest step lies wholly inside its scrolling thread and above the
// composer's top edge, and the thread does not scroll sideways.
async function expectNewestStepAboveComposer(scope) {
  const steps = scope.getByTestId("live-step");
  await expect(steps).toHaveCount(3);
  await expect
    .poll(async () => {
      const m = await steps.last().evaluate((el) => {
        const r = el.getBoundingClientRect();
        const thread = el.closest(".chat-log").getBoundingClientRect();
        const composer = el
          .closest(".chat-pane, .g-conversation")
          .querySelector(".chat-composer")
          .getBoundingClientRect();
        return { r, thread, composer };
      });
      return (
        m.r.height >= 24 &&
        m.r.top >= m.thread.top &&
        m.r.bottom <= m.thread.bottom &&
        m.r.bottom <= m.composer.top
      );
    })
    .toBe(true);
  const sideways = await steps.last().evaluate((el) => {
    const t = el.closest(".chat-log");
    return t.scrollWidth - t.clientWidth;
  });
  expect(sideways).toBeLessThanOrEqual(1);
}

async function expectDescribedAndDecoded(scope, narrow) {
  const steps = scope.getByTestId("live-step");
  await expect(steps.first().getByTestId("live-step-label")).toHaveText(
    "listing the deployment's hosts and their states",
  );
  // A call with no description is its tool and its decoded command,
  // truncated with an ellipsis inside its one-line row.
  const second = steps.nth(1).getByTestId("live-step-label");
  await expect(second).toContainText('run_command: grain grep -i "hostd"');
  const clipped = await second.evaluate((el) => ({
    over: el.scrollWidth > el.clientWidth,
    ellipsis: getComputedStyle(el).textOverflow,
    lines: Math.round(el.getBoundingClientRect().height / 16),
  }));
  expect(clipped).toEqual({ over: narrow, ellipsis: "ellipsis", lines: 1 });

  await steps.first().getByTestId("live-step-toggle").click();
  await expect(steps.first().getByTestId("live-step-call")).toHaveText(
    "cd /tmp && grain api GET /api/hosts",
  );
  await steps.last().getByTestId("live-step-toggle").click();
  const json = await steps.last().getByTestId("live-step-call").textContent();
  expect(JSON.parse(json ?? "")).toEqual({ id: "5606", view: "summary" });

  await steps.nth(1).getByTestId("live-step-toggle").click();
  const out = steps.nth(1).getByTestId("live-step-output");
  await expect(out).not.toContainText("drain check 29");
  await steps.nth(1).getByTestId("live-step-show-all").click();
  await expect(out).toContainText("drain check 29");

  const text = (await scope.textContent()) ?? "";
  expect(text).not.toContain("\\u0026");
  expect(text).not.toMatch(/\\u[0-9a-fA-F]{4}/);
}

for (const vp of [
  { width: 458, height: 1013 },
  { width: 1440, height: 900 },
]) {
  test(`Sessions: a live Director turn's newest step sits above the composer at ${vp.width}px`, async ({
    page,
  }) => {
    test.setTimeout(90_000);
    await page.setViewportSize(vp);
    await fakeLiveTurn(page);
    await page.goto("/next/sessions/director");
    const live = page.getByTestId("session-live-transcript");
    await expect(live).toBeVisible({ timeout: 30_000 });
    await expectNewestStepAboveComposer(live);
    const activity = page.getByTestId("chat-activity");
    await expect(activity).toHaveCSS("text-overflow", "ellipsis");
    await expectDescribedAndDecoded(live, vp.width < 600);
  });

  test(`Director drawer: the live turn is not squeezed behind the composer at ${vp.width}px`, async ({
    page,
  }) => {
    test.setTimeout(90_000);
    await page.setViewportSize(vp);
    await fakeLiveTurn(page);
    await page.goto("/next/tasks");
    await page.getByTestId("director-trigger").click();
    const drawer = page.getByTestId("director-drawer");
    const live = drawer.getByTestId("inbox-live-transcript");
    await expect(live).toBeVisible({ timeout: 30_000 });
    // The row is as tall as what it holds: a full thread used to shrink the
    // old <pre> to the top sliver of one line.
    const fit = await live.evaluate(
      (el) => el.getBoundingClientRect().height >= el.scrollHeight - 1,
    );
    expect(fit).toBe(true);
    await expectNewestStepAboveComposer(drawer);
    await expectDescribedAndDecoded(drawer, true);
  });
}
