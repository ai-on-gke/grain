import { test, expect } from "./fixtures.js";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

test.setTimeout(120_000);

test.describe("Closed Task Default Sorting and Lifecycle Visibility Walkthrough", () => {
  test("verifies closed_at DESC default sorting across CLI, REST API and dynamic UI updates", async ({
    page,
    demoServer,
    accessCode,
  }) => {
    const bin = process.env.GRAIN_E2E_DEMO_BIN;
    if (!bin) {
      throw new Error("GRAIN_E2E_DEMO_BIN environment variable is not set");
    }

    const runCli = async (args) => {
      const { stdout } = await execFileAsync(bin, [
        "-server",
        demoServer,
        "-token",
        accessCode,
        ...args,
      ]);
      return stdout;
    };

    const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

    // The demo is single-tenant, so a caller outside the browser shows
    // its access code, as the CLI above does.
    const auth = { Authorization: `Bearer ${accessCode}` };

    // -------------------------------------------------------------------------
    // Phase 1: Create tasks with staggered creation times and distinct metadata
    // -------------------------------------------------------------------------
    const tag = `ST-${Date.now()}`;
    const taskTitles = {
      t1: `[SORT-${tag}] 01 Oldest Task Alpha`,
      t2: `[SORT-${tag}] 02 Middle Task Bravo`,
      t3: `[SORT-${tag}] 03 Middle Task Charlie`,
      t4: `[SORT-${tag}] 04 Newer Task Delta`,
      t5: `[SORT-${tag}] 05 Newest Task Echo`,
    };

    // Helper to create task via REST API
    const createTask = async (title, repo) => {
      const res = await fetch(`${demoServer}/api/tasks`, {
        method: "POST",
        headers: { "Content-Type": "application/json", ...auth },
        body: JSON.stringify({
          title,
          repo,
          approved: true,
        }),
      });
      const data = await res.json();
      if (!res.ok) {
        throw new Error(
          `POST /api/tasks failed (${res.status}): ${JSON.stringify(data)}`,
        );
      }
      return data;
    };

    // Helper to close task via REST API
    const closeTask = async (id, reason = "verification completed") => {
      const res = await fetch(`${demoServer}/api/tasks/${id}/close`, {
        method: "POST",
        headers: { "Content-Type": "application/json", ...auth },
        body: JSON.stringify({ reason }),
      });
      expect(res.status).toBe(200);
    };

    // Create 5 tasks with staggered delays to ensure distinct created_at timestamps
    const task1 = await createTask(taskTitles.t1, "acme/widgets");
    await sleep(250);
    const task2 = await createTask(taskTitles.t2, "acme/widgets");
    await sleep(250);
    const task3 = await createTask(taskTitles.t3, "acme/widgets");
    await sleep(250);
    const task4 = await createTask(taskTitles.t4, "acme/widgets");
    await sleep(250);
    const task5 = await createTask(taskTitles.t5, "acme/widgets");

    // -------------------------------------------------------------------------
    // Phase 2: Close tasks in non-chronological order
    // -------------------------------------------------------------------------
    // Order of closing:
    // 1. Task 2 (Bravo) closed first (earliest closed_at)
    // 2. Task 4 (Delta) closed second
    // 3. Task 1 (Alpha - oldest created task) closed third (latest/most recent closed_at)
    // Tasks 3 and 5 remain open.

    await sleep(300);
    await closeTask(task2.id); // T_close_1 (Bravo)
    await sleep(400);
    await closeTask(task4.id); // T_close_2 (Delta)
    await sleep(400);
    await closeTask(task1.id); // T_close_3 (Alpha - oldest created, closed most recently)

    // -------------------------------------------------------------------------
    // Phase 3: REST API and CLI Verification of Default Sorting (closed_at DESC)
    // -------------------------------------------------------------------------
    // 3.1 Verify GET /api/tasks
    const getTasksRes = await fetch(`${demoServer}/api/tasks`, {
      headers: auth,
    });
    expect(getTasksRes.status).toBe(200);
    const allTasks = await getTasksRes.json();

    const closedSubset = allTasks.filter(
      (t) => t.id === task1.id || t.id === task2.id || t.id === task4.id,
    );

    // Assert closed tasks are strictly ordered closed_at DESC:
    // Expected order: Task 1 (Alpha, T3), Task 4 (Delta, T2), Task 2 (Bravo, T1)
    expect(closedSubset.map((t) => t.id)).toEqual([
      task1.id,
      task4.id,
      task2.id,
    ]);
    expect(closedSubset.map((t) => t.title)).toEqual([
      taskTitles.t1,
      taskTitles.t4,
      taskTitles.t2,
    ]);

    // 3.2 Verify CLI `grain list -state closed`
    const cliOutputClosed = await runCli(["list", "-state", "closed"]);
    const idx1 = cliOutputClosed.indexOf(taskTitles.t1);
    const idx4 = cliOutputClosed.indexOf(taskTitles.t4);
    const idx2 = cliOutputClosed.indexOf(taskTitles.t2);

    expect(idx1).toBeGreaterThan(-1);
    expect(idx4).toBeGreaterThan(-1);
    expect(idx2).toBeGreaterThan(-1);
    expect(idx1).toBeLessThan(idx4);
    expect(idx4).toBeLessThan(idx2);

    // 3.3 Verify CLI `grain -json list -state closed`
    const cliJsonRaw = await runCli(["-json", "list", "-state", "closed"]);
    const cliJsonTasks = JSON.parse(cliJsonRaw);
    const jsonClosedSubset = cliJsonTasks.filter(
      (t) => t.id === task1.id || t.id === task2.id || t.id === task4.id,
    );
    expect(jsonClosedSubset.map((t) => t.id)).toEqual([
      task1.id,
      task4.id,
      task2.id,
    ]);

    // -------------------------------------------------------------------------
    // Phase 4: Web UI Closed View & Dynamic Real-Time Completion Verification
    // -------------------------------------------------------------------------
    // Navigate to Closed view via URL query parameter (?f=state:any:closed)
    // The list is the deployment's, every task in it (single-tenant
    // reset, stage 3e-ii): Task 1, then Task 4, then Task 2.
    await page.goto("/?f=state:any:closed");
    await expect(page.locator(".task-row").first()).toBeVisible();
    await expect
      .poll(async () => {
        const joinedRows = (
          await page.locator(".task-row").allTextContents()
        ).join("\n");
        const uiPos1 = joinedRows.indexOf(taskTitles.t1);
        const uiPos4 = joinedRows.indexOf(taskTitles.t4);
        const uiPos2 = joinedRows.indexOf(taskTitles.t2);
        return uiPos1 > -1 && uiPos1 < uiPos4 && uiPos4 < uiPos2;
      })
      .toBe(true);

    // 4.2 Dynamic Real-Time Re-Sorting: Close Task 3 (Charlie) and verify dynamic placement
    // Task 3 was open. Closing it now should make it the most recently closed task,
    // placing it immediately at the very top of the Closed list above Task 1.
    await sleep(300);
    await closeTask(task3.id);

    // Wait for the UI poller to dynamically reflect the newly closed task at the top
    await expect
      .poll(async () => {
        const rows = await page.locator(".task-row").allTextContents();
        const text = rows.join("\n");
        const pos3 = text.indexOf(taskTitles.t3);
        const pos1 = text.indexOf(taskTitles.t1);
        return pos3 > -1 && pos1 > -1 && pos3 < pos1;
      })
      .toBe(true);

    // -------------------------------------------------------------------------
    // Phase 5: Explicit Sort Override and Recovery
    // -------------------------------------------------------------------------
    // Explicit sort by Title ASC: grain list -state closed -sort title:asc
    const cliSortedTitle = await runCli([
      "list",
      "-state",
      "closed",
      "-sort",
      "title:asc",
    ]);
    const titleIdx1 = cliSortedTitle.indexOf(taskTitles.t1);
    const titleIdx2 = cliSortedTitle.indexOf(taskTitles.t2);
    const titleIdx3 = cliSortedTitle.indexOf(taskTitles.t3);
    const titleIdx4 = cliSortedTitle.indexOf(taskTitles.t4);

    expect(titleIdx1).toBeLessThan(titleIdx2);
    expect(titleIdx2).toBeLessThan(titleIdx3);
    expect(titleIdx3).toBeLessThan(titleIdx4);

    // UI Explicit sort via URL parameter ?sort=title:asc
    await page.goto("/?f=state:any:closed&sort=title:asc");
    await expect
      .poll(async () => {
        const text = (await page.locator(".task-row").allTextContents()).join(
          "\n",
        );
        const at = [
          taskTitles.t1,
          taskTitles.t2,
          taskTitles.t3,
          taskTitles.t4,
        ].map((t) => text.indexOf(t));
        return at.every((p, i) => p > -1 && (i === 0 || at[i - 1] < p));
      })
      .toBe(true);

    // Default sort restores closed_at DESC
    const cliRestored = await runCli(["list", "-state", "closed"]);
    expect(cliRestored.indexOf(taskTitles.t3)).toBeLessThan(
      cliRestored.indexOf(taskTitles.t1),
    );
    expect(cliRestored.indexOf(taskTitles.t1)).toBeLessThan(
      cliRestored.indexOf(taskTitles.t4),
    );
    expect(cliRestored.indexOf(taskTitles.t4)).toBeLessThan(
      cliRestored.indexOf(taskTitles.t2),
    );
  });
});
