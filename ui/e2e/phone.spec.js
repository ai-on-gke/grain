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

// The phone shell (src/phone.js, src/components/PhoneNav.jsx), in the one
// suite that can actually see a layout: jsdom has no layout engine and no
// stylesheet, so the component tests next door can assert which elements
// render and nothing at all about how wide they end up. Everything this
// change is *about* -- a rail that is not taking half the window, a pane
// that fills the screen, a page that does not scroll sideways -- is only
// checkable here.
//
// Two viewports, and the second matters as much as the first: the brief
// was a phone-friendly UI that leaves the tablet exactly as it was, so
// the tablet half of that is asserted rather than assumed.
//
// And two browsers: this is the one spec the webkit project in
// playwright.config.js runs as well as the chromium one, because it is
// the file whose subject is the browser -- touch input, layout, the
// coarse-pointer rules -- and the gestures in it were written for an
// iPad. Everything here is engine-neutral and passes on both; the one
// exception, the finger drag at the bottom, says so where it skips.
const PHONE = { width: 390, height: 844 }; // a modern phone, portrait
const TABLET = { width: 820, height: 1180 }; // an iPad Air, portrait

// src/theme.js's SIDEBAR_WIDTH, repeated rather than imported: these
// specs run in plain Node, which cannot resolve the MUI import that file
// pulls in. If the rail is ever resized, the tablet assertion below is
// what says so.
const SIDEBAR_WIDTH = 232;

// recordWideBranch gives repo a branch whose name is long enough to test
// the suggestion list's width, through grain's own branch API rather than
// an intercepted listing. The demo runs no branches reconciler, so the
// branch stays recorded and is what BranchField's `/branches` half
// suggests; its GitHub half (`/github-branches`) answers "not available"
// on a demo, which the field already treats as no suggestions. Recording
// the same name twice is harmless, so either of the two tests here may
// go first.
async function recordWideBranch(page, repo) {
  const res = await page.request.post(`/api/repos/${repo}/branches`, {
    data: { name: "feature/a-branch-name-wide-enough-to-test" },
  });
  expect(res.ok()).toBe(true);
}

// The Kanban board and the timeline are saved boards rather than fixed
// routes (grain/task-162), so the pages measured below are the two
// `grain demo` seeds for exactly that -- "Board" and "Timeline", each
// over every task. Their ids are issued by the store, so they are asked
// for rather than assumed (board.spec.js's own boardPath).
async function boardPath(page, name) {
  const res = await page.request.get("/api/boards");
  expect(res.ok(), `GET /api/boards: ${res.status()}`).toBe(true);
  const board = (await res.json()).find((b) => b.name === name);
  expect(board, `no board called "${name}" in the demo store`).toBeTruthy();
  return `/boards/${board.id}`;
}

