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

// Questions and reports are two different waits: a question blocks its
// run until somebody replies, a report (a run that answered in a comment
// and opened no pull request) blocks nothing and only wants
// acknowledging. The demo seeds one of each: #4 asked a question, #7
// answered in a comment.
//
// The report is listed in its own group with an Info badge and an
// Acknowledge on the row itself, in the row's right-hand slot and centred
// on it, at desktop and phone widths; pressing it closes the task with no
// composer and no dialog, and Undo puts it back. The question keeps its
// Question badge and offers no row Acknowledge.

const QUESTION = "Investigate the merge queue stall";
const REPORT = "Why did last week's nightly suite slow down?";

async function reopen(baseURL, code, id) {
  await fetch(`${baseURL}/api/tasks/${id}/reopen`, {
    method: "POST",
    headers: { Authorization: `Bearer ${code}` },
  }).catch(() => {});
}

async function state(baseURL, code, id) {
  const res = await fetch(`${baseURL}/api/tasks/${id}`, {
    headers: { Authorization: `Bearer ${code}` },
  });
  return (await res.json()).state;
}

for (const viewport of [
  { width: 1280, height: 800 },
  { width: 390, height: 844 },
]) {
  test(`lists a report apart from a question, with a row Acknowledge in its right-hand slot at ${viewport.width}px`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/next/inbox");
    const reports = page.getByTestId("inbox-group-info");
    const questions = page.getByTestId("inbox-group-reply");
    await expect(reports).toContainText("Reports: acknowledge");
    const report = reports.getByTestId("queue-row").filter({ hasText: REPORT });
    const question = questions
      .getByTestId("queue-row")
      .filter({ hasText: QUESTION });
    await expect(report.locator(".status-pill")).toHaveText("Info");
    await expect(question.locator(".status-pill")).toHaveText("Question");
    await expect(question.getByTestId("row-acknowledge")).toHaveCount(0);

    const ack = report.getByTestId("row-acknowledge");
    await expect(ack).toBeVisible();
    const row = await report.locator(".queue-row-inner").boundingBox();
    const btn = await ack.boundingBox();
    const main = await report.locator(".row-main").boundingBox();
    // Inside the row, after the row's text, and centred on the row.
    expect(btn.x).toBeGreaterThanOrEqual(main.x + main.width);
    expect(btn.x + btn.width).toBeLessThanOrEqual(row.x + row.width);
    expect(
      Math.abs(btn.y + btn.height / 2 - (row.y + row.height / 2)),
    ).toBeLessThanOrEqual(2);
    // Nothing pushes the page sideways.
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
  });
}

test("acknowledges a report from its row in one click, and Undo reopens it", async ({
  page,
  demoServer,
  accessCode,
}) => {
  try {
    await page.goto("/next/inbox");
    const report = page
      .getByTestId("inbox-group-info")
      .getByTestId("queue-row")
      .filter({ hasText: REPORT });
    await report.getByTestId("row-acknowledge").click();
    await expect(page.getByText("Acknowledged 1 item")).toBeVisible();
    await expect.poll(() => state(demoServer, accessCode, "7")).toBe("closed");
    await page.getByRole("button", { name: "Undo" }).click();
    await expect
      .poll(() => state(demoServer, accessCode, "7"))
      .toBe("awaiting_reply");
    // The question beside it was never touched.
    expect(await state(demoServer, accessCode, "4")).toBe("awaiting_reply");
  } finally {
    if ((await state(demoServer, accessCode, "7")) === "closed")
      await reopen(demoServer, accessCode, "7");
  }
});
