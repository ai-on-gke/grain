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

// The write controls a signed-in grain/viewer is offered (src/access.js,
// AccessGate.jsx), in a real browser over `grain demo`. The demo signs in
// with the access code, which is unlimited, so the page alone is told it
// is a viewer: GET /api/config's `access` is replaced on this page's own
// requests (page.route, so nothing shared with the specs beside it
// changes). What is worth a browser here is the layout -- a refused
// button is drawn inside a wrapper that carries its tooltip, and that
// wrapper must keep the button where it was -- and the tooltip itself.
const VIEWER = {
  role: "grain/viewer",
  roles: ["grain/viewer"],
  full: false,
  scopes: [
    "boards:read",
    "ci:read",
    "repos:read",
    "schedules:read",
    "settings:read",
    "suites:read",
    "tasks:read",
    "templates:read",
  ],
};

// rewriteJSON intercepts a GET route and rewrites its JSON body. A request
// still in flight when the test navigates away or closes its page is
// cancelled with the page, disposing the response ("Response has been
// disposed"); nobody is waiting on that answer any more, so let it go.
async function rewriteJSON(route, transform, fetchOptions) {
  if (route.request().method() !== "GET") return route.fallback();
  let response, body;
  try {
    response = await route.fetch(fetchOptions);
    body = await response.json();
  } catch (err) {
    if (/disposed|closed|cancel/i.test(String(err))) return;
    throw err;
  }
  await route.fulfill({ response, json: transform(body) }).catch((err) => {
    if (!/disposed|closed|cancel|handled/i.test(String(err))) throw err;
  });
}

async function asViewer(page) {
  await page.route(
    (u) => u.pathname === "/api/config",
    async (route) => {
      const headers = { ...route.request().headers() };
      delete headers["if-none-match"];
      await rewriteJSON(route, (body) => ({ ...body, access: VIEWER }), {
        headers,
      });
    },
  );
}

// withProposal makes sure the Inbox has a proposal to answer, whatever the
// specs beside this one have done to the shared demo's: GET /api/tasks on
// this page alone carries one more task, in state proposed.
async function withProposal(page) {
  await page.route(
    (u) => u.pathname === "/api/tasks",
    async (route) => {
      const headers = { ...route.request().headers() };
      delete headers["if-none-match"];
      await rewriteJSON(
        route,
        (body) => {
          const list = Array.isArray(body) ? body : body.tasks || [];
          const extra = {
            ...(list[0] || {}),
            id: "9901",
            title: "Role-gated proposal",
            state: "proposed",
            approved: false,
          };
          return Array.isArray(body)
            ? [extra, ...list]
            : { ...body, tasks: [extra, ...list] };
        },
        { headers },
      );
    },
  );
}

// expectGatesFilled checks every refused control on the page fills the
// wrapper AccessGate draws it in. A wrapper taller than its control is a
// sibling pushed down (the task pane's Admission and Hosting kept margins
// a Stack had zeroed) or a button left at the top of a row that stretched
// its neighbours (docs/test-runs/2026-11-07-rbac-roles-ui.md U2, U3).
async function expectGatesFilled(page, within = page.locator("body")) {
  const gates = await within.locator(".access-gate:visible").all();
  expect(gates.length).toBeGreaterThan(0);
  for (const gate of gates) {
    const g = await gate.boundingBox();
    const c = await gate.locator("> *").first().boundingBox();
    const name = (await gate.innerText()).trim().slice(0, 40);
    expect(Math.abs(g.height - c.height), `${name} height`).toBeLessThan(1.5);
    expect(Math.abs(g.width - c.width), `${name} width`).toBeLessThan(1.5);
    expect(Math.abs(g.y - c.y), `${name} top`).toBeLessThan(1.5);
  }
}

const proposalRow = (page) =>
  page
    .locator(".inbox-group", { has: page.getByText("Approve a proposal") })
    .locator(".inbox-item", { hasText: "Role-gated proposal" });

const newBoardButton = (page) =>
  page.getByTestId("page-header").getByRole("button", { name: "New board" });