test.describe("on a phone", () => {
  test.use({ viewport: PHONE, hasTouch: true, isMobile: true });

  test("hides the nav rail behind the bar, and opens it as a drawer", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // The rail is not on screen: its entries are unreachable until the
    // menu is tapped, and the page has the whole width.
    await expect(page.getByRole("button", { name: /^Repos/ })).toHaveCount(0);
    await expect(page.getByRole("heading", { name: "grain" })).toBeVisible();

    await page.getByRole("button", { name: "Open navigation" }).tap();
    await expect(page.getByRole("button", { name: /^Repos/ })).toBeVisible();

    // The same rail, doing the same job -- and getting out of the way
    // once it has done it. "Boards" opens the boards page;
    // what is being tested is the drawer, and any entry that leaves the
    // task list does that.
    await page.getByRole("button", { name: /^Boards/ }).tap();
    await expect(page).toHaveURL(/\/boards$/);
    await expect(page.getByRole("button", { name: /^Repos/ })).toHaveCount(0);
  });

  // A document wider than its own viewport is the symptom the wrapping
  // rules in style.css exist to prevent, and it is asked of every route
  // a phone lands on rather than of the task list alone: the test plan
  // for 2026-09-05 (section 8) calls out the timeline board and the
  // inbox by name, and each of these pages is wide for its own reason --
  // a task row's chips (repo, base, capabilities) are wider than a phone
  // laid end to end, an inbox row carries its answers beside its text,
  // the board is columns in a row, and the timeline is a name column
  // beside a track.
  //
  // The measurement stays at the document level on purpose. Several
  // things here scroll sideways *inside* their own box because they are
  // meant to -- .board-columns, so the board is swiped through a column
  // at a time, and .markdown pre/table, so a wide code block or table in
  // a description does not stretch the pane around it. None of those add
  // to the width of the document, and none of them should fail this.
  test("does not scroll sideways", async ({ page }) => {
    for (const [path, ready] of [
      ["/", ".task-row"],
      ["/inbox", ".inbox-item"],
      // The repos list, whose rows since grain/task-199 hold a width
      // open for the repo's name whatever its count chips want
      // (--list-title-min, and e2e/narrow.spec.js for the whole of it).
      // A floor is exactly the kind of rule that pushes a row past the
      // edge of a screen this narrow, and this row keeps its on a phone
      // -- unlike a task row, which wraps here and hands its title the
      // whole width anyway -- so it is asked the question with the rest.
      ["/repos", ".repo-list-row"],
      [await boardPath(page, "Board"), ".board-column"],
      // The timeline's own fit test below says more about this page --
      // that the track keeps most of the width once the name column has
      // shrunk -- but the rule itself belongs in one list.
      [await boardPath(page, "Timeline"), ".gantt-bar"],
    ]) {
      await page.goto(path);
      await expect(page.locator(ready).first()).toBeVisible();
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      expect(
        overflow,
        `${path} is ${overflow}px wider than the phone`,
      ).toBeLessThanOrEqual(0);
    }
  });

  // Where a row's state badge sits, which is the one thing about a task
  // row this width reorders: the badge leads the row everywhere else and
  // trails it here (style.css's .task-row .badge-icon), so the number and
  // the title start at the gutter and the state is read at the end of the
  // line rather than ahead of the title it says nothing about.
  test("puts a task row's state badge at the end of its line", async ({
    page,
  }) => {
    await page.goto("/");
    const row = page.locator(".task-row").first();
    await expect(row).toBeVisible();

    const rowBox = await row.boundingBox();
    const badge = await row.locator(".badge-icon").boundingBox();
    const title = await row.locator(".task-title").boundingBox();

    // Past the title rather than ahead of it...
    expect(badge.x).toBeGreaterThanOrEqual(title.x + title.width);
    // ...at the far end of the row, with only the row's own 1rem gutter
    // after it...
    expect(
      rowBox.x + rowBox.width - (badge.x + badge.width),
    ).toBeLessThanOrEqual(20);
    // ...and still on the title's own line, not sorted down onto one of
    // the lines the activity note and the chips wrap into below it.
    expect(badge.y).toBeLessThan(title.y + title.height);
  });

  test("gives a task's pane the whole width", async ({ page }) => {
    await page.goto("/");
    await page.locator(".task-row").first().tap();

    const pane = page.locator(".MuiDialog-paper");
    await expect(pane).toBeVisible();
    const box = await pane.boundingBox();
    expect(Math.round(box.width)).toBe(PHONE.width);
  });

  // Reordering the backlog with a finger (ReorderPickup.jsx,
  // grain/task-23). This is the suite that can actually check it: the
  // gesture exists because a phone browser fires no drag events, and
  // "tap" here is a real touch event against a real touch-enabled
  // browser rather than a click the component tests next door dispatch.
  test("moves a task to the top of the backlog by tapping", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const titles = () =>
      page.locator(".task-row .task-title").allTextContents();
    const before = await titles();
    expect(before.length).toBeGreaterThan(1);

    // The handle is a touch target rather than a 20px icon: the hit
    // area is a pseudo-element centred on it, so it is the tap that has
    // to land, not the box that has to be big.
    const handle = page.locator(".task-drag-handle").nth(1);
    await expect(handle).toBeVisible();
    await handle.tap();

    await expect(page.getByRole("status")).toContainText("Moving 1 task");
    const slot = page.getByRole("button", { name: /^Move here/ }).first();
    const slotBox = await slot.boundingBox();
    expect(slotBox.height).toBeGreaterThanOrEqual(44);
    await slot.tap();

    // The second task is the first one now, and the gesture is over.
    await expect(page.locator(".task-row .task-title").first()).toHaveText(
      before[1],
    );
    await expect(page.getByRole("status")).toHaveCount(0);

    // And it is the server's own order now, not just this screen's.
    await page.reload();
    await expect(page.locator(".task-row .task-title").first()).toHaveText(
      before[1],
    );
  });

  // Everything you can press, big enough to press. 40px is the floor
  // the coarse-pointer rules in style.css put under MUI's own controls
  // (31px small buttons, 37px medium ones, a 38px checkbox); the only
  // things under it are the two kinds of target that are not a box -- a
  // link inside a sentence, and the drag handle, whose hit area is a
  // pseudo-element the next test presses. Measured on the two pages a
  // phone spends its time on: the backlog and the inbox.
  test("gives everything you can press a target a thumb can hit", async ({
    page,
  }) => {
    for (const [path, ready] of [
      ["/", ".task-row"],
      // The inbox landed (InboxPage.jsx, grain/task-20) while this pass
      // was being written, and it is the page whose whole purpose is
      // pressing something: every row carries its own answers.
      ["/inbox", ".inbox-item"],
    ]) {
      await page.goto(path);
      await expect(page.locator(ready).first()).toBeVisible();
      const { measured, small } = await pressableSizes(page);
      expect(small).toEqual([]);
      // A page that rendered no controls at all would pass the line
      // above without measuring anything.
      expect(measured).toBeGreaterThan(3);
    }
  });

  // The measurement itself: what the browser makes of every control on
  // the page it is currently showing. The ones that fall short come back
  // named, so a failure says which control shrank rather than how many.
  async function pressableSizes(page) {
    return await page.evaluate(() => {
      const out = [];
      let measured = 0;
      for (const el of document.querySelectorAll(
        "button, .MuiSelect-select, input:not([type=hidden])",
      )) {
        const cls = el.className.toString();
        // MUI hides an input inside every checkbox and select; the box
        // around it is what takes the tap.
        if (cls.includes("nativeInput") || cls.includes("PrivateSwitchBase"))
          continue;
        if (cls.includes("task-drag-handle")) continue;
        // A text field's own <input> is the text, not the target: MUI
        // draws the box around it and a tap anywhere in that box puts
        // the caret in this input. So the field is what is measured
        // where there is one -- an Autocomplete's input is 28px inside a
        // 40px field, and asking the input alone would report a control
        // nobody can miss as one nobody can hit.
        const box = el.closest(".MuiInputBase-root") || el;
        const r = box.getBoundingClientRect();
        if (r.width === 0 && r.height === 0) continue;
        measured += 1;
        if (r.height >= 40) continue;
        out.push(
          `${(el.getAttribute("aria-label") || el.getAttribute("placeholder") || el.textContent || cls).trim()} (${Math.round(r.height)}px)`,
        );
      }
      return { measured, small: out };
    });
  }

  // The dashboard's own pressable things, which the measurement above
  // does not see: a panel's "Open" is an anchor rather than a button and
  // the page it is on is not one of the two measured there. The hand run
  // of 2026-09-07 measured "Open" at 31x19px at both phone widths
  // (docs/hand-runs, section 6), which is why there is a rule for it in
  // style.css and a test for it here.
  //
  // "Keep" is the same word at the same size beside it, offered on a
  // panel showing a narrowing its reader made (PanelBoard.jsx), so it
  // is measured with it -- and it is the one of the two that writes
  // something rather than navigating, which is the worse of the two to
  // hit by accident. It only exists once the toolbar has been narrowed,
  // so the narrowing is part of the measurement.
  //
  // A dashboard is seeded rather than found: `grain demo` has no board
  // of panels, and the store belongs to every spec this worker runs, so
  // it goes away again the moment the measuring is done (boards.spec.js
  // keeps the same books for the same reason).
  test("gives a panel's Open and Keep a target a thumb can hit", async ({
    page,
  }) => {
    const res = await page.request.post("/api/boards", {
      data: {
        name: `E2E phone dashboard ${Date.now()}`,
        kind: "panels",
        panels: [
          { kind: "states", title: "Where everything is", width: "half" },
          {
            kind: "list",
            title: "Running now",
            query: "f=state:any:running",
            width: "half",
          },
        ],
      },
    });
    expect(res.ok(), `POST /api/boards: ${res.status()}`).toBe(true);
    const board = await res.json();
    try {
      await page.goto(`/boards/${board.id}`);
      const links = page.locator(".panel-open");
      await expect(links).toHaveCount(2);

      for (const link of await links.all()) {
        const box = await link.boundingBox();
        const where = await link.evaluate(
          (el) =>
            el.closest(".panel").querySelector(".panel-title").textContent,
        );
        expect(
          Math.round(box.height),
          `"Open" on "${where}" is ${Math.round(box.width)}x${Math.round(box.height)}px`,
        ).toBeGreaterThanOrEqual(44);
        expect(Math.round(box.width)).toBeGreaterThanOrEqual(44);
      }

      // "Keep", once there is a narrowing to keep. It appears on the
      // panel with no query of its own -- the other one does not follow
      // the toolbar -- and it stands beside "Open" in the same header,
      // so the two are asked for the same target and for room between
      // them: two thumb-sized boxes that touch are one box a reader
      // cannot aim inside.
      await page.getByPlaceholder("Search this dashboard…").fill("toolchain");
      const keep = page
        .locator(".panel", { hasText: "Where everything is" })
        .locator(".panel-keep");
      await expect(keep).toBeVisible();
      const keepBox = await keep.boundingBox();
      expect(
        Math.round(keepBox.height),
        `"Keep" is ${Math.round(keepBox.width)}x${Math.round(keepBox.height)}px`,
      ).toBeGreaterThanOrEqual(44);
      expect(Math.round(keepBox.width)).toBeGreaterThanOrEqual(44);
      const openBox = await page
        .locator(".panel", { hasText: "Where everything is" })
        .locator(".panel-open")
        .boundingBox();
      expect(openBox.x).toBeGreaterThanOrEqual(keepBox.x + keepBox.width);

      // ...and the header is no taller for carrying two of them: the
      // negative margins in style.css spend the panel's own padding
      // rather than standing 44px tall inside it, so this header is the
      // height of the one next to it that has only "Open".
      const headerOf = (title) =>
        page
          .locator(".panel", { hasText: title })
          .locator(".panel-header")
          .boundingBox();
      const withKeep = await headerOf("Where everything is");
      const without = await headerOf("Running now");
      expect(Math.round(withKeep.height)).toBe(Math.round(without.height));

      await page.getByPlaceholder("Search this dashboard…").fill("");
      await expect(keep).toHaveCount(0);

      // ...and the target is the link, not just a box drawn around it: a
      // tap in the corner of it, which is outside the word "Open"
      // altogether, opens the view the panel summarises with the panel's
      // own question in force.
      const open = page
        .locator(".panel", { hasText: "Running now" })
        .locator(".panel-open");
      // Brought onto the screen first: panels stack at a phone's width,
      // so the second panel's header sits below the fold once the first
      // one is tall enough -- and a tap at a coordinate the window does
      // not contain reaches nothing at all, which is a pass for the
      // wrong reason at best. What is being measured is where the tap
      // lands inside the link, not whether the link happened to be in
      // view.
      await open.scrollIntoViewIfNeeded();
      const box = await open.boundingBox();
      await page.touchscreen.tap(box.x + 4, box.y + 4);
      await expect(page).toHaveURL(/\/\?f=state:any:running/);
      await expect(
        page.locator(".task-row", { hasText: "Bump the Go toolchain to 1.24" }),
      ).toBeVisible();
    } finally {
      await page.request.delete(`/api/boards/${board.id}`);
    }
  });

  // The handle keeps its 1.25rem box, because every row's contents line
  // up against that column -- what grows on a touchscreen is the area
  // that answers to a tap, which is a pseudo-element and so invisible to
  // the measuring above. A tap 14px below the icon is outside it and
  // inside the hit area.
  test("answers a tap beside the drag handle, not just on it", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const box = await page.locator(".task-drag-handle").first().boundingBox();
    await page.touchscreen.tap(
      box.x + box.width / 2,
      box.y + box.height / 2 + 14,
    );

    await expect(page.getByRole("status")).toContainText("Moving 1 task");
  });

  // The batch-actions bar is fixed to the bottom edge and so takes no
  // space in the layout: the list goes on running underneath it, and
  // scrolled to the end with a selection up, the last row was behind the
  // bar rather than above it -- with the scroll already at its end,
  // there was nothing left to do about it. It is worst on a phone, where
  // the bar's buttons wrap onto three rows and swallow the last row and
  // a half of the list, which is why it is asked here; the reservation
  // itself (BatchActionsBar's own height, published as
  // --batch-bar-height and added to the bottom padding of whatever
  // scrolls under it) is the same on every width.
  test("leaves the end of the list clear of the batch actions bar", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    await page.locator(".task-row").first().getByRole("checkbox").check();
    // .last(): an ancestor comes before its descendant in document
    // order, so this is the bar itself rather than any Paper around it.
    const bar = page
      .locator(".MuiPaper-root", { hasText: "1 selected" })
      .last();
    await expect(bar).toBeVisible();

    // As far down as the list goes: what is under the bar at the end of
    // the scroll is unreachable, and only the end of the scroll says so.
    await page.locator("main").evaluate((el) => {
      el.scrollTop = el.scrollHeight;
    });
    const rows = page.locator(".task-row");
    const last = await rows.last().boundingBox();
    const barBox = await bar.boundingBox();
    expect(await rows.count()).toBeGreaterThan(1);
    expect(last.y + last.height).toBeLessThanOrEqual(barBox.y);
  });

  // The same reservation on the board, which pads elsewhere: its own
  // main is overflow:hidden, and the columns take their height from
  // .board-columns -- so a board whose columns still ran to the bottom
  // edge would hide the end of the longest one behind the bar.
  test("leaves the board's columns clear of the batch actions bar", async ({
    page,
  }) => {
    await page.goto(await boardPath(page, "Board"));
    await expect(page.locator(".board-column").first()).toBeVisible();

    await page
      .locator(".board-column-cards li")
      .first()
      .getByRole("checkbox")
      .check();
    // .last(): an ancestor comes before its descendant in document
    // order, so this is the bar itself rather than any Paper around it.
    const bar = page
      .locator(".MuiPaper-root", { hasText: "1 selected" })
      .last();
    await expect(bar).toBeVisible();

    // The column itself, not the .board-columns around it: the
    // reservation is that row's own bottom padding, which is inside its
    // box, so the box still reaches the bottom edge and the columns
    // standing in it are what stops short of the bar.
    const column = await page.locator(".board-column").first().boundingBox();
    const barBox = await bar.boundingBox();
    expect(column.y + column.height).toBeLessThanOrEqual(barBox.y);
  });

  // The timeline is two columns wide by construction -- a name and a
  // track (style.css) -- which is exactly the shape that pushes a
  // document past its own viewport if the name column keeps its desktop
  // width. It shrinks instead (GanttChart.jsx, grain/task-55), and this
  // is what says so.
  test("fits the timeline's name column and its track on the screen", async ({
    page,
  }) => {
    await page.goto(await boardPath(page, "Timeline"));

    await expect(page.locator(".gantt-bar").first()).toBeVisible();
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
    // ...and the track is still most of the width, which is the part
    // worth looking at.
    const track = page.locator(".gantt-track").first();
    const box = await track.boundingBox();
    expect(box.width).toBeGreaterThan(PHONE.width * 0.4);
  });

  // One column per screen, so the board is swiped through rather than
  // read three-thirds at a time.
  test("gives a board column most of the screen", async ({ page }) => {
    await page.goto(await boardPath(page, "Board"));

    const column = page.locator(".board-column").first();
    await expect(column).toBeVisible();
    const box = await column.boundingBox();
    expect(box.width).toBeGreaterThan(PHONE.width * 0.75);
  });

  // The board is the other half of the same gesture (TaskBoard.jsx), and
  // the half nothing had touched with a finger: a card is picked up by
  // its own handle and put down between the cards of the column it came
  // from -- and only that column, because a task's state is derived
  // rather than set, so there is no dropping a card into "Running".
  test("moves a card up its board column by tapping", async ({ page }) => {
    await page.goto(await boardPath(page, "Board"));
    await expect(page.locator(".board-column").first()).toBeVisible();

    // A column with two cards in it: a column of one has nowhere to
    // move anything to, and its handle is disabled saying so. Polled,
    // because the columns draw before the tasks behind them arrive.
    const columnWithTwo = () =>
      page.evaluate(() =>
        [...document.querySelectorAll(".board-column")].findIndex(
          (c) => c.querySelectorAll(".board-card").length > 1,
        ),
      );
    await expect.poll(columnWithTwo).toBeGreaterThanOrEqual(0);
    const which = await columnWithTwo();
    const column = () => page.locator(".board-column").nth(which);
    const titles = () =>
      column().locator(".board-card-title").allTextContents();
    const before = await titles();
    // The move below is only visible because these two differ.
    expect(before[1]).not.toBe(before[0]);

    await column().locator(".task-drag-handle").nth(1).tap();
    await expect(page.getByRole("status")).toContainText("Moving 1 task");

    // Every slot on the board is in this one column, and each is a
    // target rather than the 2px rule a drop draws.
    const slots = page.getByRole("button", { name: /^Move here/ });
    const here = column().getByRole("button", { name: /^Move here/ });
    await expect(here.first()).toBeVisible();
    expect(await here.count()).toBe(await slots.count());
    const slotBox = await here.first().boundingBox();
    expect(slotBox.height).toBeGreaterThanOrEqual(40);

    await here.first().tap();
    await expect(column().locator(".board-card-title").first()).toHaveText(
      before[1],
    );
    await expect(page.getByRole("status")).toHaveCount(0);

    // And the backlog itself was reordered, not just this column's view
    // of it: the move survives a reload, and the list has it too.
    await page.reload();
    await expect(column().locator(".board-card-title").first()).toHaveText(
      before[1],
    );
  });

  // The rail's own entries, once the drawer is open. The measurement
  // above is of the page, and on a phone the rail is not on it -- so
  // every nav entry in this deployment went unmeasured until here, and
  // the two section headings ("Boards", "Consoles") were 55x20px words
  // in a rail whose every other row is 44.
  test("gives the drawer's own entries a target a thumb can hit", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();
    await page.getByRole("button", { name: "Open navigation" }).tap();
    await expect(page.getByRole("button", { name: /^Repos/ })).toBeVisible();

    const { measured, small } = await pressableSizes(page);
    expect(small).toEqual([]);
    expect(measured).toBeGreaterThan(3);
  });

  // The foot of the rail -- System, the signed-in user, the build -- is
  // pinned under the drawer's list, which scrolls on its own
  // (Sidebar.jsx's pinFooter). Left to the rail's single scrolling pane,
  // as beside the page, it opened below the fold: the 2026-10-12 team
  // switcher walkthrough (F5) measured System's bottom at 824 on an 812
  // screen. Asked at that walkthrough's 375x812 rather than PHONE's 844,
  // since the shorter the screen, the more there is to push the footer
  // off it. (The second team that once filled the rail with a team
  // switcher went with teams: docs/single-tenant-reset.md, 3e-iii-c2.)
  // Nothing is scrolled first: on screen when the drawer opens is the
  // claim.
  test("keeps the rail's footer on screen when the drawer opens", async ({
    page,
  }) => {
    const SHORT_PHONE = { width: 375, height: 812 };
    await page.setViewportSize(SHORT_PHONE);
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();
    await page.getByRole("button", { name: "Open navigation" }).tap();
    const drawer = page.locator(".MuiDrawer-paper");

    for (const target of [
      drawer.getByRole("button", { name: "System", exact: true }),
      drawer.getByTestId("build-stamp"),
    ]) {
      await expect(target).toBeVisible();
      const box = await target.boundingBox();
      expect(box.y).toBeGreaterThanOrEqual(0);
      expect(box.y + box.height).toBeLessThanOrEqual(SHORT_PHONE.height);
    }
    // grain has no users (docs/design/settings-panel.md §6).
    await expect(drawer.getByTestId("user-tile")).toHaveCount(0);
  });

  // The New task form, which is a phone surface by construction: "+" is
  // one of the two controls PhoneNav keeps out of the drawer, on the
  // argument that filing a task is what somebody does on a phone without
  // navigating anywhere first. Everything in the form is measured for
  // the first time here -- the repo picker is an Autocomplete, which
  // brings its own field and its own dropdown arrow, and neither is a
  // .MuiButton-root or a small icon button, so neither of style.css's
  // coarse-pointer rules had ever reached them: a 28px field with a 28px
  // arrow on the end of it.
  test("gives the New task form's controls a target a thumb can hit", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();
    await page
      .getByRole("button", { name: /New task/i })
      .first()
      .tap();
    await expect(page.getByLabel("Title")).toBeVisible();

    const { measured, small } = await pressableSizes(page);
    expect(small).toEqual([]);
    expect(measured).toBeGreaterThan(3);

    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
  });

  // Branch suggestions were added after the form's original mobile check.
  // Their Popper is rendered outside the dialog, so measuring the dialog
  // alone would miss a list that spills off the screen or gives its rows
  // desktop-sized tap targets.
  test("keeps branch suggestions on screen and easy to tap", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();
    await page
      .getByRole("button", { name: /New task/i })
      .first()
      .tap();

    const repo = page.getByLabel(/^Repos/);
    const repoOption = repo.locator("option[value*='/']").first();
    await expect(repoOption).toBeAttached();
    const option = await repoOption.getAttribute("value");
    expect(option).toBeTruthy();
    await repo.selectOption(option);
    await recordWideBranch(page, option);

    const branch = page.getByLabel("Base branch");
    await branch.fill("");
    await branch.scrollIntoViewIfNeeded();
    await branch.click();
    const suggestion = page.getByRole("menuitem").first();
    await expect(suggestion).toBeVisible();

    const box = await suggestion.boundingBox();
    expect(box).not.toBeNull();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(PHONE.width);
    expect(box.height).toBeGreaterThanOrEqual(44);
  });

  // A standing session's pane (ConsoleSessionPage.jsx, grain/task-415):
  // the director's chat, reached from the drawer like every other rail
  // entry. It is the one destination in the shell that draws its own
  // body rather than a .content-header with rows under it, and so the
  // one that has to carry the page's own gutters -- without them its
  // title sat flush against the edge of the screen and MUI's -11px on a
  // checkbox label hung the first capability's box off it.
  test("keeps the director's pane inside the screen", async ({ page }) => {
    await page.goto("/session/director");
    await expect(page.locator(".console-session-page")).toBeVisible();

    const pane = page.locator(".console-session-page");
    await expect(pane).toBeVisible();
    // The drawer got out of the way, the way it does for a board.
    await expect(page.getByRole("button", { name: /^Repos/ })).toHaveCount(0);

    // Nothing in it starts left of the screen -- the capabilities are
    // checkbox rows, and a checkbox half off the edge is both the
    // clipped thing and the hard thing to hit.
    const spill = await page.evaluate(() => {
      const out = [];
      for (const el of document.querySelectorAll(".console-session-page *")) {
        const r = el.getBoundingClientRect();
        if (r.width === 0 && r.height === 0) continue;
        if (r.left < -0.5 || r.right > window.innerWidth + 0.5) {
          out.push(
            `${el.tagName}.${el.className.toString().slice(0, 40)} ${Math.round(r.left)}..${Math.round(r.right)}`,
          );
        }
      }
      return out;
    });
    expect(spill).toEqual([]);

    const { small } = await pressableSizes(page);
    expect(small).toEqual([]);
  });

  // An interactive task's chat (TaskChat.jsx, grain/task-414), which is
  // the pane that argues for itself by being legible on a phone -- and
  // the one thing in the tree whose layout is a library's rather than
  // this stylesheet's.
  //
  // A message is not prose that wraps: most of what an agent sends back
  // is a code block, and the library lays a bubble out as nested flex
  // items, whose default `min-width: auto` will not shrink below their
  // own contents. One 96-character line therefore sized the bubble to
  // itself, the bubble stretched .detail-main -- which on a phone takes
  // its width from its contents, the columns being stacked -- and the
  // message ended up 250px off the side of a 390px screen, clipped by
  // .task-chat-window's own overflow with nothing left to scroll. The
  // fix is two `min-width: 0`s in style.css; this is what says the
  // bubble stayed inside the conversation and the block inside it can
  // still be read to its end.
  test("keeps a chat message inside the window, whatever is in it", async ({
    page,
  }) => {
    await openChatTask(page);

    const list = await page.locator(".cs-message-list").boundingBox();
    const bubble = await page.locator(".cs-message").last().boundingBox();
    expect(
      Math.round(bubble.x),
      `the bubble starts ${Math.round(list.x - bubble.x)}px left of the conversation`,
    ).toBeGreaterThanOrEqual(Math.round(list.x));
    expect(Math.round(bubble.x + bubble.width)).toBeLessThanOrEqual(
      Math.round(list.x + list.width),
    );

    // ...and the block itself is readable rather than merely contained:
    // it is narrower than its own content, and scrolls to the end of it.
    const code = await page.evaluate(() => {
      const pre = document.querySelector(".task-chat-body pre");
      if (!pre) return null;
      pre.scrollLeft = 9999;
      return {
        clientWidth: pre.clientWidth,
        scrollWidth: pre.scrollWidth,
        scrolled: pre.scrollLeft,
      };
    });
    expect(code, "no code block in the message").toBeTruthy();
    expect(code.scrollWidth).toBeGreaterThan(code.clientWidth);
    expect(code.scrolled).toBeGreaterThan(0);

    // The page itself is unmoved by any of it -- the symptom the whole
    // "does not scroll sideways" test above exists for.
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);

    // The composer under it is a thumb target like everything else on
    // this screen, and the Send beside it likewise. The box you type
    // into is the library's contenteditable, which is not a <button> or
    // an <input> and so is invisible to the measurement above: 20px of
    // it inside a 50px row, all of whose padding belongs to a container
    // a tap on it does not reach.
    const { small } = await pressableSizes(page);
    expect(small).toEqual([]);
    const editor = await page
      .locator(".cs-message-input__content-editor")
      .boundingBox();
    expect(
      Math.round(editor.height),
      `the message box is ${Math.round(editor.height)}px tall`,
    ).toBeGreaterThanOrEqual(40);

    // ...and it takes what is typed into it: a tap in the bottom of that
    // box, which is outside the 20px strip it used to be, puts the caret
    // in the composer.
    const box = await page
      .locator(".cs-message-input__content-editor")
      .boundingBox();
    await page.touchscreen.tap(box.x + box.width / 2, box.y + box.height - 4);
    await expect(
      page.locator(".cs-message-input__content-editor"),
    ).toBeFocused();
  });
});

