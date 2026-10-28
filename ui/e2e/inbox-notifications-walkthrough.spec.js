import { test, expect } from "./fixtures.js";

// docs/test-plans/2026-10-06.md §6 step 1, in a real browser on the real
// `grain demo` server (docs/test-runs/2026-10-07-inbox-notifications-task-pane.md):
// the inbox's desktop notifications (inboxNotifications.js, grain/task-2324)
// had only ever been exercised in jsdom against a hand-written Notification.
//
// Nothing of grain's is stubbed. The browser's own Notification constructor
// is wrapped by an init script that records each call -- title, body, tag --
// into window.__notes and still constructs the real one, which is
// instrumentation of a browser API rather than a mock of anything grain
// owns. Arrivals are made through grain's own routes: POST /api/tasks files
// a proposal, and POST /api/tasks/{id}/messages is the endpoint a run's
// chat tool parks a question through (mcp.NewChatTools); a person's reply
// is the ordinary comment route.

const RECORD_NOTIFICATIONS = () => {
  window.__notes = [];
  const Real = window.Notification;
  if (!Real) return;
  class Recorded extends Real {
    constructor(title, options = {}) {
      super(title, options);
      window.__notes.push({
        title,
        body: options.body || "",
        tag: options.tag || "",
      });
      // Kept so the test can fire the one grain hung its click on.
      (window.__noteObjects ||= []).push(this);
    }
  }
  window.Notification = Recorded;
};

const notes = (page) => page.evaluate(() => window.__notes.slice());

// POLL is a little over two of App.jsx's POLL_INTERVAL_MS, for a page
// that has to have polled at least once since the store changed.
const POLL = 7_000;

async function propose(request, title) {
  const res = await request.post("/api/tasks", { data: { title } });
  expect(res.ok(), await res.text()).toBe(true);
  return res.json();
}

// inBackground opens a second tab and brings it to the front, which is
// what leaves the inbox's tab unfocused (document.hasFocus() false) in a
// real browser -- the state the notifications exist for. Playwright
// otherwise tells every page it has focus (CDP focus emulation), which
// would make "in the background" unreachable; turning that emulation off
// leaves the browser's own answer.
async function inBackground(context, page) {
  const cdp = await context.newCDPSession(page);
  await cdp.send("Emulation.setFocusEmulationEnabled", { enabled: false });
  const other = await context.newPage();
  await other.goto("about:blank");
  await other.bringToFront();
  await expect.poll(() => page.evaluate(() => document.hasFocus())).toBe(false);
  return other;
}

// The new headless Chromium rather than Playwright's default headless
// shell: the shell answers Notification.permission with "denied" whatever
// the context was granted, so it can only ever show the blocked line.
test.use({ channel: "chromium" });

