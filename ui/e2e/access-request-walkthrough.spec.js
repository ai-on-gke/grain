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

import fs from "node:fs/promises";
import path from "node:path";
import { expect, test } from "./console-rest-fixtures.js";

const VIEWPORTS = {
  desktop: { width: 1440, height: 900 },
  phone: { width: 390, height: 844 },
};

const THEME_KEY = "grain.themeMode";

async function expectInside(card, page, width) {
  const box = await card.boundingBox();
  expect(box).not.toBeNull();
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(width + 1);
  for (const name of [/^Grant/, /^Deny$/]) {
    const button = card.getByRole("button", { name }).first();
    const b = await button.boundingBox();
    expect(b).not.toBeNull();
    expect(b.x).toBeGreaterThanOrEqual(box.x - 1);
    expect(b.x + b.width).toBeLessThanOrEqual(box.x + box.width + 1);
  }
  const overflow = await card.evaluate((el) => el.scrollWidth - el.clientWidth);
  expect(overflow).toBeLessThanOrEqual(1);
  for (const chip of await card.getByTestId("access-item-chip").all()) {
    const c = await chip.boundingBox();
    expect(c).not.toBeNull();
    expect(c.x + c.width).toBeLessThanOrEqual(box.x + box.width + 1);
  }
  await expect(page.locator("body")).toBeVisible();
}

async function captureSurfaceShots(
  page,
  surfaceName,
  prepareAfterReload,
  outDir,
  shotLog,
) {
  if (!outDir) return;
  await fs.mkdir(outDir, { recursive: true });
  for (const [widthName, size] of Object.entries(VIEWPORTS)) {
    await page.setViewportSize(size);
    for (const theme of ["light", "dark"]) {
      await page.evaluate(
        ([k, t]) => localStorage.setItem(k, t),
        [THEME_KEY, theme],
      );
      await page.emulateMedia({ colorScheme: theme });
      await page.reload();
      await prepareAfterReload(widthName, size);
      await page.waitForTimeout(250);
      const filename = `${surfaceName}-${widthName}-${theme}.png`;
      const file = path.join(outDir, filename);
      await page.screenshot({ path: file });
      shotLog.push({ name: `${surfaceName} · ${widthName} · ${theme}`, file });
    }
  }
  await page.setViewportSize(VIEWPORTS.desktop);
  await page.evaluate((k) => localStorage.removeItem(k), THEME_KEY);
  await page.emulateMedia({ colorScheme: "light" });
  await page.reload();
}

async function writeContactSheet(browser, shots, file) {
  if (!shots.length) return;
  const cells = await Promise.all(
    shots.map(async (shot) => {
      const data = (await fs.readFile(shot.file)).toString("base64");
      return `<figure><img src="data:image/png;base64,${data}"><figcaption>${shot.name}</figcaption></figure>`;
    }),
  );
  const html = `<!doctype html><style>
    body { margin: 0; padding: 16px; background: #1e232a; color: #f0f4f8; font: 13px system-ui, sans-serif;
      display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 14px; align-items: start; }
    figure { margin: 0; background: #2b313a; padding: 8px; border-radius: 6px; }
    img { display: block; width: 100%; height: auto; max-height: 540px; object-fit: contain; border: 1px solid #444c56; background: #111; }
    figcaption { padding-top: 6px; font-weight: 500; text-align: center; }
  </style>${cells.join("")}`;
  const sheetPage = await browser.newPage({
    viewport: { width: 2000, height: 1200 },
  });
  await sheetPage.setContent(html);
  await sheetPage.screenshot({ path: file, fullPage: true });
  await sheetPage.close();
}

