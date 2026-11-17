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

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { expect, test } from "./fixtures";

test.use({
  demoArgs: [
    "-schedules",
    "-github-sim",
    "-fake-ci",
    "-scheduled-workflows",
    "-reconcile",
    "500ms",
  ],
});
test.describe.configure({ mode: "serial" });
test.setTimeout(120_000);

function git(cwd: string, args: string[]): string {
  return execFileSync("git", args, {
    cwd,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
    env: {
      ...process.env,
      GIT_AUTHOR_NAME: "walk",
      GIT_AUTHOR_EMAIL: "walk@example.com",
      GIT_COMMITTER_NAME: "walk",
      GIT_COMMITTER_EMAIL: "walk@example.com",
    },
  });
}

test("legacy item URLs, ?kind/&?q= narrowing, and rail New menu reopen work end to end", async ({
  page,
}) => {
  await page.goto("schedules/sched-2");
  await expect(page).toHaveURL(/\/next\/processes\/schedules\/sched-2$/);
  await expect(
    page.getByRole("heading", {
      level: 2,
      name: "Weekly Dependency & Vulnerability Audit",
    }),
  ).toBeVisible();

  await page.goto("templates/template-2");
  await expect(page).toHaveURL(/\/next\/processes\/templates\/template-2$/);
  await expect(
    page.getByRole("heading", { level: 2, name: "security-audit" }),
  ).toBeVisible();

  await page.goto("suites/suite-1");
  await expect(page).toHaveURL(/\/next\/processes\/suites\/suite-1$/);
  await expect(
    page.getByRole("heading", {
      level: 2,
      name: "Release Qualification Suite",
    }),
  ).toBeVisible();

  await page.goto("suites/runs/999");
  await expect(page).toHaveURL(/\/next\/processes\/suites\/runs\/999$/);
  await expect(page.getByRole("alert")).toContainText('No suite run "999".');

  await page.goto("processes?kind=template&q=security");
  await expect(page.getByRole("link", { name: /^Templates/ })).toHaveAttribute(
    "aria-current",
    "page",
  );
  await expect(page.getByLabel("Filter this view")).toHaveValue("security");
  await page.getByLabel("Filter this view").fill("");

  // Rail New menu opens New schedule, strips ?new=1, and reopens after Cancel.
  await page.goto("inbox");
  const rail = page.getByRole("navigation", { name: "Primary destinations" });
  await rail.getByRole("button", { name: "New", exact: true }).click();
  await page.getByRole("link", { name: /New schedule/ }).click();

  const dlg = page.getByRole("dialog", { name: "New schedule or trigger" });
  await expect(dlg).toBeVisible();
  await expect(page).toHaveURL(/\/next\/processes\/schedules$/);

  await dlg.getByRole("button", { name: "Cancel" }).click();
  await expect(dlg).toBeHidden();

  await rail.getByRole("button", { name: "New", exact: true }).click();
  await page.getByRole("link", { name: /New schedule/ }).click();
  await expect(dlg).toBeVisible();
  await dlg.getByRole("button", { name: "Cancel" }).click();
});

