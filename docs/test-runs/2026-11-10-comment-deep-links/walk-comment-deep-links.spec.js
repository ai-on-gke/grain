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

// Walkthrough of docs/test-plans/2026-11-10.md §5 items 1, 2 and 4 over
// `grain demo -github-sim -fake-ci`: every surface a #comment=<id> link
// can land on, navigation semantics, and focus/contrast. Run from ui/
// (copy into e2e/ first):
//   PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test \
//     e2e/walk-comment-deep-links.spec.js --project=chromium

import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-github-sim", "-fake-ci"] });
test.setTimeout(180_000);

// consoleErrors collects console.error lines and page errors.
function consoleErrors(page) {
  const errs = [];
  page.on("console", (m) => {
    if (m.type() === "error") errs.push(m.text());
  });
  page.on("pageerror", (e) => errs.push(String(e)));
  return errs;
}

async function newTask(request, data) {
  const r = await request.post("/api/tasks", {
    data: { body: "x", approved: false, ...data },
  });
  expect(r.ok()).toBe(true);
  return r.json();
}

async function comment(request, id, body) {
  const r = await request.post(`/api/tasks/${id}/comments`, {
    data: { body },
  });
  expect(r.ok()).toBe(true);
}

async function detail(request, id) {
  return (await request.get(`/api/tasks/${id}`)).json();
}

async function landed(page, selector, report, name) {
  const el = page.locator(selector).locator("visible=true").first();
  await expect(el).toBeInViewport();
  await expect(el).toHaveClass(/comment-highlight/);
  const focus = await page.evaluate((sel) => {
    const t = Array.from(document.querySelectorAll(sel)).find(
      (n) => n.getClientRects().length > 0,
    );
    const a = document.activeElement;
    return {
      focused: Boolean(t && a && (t === a || t.contains(a))),
      active: a ? `${a.tagName}.${a.className}`.slice(0, 80) : null,
    };
  }, selector);
  report.push({ name, ...focus });
}

test("item 1: every surface and malformed case", async ({ page, request }) => {
  const errs = consoleErrors(page);
  const report = [];

  // Plain timeline comment on a seeded task.
  const four = await detail(request, "4");
  const c1 = four.comments[0].id;
  await page.goto(`/tasks/4#comment=${c1}`);
  await landed(page, `[data-comment-id="${c1}"]`, report, "plain timeline");

  // Interactive task: chat preferred, history copy folded.
  const chat = await newTask(request, {
    title: "Walk chat",
    interactive: true,
  });
  for (let i = 0; i < 200; i++) await comment(request, chat.id, `Line ${i}`);
  const cd = await detail(request, chat.id);
  await page.goto(`/tasks/${chat.id}#comment=${cd.comments[1].id}`);
  await landed(
    page,
    `.task-chat-window [data-comment-id="${cd.comments[1].id}"]`,
    report,
    "interactive chat, 200 lines, 2nd",
  );

  // Director session, by its task path and by its session page.
  const sessions = await (await request.get("/api/console-sessions")).json();
  const dirTask = sessions.sessions.find((s) => s.id === "director").taskId;
  const dd = await detail(request, dirTask);
  const dc = dd.comments[0].id;
  await page.goto(`/tasks/${dirTask}#comment=${dc}`);
  await landed(
    page,
    `[data-comment-id="${dc}"]`,
    report,
    "director via /tasks",
  );
  await page.goto(`/session/director#comment=${dc}`);
  await landed(
    page,
    `[data-comment-id="${dc}"]`,
    report,
    "director via /session",
  );

  // Another task's comment id.
  const seven = await detail(request, "7");
  const foreign = seven.comments[0].id;
  await page.goto(`/tasks/4#comment=${foreign}`);
  await expect(page.getByTestId("comment-link-notice")).toContainText(
    `Comment ${foreign} is not in this task's conversation.`,
  );

  // Non-existent.
  await page.goto(`/tasks/4#comment=99999999`);
  await expect(page.getByTestId("comment-link-notice")).toContainText(
    "Comment 99999999 is not in this task's conversation.",
  );

  // Malformed fragments.
  const malformed = {};
  for (const h of [
    "#comment=abc",
    "#comment=",
    "#comment=12abc",
    "#comment=-1",
  ]) {
    await page.goto(`/tasks/4${h}`);
    await page.waitForTimeout(2500);
    malformed[h] = await page
      .getByTestId("comment-link-notice")
      .allInnerTexts();
  }

  console.log("FOCUS", JSON.stringify(report, null, 1));
  console.log("MALFORMED", JSON.stringify(malformed));
  console.log("ERRORS", JSON.stringify(errs));
});

