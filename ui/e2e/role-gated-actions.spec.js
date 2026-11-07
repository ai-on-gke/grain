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
});
