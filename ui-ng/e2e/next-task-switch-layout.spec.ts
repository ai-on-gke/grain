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

test.use({ demoArgs: ["-as", "demo-operator"] });

// Selecting another task re-creates the inspector's header and tabs inside
// .task-page. When .task-page was an inline-size container nested in the
// .tasks-detail-pane size container, Chromium left about half of those
// re-inserted children without layout boxes at this size: the header and
// its actions read 0×0 until something restyled the page.
test.describe("Task inspector at 1440×1200", () => {
  test.use({ viewport: { width: 1440, height: 1200 } });

  test("Switching tasks always lays out the header", async ({
    page,
    demoServer,
    accessCode,
  }) => {
    const res = await fetch(`${demoServer}/api/tasks`, {
      headers: { Authorization: `Bearer ${accessCode}` },
    });
    expect(res.ok).toBeTruthy();
    const data = await res.json();
    const [first, second] = (Array.isArray(data) ? data : data.tasks).filter(
      (t: { consoleSession?: unknown }) => !t.consoleSession,
    );
    expect(second).toBeTruthy();

    await page.goto(`tasks?tab=all&task=${first.id}&pane=details`);
    const inspector = page.getByTestId("tasks-pane");
    const header = inspector.locator(".task-header");
    const actions = inspector.locator(".task-actions");
    await expect(actions).toBeInViewport({ ratio: 1 });

    for (let i = 0; i < 12; i++) {
      const task = i % 2 ? first : second;
      await page.getByTestId(`open-${task.id}`).click();
      await expect(inspector.locator("#task-title")).toContainText(task.title);
      await expect(actions).toBeInViewport({ ratio: 1 });
      const box = (await header.boundingBox())!;
      expect(box.height).toBeGreaterThan(32);
    }
  });
});
