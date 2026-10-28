import { expect, test } from "./fixtures.js";

// docs/test-plans/2026-10-15.md §3, steps 3-5 and 7, in a real browser
// over a running demo: the repository page's "Standing work on this
// deployment" switch, flipped off, stops each piece of standing work the
// demo can run; one schedule's own override turns it back on alone; and
// turning the default back on owes no catch-up
// (docs/test-runs/2026-10-15-repo-tabs-standing-work.md).
//
// `grain demo -github-sim -fake-ci -base-branch-ci -scheduled-workflows
// -schedules -reconcile 2s` runs the daemon's own "schedule" reconciler
// (pkg/demo/demo_schedules.go), "declarations" and "scheduled-workflows",
// and judges main on a -fake-ci verdict through the real "base-branch-ci"
// reconciler when POST /api/e2e/base-branch/{red,green} asks. The stubs
// are the demo's own: GitHub is a bare repo on disk and grain CI is
// -fake-ci, which executes nothing. Schedules have no cadence finer than
// an hour, so each firing here is a daily schedule moved to the coming
// minute, and the spec waits for it.
test.use({
  demoArgs: [
    "-github-sim",
    "-fake-ci",
    "-base-branch-ci",
    "-scheduled-workflows",
    "-schedules",
    "-reconcile",
    "2s",
  ],
});
test.describe.configure({ mode: "serial" });
test.setTimeout(300_000);

const repo = "acme/widgets";
const SWITCH = "Standing work on this deployment";

async function ok(res, what) {
  expect(res.ok(), `${what}: ${res.status()} ${await res.text()}`).toBe(true);
  return res.json();
}

async function tasks(request) {
  return ok(await request.get("/api/tasks"), "tasks");
}

async function schedule(request, id) {
  const all = await ok(await request.get("/api/schedules"), "schedules");
  return all.find((s) => s.id === id);
}

async function ownership(request) {
  return ok(
    await request.get(`/api/repos/${repo}/work-ownership`),
    "work ownership",
  );
}

// nextMinute is the "HH:MM" (UTC, the demo schedules' zone) of a minute
// far enough ahead that the PATCH below lands before it starts.
function nextMinute() {
  const at = new Date(
    Date.now() + (new Date().getUTCSeconds() < 45 ? 60_000 : 120_000),
  );
  return `${String(at.getUTCHours()).padStart(2, "0")}:${String(at.getUTCMinutes()).padStart(2, "0")}`;
}

// dueAt moves each schedule's daily firing to timeOfDay, which is also
// what makes it come due today.
async function dueAt(request, ids, timeOfDay) {
  for (const id of ids) {
    const s = await ok(
      await request.patch(`/api/schedules/${id}`, {
        data: { recurrence: { kind: "daily", timeOfDay } },
      }),
      `schedule ${id}`,
    );
    expect(s.nextRunAt).toContain(`T${timeOfDay}:00`);
  }
}

async function setSwitch(page, on) {
  await page.goto(`/repos/${repo}#releases`);
  const box = page.getByRole("switch", { name: SWITCH });
  if ((await box.isChecked()) !== on) await box.click();
  await expect(box).toBeChecked({ checked: on });
  await expect
    .poll(async () => (await ownership(page.request)).defaultRuns)
    .toBe(on);
}

let before;
let skipped;