test("a viewer's New board is disabled, keeps its place, and says why", async ({
  page,
}) => {
  await asViewer(page);
  await page.goto("/boards");
  const button = newBoardButton(page);
  await expect(button).toBeDisabled();

  // Still at the right-hand end of the header, where the wrapper's own
  // margin puts it now that the button's is inside it.
  const header = await page.getByTestId("page-header").boundingBox();
  const box = await button.boundingBox();
  expect(box.x + box.width).toBeGreaterThan(header.x + header.width / 2);
  expect(box.x + box.width).toBeLessThanOrEqual(header.x + header.width + 1);

  await button.locator("xpath=..").hover();
  await expect(page.getByRole("tooltip")).toHaveText(
    "Needs tasks:write, which your role grain/viewer does not grant.",
  );
});

test("a viewer's task Edit is disabled beside Prompt, which still opens", async ({
  page,
}) => {
  await asViewer(page);
  await page.goto("/tasks/1");
  const edit = page.getByRole("button", { name: "Edit", exact: true });
  const prompt = page.getByRole("button", { name: "Prompt", exact: true });
  await expect(edit).toBeDisabled();
  await expect(prompt).toBeEnabled();
  // One row: the wrapper does not drop Edit onto a line of its own.
  const e = await edit.boundingBox();
  const p = await prompt.boundingBox();
  expect(Math.abs(e.y + e.height / 2 - (p.y + p.height / 2))).toBeLessThan(4);
  expect(e.x).toBeGreaterThan(p.x);
});

test("a viewer sees a settings section read-only, and why", async ({
  page,
}) => {
  await asViewer(page);
  await page.goto("/settings/agents");
  await expect(page.getByTestId("settings-role-read-only")).toContainText(
    "Needs settings:write, which your role grain/viewer does not grant.",
  );
  await expect(
    page.getByLabel("Standing instructions for every run"),
  ).toBeDisabled();
});

// The Inbox's row answers were the one write surface left ungated: a
// viewer (or a contributor, who lacks tasks:admin) was offered Approve and
// its click ended in a 403 toast (docs/test-runs/2026-11-07-rbac-roles.md
// F6).
test("a viewer's Inbox Approve is disabled, stays on its row, and says why", async ({
  page,
}) => {
  await asViewer(page);
  await withProposal(page);
  await page.goto("/inbox");
  const row = proposalRow(page);
  const approve = row.getByRole("button", { name: "Approve", exact: true });
  await expect(approve).toBeDisabled();
  await expect(row.getByRole("button", { name: "Defer" })).toBeDisabled();
  const r = await row.boundingBox();
  const a = await approve.boundingBox();
  expect(a.y).toBeGreaterThanOrEqual(r.y - 1);
  expect(a.y + a.height).toBeLessThanOrEqual(r.y + r.height + 1);
  expect(a.x + a.width).toBeLessThanOrEqual(r.x + r.width + 1);
  await approve.locator("xpath=..").hover();
  await expect(page.getByRole("tooltip")).toHaveText(
    "Needs tasks:admin, which your role grain/viewer does not grant.",
  );
});

// New task was offered to a viewer on every page -- the rail's split
// button, the phone bar's "+" and the form's Create -- and filing ended in
// a 403 toast (docs/test-runs/2026-11-07-rbac-roles.md F7).
test("a viewer's New task is disabled in the rail and on the form, and says why", async ({
  page,
}) => {
  await asViewer(page);
  await page.goto("/");
  const rail = page.getByRole("button", { name: "+ New task" });
  await expect(rail).toBeDisabled();
  // The wrapper keeps the split button one row with its dropdown half.
  const options = page.getByRole("button", { name: "Create options" });
  const r = await rail.boundingBox();
  const o = await options.boundingBox();
  expect(Math.abs(r.y - o.y)).toBeLessThan(2);
  expect(r.x + r.width).toBeLessThanOrEqual(o.x + 1);
  expect(r.width).toBeGreaterThan(o.width);
  await rail.locator("xpath=..").hover();
  await expect(page.getByRole("tooltip")).toHaveText(
    "Needs tasks:write, which your role grain/viewer does not grant.",
  );

  await page.goto("/new");
  await expect(
    page.getByRole("button", { name: "Create task" }),
  ).toBeDisabled();
});

// Dragging a row reorders the queue (POST /api/tasks/reorder, tasks:write).
// A viewer was offered the handles and the drag, and the drop ended in a
// 403 toast (docs/test-runs/2026-11-07-rbac-roles-ui.md U1).
test("a viewer is offered no reorder handles on the task list", async ({
  page,
}) => {
  await page.goto("/");
  // The access code is unlimited: the demo's list offers the handles.
  await expect(page.locator("main [draggable=true]").first()).toBeVisible();
  await asViewer(page);
  await page.reload();
  await expect(page.locator("main .task-row").first()).toBeVisible();
  await expect(page.locator("main [draggable=true]")).toHaveCount(0);
  await expect(page.getByRole("button", { name: /^Move task / })).toHaveCount(
    0,
  );
});