// An interactive task with one wide code block said back on it: the
// shape of an ordinary exchange with an agent, and the one that used to
// take the pane apart. Filed over the API rather than sought in the
// demo store, which seeds no interactive task -- and left there
// afterwards, there being no way to delete a task, which is why this is
// the last thing either describe does.
// openChatTask files that task, opens it, and waits until the
// conversation is really on the screen -- which is what both chat tests
// below need before they measure anything.
//
// It exists because those two did `goto` and then asserted on
// `.task-chat-window` with the default five-second expect timeout, and
// that window is not a layout question: DetailOverlay renders the chat
// only once the task has arrived and reads interactive (`t.interactive ?`),
// so the wait covers the whole cold start -- the bundle, the first
// /api/tasks, the render. A chromium worker starting cold beside three
// others has been seen to need longer than five seconds for that, and
// this suite retries nothing (retries: 0 in playwright.config.js), so
// one slow start was one red build with nothing wrong in it.
//
// The generous timeout is on this step only. Everything after it is a
// question about layout, where five seconds is already far more than an
// answer can honestly take, and lengthening those would only make a real
// failure slower to report.
async function openChatTask(page) {
  const task = await chatTaskWithCodeBlock(page);
  await page.goto(`/tasks/${task.id}`);
  await expect(page.locator(".task-chat-window")).toBeVisible({
    timeout: 30000,
  });
  // And the message itself, before anything measures it. `.last()` on an
  // empty locator resolves to nothing and boundingBox() answers null, so
  // a conversation one tick from rendering failed as "cannot read
  // properties of null (reading 'x')" -- a TypeError that names neither
  // the element nor the wait it needed.
  await expect(page.locator(".cs-message").last()).toBeVisible();
  return task;
}

