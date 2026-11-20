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

// Regression coverage for the 2026-11-19 §5 /next/ task lifecycle parity
// walkthrough defects:
// 1. #comment= deep-link focus and focus ring outlast the 4s highlight fill.
// 2. Task header More ▾ menu (act-more-menu) uses <li role="none"> so axe
//    reports zero critical/serious ARIA menu violations.
// 3. History renders Owner/Admission/Hosting change details ("from X to Y")
//    and spaces actor/reason cleanly.
// 4. Task Edit panel gates Keep running, Auto-merge, Auto-approve, Admission,
//    and priority > 5 for scoped roles (grain/contributor).

import { expect, test } from "./fixtures";

test.use({ demoArgs: ["-merge-rules", "-as", "next-task-lifecycle-parity"] });

async function apiJson(
  demoServer: string,
  accessCode: string,
  method: string,
  p: string,
  body?: unknown,
) {
  const res = await fetch(`${demoServer}${p}`, {
    method,
    headers: {
      Authorization: `Bearer ${accessCode}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  expect(res.ok, `${method} ${p}: ${res.status}`).toBe(true);
  const txt = await res.text();
  return txt ? JSON.parse(txt) : null;
}

test("#comment= deep link keeps focus and visible focus ring after the highlight fill fades", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const created = await apiJson(demoServer, accessCode, "POST", "/api/tasks", {
    title: "Deep link focus ring regression",
    description: "Verifies focus ring outlasts highlight fill.",
    no_repo: true,
    approved: false,
  });
  try {
    await apiJson(
      demoServer,
      accessCode,
      "POST",
      `/api/tasks/${created.id}/comments`,
      { body: "First linked comment" },
    );
    await apiJson(
      demoServer,
      accessCode,
      "POST",
      `/api/tasks/${created.id}/comments`,
      { body: "Second comment" },
    );
    const detail = await apiJson(
      demoServer,
      accessCode,
      "GET",
      `/api/tasks/${created.id}`,
    );
    const targetId = detail.comments[0].id as number;

    await page.goto(`task/${created.id}#comment=${targetId}`);
    const comment = page.locator(`article.msg[data-comment-id="${targetId}"]`);
    await expect(comment).toBeFocused();
    await expect(comment).toHaveClass(/\bcomment-highlight\b/);

    // Wait past HIGHLIGHT_MS (4000ms) for the highlight fill to fade.
    await expect(comment).not.toHaveClass(/\bcomment-highlight\b/, {
      timeout: 7000,
    });
    // Focus, tabindex="-1", and visible outline must remain until blur.
    await expect(comment).toBeFocused();
    await expect(comment).toHaveAttribute("tabindex", "-1");
    const outline = await comment.evaluate(
      (el) => getComputedStyle(el).outlineStyle,
    );
    expect(outline).toBe("solid");
  } finally {
    await apiJson(
      demoServer,
      accessCode,
      "POST",
      `/api/tasks/${created.id}/close`,
      {},
    ).catch(() => {});
  }
});

test("More lifecycle menu has clean ARIA roles and zero axe violations @phone @dark", async ({
  page,
  axe,
  demoServer,
  accessCode,
}) => {
  const body = await apiJson(demoServer, accessCode, "GET", "/api/tasks");
  const tasks = (Array.isArray(body) ? body : body.tasks) as Array<{
    id: string;
    title: string;
    state: string;
    mergeQueueState?: string;
  }>;
  const failed = tasks.find(
    (t) => t.state === "completed" && t.mergeQueueState === "merge_failed",
  );
  expect(failed, "seeded merge_failed task").toBeTruthy();

  await page.goto(`task/${failed!.id}`);
  await page.getByTestId("act-more").click();
  const menu = page.getByRole("menu", { name: "More lifecycle actions" });
  await expect(menu).toBeVisible();
  await expect(menu.getByRole("menuitem", { name: "Run now" })).toBeVisible();
  expect(await axe()).toEqual([]);
});

test("History shows Owner and Admission change values and Edit panel gates operator-only fields for scoped roles", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const created = await apiJson(demoServer, accessCode, "POST", "/api/tasks", {
    title: "History and role gating regression",
    description: "Checks History transfer details and contributor Edit gating.",
    repo: "acme/widgets",
    approved: false,
  });
  try {
    await page.goto(`task/${created.id}`);
    await page.getByTestId("act-edit").click();
    const panel = page.getByTestId("task-edit-panel");
    await expect(panel).toBeVisible();
    await panel.getByTestId("edit-owner").fill("bob@example.com");
    await panel.getByTestId("edit-admission").click();
    await panel.getByTestId("edit-admission-opt-bypass").click();
    await Promise.all([
      page.waitForResponse(
        (r) =>
          r.request().method() === "PATCH" &&
          r.url().endsWith(`/api/tasks/${created.id}`) &&
          r.status() === 200,
      ),
      panel.getByTestId("edit-save-btn").click(),
    ]);
    await expect(panel).toBeHidden();

    await page.reload();
    const history = page.locator(".history:visible");
    await expect(history).toContainText("Owner changed from");
    await expect(history).toContainText("to bob@example.com");
    await expect(history).toContainText(
      "Admission changed from Standard to Bypass queue",
    );

    // Reload as a scoped contributor (no full access, no tasks:admin):
    // Keep running, Auto-merge, Auto-approve, and Admission must be disabled,
    // and raising priority above 5 must be refused inline.
    await page.route("**/api/config", async (route) => {
      const res = await route.fetch();
      const cfg = await res.json();
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          ...cfg,
          access: {
            full: false,
            role: "grain/contributor",
            roles: ["grain/contributor"],
            scopes: ["tasks:write", "tasks:read"],
          },
        }),
      });
    });
    await page.reload();
    await page.getByTestId("act-edit").click();
    await expect(panel).toBeVisible();
    await expect(panel.getByTestId("edit-keep-running")).toBeDisabled();
    await expect(panel.getByTestId("edit-auto-merge")).toBeDisabled();
    await expect(panel.getByTestId("edit-auto-approve")).toBeDisabled();
    await expect(panel.getByTestId("edit-admission")).toBeDisabled();

    await panel.getByTestId("edit-priority").fill("25");
    await expect(panel.getByTestId("edit-save-btn")).toBeDisabled();
    await page.unroute("**/api/config");
  } finally {
    await apiJson(
      demoServer,
      accessCode,
      "POST",
      `/api/tasks/${created.id}/close`,
      {},
    ).catch(() => {});
  }
});
