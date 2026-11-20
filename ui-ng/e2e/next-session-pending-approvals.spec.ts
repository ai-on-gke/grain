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
import type { Locator, Page } from "@playwright/test";

// Pending approvals inline on /next/sessions/<id>:
//  1. Access requests (`pendingAccess`) driven end-to-end through the real
//     `grain demo` store and grant/deny routes (zero route stubs).
//  2. Config-repo changes (`pendingConfigChange`) and local-script approvals
//     (`localScriptApprovals`) with only the session task's pending fields
//     stubbed onto GET /api/tasks/<id>, verified at desktop and phone in light
//     and dark with axe.
//  3. Short-viewport and phone geometry (B5): composer and Send stay reachable
//     and clickable while tall inline approval cards are shown.

async function apiReq(
  baseURL: string,
  code: string,
  method: string,
  path: string,
  data?: unknown,
) {
  const res = await fetch(`${baseURL}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      ...(data === undefined ? {} : { "Content-Type": "application/json" }),
    },
    body: data === undefined ? undefined : JSON.stringify(data),
  });
  if (!res.ok) {
    throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
  }
  const text = await res.text();
  return text ? JSON.parse(text) : null;
}

async function hitsItself(locator: Locator): Promise<boolean> {
  return locator.evaluate((el) => {
    const r = el.getBoundingClientRect();
    if (r.width <= 0 || r.height <= 0) return false;
    const cx = r.left + r.width / 2;
    const cy = r.top + r.height / 2;
    if (cx < 0 || cy < 0 || cx > window.innerWidth || cy > window.innerHeight) {
      return false;
    }
    const hit = document.elementFromPoint(cx, cy);
    return !!hit && el.contains(hit);
  });
}

async function setTheme(page: Page, theme: "light" | "dark"): Promise<void> {
  await page.emulateMedia({ colorScheme: theme });
  await page.evaluate(async (t) => {
    localStorage.setItem("grain.themeMode", t);
    document.documentElement.dataset.theme = t;
    // Wait for any theme-switch color transitions (e.g. .msg 600ms) to finish
    // so axe measures the settled theme tokens rather than an intermediate frame.
    await new Promise<void>((r) => requestAnimationFrame(() => r()));
    await Promise.all(
      document
        .getAnimations()
        .filter((a) => a instanceof CSSTransition)
        .map((a) => a.finished.catch(() => {})),
    );
  }, theme);
}

test(
  "grants and denies a session's real pending access request inline in /next/sessions",
  { tag: ["@phone", "@dark"] },
  async ({ page, demoServer, accessCode, axe }) => {
    const title = `Approval session ${Date.now()}`;
    const created = await apiReq(
      demoServer,
      accessCode,
      "POST",
      "/api/console-sessions",
      { title, networkAccess: "none" },
    );
    const sessionId: string = created.created;
    const row = (created.sessions ?? []).find(
      (s: { id: string; taskId?: string }) => s.id === sessionId,
    );
    const taskId: string = row?.taskId;
    expect(sessionId).toBeTruthy();
    expect(taskId).toBeTruthy();

    try {
      // Park the session's real task on a real PendingAccessRequest in the demo store.
      await apiReq(
        demoServer,
        accessCode,
        "POST",
        `/api/e2e/tasks/${taskId}/pending-access`,
        {
          summary: "Open network for package index lookup",
          reason: "Need to verify the latest patch release.",
          items: [{ kind: "network", access: "open" }],
        },
      );

      await page.goto(`sessions/${sessionId}`);
      const region = page.getByRole("region", { name: "Pending approvals" });
      await expect(region).toBeVisible();
      const heading = region.getByRole("heading", {
        level: 3,
        name: /Waiting for an access grant: Open network for package index lookup/,
      });
      await expect(heading).toBeVisible();

      // Composer and Send remain reachable while the approval card is open.
      const composer = page.getByTestId("composer");
      const sendBtn = page.getByTestId("send");
      await expect(composer).toBeVisible();
      expect(await hitsItself(composer)).toBe(true);
      expect(await hitsItself(sendBtn)).toBe(true);

      // Axe passes with zero serious/critical violations.
      expect(await axe()).toEqual([]);

      // Grant goes straight to the real demo route (no route stubs).
      const grantReq = page.waitForRequest(
        (r) =>
          r.method() === "POST" &&
          r.url().endsWith(`/api/tasks/${taskId}/access-request/grant`),
      );
      const grantBtn = region.getByRole("button", { name: /^Grant/ });
      await grantBtn.scrollIntoViewIfNeeded();
      await grantBtn.click();
      await grantReq;
      await expect(region).toHaveCount(0, { timeout: 10_000 });

      const afterGrant = await apiReq(
        demoServer,
        accessCode,
        "GET",
        `/api/tasks/${taskId}`,
      );
      expect(afterGrant.pendingAccess ?? null).toBeNull();
      expect(afterGrant.networkAccess).toBe("open");

      // Park again and drive Deny through the real demo route.
      await apiReq(
        demoServer,
        accessCode,
        "POST",
        `/api/e2e/tasks/${taskId}/pending-access`,
        {
          summary: "Read acme/widgets repository",
          reason: "Want to inspect a template.",
          items: [{ kind: "read", repo: "acme/widgets" }],
        },
      );
      await expect(region).toBeVisible({ timeout: 10_000 });
      const denyOpen = region.getByRole("button", { name: "Deny…" });
      await denyOpen.scrollIntoViewIfNeeded();
      await denyOpen.click();
      await region
        .getByRole("textbox", { name: "Reason for denying access" })
        .fill("Stay read-only for now");

      const denyReq = page.waitForRequest(
        (r) =>
          r.method() === "POST" &&
          r.url().endsWith(`/api/tasks/${taskId}/access-request/deny`),
      );
      await region.getByRole("button", { name: "Deny request" }).click();
      expect((await denyReq).postDataJSON()).toEqual({
        reason: "Stay read-only for now",
      });
      await expect(region).toHaveCount(0, { timeout: 10_000 });

      const afterDeny = await apiReq(
        demoServer,
        accessCode,
        "GET",
        `/api/tasks/${taskId}`,
      );
      expect(afterDeny.pendingAccess ?? null).toBeNull();
    } finally {
      await apiReq(
        demoServer,
        accessCode,
        "DELETE",
        `/api/console-sessions/${sessionId}`,
      ).catch(() => {});
    }
  },
);

test(
  "shows pending config-repo change and local-script approval inline on /next/sessions/director in light and dark with axe",
  { tag: ["@phone", "@dark"] },
  async ({ page, demoServer, accessCode, axe }) => {
    const list = await apiReq(
      demoServer,
      accessCode,
      "GET",
      "/api/console-sessions",
    );
    const directorTaskId: string = (list.sessions ?? []).find(
      (s: { id: string; taskId?: string }) => s.id === "director",
    )?.taskId;
    expect(directorTaskId).toBeTruthy();

    let configPending = true;
    let scriptPending = true;

    // Stub ONLY the task detail's pending fields laid over the demo's own answer.
    await page.route(
      (u) => u.pathname === `/api/tasks/${directorTaskId}`,
      async (route, req) => {
        if (req.method() !== "GET") return route.continue();
        const res = await route.fetch();
        const body = await res.json();
        const patched = {
          ...body,
          state: configPending || scriptPending ? "awaiting_reply" : body.state,
          pendingConfigChange: configPending
            ? {
                summary: "Raise the director's model budget",
                branch: "grain/cfg-1",
                headSha: "abc1234567890",
                diff: "--- a/settings.yml\n+++ b/settings.yml\n@@\n-budget: 1\n+budget: 2\n",
                files: ["settings.yml"],
                validation: { ok: true },
              }
            : null,
          localScriptApprovals: scriptPending
            ? [
                {
                  id: "lsa-demo-1",
                  taskId: directorTaskId,
                  tool: "deploy",
                  script: "./deploy.sh",
                  args: ["--staging"],
                  status: "pending",
                },
              ]
            : [],
        };
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(patched),
        });
      },
    );

    await page.goto("sessions/director");
    const region = page.getByRole("region", { name: "Pending approvals" });
    await expect(region).toBeVisible();
    await expect(
      region.getByRole("heading", {
        level: 3,
        name: /Waiting for configuration change: Raise the director's model budget/,
      }),
    ).toBeVisible();
    await expect(
      region.getByRole("heading", {
        level: 3,
        name: /Local script approval: deploy/,
      }),
    ).toBeVisible();

    // Config diff lines are rendered and styled with diff tokens.
    const diffViewer = region.getByTestId("config-change-diff");
    await expect(diffViewer).toBeVisible();
    await expect(diffViewer).toContainText("settings.yml");
    await expect(diffViewer).toContainText("budget: 2");
    const diffStyled = await diffViewer
      .locator(".diff-line-add")
      .first()
      .evaluate((el) => {
        const bg = getComputedStyle(el).backgroundColor;
        return bg !== "" && bg !== "rgba(0, 0, 0, 0)" && bg !== "transparent";
      });
    expect(diffStyled, "diff-viewer add line has styled background").toBe(true);

    // Check no sideways scroll and clean axe in both themes on this viewport.
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      expect(
        overflow,
        `no horizontal overflow in ${theme}`,
      ).toBeLessThanOrEqual(1);
      expect(await axe(), `axe violations in ${theme}`).toEqual([]);
    }

    // Drive config-change approve against the real demo route first (no config
    // repo in stock demo, so the real refusal surfaces in the session banner).
    const approveBtn = region.getByRole("button", {
      name: "Approve & merge config",
    });
    await approveBtn.scrollIntoViewIfNeeded();
    const realApproveReq = page.waitForRequest(
      (r) =>
        r.method() === "POST" &&
        r.url().endsWith(`/api/tasks/${directorTaskId}/config-change/approve`),
    );
    await approveBtn.click();
    expect((await realApproveReq).postDataJSON()).toEqual({
      headSha: "abc1234567890",
    });
    await expect(page.getByTestId("action-error")).toBeVisible();

    // Drive Reject and Allow local script, confirming the exact payloads.
    await page.route(
      (u) =>
        u.pathname === `/api/tasks/${directorTaskId}/config-change/reject` ||
        u.pathname ===
          `/api/tasks/${directorTaskId}/local-script-approvals/lsa-demo-1/answer`,
      async (route, req) => {
        if (req.url().endsWith("/config-change/reject")) {
          configPending = false;
        } else {
          scriptPending = false;
        }
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ ok: true }),
        });
      },
    );

    const rejectOpen = region.getByRole("button", { name: "Reject…" });
    await rejectOpen.scrollIntoViewIfNeeded();
    await rejectOpen.click();
    await region
      .getByRole("textbox", { name: "Reason for rejecting config change" })
      .fill("Hold budget change");
    const rejectReq = page.waitForRequest(
      (r) =>
        r.method() === "POST" &&
        r.url().endsWith(`/api/tasks/${directorTaskId}/config-change/reject`),
    );
    await region.getByRole("button", { name: "Confirm reject" }).click();
    expect((await rejectReq).postDataJSON()).toEqual({
      headSha: "abc1234567890",
      reason: "Hold budget change",
    });
    await expect(
      region.getByRole("heading", {
        level: 3,
        name: /Waiting for configuration change/,
      }),
    ).toHaveCount(0);

    const allowBtn = region.getByRole("button", { name: "Allow" });
    await allowBtn.scrollIntoViewIfNeeded();
    const allowReq = page.waitForRequest(
      (r) =>
        r.method() === "POST" &&
        r
          .url()
          .endsWith(
            `/api/tasks/${directorTaskId}/local-script-approvals/lsa-demo-1/answer`,
          ),
    );
    await allowBtn.click();
    expect((await allowReq).postDataJSON()).toEqual({ approve: true });
    await expect(region).toHaveCount(0);
  },
);

test("keeps composer and Send reachable at phone and short sizes while tall approvals are shown", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const list = await apiReq(
    demoServer,
    accessCode,
    "GET",
    "/api/console-sessions",
  );
  const directorTaskId: string = (list.sessions ?? []).find(
    (s: { id: string; taskId?: string }) => s.id === "director",
  )?.taskId;
  expect(directorTaskId).toBeTruthy();

  const tallDiffLines = Array.from(
    { length: 24 },
    (_, i) => `-line_${i + 1}: old\n+line_${i + 1}: new`,
  ).join("\n");

  await page.route(
    (u) => u.pathname === `/api/tasks/${directorTaskId}`,
    async (route, req) => {
      if (req.method() !== "GET") return route.continue();
      const res = await route.fetch();
      const body = await res.json();
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          ...body,
          state: "awaiting_reply",
          pendingAccess: {
            summary: "Read billing and open network",
            reason: "Inspecting release usage metrics.",
            createdAt: "2026-11-18T10:00:00Z",
            items: [{ kind: "network", access: "open" }],
          },
          pendingConfigChange: {
            summary: "Raise the director's model budget",
            branch: "grain/cfg-1",
            headSha: "abc1234567890",
            diff: `diff --git a/settings.yml b/settings.yml\n--- a/settings.yml\n+++ b/settings.yml\n@@ -1,24 +1,24 @@\n${tallDiffLines}\n`,
            files: ["settings.yml"],
            validation: { ok: true },
          },
          localScriptApprovals: [
            {
              id: "lsa-geom-1",
              taskId: directorTaskId,
              tool: "deploy",
              script: "./deploy.sh",
              args: ["--check"],
              status: "pending",
            },
          ],
        }),
      });
    },
  );

  for (const [width, height] of [
    [1440, 900],
    [1280, 600],
    [1024, 600],
    [960, 540],
    [390, 844],
    [320, 568],
  ]) {
    await page.setViewportSize({ width, height });
    await page.goto("sessions/director");

    const region = page.getByRole("region", { name: "Pending approvals" });
    await expect(region).toBeAttached();

    const composer = page.getByTestId("composer");
    const sendBtn = page.getByTestId("send");
    await expect(composer).toBeVisible();
    await expect(sendBtn).toBeVisible();

    const sendBox = await sendBtn.boundingBox();
    expect(sendBox, `Send boundingBox at ${width}x${height}`).not.toBeNull();
    expect(
      sendBox!.y + sendBox!.height,
      `Send bottom inside viewport at ${width}x${height}`,
    ).toBeLessThanOrEqual(height);
    expect(
      await hitsItself(composer),
      `composer clickable at ${width}x${height}`,
    ).toBe(true);
    expect(
      await hitsItself(sendBtn),
      `Send clickable at ${width}x${height}`,
    ).toBe(true);

    const logBox = await page.getByTestId("chat-log").boundingBox();
    expect(logBox, `chat-log boundingBox at ${width}x${height}`).not.toBeNull();
    expect(
      logBox!.height,
      `chat-log not squashed at ${width}x${height}`,
    ).toBeGreaterThanOrEqual(width >= 1440 && height >= 900 ? 80 : 200);

    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(
      overflow,
      `no horizontal overflow at ${width}x${height}`,
    ).toBeLessThanOrEqual(1);

    // On phones and short screens where the pane scrolls as a whole, the
    // warning notice is immediately visible at scrollTop=0 and jumps straight
    // to the inline approval cards above the sticky composer.
    if (width <= 599 || height <= 760) {
      const notice = page.getByTestId("session-approvals-notice");
      await expect(notice).toBeVisible();
      await expect(notice).toContainText("3 pending approvals waiting");
      const jump = page.getByTestId("session-approvals-jump");
      expect(
        await hitsItself(jump),
        `Review approvals jump clickable at ${width}x${height}`,
      ).toBe(true);
      await jump.click();
      await expect(region).toBeInViewport();
    }

    // Every pending approval action is reachable by scrolling into view.
    const approveBtn = region.getByRole("button", {
      name: "Approve & merge config",
    });
    await approveBtn.scrollIntoViewIfNeeded();
    await expect(approveBtn).toBeVisible();
    expect(
      await hitsItself(approveBtn),
      `Approve button clickable after scroll at ${width}x${height}`,
    ).toBe(true);
  }
});
