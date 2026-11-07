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

import { expect, test } from "./fixtures.js";

// Settings → Capabilities lists the in-process grain and repo servers
// (pkg/capability/grainserver) beside the configured ones, with their
// methods and who holds them (2026-11-02 walkthrough, finding 6): a
// deployment default granting grain#host-exec, a template granting
// grain#sql write and a task granting repo#write each show up under
// "Granted by", at desktop and phone widths, light and dark.

const GRAIN = "https://grain.dev/capabilities/grain";
const REPO = "https://grain.dev/capabilities/repo";
const GITHUB = "https://grain.dev/capabilities/github";
const HOST_EXEC = `${GRAIN}#host-exec={}`;
const SQL_WRITE = `${GRAIN}#sql={"write":true}`;
const PUSH_ANY = `${REPO}#write={"force":true,"refs":["refs/heads/*","refs/tags/*"]}`;
const READ_ISSUES = `${GITHUB}#read-issues={}`;

const VIEWPORTS = [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
];

function row(page, id) {
  return page.locator(`[data-capability-server-row="${id}"]`);
}

// within asserts inner's box lies inside outer's.
function within(inner, outer, what) {
  expect(inner.x, `${what} left`).toBeGreaterThanOrEqual(outer.x - 0.5);
  expect(inner.x + inner.width, `${what} right`).toBeLessThanOrEqual(
    outer.x + outer.width + 0.5,
  );
}

test("lists the in-process grain and repo servers with their methods and holders", async ({
  page,
  request,
}) => {
  const settings = await (await request.get("/api/settings")).json();
  const defaultsBefore = settings.defaultCapabilities || [];
  const made = {};
  try {
    expect(
      (
        await request.put("/api/settings", {
          data: { defaultCapabilities: [...defaultsBefore, HOST_EXEC] },
        })
      ).ok(),
    ).toBeTruthy();
    const template = await request.post("/api/templates", {
      data: {
        name: `e2e-in-process-${Date.now()}`,
        title: "Database maintenance",
        capabilities: [SQL_WRITE],
      },
    });
    expect(template.ok()).toBeTruthy();
    made.template = (await template.json()).id;
    const task = await request.post("/api/tasks", {
      data: {
        title: "E2E retag the release branch",
        description: "d",
        repo: "acme/widgets",
        capabilities: [PUSH_ANY, "read-issues"],
      },
    });
    expect(task.ok()).toBeTruthy();
    made.task = (await task.json()).id;

    for (const theme of ["light", "dark"]) {
      for (const [name, viewport] of VIEWPORTS) {
        await page.emulateMedia({ colorScheme: theme });
        await page.setViewportSize(viewport);
        await page.goto("/settings/capabilities#capability-servers");

        for (const [id, short] of [
          [GRAIN, "grain"],
          [REPO, "repo"],
          [GITHUB, "github"],
        ]) {
          const r = row(page, id);
          await expect(r).toHaveAttribute("data-in-process-row", short);
          await expect(r).toContainText(id);
          await expect(r.getByTestId("capability-server-kind")).toHaveText(
            "built-in",
          );
          await expect(r.getByTestId("capability-server-runs")).toHaveText(
            "in-process",
          );
          await expect(r.getByTestId("capability-server-state")).toHaveText(
            "attached",
          );
          await expect(
            r.getByTestId("capability-server-usage-count"),
          ).toHaveText(/granted by [1-9]\d*/);
          // The row, and every chip on it, stays inside the page.
          const box = await r.boundingBox();
          expect(box.x, `${name} ${theme} ${short} row`).toBeGreaterThanOrEqual(
            0,
          );
          expect(box.x + box.width).toBeLessThanOrEqual(viewport.width);
          for (const chip of await r.locator(".MuiChip-root").all())
            within(await chip.boundingBox(), box, `${short} chip`);
        }
        // In-process rows follow the configured built-ins and precede
        // the operator's own servers.
        const order = await page
          .locator("[data-capability-server-row]")
          .evaluateAll((els) =>
            els.map((e) =>
              e.dataset.builtinRow
                ? "builtin"
                : e.dataset.inProcessRow
                  ? "in-process"
                  : "custom",
            ),
          );
        expect(order.join(",")).toMatch(/^(builtin,)+in-process,in-process,/);
        const overflow = await page.evaluate(
          () =>
            document.documentElement.scrollWidth -
            document.documentElement.clientWidth,
        );
        expect(overflow, `${name} ${theme} horizontal overflow`).toBe(0);

        // The grain server's page: its methods and every holder, with
        // nothing to configure.
        await row(page, GRAIN).click();
        const card = page.locator(`[data-capability-server="${GRAIN}"]`);
        await expect(card).toHaveAttribute("data-in-process", "grain");
        const methods = card.getByTestId("capability-server-methods");
        for (const m of ["health", "host-exec", "journal", "source", "sql"])
          await expect(methods).toContainText(m);
        const usage = card.getByTestId("capability-server-usage");
        // The demo's own deployment defaults hold grain#source (once
        // self-debug) too, so this picks the host-exec one out.
        const hostExecDefault = usage
          .locator('[data-holder="default:deployment"]')
          .filter({ hasText: HOST_EXEC });
        await expect(hostExecDefault).toHaveCount(1);
        await expect(hostExecDefault.locator("a")).toHaveAttribute(
          "href",
          "/settings/tasks#task-defaults",
        );
        await expect(
          usage.locator(`[data-holder="template:${made.template}"]`),
        ).toContainText(SQL_WRITE);
        await expect(
          card.getByTestId("capability-server-in-process"),
        ).toBeVisible();
        await expect(
          card.getByRole("button", { name: /Edit connection|Remove|Disable/ }),
        ).toHaveCount(0);
        const cardBox = await card.boundingBox();
        expect(cardBox.x + cardBox.width).toBeLessThanOrEqual(viewport.width);

        // The repo server's page names the task.
        await page.getByTestId("capability-servers-back").click();
        await row(page, REPO).click();
        const repoCard = page.locator(`[data-capability-server="${REPO}"]`);
        await expect(
          repoCard.locator(`[data-holder="task:${made.task}"]`),
        ).toContainText(PUSH_ANY);
        await expect(
          repoCard.getByTestId("capability-server-methods"),
        ).toContainText("write");

        // The github server's page lists read-issues and names the task
        // whose bare read-issues alias was recorded as github#read-issues={}.
        await page.getByTestId("capability-servers-back").click();
        await row(page, GITHUB).click();
        const ghCard = page.locator(`[data-capability-server="${GITHUB}"]`);
        await expect(
          ghCard.locator(`[data-holder="task:${made.task}"]`),
        ).toContainText(READ_ISSUES);
        await expect(
          ghCard.getByTestId("capability-server-methods"),
        ).toContainText("read-issues");
        const ghBox = await ghCard.boundingBox();
        expect(ghBox.x + ghBox.width).toBeLessThanOrEqual(viewport.width);
      }
    }
  } finally {
    await request.put("/api/settings", {
      data: { defaultCapabilities: defaultsBefore },
    });
    if (made.template) await request.delete(`/api/templates/${made.template}`);
    if (made.task) {
      await request.post(`/api/tasks/${made.task}/capabilities`, {
        data: { capability: PUSH_ANY, attach: false },
      });
      await request.post(`/api/tasks/${made.task}/capabilities`, {
        data: { capability: READ_ISSUES, attach: false },
      });
    }
  }
});
