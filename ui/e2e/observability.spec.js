import { test, expect } from "./fixtures.js";

// This scenario is intentionally not built from intercepted snapshots. The
// opt-in demo owns pkg/ci.Service, an on-disk DirCache, HostSandboxes, the
// daemon's sandbox-health adapter, and the ordinary UI server. The tiny e2e
// endpoints only hold/release work so a browser can observe states that would
// otherwise pass in a fraction of a second.
test.use({ demoArgs: ["-observability"] });

async function startRun(page) {
  await page.getByRole("button", { name: "Start a run" }).click();
  const repoSelect = page.locator('select[name="repo"]');
  await expect(repoSelect).toBeVisible();
  await repoSelect.selectOption("acme/widgets");
  await page.getByLabel("Branch").fill("main");
  // A manual run names the workflow it is asking for, so the form will
  // not submit until one has been discovered at that ref and chosen
  // (CIPage.jsx's StartRunForm). The demo's repo has exactly one, which
  // the form selects for the reader -- so this waits for the picker to
  // appear rather than choosing from it.
  await page.getByRole("button", { name: "Load workflows" }).click();
  await expect(page.getByLabel("Workflow")).toBeVisible();
  await page.getByRole("button", { name: "Start run", exact: true }).click();
}

function sandboxRow(page, identity) {
  return page
    .getByRole("row")
    .filter({ has: page.getByRole("cell", { name: identity, exact: true }) });
}

async function currentLabels(row) {
  return row.evaluate((element) => {
    const cells = [...element.querySelectorAll("td")];
    const load = Number(cells[8].textContent.trim().split(" ")[0]);
    const memory = Number(
      cells[10].textContent.trim().match(/^(\d+) \/ 1000 MB$/)?.[1],
    );
    const trend = (index) =>
      cells[index].querySelector("span > span")?.textContent.trim();
    return {
      load,
      memory,
      cpuTrend: trend(9),
      memoryTrend: trend(12),
      runningFor: cells[5].textContent.trim(),
    };
  });
}

