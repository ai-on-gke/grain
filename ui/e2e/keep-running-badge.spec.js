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

// docs/test-runs/2026-11-02-security-fixes-live.md F2: a proposal held
// because it asks to keep running sat in the Inbox under an Approve
// button with nothing on its row saying so. Its row, and its card on the
// board, now carry a "keeps running" badge (KeepRunningChip.jsx) -- on
// screen and inside the row at a desktop's and a phone's width, in both
// themes -- and a one-off proposal beside it carries none. Both tasks
// filed here are closed again.

const sizes = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
];

async function expectInside(badge, container, width) {
  const box = await badge.boundingBox();
  const outer = await container.boundingBox();
  expect(box.x).toBeGreaterThanOrEqual(outer.x - 0.5);
  expect(box.x + box.width).toBeLessThanOrEqual(
    Math.min(outer.x + outer.width, width) + 0.5,
  );
  expect(box.y).toBeGreaterThanOrEqual(outer.y - 0.5);
  expect(box.y + box.height).toBeLessThanOrEqual(outer.y + outer.height + 0.5);
}

for (const size of sizes) {
  for (const scheme of ["light", "dark"]) {
    test(`a held keep_running proposal is marked in the inbox and on the board at ${size.width}px ${scheme}`, async ({
      page,
    }) => {
      await page.setViewportSize({ width: size.width, height: size.height });
      await page.emulateMedia({ colorScheme: scheme });
      const stamp = `${Date.now()}-${size.name}-${scheme}`;
      const standing = `Keep-running walk ${stamp} standing`;
      const oneOff = `Keep-running walk ${stamp} one-off`;
      const ids = [];
      try {
        for (const [title, keepRunning] of [
          [standing, true],
          [oneOff, false],
        ]) {
          const created = await page.request.post("/api/tasks", {
            data: { title, keepRunning },
          });
          expect(created.ok()).toBe(true);
          const task = await created.json();
          ids.push(task.id);
          // Filed unapproved, which is what puts it in the inbox with an
          // Approve button.
          expect(task.state).toBe("proposed");
          expect(!!task.keepRunning).toBe(keepRunning);
        }

        await page.goto("/inbox");
        const row = page.locator(".inbox-item", { hasText: standing });
        await expect(row).toBeVisible();
        await expect(
          row.getByRole("button", { name: "Approve" }),
        ).toBeVisible();
        const badge = row.getByTestId("task-keep-running-badge");
        await expect(badge).toHaveText("keeps running");
        await expect(badge).toHaveAttribute(
          "title",
          /never completes on its own/,
        );
        await expectInside(badge, row, size.width);
        const plain = page.locator(".inbox-item", { hasText: oneOff });
        await expect(plain).toBeVisible();
        await expect(plain.getByTestId("task-keep-running-badge")).toHaveCount(
          0,
        );

        const boards = await (await page.request.get("/api/boards")).json();
        const board = boards.find((b) => b.name === "Board") || boards[0];
        await page.goto(`/boards/${board.id}`);
        const card = page.locator(".board-card", { hasText: standing });
        await card.scrollIntoViewIfNeeded();
        await expect(card).toBeVisible();
        const cardBadge = card.getByTestId("task-keep-running-badge");
        await expect(cardBadge).toHaveText("keeps running");
        await expectInside(cardBadge, card, Number.POSITIVE_INFINITY);
        await expect(
          page
            .locator(".board-card", { hasText: oneOff })
            .getByTestId("task-keep-running-badge"),
        ).toHaveCount(0);
      } finally {
        for (const id of ids) {
          await page.request.post(`/api/tasks/${id}/close`, { data: {} });
        }
      }
    });
  }
}
