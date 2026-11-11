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

// The capability policy in the grant pickers that
// capability-server-settings.spec.js does not walk (test plan 2026-10-30
// §3, docs/test-runs/2026-11-01-capability-policy-pickers.md): a grant a
// task holds that a later policy save denies, a repo-conditional rule
// judged for the task's own repo, the repo page's CI grants judged for
// that repo, removing a scope set that a task, a template, a schedule
// and a workflow job all hold, and repo#read worded as CI-only rather
// than blamed on the policy (grain/task-2726).

const DEMO = "https://capabilities.demo.grain.example/cloud";
const VIEWER = `${DEMO}#cloud-viewer`;
const KEY = `${DEMO}#cloud-key`;

const REPO_READ = "https://grain.dev/capabilities/repo#read";

const VIEWPORTS = [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
];

// expectInside asserts that a menu item, and its policy verdict, lie
// inside the menu: a long refusal wraps rather than running out of the
// popover on a phone.
//
// The popover opens with MUI's Grow transition, a scale transform, so the
// boxes are read only once its animations have finished, and all three in
// one snapshot: read one round trip apart on a slow runner, the menu was
// measured at one scale and the item at a larger one, and a row that fits
// read as 1.6px wider than its menu.
// expectRowInside is a menu row that fits within its menu's width.
async function expectRowInside(menu, row) {
  await row.scrollIntoViewIfNeeded();
  const m = await menu.boundingBox();
  const r = await row.boundingBox();
  expect(r.x).toBeGreaterThanOrEqual(m.x - 1);
  expect(r.x + r.width).toBeLessThanOrEqual(m.x + m.width + 1);
}

async function expectInside(menu, item) {
  await item.scrollIntoViewIfNeeded();
  await menu.evaluate((el) =>
    Promise.all(
      (el.closest(".MuiPopover-paper") || el)
        .getAnimations({ subtree: true })
        .map((a) => a.finished),
    ),
  );
  const { menuBox, itemBox, verdictBox } = await item.evaluate(
    (el, m) => {
      const box = (e) => {
        const r = e.getBoundingClientRect();
        return { x: r.x, width: r.width };
      };
      return {
        menuBox: box(m),
        itemBox: box(el),
        verdictBox: box(el.querySelector(".method-policy-verdict")),
      };
    },
    await menu.elementHandle(),
  );
  expect(itemBox.x).toBeGreaterThanOrEqual(menuBox.x - 1);
  expect(itemBox.x + itemBox.width).toBeLessThanOrEqual(
    menuBox.x + menuBox.width + 1,
  );
  expect(verdictBox.x).toBeGreaterThanOrEqual(itemBox.x - 1);
  expect(verdictBox.x + verdictBox.width).toBeLessThanOrEqual(
    itemBox.x + itemBox.width + 1,
  );
}

async function openGrantMenu(page, region) {
  await region.getByRole("button", { name: "Add grant…" }).click();
  return page.getByRole("menu", { name: "Grants to add" });
}

