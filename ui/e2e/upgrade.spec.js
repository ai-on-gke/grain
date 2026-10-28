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

// The Upgrade button, clicked -- in a real browser, in the place an
// operator actually finds it: Settings, Upgrade tab, type a branch, click.
//
// Every other test of this feature starts below that click. The component
// tests next door (../src/components/UpgradePanel.test.jsx) render the
// panel on its own in jsdom and mock the API module out from under it;
// the Go tests post to /api/upgrade themselves. So the part nothing
// covered was the route from the sidebar to the request: that the tab
// exists, that the panel renders inside it, that the button is wired to
// the form, and that what leaves the browser is the branch that was
// typed.
//
// The API is stubbed because `grain demo` -- playwright.config.js's
// server -- deliberately configures no upgrader (it is a throwaway UI
// over a temp store, with no deployment to upgrade), so its own
// /api/upgrade answers "not available" and there would be no button at
// all. Same reasoning as shell.spec.js's own interception of
// /api/config: the one fact the demo cannot be is rewritten on its way
// past, and everything downstream of it is the real UI making a real
// request. The second test below asserts on the un-stubbed answer.

// Said rather than assumed, because one assertion below is about the
// mark that animates while the upgrade runs: GrainMark falls back to the
// still for a reader who has asked for less motion, and what a headless
// browser reports for that is a default this suite would otherwise be
// inheriting silently.
test.use({ reducedMotion: "no-preference" });

// RUNNING_IMAGE is what the stubbed deployment is on before anything is
// clicked -- GET /api/upgrade's "running", the ref file's own line on a
// real host (pkg/upgrade's Running).
const RUNNING_IMAGE = "ghcr.io/bwsalmon/grain/grain:main";

// IDLE is what the stubbed deployment reports before anything is
// clicked: an upgrader, the image it is on, and no upgrade of any kind
// on record.
const IDLE = {
  enabled: true,
  // What a deployment scripts/setup.sh installed reports, and so what
  // the pane's copy describes here (pkg/upgrade.ModeImage).
  mode: "image",
  // ... including one such deployment's defining case: an image it is
  // demonstrably running, and no upgrade attempt on record at all to
  // have learned that from (grain/task-50).
  running: { image: RUNNING_IMAGE },
  branch: "",
  phase: "",
  detail: "",
};

// Stubbed on purpose. A real upgrade rebuilds or re-pulls grain and
// restarts the deployment onto it, and the demo wires no Upgrader, so
// /api/upgrade cannot be driven for real here.
//
// stubUpgrade stands in for a deployment that has an upgrader: idle until
// something posts to it, then reporting whatever that post produced --
// an upgrade under way, or one merely scheduled to happen once the
// deployment is idle (pkg/ui's afterTasksFinish, grain/task-156).
// Pushes each posted body onto `posted` so the test can assert on what
// the button actually sent, and answers the cancel route the scheduled
// case needs.
async function stubUpgrade(page, posted) {
  let state = null;
  await page.route("**/api/upgrade", async (route) => {
    const request = route.request();
    if (request.method() === "POST") {
      const body = JSON.parse(request.postData() || "{}");
      posted.push(body);
      state = body.afterTasksFinish
        ? {
            ...IDLE,
            // Nothing has started: what the deployment has is a row
            // saying it will, and the counts saying what it is still
            // waiting for.
            scheduled: {
              branch: body.branch,
              requestedAt: new Date().toISOString(),
              byId: "operator",
              busyRuns: 2,
              busyCiRuns: 1,
            },
          }
        : {
            ...IDLE,
            branch: body.branch,
            phase: "running",
          };
      await route.fulfill({ status: 202, json: state });
      return;
    }
    await route.fulfill({ json: state || IDLE });
  });
  await page.route("**/api/upgrade/scheduled", async (route) => {
    if (route.request().method() !== "DELETE") {
      await route.fallback();
      return;
    }
    state = null;
    await route.fulfill({ json: { ...IDLE, cancelled: true } });
  });
}

test("upgrades the branch typed into Settings → Upgrade", async ({ page }) => {
  const posted = [];
  await stubUpgrade(page, posted);

  await page.goto("/");
  await page.getByTestId("top-bar-settings").click();
  await page.getByTestId("settings-nav-upgrade").click();

  // Before anything is typed, the pane answers "what am I running right
  // now?" -- the half of "from X to Y" an operator arrives with.
  await expect(page.locator("p.upgrade-running")).toContainText(
    `Now running ${RUNNING_IMAGE}`,
  );

  await page.getByLabel("Branch").fill("grain/task-31");
  await page
    .getByTestId("upgrade-section")
    .getByRole("button", { name: "Upgrade" })
    .click();

  // What left the browser is what the operator typed, in the shape
  // pkg/ui's handler reads.
  await expect
    .poll(() => posted)
    .toEqual([{ branch: "grain/task-31", afterTasksFinish: false }]);

  // And the pane says so: the upgrade it started is reported back, and
  // the button is out of action until it lands, so a second click cannot
  // race the first (pkg/upgrade allows one upgrade at a time).
  // Scoped to the panel's own status list: "Running" is also a nav entry
  // in the rail behind this overlay.
  const status = page.locator("dl.upgrade-status");
  await expect(status.locator(".upgrade-phase")).toHaveText("running");
  await expect(status).toContainText("grain/task-31");
  await expect(page.getByRole("button", { name: "Upgrading…" })).toBeDisabled();

  // ... and the mark comes up with it, big and moving, which is the one
  // thing on this pane that keeps saying "still working" through the
  // minutes a build spends printing nothing new. A real browser here, so
  // this is the live canvas rather than the still jsdom falls back to in
  // the component test next door.
  const mark = page.locator(".upgrade-mark canvas");
  await expect(mark).toBeVisible();
  expect(await mark.boundingBox()).toMatchObject({ width: 96, height: 96 });
});

test("schedules the upgrade for when the deployment is idle", async ({
  page,
}) => {
  const posted = [];
  await stubUpgrade(page, posted);

  await page.goto("/");
  await page.getByTestId("top-bar-settings").click();
  await page.getByTestId("settings-nav-upgrade").click();

  await page.getByLabel("Branch").fill("grain/task-31");
  await page.getByLabel(/Wait until this deployment is idle/).check();

  // The button says what it is now going to do, which is the only sign
  // before the click that this is not the same act as the one above.
  const schedule = page.getByRole("button", { name: "Schedule upgrade" });
  await expect(schedule).toBeEnabled();
  await schedule.click();

  await expect
    .poll(() => posted)
    .toEqual([{ branch: "grain/task-31", afterTasksFinish: true }]);

  // What comes back is not an upgrade at all: it is a deployment holding
  // its queue, saying what it is still waiting for and how to call it
  // off.
  const scheduled = page.locator(".upgrade-scheduled");
  await expect(scheduled).toContainText("Waiting to upgrade to grain/task-31");
  await expect(scheduled).toContainText("2 runs and 1 CI run still to finish");

  await scheduled.getByRole("button", { name: "Cancel" }).click();
  await expect(scheduled).toHaveCount(0);
});

test("offers no button on a deployment with no upgrader configured", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByTestId("top-bar-settings").click();
  await page.getByTestId("settings-nav-upgrade").click();

  // The real `grain demo` answer, unintercepted: enabled false, which the
  // panel has to turn into an explanation rather than a form whose every
  // submission would 404.
  await expect(page.getByText(/Not available/i)).toBeVisible();
  await expect(page.getByLabel("Branch")).toHaveCount(0);
});
