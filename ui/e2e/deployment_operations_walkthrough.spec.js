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
import { alertsBell } from "./alerts.js";

test.use({ demoArgs: ["-observability"] });

const TABLET = { width: 820, height: 1180 };

const BASELINE_SETTINGS = {
  configured: true,
  pollInterval: "30s",
  maxMergesPerRepo: 1,
  geminiModel: "gemini-2.5-flash",
  claudeModel: "claude-3-7-sonnet",
  githubHost: "github.com",
};

// Everything this file looks at is the -observability demo's own state
// (pkg/demo/demo_observability.go): its real pkg/ci.Service and job
// cache, its host-directory sandboxes, its secrets store, its `top`
// snapshot and the store's own pipeline pause. Runs are started and
// finished through the same API the UI uses, plus the demo's
// /api/e2e/ci/finish, which releases the one step its workflow holds.
// The only routes stubbed below are the ones whose real answer would
// restart, reboot, power off or upgrade the deployment -- each says so
// where it is installed -- and docs/test-runs/2026-10-02-e2e-own-api-stubs.md
// lists them.

// startHeldRun starts a manual run of the demo's one workflow on
// acme/widgets at ref. Its step waits for /api/e2e/ci/finish, so the run
// holds a live CI sandbox until finishHeldRun lets it go.
//
// Every run here names a branch of its own. The job cache keys on the
// ref, so a finished run on main would turn the next main run into a
// cache hit -- this file's own cancel walkthrough, or observability.spec's
// uncached run when it lands on the same worker's demo after this file.
async function startHeldRun(page, ref) {
  const res = await page.request.post("/api/ci/runs", {
    data: { repo: "acme/widgets", ref },
  });
  expect(res.ok()).toBe(true);
  return (await res.json()).id;
}

async function runConclusion(page, id) {
  const res = await page.request.get(`/api/ci/runs/${id}`);
  return (await res.json()).conclusion || "";
}

// finishHeldRun releases a run startHeldRun began and waits for its
// verdict. The finish control refuses until the run's sandbox is live,
// which is a moment after the run itself exists.
async function finishHeldRun(page, id) {
  await expect
    .poll(async () => (await page.request.post("/api/e2e/ci/finish")).status())
    .toBe(204);
  await expect.poll(() => runConclusion(page, id)).toBe("success");
}

