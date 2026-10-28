import { execFileSync } from "node:child_process";
import { test, expect } from "./director-fixtures.js";
import { accessCode } from "./accesscode.js";

const DESKTOP = { width: 1280, height: 800 };
const PHONE = { width: 390, height: 844 };

async function task(page, id) {
  const response = await page.request.get(`/api/tasks/${id}`);
  expect(response.ok()).toBeTruthy();
  return response.json();
}

async function send(page, text) {
  // chatscope applies its placeholder through CSS rather than an accessible
  // placeholder attribute; this is the library's contenteditable element.
  const composer = page.locator(".cs-message-input__content-editor");
  await composer.fill(text);
  await composer.press("Enter");
}

async function sessionTaskID(page, id = "director") {
  const response = await page.request.get("/api/console-sessions");
  expect(response.ok()).toBeTruthy();
  const body = await response.json();
  return body.sessions.find((session) => session.id === id)?.taskId;
}

async function taskTags(page, id) {
  const response = await page.request.get(
    `/e2e/task-tags?task=${encodeURIComponent(id)}`,
  );
  expect(response.ok()).toBeTruthy();
  const body = await response.json();
  return body.tags || [];
}

function grainList(server, ...args) {
  return JSON.parse(
    execFileSync(
      process.env.GRAIN_E2E_DEMO_BIN,
      ["-server", server, "-token", accessCode(), "-json", "list", ...args],
      { encoding: "utf8" },
    ),
  );
}

async function seedBoard(page, data) {
  const response = await page.request.post("/api/boards", { data });
  expect(response.ok(), `POST /api/boards: ${response.status()}`).toBe(true);
  return response.json();
}

function taskSurface(page, selector, id) {
  return page
    .locator(selector)
    .filter({ has: page.locator(`.task-number:text-is("${id}")`) });
}

async function themeColor(page, variable) {
  return page.evaluate((name) => {
    const probe = document.createElement("span");
    probe.style.color = `var(${name})`;
    document.body.append(probe);
    const color = getComputedStyle(probe).color;
    probe.remove();
    return color;
  }, variable);
}

async function expectTypingStyle(page) {
  const indicator = page.locator(".cs-typing-indicator");
  await expect(indicator).toBeVisible();
  expect(
    await indicator.evaluate((el) => getComputedStyle(el).backgroundColor),
  ).toBe(await themeColor(page, "--surface"));
  expect(
    await indicator
      .locator(".cs-typing-indicator__dot")
      .first()
      .evaluate((el) => getComputedStyle(el).backgroundColor),
  ).toBe(await themeColor(page, "--text-tertiary"));
}

async function liveColor(surface) {
  const mark = surface.locator(".grain-mark-interactive");
  await expect(mark).toBeVisible();
  return mark.evaluate((element) => {
    const probe = document.createElement("span");
    probe.style.color = "var(--state-interactive)";
    document.body.append(probe);
    const expected = getComputedStyle(probe).color;
    probe.remove();
    const actual = getComputedStyle(element).backgroundColor;
    return { actual, expected };
  });
}

async function expectPurpleLive(surface, expectedColor = null) {
  const color = await liveColor(surface);
  expect(color.actual).toBe(color.expected);
  if (expectedColor) expect(color.actual).toBe(expectedColor);
  return color.actual;
}

async function expectFullHeightChat(page) {
  await expect
    .poll(async () => {
      return await page.locator(".console-session-page").evaluate((pane) => {
        const body = pane?.querySelector(".console-session-body");
        const chat = pane?.querySelector(".task-chat");
        const window = pane?.querySelector(".task-chat-window");
        if (!pane || !body || !chat || !window) return false;
        const geometry = {
          pane: pane.getBoundingClientRect().height,
          body: body.getBoundingClientRect().height,
          chat: chat.getBoundingClientRect().height,
          window: window.getBoundingClientRect().height,
          overflow: document.documentElement.scrollWidth - innerWidth,
        };
        return (
          geometry.body > geometry.pane * 0.7 &&
          geometry.chat > geometry.body * 0.6 &&
          geometry.window > geometry.chat * 0.5 &&
          geometry.overflow <= 0
        );
      });
    })
    .toBe(true);
}

