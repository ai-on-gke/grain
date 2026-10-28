import { expect, test } from "./fixtures.js";

// docs/test-plans/2026-10-02.md §8, on `candidate`: the keyboard row
// opening and the 2026-10-01 walkthrough fixes, run together. Findings
// and screenshots are in
// docs/test-runs/2026-10-02-candidate-keyboard-and-walkthrough-fixes.md.
//
// keyboard-open-rows.spec.js already walks to a row on each list with
// Tab and opens it with Enter. This adds what that never asserted: the
// ring a sighted keyboard user sees, the name a screen reader announces,
// and where focus is once Back has brought the list back -- which, for a
// board or a repo, was <body> (grain/task-1580).

test.describe.configure({ mode: "serial" });
test.setTimeout(180_000);

const seeded = [];

async function seed(page, path, data) {
  const res = await page.request.post(path, { data });
  expect(res.ok(), `POST ${path}: ${res.status()} ${await res.text()}`).toBe(
    true,
  );
  const body = await res.json();
  seeded.push(`${path}/${body.id}`);
  return body;
}

test.afterAll(async ({ request }) => {
  while (seeded.length > 0) await request.delete(seeded.pop());
});

async function tabTo(page, target, limit = 300) {
  await page.evaluate(() => document.activeElement?.blur());
  for (let i = 0; i < limit; i++) {
    await page.keyboard.press("Tab");
    if (await target.evaluate((el) => el === document.activeElement)) return;
  }
  throw new Error(`Tab never reached ${target}`);
}

// A ring a keyboard user can see: :focus-visible matched, and an outline
// actually drawn rather than suppressed.
async function expectRing(target) {
  await expect(target).toBeFocused();
  const ring = await target.evaluate((el) => ({
    visible: el.matches(":focus-visible"),
    style: getComputedStyle(el).outlineStyle,
    width: parseFloat(getComputedStyle(el).outlineWidth),
  }));
  expect(ring.visible).toBe(true);
  expect(ring.style).not.toBe("none");
  expect(ring.width).toBeGreaterThan(0);
}

test("every list opens a row from the keyboard and Back returns focus to it", async ({
  page,
}) => {
  const stamp = Date.now();
  const kanban = await seed(page, "/api/boards", {
    name: `KW kanban ${stamp}`,
    kind: "kanban",
    query: "",
  });
  const timeline = await seed(page, "/api/boards", {
    name: `KW timeline ${stamp}`,
    kind: "gantt",
    query: "",
  });
  const template = await seed(page, "/api/templates", {
    name: `KW template ${stamp}`,
    title: "Run benchmarks",
  });
  const suite = await seed(page, "/api/suites", {
    name: `KW suite ${stamp}`,
    templateIds: [template.id],
    mode: "count",
    count: 1,
  });
  await seed(page, "/api/schedules", {
    title: `KW schedule ${stamp}`,
    repo: "acme/widgets",
    recurrence: { kind: "everyNHours", everyNHours: 24 },
  });
  const run = await page.request.post("/api/suite-runs", {
    data: { suiteId: suite.id, repo: "acme/widgets", base: "main" },
  });
  expect(run.ok()).toBe(true);

  // Each row the way assistive technology finds it -- by role and the
  // name the row shows -- then opened with a key, and Back.
  const task = "Add a dark mode toggle";
  const cases = [
    {
      list: "/",
      row: (p) =>
        p.locator(".task-list").getByRole("link", { name: task, exact: true }),
      opens: /\/tasks\/\d+$/,
      key: "Enter",
    },
    {
      list: `/boards/${kanban.id}`,
      row: (p) =>
        p
          .locator(".board-card")
          .getByRole("link", { name: task, exact: true })
          .first(),
      opens: /\/tasks\/\d+$/,
      key: "Enter",
    },
    {
      list: `/boards/${timeline.id}`,
      row: (p) =>
        p
          .locator(".gantt-row")
          .getByRole("link", { name: task, exact: true })
          .first(),
      opens: /\/tasks\/\d+$/,
      key: "Enter",
    },
    {
      list: "/boards",
      row: (p) => p.getByRole("link", { name: `KW kanban ${stamp}` }),
      opens: new RegExp(`/boards/${kanban.id}$`),
      key: "Enter",
      back: "← Boards",
    },
    {
      list: "/processes?kind=template",
      row: (p) => p.getByRole("link", { name: `KW template ${stamp}` }),
      opens: new RegExp(`/processes/templates/${template.id}$`),
      key: "Enter",
    },
    {
      list: "/processes?kind=suite",
      row: (p) =>
        p
          .locator(".process-row-suite")
          .getByRole("link", { name: `KW suite ${stamp}` }),
      opens: new RegExp(`/processes/suites/${suite.id}$`),
      key: "Enter",
    },
    {
      list: "/processes?kind=suite",
      row: (p) =>
        p
          .locator(".suite-run-row")
          .getByRole("link", { name: `KW suite ${stamp}` }),
      opens: /\/processes\/suites\/runs\/\d+$/,
      key: "Enter",
    },
    {
      list: "/processes?kind=schedule",
      row: (p) => p.getByRole("link", { name: `KW schedule ${stamp}` }),
      opens: /\/processes\/schedules\/[^/]+$/,
      key: "Enter",
    },
    {
      list: "/repos",
      row: (p) => p.getByRole("link", { name: "acme/widgets" }),
      opens: /\/repos\/acme\/widgets/,
      key: "Enter",
      back: "← Repos",
    },
  ];

  for (const c of cases) {
    await test.step(`${c.list} ${c.key}`, async () => {
      await page.goto(c.list);
      const row = c.row(page);
      await expect(row).toBeVisible();
      await tabTo(page, row);
      await expectRing(row);

      await page.keyboard.press(c.key);
      await expect(page).toHaveURL(c.opens);
      await page.goBack();
      await expect(page).toHaveURL(
        new RegExp(`${c.list.replace("?", "\\?")}$`),
      );
      await expectRing(c.row(page));

      // A page that replaced the list has its own way back, which a
      // keyboard user reaches and presses too.
      if (c.back) {
        await page.keyboard.press(c.key);
        await expect(page).toHaveURL(c.opens);
        const back = page.getByRole("button", { name: c.back });
        await tabTo(page, back);
        await page.keyboard.press("Enter");
        await expect(page).toHaveURL(new RegExp(`${c.list}$`));
        await expectRing(c.row(page));
      }
    });
  }
});

