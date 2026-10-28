import { test, expect } from "./fixtures.js";

// docs/local-scripts.md ("Approval") in a real browser on the real
// `grain demo` server. Nothing of grain's is stubbed. The one stand-in is
// the run's forked `grain mcpserver`, whose half of the hop is two plain
// API calls made here with no person on them -- exactly what its
// daemonLocalScriptApprover sends: POST .../local-script-approvals for an
// approve: true call, then POST .../wait until somebody answers. Demo has
// no dispatch to fork one, and pkg/daemon's localscripts tests drive the
// real executor behind it.

async function fileTask(request, title, capabilities) {
  const res = await request.post("/api/tasks", {
    data: { title, description: "Uses the ops local scripts.", capabilities },
  });
  expect(res.ok(), await res.text()).toBe(true);
  return res.json();
}

async function askForApproval(run, taskId, call, args, stdin = null) {
  const res = await run.post(`/api/tasks/${taskId}/local-script-approvals`, {
    data: {
      id: call,
      tool: "local_deploy",
      script: "deploy",
      args,
      stdin_bytes: stdin ? stdin.bytes : 0,
      stdin_preview: stdin ? stdin.preview : "",
      stdin_truncated: stdin ? stdin.truncated : false,
      stdin_sha256: stdin ? stdin.sha256 : "",
      deadline: new Date(Date.now() + 5 * 60 * 1000).toISOString(),
    },
  });
  expect(res.ok(), await res.text()).toBe(true);
  return res.json();
}

async function waitForAnswer(run, taskId, approvalId) {
  for (let i = 0; i < 10; i++) {
    const res = await run.post(
      `/api/tasks/${taskId}/local-script-approvals/${approvalId}/wait?wait=5`,
    );
    expect(res.ok(), await res.text()).toBe(true);
    const approval = await res.json();
    if (approval.status !== "pending") return approval;
  }
  throw new Error(`approval ${approvalId} was never answered`);
}

test("a local script's call is approved from the task pane and denied from the inbox", async ({
  page,
  playwright,
  baseURL,
}) => {
  const task = await fileTask(
    page.request,
    "Deploy a preview with local scripts",
    ["local-scripts:ops"],
  );
  expect(task.capabilities).toContain("local-scripts:ops");
  // The run's own hop: a request context with no session on it.
  const run = await playwright.request.newContext({ baseURL });

  // Approve, in the task pane. The arguments are shown as the script
  // gets them: quoted, with nothing a shell would have expanded.
  const first = await askForApproval(run, task.id, "1", [
    "main",
    "$(rm -rf /)",
  ]);
  expect(first.status).toBe("pending");
  const answered = waitForAnswer(run, task.id, first.id);
  await page.goto(`/tasks/${task.id}`);
  const card = page.locator(
    `.local-script-approval[data-approval="${first.id}"]`,
  );
  await expect(card).toContainText("A local script is waiting for approval");
  await expect(card.locator(".local-script-call")).toHaveText(
    'deploy "main" "$(rm -rf /)"',
  );
  await card.getByRole("button", { name: "Approve" }).click();
  expect((await answered).status).toBe("approved");
  await expect(card).toHaveCount(0);
  // Recorded on the task.
  await expect(page.getByText("Approved local script call 1")).toBeVisible();

  // Deny, from the inbox. A call that reads its instructions from stdin
  // shows the start of them and the hash of all of them.
  const second = await askForApproval(run, task.id, "2", ["release-1"], {
    bytes: 6000,
    preview: "drop table users;\n",
    truncated: true,
    sha256: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
  });
  const denied = waitForAnswer(run, task.id, second.id);
  await page.goto("/inbox");
  const group = page.locator(".inbox-group", {
    hasText: "Local scripts waiting for approval",
  });
  await expect(group).toBeVisible();
  const row = group.locator(
    `.local-script-approval[data-approval="${second.id}"]`,
  );
  await expect(row.locator(".local-script-call")).toHaveText(
    'deploy "release-1" (and 6000 bytes on stdin)',
  );
  await expect(row.locator(".local-script-stdin")).toContainText(
    "drop table users;",
  );
  await expect(row.locator(".local-script-stdin")).toContainText("truncated");
  await expect(row.locator(".local-script-stdin-line")).toHaveText(
    "On stdin: 6000 bytes, sha256 9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08 (truncated: first 18 of 6000 bytes shown)",
  );
  // The preview stays inside its card at desktop and phone widths, and
  // above the buttons.
  for (const viewport of [
    { width: 1280, height: 900 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    const cardBox = await row.boundingBox();
    const stdinBox = await row.locator(".local-script-stdin").boundingBox();
    const denyBox = await row
      .getByRole("button", { name: "Deny" })
      .boundingBox();
    expect(stdinBox.x).toBeGreaterThanOrEqual(cardBox.x);
    expect(stdinBox.x + stdinBox.width).toBeLessThanOrEqual(
      cardBox.x + cardBox.width,
    );
    expect(stdinBox.y + stdinBox.height).toBeLessThanOrEqual(denyBox.y);
  }
  await row.getByRole("button", { name: "Deny" }).click();
  expect((await denied).status).toBe("denied");
  await expect(group).toHaveCount(0);

  // An answered call cannot be answered again.
  const again = await page.request.post(
    `/api/tasks/${task.id}/local-script-approvals/${second.id}/answer`,
    { data: { approve: true } },
  );
  expect(again.status()).toBe(400);
  await run.dispose();
});

test("a task without the capability cannot ask for a local-script approval", async ({
  page,
  playwright,
  baseURL,
}) => {
  const task = await fileTask(page.request, "No local scripts here", []);
  const run = await playwright.request.newContext({ baseURL });
  const res = await run.post(`/api/tasks/${task.id}/local-script-approvals`, {
    data: { id: "1", tool: "local_deploy", script: "deploy", args: [] },
  });
  expect(res.status()).toBe(400);
  expect((await res.json()).error).toContain(
    "holds no local-scripts capability",
  );
  await run.dispose();
});