test("schedule dialog surfaces validation errors inside the modal, enforces 10-min floor, and shows suppressed off-target git_ref", async ({
  page,
}) => {
  // Seed an existing off-target git_ref schedule via supported API.
  const createdOff = await page.request.post("/api/schedules", {
    data: {
      title: "Off-target upstream ref",
      description: "Watches offlist/upstream",
      repo: "acme/widgets",
      base: "main",
      recurrence: {
        kind: "git_ref",
        gitRef: { repo: "offlist/upstream", ref: "refs/heads/main" },
      },
      overlap: "skip",
    },
  });
  expect(createdOff.ok()).toBe(true);
  const offSched = await createdOff.json();

  try {
    await page.goto(`processes/schedules/${offSched.id}`);
    await expect(
      page.getByText("· next: suppressed", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByTestId("sched-config-pane").getByText("suppressed", {
        exact: true,
      }),
    ).toBeVisible();
    await expect(page.getByText("2027-")).toHaveCount(0);

    // Pause and resume the off-target suppressed schedule.
    await page.getByRole("button", { name: "Pause", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Resume", exact: true }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Resume", exact: true }).click();
    await expect(
      page.getByRole("button", { name: "Pause", exact: true }),
    ).toBeVisible();

    // Open New schedule and verify forbidden git_ref error renders inside the dialog.
    await page.getByRole("button", { name: "New ▾" }).click();
    await page.getByRole("menuitem", { name: "Schedule" }).click();
    const dlg = page.getByRole("dialog", { name: "New schedule or trigger" });
    await expect(dlg).toBeVisible();

    await dlg
      .getByLabel("Every N min")
      .or(dlg.getByRole("button", { name: "Every N min" }))
      .click();
    await expect(dlg.getByLabel("Every N minutes")).toHaveAttribute(
      "min",
      "10",
    );

    await dlg.getByLabel("Task title").fill("Forbidden git_ref probe");
    await dlg.getByLabel("Task prompt").fill("Check forbidden watched repo.");
    await dlg.getByRole("button", { name: "Git ref push" }).click();
    await dlg.getByLabel("Watched git repository").fill("grain/config-infra");
    await dlg.getByLabel("Watched git ref / branch").fill("refs/heads/main");
    await dlg.getByRole("button", { name: "Save schedule" }).click();

    const alert = dlg.getByRole("alert");
    await expect(alert).toBeVisible();
    await expect(alert).toContainText("recurrence.gitRef.repo");
    const dlgBox = await dlg.boundingBox();
    const alertBox = await alert.boundingBox();
    expect(dlgBox).not.toBeNull();
    expect(alertBox).not.toBeNull();
    expect(alertBox!.x).toBeGreaterThanOrEqual(dlgBox!.x - 1);
    expect(alertBox!.x + alertBox!.width).toBeLessThanOrEqual(
      dlgBox!.x + dlgBox!.width + 1,
    );

    await dlg.getByRole("button", { name: "Cancel" }).click();
  } finally {
    await page.request.delete(`/api/schedules/${offSched.id}`);
  }
});

test("templates with required parameters preview without 400, workflow Run now confirms, triggers link to Advanced, and suite runs render cleanly", async ({
  page,
}) => {
  const fourHundreds: string[] = [];
  page.on("response", (res) => {
    if (res.url().includes("/api/") && res.status() >= 400) {
      fourHundreds.push(
        `${res.request().method()} ${res.url()} -> ${res.status()}`,
      );
    }
  });

  // Push a workflow schedule and a path trigger to acme/widgets and enable declarations.
  const swRes = await page.request.get("/api/e2e/scheduled-workflows");
  expect(swRes.ok()).toBe(true);
  const { bare } = await swRes.json();
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "grain-ng-proc-"));
  try {
    git(tmp, ["clone", "-q", "--branch", "main", `file://${bare}`, "repo"]);
    const repo = path.join(tmp, "repo");
    fs.mkdirSync(path.join(repo, ".grain/workflows"), { recursive: true });
    fs.mkdirSync(path.join(repo, ".grain/triggers"), { recursive: true });
    fs.mkdirSync(path.join(repo, ".grain/templates"), { recursive: true });
    fs.writeFileSync(
      path.join(repo, ".grain/templates/docs-check.yml"),
      "name: Docs Check\ntitle: Verify docs\nprompt: Check docs.\n",
    );
    fs.writeFileSync(
      path.join(repo, ".grain/workflows/nightly-ci.yml"),
      'name: nightly-ci\non:\n  schedule:\n    - cron: "0 4 * * *"\njobs:\n  smoke:\n    runs-on: default\n    steps:\n      - run: echo ok\n',
    );
    fs.writeFileSync(
      path.join(repo, ".grain/triggers/docs-guard.yml"),
      'on: pull_request\npaths: ["docs/**"]\ntemplate: docs-check\ngate: true\n',
    );
    git(repo, ["add", "-A"]);
    git(repo, ["commit", "-qm", "seed workflow and trigger"]);
    git(repo, ["push", "-q", "origin", "main"]);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }

  await page.request.put("/api/repos/acme/widgets/declarations", {
    data: { declarations: true },
  });

  await expect
    .poll(
      async () => {
        const r = await (
          await page.request.get("/api/repos/acme/widgets/declarations")
        ).json();
        return r.declarationsStatus?.triggers?.length ?? 0;
      },
      { timeout: 20_000 },
    )
    .toBe(1);

  // Trigger link on Declarations opens repository Advanced tab.
  await page.goto("processes/declarations");
  await page
    .getByRole("link", { name: ".grain/triggers/docs-guard.yml" })
    .click();
  await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/advanced$/);

  // Workflow schedule Run now asks confirmation explaining immediate CI run.
  await page.goto("processes/schedules");
  await page
    .getByRole("button", {
      name: /Review last night's "nightly-ci" workflow run/,
    })
    .click();
  await page.getByRole("button", { name: "Run now" }).click();
  const confirm = page.getByRole("dialog", {
    name: "Run workflow schedule now?",
  });
  await expect(confirm).toBeVisible();
  await expect(confirm).toContainText(
    "starts a CI run of the workflow it was declared in straight away",
  );
  await confirm.getByRole("button", { name: "Cancel" }).click();

  // Create template with required parameter and no default -> zero 400s on composition preview.
  await page.goto("processes/templates");
  await page.getByRole("button", { name: "New ▾" }).click();
  await page.getByRole("menuitem", { name: "Template" }).click();
  const tmplDlg = page.getByRole("dialog", { name: "New task template" });
  await tmplDlg.getByLabel("Template name").fill("e2e-req-param");
  await tmplDlg.getByLabel(/Filed task title/).fill("chore: update {{module}}");
  await tmplDlg.getByLabel("Prompt body").fill("Update {{module}}.");
  await tmplDlg.getByLabel("Parameters (comma-separated names)").fill("module");
  await tmplDlg.getByRole("button", { name: "Save template" }).click();
  await expect(tmplDlg).toBeHidden();
  await expect(
    page.getByText("chore: update {{module}}").first(),
  ).toBeVisible();
  expect(fourHundreds).toEqual([]);

  // Start a suite run and verify direct URL /next/processes/suites/runs/<id>.
  const runRes = await page.request.post("/api/suite-runs", {
    data: { suiteId: "suite-1", repo: "acme/widgets", base: "main" },
  });
  expect(runRes.ok()).toBe(true);
  const run = await runRes.json();

  await page.goto(`processes/suites/runs/${run.id}`);
  await expect(
    page.getByRole("heading", {
      level: 2,
      name: new RegExp(`Release Qualification Suite · Run #${run.id}`),
    }),
  ).toBeVisible();
});