test("traces an uncached CI sandbox through polling and serves the identical run from cache", async ({
  page,
}) => {
  test.setTimeout(90_000);
  // Acquires are counted for the demo's whole life, and another
  // -observability spec on this worker may have run CI first
  // (deployment_operations_walkthrough.spec.js does), so this test counts
  // from where it found the backend.
  const acquiresBefore = (
    await (await page.request.get("/api/e2e/sandboxes/acquires")).json()
  ).acquires;
  await page.goto("/");
  await page.getByRole("button", { name: /^CI \d+$/ }).click();
  await expect(page.getByRole("heading", { name: "CI" })).toBeVisible();

  await startRun(page);
  await expect(page.getByRole("button", { name: "Cancel run" })).toBeVisible();
  await page.getByRole("button", { name: "Back" }).click();

  await expect(
    page.getByRole("heading", { name: "Active sandboxes (1)" }),
  ).toBeVisible({ timeout: 10_000 });
  // Collapsed to its one-line summary on load; the table is a click away.
  const sandboxesToggle = page.getByRole("button", {
    name: /Active sandboxes \(1\)/,
  });
  await expect(sandboxesToggle).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator("#ci-active-sandboxes-summary")).toContainText(
    "1 CI sandbox running",
  );
  await expect(page.locator("#ci-active-sandboxes table")).toHaveCount(0);
  await sandboxesToggle.click();
  await expect(sandboxesToggle).toHaveAttribute("aria-expanded", "true");
  await expect(
    page.locator("#ci-active-sandboxes").getByRole("columnheader", {
      name: "Sandbox",
    }),
  ).toBeVisible();

  await page.getByRole("button", { name: "System" }).click();
  await page.getByRole("tab", { name: "Sandboxes" }).click();
  await expect(page.getByRole("heading", { name: "Sandboxes" })).toBeVisible();

  // The live table folds its CI rows behind a summary row on load
  // (grain/task-990); the row this test follows is one click away.
  const ciToggle = page.getByRole("button", { name: "CI sandboxes (1)" });
  await expect(ciToggle).toHaveAttribute("aria-expanded", "false", {
    timeout: 10_000,
  });
  // The demo's HostSandboxes reports when it acquired each sandbox, so
  // the summary names the longest-running one (grain/task-1077).
  await expect(page.locator("#ci-sandboxes-summary")).toContainText(
    /1 CI sandbox.* · longest \d+[smh]( \d+[sm])? \(c[^)]*\)/,
  );
  await ciToggle.click();
  await expect(ciToggle).toHaveAttribute("aria-expanded", "true");

  const ciRow = page
    .getByRole("row")
    .filter({ has: page.getByRole("cell", { name: "ci", exact: true }) });
  await expect(ciRow).toBeVisible();
  const ciIdentity = await ciRow.getByRole("cell").first().innerText();
  expect(ciIdentity).toMatch(/^c/);

  const firstLabels = await currentLabels(ciRow);
  expect(firstLabels.memory).toBeGreaterThan(100);
  expect(firstLabels.cpuTrend).toBe(firstLabels.load.toFixed(2));
  expect(firstLabels.memoryTrend).toBe(`${firstLabels.memory} MB`);
  expect(firstLabels.runningFor).toMatch(/^\d+[smh]/);

  // Insert an agent sandbox whose name sorts before the CI sandbox. The next
  // health poll prepends its row. Each current label must still end in the
  // series belonging to that row's sandbox identity, not the former array
  // position.
  const added = await page.request.post("/api/e2e/sandboxes/other");
  expect(added.ok()).toBe(true);

  const otherRow = sandboxRow(page, "1-identity-check");
  await expect(otherRow).toBeVisible({ timeout: 10_000 });
  const laterLabels = await currentLabels(ciRow);
  expect(laterLabels.memory).toBeGreaterThan(firstLabels.memory);
  expect(laterLabels.cpuTrend).toBe(laterLabels.load.toFixed(2));
  expect(laterLabels.memoryTrend).toBe(`${laterLabels.memory} MB`);
  const otherLabels = await currentLabels(otherRow);
  expect(otherLabels.memory).toBeGreaterThan(800);
  expect(otherLabels.cpuTrend).toBe(otherLabels.load.toFixed(2));
  expect(otherLabels.memoryTrend).toBe(`${otherLabels.memory} MB`);

  const released = await page.request.delete("/api/e2e/sandboxes/other");
  expect(released.ok()).toBe(true);
  await expect(otherRow).toHaveCount(0, { timeout: 10_000 });

  const finished = await page.request.post("/api/e2e/ci/finish");
  expect(finished.ok()).toBe(true);
  await expect(
    page.getByText("Nothing in flight, so no sandboxes exist."),
  ).toBeVisible({ timeout: 10_000 });

  await page.getByRole("button", { name: /^CI/ }).click();
  await expect(page.getByText("1 job, all green", { exact: true })).toBeVisible(
    {
      timeout: 10_000,
    },
  );

  // The same repo, ref, resolved commit and workflow fingerprint is a cache
  // hit. It gets a distinct run and visible result, but Engine checks the
  // cache before acquiring capacity.
  await startRun(page);
  await expect(page.getByText("Cache hit", { exact: true })).toBeVisible({
    timeout: 10_000,
  });
  const acquired = await page.request.get("/api/e2e/sandboxes/acquires");
  expect(await acquired.json()).toEqual({ acquires: acquiresBefore + 2 });

  await page.getByRole("button", { name: "Back" }).click();
  await expect(
    page.getByText("1 job, all green · 1 cache hit", { exact: true }),
  ).toBeVisible({ timeout: 10_000 });
  await page.getByRole("button", { name: "System" }).click();
  await page.getByRole("tab", { name: "Sandboxes" }).click();
  await expect(
    page.getByText("Nothing in flight, so no sandboxes exist."),
  ).toBeVisible();
});
