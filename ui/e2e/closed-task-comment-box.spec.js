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

// A closed task's comment box says a comment won't reach an agent, and
// offers Reopen and send beside Comment only (task 3803). The note sits
// over the box, inside the timeline, and the two buttons fit beside the
// textarea on a desktop and stack full-width under it on a phone. Reopen
// and send reopens the task with the comment.

import { test, expect } from "./fixtures.js";

const VIEWPORTS = [
  { label: "desktop", width: 1440, height: 900 },
  { label: "phone", width: 390, height: 844 },
];

const NOTE = "This task is closed; a comment won't reach an agent.";

for (const vp of VIEWPORTS) {
  test(`a closed task's comment box warns and offers Reopen and send (${vp.label})`, async ({
    page,
    request,
  }) => {
    const created = await request.post("/api/tasks", {
      data: {
        title: `E2E closed comment ${vp.label} ${Date.now()}`,
        repo: "acme/widgets",
        approved: true,
      },
    });
    expect(created.ok()).toBe(true);
    const { id } = await created.json();
    expect(
      (await request.post(`/api/tasks/${id}/close`, { data: {} })).ok(),
    ).toBe(true);

    await page.setViewportSize({ width: vp.width, height: vp.height });
    await page.goto(`/tasks/${id}`);
    const note = page.getByText(NOTE);
    await expect(note).toBeVisible();
    const form = page.locator(".timeline .comment-form");
    const textarea = form.locator("textarea").first();
    const send = form.getByRole("button", { name: "Reopen and send" });
    const only = form.getByRole("button", { name: "Comment only" });
    await expect(send).toBeVisible();
    await expect(only).toBeVisible();

    const timeline = await page.locator(".timeline").boundingBox();
    const noteBox = await note.boundingBox();
    const formBox = await form.boundingBox();
    const taBox = await textarea.boundingBox();
    const sendBox = await send.boundingBox();
    const onlyBox = await only.boundingBox();
    // The note is over the box, and nothing runs out of the timeline.
    expect(noteBox.y + noteBox.height).toBeLessThanOrEqual(formBox.y + 1);
    for (const b of [noteBox, formBox, sendBox, onlyBox]) {
      expect(b.x).toBeGreaterThanOrEqual(timeline.x - 1);
      expect(b.x + b.width).toBeLessThanOrEqual(
        timeline.x + timeline.width + 1,
      );
    }
    // Each label on one line.
    expect(sendBox.height).toBeLessThan(48);
    expect(onlyBox.height).toBeLessThan(48);
    if (vp.label === "desktop") {
      // Beside the textarea, Reopen and send first.
      expect(sendBox.x).toBeGreaterThan(taBox.x + taBox.width - 1);
      expect(onlyBox.x).toBeGreaterThan(sendBox.x + sendBox.width - 1);
    } else {
      // Stacked under it, each a full-width target.
      expect(sendBox.y).toBeGreaterThan(taBox.y + taBox.height - 1);
      expect(onlyBox.y).toBeGreaterThan(sendBox.y + sendBox.height - 1);
      expect(Math.abs(sendBox.width - formBox.width)).toBeLessThan(2);
    }

    await textarea.fill("one more question");
    await send.click();
    await expect(note).toBeHidden();
    const after = await (await request.get(`/api/tasks/${id}`)).json();
    expect(after.state).not.toBe("closed");
    expect(
      (after.comments || []).some((c) => c.body === "one more question"),
    ).toBe(true);
  });
}