test.describe("Inbox desktop notifications", () => {
  test.use({ permissions: ["notifications"] });

  test("baseline, focus, arrivals, summary, a returning question, click-through and the off switch", async ({
    page,
    context,
  }) => {
    // Several of the steps below wait out a poll on purpose, to show
    // that nothing was shown.
    test.setTimeout(120_000);
    await context.addInitScript(RECORD_NOTIFICATIONS);
    const stamp = Date.now();

    // The seeded inbox is the baseline: a parked question, a proposal, a
    // failure, and more -- and none of it is announced.
    await page.goto("/inbox");
    await expect(
      page.locator(".inbox-item", {
        hasText: "Investigate the merge queue stall",
      }),
    ).toBeVisible();
    // Granted permission and no "off" said here: on already.
    await expect(
      page.getByRole("button", { name: "Turn off notifications" }),
    ).toBeVisible();
    await page.waitForTimeout(POLL);
    expect(await notes(page)).toEqual([]);

    // In front of the reader, an arrival is the rail's news, not a toast.
    const front = await propose(page.request, `E2E notify focused ${stamp}`);
    await expect(
      page.locator(".inbox-item", { hasText: front.title }),
    ).toBeVisible();
    await page.waitForTimeout(POLL);
    expect(await notes(page)).toEqual([]);

    // Behind another tab: one notification per arrival.
    const other = await inBackground(context, page);
    const one = await propose(page.request, `E2E notify one ${stamp}`);
    await expect.poll(() => notes(page), { timeout: 15_000 }).toHaveLength(1);
    expect((await notes(page))[0]).toEqual({
      title: `${one.id} was proposed and needs approval`,
      body: one.title,
      tag: `task:${one.id}:proposed`,
    });

    // Four together collapse into one summary.
    const four = [];
    for (let i = 0; i < 4; i++)
      four.push(await propose(page.request, `E2E notify burst ${i} ${stamp}`));
    await expect.poll(() => notes(page), { timeout: 15_000 }).toHaveLength(2);
    const summary = (await notes(page))[1];
    expect(summary.title).toBe("4 new items in your inbox");
    expect(summary.tag).toBe("grain-inbox-summary");
    expect(summary.body.split("\n")).toHaveLength(3);

    // A task that asks, is answered, and asks again is news both times.
    const asker = four[0];
    await page.request.post(`/api/tasks/${asker.id}/approve`);
    let res = await page.request.post(`/api/tasks/${asker.id}/messages`, {
      data: { body: "Which branch should this go on?" },
    });
    expect(res.ok(), await res.text()).toBe(true);
    await expect.poll(() => notes(page), { timeout: 15_000 }).toHaveLength(3);
    expect((await notes(page))[2].tag).toBe(`task:${asker.id}:reply`);
    res = await page.request.post(`/api/tasks/${asker.id}/comments`, {
      data: { body: "main" },
    });
    expect(res.ok(), await res.text()).toBe(true);
    // Let the page see it leave before it comes back.
    await page.waitForTimeout(POLL);
    expect(await notes(page)).toHaveLength(3);
    res = await page.request.post(`/api/tasks/${asker.id}/messages`, {
      data: {
        body: "And which reviewer?",
        summary: "Pick a reviewer for this change",
      },
    });
    expect(res.ok(), await res.text()).toBe(true);
    await expect.poll(() => notes(page), { timeout: 15_000 }).toHaveLength(4);
    // A question is announced by its own one-line summary.
    expect((await notes(page))[3]).toMatchObject({
      title: "Pick a reviewer for this change",
      tag: `task:${asker.id}:reply`,
    });

    // Clicking it brings the tab forward and opens the task.
    await page.evaluate(() => window.__noteObjects.at(-1).onclick());
    await expect(page).toHaveURL(new RegExp(`/tasks/${asker.id}$`));
    await expect(page.locator(".detail-header h2")).toContainText(asker.title);
    // window.focus() from a script, rather than from the browser's own
    // notification click, is not a user activation, so whether the tab
    // came forward is recorded here rather than asserted.
    console.log(
      `focused after click: ${await page.evaluate(() => document.hasFocus())}`,
    );
    console.log(`notifications: ${JSON.stringify(await notes(page))}`);
    await other.close();
    await page.bringToFront();

    // The off switch is kept across a reload, and holds: an arrival in the
    // background shows nothing.
    await page.goto("/inbox");
    await page.getByRole("button", { name: "Turn off notifications" }).click();
    await expect(
      page.getByRole("button", { name: "Turn on notifications" }),
    ).toBeVisible();
    await page.reload();
    await expect(
      page.getByRole("button", { name: "Turn on notifications" }),
    ).toBeVisible();
    expect(
      await page.evaluate(() =>
        localStorage.getItem("grain.inboxNotifications"),
      ),
    ).toBe("off");
    const away = await inBackground(context, page);
    await propose(page.request, `E2E notify while off ${stamp}`);
    await page.waitForTimeout(POLL);
    expect(await notes(page)).toEqual([]);
    await away.close();
  });

  test("a summary's click opens the inbox", async ({ page, context }) => {
    await context.addInitScript(RECORD_NOTIFICATIONS);
    const stamp = Date.now();
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();
    const other = await inBackground(context, page);
    for (let i = 0; i < 5; i++)
      await propose(page.request, `E2E notify summary ${i} ${stamp}`);
    await expect.poll(() => notes(page), { timeout: 15_000 }).toHaveLength(1);
    expect((await notes(page))[0].title).toBe("5 new items in your inbox");
    await page.evaluate(() => window.__noteObjects.at(-1).onclick());
    await expect(page).toHaveURL(/\/inbox$/);
    await other.close();
  });
});

test.describe("Inbox notifications when the site is blocked", () => {
  test("says so rather than offering a button that cannot work", async ({
    page,
  }) => {
    // Playwright cannot grant "denied", so the browser's own answer is
    // read through Notification.permission the page sees: a getter on
    // the real constructor, overridden to the value a reader who blocked
    // the site would leave there.
    await page.addInitScript(() => {
      Object.defineProperty(window.Notification, "permission", {
        get: () => "denied",
      });
    });
    await page.goto("/inbox");
    const blocked = page.getByText("Notifications blocked");
    await expect(blocked).toBeVisible();
    await expect(blocked).toHaveAttribute(
      "title",
      "Allow notifications for this site in the browser's settings to turn them on.",
    );
    await expect(
      page.getByRole("button", { name: /notifications/ }),
    ).toHaveCount(0);
  });
});