test("3. off: a due schedule records a skip and files nothing; main is not judged and nothing is triaged", async ({
  page,
}) => {
  await setSwitch(page, false);
  // Counted once the switch is off: the demo's own sched-1 is daily at
  // 00:00, so a run that crosses midnight sees it fire by itself before
  // this, and that task is not one this step files.
  before = (await tasks(page.request)).length;
  const off = await ownership(page.request);
  expect(off.default).toBe("off");
  for (const item of off.items) expect(item.runs, item.item).toBe(false);

  // A by-hand base-branch run is refused, readably, while the check is
  // off (step 7), from the button on the page.
  await page
    .getByRole("button", { name: /run the base-branch check now/i })
    .click();
  await expect(
    page.getByText(/doesn't run acme\/widgets's standing work/).first(),
  ).toBeVisible();

  // The loopback hook asks for a run the way that button does, then runs
  // the real reconciler: nothing is judged, no run starts, nothing is
  // filed to repair it.
  const red = await page.request.post("/api/e2e/base-branch/red", {
    data: { branch: "main" },
  });
  expect(red.status()).toBe(409);
  const runs = await ok(await page.request.get("/api/ci/runs"), "ci runs");
  expect(runs.runs).toEqual([]);

  const at = nextMinute();
  await dueAt(page.request, ["sched-1", "sched-2"], at);
  await expect
    .poll(
      async () =>
        Promise.all(
          ["sched-1", "sched-2"].map(
            async (id) => (await schedule(page.request, id)).skips?.length || 0,
          ),
        ),
      { timeout: 150_000, intervals: [2_000] },
    )
    .toEqual([1, 1]);
  for (const id of ["sched-1", "sched-2"]) {
    const s = await schedule(page.request, id);
    expect(s.skips[0]).toMatchObject({
      reason: "standingWork",
      occurrence: expect.stringContaining(`T${at}:00`),
    });
    // The cadence moved on to tomorrow, with nothing run.
    expect(s.nextRunAt).not.toContain(
      new Date().toISOString().slice(0, 10) + `T${at}`,
    );
    expect(s.lastRunAt).toBeUndefined();
  }
  skipped = (await schedule(page.request, "sched-1")).nextRunAt;

  // A few more reconcile cycles: still nothing filed, nothing run.
  await page.waitForTimeout(6_000);
  expect((await tasks(page.request)).length).toBe(before);
  expect(
    (await ok(await page.request.get("/api/ci/runs"), "ci runs")).runs,
  ).toEqual([]);
});

test("4. a hand-filed task is still accepted while standing work is off", async ({
  page,
}) => {
  const filed = await ok(
    await page.request.post("/api/tasks", {
      data: {
        title: "Hand-filed while standing work is off",
        description: "walk",
        repo,
      },
    }),
    "file task",
  );
  expect(filed.id).toBeTruthy();
  const all = await tasks(page.request);
  expect(all.find((t) => t.id === filed.id)).toBeTruthy();
  before = all.length;
});

test("5. one schedule's override turns only it back on; the default back on owes no catch-up", async ({
  page,
}) => {
  await page.goto(`/repos/${repo}#releases`);
  await page
    .getByRole("button", {
      name: "Weekly Dependency & Vulnerability Audit: on",
    })
    .click();
  await expect
    .poll(async () =>
      (await ownership(page.request)).items
        .filter((i) => i.runs)
        .map((i) => i.item),
    )
    .toEqual(["sched-2"]);

  const at = nextMinute();
  await dueAt(page.request, ["sched-1", "sched-2"], at);
  await expect
    .poll(async () => (await schedule(page.request, "sched-2")).lastRunAt, {
      timeout: 150_000,
      intervals: [2_000],
    })
    .toBeTruthy();
  await expect
    .poll(async () => (await schedule(page.request, "sched-1")).skips?.length, {
      timeout: 30_000,
    })
    .toBe(2);
  const filed = (await tasks(page.request)).filter(
    (t) => t.title === "Weekly Dependency & Vulnerability Audit",
  );
  expect(filed).toHaveLength(1);
  before += 1;

  // The default back on: sched-1's skipped firings are not made up, and
  // its next firing is still tomorrow's.
  const pending = (await schedule(page.request, "sched-1")).nextRunAt;
  await setSwitch(page, true);
  await page.waitForTimeout(6_000);
  expect((await tasks(page.request)).length).toBe(before);
  const s1 = await schedule(page.request, "sched-1");
  expect(s1.lastRunAt).toBeUndefined();
  expect(s1.nextRunAt).toBe(pending);
  expect(s1.nextRunAt > skipped || s1.nextRunAt === skipped).toBe(true);

  // And main is judged again: red, and confirmed -- filing a repair is
  // system:pipeline-steward's, not the daemon's.
  const red = await ok(
    await page.request.post("/api/e2e/base-branch/red", {
      data: { branch: "main" },
    }),
    "red",
  );
  expect(red.conclusion).toBe("failure");
  expect(red.confirmation).toBe("confirmed");
  expect(red.repairTaskId).toBeFalsy();
  await ok(
    await page.request.post("/api/e2e/base-branch/green", {
      data: { branch: "main" },
    }),
    "green",
  );
});
