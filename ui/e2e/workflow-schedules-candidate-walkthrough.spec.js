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
import { expect, test } from "./fixtures.js";

// docs/test-plans/2026-10-13.md §3 in a real browser: a repository's
// `.grain/schedules/x-candidate.yml` says `workflow: x` and `base:
// candidate`, and its firings run the workflow on candidate and are
// reviewed only when the run does not pass
// (docs/test-runs/2026-10-14-workflow-schedules-candidate.md).
//
// `grain demo -github-sim -fake-ci -scheduled-workflows` runs the daemon's
// own "declarations" and "scheduled-workflows" reconcilers against the
// simulated repo (pkg/demo/demo_scheduled_workflows.go). The stubs are
// the ones the demo is built on: GitHub is a bare repo on disk, and grain
// CI is -fake-ci, whose runs this spec ends with the verdict each step
// needs (POST /api/e2e/fake-ci/runs/{id}/finish) because it executes
// nothing.
test.use({
  demoArgs: [
    "-github-sim",
    "-fake-ci",
    "-scheduled-workflows",
    "-reconcile",
    "500ms",
  ],
});
test.describe.configure({ mode: "serial" });
test.setTimeout(120_000);

const candidateTitle = `Review last night's "x" workflow run on candidate`;
const alwaysTitle = `Review last night's "x" workflow run on candidate (always)`;
const candidateID = "repo:schedule:acme:widgets:x-candidate";
const alwaysID = "repo:schedule:acme:widgets:x-always";

const workflow = `name: x
on:
  schedule:
    - cron: "0 3 * * *"
jobs:
  smoke:
    runs-on: default
    steps:
      - run: echo ok
`;

const schedule = (extra = "") => `workflow: x
base: candidate
daily:
  at: "15:00"
overlap: skip
${extra}`;

