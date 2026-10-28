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

// grain/task-15: a standing session is deleted from the Sessions list,
// not only from inside its own pane. Every step is a real gesture
// against the real server; nothing is stubbed.
//
// The layout half is pinned with bounding boxes, as docs/ui-screenshots.md
// asks where the layout is the requirement: the bin sits in the row's
// right-hand slot and on the row's centre line, at a desktop width and
// on a phone, and the row stays one line without pushing the page
// sideways.

const DESKTOP = { width: 1440, height: 900 };
const PHONE_PORTRAIT = { width: 390, height: 844 }; // iPhone 14

async function assertNoHorizontalOverflow(page) {
  const { docWidth, scrollWidth } = await page.evaluate(() => ({
    docWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
  expect(
    scrollWidth,
    `Document scrollWidth (${scrollWidth}px) exceeds clientWidth (${docWidth}px)`,
  ).toBeLessThanOrEqual(docWidth + 1);
}

// A session made for the test over the API, titled from its first
// message the way the UI makes one (CreateConsoleSessionRequest.Message),
// and the ids of every session carrying that title, for the cleanup.
async function createSession(page, title) {
  const res = await page.request.post("/api/console-sessions", {
    data: { message: title },
  });
  expect(res.ok()).toBe(true);
  return (await res.json()).created;
}

async function sessionIds(page, title) {
  const res = await page.request.get("/api/console-sessions");
  return (await res.json()).sessions
    .filter((s) => s.title === title)
    .map((s) => s.id);
}

// sessionRow is a session's row on the list: the <li> holding its "Open
// session" button, its status chip and its bin side by side.
function sessionRow(page, title) {
  return page.getByRole("listitem").filter({
    has: page.getByRole("button", { name: `Open session ${title}` }),
  });
}

test.describe("Deleting a session from the Sessions list", () => {
  test("the row's bin sits at the row's right edge, centred, at desktop and phone widths", async ({
    page,
  }) => {
    const title = `Layout check ${Date.now()}`;
    await createSession(page, title);
    try {
      for (const size of [DESKTOP, PHONE_PORTRAIT]) {
        await page.setViewportSize(size);
        await page.goto("/sessions");
        const row = sessionRow(page, title);
        await expect(row).toBeVisible();
        const bin = row.getByRole("button", {
          name: `Delete session ${title}`,
        });
        await expect(bin).toBeVisible();

        const rowBox = await row.boundingBox();
        const binBox = await bin.boundingBox();
        const rowPaddingRight = await row.evaluate(
          (el) => parseFloat(getComputedStyle(el).paddingRight) || 0,
        );
        // In the right-hand slot: the bin ends where the row's content
        // does, within a pixel of its padding edge.
        expect(
          Math.abs(
            binBox.x +
              binBox.width -
              (rowBox.x + rowBox.width - rowPaddingRight),
          ),
          `bin right edge at ${size.width}px`,
        ).toBeLessThan(2);
        // On the row's centre line, like the name and the chips beside it.
        expect(
          Math.abs(
            binBox.y + binBox.height / 2 - (rowBox.y + rowBox.height / 2),
          ),
          `bin vertical centre at ${size.width}px`,
        ).toBeLessThan(2);
        // The bin and the name share the row rather than the bin being
        // wrapped under it: the name ends before the bin begins.
        const nameBox = await row.locator(".template-name").boundingBox();
        expect(nameBox.x + nameBox.width).toBeLessThanOrEqual(binBox.x + 1);
        // The bin is paid for by the row's hint, not by the row's
        // height nor, on a phone, by its status chip: the row is the
        // 24px line plus its padding, as a template's row is, and the
        // Director's "Starting" is whole at 390px rather than "Starti…"
        // (the chip's label fits its pill).
        const rowPaddingY = await row.evaluate(
          (el) =>
            parseFloat(getComputedStyle(el).paddingTop) +
            parseFloat(getComputedStyle(el).paddingBottom),
        );
        expect(rowBox.height).toBeLessThan(rowPaddingY + 24 + 2);
        const directorChip = sessionRow(page, "Director").locator(
          ".MuiChip-label",
        );
        await expect(directorChip).toHaveText(/\S/);
        const chipFits = await directorChip.evaluate(
          (el) => el.scrollWidth <= el.clientWidth + 1,
        );
        expect(
          chipFits,
          `Director's status chip whole at ${size.width}px`,
        ).toBe(true);
        await assertNoHorizontalOverflow(page);
      }
    } finally {
      for (const id of await sessionIds(page, title)) {
        await page.request.delete(`/api/console-sessions/${id}`);
      }
    }
  });

  test("deletes the row's session once confirmed, and stays on the list", async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);
    const title = `Night audit ${Date.now()}`;
    const id = await createSession(page, title);
    try {
      await page.goto("/sessions");
      const row = sessionRow(page, title);
      await expect(row).toBeVisible();
      const bin = row.getByRole("button", { name: `Delete session ${title}` });

      // Dismissing the confirmation keeps the session, and the click on
      // the bin did not open the row under it.
      page.once("dialog", (d) => d.dismiss());
      await bin.click();
      await expect(page).toHaveURL(/\/sessions$/);
      await expect(row).toBeVisible();
      expect(await sessionIds(page, title)).toEqual([id]);

      // Accepting it deletes the session: the row goes, the reader is
      // still on the list they were reading, and the server no longer
      // has it.
      page.once("dialog", (d) => d.accept());
      await bin.click();
      await expect(row).toHaveCount(0);
      await expect(page).toHaveURL(/\/sessions$/);
      await expect(
        page.getByRole("heading", { name: /sessions/i }),
      ).toBeVisible();
      await expect.poll(() => sessionIds(page, title)).toEqual([]);
      // The Director is still there to be opened: nothing else went.
      await expect(
        page.getByRole("button", { name: "Open session Director" }),
      ).toBeVisible();
    } finally {
      for (const left of await sessionIds(page, title)) {
        await page.request.delete(`/api/console-sessions/${left}`);
      }
    }
  });

  test("deleting the session last opened, from the list, stays on the list", async ({
    page,
  }) => {
    await page.setViewportSize(DESKTOP);
    const title = `Last opened ${Date.now()}`;
    const id = await createSession(page, title);
    try {
      // Open it, so it is the shell's open session, then go back to the
      // list by the rail and delete it there. Before grain/task-15's
      // change to deleteStandingSession this sent the reader into the
      // first remaining session's pane instead of leaving them on the
      // list.
      await page.goto(`/session/${id}`);
      await expect(page.locator(".console-session-title")).toHaveText(title);
      await page
        .getByRole("complementary")
        .getByRole("button", { name: /^Sessions/ })
        .click();
      await expect(page).toHaveURL(/\/sessions$/);

      const row = sessionRow(page, title);
      page.once("dialog", (d) => d.accept());
      await row
        .getByRole("button", { name: `Delete session ${title}` })
        .click();
      await expect(row).toHaveCount(0);
      await expect(page).toHaveURL(/\/sessions$/);
      await expect(page.locator(".console-session-title")).toHaveCount(0);
      await expect.poll(() => sessionIds(page, title)).toEqual([]);
    } finally {
      for (const left of await sessionIds(page, title)) {
        await page.request.delete(`/api/console-sessions/${left}`);
      }
    }
  });
  // The 2026-10-26 walkthrough's axe pass found the row was an
  // <li role="button"> with the bin nested inside it. Now the row is a
  // list item holding an "Open session" button and the bin side by side:
  // Tab reaches one and then the other, each does its own thing from the
  // keyboard, and a pointer still opens the session from anywhere else
  // on the row.
  test("a row is a list item with its open and delete controls side by side", async ({
    page,
  }) => {
    await page.setViewportSize(PHONE_PORTRAIT);
    const title = `Side by side ${Date.now()}`;
    const id = await createSession(page, title);
    try {
      await page.goto("/sessions");
      const row = sessionRow(page, title);
      await expect(row).toHaveCount(1);
      const open = page.getByRole("button", { name: `Open session ${title}` });
      const bin = row.getByRole("button", { name: `Delete session ${title}` });
      // Neither control is inside another, and the list's children are
      // list items rather than buttons.
      const nested = await page.evaluate(
        () =>
          [...document.querySelectorAll(".session-row button")].filter((b) =>
            b.parentElement.closest("button, [role=button]"),
          ).length,
      );
      expect(nested).toBe(0);
      expect(
        await page
          .locator(".template-list > :not(li), .template-list > [role]")
          .count(),
      ).toBe(0);

      await open.focus();
      await page.keyboard.press("Tab");
      await expect(bin).toBeFocused();
      page.once("dialog", (d) => d.dismiss());
      await page.keyboard.press("Enter");
      await expect(page).toHaveURL(/\/sessions$/);

      await open.focus();
      await page.keyboard.press("Enter");
      await expect(page).toHaveURL(new RegExp(`/session/${id}$`));

      await page.goBack();
      await expect(page).toHaveURL(/\/sessions$/);
      await row.locator(".chips").click();
      await expect(page).toHaveURL(new RegExp(`/session/${id}$`));
    } finally {
      for (const left of await sessionIds(page, title)) {
        await page.request.delete(`/api/console-sessions/${left}`);
      }
    }
  });
});
