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

// A durable task's page (design/design/durable-tasks.design.md#DT-15):
// the current brief stands where the description does, the Context panel
// with its stale badge and Compact sits inside the side column, and the
// comments the compaction summarised are collapsed in the timeline -- at
// desktop and phone widths. Nothing here is posted: the demo is
// worker-scoped, and a compaction request cannot be taken back.
const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

async function expectInside(inner, outer, what) {
  const a = await inner.boundingBox();
  const b = await outer.boundingBox();
  expect(a, `${what} has no box`).not.toBeNull();
  expect(a.x, `${what} starts left of its column`).toBeGreaterThanOrEqual(
    b.x - 1,
  );
  expect(a.x + a.width, `${what} runs past its column`).toBeLessThanOrEqual(
    b.x + b.width + 1,
  );
}

async function durableTaskID(page) {
  const res = await page.request.get("/api/tasks");
  expect(res.ok()).toBe(true);
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  const t = tasks.find(
    (x) => x.title === "Maintain the acme/widgets changelog",
  );
  expect(t, "the demo's durable task").toBeTruthy();
  return t.id;
}

test("A durable task's brief, context and summarised comments are laid out in their columns", async ({
  page,
}) => {
  test.setTimeout(90_000);
  const id = await durableTaskID(page);
  for (const viewport of [DESKTOP, PHONE]) {
    await page.setViewportSize(viewport);
    await page.goto(`/tasks/${id}`);

    const brief = page.getByTestId("brief-section");
    await expect(brief.getByTestId("brief-meta")).toContainText(
      "entries should link their pull requests",
    );
    await expect(brief.locator(".description.brief").first()).toContainText(
      "link every entry to its pull request",
    );
    // The original description is kept, folded away.
    const original = brief.getByTestId("brief-original");
    await expect(original).not.toHaveAttribute("open", "");
    await original.locator("summary").click();
    await expect(original).toContainText("Stage 1");
    await expect(brief.getByTestId("brief-history")).toContainText("2 edits");

    const section = page.getByTestId("context-section");
    const column = section.locator("xpath=..");
    await expectInside(section, column, `context at ${viewport.width}px`);
    await expect(section.getByTestId("context-handover")).toContainText("v1");
    const stale = section.getByTestId("handover-stale");
    await expect(stale).toBeVisible();
    // The badge sits on the handover's own line.
    const line = await section.getByTestId("context-handover").boundingBox();
    const chip = await stale.boundingBox();
    expect(chip.y).toBeGreaterThanOrEqual(line.y - 1);
    expect(chip.y + chip.height).toBeLessThanOrEqual(line.y + line.height + 1);
    await expect(section.getByTestId("context-compaction")).toContainText(
      "4 comments summarised",
    );
    const compact = section.getByRole("button", { name: "Compact" });
    const write = section.getByRole("button", { name: "Write handover…" });
    await expectInside(compact, section, `Compact at ${viewport.width}px`);
    await expectInside(write, section, `Write handover at ${viewport.width}px`);
    await write.click();
    await expectInside(
      section.getByTestId("compact-editor"),
      section,
      `handover editor at ${viewport.width}px`,
    );
    await section.getByRole("button", { name: "Cancel" }).click();

    const summarised = page.locator(".timeline-summarised");
    await expect(summarised).toHaveCount(4);
    await expect(summarised.first()).not.toHaveAttribute("open", "");

    // Nothing on the page is wider than the window.
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(
      overflow,
      `page overflow at ${viewport.width}px`,
    ).toBeLessThanOrEqual(0);
  }
});