function git(dir, args) {
  return execFileSync("git", args, {
    cwd: dir,
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

// declare pushes files onto the simulated repo's main, as the repository's
// own merge would.
async function declare(page, files) {
  const res = await page.request.get("/api/e2e/scheduled-workflows");
  expect(res.ok()).toBe(true);
  const { bare } = await res.json();
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "grain-walk-decl-"));
  try {
    git(dir, ["clone", "-q", "--branch", "main", `file://${bare}`, "repo"]);
    const repo = path.join(dir, "repo");
    for (const [name, body] of Object.entries(files)) {
      const file = path.join(repo, name);
      fs.mkdirSync(path.dirname(file), { recursive: true });
      fs.writeFileSync(file, body);
    }
    git(repo, ["add", "-A"]);
    git(repo, ["commit", "-qm", "declare"]);
    git(repo, ["push", "-q", "origin", "main"]);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

async function setDeclarations(page, on) {
  await page.goto("/repos/acme/widgets#advanced");
  const box = page.getByRole("checkbox", {
    name: "Read acme/widgets's own declarations",
  });
  if ((await box.isChecked()) !== on) await box.click();
  await expect(box).toBeChecked({ checked: on });
}

// seeOnReload reloads until locator shows: the repository page reads the
// declaration status once, and the reconciler writes it a moment later.
async function seeOnReload(page, locator) {
  await expect
    .poll(
      async () => {
        await page.reload();
        return locator
          .waitFor({ state: "visible", timeout: 3_000 })
          .then(() => true)
          .catch(() => false);
      },
      { timeout: 30_000 },
    )
    .toBe(true);
}

async function ciRuns(page) {
  return (await (await page.request.get("/api/ci/runs")).json()).runs || [];
}

// runNow presses the schedule pane's Run now, accepting its confirmation,
// and returns the -fake-ci run the reconciler started for the firing.
async function runNow(page, title) {
  const before = new Set((await ciRuns(page)).map((r) => r.id));
  await page.goto("/processes?kind=workflow");
  await page.getByRole("link", { name: title, exact: true }).click();
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: "Run now" }).click();
  let run;
  await expect
    .poll(async () => {
      run = (await ciRuns(page)).find((r) => !before.has(r.id));
      return run;
    })
    .toBeTruthy();
  expect(run.ref).toBe("candidate");
  expect(run.event).toBe("schedule");
  return run;
}

async function finish(page, run, conclusion) {
  const res = await page.request.post(
    `/api/e2e/fake-ci/runs/${run.id}/finish`,
    { data: { conclusion, job: "smoke" } },
  );
  expect(res.ok(), await res.text()).toBe(true);
}

async function reviewTasks(page) {
  const body = await (await page.request.get("/api/tasks")).json();
  const tasks = Array.isArray(body) ? body : body.tasks || [];
  return tasks.filter((t) => t.title.startsWith("Review last night's"));
}

function scheduleRow(page, title) {
  return page
    .locator(".process-row-schedule")
    .filter({ has: page.getByRole("link", { name: title, exact: true }) });
}

test("1. the repository's workflow schedules are read, the candidate one with its base", async ({
  page,
}) => {
  await declare(page, {
    ".grain/workflows/x.yml": workflow,
    ".grain/schedules/x-candidate.yml": "# Runs x on candidate.\n" + schedule(),
    ".grain/schedules/x-always.yml": schedule(
      `name: ${JSON.stringify(alwaysTitle)}\nreview: always\n`,
    ),
  });
  await setDeclarations(page, true);
  await seeOnReload(
    page,
    page.getByText(/Read at [0-9a-f]{12}: .*3 schedule\(s\)/),
  );

  await page.goto("/processes?kind=workflow");
  await expect(
    page.getByRole("link", { name: candidateTitle, exact: true }),
  ).toBeVisible();
  // Only the schedule that asks for every run reviewed says so.
  await expect(
    scheduleRow(page, alwaysTitle).getByText("Reviews every run"),
  ).toBeVisible();
  await expect(
    scheduleRow(page, candidateTitle).getByText("Reviews every run"),
  ).toHaveCount(0);

  // Its pane says it cannot be changed here and has Pause and Save off,
  // so the built-in-job note under it names the file the cadence lives
  // in rather than offering to change it.
  await page.getByRole("link", { name: candidateTitle, exact: true }).click();
  const pane = page.getByRole("dialog");
  await expect(pane).toContainText("cannot be edited or deleted here");
  await expect(pane).toContainText(
    "whether it runs at all are set in .grain/schedules/x-candidate.yml.",
  );
  await expect(pane).not.toContainText("You can change how often it runs");
  await expect(pane.getByRole("button", { name: "Pause" })).toBeDisabled();
});

test("2. a passing firing files no review and its row says so", async ({
  page,
}) => {
  const run = await runNow(page, candidateTitle);
  expect(run.requestedBy).toBe(`workflow-schedule:${candidateID}`);
  await finish(page, run, "success");

  await page.goto("/processes?kind=workflow");
  const last = scheduleRow(page, candidateTitle).getByTestId(
    "last-workflow-run",
  );
  await expect(last).toContainText(
    new RegExp(`Last run ${run.id} on candidate: green .*, no review needed`),
    { timeout: 15_000 },
  );
  await expect(last.getByRole("link", { name: run.id })).toHaveAttribute(
    "href",
    `/ci/${run.id}`,
  );
  expect(await reviewTasks(page)).toHaveLength(0);
});

test("3. a failing firing files one review on candidate, titled and told which run", async ({
  page,
}) => {
  const run = await runNow(page, candidateTitle);
  await finish(page, run, "failure");

  await page.goto("/processes?kind=workflow");
  const last = scheduleRow(page, candidateTitle).getByTestId(
    "last-workflow-run",
  );
  await expect(last).toContainText(/: failure .*, reviewed in task \d+/, {
    timeout: 15_000,
  });
  const reviews = await reviewTasks(page);
  expect(reviews).toHaveLength(1);
  expect(reviews[0].title).toBe(candidateTitle);
  expect(reviews[0].base).toBe("candidate");

  await last.getByRole("link", { name: /task \d+/ }).click();
  await expect(page).toHaveURL(new RegExp(`/tasks/${reviews[0].id}$`));
  const opening = page.getByText(
    `grain CI run ${run.id} of .grain/workflows/x.yml on acme/widgets, branch candidate`,
    { exact: false },
  );
  await expect(opening).toBeVisible();
  await expect(opening).toContainText(`its schedule ${candidateID} came due`);
  await expect(opening).toContainText("recorded as failure");

  // While that review is open the schedule does not fire over it, and
  // the refusal says what to close -- not an overlap setting a workflow
  // schedule cannot have.
  await page.goto("/processes?kind=workflow");
  await page.getByRole("link", { name: candidateTitle, exact: true }).click();
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: "Run now" }).click();
  const refusal = page.getByText(/its previous run has not finished yet/);
  await expect(refusal).toBeVisible();
  await expect(refusal).toContainText(`task ${reviews[0].id}`);
  await expect(refusal).not.toContainText("If the last run has not finished");
});