test("judges a held grant, a repo-conditional rule and a repo's CI grants by the policy for that repo", async ({
  page,
  request,
}, testInfo) => {
  const before = await (await request.get("/api/capability-policy")).json();
  let taskID = "";
  try {
    // Filed while the policy admits it...
    const created = await request.post("/api/tasks", {
      data: {
        title: `E2E held grant ${Date.now()}`,
        description: "d",
        repo: "acme/gadgets",
        capabilities: [VIEWER],
      },
    });
    expect(created.ok()).toBeTruthy();
    taskID = (await created.json()).id;

    // ...then denied it on acme/gadgets, while cloud-key is admitted on
    // acme/gadgets alone.
    const put = await request.put("/api/capability-policy", {
      data: {
        policy: {
          ...before.policy,
          rules: [
            {
              capability: VIEWER,
              repos: ["acme/gadgets"],
              runKinds: ["task"],
              deny: true,
            },
            { capability: KEY, repos: ["acme/gadgets"] },
          ],
        },
      },
    });
    expect(put.ok()).toBeTruthy();

    for (const [label, viewport] of VIEWPORTS) {
      await page.setViewportSize(viewport);

      // The task pane flags the held grant, and its menu judges the
      // task's own run: cloud-viewer disabled with the rule, cloud-key
      // offered without the kind-wide "may be refused" -- on acme/gadgets
      // the policy admits it.
      await page.goto(`/tasks/${taskID}`);
      const pane = page.getByTestId(`task-${taskID}-grants`);
      await expect(pane).toContainText("denied by policy");
      let menu = await openGrantMenu(page, pane);
      const denied = menu.locator(`[data-method="${VIEWER}"]`);
      await expect(denied).toHaveAttribute("aria-disabled", "true");
      await expect(denied).toContainText(
        `the capability policy denies ${VIEWER} to this task on acme/gadgets (rule 1: deny ${VIEWER})`,
      );
      await expectInside(menu, denied);
      const admitted = menu.locator(`[data-method="${KEY}"]`);
      await expect(admitted).not.toHaveAttribute("aria-disabled", "true");
      await expect(admitted).not.toHaveAttribute("data-policy-verdict");
      await expect(admitted).not.toContainText("may be refused");
      await page.screenshot({
        path: testInfo.outputPath(`task-pane-${label}.png`),
      });
      await page.keyboard.press("Escape");

      // The repo page's CI grants ask about CI jobs on that repo:
      // cloud-key is not admitted on acme/widgets, so it is disabled
      // with the reason rather than "may be refused".
      await page.goto("/repos/acme/widgets#ci");
      menu = await openGrantMenu(page, page.getByTestId("repo-ci-grants"));
      const ciKey = menu.locator(`[data-method="${KEY}"]`);
      await expect(ciKey).toHaveAttribute("aria-disabled", "true");
      await expect(ciKey).toContainText(
        `the capability policy does not admit ${KEY} for this CI job on acme/widgets`,
      );
      await expectInside(menu, ciKey);
      await page.screenshot({
        path: testInfo.outputPath(`repo-ci-grants-${label}.png`),
      });
      await page.keyboard.press("Escape");
    }
  } finally {
    await request.put("/api/capability-policy", {
      data: { policy: before.policy },
    });
    if (taskID) {
      await request.post(`/api/tasks/${taskID}/capabilities`, {
        data: { capability: VIEWER, attach: false },
      });
    }
  }
});

// repo#read is refused every task and process by grain's own CI-only
// rule, before the policy's rules are looked at: the listing says
// ci_only, not denied. A task's own read access is its reads list, so
// the task pickers do not offer the CI job's method at all: "Add grant…"
// offers "Read a repository" instead (unified-grants slice 5), which
// adds a repo#read line the task files as a read.
test("lists repo#read as CI-only, and offers task pickers a read of their own", async ({
  page,
  request,
}, testInfo) => {
  // Four passes (desktop and phone, light and dark), each loading two
  // pages, opening two grant menus and taking two screenshots: about 23s
  // on an idle machine, which a busy CI runner pushed past the default
  // 30s with nothing wrong on the page.
  test.setTimeout(90_000);
  const listing = await (await request.get("/api/capability-methods")).json();
  const read = listing.find((m) => m.ref === REPO_READ);
  expect(read?.policy).toEqual({ task: "ci_only", process: "ci_only" });
  const forTask = await (
    await request.get("/api/capability-methods?kind=task&repo=acme/widgets")
  ).json();
  const refusal = forTask.find((m) => m.ref === REPO_READ)?.policyRefusal;
  expect(refusal).toContain("is a CI capability, not a task grant");
  expect(refusal).toContain("read access");
  expect(refusal).not.toContain("writes to the repository");

  let taskID = "";
  try {
    const created = await request.post("/api/tasks", {
      data: {
        title: `E2E repo read ${Date.now()}`,
        description: "d",
        repo: "acme/widgets",
      },
    });
    expect(created.ok()).toBeTruthy();
    taskID = (await created.json()).id;

    for (const [label, viewport, theme] of VIEWPORTS.flatMap(([l, v]) =>
      ["light", "dark"].map((th) => [`${l}-${th}`, v, th]),
    )) {
      await page.emulateMedia({ colorScheme: theme });
      await page.setViewportSize(viewport);

      await page.goto("/processes");
      await page.getByRole("button", { name: "+ New", exact: true }).click();
      await page
        .getByTestId("new-process-kind")
        .getByRole("menuitem", { name: "Template" })
        .click();
      let menu = await openGrantMenu(page, page.getByTestId("template-grants"));
      await expect(menu.locator(`[data-method="${REPO_READ}"]`)).toHaveCount(0);
      let row = menu.locator('[data-repo-grant="read"]');
      await expect(row).toContainText("Read a repository");
      await expectRowInside(menu, row);
      await page.screenshot({
        path: testInfo.outputPath(`template-repo-read-${label}.png`),
      });
      await page.keyboard.press("Escape");

      await page.goto(`/tasks/${taskID}`);
      menu = await openGrantMenu(
        page,
        page.getByTestId(`task-${taskID}-grants`),
      );
      await expect(menu.locator(`[data-method="${REPO_READ}"]`)).toHaveCount(0);
      row = menu.locator('[data-repo-grant="read"]');
      await expect(row).toContainText("Read a repository");
      await page.screenshot({
        path: testInfo.outputPath(`task-repo-read-${label}.png`),
      });
      await expectRowInside(menu, row);
      await page.keyboard.press("Escape");
    }
  } finally {
    if (taskID) await request.delete(`/api/tasks/${taskID}`);
  }
});

