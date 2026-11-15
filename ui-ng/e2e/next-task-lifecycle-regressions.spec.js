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

// Regressions from the 2026-11-15 task intake, Inbox and lifecycle
// walkthrough (docs/test-runs/2026-11-15-task-intake-inbox-lifecycle.md),
// all on the /next/ task page:
//
//   - Pause immediately throws away what a run has not pushed, so it asks
//     first, as the classic Pause now does;
//   - a paused task that was also stopped offers one Resume, not a second
//     that calls Retry, which the server always refuses while paused;
//   - a dependency chip says the state of the task it waits on, and its
//     title on hover.

async function taskByTitle(demoServer, accessCode, title) {
  const res = await fetch(`${demoServer}/api/tasks?fields=list`, {
    headers: { Authorization: `Bearer ${accessCode}` },
  });
  const body = await res.json();
  const tasks = Array.isArray(body) ? body : body.tasks;
  return tasks.find((t) => t.title === title);
}

test("Pause immediately asks first, and Cancel leaves the run going", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const t = await taskByTitle(
    demoServer,
    accessCode,
    "Bump the Go toolchain to 1.24",
  );
  expect(t?.state).toBe("running");
  const pauses = [];
  page.on("request", (r) => {
    if (r.method() === "POST" && r.url().endsWith(`/api/tasks/${t.id}/pause`))
      pauses.push(r.postDataJSON());
  });
  await page.goto(`/next/task/${t.id}`);

  await page.getByTestId("act-more").click();
  await page.getByTestId("act-pause-immediate").click();
  const dialog = page.getByTestId("confirm-dialog");
  await expect(dialog).toContainText("whatever it has not pushed is lost");
  await page.getByTestId("confirm-cancel").click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByTestId("act-pause")).toBeVisible();
  expect(pauses).toEqual([]);

  await page.getByTestId("act-more").click();
  await page.getByTestId("act-pause-immediate").click();
  await page.getByTestId("confirm-accept").click();
  await expect(page.getByTestId("act-resume-pause")).toBeVisible();
  expect(pauses).toEqual([{ immediate: true }]);

  await page.getByTestId("act-resume-pause").click();
  await expect(page.getByTestId("act-pause")).toBeVisible();
});

test("a stopped task that is also paused offers only the pause's Resume", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const t = await taskByTitle(
    demoServer,
    accessCode,
    "Add pagination to the tasks API",
  );
  expect(t?.state).toBe("running");
  await page.goto(`/next/task/${t.id}`);

  await page.getByTestId("act-stop").click();
  await expect(page.getByTestId("act-retry")).toHaveText("Resume");
  await page.getByTestId("act-pause").click();
  await expect(page.getByTestId("act-resume-pause")).toBeVisible();
  // Retry is refused while paused (ui.Client.Retry), so it is not offered.
  await expect(page.getByTestId("act-retry")).toHaveCount(0);
  await expect(
    page
      .getByRole("group", { name: "Task actions" })
      .getByRole("button", { name: "Resume", exact: true }),
  ).toHaveCount(1);

  // Lifting the pause brings the stop's own Resume back, and it works.
  await page.getByTestId("act-resume-pause").click();
  await expect(page.getByTestId("act-retry")).toBeVisible();
  await page.getByTestId("act-retry").click();
  await page.getByTestId("confirm-accept").click();
  await expect(page.getByTestId("act-retry")).toHaveCount(0);
  await expect
    .poll(
      async () =>
        (
          await taskByTitle(
            demoServer,
            accessCode,
            "Add pagination to the tasks API",
          )
        )?.state,
    )
    .toBe("queued");
});

test("a dependency chip says its task's state, and its title on hover", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const t = await taskByTitle(demoServer, accessCode, "Fix flaky retry test");
  const dep = await taskByTitle(
    demoServer,
    accessCode,
    "Replace the settings pane with a form library",
  );
  await page.goto(`/next/task/${t.id}`);
  await page.getByTestId("details-dep-input").fill(dep.id);
  await page.getByTestId("details-dep-add").click();

  const chip = page.getByTestId(`details-dep-link-${dep.id}`);
  await expect(chip).toHaveText(`#${dep.id}`);
  await expect(chip.locator("xpath=..")).toContainText("Deferred");
  await chip.hover();
  await expect(page.getByRole("tooltip")).toContainText(
    "Replace the settings pane with a form library",
  );

  await page
    .getByRole("button", { name: `Remove dependency #${dep.id}` })
    .click();
  await expect(chip).toHaveCount(0);
});