// f7b4a43ed refuses a base branch no run could check out -- by its
// shape. A well-formed name that is simply not on the repo
// (`no-such-branch`) is still accepted by every writer; see the report.
test("task, schedule and template forms say what is wrong with an unusable base branch", async ({
  page,
}) => {
  const stamp = Date.now();
  const bad = "no such branch ..lock";
  const message = `base "${bad}" is not a usable branch name`;
  const submit = async (path, button, method = "POST") => {
    const answer = page.waitForResponse(
      (r) => r.url().endsWith(path) && r.request().method() === method,
    );
    await page.getByRole("button", { name: button, exact: true }).click();
    return answer;
  };

  // The task form.
  await page.goto("/");
  await page.getByRole("button", { name: "+ New task" }).click();
  await page.getByLabel("Title").fill(`KW base ${stamp}`);
  await page.getByLabel(/^Repos/).selectOption("acme/widgets");
  await page.getByLabel("Base branch").fill("no-such-branch");
  let res = await submit("/api/tasks", "Create task");
  expect(res.status()).toBe(201);
  seeded.push(`/api/tasks/${(await res.json()).id}`);
  await page.getByRole("button", { name: "+ New task" }).click();
  await page.getByLabel("Title").fill(`KW base ${stamp}`);
  await page.getByLabel(/^Repos/).selectOption("acme/widgets");
  await page.getByLabel("Base branch").fill(bad);
  res = await submit("/api/tasks", "Create task");
  expect(res.status()).toBe(400);
  await expect(page.getByRole("alert", { includeHidden: true })).toContainText(
    message,
  );
  await page.keyboard.press("Escape");

  // The schedule form.
  await page.goto("/schedules");
  await page.getByRole("button", { name: /New schedule/ }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel("Title").fill(`KW base schedule ${stamp}`);
  await dialog.getByLabel("Base branch").fill(bad);
  res = await submit("/api/schedules", "Add schedule");
  expect(res.status()).toBe(400);
  await expect(page.getByRole("alert", { includeHidden: true })).toContainText(
    message,
  );
  await expect(dialog.getByLabel("Base branch")).toHaveValue(bad);
  await page.keyboard.press("Escape");

  // The template form. The schedule form is still open: it has been
  // edited, so Escape only asked, and Playwright dismisses that question.
  // Its debounced composition preview may still be in flight, and
  // WebKit reports a fetch that page.goto cancels as an uncaught page
  // error ("Fetch API cannot load … due to access control checks"), so
  // let it land first.
  await page.waitForLoadState("networkidle");
  await page.goto("/templates");
  await page.getByRole("button", { name: /New template/ }).click();
  const form = page.getByRole("dialog");
  await form.getByLabel(/^Name/).fill(`kw-base-${stamp}`);
  await form.getByLabel("Task title").fill("Bump the toolchain");
  await form
    .locator('select[name="repo"]')
    .first()
    .selectOption("acme/widgets");
  await form.getByLabel("Base branch").fill(bad);
  res = await submit("/api/templates", "Add template");
  expect(res.status()).toBe(400);
  await expect(page.getByRole("alert", { includeHidden: true })).toContainText(
    message,
  );
  await expect(form.getByLabel("Base branch")).toHaveValue(bad);
});

// 5c08a7ad4: a link to a schedule that has since been deleted -- taken
// from a board, here -- says so, and Back returns to the board.
test("a link to a deleted schedule, followed from a board, is reported and Back returns to the board", async ({
  page,
}) => {
  const stamp = Date.now();
  const board = await seed(page, "/api/boards", {
    name: `KW stale ${stamp}`,
    kind: "list",
    query: "",
  });
  const schedule = await seed(page, "/api/schedules", {
    title: `KW doomed ${stamp}`,
    repo: "acme/widgets",
    recurrence: { kind: "everyNHours", everyNHours: 24 },
  });
  expect(
    (await page.request.delete(`/api/schedules/${schedule.id}`)).ok(),
  ).toBe(true);

  await page.goto(`/boards/${board.id}`);
  await expect(
    page.getByRole("heading", { name: `KW stale ${stamp}` }),
  ).toBeVisible();
  await page.evaluate((href) => {
    history.pushState(null, "", href);
    dispatchEvent(new PopStateEvent("popstate"));
  }, `/schedules/${schedule.id}`);
  await expect(page.getByText(`no schedule "${schedule.id}"`)).toBeVisible();
  await expect(page).toHaveURL(/\/processes(\?kind=schedule)?$/);
  await page.goBack();
  await expect(page).toHaveURL(new RegExp(`/boards/${board.id}$`));
});

// 280e9c2fb: the deployment's default model applies only to a task that
// named none, and the pane says so. Templates carry no model of their
// own, so "a template naming one" is a task filed from a template that
// names one. The pane still words it "team default": the deployment's
// model lives on the default team's row until docs/single-tenant-reset.md
// stage 3e-iv.
test("the deployment's default model shows on a task that named none, and only there", async ({
  page,
}) => {
  const stamp = Date.now();
  const before = await (await page.request.get("/api/deployment")).json();
  const patched = await page.request.put("/api/deployment", {
    data: { defaultModel: "gemini-2.5-flash" },
  });
  expect(patched.ok(), await patched.text()).toBe(true);
  try {
    await expectDefaultModelRows(page, stamp);
  } finally {
    const restored = await page.request.put("/api/deployment", {
      data: { defaultModel: before.defaultModel || "" },
    });
    expect(restored.ok(), await restored.text()).toBe(true);
  }
});

async function expectDefaultModelRows(page, stamp) {
  const template = await seed(page, "/api/templates", {
    name: `kw-models-${stamp}`,
    title: "From a template",
  });
  const file = (data) =>
    seed(page, "/api/tasks", {
      repo: "acme/widgets",
      description: "d",
      ...data,
    });
  const none = await file({ title: `KW none ${stamp}` });
  const pinned = await file({
    title: `KW pinned ${stamp}`,
    agentBackendId: "gemini-pro",
  });
  const fromTemplate = await file({
    templateId: template.id,
    agentBackendId: "gemini-pro",
  });

  // The model and the pinned connection are separate rows.
  const declared = async (id, key) => {
    await page.goto(`/tasks/${id}`);
    const row = page.locator(".declared-row", {
      has: page.locator(".declared-key", { hasText: new RegExp(`^${key}$`) }),
    });
    await expect(row).toBeVisible();
    return (await row.locator(".declared-value").textContent()).trim();
  };
  expect(await declared(none.id, "Model")).toBe(
    "deployment default (gemini-2.5-flash)",
  );
  expect(await declared(pinned.id, "Model")).not.toContain(
    "deployment default",
  );
  expect(await declared(pinned.id, "Connection")).toContain("Gemini 2.5 Pro");
  expect(await declared(fromTemplate.id, "Connection")).toContain(
    "Gemini 2.5 Pro",
  );
  expect(await declared(fromTemplate.id, "Model")).not.toContain(
    "deployment default",
  );
}