async function expectCapabilityDisclosure(page) {
  await page
    .getByRole("button", { name: "Edit Director capabilities" })
    .click();
  await expect(
    page.getByRole("heading", { name: "What it may reach" }),
  ).toBeVisible();
  await expect(
    page.getByRole("checkbox", { name: /Deployment health/ }),
  ).toBeVisible();
  await expect(
    page.getByLabel(/What it may reach on the internet/),
  ).toBeVisible();
  await page
    .getByRole("button", { name: "Hide Director capabilities" })
    .click();
  await expect(
    page.getByRole("checkbox", { name: /Deployment health/ }),
  ).toBeHidden();
}

// Sections 1 and 7 of docs/test-plans/2026-09-13.md. This is deliberately
// one operator journey over a running deployment. The provider is scripted in
// directorserver; Chromium, the React app, CLI, HTTP API, SQLite store,
// reconcile cycles, host sandbox, state checkout and MCP server are all real.
test("follows a live Director task through its durable exchange", async ({
  page,
  context,
  directorServer,
}) => {
  test.setTimeout(150_000);
  await page.setViewportSize(DESKTOP);
  await page.goto("/session/director");
  await expect(
    page.getByRole("heading", { name: "Director", exact: true }),
  ).toBeVisible();
  await expect(page.locator(".console-session-header .count")).toHaveCount(0);
  await expect(
    page.getByRole("link", { name: "Open the task behind this session" }),
  ).toHaveCount(0);
  const firstTask = await sessionTaskID(page);
  expect(firstTask).toBeTruthy();

  // A fresh standing session is idle and waiting on the operator. Each entry
  // retains the library's single-message treatment even when the two seeded
  // messages share an author, and the shared chat consumes the available pane
  // at both target widths.
  await expect
    .poll(async () => (await task(page, firstTask)).state)
    .toBe("awaiting_reply");
  await expect(page.getByText("waiting for your reply")).toBeVisible();
  await expect(page.locator(".cs-message")).toHaveCount(2);
  for (const message of await page.locator(".cs-message").all()) {
    await expect(message).toHaveClass(/cs-message--single/);
  }
  expect(
    await page
      .locator(".cs-message--incoming .cs-message__content")
      .first()
      .evaluate((el) => getComputedStyle(el).backgroundColor),
  ).toBe(await themeColor(page, "--surface-hover"));
  await expectFullHeightChat(page);
  await expectCapabilityDisclosure(page);

  await page.setViewportSize(PHONE);
  await expect(page.locator(".cs-message")).toHaveCount(2);
  await expect(page.getByText("waiting for your reply")).toBeVisible();
  await expectFullHeightChat(page);
  await expectCapabilityDisclosure(page);
  await page.setViewportSize(DESKTOP);

  const liveBoard = await seedBoard(page, {
    name: "E2E live Director",
    kind: "kanban",
    query: "f=state:any:running",
    columns: [{ name: "Live", states: ["running"] }],
  });
  const liveTimeline = await seedBoard(page, {
    name: "E2E live Director timeline",
    kind: "gantt",
    query: "f=state:any:running",
  });

  // This fixture starts with no optional capabilities. State-repository read
  // access is already attached to the session task; the operator explicitly
  // grants the bounded controller diagnostic that can read it.
  await page
    .getByRole("button", { name: "Edit Director capabilities" })
    .click();
  const health = page.getByRole("checkbox", { name: /Deployment health/ });
  await expect(health).not.toBeChecked();
  await health.click();
  await expect(health).toBeChecked();
  await expect
    .poll(async () => (await task(page, firstTask)).capabilities)
    .toContain("deployment-health");
  await page
    .getByRole("button", { name: "Hide Director capabilities" })
    .click();

  await send(
    page,
    "Diagnose the controller and sandbox state, verify bounds and redaction, then file the pressure follow-up.",
  );
  await expect
    .poll(async () => (await task(page, firstTask)).state)
    .toBe("running");

  // The fixture holds this real attempt at the scripted-provider boundary so
  // the same live task can be inspected without racing its reply. Ordinary CLI
  // lists omit it and an explicit kind question reveals it there. The web's
  // task views never show it, whatever they ask: a session lives in the
  // Sessions pane (taskFilters.js's isSessionTask), so a list, a board and a
  // timeline that all admit running work still leave it out.
  expect(grainList(directorServer).some((row) => row.id === firstTask)).toBe(
    false,
  );
  expect(
    grainList(directorServer, "-filter", "kind:any:interactive").some(
      (row) => row.id === firstTask,
    ),
  ).toBe(true);

  await page.goto("/");
  await expect(page.getByPlaceholder("Search tasks…")).toBeVisible();
  await expect(taskSurface(page, ".task-row", firstTask)).toHaveCount(0);
  await page.goto("/?f=state:any:running");
  await expect(page.getByPlaceholder("Search tasks…")).toBeVisible();
  await expect(taskSurface(page, ".task-row", firstTask)).toHaveCount(0);

  await page.goto(`/boards/${liveBoard.id}`);
  await expect(
    page.locator(".board-column-title", { hasText: "Live" }),
  ).toBeVisible();
  await expect(taskSurface(page, ".board-card", firstTask)).toHaveCount(0);

  await page.goto(`/boards/${liveTimeline.id}`);
  await expect(page.locator(".gantt").first()).toBeVisible();
  await expect(taskSurface(page, ".gantt-row", firstTask)).toHaveCount(0);

  await page.goto(`/tasks/${firstTask}`);
  await expect(page).toHaveURL(/\/session\/director$/);
  await expect(page.locator(".detail-state")).toHaveCount(0);
  await expect(page.locator(".console-session-header .count")).toHaveCount(0);
  await expect(page.locator(".console-session-header")).toContainText(
    "Interactive",
  );
  const purple = await expectPurpleLive(
    page.locator(".console-session-header"),
  );
  await expectTypingStyle(page);
  expect(
    await page
      .locator(".cs-message--outgoing .cs-message__content")
      .last()
      .evaluate((el) => getComputedStyle(el).backgroundColor),
  ).toBe(await themeColor(page, "--accent-subtle"));
  await expect(page.getByText("waiting for your reply")).toHaveCount(0);
  await expectFullHeightChat(page);

  await page.setViewportSize(PHONE);
  // A phone has no page header rail, so the session's header row moves
  // from the rail back into the page (PageHeader.jsx): read it once it
  // has, not the one on its way out.
  await expect(page.getByTestId("page-header")).toHaveCount(0);
  await expectPurpleLive(page.locator(".console-session-header"), purple);
  await expectTypingStyle(page);
  await expectFullHeightChat(page);
  await expectCapabilityDisclosure(page);

  // Let the scripted provider continue only after the browser has followed the
  // task through every live surface.
  const released = await page.request.post("/e2e/release-first-run");
  expect(released.status()).toBe(204);

  // The scripted provider calls propose_task and then chat. Chat is still
  // blocked here, so the parent attempt has not finished: seeing the assigned
  // task and link now proves live persistence rather than end-of-run replay.
  await expect(
    page.getByText(/Controller diagnostics are bounded and healthy:/),
  ).toBeVisible();
  await expect(
    page.getByText(/oversized state table was refused at the per-call limit/),
  ).toBeVisible();
  await expect(
    page.getByText(/read-only grain-state checkout was available/),
  ).toBeVisible();
  await expect(
    page.getByText("[redacted: secret diagnostic/token]", { exact: true }),
  ).toBeVisible();
  await expect(page.locator("body")).not.toContainText(
    "director-e2e-secret-42",
  );

  const proposalLink = page.getByRole("link", {
    name: /Open proposed task #/,
  });
  await expect(proposalLink).toBeVisible();
  const proposalHref = await proposalLink.getAttribute("href");
  expect(proposalHref).toMatch(/\/tasks\/[^/]+$/);
  const proposalID = proposalHref.split("/").at(-1);

  const liveParent = await task(page, firstTask);
  expect(liveParent.state).toBe("awaiting_reply");
  expect(liveParent.attempts.at(-1).finishedAt).toBeUndefined();
  await expect(page.getByText("waiting for your reply")).toBeVisible();
  await expect(page.locator(".cs-typing-indicator")).toHaveCount(0);
  await expect(page.locator(".grain-mark-interactive")).toHaveCount(0);
  await expectFullHeightChat(page);
  await page.setViewportSize(DESKTOP);
  await expect(page.getByText("waiting for your reply")).toBeVisible();
  await expectFullHeightChat(page);
  const proposed = await task(page, proposalID);
  expect(proposed.title).toBe("Investigate controller pressure");
  expect(proposed.state).toBe("proposed");
  expect(proposed.generatedFrom).toBe(firstTask);

  const proposalPage = await context.newPage();
  await proposalPage.goto(proposalHref);
  await expect(
    proposalPage.getByRole("heading", {
      name: /Investigate controller pressure/,
    }),
  ).toBeVisible();
  await proposalPage.close();

  // Both the chat result and the proposed task came from the store, not
  // provider memory: a hard reload has to recover both.
  await page.reload();
  await expect(
    page.getByText(/Controller diagnostics are bounded and healthy:/),
  ).toBeVisible();
  await expect(
    page.getByRole("link", { name: `Open proposed task #${proposalID}` }),
  ).toHaveAttribute("href", proposalHref);
  expect((await task(page, proposalID)).state).toBe("proposed");

  await send(page, "Thanks; finish this diagnostic exchange.");
  await expect(page.getByText("Diagnostic exchange complete.")).toBeVisible();
  await expect
    .poll(async () => (await task(page, firstTask)).state)
    .toBe("awaiting_reply");

  // Start a fresh chat directly from the session pane. The restart endpoint
  // synchronously files a replacement task and closes the old one, and the
  // stable /session/director route attaches to it without exposing task IDs.
  await page.getByRole("button", { name: "Start a fresh chat" }).click();
  await page
    .getByRole("button", { name: "Close this chat and start a fresh one" })
    .click();

  await expect.poll(async () => sessionTaskID(page)).not.toBe(firstTask);
  const replacementTask = await sessionTaskID(page);
  expect(replacementTask).toBeTruthy();

  await expect(page).toHaveURL(/\/session\/director$/);
  await expect(page.locator(".console-session-header .count")).toHaveCount(0);
  await expect(
    page.getByRole("link", { name: "Open the chat before this one" }),
  ).toHaveCount(0);
  await expect
    .poll(async () => (await task(page, firstTask)).state)
    .toBe("closed");

  await send(page, "Which task is this reply attached to?");
  await expect(
    page.getByText(
      `This reply is attached to the open replacement task #${replacementTask}, not its closed history.`,
    ),
  ).toBeVisible();

  const closedHistory = await task(page, firstTask);
  expect(
    closedHistory.comments.some((comment) =>
      comment.body.includes("Which task is this reply attached to?"),
    ),
  ).toBeFalsy();
  expect(
    (await task(page, replacementTask)).comments.some((comment) =>
      comment.body.includes("Which task is this reply attached to?"),
    ),
  ).toBeTruthy();

  await send(page, "Finish the replacement exchange.");
  await expect(
    page.getByText(
      `Replacement exchange complete on task #${replacementTask}.`,
    ),
  ).toBeVisible();
  await expect
    .poll(async () => (await task(page, replacementTask)).state)
    .toBe("awaiting_reply");

  // Start one more real turn and stop it while the provider is in flight. A
  // cancelled run becomes a chat separator and returns the session to
  // awaiting_reply, and restarting while in flight closes the replacement task.
  await send(page, "Stay here until I close this live task.");
  await expect
    .poll(async () => (await task(page, replacementTask)).state)
    .toBe("running");
  await expect(page.locator(".cs-typing-indicator")).toBeVisible();
  await page.getByRole("button", { name: "Stop", exact: true }).click();
  await expect
    .poll(async () => (await task(page, replacementTask)).state)
    .toBe("awaiting_reply");
  const cancelled = page
    .locator(".cs-message-separator")
    .filter({ hasText: "Cancelled" });
  await expect(cancelled).toBeVisible();
  const border = await themeColor(page, "--border");
  await expect
    .poll(() =>
      cancelled.evaluate((element) => [
        getComputedStyle(element, "::before").backgroundColor,
        getComputedStyle(element, "::after").backgroundColor,
      ]),
    )
    .toEqual([border, border]);
  await expect(page.getByText("waiting for your reply")).toBeVisible();
  await expect(page.locator(".cs-typing-indicator")).toHaveCount(0);
  await expect(page.locator(".grain-mark-interactive")).toHaveCount(0);

  await page.reload();
  await expect(cancelled).toBeVisible();
  await expect(page.getByText("waiting for your reply")).toBeVisible();
  await expect(page.locator(".cs-typing-indicator")).toHaveCount(0);
  await expect(page.locator(".grain-mark-interactive")).toHaveCount(0);

  await page.getByRole("button", { name: "Start a fresh chat" }).click();
  await page
    .getByRole("button", { name: "Close this chat and start a fresh one" })
    .click();
  await expect
    .poll(async () => (await task(page, replacementTask)).state)
    .toBe("closed");

  // A closed session task is still a session's, so even the closed list
  // leaves it out (taskFilters.js's isSessionTask); the CLI, which has no
  // Sessions view, still finds it by kind.
  await page.goto("/?state=closed");
  await expect(page.getByPlaceholder("Search tasks…")).toBeVisible();
  await expect(taskSurface(page, ".task-row", replacementTask)).toHaveCount(0);
  const closedCLI = grainList(
    directorServer,
    "-closed",
    "-filter",
    "kind:any:interactive",
  ).find((row) => row.id === replacementTask);
  expect(closedCLI?.state).toBe("closed");
});