async function chatTaskWithCodeBlock(page) {
  const res = await page.request.post("/api/tasks", {
    data: {
      title: "E2E chat layout",
      body: "What did the run actually do?",
      interactive: true,
    },
  });
  expect(res.ok(), `POST /api/tasks: ${res.status()}`).toBe(true);
  const task = await res.json();
  const said = await page.request.post(`/api/tasks/${task.id}/comments`, {
    data: {
      body:
        "```\nmake test-e2e && go test -race -timeout 30m ./... && " +
        "echo a very wide line of code here indeed\n```",
    },
  });
  expect(
    said.ok(),
    `POST /api/tasks/${task.id}/comments: ${said.status()}`,
  ).toBe(true);
  return task;
}

// The tablet is the layout this change is careful not to touch: the rail
// stands beside the page, and a pane starts exactly where the rail ends.
test.describe("on a tablet", () => {
  test.use({ viewport: TABLET, hasTouch: true, isMobile: true });

  test("keeps branch suggestions inside the task form", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();
    await page
      .getByRole("button", { name: /New task/i })
      .first()
      .tap();

    const repo = page.getByLabel(/^Repos/);
    const repoOption = repo.locator("option[value*='/']").first();
    await expect(repoOption).toBeAttached();
    const option = await repoOption.getAttribute("value");
    expect(option).toBeTruthy();
    await repo.selectOption(option);
    await recordWideBranch(page, option);
    const branch = page.getByLabel("Base branch");
    await branch.fill("");
    await branch.tap();

    const suggestion = page.getByRole("menuitem").first();
    await expect(suggestion).toBeVisible();
    const box = await suggestion.boundingBox();
    expect(box).not.toBeNull();
    expect(box.x).toBeGreaterThanOrEqual(SIDEBAR_WIDTH);
    expect(box.x + box.width).toBeLessThanOrEqual(TABLET.width);
    expect(box.height).toBeGreaterThanOrEqual(44);
  });

  test("keeps the rail beside the page", async ({ page }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    await expect(page.getByRole("button", { name: /^Repos/ })).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Open navigation" }),
    ).toHaveCount(0);

    await page.locator(".task-row").first().tap();
    const box = await page.locator(".MuiDialog-paper").boundingBox();
    expect(Math.round(box.width)).toBe(TABLET.width - SIDEBAR_WIDTH);
  });

  // The other half of the phone's own badge rule above: a tablet has the
  // width for one line of "badge, number, title, chips", so it keeps the
  // badge where every other width puts it -- ahead of the number, in the
  // column the titles line up after.
  test("keeps a task row's state badge ahead of its title", async ({
    page,
  }) => {
    await page.goto("/");
    const row = page.locator(".task-row").first();
    await expect(row).toBeVisible();

    const badge = await row.locator(".badge-icon").boundingBox();
    const title = await row.locator(".task-title").boundingBox();
    expect(badge.x + badge.width).toBeLessThanOrEqual(title.x);
  });

  // Dragging the backlog with a finger (ReorderPickup.jsx,
  // grain/task-51), on the device it was reported from: an iPad, where
  // the tap gesture above was already shipping and "touch and drag a
  // task" still did nothing at all.
  //
  // It is checked here and nowhere else for the reason the tap is: this
  // is the only suite with a real browser, a real layout and a real
  // touchscreen behind it. Playwright's own touchscreen can tap and
  // cannot drag, so the finger below is Chromium's own input pipeline
  // driven over CDP -- which is what makes these real pointer events
  // with a real touch pointerType, rather than events the page has been
  // handed and would believe either way.
  //
  // Which is also why this is the one test in this file the webkit
  // project cannot run (grain/task-74). CDP is Chromium-only, and there
  // is no honest stand-in for it there: WebKit's own protocol has the
  // command (Input.dispatchTouchEvent) and Playwright exposes neither it
  // nor any way to reach it, a page.mouse drag arrives as pointerType
  // "mouse" and this gesture ignores those on purpose, and a TouchEvent
  // dispatched from the page produces no pointer events at all -- which
  // would make this test pass against a page that does nothing. WebKit
  // will not even construct one. So it skips, loudly, and the gesture
  // this file's tablet section is about goes on being checked against
  // one engine only. See playwright.config.js for what that leaves
  // asserted rather than verified about Safari.
  test("drags a task to the top of the backlog with a finger", async ({
    page,
    browserName,
  }) => {
    test.skip(
      browserName !== "chromium",
      "needs a touch point that moves: Playwright's touchscreen only taps, and the CDP this drives it through is Chromium-only",
    );
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const titles = () =>
      page.locator(".task-row .task-title").allTextContents();
    const before = await titles();
    expect(before.length).toBeGreaterThan(1);

    const finger = await touchscreen(page);
    const handle = page.locator(".task-drag-handle").nth(1);
    await finger.press(await middleOf(handle));

    // Pressed, not tapped: the row comes up under the finger while it is
    // still down, so that there is something to drag towards.
    await expect(page.getByRole("status")).toContainText("Moving 1 task");

    // The slots only exist now, and the rows moved down to make room for
    // them, so where to drag to is a question that can only be asked
    // after the press.
    const slot = page.getByRole("button", { name: /^Move here/ }).first();
    const target = await middleOf(slot);
    await finger.dragTo(target);

    // The slot under the finger says it is the one that would take the
    // row -- the whole feedback a drag has, and the thing whose absence
    // was the complaint.
    await expect(slot).toHaveClass(/reorder-slot-button-over/);

    await finger.lift();
    await expect(page.locator(".task-row .task-title").first()).toHaveText(
      before[1],
    );
    await expect(page.getByRole("status")).toHaveCount(0);

    // And it is the backlog that moved, not this screen's picture of it.
    await page.reload();
    await expect(page.locator(".task-row .task-title").first()).toHaveText(
      before[1],
    );
  });

  // The pane a tablet opens a task in is the window minus the rail --
  // 588px of an iPad Air's 820 -- and the two columns of a task detail
  // were laid out against the *window*: below 720px they stack, above
  // it they stand side by side. So this was the width that fell between
  // the two answers, and what it drew was a 260px column of metadata
  // beside 243px for the task itself: the title, the description, the
  // timeline and (on an interactive task) the whole conversation, all
  // narrower than the chips beside them. The rule reads the pane's
  // width now (style.css, 720 + the rail), and this is what says so.
  test("gives a task's own column the pane, not a third of it", async ({
    page,
  }) => {
    await page.goto("/");
    await page.locator(".task-row").first().tap();
    await expect(page.locator(".detail-layout")).toBeVisible();

    const layout = await page.locator(".detail-layout").boundingBox();
    const main = await page.locator(".detail-main").boundingBox();
    const side = await page.locator(".detail-side").boundingBox();

    expect(
      Math.round(main.width),
      `the task's own column is ${Math.round(main.width)}px of a ${Math.round(layout.width)}px pane`,
    ).toBe(Math.round(layout.width));
    // Stacked rather than squeezed: the metadata is under the task, not
    // beside it, and it is the full width there too.
    expect(Math.round(side.y)).toBeGreaterThanOrEqual(
      Math.round(main.y + main.height),
    );
    expect(Math.round(side.width)).toBe(Math.round(layout.width));
  });

  // The rail is permanently on screen here and every entry in it is
  // pressed with a finger, which the phone's own measurement cannot say
  // anything about -- there the rail is behind a button. The two
  // section headings are the ones this caught: 55x20px words that open
  // the boards list and the consoles list.
  test("gives the rail's own entries a target a thumb can hit", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    const headings = page.locator(".sidebar-section-heading");
    expect(await headings.count()).toBeGreaterThan(0);
    for (const heading of await headings.all()) {
      const box = await heading.boundingBox();
      expect(
        Math.round(box.height),
        `"${await heading.textContent()}" is ${Math.round(box.width)}x${Math.round(box.height)}px`,
      ).toBeGreaterThanOrEqual(40);
    }
  });

  // The same chat the phone measures, at the width where the bubble had
  // a whole rail's worth of room to spill into: on a tablet the pane
  // starts at 232px, so a message that overflowed to the left landed on
  // top of the rail rather than off the screen. Both halves of the fix
  // are checked here for that reason -- the bubble inside the
  // conversation, and the conversation inside the pane.
  test("keeps a chat message inside the window", async ({ page }) => {
    await openChatTask(page);

    const pane = await page.locator(".MuiDialog-paper").boundingBox();
    const list = await page.locator(".cs-message-list").boundingBox();
    const bubble = await page.locator(".cs-message").last().boundingBox();
    expect(Math.round(bubble.x)).toBeGreaterThanOrEqual(Math.round(list.x));
    expect(Math.round(bubble.x + bubble.width)).toBeLessThanOrEqual(
      Math.round(list.x + list.width),
    );
    expect(Math.round(list.x)).toBeGreaterThanOrEqual(Math.round(pane.x));

    const code = await page.evaluate(() => {
      const pre = document.querySelector(".task-chat-body pre");
      if (!pre) return null;
      pre.scrollLeft = 9999;
      return {
        clientWidth: pre.clientWidth,
        scrollWidth: pre.scrollWidth,
        scrolled: pre.scrollLeft,
      };
    });
    expect(code, "no code block in the message").toBeTruthy();
    expect(code.scrollWidth).toBeGreaterThan(code.clientWidth);
    expect(code.scrolled).toBeGreaterThan(0);
  });
});