test(
  "Processes pages fit desktop and phone without sideways scroll or serious axe violations",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe }) => {
    for (const sub of [
      "processes/schedules",
      "processes/templates",
      "processes/suites",
      "processes/declarations",
    ]) {
      await page.goto(sub);
      await expect(
        page.getByRole("heading", { level: 1, name: "Processes" }),
      ).toBeVisible();
      const sideways = await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
      expect(sideways, `${sub} scrolled sideways`).toBeLessThanOrEqual(0);

      const violations = await axe();
      expect(
        violations.map((v) => `${v.id} (${v.nodes.length}) ${v.help}`),
        `axe violations on ${sub}`,
      ).toEqual([]);
    }
  },
);

// A refused save says why beside the Save that was pressed: at phone
// width the dialog scrolls, and an error at its top was out of view
// (viewport ratio 0) when the server turned the save down.
test(
  "a refused schedule save is in view beside its actions",
  { tag: ["@phone"] },
  async ({ page }) => {
    await page.goto("processes/schedules");
    await page.getByTestId("proc-new-menu-btn").click();
    await page.getByTestId("proc-new-schedule").click();
    const dialog = page.getByRole("dialog");
    await page.getByTestId("sched-title-input").fill("e2e: too often");
    await page.getByTestId("sched-desc-input").fill("Throwaway.");
    await page.getByTestId("sched-repo-input").fill("acme/widgets");
    await page.getByTestId("sched-kind-everyNMinutes").click();
    await dialog.getByLabel("Every N minutes").fill("5");
    const save = page.getByTestId("sched-save-btn");
    await save.click();

    const error = dialog.getByTestId("proc-dialog-error");
    await expect(error).toContainText("at least 10");
    await expect(error).toBeInViewport();
    const box = (await error.boundingBox())!;
    const frame = (await dialog.boundingBox())!;
    const saveBox = (await save.boundingBox())!;
    // Inside the dialog, wrapped rather than running past its edge, and
    // above the actions.
    expect(box.x).toBeGreaterThanOrEqual(frame.x);
    expect(box.x + box.width).toBeLessThanOrEqual(frame.x + frame.width + 0.5);
    expect(box.y + box.height).toBeLessThanOrEqual(saveBox.y + 0.5);

    await dialog.getByRole("button", { name: "Cancel" }).click();
    await page.getByTestId("proc-new-menu-btn").click();
    await page.getByTestId("proc-new-schedule").click();
    await expect(page.getByTestId("proc-dialog-error")).toHaveCount(0);
  },
);