test("Console session lifecycle controls, backing-task redirects, and session WorkTree", async ({
  page,
  restartDirectorServer,
}) => {
  test.setTimeout(180_000);
  await page.setViewportSize(DESKTOP);

  // 1. Backing-task route redirect & hidden task pill:
  // Navigating to /director canonicalizes to /session/director.
  await page.goto("/director");
  await expect(page).toHaveURL(/\/session\/director$/);
  await expect(page.getByText("waiting for your reply")).toBeVisible();
  await expect(page.locator(".console-session-header .count")).toHaveCount(0);
  await expect(
    page.getByRole("link", { name: "Open the task behind this session" }),
  ).toHaveCount(0);

  const directorTaskId = await sessionTaskID(page, "director");
  expect(directorTaskId).toBeTruthy();

  // Create a second console session `triage` via the UI: "New session"
  // opens an empty chat, and its first message creates the session,
  // named from that message.
  await page.getByRole("button", { name: "Create options" }).click();
  await page.getByRole("menuitem", { name: "New session" }).click();
  await expect(page).toHaveURL(/\/session\/new$/);
  await page.locator(".cs-message-input__content-editor").click();
  await page.keyboard.type("Triage");
  await page.keyboard.press("Enter");

  await expect(page).toHaveURL(/\/session\/triage$/);
  await expect.poll(async () => sessionTaskID(page, "triage")).toBeTruthy();
  const triageTaskId = await sessionTaskID(page, "triage");
  expect(triageTaskId).not.toBe(directorTaskId);
  await expect(page.getByText("waiting for your reply")).toBeVisible();

  // Navigate directly to /tasks/<directorTaskId> and /tasks/<triageTaskId>
  // at both desktop (1280x800) and phone (390x844) viewports.
  for (const viewport of [DESKTOP, PHONE]) {
    await page.setViewportSize(viewport);
    await page.goto(`/tasks/${directorTaskId}`);
    await expect(page).toHaveURL(/\/session\/director$/);
    await expect(page.locator(".detail-state")).toHaveCount(0);
    await expect(page.locator(".console-session-header .count")).toHaveCount(0);
    await expect(
      page.getByRole("link", { name: "Open the task behind this session" }),
    ).toHaveCount(0);

    await page.goto(`/tasks/${triageTaskId}`);
    await expect(page).toHaveURL(/\/session\/triage$/);
    await expect(page.locator(".detail-state")).toHaveCount(0);
    await expect(page.locator(".console-session-header .count")).toHaveCount(0);
    await expect(
      page.getByRole("link", { name: "Open the task behind this session" }),
    ).toHaveCount(0);
  }

  // 2. Session WorkTree disclosure:
  // Have `triage`'s backing task file two follow-up tasks and record a
  // standing handover summary.
  await page.setViewportSize(DESKTOP);
  await send(
    page,
    "File two follow-up tasks and record the standing handover summary.",
  );
  await expect(
    page.getByText(
      "Filed two follow-up tasks and recorded the standing handover summary.",
    ),
  ).toBeVisible();
  await expect
    .poll(async () => (await task(page, triageTaskId)).state)
    .toBe("awaiting_reply");

  // Verify the two follow-up tasks were filed with generatedFrom === triageTaskId.
  const tasksResp = await page.request.get("/api/tasks");
  expect(tasksResp.ok()).toBeTruthy();
  const allTasks = await tasksResp.json();
  const childTasks = (Array.isArray(allTasks) ? allTasks : allTasks.tasks)
    .filter((t) => t.generatedFrom === triageTaskId)
    .sort((a, b) => Number(a.id) - Number(b.id));
  expect(childTasks.map((t) => t.title)).toEqual([
    "Audit session redirect handling",
    "Verify session work tree links",
  ]);

  // Verify WorkTree disclosure renders on ConsoleSessionPage at 1280x800 and 390x844.
  const workTree = page.getByRole("region", { name: "Work tree" });
  await expect(workTree).toContainText(
    `Work from task ${triageTaskId}: 0 of 2 done`,
  );

  await page.setViewportSize(PHONE);
  await expect(workTree).toContainText(
    `Work from task ${triageTaskId}: 0 of 2 done`,
  );
  await workTree.locator(".provenance-toggle").click();
  const rootNode = workTree.locator(`li[data-task-id="${triageTaskId}"]`);
  await expect(
    rootNode.locator(`li[data-task-id="${childTasks[0].id}"]`),
  ).toBeVisible();
  await expect(
    rootNode.locator(`li[data-task-id="${childTasks[1].id}"]`),
  ).toBeVisible();

  await page.setViewportSize(DESKTOP);
  await expect(
    rootNode.locator(`li[data-task-id="${childTasks[0].id}"]`),
  ).toBeVisible();

  // Click one of the follow-up task rows to confirm navigation to /tasks/<childId>.
  await rootNode
    .locator(`li[data-task-id="${childTasks[0].id}"]`)
    .getByRole("link", { name: childTasks[0].id, exact: true })
    .click();
  await expect(page).toHaveURL(new RegExp(`/tasks/${childTasks[0].id}$`));
  await expect(
    page.getByRole("heading", { name: /Audit session redirect handling/ }),
  ).toBeVisible();

  // 3. Stop (`cancel`) immediate UI refresh (3a81d0003):
  await page.goto("/session/triage");
  await expect(page).toHaveURL(/\/session\/triage$/);
  await send(page, "Hold this turn in running until stopped.");
  await expect
    .poll(async () => (await task(page, triageTaskId)).state)
    .toBe("running");
  await expect(page.locator(".cs-typing-indicator")).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Stop", exact: true }),
  ).toBeVisible();

  await page.setViewportSize(PHONE);
  await expect(
    page.getByRole("button", { name: "Stop", exact: true }),
  ).toBeVisible();
  await page.setViewportSize(DESKTOP);

  const cancelResponsePromise = page.waitForResponse(
    (res) =>
      res.url().endsWith("/api/console-sessions/triage/cancel") &&
      res.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Stop", exact: true }).click();
  const cancelRes = await cancelResponsePromise;
  expect(cancelRes.ok()).toBeTruthy();
  const cancelResolvedAt = Date.now();

  // Assert the UI transitions back to awaiting_reply immediately upon
  // cancel resolving, well before the 3000ms background poll interval.
  await expect(
    page.getByRole("button", { name: "Stop", exact: true }),
  ).toHaveCount(0);
  await expect(page.getByText("waiting for your reply")).toBeVisible();
  await expect(
    page.locator(".cs-message-separator").filter({ hasText: "Cancelled" }),
  ).toBeVisible();
  await expect(page.locator(".cs-typing-indicator")).toHaveCount(0);
  await expect(page.locator(".grain-mark-interactive")).toHaveCount(0);
  expect(Date.now() - cancelResolvedAt).toBeLessThan(1500);
  expect((await task(page, triageTaskId)).state).toBe("awaiting_reply");

  // 4. Restart with carryHandover: true vs. carryHandover: false:
  const beforeRestartTask = await task(page, triageTaskId);
  expect(
    beforeRestartTask.comments.some(
      (c) =>
        c.handover === true &&
        c.body.includes("Triage handover summary: audited redirect handling"),
    ),
  ).toBe(true);

  // 4a. Trigger Restart keeping handover (carryHandover: true):
  await page.getByRole("button", { name: "Start a fresh chat" }).click();
  const restartWithSummaryPromise = page.waitForResponse(
    (res) =>
      res.url().endsWith("/api/console-sessions/triage/restart") &&
      res.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Start fresh with summary" }).click();
  const restartWithSummaryRes = await restartWithSummaryPromise;
  expect(restartWithSummaryRes.ok()).toBeTruthy();
  const restartWithSummaryAt = Date.now();

  // Immediately switches to the replacement task with the carried handover
  // summary visible, without waiting for a 3-second poll tick.
  await expect(
    page.getByText(
      "Triage handover summary: audited redirect handling and queued two follow-up tasks.",
    ),
  ).toBeVisible();
  await expect(
    page.getByText(
      "Filed two follow-up tasks and recorded the standing handover summary.",
    ),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Start a fresh chat" }),
  ).toBeVisible();
  expect(Date.now() - restartWithSummaryAt).toBeLessThan(1500);

  const triageTask2Id = await sessionTaskID(page, "triage");
  expect(triageTask2Id).toBeTruthy();
  expect(triageTask2Id).not.toBe(triageTaskId);
  expect((await task(page, triageTaskId)).state).toBe("closed");
  expect(await taskTags(page, triageTaskId)).not.toContain(
    "console-session-reset",
  );

  const triageTask2 = await task(page, triageTask2Id);
  expect(triageTask2.state).toBe("awaiting_reply");
  expect(triageTask2.continues).toBe(triageTaskId);
  expect(
    triageTask2.comments.some(
      (c) =>
        c.handover === true &&
        c.body.includes("Triage handover summary: audited redirect handling"),
    ),
  ).toBe(true);

  // 4b. Trigger Restart starting fresh (carryHandover: false):
  await page.getByRole("button", { name: "Start a fresh chat" }).click();
  const restartFreshPromise = page.waitForResponse(
    (res) =>
      res.url().endsWith("/api/console-sessions/triage/restart") &&
      res.request().method() === "POST",
  );
  await page
    .getByRole("button", { name: "Close this chat and start a fresh one" })
    .click();
  const restartFreshRes = await restartFreshPromise;
  expect(restartFreshRes.ok()).toBeTruthy();
  const restartFreshAt = Date.now();

  await expect(
    page.getByText(
      "Triage handover summary: audited redirect handling and queued two follow-up tasks.",
    ),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Start a fresh chat" }),
  ).toBeVisible();
  expect(Date.now() - restartFreshAt).toBeLessThan(1500);

  const triageTask3Id = await sessionTaskID(page, "triage");
  expect(triageTask3Id).toBeTruthy();
  expect(triageTask3Id).not.toBe(triageTask2Id);
  expect((await task(page, triageTask2Id)).state).toBe("closed");
  expect(await taskTags(page, triageTask2Id)).toContain(
    "console-session-reset",
  );

  const triageTask3 = await task(page, triageTask3Id);
  expect(triageTask3.state).toBe("awaiting_reply");
  expect(triageTask3.continues).toBeUndefined();
  expect(triageTask3.comments.some((c) => Boolean(c.handover))).toBe(false);

  // 5. Delete `director` without resurrection & fresh recreation:
  // Delete `triage` first so that deleting `director` leaves an empty
  // ConsoleSessions slice (`[]model.ConsoleSession{}`) and exercises
  // ConsoleSessionsOr not resurrecting `director` when 0 sessions remain.
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: "Delete session" }).click();
  await expect.poll(async () => sessionTaskID(page, "triage")).toBeUndefined();

  await page.goto("/session/director");
  await expect(page).toHaveURL(/\/session\/director$/);
  await send(page, "Record director handover before delete.");
  await expect(
    page.getByText("Director pre-delete handover recorded."),
  ).toBeVisible();
  await expect
    .poll(async () => (await task(page, directorTaskId)).state)
    .toBe("awaiting_reply");

  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: "Delete session" }).click();

  await expect
    .poll(async () => {
      const res = await page.request.get("/api/console-sessions");
      const body = await res.json();
      return body.sessions.length;
    })
    .toBe(0);
  expect((await task(page, directorTaskId)).state).toBe("closed");
  expect(await taskTags(page, directorTaskId)).toContain(
    "console-session-reset",
  );

  // Reload the browser and restart the daemon/server over the same SQLite DB;
  // assert GET /api/console-sessions still does not contain `director`.
  await page.reload();
  await restartDirectorServer();
  await page.reload();

  const afterRestartSessions = await (
    await page.request.get("/api/console-sessions")
  ).json();
  expect(afterRestartSessions.sessions).toEqual([]);
  await expect(page.getByText("No interactive sessions yet.")).toBeVisible();

  // Recreate `director` and confirm it receives a brand-new backing task
  // that does not inherit the pre-delete handover or conversation.
  await page.getByRole("button", { name: "+ New session" }).first().click();
  await expect(page).toHaveURL(/\/session\/new$/);
  await page.locator(".cs-message-input__content-editor").click();
  await page.keyboard.type("Director");
  await page.keyboard.press("Enter");

  await expect(page).toHaveURL(/\/session\/director$/);
  await expect.poll(async () => sessionTaskID(page, "director")).toBeTruthy();
  const recreatedDirectorTaskId = await sessionTaskID(page, "director");
  expect(recreatedDirectorTaskId).not.toBe(directorTaskId);

  await expect(page.getByText("waiting for your reply")).toBeVisible();
  await expect(
    page.getByText("Director pre-delete handover recorded."),
  ).toHaveCount(0);
  await expect(
    page.getByText(
      "Pre-delete director handover summary that must not survive deletion.",
    ),
  ).toHaveCount(0);

  const recreatedDirectorTask = await task(page, recreatedDirectorTaskId);
  expect(recreatedDirectorTask.state).toBe("awaiting_reply");
  expect(recreatedDirectorTask.continues).toBeUndefined();
  expect(recreatedDirectorTask.comments.some((c) => Boolean(c.handover))).toBe(
    false,
  );
});
