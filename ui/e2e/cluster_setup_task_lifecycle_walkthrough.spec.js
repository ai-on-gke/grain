import { test, expect } from "./fixtures.js";

const BASELINE_SETTINGS = {
  general: {
    environment_name: "prod-cluster-e2e",
    show_closed_tasks: true,
  },
  engine: {
    poll_interval: "30s",
    max_workers: 4,
    max_mergers: 2,
    max_ci_jobs: 4,
  },
  sandboxes: {
    cpus: 4,
    memory_mb: 8192,
    disk_gb: 40,
    network_access: "open",
  },
};

test.describe("Grain Cluster Setup, Task Submission, and Lifecycle Completion Walkthrough", () => {
  test.setTimeout(120_000);

  test("1. Cluster Initialization & Admin Deployment Configuration", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page).toHaveTitle(/grain/i);
    await expect(page.locator(".task-row").first()).toBeVisible();

    await expect(page.getByRole("button", { name: /^Tasks/ })).toBeVisible();
    await expect(page.getByRole("button", { name: /^Inbox/ })).toBeVisible();

    // Navigate to Settings (the old /admin lands on General)
    await page.goto("/admin");
    await expect(
      page.getByRole("heading", { name: "Settings", exact: true }),
    ).toBeVisible();

    // Ensure baseline settings are initialized
    const checkRes = await page.request.get("/api/settings");
    const checkJson = await checkRes.json();
    if (!checkJson.configured) {
      const initRes = await page.request.put("/api/settings", {
        data: BASELINE_SETTINGS,
      });
      expect(initRes.ok()).toBe(true);
      await page.reload();
    }

    // Configure General Deployment Settings
    const envInput = page.getByLabel("Environment name");
    await expect(envInput).toBeVisible();
    await envInput.fill("prod-grain-cluster-alpha");
    await envInput.blur();

    // Configure Engine & Concurrency. There is no deployment-wide worker
    // or CI job count here any more (grain/task-28): the counts belong to
    // the teams, and what bounds this host is the sandbox budget.
    await page.getByTestId("settings-nav-engine").click();
    const pollInput = page.getByLabel("Poll interval");
    await expect(pollInput).toBeVisible();
    await pollInput.fill("45s");
    await pollInput.blur();

    // Configure Sandboxes & MicroVM Infrastructure: the default sandbox
    // shape lives on Settings → Sandboxes (System → Sandboxes is the
    // pool's health page).
    await page.getByTestId("settings-nav-sandboxes").click();
    const cpusInput = page.getByLabel("Sandbox vCPUs");
    await expect(cpusInput).toBeVisible();
    await cpusInput.fill("4");
    await cpusInput.blur();

    // Verify persistence across reload. The reload stays on the section
    // it was taken on, so General is reached from the section nav.
    await page.reload();
    await expect(
      page.getByRole("heading", { name: "Settings", exact: true }),
    ).toBeVisible();
    await expect(page).toHaveURL(/\/settings\/sandboxes$/);
    await expect(page.getByLabel("Sandbox vCPUs")).toHaveValue("4");
    await page.getByTestId("settings-nav-general").click();
    await expect(page.getByLabel("Environment name")).toHaveValue(
      "prod-grain-cluster-alpha",
    );
  });

  test("2. Repository Onboarding, Workflows, and Team Management", async ({
    page,
  }) => {
    // Repository Inspection
    await page.goto("/repos");
    await expect(page.locator(".repo-list")).toBeVisible();
    const defaultRepoLink = page.locator(".repo-list-row").first();
    await expect(defaultRepoLink).toBeVisible();

    // Settings inspection
    await page.goto("/settings");
    await expect(
      page.getByRole("heading", { name: "Settings", exact: true }),
    ).toBeVisible();
  });

  test("3. Task Intake with Full Metadata, Proposals, and Inbox Triage", async ({
    page,
  }) => {
    await page.goto("/");
    const queuedTitle = `E2E Queued Cluster Task ${Date.now()}`;
    const queuedDesc =
      "Validate end-to-end task dispatch and lifecycle completion on new cluster.";

    // 1. Submit Queued Task
    await page.getByRole("button", { name: "+ New task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

    await page.getByLabel("Title").fill(queuedTitle);
    await page.getByLabel("Description").fill(queuedDesc);
    await page.getByLabel(/^Repos/).selectOption("none");

    await page.getByRole("button", { name: "Create task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(
      0,
    );

    // Verify Queued task appears in list
    const queuedRow = page.locator(".task-row", { hasText: queuedTitle });
    await expect(queuedRow).toBeVisible();
    await expect(queuedRow.locator(".badge-queued")).toBeVisible();

    // Open Queued Task Detail
    await queuedRow.click();
    await expect(page.locator(".detail-header")).toBeVisible();
    await expect(page.locator(".detail-header h2")).toContainText(queuedTitle);
    await expect(page.locator(".description")).toContainText(queuedDesc);
    await page.getByRole("button", { name: /Back/ }).first().click();

    // 2. Submit Proposed Task
    const proposedTitle = `E2E Proposal Cluster Task ${Date.now()}`;
    await page.getByRole("button", { name: "+ New task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

    await page.getByLabel("Title").fill(proposedTitle);
    await page
      .getByLabel("Description")
      .fill("Architectural proposal for cluster autoscaling.");
    await page.getByLabel(/^Repos/).selectOption("none");
    await page.getByLabel(/Queue immediately/).uncheck();

    await page.getByRole("button", { name: "Create task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(
      0,
    );

    const proposedRow = page.locator(".task-row", { hasText: proposedTitle });
    await expect(proposedRow).toBeVisible();
    await expect(proposedRow.locator(".badge-proposed")).toBeVisible();

    // 3. Inbox Triage & Proposal Approval
    await page.getByRole("button", { name: /^Inbox/ }).click();
    await expect(page).toHaveURL(/\/inbox$/);
    await expect(page.getByRole("heading", { name: "Inbox" })).toBeVisible();

    // Find the proposal in inbox and approve it
    const inboxProposalRow = page.locator(".inbox-item", {
      hasText: proposedTitle,
    });
    await expect(inboxProposalRow).toBeVisible();
    await inboxProposalRow.getByRole("button", { name: "Approve" }).click();
    await expect(inboxProposalRow).toHaveCount(0);

    // Approval queues it
    await page.getByRole("button", { name: /^Tasks/ }).click();
    await expect(proposedRow.locator(".badge-queued")).toBeVisible();
  });

  test("4. Execution Lifecycle Surfaces: Boards, Gantt, and Navigation", async ({
    page,
  }) => {
    // Boards verification
    await page.goto("/");
    await page.getByRole("button", { name: /^Boards/ }).click();
    const boardOption = page.getByText("Board", { exact: true });
    await expect(boardOption).toBeVisible();
    await boardOption.click();
    await expect(page.locator(".board-column").first()).toBeVisible();

    // Timeline Gantt verification
    await page.getByRole("button", { name: /^Boards/ }).click();
    const timelineRow = page
      .locator(".template-row", { hasText: "Timeline" })
      .first();
    await expect(timelineRow).toBeVisible();
    await timelineRow.click();
    await expect(page.locator(".gantt-row").first()).toBeVisible();

    // Return to main task list
    await page.getByRole("button", { name: /^Tasks/ }).click();
    await expect(page).toHaveURL(/\/(?:\?.*)?$/);
    await expect(page.locator(".task-row").first()).toBeVisible();
  });
});
