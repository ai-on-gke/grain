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

async function asViewer(page) {
  await page.route(
    (u) => u.pathname === "/api/config",
    async (route) => {
      const response = await route.fetch();
      const json = { ...(await response.json()), access: VIEWER };
      await route.fulfill({ response, json });
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
      if (route.request().method() !== "GET") return route.fallback();
      const response = await route.fetch();
      const body = await response.json();
      const list = Array.isArray(body) ? body : body.tasks || [];
      const extra = {
        ...(list[0] || {}),
        id: "9901",
        title: "Role-gated proposal",
        state: "proposed",
        approved: false,
      };
      const json = Array.isArray(body)
        ? [extra, ...list]
        : { ...body, tasks: [extra, ...list] };
      await route.fulfill({ response, json });
    },
  );
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
});