test("request_access end-to-end grant/deny, earlier partial decision, refusals, and interactive chat card", async ({
  page,
  request,
  browser,
  baseURL,
}) => {
  test.setTimeout(180_000);
  const shotsDir = process.env.SHOTS_DIR || "";
  const shotLog = [];
  const initialDeployment = await (await request.get("/api/deployment")).json();
  const createdTaskIds = [];

  try {
    // Register targetRepos allowlist and work-ownership so request_access check accepts
    // acme/docs and acme/lib while refusing unregistered repositories such as acme/unknown-repo.
    const deployRes = await request.put("/api/deployment", {
      data: { targetRepos: ["acme/widgets", "acme/docs", "acme/lib"] },
    });
    expect(deployRes.ok()).toBeTruthy();
    for (const repo of ["acme/widgets", "acme/docs", "acme/lib"]) {
      const res = await request.put(`/api/repos/${repo}/work-ownership`, {
        data: { default: "on" },
      });
      expect(res.ok()).toBeTruthy();
    }

    // Create an agent task holding api:tasks:admin (kept unapproved so it does not run)
    // to verify an agent's own api:tasks:admin grant cannot approve or deny access requests.
    const adminTaskRes = await request.post("/api/tasks", {
      data: {
        title: "Admin caller task",
        repo: "acme/widgets",
        capabilities: ["api:tasks:admin"],
      },
    });
    expect(adminTaskRes.ok()).toBeTruthy();
    const adminTask = await adminTaskRes.json();
    createdTaskIds.push(adminTask.id);

    // Queue 3 scripted MCP turns by title BEFORE creating the task so the 50ms
    // orchestrator ticker binds them on the very first dispatch:
    // - Turn 1: calls request_access with bad repo acme/unknown-repo and unknown
    //   capability nonexistent-cap (refused by check), fixes it in the same turn
    //   with 3 valid items, and parks.
    // - Turn 2: resumes holding the 2 granted items, then asks again for the 3rd item
    //   (api:tasks:admin) so the UI shows the earlier partial decision.
    // - Turn 3: resumes after denial and leaves a closing note.
    const bgTitle = `Cross-repo docs check ${Date.now()}`;
    const queueRes = await request.post("/e2e/script-runs", {
      data: {
        title: bgTitle,
        turns: [
          {
            mcpCalls: [
              {
                name: "request_access",
                arguments: {
                  summary: "Need unknown repo and nonexistent capability",
                  reason: "Initial typo in repository and capability names.",
                  requests: [
                    { kind: "read", repo: "acme/unknown-repo" },
                    { kind: "grant", capability: "nonexistent-cap" },
                  ],
                },
              },
              {
                name: "request_access",
                arguments: {
                  summary:
                    "Grant read on acme/docs and open network to verify docs",
                  reason:
                    "Need read access to acme/docs and outbound network to validate external links.",
                  requests: [
                    { kind: "read", repo: "acme/docs" },
                    { kind: "network", access: "open" },
                    { kind: "grant", capability: "api:tasks:admin" },
                  ],
                },
              },
            ],
            finalText: "Parking on corrected access request.",
          },
          {
            mcpCalls: [
              {
                name: "request_access",
                arguments: {
                  summary:
                    "Requesting api:tasks:admin again to inspect sibling tasks",
                  reason:
                    "Checking whether sibling tasks already updated the guide.",
                  requests: [{ kind: "grant", capability: "api:tasks:admin" }],
                },
              },
            ],
            finalText: "Parking on follow-up capability request.",
          },
          {
            mcpCalls: [
              {
                name: "comment_on_issue",
                arguments: {
                  comment:
                    "Completed verification using granted acme/docs read and open network.",
                },
              },
            ],
            finalText: "Verification complete.",
          },
        ],
      },
    });
    expect(queueRes.status()).toBe(204);

    // Create the background task starting with networkAccess: "none" and zero extra reads/grants.
    const createdRes = await request.post("/api/tasks", {
      data: {
        title: bgTitle,
        repo: "acme/widgets",
        description: "Verify retry behaviour against acme/docs.",
        networkAccess: "none",
      },
    });
    expect(createdRes.ok()).toBeTruthy();
    const bgTask = await createdRes.json();
    createdTaskIds.push(bgTask.id);
    if (!bgTask.approved) {
      const approveBg = await request.post(`/api/tasks/${bgTask.id}/approve`, {
        data: {},
      });
      expect(approveBg.ok()).toBeTruthy();
    }

    // Wait for Turn 1 to finish and park on the corrected access request.
    let lastBgDetail = null;
    await expect
      .poll(
        async () => {
          const res = await request.get(`/api/tasks/${bgTask.id}`);
          if (!res.ok()) return null;
          lastBgDetail = await res.json();
          return lastBgDetail.awaitingAccess
            ? lastBgDetail.state
            : lastBgDetail.state;
        },
        { timeout: 20_000 },
      )
      .toBe("awaiting_reply");
    expect(lastBgDetail?.awaitingAccess).toBe(true);

    // Verify Turn 1's first request_access failed the pre-park check and the second succeeded.
    const turn1Runs = await (
      await request.get(`/e2e/runs?task=${bgTask.id}`)
    ).json();
    expect(turn1Runs.runs).toHaveLength(1);
    expect(turn1Runs.runs[0].toolCalls).toHaveLength(2);
    expect(turn1Runs.runs[0].toolCalls[0].isError).toBe(true);
    expect(turn1Runs.runs[0].toolCalls[0].text).toContain(
      "This request cannot be relayed as it stands",
    );
    expect(turn1Runs.runs[0].toolCalls[0].text).toContain("acme/unknown-repo");
    expect(turn1Runs.runs[0].toolCalls[0].text).toContain("nonexistent-cap");
    expect(turn1Runs.runs[0].toolCalls[1].isError).toBe(false);

    // Item 2 Refusals over HTTP:
    // 1) Agent holding api:tasks:admin cannot grant or deny an access request.
    const accessCode = process.env.GRAIN_E2E_ACCESS_CODE;
    const agentGrantRes = await fetch(
      `${baseURL}/api/tasks/${bgTask.id}/access-request/grant`,
      {
        method: "POST",
        headers: {
          Authorization: `Bearer ${accessCode}`,
          "X-Grain-Acting-Task": adminTask.id,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ items: [0] }),
      },
    );
    expect(agentGrantRes.status).toBe(403);
    expect(await agentGrantRes.text()).toContain(
      "is not part of the agent API",
    );

    const agentDenyRes = await fetch(
      `${baseURL}/api/tasks/${bgTask.id}/access-request/deny`,
      {
        method: "POST",
        headers: {
          Authorization: `Bearer ${accessCode}`,
          "X-Grain-Acting-Task": adminTask.id,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({ reason: "agent trying to deny" }),
      },
    );
    expect(agentDenyRes.status).toBe(403);
    expect(await agentDenyRes.text()).toContain("is not part of the agent API");

    // 2) Deny without a reason is refused by the API with 400.
    const blankDenyRes = await request.post(
      `/api/tasks/${bgTask.id}/access-request/deny`,
      { data: { reason: "   " } },
    );
    expect(blankDenyRes.status()).toBe(400);
    expect(await blankDenyRes.text()).toContain(
      "reason is required: the next run is told why it was denied",
    );

    // Open Inbox, verify the card at both desktop (1440x900) and phone (390x844),
    // capture Inbox screenshots if SHOTS_DIR is set, and grant 2 of 3 items.
    for (const size of Object.values(VIEWPORTS)) {
      await page.setViewportSize(size);
      await page.goto("/inbox");
      const row = page.locator(".inbox-item", { hasText: bgTitle });
      await expect(row).toBeVisible();
      await row.getByRole("button", { name: "Reply" }).click();
      const inboxCard = row.getByRole("region", {
        name: "Access request waiting for your decision",
      });
      await expect(inboxCard).toBeVisible();
      await expect(row.getByPlaceholder("Reply...")).toHaveCount(0);
      await expectInside(inboxCard, page, size.width);
    }

    await captureSurfaceShots(
      page,
      "inbox",
      async (_w, size) => {
        const row = page.locator(".inbox-item", { hasText: bgTitle });
        await expect(row).toBeVisible();
        await row.getByRole("button", { name: "Reply" }).click();
        const inboxCard = row.getByRole("region", {
          name: "Access request waiting for your decision",
        });
        await expect(inboxCard).toBeVisible();
        await expectInside(inboxCard, page, size.width);
      },
      shotsDir,
      shotLog,
    );

    await page.setViewportSize(VIEWPORTS.desktop);
    await page.goto("/inbox");
    const inboxRow = page.locator(".inbox-item", { hasText: bgTitle });
    await inboxRow.getByRole("button", { name: "Reply" }).click();
    const inboxCard = inboxRow.getByRole("region", {
      name: "Access request waiting for your decision",
    });
    await expect(inboxCard).toBeVisible();
    // Untick item 2 (api:tasks:admin) and grant 2 of 3.
    await inboxCard
      .getByRole("checkbox", { name: /Grant api:tasks:admin/ })
      .click();
    await inboxCard.getByRole("button", { name: "Grant 2 of 3" }).click();

    // Wait for Turn 2 to run and park on the second access request.
    await expect
      .poll(async () => {
        const r = await (
          await request.get(`/e2e/runs?task=${bgTask.id}`)
        ).json();
        return r.runs?.length || 0;
      })
      .toBe(2);

    await expect
      .poll(async () => {
        const res = await request.get(`/api/tasks/${bgTask.id}`);
        if (!res.ok()) return false;
        const d = await res.json();
        return Boolean(d.awaitingAccess);
      })
      .toBe(true);

    const afterTurn2 = await (
      await request.get(`/e2e/runs?task=${bgTask.id}`)
    ).json();
    const turn2 = afterTurn2.runs[1];
    expect(turn2.reads).toEqual(["acme/docs"]);
    expect(turn2.networkAccess).toBe("open");
    expect(turn2.grants || []).not.toContain(
      "https://grain.dev/capabilities/api#tasks:admin",
    );
    expect(turn2.grants || []).not.toContain("api:tasks:admin");
    // The decision comment in Turn 2's prompt lists both granted and declined items.
    expect(turn2.prompt).toContain("Access request granted:");
    expect(turn2.prompt).toContain("read acme/docs");
    expect(turn2.prompt).toContain("open network");
    expect(turn2.prompt).toContain("Declined");
    expect(turn2.prompt).toContain("api:tasks:admin");

    // Open the task pane on the second request: verify earlier partial decision alert,
    // current holds, bounding boxes at desktop and phone, and Deny validation.
    for (const size of Object.values(VIEWPORTS)) {
      await page.setViewportSize(size);
      await page.goto(`/tasks/${bgTask.id}`);
      const taskCard = page.getByRole("region", {
        name: "Access request waiting for your decision",
      });
      await expect(taskCard).toBeVisible();
      await expect(taskCard.getByTestId("access-earlier")).toContainText(
        /Answered before by (console-rest-e2e-operator|access-code:alice@example\.com): 2 granted, 1 declined \(api:tasks:admin\)\./,
      );
      await expect(taskCard.getByTestId("access-holds")).toContainText(
        "reads acme/docs",
      );
      await expect(taskCard.getByTestId("access-holds")).toContainText(
        "network open",
      );
      await expectInside(taskCard, page, size.width);
    }

    await captureSurfaceShots(
      page,
      "task-pane",
      async (_w, size) => {
        const taskCard = page.getByRole("region", {
          name: "Access request waiting for your decision",
        });
        await expect(taskCard).toBeVisible();
        await expectInside(taskCard, page, size.width);
      },
      shotsDir,
      shotLog,
    );

    // Verify UI Deny requires a non-blank reason, then deny and confirm Turn 3 receives it.
    await page.setViewportSize(VIEWPORTS.desktop);
    await page.goto(`/tasks/${bgTask.id}`);
    const taskCard = page.getByRole("region", {
      name: "Access request waiting for your decision",
    });
    await taskCard.getByRole("button", { name: "Deny", exact: true }).click();
    const denyBtn = taskCard.getByRole("button", { name: "Deny request" });
    await expect(denyBtn).toBeDisabled();
    await taskCard.getByRole("textbox", { name: /Why/ }).fill("   ");
    await expect(denyBtn).toBeDisabled();
    await taskCard
      .getByRole("textbox", { name: /Why/ })
      .fill("Inspect task summary from parent note instead.");
    await expect(denyBtn).toBeEnabled();
    await denyBtn.click();

    await expect
      .poll(async () => {
        const r = await (
          await request.get(`/e2e/runs?task=${bgTask.id}`)
        ).json();
        return r.runs?.length || 0;
      })
      .toBe(3);

    const afterTurn3 = await (
      await request.get(`/e2e/runs?task=${bgTask.id}`)
    ).json();
    expect(afterTurn3.runs[2].prompt).toContain("Access request denied:");
    expect(afterTurn3.runs[2].prompt).toContain(
      "Inspect task summary from parent note instead.",
    );

    // Item 3: Interactive chat session parked on request_access.
    const chatTitle = `Interactive access check ${Date.now()}`;
    const chatQueueRes = await request.post("/e2e/script-runs", {
      data: {
        title: chatTitle,
        turns: [
          {
            mcpCalls: [
              {
                name: "request_access",
                arguments: {
                  summary: "Grant read on acme/lib in interactive chat",
                  reason:
                    "Interactive debugging session needs acme/lib to inspect shared types.",
                  requests: [
                    { kind: "read", repo: "acme/lib" },
                    { kind: "grant", capability: "api:memory" },
                    { kind: "grant", capability: "api:tasks:admin" },
                  ],
                },
              },
            ],
            finalText: "Waiting for interactive access grant.",
          },
          {
            mcpCalls: [
              {
                name: "comment_on_issue",
                arguments: {
                  comment:
                    "Interactive chat resumed with acme/lib read access.",
                },
              },
            ],
            finalText: "Interactive chat finished.",
          },
        ],
      },
    });
    expect(chatQueueRes.status()).toBe(204);

    const chatTaskRes = await request.post("/api/tasks", {
      data: {
        title: chatTitle,
        repo: "acme/widgets",
        interactive: true,
      },
    });
    expect(chatTaskRes.ok()).toBeTruthy();
    const chatTask = await chatTaskRes.json();
    createdTaskIds.push(chatTask.id);
    if (!chatTask.approved) {
      const approveChat = await request.post(
        `/api/tasks/${chatTask.id}/approve`,
        {
          data: {},
        },
      );
      expect(approveChat.ok()).toBeTruthy();
    }

    await expect
      .poll(
        async () => {
          const res = await request.get(`/api/tasks/${chatTask.id}`);
          if (!res.ok()) return false;
          const d = await res.json();
          return Boolean(d.awaitingAccess);
        },
        { timeout: 20_000 },
      )
      .toBe(true);

    // Regression check: interactive task parked on request_access surfaces in /inbox
    // with "Asked for access" (not hidden as "Waiting for your message"), and renders
    // PendingAccess inside TaskChat at both widths.
    await page.goto("/inbox");
    const chatInboxRow = page.locator(".inbox-item", { hasText: chatTitle });
    await expect(chatInboxRow).toBeVisible();
    await expect(chatInboxRow).not.toContainText("Waiting for your message");

    for (const size of Object.values(VIEWPORTS)) {
      await page.setViewportSize(size);
      await page.goto(`/tasks/${chatTask.id}`);
      const chatCard = page.getByRole("region", {
        name: "Access request waiting for your decision",
      });
      await expect(chatCard).toBeVisible();
      await expect(chatCard).toContainText(
        "Grant read on acme/lib in interactive chat",
      );
      await expectInside(chatCard, page, size.width);
    }

    await captureSurfaceShots(
      page,
      "interactive-chat",
      async (_w, size) => {
        const chatCard = page.getByRole("region", {
          name: "Access request waiting for your decision",
        });
        await expect(chatCard).toBeVisible();
        await expectInside(chatCard, page, size.width);
      },
      shotsDir,
      shotLog,
    );

    if (shotsDir) {
      await writeContactSheet(
        browser,
        shotLog,
        path.join(shotsDir, "contact-sheet.png"),
      );
    }

    // Grant 2 of 3 items (read acme/lib + grant api:memory) from the interactive chat card via the access-code session.
    await page.setViewportSize(VIEWPORTS.desktop);
    await page.goto(`/tasks/${chatTask.id}`);
    const chatCard = page.getByRole("region", {
      name: "Access request waiting for your decision",
    });
    await chatCard
      .getByRole("checkbox", { name: /Grant api:tasks:admin/ })
      .click();
    await chatCard.getByRole("button", { name: "Grant 2 of 3" }).click();

    await expect
      .poll(async () => {
        const r = await (
          await request.get(`/e2e/runs?task=${chatTask.id}`)
        ).json();
        return r.runs?.length || 0;
      })
      .toBe(2);

    const chatRuns = await (
      await request.get(`/e2e/runs?task=${chatTask.id}`)
    ).json();
    expect(chatRuns.runs[1].reads).toEqual(["acme/lib"]);
    expect(chatRuns.runs[1].grants || []).toContain(
      "https://grain.dev/capabilities/api#memory",
    );
    expect(chatRuns.runs[1].grants || []).not.toContain(
      "https://grain.dev/capabilities/api#tasks:admin",
    );
  } finally {
    for (const id of createdTaskIds) {
      await request
        .post(`/api/tasks/${id}/close`, {
          data: { reason: "access-request-walkthrough cleanup" },
        })
        .catch(() => {});
    }
    for (const repo of ["acme/docs", "acme/lib"]) {
      await request
        .put(`/api/repos/${repo}/work-ownership`, {
          data: { default: "off" },
        })
        .catch(() => {});
    }
    await request
      .put("/api/deployment", {
        data: { targetRepos: initialDeployment.targetRepos || [] },
      })
      .catch(() => {});
  }
});