test("4. review: always files a review for a passing run", async ({ page }) => {
  const before = (await reviewTasks(page)).length;
  const run = await runNow(page, alwaysTitle);
  await finish(page, run, "success");

  await page.goto("/processes?kind=workflow");
  const row = scheduleRow(page, alwaysTitle);
  await expect(row.getByText("Reviews every run")).toBeVisible();
  await expect(row.getByTestId("last-workflow-run")).toContainText(
    /: green .*, reviewed in task \d+/,
    { timeout: 15_000 },
  );
  const reviews = await reviewTasks(page);
  expect(reviews).toHaveLength(before + 1);
  // The review is titled from the workflow and the base, as every
  // workflow review is, not from the schedule's own `name:`; its opening
  // comment is what names the schedule.
  const link = row
    .getByTestId("last-workflow-run")
    .getByRole("link", { name: /task \d+/ });
  const id = (await link.innerText()).replace("task ", "");
  const filed = reviews.find((t) => t.id === id);
  expect(filed, JSON.stringify(reviews)).toBeTruthy();
  expect(filed.title).toBe(candidateTitle);
  expect(filed.base).toBe("candidate");
  await link.click();
  await expect(
    page.getByText(`its schedule ${alwaysID} came due`, { exact: false }),
  ).toBeVisible();
  await expect(
    page.getByText("recorded as success", { exact: false }),
  ).toBeVisible();
});

test("6. each refused form is said readably, one file to a line, on the repository page", async ({
  page,
}) => {
  await declare(page, {
    ".grain/workflows/noschedule.yml": workflow.replace(
      'schedule:\n    - cron: "0 3 * * *"',
      "push:",
    ),
    ".grain/workflows/yamlext.yaml": workflow.replace(
      "name: x",
      "name: yamlext",
    ),
    ".grain/schedules/r-missing.yml": schedule().replace(
      "workflow: x",
      "workflow: nosuch",
    ),
    ".grain/schedules/r-noschedule.yml": schedule().replace(
      "workflow: x",
      "workflow: noschedule",
    ),
    ".grain/schedules/r-yaml.yml": schedule().replace(
      "workflow: x",
      "workflow: yamlext",
    ),
    ".grain/schedules/r-overlap.yml": schedule().replace(
      "overlap: skip",
      "overlap: allow",
    ),
    ".grain/schedules/r-template.yml": schedule("template: something\n"),
  });
  // The demo reads a repository at most every two minutes, as the daemon
  // does (orchestrator.declarationPollInterval); switching its
  // declarations off and back on is a read now.
  await setDeclarations(page, false);
  await expect
    .poll(async () => {
      const res = await page.request.get(
        "/api/repos/acme/widgets/declarations",
      );
      return (await res.json()).declarationsStatus;
    })
    .toBeFalsy();
  await setDeclarations(page, true);

  const failed = page.getByText(/^Last read failed:/);
  await seeOnReload(page, failed);
  const text = await failed.innerText();
  const lines = text.split("\n");
  for (const [file, says] of [
    ["r-missing.yml", 'no workflow "nosuch"'],
    ["r-noschedule.yml", "has no `on: schedule`"],
    ["r-yaml.yml", ".grain/workflows/yamlext.yaml is not one -- rename it"],
    ["r-overlap.yml", "a schedule that runs a workflow cannot overlap"],
    [
      "r-template.yml",
      "one of `template:`, `suite:` and `workflow:`, not several",
    ],
  ]) {
    const line = lines.find((l) => l.includes(`.grain/schedules/${file}`));
    expect(line, `${file} has a line of its own in:\n${text}`).toBeTruthy();
    expect(line).toContain(says);
  }
});