test("removing a scope set lists the task, template, schedule and workflow job holding it, and Cancel keeps it", async ({
  page,
  request,
}) => {
  const SET = `e2e/holders${Date.now()}`;
  const GRANT = `api:${SET}`;
  const workflowsBefore = await (
    await request.get("/api/repos/acme/widgets/workflows")
  ).json();
  const made = { task: "", template: "", schedule: "" };
  try {
    expect(
      (
        await request.put("/api/api-scope-sets", {
          data: { set: { name: SET, scopes: ["tasks:read"] } },
        })
      ).ok(),
    ).toBeTruthy();
    const task = await request.post("/api/tasks", {
      data: {
        title: `E2E ${SET}`,
        description: "d",
        repo: "acme/widgets",
        capabilities: [GRANT],
      },
    });
    made.task = (await task.json()).id;
    const template = await request.post("/api/templates", {
      data: {
        name: `e2e-holders-${Date.now()}`,
        title: "t",
        capabilities: [GRANT],
      },
    });
    made.template = (await template.json()).id;
    const schedule = await request.post("/api/schedules", {
      data: {
        title: `E2E ${SET}`,
        description: "d",
        repo: "acme/widgets",
        recurrence: { kind: "weekly", timeOfDay: "06:00", weekday: "monday" },
        capabilities: [GRANT],
      },
    });
    made.schedule = (await schedule.json()).id;
    const workflow = await request.put("/api/repos/acme/widgets/workflows", {
      data: {
        workflows: [
          ...(workflowsBefore.workflows || []),
          {
            path: ".grain/workflows/e2e-holders.yml",
            text: `name: holders\non: [push]\njobs:\n  lint:\n    capabilities: ["${GRANT}"]\n    steps:\n      - run: "true"\n`,
          },
        ],
      },
    });
    expect(workflow.ok()).toBeTruthy();
    expect(made.task && made.template && made.schedule).toBeTruthy();

    for (const [, viewport] of VIEWPORTS) {
      await page.setViewportSize(viewport);
      await page.goto("/settings/capabilities#api-scope-sets");
      const row = page.getByTestId(`api-scope-set-${SET}`);
      await row.getByRole("button", { name: "Remove" }).click();
      const dialog = page.getByRole("dialog");
      const canonicalGrant = `https://grain.dev/capabilities/api#${SET}`;
      await expect(dialog).toContainText(
        `4 grants hold ${canonicalGrant}, and will grant nothing once it is removed:`,
      );
      for (const [holder, expectedGrant] of [
        [`task:${made.task}`, canonicalGrant],
        [`template:${made.template}`, canonicalGrant],
        [`schedule:${made.schedule}`, canonicalGrant],
        ["workflow:acme/widgets:.grain/workflows/e2e-holders.yml#lint", GRANT],
      ]) {
        await expect(dialog.locator(`[data-holder="${holder}"]`)).toContainText(
          expectedGrant,
        );
      }
      const box = await dialog.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(viewport.width);
      await dialog.getByRole("button", { name: "Cancel" }).click();
      await expect(dialog).toHaveCount(0);
      await page.reload();
      await expect(row).toBeVisible();
    }
    const sets = await (await request.get("/api/api-scope-sets")).json();
    expect(JSON.stringify(sets)).toContain(`"${SET}"`);
  } finally {
    await request.put("/api/repos/acme/widgets/workflows", {
      data: { workflows: workflowsBefore.workflows || [] },
    });
    if (made.schedule) await request.delete(`/api/schedules/${made.schedule}`);
    if (made.template) await request.delete(`/api/templates/${made.template}`);
    if (made.task) {
      await request.post(`/api/tasks/${made.task}/capabilities`, {
        data: { capability: GRANT, attach: false },
      });
    }
    await request.post("/api/api-scope-sets/delete", { data: { name: SET } });
  }
});
