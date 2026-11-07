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
import { alertRow, openAlerts } from "./alerts.js";

// The base-branch reconciler's "acme/widgets main is red" notification
// is written in markdown and, once a repair is filed, names it in taskId
// and as a [Task #N](/tasks/N) link in its body. Before the 2026-11-03
// deployment-operations walkthrough the bell drew that body as text --
// backticks, brackets and all -- and offered no way to the repair; the
// body also named the branch before its repository ("grain CI on `main`
// acme/widgets").
//
// The verdict is the demo's own: POST /api/e2e/base-branch/red runs the
// real reconciler over -fake-ci's runs and raises the real notification.
// The one stub is the repair: filing one is the pipeline steward's, which
// the demo does not run, so GET /api/notifications has the reconciler's
// repair sentence and taskId added to it, as it writes them when a repair
// is open.
test.use({ demoArgs: ["-github-sim", "-fake-ci", "-base-branch-ci"] });

// OPEN_LIST is the open list the bell polls: fields=list, each body cut
// to the first line it draws (notifications.js's useOpenNotifications).
const OPEN_LIST = /\/api\/notifications\?open=1&fields=list$/;
test.setTimeout(120_000);

test("the red-base alert reads as text and opens its repair", async ({
  page,
}) => {
  const red = await page.request.post("/api/e2e/base-branch/red", {
    data: { branch: "main" },
  });
  expect(red.status(), await red.text()).toBe(200);
  await page.route(OPEN_LIST, async (route) => {
    const res = await route.fetch({
      headers: { ...route.request().headers(), "if-none-match": "" },
    });
    const list = await res.json();
    for (const n of list) {
      if (!n.subject.startsWith("pipeline.base-red:")) continue;
      n.taskId = "1";
      n.body =
        n.body.replace(/ grain has filed no repair for it.*$/, "") +
        " [Task #1](/tasks/1) is repairing it; everything waiting on the branch waits on that.";
    }
    await route.fulfill({ response: res, json: list });
  });
  try {
    await page.goto("/");
    await openAlerts(page);
    const row = alertRow(page, "acme/widgets main is red");
    await expect(row).toHaveCount(1);
    await expect(row).toContainText(
      "grain CI on acme/widgets main is confirmed red",
    );
    await expect(row).toContainText("Task #1 is repairing it");
    await expect(row).not.toContainText("`");
    await expect(row).not.toContainText("](/tasks/");

    await row.getByRole("button", { name: "Open #1" }).click();
    await expect(page).toHaveURL(/\/tasks\/1$/);
  } finally {
    await page.unroute(OPEN_LIST);
    const green = await page.request.post("/api/e2e/base-branch/green", {
      data: { branch: "main" },
    });
    expect(green.status(), await green.text()).toBe(200);
  }
  // Green clears it: the next read has no such row.
  await page.goto("/");
  await openAlerts(page);
  await expect(alertRow(page, "acme/widgets main is red")).toHaveCount(0);
});