async function pipelinePaused(page) {
  const res = await page.request.get("/api/pipeline-pause");
  return (await res.json()).pause.paused;
}
test.describe("Deployment Settings and Operations Walkthrough", () => {
  test("1. Settings Navigation and Local Preferences", async ({ page }) => {
    await page.setViewportSize({ width: 700, height: 800 });
    await page.goto("/settings/preferences");
    await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();

    // This browser's preferences
    await page.getByLabel("Dark").click();
    await page.getByLabel("Oldest first").click();

    const storedTheme = await page.evaluate(() =>
      localStorage.getItem("grain.themeMode"),
    );
    const storedOrder = await page.evaluate(() =>
      localStorage.getItem("grain.timelineOrder"),
    );
    expect(storedTheme).toBe("dark");
    expect(storedOrder).toBe("oldest");

    await page.reload();
    await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
    const reloadedTheme = await page.evaluate(() =>
      localStorage.getItem("grain.themeMode"),
    );
    const reloadedOrder = await page.evaluate(() =>
      localStorage.getItem("grain.timelineOrder"),
    );
    expect(reloadedTheme).toBe("dark");
    expect(reloadedOrder).toBe("oldest");

    // Settings and the tabs that took Team settings' over
    await page.goto("/admin");
    await expect(
      page.getByRole("heading", { name: "Settings", exact: true }),
    ).toBeVisible();

    // The sections that took Team settings' over
    // (docs/design/settings-panel.md §3.2).
    for (const [id, name] of [
      ["models", "Models"],
      ["git", "Git"],
      ["executors", "Executors"],
      ["tasks", "Tasks"],
    ]) {
      // At 700px the nav is the Section select (§3.6).
      await page.getByRole("combobox", { name: /^Section/ }).click();
      await page.getByRole("option", { name, exact: true }).click();
      await expect(page.getByRole("heading", { name, level: 1 })).toBeVisible();
      await expect(page).toHaveURL(new RegExp(`/settings/${id}$`));
    }
  });

  test("2. General and Engine Settings Validation, Preview, Reset, and Persistence", async ({
    page,
  }) => {
    await page.goto("/admin");
    await expect(
      page.getByRole("heading", { name: "Settings", exact: true }),
    ).toBeVisible();

    const checkRes = await page.request.get("/api/settings");
    const checkJson = await checkRes.json();
    if (!checkJson.configured) {
      const initRes = await page.request.put("/api/settings", {
        data: BASELINE_SETTINGS,
      });
      expect(initRes.ok()).toBe(true);
      await page.reload();
    }

    const envInput = page.getByLabel("Environment name");
    await envInput.fill("ops-walkthrough-2026");
    await envInput.blur();

    const closedCheckbox = page.getByLabel(/Show closed tasks by default/i);
    await closedCheckbox.check();

    await page.getByTestId("settings-nav-engine").click();
    const pollInput = page.getByLabel("Poll interval");
    await pollInput.fill("45s");
    await pollInput.blur();

    const mergesInput = page.getByLabel("Max merges per repo");
    await mergesInput.fill("3");
    await mergesInput.blur();

    // The reload stays on the tab it was taken on (/admin/engine), so the
    // Engine values are read first and General is reached by its tab.
    await page.reload();
    await expect(
      page.getByRole("heading", { name: "Settings", exact: true }),
    ).toBeVisible();
    await expect(page).toHaveURL(/\/settings\/engine$/);
    await expect(page.getByLabel("Poll interval")).toHaveValue("45s");
    await expect(page.getByLabel("Max merges per repo")).toHaveValue("3");

    await page.getByTestId("settings-nav-general").click();
    await expect(page.getByLabel("Environment name")).toHaveValue(
      "ops-walkthrough-2026",
    );
  });

  test("3. Connections and Models Configuration and Fallback Discovery", async ({
    page,
  }) => {
    await page.goto("/settings/models");
    await expect(
      page.getByRole("heading", { name: "Models", level: 1 }),
    ).toBeVisible();

    // The Models tab's one model field is the default model, picked from
    // the models the deployment's connections advertise and saved as soon
    // as it is chosen (settings/ModelDefaults.jsx). There is no per-framework
    // "geminiModel" field on it any more.
    const picker = page.getByRole("combobox", { name: "Default model" });
    await expect(picker).toBeVisible();
    const stored = page.locator('input[name="defaultModel"]');
    const before = await stored.inputValue();
    // Saved by a PUT the pick starts and nothing waits on, so the reload
    // below waits for it rather than racing it. MUI gives each option the
    // value it stands for as data-value; "" is "None".
    const pick = async (value) => {
      const saved = page.waitForResponse(
        (res) =>
          res.request().method() === "PUT" &&
          res.url().endsWith("/api/deployment"),
      );
      await picker.click();
      await page
        .getByRole("listbox")
        .locator(`[role="option"][data-value="${value}"]`)
        .click();
      expect((await saved).ok()).toBe(true);
    };

    // Any advertised model other than the current one.
    await picker.click();
    const values = await page
      .getByRole("listbox")
      .getByRole("option")
      .evaluateAll((els) => els.map((el) => el.getAttribute("data-value")));
    await page.keyboard.press("Escape");
    const chosen = values.find((v) => v && v !== before);
    expect(chosen, `an advertised model besides ${before}`).toBeTruthy();
    await pick(chosen);

    await page.reload();
    await expect(stored).toHaveValue(chosen);

    // Put the demo's own default back for whatever runs after this.
    await pick(before);
    await page.reload();
    await expect(stored).toHaveValue(before);
  });

  test("4. GitHub and Sandbox Configuration with Restart Notices", async ({
    page,
  }) => {
    await page.goto("/admin");

    await page.getByTestId("settings-nav-sandboxes").click();
    // Named exactly: a pattern like /cpu/i or /memory/i also matches the
    // budget's "CPU overcommit" and "Memory overcommit" further down.
    const fields = [
      [page.getByLabel("Sandbox vCPUs", { exact: true }), "3"],
      [page.getByLabel("Sandbox memory (MiB)", { exact: true }), "4096"],
      [page.getByLabel("Sandbox disk (GiB)", { exact: true }), "40"],
      [
        page.getByLabel("Custom sandbox image (OCI reference)", {
          exact: true,
        }),
        "registry.example/demo-ops/sandbox:v1",
      ],
    ];
    for (const [input, value] of fields) {
      await expect(input).toBeVisible();
      await input.fill(value);
      await input.blur();
    }
    await expect(page.getByTestId("settings-status")).toHaveText(
      "All changes saved",
    );

    await page.reload();
    await expect(
      page.getByRole("heading", { name: "Settings", exact: true }),
    ).toBeVisible();
    await page.getByTestId("settings-nav-sandboxes").click();
    for (const [input, value] of fields) {
      await expect(input).toHaveValue(value);
    }
    // The demo cannot inspect a registry, so the custom image's contract
    // is reported unverified rather than silently accepted.
    await expect(page.getByTestId("sandbox-image-warning")).toContainText(
      "unverified",
    );
    // Put the demo back on its stamped image: the server is shared with
    // every later spec in this worker.
    const imageBox = page.getByLabel("Custom sandbox image (OCI reference)", {
      exact: true,
    });
    await imageBox.fill("");
    await imageBox.blur();
    await expect(page.getByTestId("settings-status")).toHaveText(
      "All changes saved",
    );
    await expect(page.getByTestId("sandbox-image-warning")).toHaveCount(0);
  });

  test("5. Capabilities, Fake Secret Validation, Memory, State Repo & Upgrade Planning", async ({
    page,
  }) => {
    // Stubbed, with /api/upgrade/scheduled below: a real POST rebuilds or
    // re-pulls grain and restarts the deployment onto it (pkg/ui/upgrade.go),
    // which is not something a spec may do to the server it is driving.
    // The demo wires no Upgrader at all, so GET is answered here too -- it
    // is what reports the schedule the stubbed POST made -- and the pane
    // is exercised against this stand-in for the whole round trip.
    let upgradeScheduled = null;
    await page.route("**/api/upgrade", async (route) => {
      const req = route.request();
      if (req.method() === "POST") {
        const body = JSON.parse(req.postData() || "{}");
        if (body.afterTasksFinish) {
          upgradeScheduled = {
            branch: body.branch,
            requestedAt: new Date().toISOString(),
            byId: "operator",
            busyRuns: 1,
            busyCiRuns: 0,
          };
          await route.fulfill({
            status: 202,
            json: {
              enabled: true,
              mode: "image",
              running: { image: "ghcr.io/bwsalmon/grain/grain:main" },
              scheduled: upgradeScheduled,
            },
          });
        } else {
          await route.fulfill({
            status: 202,
            json: {
              enabled: true,
              mode: "image",
              running: { image: "ghcr.io/bwsalmon/grain/grain:main" },
              branch: body.branch,
              phase: "running",
            },
          });
        }
      } else {
        await route.fulfill({
          status: 200,
          json: {
            enabled: true,
            mode: "image",
            running: { image: "ghcr.io/bwsalmon/grain/grain:main" },
            scheduled: upgradeScheduled,
          },
        });
      }
    });

    await page.route("**/api/upgrade/scheduled", async (route) => {
      if (route.request().method() === "DELETE") {
        upgradeScheduled = null;
        await route.fulfill({
          status: 200,
          json: {
            enabled: true,
            mode: "image",
            running: { image: "ghcr.io/bwsalmon/grain/grain:main" },
            cancelled: true,
          },
        });
      } else {
        await route.fallback();
      }
    });

    // A fake secret through the demo's real secrets store: it is listed by
    // name and key once set, never by value, and deleting it takes it off
    // the list again.
    await page.goto("/admin");
    await page.getByTestId("settings-nav-credentials").click();
    await page.getByLabel("Secret", { exact: true }).fill("ops-walkthrough");
    await page.getByLabel("Key", { exact: true }).fill("token");
    await page
      .getByLabel("Value", { exact: true })
      .fill("dummy-secret-fake-1234");
    await page.getByRole("button", { name: "Set secret" }).click();
    const secretRow = page
      .locator(".secret-row")
      .filter({ hasText: "ops-walkthrough" });
    await expect(secretRow).toContainText("token");
    await expect(page.getByText("dummy-secret-fake-1234")).toHaveCount(0);
    const listed = await (await page.request.get("/api/secrets")).json();
    expect(listed.secrets).toContainEqual(
      expect.objectContaining({ name: "ops-walkthrough", keys: ["token"] }),
    );
    // Deleting a write-only value is confirmed first: dismissed, it stays.
    page.once("dialog", (dialog) => dialog.dismiss());
    await secretRow.getByRole("button", { name: "Delete secret" }).click();
    await expect(secretRow).toContainText("token");
    page.once("dialog", (dialog) => dialog.accept());
    await secretRow.getByRole("button", { name: "Delete secret" }).click();
    await expect(secretRow).toHaveCount(0);

    await page.goto("/admin");
    await page.getByTestId("settings-nav-config-repo").click();
    await expect(
      page.locator(".state-repo-panel, .pane-form, p").first(),
    ).toBeVisible();

    await page.getByTestId("top-bar-settings").click();
    await page.getByTestId("settings-nav-upgrade").click();
    await expect(
      page.locator("p.upgrade-running, .upgrade-status, p").first(),
    ).toBeVisible();

    const branchInput = page.getByLabel("Branch");
    await branchInput.fill("grain/task-310-upgrade");
    const idleCheck = page.getByLabel(/Wait until this deployment is idle/i);
    await idleCheck.check();

    const scheduleBtn = page.getByRole("button", { name: "Schedule upgrade" });
    await scheduleBtn.click();
    await expect(page.locator(".upgrade-scheduled")).toContainText(
      /Waiting to upgrade to grain\/task-310-upgrade/,
    );

    const cancelSchedBtn = page
      .locator(".upgrade-scheduled")
      .getByRole("button", { name: "Cancel" });
    await cancelSchedBtn.click();
    await expect(page.locator(".upgrade-scheduled")).toHaveCount(0);
  });

  test("6. Sandboxes Observability, Host Pressure and a Live CI Sandbox", async ({
    page,
  }) => {
    // A held run is what puts a sandbox on this pane: the demo's backend
    // is real host directories, one per CI job in flight.
    const runId = await startHeldRun(page, "ops-walkthrough/sandboxes");

    await page.goto("/sandboxes");
    await expect(
      page
        .locator("h1, h2, .list-header-title")
        .filter({ hasText: "Sandboxes" })
        .first(),
    ).toBeVisible();

    await expect(
      page.getByText(/Load average \(1\/5\/15 min\)/i).first(),
    ).toBeVisible();

    // The live table folds its CI rows behind a summary row on load
    // (grain/task-990).
    const ciToggle = page.getByRole("button", {
      name: /^CI sandboxes \(\d+\)$/,
    });
    await expect(ciToggle).toHaveAttribute("aria-expanded", "false", {
      timeout: 10_000,
    });
    await ciToggle.click();
    const ciRow = page
      .getByRole("row")
      .filter({ has: page.getByRole("cell", { name: "ci", exact: true }) });
    await expect(ciRow.first()).toBeVisible({ timeout: 10_000 });

    await finishHeldRun(page, runId);
    await expect(
      page.getByText("Nothing in flight, so no sandboxes exist."),
    ).toBeVisible({ timeout: 10_000 });
  });

  test("7. CI Observability, Log Inspection, Deep-Links & Cancel Control", async ({
    page,
  }) => {
    // A run started from the pane and cancelled from it, through the
    // real DELETE /api/ci/runs/{id}.
    await page.goto("/ci");
    await expect(
      page
        .locator("h1, h2, .list-header-title")
        .filter({ hasText: "CI" })
        .first(),
    ).toBeVisible();
    await page.getByRole("button", { name: "Start a run" }).click();
    const repoSelect = page.locator('select[name="repo"]');
    await expect(repoSelect).toBeVisible();
    await repoSelect.selectOption("acme/widgets");
    await page.getByLabel("Branch").fill("ops-walkthrough/cancel");
    await page.getByRole("button", { name: "Load workflows" }).click();
    await expect(page.getByLabel("Workflow")).toBeVisible();
    await page.getByRole("button", { name: "Start run", exact: true }).click();

    const cancelBtn = page.getByRole("button", { name: "Cancel run" });
    await expect(cancelBtn).toBeVisible();
    await expect(page).toHaveURL(/\/ci\/[0-9a-f]+$/);
    const cancelledId = new URL(page.url()).pathname.split("/").pop();

    page.once("dialog", (dialog) => dialog.dismiss());
    await cancelBtn.click();
    expect(await runConclusion(page, cancelledId)).toBe("");

    page.once("dialog", (dialog) => dialog.accept());
    await cancelBtn.click();
    await expect.poll(() => runConclusion(page, cancelledId)).toBe("cancelled");
    await expect(cancelBtn).toHaveCount(0);

    // A run that finishes, inspected from the list down to its step's
    // own output.
    const runId = await startHeldRun(page, "ops-walkthrough/logs");
    await finishHeldRun(page, runId);

    await page.goto("/ci");
    await expect(page.getByText("acme/widgets").first()).toBeVisible();
    await page.goto(`/ci/${runId}`);
    await expect(page.getByText("observe").first()).toBeVisible();
    await page.getByText("observe").first().click();
    await expect(page.getByText("Wait for the browser")).toBeVisible();
    await page.getByRole("button", { name: "Show output" }).first().click();
    // Exactly the line the step printed: the demo's printf once carried
    // a doubled backslash and the pane showed a literal "\n" after it
    // (docs/test-runs/2026-10-03-deployment-operations.md).
    await expect(
      page.getByText("observed through the sandbox", { exact: true }),
    ).toBeVisible();

    // A task's own run, recorded on the task by POST /api/ci/runs with a
    // taskId. The demo's workflow answers workflow_dispatch only, so a
    // task's push run errors saying so -- a real verdict the task links to.
    const taskRun = await page.request.post("/api/ci/runs", {
      data: { taskId: "2" },
    });
    expect(taskRun.ok()).toBe(true);
    const taskRunId = (await taskRun.json()).id;
    await expect.poll(() => runConclusion(page, taskRunId)).toBe("errored");

    await page.goto("/tasks/2");
    await page.getByRole("button", { name: "Show the run" }).click();
    await expect(
      page.getByText(/no workflow of acme\/widgets answers push/).first(),
    ).toBeVisible();
  });

  test("8. System Logs, Top, Pipeline Pause/Resume & Power Confirmations", async ({
    page,
  }) => {
    let restartCount = 0;
    let rebootCount = 0;
    let shutdownCount = 0;

    // The three power routes are stubbed because their real answer is the
    // deployment going away: a restart, a reboot or a power-off of the
    // machine. The demo wires hooks that refuse rather than act (so the
    // Power tab offers the buttons at all), and the stubs stand in for
    // what each looks like from the browser when it happens -- a restart
    // that answers, a reboot that drops the connection, a shutdown that
    // fails -- which no refusing hook can show.
    await page.route("**/api/host/restart", async (route) => {
      restartCount++;
      await route.fulfill({ status: 200, json: { ok: true } });
    });

    await page.route("**/api/host/reboot", async (route) => {
      rebootCount++;
      await route.abort("connectionreset");
    });

    await page.route("**/api/host/shutdown", async (route) => {
      shutdownCount++;
      await route.fulfill({
        status: 500,
        json: { error: "intercepted demo shutdown failure" },
      });
    });

    await page.goto("/system/logs");
    await expect(
      page.getByRole("heading", { name: "System", exact: true }),
    ).toBeVisible();

    const logsView = page.locator("pre.logs-view");
    await expect(logsView).toBeVisible();
    // A label, not a placeholder (LogsPage.jsx).
    const searchInput = page.getByLabel("Search logs");
    await expect(searchInput).toBeVisible();
    await searchInput.fill("daemon");
    await searchInput.fill("nonexistent-pattern-xyz");
    await expect(page.getByText(/no matching log lines/i)).toBeVisible();
    await searchInput.fill("");
    await expect(page.getByText(/no matching log lines/i)).toHaveCount(0);

    const refreshLogs = page.getByRole("button", { name: /refresh/i }).first();
    await expect(refreshLogs).toBeVisible();
    await refreshLogs.click();

    await page.getByRole("tab", { name: "Top" }).click();
    await expect(page.locator("pre.top-view, pre.logs-view")).toContainText(
      "grain-daemon",
    );
    const autoRefresh = page.getByRole("checkbox", { name: /auto-refresh/i });
    await expect(autoRefresh).toBeVisible();
    await autoRefresh.uncheck();
    const refreshTop = page.getByRole("button", { name: /refresh/i }).first();
    await expect(refreshTop).toBeVisible();
    await refreshTop.click();

    await page.getByRole("tab", { name: "Pipeline" }).click();
    const pauseBtn = page.getByRole("button", { name: "Pause pipeline" });
    page.once("dialog", (d) => d.dismiss());
    await pauseBtn.click();
    expect(await pipelinePaused(page)).toBe(false);

    page.once("dialog", (d) => d.accept());
    await pauseBtn.click();
    await expect.poll(() => pipelinePaused(page)).toBe(true);

    const resumeBtn = page.getByRole("button", { name: "Resume pipeline" });
    page.once("dialog", (d) => d.dismiss());
    await resumeBtn.click();
    expect(await pipelinePaused(page)).toBe(true);

    page.once("dialog", (d) => d.accept());
    await resumeBtn.click();
    await expect.poll(() => pipelinePaused(page)).toBe(false);

    // Scheduled upgrade hold on System → Pipeline warns that resuming cancels
    // the scheduled upgrade.
    let holdActive = true;
    await page.route("**/api/config", async (route) => {
      const response = await route.fetch();
      const json = await response.json();
      if (holdActive) {
        json.pipelinePause = {
          paused: true,
          byKind: "automation",
          byId: "upgrade-schedule",
          until: "2026-10-27T04:00:00Z",
          reason: "Waiting for in-flight runs to finish before upgrading",
        };
      }
      await route.fulfill({ response, json });
    });
    await page.goto("/system/pipeline");
    const upgradeResumeBtn = page.getByRole("button", {
      name: "Resume pipeline",
    });
    await expect(upgradeResumeBtn).toBeVisible();
    let upgradeDialogMessage = "";
    page.once("dialog", (d) => {
      upgradeDialogMessage = d.message();
      d.dismiss();
    });
    await upgradeResumeBtn.click();
    expect(upgradeDialogMessage).toMatch(/resuming cancels that upgrade/i);
    holdActive = false;
    await page.unroute("**/api/config");

    // Operator notification on System → Health immediately refreshes and clears
    // when acknowledged without waiting for the 30s notification poll.
    let notifAcked = false;
    await page.route("**/api/notifications?open=1", async (route) => {
      await route.fulfill({
        status: 200,
        json: [
          {
            id: "notif-e2e-1",
            subject: "runner-quota",
            severity: "warning",
            title: "CI runner quota exhausted",
            body: "No cloud runners available in us-central1.",
            raisedAt: "2026-10-26T15:00:00Z",
            ...(notifAcked ? { ackedAt: "2026-10-26T15:05:00Z" } : {}),
          },
        ],
      });
    });
    await page.route("**/api/notifications/notif-e2e-1/ack", async (route) => {
      notifAcked = true;
      await route.fulfill({ status: 200, json: { status: "ok" } });
    });
    await page.goto("/system");
    const healthPanel = page.getByTestId("system-health");
    await expect(healthPanel).toContainText("CI runner quota exhausted");
    await expect(alertsBell(page)).toHaveAttribute("data-severity", "warning");
    await healthPanel.getByRole("button", { name: "Acknowledge" }).click();
    await expect(healthPanel).not.toContainText("CI runner quota exhausted");
    await expect(alertsBell(page)).not.toHaveAttribute(
      "data-severity",
      "warning",
    );
    await page.unroute("**/api/notifications?open=1");
    await page.unroute("**/api/notifications/notif-e2e-1/ack");

    await page.getByRole("tab", { name: "Power" }).click();
    await expect(page.getByText("Danger zone")).toBeVisible();

    const restartBtn = page.getByRole("button", { name: "Restart grain" });
    page.once("dialog", (d) => d.dismiss());
    await restartBtn.click();
    expect(restartCount).toBe(0);

    page.once("dialog", (d) => d.accept());
    await restartBtn.click();
    await expect.poll(() => restartCount).toBe(1);

    const rebootBtn = page.getByRole("button", { name: "Reboot host" });
    page.once("dialog", (d) => d.dismiss());
    await rebootBtn.click();
    expect(rebootCount).toBe(0);

    page.once("dialog", (d) => d.accept());
    await rebootBtn.click();
    await expect.poll(() => rebootCount).toBe(1);

    const shutdownBtn = page.getByRole("button", { name: "Shut down host" });
    page.once("dialog", (d) => d.dismiss());
    await shutdownBtn.click();
    expect(shutdownCount).toBe(0);

    page.once("dialog", (d) => d.accept());
    await shutdownBtn.click();
    await expect.poll(() => shutdownCount).toBe(1);
    await expect(
      page.getByText(/intercepted demo shutdown failure/i),
    ).toBeVisible();
  });

  test("9. Metrics Dashboard and Refresh", async ({ page }) => {
    await page.goto("/");
    await page.getByTestId("top-bar-system").click();
    await page.getByRole("tab", { name: "Metrics" }).click();
    await expect(page.getByRole("heading", { name: "Metrics" })).toBeVisible();

    await expect(page.getByText(/throughput/i).first()).toBeVisible();
    await expect(page.getByText(/capacity/i).first()).toBeVisible();
    await expect(page.getByText(/latency/i).first()).toBeVisible();
    await expect(page.getByText(/backlog/i).first()).toBeVisible();

    // Refreshing asks the real GET /api/metrics again; the sections stay
    // drawn over its answer.
    const refreshBtn = page.getByRole("button", { name: "Refresh" }).first();
    const refetched = page.waitForResponse(
      (res) => new URL(res.url()).pathname === "/api/metrics" && res.ok(),
    );
    await refreshBtn.click();
    await refetched;
    await expect(page.getByText(/throughput/i).first()).toBeVisible();
  });

  test("10. Responsive Tablet Shell at 820x1180 with a Standing Alert", async ({
    page,
  }) => {
    await page.setViewportSize(TABLET);
    // A real pause, taken through the API and lifted again at the end so
    // the next spec on this worker's demo finds the pipeline running.
    const paused = await page.request.post("/api/pipeline-pause", {
      data: { reason: "Maintenance in progress" },
    });
    expect(paused.ok()).toBe(true);
    try {
      await page.goto("/");
      await expect(alertsBell(page)).toHaveAttribute(
        "data-severity",
        "warning",
      );

      // The gear is in the page header rail, on screen whatever the rail holds.
      const settingsBtn = page.getByTestId("top-bar-settings");
      await expect(settingsBtn).toBeVisible();
      const box = await settingsBtn.boundingBox();
      expect(box.y + box.height).toBeLessThanOrEqual(TABLET.height);

      await settingsBtn.click();
      await expect(
        page.getByRole("heading", { name: "Settings" }),
      ).toBeVisible();
    } finally {
      await page.request.delete("/api/pipeline-pause");
    }
  });
});