// A finger, as Chromium's input pipeline sees one. Playwright's
// touchscreen only taps, and a touch event dispatched into the page by
// hand generates no pointer events at all -- which is precisely what the
// gesture under test listens to -- so this goes in at the same place a
// real touch does. Chromium only: newCDPSession throws on any other
// browser, which is what the skip on its one caller is about.
async function touchscreen(page) {
  const cdp = await page.context().newCDPSession(page);
  const send = (type, points) =>
    cdp.send("Input.dispatchTouchEvent", { type, touchPoints: points });
  let at = null;
  return {
    press: async (p) => {
      at = p;
      await send("touchStart", [{ x: p.x, y: p.y }]);
    },
    // In steps, the way a finger travels: the first one has to clear the
    // few pixels of slop that tell a drag from a tap, and the ones after
    // it are what a real drag would be marking a target through.
    dragTo: async (p) => {
      const from = at;
      for (let i = 1; i <= 5; i += 1) {
        const x = from.x + ((p.x - from.x) * i) / 5;
        const y = from.y + ((p.y - from.y) * i) / 5;
        await send("touchMove", [{ x, y }]);
      }
      at = p;
    },
    lift: () => send("touchEnd", []),
  };
}

async function middleOf(locator) {
  const box = await locator.boundingBox();
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 };
}