test("a viewer is offered no drag on a kanban board's cards", async ({
  page,
}) => {
  const res = await page.request.get("/api/boards");
  const board = (await res.json()).find((b) => b.name === "Board");
  expect(board, "the demo's kanban board").toBeTruthy();
  await page.goto(`/boards/${board.id}`);
  await expect(
    page.locator(".board-column [draggable=true]").first(),
  ).toBeVisible();
  await asViewer(page);
  await page.reload();
  await expect(page.locator(".board-card").first()).toBeVisible();
  await expect(page.locator(".board-column [draggable=true]")).toHaveCount(0);
});

test("a viewer's refused task controls keep the pane's own spacing", async ({
  page,
}) => {
  await asViewer(page);
  await page.goto("/tasks/1");
  await expect(
    page.getByRole("button", { name: "Edit", exact: true }),
  ).toBeDisabled();
  await expectGatesFilled(page, page.locator(".actions"));
});

// A refused control must sit where the allowed one does: SuiteOverlay's
// "+ New template" kept its own margin inside a wrapper that carried it
// too, and the wrapper's line box added more, so it and everything under
// it sat lower for a viewer (docs/test-runs/2026-11-07-rbac-roles-ui.md
// U3).
test("a viewer's suite New template sits where the allowed one does", async ({
  page,
  request,
}) => {
  const res = await request.get("/api/suites");
  const body = await res.json();
  const suite = (Array.isArray(body) ? body : body.suites || [])[0];
  expect(suite, "a demo suite").toBeTruthy();
  const place = async () => {
    await page.goto(`/processes/suites/${suite.id}`);
    const button = page.getByRole("button", { name: "+ New template" });
    await expect(button).toBeVisible();
    const save = page.getByRole("button", { name: "Save", exact: true });
    return [await button.boundingBox(), await save.boundingBox()];
  };
  const [allowed, allowedSave] = await place();
  await asViewer(page);
  const [refused, refusedSave] = await place();
  await expect(
    page.getByRole("button", { name: "+ New template" }),
  ).toBeDisabled();
  expect(Math.abs(refused.y - allowed.y)).toBeLessThan(1.5);
  expect(Math.abs(refused.x - allowed.x)).toBeLessThan(1.5);
  expect(Math.abs(refusedSave.y - allowedSave.y)).toBeLessThan(1.5);
});

// An access request's Grant and Deny are POST .../access-request/...,
// which needs capabilities:admin; the task pane offered them to every
// role (docs/test-runs/2026-11-07-rbac-roles-ui.md U4).
test("a viewer's access request Grant and Deny are disabled, and say why", async ({
  page,
}) => {
  await asViewer(page);
  await page.route(/\/api\/tasks\/1(\?.*)?$/, async (route) => {
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    await rewriteJSON(
      route,
      (body) => ({
        ...body,
        awaitingAccess: true,
        pendingAccess: {
          summary: "Grant read on acme/docs",
          reason: "Role-gated fixture.",
          items: [{ kind: "read", repo: "acme/docs" }],
          holds: {},
          earlier: [],
        },
      }),
      { headers },
    );
  });
  await page.goto("/tasks/1");
  const card = page.getByRole("region", {
    name: "Access request waiting for your decision",
  });
  await expect(card).toBeVisible();
  const grant = card.getByRole("button", { name: "Grant all" });
  const deny = card.getByRole("button", { name: "Deny" });
  await expect(grant).toBeDisabled();
  await expect(deny).toBeDisabled();
  // Still one row, Grant first.
  const g = await grant.boundingBox();
  const d = await deny.boundingBox();
  expect(Math.abs(g.y - d.y)).toBeLessThan(2);
  expect(g.x + g.width).toBeLessThanOrEqual(d.x);
  await grant.locator("xpath=..").hover();
  await expect(page.getByRole("tooltip")).toHaveText(
    "Needs capabilities:admin, which your role grain/viewer does not grant.",
  );
});

