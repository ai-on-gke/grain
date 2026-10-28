import { test, expect } from "./fixtures.js";

// The first test triages the inbox rows `grain demo` seeds -- one in
// each of its four groups -- and approves and answers two of them, so it
// asks for a demo of its own: on a shared worker an earlier file (e.g.
// inbox.spec.js) may already have spent those rows. The flag repeats the
// fixture's own `-open=false`; a distinct demoArgs is what puts this file
// on a worker, and a server, no other file shares.
test.use({ demoArgs: ["-open=false"] });
test.setTimeout(120_000);

test.describe("Task Intake, Inbox, and Lifecycle Operator Walkthrough", () => {
  test("thoroughly exercises inbox, new task intake, search/filter, task detail lifecycle, and bulk edit in a real browser", async ({
    page,
  }) => {
    // -----------------------------------------------------------------------
    // 1. INBOX: Cold load, inspection, row navigation, approval, reply, empty state
    // -----------------------------------------------------------------------

    // 1a. Cold load /inbox directly
    await page.goto("/inbox");
    await expect(page.getByRole("heading", { name: "Inbox" })).toBeVisible();
    await expect(page.locator(".inbox-group").first()).toBeVisible();

    // Verify inbox triage groups render
    const proposalGroup = page.locator(".inbox-group", {
      has: page.getByText("Approve a proposal"),
    });
    const questionGroup = page.locator(".inbox-group", {
      has: page.getByText("Asked: reply"),
    });
    const submitGroup = page.locator(".inbox-group", {
      has: page.getByText("Asked: submit for merge"),
    });

    // The demo seeds tasks across all three user-waiting groups.
    await expect(proposalGroup).toBeVisible();
    await expect(questionGroup).toBeVisible();
    await expect(submitGroup).toBeVisible();

    // 1b. Test row navigation into detail overlay and back
    const firstProposal = proposalGroup.locator(".inbox-item").first();
    await expect(firstProposal).toBeVisible();
    await firstProposal.click();
    await expect(page.locator(".detail-header h2")).toBeVisible();
    await page.locator(".overlay-pane-back button").click();
    await expect(page).toHaveURL(/\/inbox$/);

    // 1c. Test rail navigation: Tasks -> Inbox
    await page.getByRole("button", { name: /^Tasks/ }).click();
    await expect(page.locator(".task-row").first()).toBeVisible();
    await page.getByRole("button", { name: /^Inbox/ }).click();
    await expect(page).toHaveURL(/\/inbox$/);

    // 1d. Approve the seeded proposal from Inbox
    const seededProposalRow = page.locator(".inbox-item", {
      hasText: "Add a dark mode toggle",
    });
    await expect(seededProposalRow).toBeVisible();
    await seededProposalRow.getByRole("button", { name: "Approve" }).click();
    await expect(
      page.locator(".inbox-item", { hasText: "Add a dark mode toggle" }),
    ).toHaveCount(0);

    // 1e. Answer the seeded parked question from Inbox
    const seededQuestionRow = page.locator(".inbox-item", {
      hasText: "Investigate the merge queue stall",
    });
    await expect(seededQuestionRow).toBeVisible();
    await seededQuestionRow.getByRole("button", { name: "Reply" }).click();
    await expect(seededQuestionRow.locator(".inbox-question")).toBeVisible();
    await seededQuestionRow
      .getByRole("textbox")
      .fill("Fix both PRs and unblock the queue immediately.");
    await seededQuestionRow.getByRole("button", { name: "Send" }).click();
    await expect(
      page.locator(".inbox-item", {
        hasText: "Investigate the merge queue stall",
      }),
    ).toHaveCount(0);

    // 1f. Inspect action buttons on the submit and question groups
    const submitItem = submitGroup.locator(".inbox-item").first();
    await expect(submitItem).toBeVisible();
    await expect(
      submitItem.getByRole("button", { name: "Submit" }),
    ).toBeVisible();

    const ackItem = questionGroup.locator(".inbox-item").first();
    await expect(ackItem).toBeVisible();
    await expect(
      ackItem.getByRole("button", { name: "Acknowledge" }),
    ).toBeVisible();

    // Verify approved and answered tasks landed in queued state
    await page.getByRole("button", { name: /^Tasks/ }).click();
    const darkModeRow = page.locator(".task-row", {
      hasText: "Add a dark mode toggle",
    });
    await expect(darkModeRow).toBeVisible();
    await expect(darkModeRow.locator(".badge-queued")).toBeVisible();

    const stallRow = page.locator(".task-row", {
      hasText: "Investigate the merge queue stall",
    });
    await expect(stallRow).toBeVisible();
    await expect(stallRow.locator(".badge-queued")).toBeVisible();

    // -----------------------------------------------------------------------
    // 2. TASK INTAKE / NEW TASK FORM: Validation, cancellation, queued and proposed
    // -----------------------------------------------------------------------

    await page.getByRole("button", { name: "+ New task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

    // The form opens on its Title box, so a title typed straight after
    // "+ New task" lands there (2026-10-15 walkthrough: focus sat on the
    // dialog's paper and the keystrokes went nowhere).
    await expect(page.getByLabel("Title")).toBeFocused();
    await page.keyboard.type("Typed on open");
    await expect(page.getByLabel("Title")).toHaveValue("Typed on open");

    // 2a. Validation check: Title is required
    const createBtn = page.getByRole("button", { name: "Create task" });
    await page.getByLabel("Title").fill("");
    await expect(createBtn).toBeDisabled();

    // 2b. Cancel / Discard draft check
    await page.getByLabel("Title").fill("Discardable draft task");
    await page.getByRole("button", { name: "Discard draft" }).click();
    await expect(page.getByLabel("Title")).toHaveValue("");
    await page.getByRole("button", { name: "Close dialog" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(
      0,
    );

    // 2c. Create a fully configured queued task with repo, custom fields, and advanced options
    const queuedTimestamp = Date.now();
    const queuedTitle = `E2E Queued Intake Task ${queuedTimestamp}`;
    const queuedDesc =
      "End-to-end task intake verification with extensive options and parameters.";

    await page.getByRole("button", { name: "+ New task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

    await page.getByLabel("Title").fill(queuedTitle);
    await page.getByLabel("Description").fill(queuedDesc);

    // Repos and base branch
    await page.getByLabel(/^Repos/).selectOption("acme/widgets");
    await page.getByLabel("Base branch").fill("main");

    // Auto-merge checkbox
    const autoMergeCheckbox = page.getByRole("checkbox", {
      name: "Auto-merge once checks pass",
    });
    await expect(autoMergeCheckbox).toBeVisible();
    await autoMergeCheckbox.check();

    // Custom fields
    await page.getByLabel("Priority").click();
    await page.getByRole("option", { name: "high" }).click();

    await page.getByLabel("Area").click();
    await page.getByRole("option", { name: "frontend" }).click();

    await page.getByLabel("Requested by").fill("OperatorTeam");
    await page.getByLabel("Estimate (days)").fill("4");
    await page.getByLabel("Customer-facing").click();
    await page.getByRole("option", { name: "Yes", exact: true }).click();

    // Advanced options
    await page.getByText("Advanced options").click();
    await page
      .getByLabel("Prompt extension override")
      .fill("Strictly verify all code style and types.");

    // Network access: an override rather than the deployment default, so
    // the detail pane below has one to show.
    const netAccess = page.getByLabel("Network access");
    await expect(netAccess).toBeVisible();
    await netAccess.click();
    await page
      .getByRole("option", { name: "None (this deployment only)" })
      .click();

    // Resource options
    const cpusField = page.getByLabel(/vCPUs/);
    await expect(cpusField).toBeEnabled();
    await cpusField.fill("2");
    const memField = page.getByLabel(/Memory \(MiB\)/);
    await expect(memField).toBeEnabled();
    await memField.fill("4096");
    const diskField = page.getByLabel(/Disk \(GiB\)/);
    await expect(diskField).toBeEnabled();
    await diskField.fill("20");

    // Submit queued task
    await page.getByRole("button", { name: "Create task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(
      0,
    );

    // Switch to Tasks view and verify queued task appears
    await page.getByRole("button", { name: /^Tasks/ }).click();
    const queuedRow = page.locator(".task-row", { hasText: queuedTitle });
    await expect(queuedRow).toBeVisible();
    await expect(queuedRow.locator(".badge-queued")).toBeVisible();
    await expect(queuedRow.locator(".chip-field")).toContainText("high");

    // 2d. Create a proposed task with "No repo"
    const proposedTimestamp = Date.now();
    const proposedTitle = `E2E Proposed Architecture Task ${proposedTimestamp}`;
    const proposedDesc =
      "Unassigned architectural proposal for distributed queue design.";

    await page.getByRole("button", { name: "+ New task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();

    await page.getByLabel("Title").fill(proposedTitle);
    await page.getByLabel("Description").fill(proposedDesc);
    // A repo picked first, with a Base branch, and then given up for No
    // repo: the branch used to be filed anyway and shown as the no-repo
    // task's target (docs/test-runs/
    // 2026-10-13-task-intake-inbox-lifecycle.md).
    await page.getByLabel(/^Repos/).selectOption("acme/widgets");
    await page.getByLabel("Base branch").fill("release-9");
    await page.getByLabel(/^Repos/).selectOption("none");

    // Uncheck Queue immediately to file as proposal
    await page.getByRole("checkbox", { name: "Queue immediately" }).uncheck();

    // Area by keyboard alone: an option typed and entered, then one typed
    // in full and left in the box when Create is clicked. Both used to be
    // dropped without a word (docs/test-runs/
    // 2026-10-12-task-intake-inbox-lifecycle.md).
    const areaBox = page.getByRole("combobox", { name: "Area" });
    await areaBox.fill("backe");
    await areaBox.press("Enter");
    await areaBox.fill("docs");

    await page.getByRole("button", { name: "Create task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toHaveCount(
      0,
    );

    // Verify proposed task in list
    const proposedRow = page.locator(".task-row", { hasText: proposedTitle });
    await expect(proposedRow).toBeVisible();
    await expect(proposedRow.locator(".badge-proposed")).toBeVisible();
    const filed = (await (await page.request.get("/api/tasks")).json()).find(
      (t) => t.title === proposedTitle,
    );
    expect(filed.customFields.area).toEqual(["backend", "docs"]);
    expect(filed.repo ?? "").toBe("");
    expect(filed.base ?? "").toBe("");
    await expect(proposedRow).not.toContainText("release-9");

    // -----------------------------------------------------------------------
    // 3. ALL TASKS, STATE VIEWS, SEARCH, SORT, FILTER, AND URL PERSISTENCE
    // -----------------------------------------------------------------------

    // 3a. Search filtering
    await page.getByPlaceholder("Search tasks…").fill(queuedTitle);
    await expect(page.locator(".task-row")).toHaveCount(1);
    await expect(
      page.locator(".task-row", { hasText: queuedTitle }),
    ).toBeVisible();
    expect(page.url()).toContain("q=");

    // Refresh page and verify search persistence in URL
    await page.reload();
    await expect(page.locator(".task-row")).toHaveCount(1);
    await expect(
      page.locator(".task-row", { hasText: queuedTitle }),
    ).toBeVisible();

    // Clear search
    await page.getByPlaceholder("Search tasks…").fill("");

    // 3b. State filter from rail
    await page.getByRole("button", { name: /^Waiting\s+\d+/ }).click();
    await expect(
      page.locator(".task-row", { hasText: queuedTitle }),
    ).toBeVisible();
    await expect(
      page.locator(".task-row", { hasText: proposedTitle }),
    ).toHaveCount(0);

    // 3c. Compound narrowing filter
    await page.getByRole("button", { name: /^Tasks/ }).click();
    await page
      .getByTitle("Add and edit the conditions this list is narrowed by")
      .click();

    // Add Priority = high condition
    await page
      .getByRole("button", { name: /^(Filter by…|Add filter)$/ })
      .click();
    await page.getByRole("menuitem", { name: "Priority" }).click();
    await expect(page.getByRole("menu")).not.toBeVisible();
    await page
      .getByLabel(/^Value for filter \d+$/)
      .last()
      .click();
    await page.getByRole("option", { name: "high" }).click();
    await page.keyboard.press("Escape");

    // Verify filtered list shows our queued high-priority task
    await expect(
      page.locator(".task-row", { hasText: queuedTitle }),
    ).toBeVisible();

    // 3d. Browser history navigation (Back / Forward)
    await page.goBack();
    await page.goForward();
    await expect(
      page.locator(".task-row", { hasText: queuedTitle }),
    ).toBeVisible();

    // Clear filters
    const clearBtn = page.getByRole("button", { name: "Clear", exact: true });
    await expect(clearBtn).toBeVisible();
    await clearBtn.click();

    // -----------------------------------------------------------------------
    // 4. TASK DETAIL: Editing, Defer/Undefer, Proposal Approval, Run & Transcripts
    // -----------------------------------------------------------------------

    // 4a. Open detail and edit Title & Description
    await queuedRow.click();
    await expect(
      page.locator(".detail-header h2", { hasText: queuedTitle }),
    ).toBeVisible();

    // The network override filed above is on the task's own page
    // (docs/test-runs/2026-10-11-task-intake-inbox-lifecycle.md: it had
    // no row there at all).
    await expect(
      page.locator(".declared-row", { hasText: "Network access" }),
    ).toContainText("None (this deployment only)");

    await page
      .locator(".detail-header")
      .getByRole("button", { name: "Edit" })
      .click();
    const updatedTitle = `${queuedTitle} (Updated)`;
    await page.getByLabel("Title").fill(updatedTitle);
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect(
      page.locator(".detail-header h2", { hasText: updatedTitle }),
    ).toBeVisible();

    // 4b. Edit Custom Fields
    await page.getByRole("button", { name: "Edit fields" }).click();
    await page.getByLabel("Priority").click();
    await page.getByRole("option", { name: "medium" }).click();
    await page.getByLabel("Requested by").fill("CoreEngineering");
    await page.getByRole("button", { name: "Save fields" }).click();

    const declaredRows = page.locator(".declared-row");
    await expect(declaredRows.filter({ hasText: "Priority" })).toContainText(
      "medium",
    );
    await expect(
      declaredRows.filter({ hasText: "Requested by" }),
    ).toContainText("CoreEngineering");

    // 4c. Edit Target Repo & Base Branch
    const baseBranchInput = page.getByLabel("Base branch");
    await expect(baseBranchInput).toBeVisible();
    await baseBranchInput.fill("release-v2");
    await baseBranchInput.press("Enter");
    await expect(baseBranchInput).toHaveValue("release-v2");

    // 4d. Toggle Auto-merge checkbox in Actions. The task was filed with
    // auto-merge checked above, so this unchecks it.
    const detailAutoMerge = page.getByLabel("Auto-merge once checks pass");
    await expect(detailAutoMerge).toBeChecked();
    await detailAutoMerge.click();
    await expect(detailAutoMerge).not.toBeChecked();

    // 4e. Defer indefinitely and undefer
    await page.getByRole("button", { name: "Defer" }).click();
    await expect(
      page.locator(".detail-layout .badge-deferred").first(),
    ).toBeVisible();

    await page.getByRole("button", { name: "Undefer" }).click();
    await expect(
      page.locator(".detail-layout .badge-queued").first(),
    ).toBeVisible();

    // 4f. Defer with a wake date
    await page.getByLabel("Until (optional)").fill("2026-12-31");
    await page.getByRole("button", { name: "Defer" }).click();
    await expect(
      page.locator(".detail-layout .badge-deferred").first(),
    ).toBeVisible();
    await expect(page.getByText(/Picks itself back up on/)).toBeVisible();

    await page.getByRole("button", { name: "Undefer" }).click();
    await expect(
      page.locator(".detail-layout .badge-queued").first(),
    ).toBeVisible();

    // Close detail overlay
    await page.locator(".overlay-pane-back button").click();

    // 4g. Approve proposed task from detail view
    await proposedRow.click();
    await expect(
      page.locator(".detail-header h2", { hasText: proposedTitle }),
    ).toBeVisible();
    await expect(
      page.locator(".detail-layout .badge-proposed").first(),
    ).toBeVisible();

    await page.getByRole("button", { name: "Approve" }).click();
    await expect(
      page.locator(".detail-layout .badge-queued").first(),
    ).toBeVisible();

    await page.locator(".overlay-pane-back button").click();

    // 4h. Inspect running task timeline and transcript overlay if available
    const runningRow = page.locator(".task-row", {
      hasText: "Bump the Go toolchain to 1.24",
    });
    await expect(runningRow).toBeVisible();
    await runningRow.click();
    await expect(
      page.locator(".detail-header h2", {
        hasText: "Bump the Go toolchain to 1.24",
      }),
    ).toBeVisible();

    // Timeline attempt inspection: the demo seeds this task's attempt
    // with a transcript.
    const attemptItem = page.locator(".timeline-item-attempt").first();
    await expect(attemptItem).toBeVisible();
    await attemptItem.click();

    // Verify transcript overlay opens and displays seeded transcript
    const transcriptDialog = page.locator(".MuiDialog-paper", {
      has: page.getByRole("heading", { name: /Attempt #\d+ transcript/ }),
    });
    await expect(
      page.getByRole("heading", { name: /Attempt #\d+ transcript/ }),
    ).toBeVisible();
    await expect(transcriptDialog.locator("pre.logs-view")).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(
      page.getByRole("heading", { name: /Attempt #\d+ transcript/ }),
    ).toHaveCount(0);

    await page.locator(".overlay-pane-back button").click();

    // -----------------------------------------------------------------------
    // 5. BULK OPERATIONS: Multi-select, cancel, apply bulk edit, verification
    // -----------------------------------------------------------------------

    // Create two seeded tasks specifically for bulk editing
    const bulkStamp = Date.now();
    const taskA = await page.request.post("/api/tasks", {
      data: {
        title: `E2E Bulk Target A ${bulkStamp}`,
        customFields: { priority: "low", "requested-by": "BulkAlpha" },
      },
    });
    expect(taskA.ok()).toBe(true);
    const taskB = await page.request.post("/api/tasks", {
      data: {
        title: `E2E Bulk Target B ${bulkStamp}`,
        customFields: { priority: "low", "requested-by": "BulkBeta" },
      },
    });
    expect(taskB.ok()).toBe(true);

    await page.goto("/");
    const rowA = page.locator(".task-row", {
      hasText: `E2E Bulk Target A ${bulkStamp}`,
    });
    const rowB = page.locator(".task-row", {
      hasText: `E2E Bulk Target B ${bulkStamp}`,
    });

    await rowA.getByRole("checkbox").check();
    await rowB.getByRole("checkbox").check();

    // Floating batch bar shows count
    await expect(page.getByText("2 selected")).toBeVisible();

    // 5a. Open bulk dialog and test cancel (selection preserved)
    await page.getByRole("button", { name: "Bulk edit…" }).click();
    const bulkDialog = page.getByRole("dialog");
    await expect(bulkDialog).toBeVisible();
    await bulkDialog.getByRole("button", { name: "Cancel" }).click();
    await expect(bulkDialog).toHaveCount(0);
    await expect(page.getByText("2 selected")).toBeVisible();

    // 5b. Apply bulk edit to update Priority to "high"
    await page.getByRole("button", { name: "Bulk edit…" }).click();
    const activeDialog = page.getByRole("dialog");
    await activeDialog.getByRole("checkbox", { name: /^Priority/ }).check();
    await activeDialog.getByRole("combobox", { name: "Priority" }).click();
    await page.getByRole("option", { name: "high" }).click();

    await activeDialog
      .getByRole("button", { name: "Apply to 2 tasks" })
      .click();
    await expect(page.getByText("2 selected")).toHaveCount(0);

    // Verify both tasks updated
    await expect(rowA.locator(".chip-field")).toHaveText("Priorityhigh");
    await expect(rowB.locator(".chip-field")).toHaveText("Priorityhigh");

    // Verify GET response preserves other fields
    const resA = await page.request.get(
      `/api/tasks/${(await taskA.json()).id}`,
    );
    const bodyA = await resA.json();
    expect(bodyA.customFields).toMatchObject({
      priority: "high",
      "requested-by": "BulkAlpha",
    });

    const resB = await page.request.get(
      `/api/tasks/${(await taskB.json()).id}`,
    );
    const bodyB = await resB.json();
    expect(bodyB.customFields).toMatchObject({
      priority: "high",
      "requested-by": "BulkBeta",
    });

    // -----------------------------------------------------------------------
    // 6. ERROR HANDLING / 404 DEEP LINK
    // -----------------------------------------------------------------------
    await page.goto("/tasks/non-existent-task-id-99999");
    await expect(page.locator(".task-row").first()).toBeVisible();
  });

  // grain/task-1355: the rail's state view was held by App alone, so a
  // reload of Closed opened All tasks with closed and deferred tasks
  // mixed in, and Back could not step between state views.
  test("keeps a rail state view across reload, Back and Forward", async ({
    page,
  }) => {
    const closedRow = page
      .locator(".task-row")
      .filter({ hasText: "Spike: websocket transport" });
    const deferredRow = page
      .locator(".task-row")
      .filter({ hasText: "Replace the settings pane with a form library" });
    const rail = page.locator("aside");

    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();
    await expect(closedRow).toHaveCount(0);

    await rail.getByRole("button", { name: /^Deferred/ }).click();
    await expect(page).toHaveURL(/[?&]state=deferred\b/);
    await expect(deferredRow).toBeVisible();

    await rail.getByRole("button", { name: /^Closed/ }).click();
    await expect(page).toHaveURL(/[?&]state=closed\b/);
    await expect(closedRow).toBeVisible();
    await expect(deferredRow).toHaveCount(0);

    await page.reload();
    await expect(closedRow).toBeVisible();
    await expect(deferredRow).toHaveCount(0);

    await page.goBack();
    await expect(page).toHaveURL(/[?&]state=deferred\b/);
    await expect(deferredRow).toBeVisible();
    await expect(closedRow).toHaveCount(0);

    await page.goForward();
    await expect(page).toHaveURL(/[?&]state=closed\b/);
    await expect(closedRow).toBeVisible();

    // A link naming nothing but the state opens that state's list.
    await page.goto("/?state=closed");
    await expect(closedRow).toBeVisible();
    // grain/task-176: a state view's own baseline drops All tasks'
    // default `state:none:deferred|closed` exclusion. Before the fix,
    // TaskList compared narrowing against the unfiltered defaultFilters,
    // so every state view showed a spurious Clear button and clicking
    // Clear on Closed or Deferred restored `state:none:deferred|closed`
    // and wiped out the state's rows.
    await expect(
      page.getByRole("button", { name: "Clear", exact: true }),
    ).toHaveCount(0);
    await page.getByPlaceholder("Search tasks…").fill("no-such-task-in-closed");
    await expect(closedRow).toHaveCount(0);
    await page.getByRole("button", { name: "Clear", exact: true }).click();
    await expect(closedRow).toBeVisible();
  });

  // grain/task-176: GET /api/tasks/{id} caches TaskDetail by
  // Store.ChangeToken(ctx), which only moves on SQLite writes. A
  // local-script approval lives in memory (LocalScriptApprovals), so
  // requesting one after the task pane had already cached /api/tasks/{id}
  // used to return the stale cached response (or 304 Not Modified) and
  // never draw .local-script-approval until a database write bumped the
  // token.
  test("surfaces a local-script approval on an already-cached task pane", async ({
    page,
  }) => {
    const taskRes = await page.request.post("/api/tasks", {
      data: {
        title: `Cached pane local-script ${Date.now()}`,
        capabilities: ["local-scripts:ops"],
      },
    });
    expect(taskRes.ok()).toBe(true);
    const task = await taskRes.json();

    await page.goto(`/tasks/${task.id}`);
    const pane = page.getByRole("dialog", {
      name: new RegExp(`^Task ${task.id}: `),
    });
    await expect(pane).toBeVisible();
    await expect(pane.locator(".local-script-approval")).toHaveCount(0);

    const reqRes = await page.request.post(
      `/api/tasks/${task.id}/local-script-approvals`,
      {
        data: {
          id: "call-e2e-1",
          tool: "ops_deploy",
          script: "./scripts/deploy.sh",
          args: ["--check", "staging"],
        },
      },
    );
    expect(reqRes.ok()).toBe(true);
    const box = pane.locator(".local-script-approval");
    await expect(box).toBeVisible({ timeout: 10000 });
    await expect(box).toContainText('./scripts/deploy.sh "--check" "staging"');
    await box.getByRole("button", { name: "Approve" }).click();
    await expect(box).toHaveCount(0);
  });

  // grain/task-1355: the demo seeded its pull requests as URLs, and the
  // pane's link came out as github.com/https://github.com/.../undefined.
  test("links a seeded task's pull request to GitHub", async ({ page }) => {
    await page.goto("/tasks/5");
    const link = page
      .locator(".overlay-pane")
      .getByRole("link", { name: "acme/widgets#101" })
      .first();
    await expect(link).toHaveAttribute(
      "href",
      "https://github.com/acme/widgets/pull/101",
    );
  });
  // grain/task-1530: what a screen reader is told inside the intake
  // flow's modals. Each of these was drawn on screen but missing from
  // the accessibility tree -- an error raised from the New task form (the
  // banner sat in the #root the modal hides), the task pane and Bulk
  // edit (unnamed dialogs), and the chat composer (an unnamed
  // contenteditable). Nothing here changes the seeded store: the form's
  // submission is refused, and the chat sends nothing.
  test("names the intake modals and announces an error raised in one", async ({
    page,
  }) => {
    await page.goto("/new");
    const form = page.getByRole("dialog", { name: "New task" });
    await form.getByLabel("Title").fill("Malformed repo probe");
    await form
      .getByRole("combobox", { name: /^Repos/ })
      .selectOption({ label: "Other…" });
    await form
      .getByPlaceholder("owner/name", { exact: true })
      .fill("not a repo");
    await form.getByRole("button", { name: "Create task" }).click();
    await expect(page.getByRole("alert")).toContainText(
      'repo must be owner/name, got "not a repo"',
    );
    await expect(form).toBeVisible();
    await form.getByRole("button", { name: /discard/i }).click();

    await page.goto("/tasks/2");
    // By number only: other specs on this worker's store may have
    // retitled #2 by the time this runs.
    await expect(page.getByRole("dialog", { name: /^Task 2: / })).toBeVisible();

    // Whichever row is first: earlier specs on this worker's store file
    // and move tasks, so no particular one is promised to be listed.
    await page.goto("/");
    await page.locator(".task-row").first().getByRole("checkbox").check();
    await expect(
      page.getByRole("combobox", { name: "Capability to attach or detach" }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Bulk edit…" }).click();
    const bulk = page.getByRole("dialog", { name: "Bulk edit" });
    await expect(bulk).toBeVisible();
    await bulk.getByRole("button", { name: "Cancel" }).click();
    await page.getByRole("button", { name: "Clear selection" }).click();

    await page.goto("/session/director");
    await expect(page.getByRole("textbox", { name: "Message" })).toBeVisible();
  });

  // 2026-10-23 intake walkthrough, O2: a "File a task with these fields"
  // link saved before the single-tenant reset names a team,
  // /t/<team>/new?field.…, and landed on the bare task list with its
  // values lost. It opens the prefilled form, at the team-less address.
  test("opens a pre-reset team new-task link prefilled", async ({ page }) => {
    await page.goto("/t/default/new?field.priority=high");
    const form = page.getByRole("dialog", { name: "New task" });
    await expect(form).toBeVisible();
    await expect(form.getByLabel("Priority")).toHaveText("high");
    await expect(page).toHaveURL(/\/new\?field\.priority=high$/);
    expect(new URL(page.url()).pathname).toBe("/new");
  });
  // 2026-10-23 intake walkthrough: a value the browser refuses inside
  // the collapsed Advanced options -- a draft keeps one, and reopens the
  // form with the section shut -- left Create doing nothing at all. The
  // browser could not focus the hidden field to say why, so only the
  // console did ("An invalid form control ... is not focusable"). The
  // section now opens on the refusal, from a click on Create and from
  // Enter in a field alike, and nothing is filed until it is fixed.
  test("opens Advanced options on a refused value hidden inside it", async ({
    page,
  }) => {
    const filed = [];
    page.on("request", (req) => {
      if (
        req.method() === "POST" &&
        new URL(req.url()).pathname === "/api/tasks"
      )
        filed.push(req);
    });
    await page.goto("/new");
    const form = page.getByRole("dialog", { name: "New task" });
    const title = `Hidden refusal ${Date.now()}`;
    await form.getByLabel("Title").fill(title);
    await form.getByRole("combobox", { name: /^Repos/ }).selectOption({
      label: "No repo (standalone task -- nothing to check out)",
    });
    const advanced = form.getByRole("button", { name: "Advanced options" });
    await advanced.click();
    await form.getByLabel("vCPUs").fill("-1");
    await advanced.click();
    await expect(advanced).toHaveAttribute("aria-expanded", "false");

    await form.getByRole("button", { name: "Create task" }).click();
    await expect(advanced).toHaveAttribute("aria-expanded", "true");
    await expect(form.getByLabel("vCPUs")).toBeFocused();
    // Focused once the section is open, not mid-animation, which left it
    // scrolled below the fold.
    await expect(form.getByLabel("vCPUs")).toBeInViewport();

    await advanced.click();
    await expect(advanced).toHaveAttribute("aria-expanded", "false");
    await form.getByLabel("Title").press("Enter");
    await expect(advanced).toHaveAttribute("aria-expanded", "true");
    expect(filed).toHaveLength(0);

    await form.getByLabel("vCPUs").fill("2");
    await form.getByRole("button", { name: "Create task" }).click();
    await expect(form).toBeHidden();
    expect(filed).toHaveLength(1);
    expect(filed[0].postDataJSON()).toMatchObject({ title, sandboxCpus: 2 });
  });

  // 2026-10-26 intake walkthrough: a demo with -github-sim pre-ticks
  // Gemini key while saying it is not ready. Unticking it took its row
  // out of the open menu, so it could not be ticked again, and focus fell
  // out of the list. The row now stays for as long as the form is open.
  // This file's demo has Gemini key ready, so the real /api/config is
  // read and only that row's readiness is rewritten.
  test("keeps an unticked unready capability in the open menu", async ({
    page,
  }) => {
    await page.route("**/api/config", async (route) => {
      // A poll the page has already given up on disposes its response;
      // there is nobody left to answer then.
      try {
        const response = await route.fetch();
        const config = JSON.parse(await response.text());
        config.capabilities = (config.capabilities || []).map((c) =>
          c.id === "gemini-key"
            ? { ...c, ready: false, needs: ["a GCP project"] }
            : c,
        );
        config.defaultCapabilities = [
          ...new Set([...(config.defaultCapabilities || []), "gemini-key"]),
        ];
        await route.fulfill({ response, json: config });
      } catch {
        await route.abort().catch(() => {});
      }
    });
    const filed = [];
    page.on("request", (req) => {
      if (
        req.method() === "POST" &&
        new URL(req.url()).pathname === "/api/tasks"
      )
        filed.push(req);
    });
    await page.goto("/new");
    const form = page.getByRole("dialog", { name: "New task" });
    const discard = page.getByRole("button", { name: /discard draft/i });
    if (await discard.isVisible()) await discard.click();
    const title = `Re-ticked capability ${Date.now()}`;
    await form.getByLabel("Title").fill(title);
    await form.getByRole("combobox", { name: /^Repos/ }).selectOption({
      label: "No repo (standalone task -- nothing to check out)",
    });

    const capabilities = form.getByRole("combobox", {
      name: "Legacy capabilities",
    });
    await expect(capabilities).toContainText("Gemini key");
    await capabilities.click();
    const gemini = page.getByRole("option", { name: /Gemini key/ });
    await expect(gemini).toContainText(/Not ready/);
    await gemini.click();
    await expect(gemini).toBeVisible();
    await expect(gemini).toHaveAttribute("aria-selected", "false");
    await expect(gemini).toBeFocused();
    await gemini.click();
    await expect(gemini).toHaveAttribute("aria-selected", "true");
    await page.keyboard.press("Escape");
    await expect(page.getByRole("listbox")).toBeHidden();
    await expect(form).toBeVisible();
    await expect(capabilities).toContainText("Gemini key");

    await form.getByRole("button", { name: "Create task" }).click();
    await expect(form).toBeHidden();
    expect(filed).toHaveLength(1);
    expect(filed[0].postDataJSON().capabilities).toContain("gemini-key");
  });

  // grain/task-1625: a selection outlives the view it was made in.
  // Ticking proposed rows and opening Running left the bar reading "2
  // selected" over a list with nothing ticked, and its Close closed both
  // without a word about where they were. Bulk edit's "(mixed across the
  // selection)" note also ran into its field's name ("Requested
  // by(mixed…") in the accessible name, which jsdom spaces and Chromium
  // does not -- so it is asked here rather than only in a unit test.
  test("says when a batch selection is not on screen", async ({ page }) => {
    const stamp = `Offscreen ${Date.now()}`;
    const ids = [];
    for (const [suffix, who] of [
      ["A", "Alpha"],
      ["B", "Beta"],
    ]) {
      const res = await page.request.post("/api/tasks", {
        data: {
          title: `${stamp} ${suffix}`,
          customFields: { "requested-by": who },
        },
      });
      expect(res.ok()).toBe(true);
      ids.push((await res.json()).id);
    }

    // Filed through the API, so they are proposals.
    await page.goto(`/?state=proposed&q=${encodeURIComponent(stamp)}`);
    const rows = page.locator(".task-row", { hasText: stamp });
    await expect(rows).toHaveCount(2);
    await page.getByRole("checkbox", { name: "Select all" }).check();
    await expect(page.getByText("2 selected")).toBeVisible();
    await expect(page.getByText(/shown here/)).toHaveCount(0);

    await page.getByRole("button", { name: "Bulk edit…" }).click();
    const bulk = page.getByRole("dialog", { name: "Bulk edit" });
    await expect(
      bulk.getByRole("checkbox", {
        name: "Requested by (mixed across the selection)",
        exact: true,
      }),
    ).toBeVisible();
    await bulk.getByRole("button", { name: "Cancel" }).click();
    await expect(bulk).toHaveCount(0);

    await page
      .locator("aside")
      .getByRole("button", { name: /^Running/ })
      .click();
    await expect(page).toHaveURL(/[?&]state=running\b/);
    await expect(rows).toHaveCount(0);
    await expect(page.getByText("2 selected")).toBeVisible();
    await expect(page.getByText("none of them shown here")).toBeVisible();

    let asked = "";
    page.once("dialog", (d) => {
      asked = d.message();
      d.dismiss();
    });
    await page.getByRole("button", { name: "Close", exact: true }).click();
    await expect
      .poll(() => asked)
      .toContain("2 of them are not shown in this view.");
    for (const id of ids) {
      const res = await page.request.get(`/api/tasks/${id}`);
      expect((await res.json()).state).toBe("proposed");
    }
    await page.getByRole("button", { name: "Clear selection" }).click();
    await expect(page.getByText(/selected$/)).toHaveCount(0);
  });

  // A wake date names a day in the zone times are shown in: the
  // deployment's clock where it has one, the browser's own where it does
  // not (grain/task-322) -- the zone the "Picks itself back up on" note
  // prints it in either way. The pane used to read it as midnight where
  // the browser is whatever the deployment said, so from Tokyo a pick of
  // the 31st on a UTC deployment was sent as 15:00 UTC on the 30th and
  // the note said so -- a day earlier than the one picked.
  test.describe("from a browser in another time zone", () => {
    test.use({ timezoneId: "Asia/Tokyo", locale: "en-US" });

    async function deferToTheLastOf2030(page) {
      const res = await page.request.post("/api/tasks", {
        data: { title: `Wake zone ${Date.now()}` },
      });
      expect(res.ok()).toBe(true);
      const { id } = await res.json();

      await page.goto(`/tasks/${id}`);
      await page.getByLabel("Until (optional)").fill("2030-12-31");
      await page.getByRole("button", { name: "Defer", exact: true }).click();
      await expect(page.getByText(/Picks itself back up on/)).toHaveText(
        /on 12\/31\/2030, 12:00:00\s?AM/,
      );
      const task = await (await page.request.get(`/api/tasks/${id}`)).json();
      expect(task.state).toBe("deferred");
      return task.deferredUntil;
    }

    test("defers to the start of the picked day in the deployment's zone", async ({
      page,
    }) => {
      // The demo sets no clock of its own; this one is on UTC on purpose.
      const set = await page.request.put("/api/settings", {
        data: { timeZone: "UTC" },
      });
      expect(set.ok()).toBe(true);
      try {
        expect(await deferToTheLastOf2030(page)).toBe("2030-12-31T00:00:00Z");
      } finally {
        await page.request.put("/api/settings", { data: { timeZone: "" } });
      }
    });

    test("defers to the start of the picked day in the browser's zone on a deployment with no clock", async ({
      page,
    }) => {
      expect(await deferToTheLastOf2030(page)).toBe("2030-12-30T15:00:00Z");
    });
  });

  // 2026-10-23 intake walkthrough, task 271. Two pieces of input that
  // were given up on and still acted on: a repo typed into "Other…" and
  // then left with "Choose from list" kept its warning and Create, and
  // was filed; and a wake date kept through Undefer made the next plain
  // Defer wake on it.
  test("acts on neither an abandoned repo nor an abandoned wake date", async ({
    page,
  }) => {
    const filed = [];
    page.on("request", (req) => {
      if (
        req.method() === "POST" &&
        new URL(req.url()).pathname === "/api/tasks"
      )
        filed.push(req.postDataJSON());
    });
    await page.goto("/new");
    const form = page.getByRole("dialog", { name: "New task" });
    const title = `Abandoned input ${Date.now()}`;
    await form.getByLabel("Title").fill(title);
    const repos = form.getByRole("combobox", { name: /^Repos/ });
    await repos.selectOption({ label: "Other…" });
    await form
      .getByPlaceholder("owner/name", { exact: true })
      .fill("unlisted/custom-repo");
    await expect(form.getByTestId("repo-off-allowlist")).toBeVisible();
    await form.getByRole("button", { name: "Choose from list" }).click();
    await expect(repos).toHaveValue("");
    await expect(form.getByTestId("repo-off-allowlist")).toHaveCount(0);
    await expect(
      form.getByRole("button", { name: "Create task" }),
    ).toBeDisabled();
    await repos.selectOption("acme/widgets");
    await form.getByRole("button", { name: "Create task" }).click();
    await expect(form).toBeHidden();
    expect(filed).toHaveLength(1);
    expect(filed[0]).toMatchObject({ title, repo: "acme/widgets" });

    const task = (await (await page.request.get("/api/tasks")).json()).find(
      (t) => t.title === title,
    );
    await page.goto(`/tasks/${task.id}`);
    const pane = page.getByRole("dialog", {
      name: new RegExp(`^Task ${task.id}: `),
    });
    await pane.getByLabel("Until (optional)").fill("2030-01-15");
    await pane.getByRole("button", { name: "Defer", exact: true }).click();
    await expect(pane.getByText(/Picks itself back up on/)).toBeVisible();
    await pane.getByRole("button", { name: "Undefer", exact: true }).click();
    await expect(pane.getByLabel("Until (optional)")).toHaveValue("");
    await pane.getByRole("button", { name: "Defer", exact: true }).click();
    await expect(
      pane.getByRole("button", { name: "Undefer", exact: true }),
    ).toBeVisible();
    await expect(pane.getByText(/Picks itself back up on/)).toHaveCount(0);
    const after = await (
      await page.request.get(`/api/tasks/${task.id}`)
    ).json();
    expect(after.state).toBe("deferred");
    expect(after.deferredUntil ?? "").toBe("");
  });
});