test("item 2: back/forward, modified clicks, cross-task", async ({
  page,
  request,
  context,
}) => {
  const errs = consoleErrors(page);
  const target = await newTask(request, { title: "Walk target" });
  for (let i = 0; i < 40; i++) await comment(request, target.id, `T ${i}`);
  const td = await detail(request, target.id);
  const tc = td.comments[3].id;
  const source = await newTask(request, { title: "Walk source" });
  await comment(
    request,
    source.id,
    `See [cross](/tasks/${target.id}#comment=${tc}) and [same](/tasks/${source.id}).`,
  );

  await page.goto(`/tasks/${source.id}`);
  await page.evaluate(() => (window.__noReload = true));
  await page.getByRole("link", { name: "cross" }).click();
  await expect(page).toHaveURL(
    new RegExp(`/tasks/${target.id}#comment=${tc}$`),
  );
  await expect(page.locator(`[data-comment-id="${tc}"]`)).toBeInViewport();

  await page.goBack();
  await expect(page).toHaveURL(new RegExp(`/tasks/${source.id}$`));
  await expect(page.getByRole("link", { name: "cross" })).toBeVisible();
  const backTitle = await page.locator(".detail-header").first().innerText();

  await page.goForward();
  await expect(page).toHaveURL(
    new RegExp(`/tasks/${target.id}#comment=${tc}$`),
  );
  const fwdEntry = page.locator(`[data-comment-id="${tc}"]`);
  await expect(fwdEntry).toBeInViewport();
  const fwdHighlighted = await fwdEntry
    .evaluate((el) => el.classList.contains("comment-highlight"))
    .catch(() => false);
  expect(await page.evaluate(() => window.__noReload)).toBe(true);

  // Modified clicks open a new tab and leave this one alone.
  await page.goto(`/tasks/${source.id}`);
  const link = page.getByRole("link", { name: "cross" });
  const tabs = {};
  for (const mod of ["Control", "Shift", "Meta", "middle"]) {
    await page.goto(`/tasks/${source.id}`);
    const popup = context
      .waitForEvent("page", { timeout: 5000 })
      .catch(() => null);
    if (mod === "middle") await link.click({ button: "middle" });
    else await link.click({ modifiers: [mod] });
    const p = await popup;
    await page.waitForTimeout(500);
    tabs[mod] = { newTab: p ? p.url() : null, here: page.url() };
    if (p) await p.close();
  }

  console.log(
    "BACK",
    backTitle.slice(0, 80),
    "FWD highlighted",
    fwdHighlighted,
  );
  console.log("TABS", JSON.stringify(tabs));
  console.log("ERRORS", JSON.stringify(errs));
});

test("item 4: highlight contrast in both themes", async ({ page, request }) => {
  const t = await newTask(request, { title: "Walk contrast" });
  for (let i = 0; i < 10; i++)
    await comment(request, t.id, `Contrast line ${i}`);
  const d = await detail(request, t.id);
  const id = d.comments[5].id;
  const out = {};
  for (const scheme of ["light", "dark"]) {
    await page.emulateMedia({ colorScheme: scheme });
    await page.goto(`/tasks/${t.id}#comment=${id}`);
    const el = page.locator(`[data-comment-id="${id}"]`);
    await expect(el).toHaveClass(/comment-highlight/);
    await page.waitForTimeout(300);
    out[scheme] = await el.evaluate((node) => {
      const cs = getComputedStyle(node);
      let p = node.parentElement;
      let bg = "rgba(0, 0, 0, 0)";
      while (p) {
        const b = getComputedStyle(p).backgroundColor;
        if (b && b !== "rgba(0, 0, 0, 0)" && b !== "transparent") {
          bg = b;
          break;
        }
        p = p.parentElement;
      }
      const text = node.querySelector(".markdown-body, p") || node;
      return {
        background: cs.backgroundColor,
        ring: cs.boxShadow,
        surface: bg,
        text: getComputedStyle(text).color,
        theme: document.documentElement.dataset.theme || null,
      };
    });
  }
  console.log("CONTRAST", JSON.stringify(out, null, 1));
});

for (const [name, size] of Object.entries({
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
})) {
  test(`item 4: highlight, focus and shots (${name})`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize(size);
    const errs = consoleErrors(page);
    const t = await newTask(request, { title: `Walk shots ${name}` });
    for (let i = 0; i < 30; i++)
      await comment(request, t.id, `Shot line ${i} with some words in it.`);
    const d = await detail(request, t.id);
    const id = d.comments[10].id;
    const res = {};
    for (const scheme of ["light", "dark"]) {
      await page.emulateMedia({ colorScheme: scheme });
      await page.goto("about:blank");
      await page.goto(`/tasks/${t.id}#comment=${id}`);
      const el = page.locator(`[data-comment-id="${id}"]`);
      await expect(el).toHaveClass(/comment-highlight/);
      await expect(el).toBeFocused();
      await page.waitForTimeout(250);
      const box = await el.boundingBox();
      // Not under a sticky header or the phone's bottom bar: the point
      // at the comment's centre is the comment.
      const hit = await page.evaluate(
        ([x, y, cid]) =>
          Boolean(
            document
              .elementFromPoint(x, y)
              ?.closest(`[data-comment-id="${cid}"]`),
          ),
        [box.x + box.width / 2, box.y + box.height / 2, id],
      );
      await page.screenshot({
        path: `/tmp/shots/highlight-${name}-${scheme}.png`,
      });
      res[scheme] = { box, hit };
    }
    console.log("SHOT", name, JSON.stringify(res), "ERRORS", JSON.stringify(errs));
  });
}