// 2026-11-08 saved-boards walkthrough (task 3612): BoardPage refused a
// viewer "Edit board" and "New board", while the dashboard under it still
// offered "Add panel" and "Keep", and the rail's Create options menu
// offered "New board" -- each opening a form the viewer could not submit.
test("a viewer's Create options New board and dashboard Add panel / Keep are refused in place", async ({
  page,
}) => {
  const created = await page.request.post("/api/boards", {
    data: {
      name: `Viewer dash ${Date.now()}`,
      folder: "",
      kind: "panels",
      description: "",
      query: "f=repo:any:acme/widgets",
      panels: [
        { kind: "list", title: "Queue", width: "half", orderKey: 1000 },
        { kind: "states", title: "States", width: "half", orderKey: 2000 },
      ],
    },
  });
  expect(created.ok()).toBe(true);
  const dash = await created.json();
  try {
    const dashUrl = `/boards/${dash.id}?f=repo:any:acme/widgets&q=retry`;
    const open = () =>
      page.locator(".panel-header").first().getByRole("link", { name: "Open" });
    const keepName = "Keep this narrowing on “Queue”";
    await page.goto(dashUrl);
    const allowedAdd = await page
      .getByRole("button", { name: "Add panel" })
      .boundingBox();
    const allowedKeep = await page
      .getByRole("button", { name: keepName })
      .boundingBox();
    const allowedOpen = await open().boundingBox();

    await asViewer(page);
    await page.goto(dashUrl);

    await page.getByRole("button", { name: "Create options" }).click();
    await expect(
      page.getByRole("menuitem", { name: "New task" }),
    ).toHaveAttribute("aria-disabled", "true");
    await expect(
      page.getByRole("menuitem", { name: "New board" }),
    ).toHaveAttribute("aria-disabled", "true");
    await page.keyboard.press("Escape");

    const addBtn = page.getByRole("button", { name: "Add panel" });
    const keepBtn = page.getByRole("button", { name: keepName });
    await expect(addBtn).toBeDisabled();
    await expect(keepBtn).toBeDisabled();
    await expectGatesFilled(page, page.locator(".panel-board"));

    const refusedAdd = await addBtn.boundingBox();
    const refusedKeep = await keepBtn.boundingBox();
    const refusedOpen = await open().boundingBox();
    for (const [a, b] of [
      [allowedAdd, refusedAdd],
      [allowedKeep, refusedKeep],
      [allowedOpen, refusedOpen],
    ]) {
      expect(Math.abs(a.x - b.x)).toBeLessThan(1.5);
      expect(Math.abs(a.y - b.y)).toBeLessThan(1.5);
    }
  } finally {
    await page.request.delete(`/api/boards/${dash.id}`).catch(() => {});
  }
});

test.describe("on a phone", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("a viewer's New board stays on the screen", async ({ page }) => {
    await asViewer(page);
    await page.goto("/boards");
    // A phone has no page header: the page's own button is the one.
    const button = page
      .getByRole("main")
      .getByRole("button", { name: "New board" })
      .first();
    await expect(button).toBeDisabled();
    const box = await button.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(390);
  });

  test("a viewer's Inbox Approve stays on the screen", async ({ page }) => {
    await asViewer(page);
    await withProposal(page);
    await page.goto("/inbox");
    const approve = proposalRow(page).getByRole("button", {
      name: "Approve",
      exact: true,
    });
    await expect(approve).toBeDisabled();
    const box = await approve.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(390);
  });

  test("a viewer's New task on the bar is disabled and on the screen", async ({
    page,
  }) => {
    await asViewer(page);
    await page.goto("/");
    const plus = page.getByRole("banner").getByRole("button", {
      name: "New task",
    });
    await expect(plus).toBeDisabled();
    const box = await plus.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(390);
  });

  test("a viewer's refused template buttons fill their footer row", async ({
    page,
    request,
  }) => {
    const res = await request.get("/api/templates");
    const body = await res.json();
    const list = Array.isArray(body) ? body : body.templates || [];
    const tmpl = list.find((t) => !t.builtIn) || list[0];
    expect(tmpl, "a demo template").toBeTruthy();
    await asViewer(page);
    await page.goto(`/processes/templates/${tmpl.id}`);
    const save = page.getByRole("button", { name: "Save", exact: true });
    await expect(save).toBeDisabled();
    await expectGatesFilled(page, save.locator("xpath=../.."));
  });
});
