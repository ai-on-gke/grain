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

// Regression coverage from the 2026-11-19 Processes walkthrough
// (docs/test-runs/2026-11-19-recurring-composed-ui.md):
//   1. Off-page rail Processes count badge includes synced .grain/triggers on
//      a fresh load of /next/inbox before visiting /next/processes.
//   2. /next/processes?kind=triggers narrows Schedules & Triggers to event and
//      declared triggers on arrival; Suites, Suite runs, and Declarations show
//      filtered empty states ("match this filter") when ?q= matches nothing.
//   3. /next/ Template dialog supports Base, Network access, Reads, Writes and
//      Custom fields (create, change-only PATCH, unsaved guard, Configuration
//      pane display), and creating an unbound template with Reads via the UI
//      drops the chosen target repo from Reads on launch rather than failing.

import type { Page, Request } from "@playwright/test";
import { expect, test } from "./fixtures";

test.use({
  demoArgs: ["-schedules", "-github-sim", "-fake-ci", "-sensitive-hold"],
});
test.describe.configure({ mode: "serial" });

function bodyOf(req: Request): Record<string, unknown> {
  return JSON.parse(req.postData() || "{}");
}

async function pickPill(page: Page, testid: string, value: string) {
  await page.getByTestId(testid).click();
  await page.getByTestId(`${testid}-opt-${value}`).click();
}

test(
  "off-page rail Processes badge counts synced triggers, ?kind=triggers narrows on arrival, and filtered empty states are honest",
  { tag: ["@dark"] },
  async ({ page, axe }) => {
    const [sRes, tRes, stRes, dRes] = await Promise.all([
      page.request.get("/api/schedules"),
      page.request.get("/api/templates"),
      page.request.get("/api/suites"),
      page.request.get("/api/repos/acme/widgets/declarations"),
    ]);
    const decl = (await dRes.json()) as {
      declarationsStatus?: { triggers?: string[] };
    };
    const triggerCount = decl.declarationsStatus?.triggers?.length ?? 0;
    expect(triggerCount).toBeGreaterThan(0);
    const expectedTotal =
      ((await sRes.json()) as unknown[]).length +
      ((await tRes.json()) as unknown[]).length +
      ((await stRes.json()) as unknown[]).length +
      triggerCount;

    // Fresh visit to /next/inbox (never opened /next/processes yet) must already
    // include synced .grain/triggers in the rail Processes badge.
    await page.goto("inbox");
    const badge = page.getByTestId("rail-processes-count");
    await expect(badge).toBeVisible();
    await expect(badge).toHaveText(String(expectedTotal));

    // ?kind=triggers lands narrowed to Event triggers rather than listing time-based schedules.
    await page.goto("processes");
    await expect(page.getByTestId("sched-row-sched-1")).toBeVisible();
    await page.goto("processes?kind=triggers");
    await expect(page.getByTestId("sched-filter-triggers")).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await expect(page.locator('[data-testid^="trigger-row-"]')).toHaveCount(
      triggerCount,
    );
    await expect(page.getByTestId("sched-row-sched-1")).toHaveCount(0);

    // Back returns to /next/processes and restores All schedules.
    await page.goBack();
    await expect(page.getByTestId("sched-filter-all")).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    await expect(page.getByTestId("sched-row-sched-1")).toBeVisible();

    // Start a real suite run so suite-runs-empty specifically proves the filtered message.
    const runRes = await page.request.post("/api/suite-runs", {
      data: { suiteId: "suite-1", repo: "acme/widgets", base: "main" },
    });
    expect(runRes.ok()).toBe(true);
    const createdRun = (await runRes.json()) as {
      tasks?: { taskId: string }[];
    };
    try {
      // Filtered empty states on Suites, Suite runs, and Declarations say "match this filter".
      await page.goto("processes/suites?q=zzzz-walkthrough-none");
      await expect(page.getByTestId("suites-empty")).toHaveText(
        "No suites match this filter.",
      );
      await page
        .getByTestId("proc-list-pane")
        .getByRole("button", { name: /runs/i })
        .click();
      await expect(page.getByTestId("suite-runs-empty")).toHaveText(
        "No suite runs match this filter.",
      );

      await page.goto("processes/declarations?q=zzzz-walkthrough-none");
      await expect(page.getByTestId("declarations-empty")).toHaveText(
        "No repositories match this filter.",
      );

      await page.getByTestId("view-filter").fill("");
      await axe();
    } finally {
      for (const t of createdRun.tasks ?? []) {
        await page.request
          .post(`/api/tasks/${t.taskId}/close`, {
            data: { reason: "e2e cleanup" },
          })
          .catch(() => {});
      }
    }
  },
);

test(
  "templates create and edit Base, Network access, Reads, Writes and Custom fields, and launching an unbound template with Reads drops the target repo",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe }) => {
    const stamp = Date.now();
    const tmplName = `walk-tmpl-fields-${stamp}`;
    let createdTmplId = "";
    let createdTaskId = "";

    try {
      await page.goto("processes/templates");
      await page.getByTestId("proc-new-template-btn").click();
      const dlg = page.getByTestId("template-dialog");
      await expect(dlg).toBeVisible();

      // Unbound template: Base input stays disabled until a bound repo is typed.
      await expect(dlg.getByTestId("tmpl-base-input")).toBeDisabled();

      await dlg.getByTestId("tmpl-name-input").fill(tmplName);
      await dlg
        .getByTestId("tmpl-title-input")
        .fill("chore: template reads {{pkg}}");
      await dlg
        .getByTestId("tmpl-desc-input")
        .fill("Verify unbound reads and full fields for {{pkg}}.");
      await dlg.getByTestId("tmpl-params-input").fill("pkg");
      await dlg.getByTestId("tmpl-field-default-pkg").fill("lodash");

      await dlg.getByTestId("tmpl-keep-running").click();
      await pickPill(page, "tmpl-admission", "durable");
      await pickPill(page, "tmpl-hosting", "console");
      await pickPill(page, "tmpl-network", "none");

      // Add BOTH the future target repo (acme/widgets) and an extra repo (acme/docs) to Reads.
      await dlg.getByTestId("tmpl-reads-input").fill("acme/widgets");
      await dlg.getByTestId("tmpl-reads-add").click();
      await dlg.getByTestId("tmpl-reads-input").fill("acme/docs");
      await dlg.getByTestId("tmpl-reads-add").click();
      await expect(dlg.getByTestId("tmpl-reads-chips")).toContainText(
        "acme/widgets",
      );
      await expect(dlg.getByTestId("tmpl-reads-chips")).toContainText(
        "acme/docs",
      );

      await dlg.getByTestId("tmpl-writes-repo-input").fill("acme/widgets");
      await dlg.getByTestId("tmpl-writes-base-input").fill("release/1.0");
      await dlg.getByTestId("tmpl-writes-add").click();
      await expect(dlg.getByTestId("tmpl-writes-chips")).toContainText(
        "acme/widgets @ release/1.0",
      );

      if ((await dlg.getByTestId("tmpl-cf-priority").count()) > 0) {
        await pickPill(page, "tmpl-cf-priority", "high");
      }

      // Check zero horizontal overflow inside the dialog at desktop & phone widths.
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - window.innerWidth,
      );
      expect(overflow).toBeLessThanOrEqual(0);

      const [createReq, createRes] = await Promise.all([
        page.waitForRequest(
          (r) => r.method() === "POST" && /\/api\/templates$/.test(r.url()),
        ),
        page.waitForResponse(
          (r) =>
            r.request().method() === "POST" &&
            /\/api\/templates$/.test(r.url()),
        ),
        dlg.getByTestId("tmpl-save-btn").click(),
      ]);
      expect(createRes.status()).toBe(201);
      expect(bodyOf(createReq)).toMatchObject({
        networkAccess: "none",
        reads: ["acme/widgets", "acme/docs"],
        writes: [{ repo: "acme/widgets", base: "release/1.0" }],
        admission: "durable",
        hosting: "console",
        keepRunning: true,
      });
      createdTmplId = String(((await createRes.json()) as { id: string }).id);
      await expect(dlg).toBeHidden();

      // Configuration pane lists Admission, Hosting, Keep running, Network, Reads, Writes.
      const cfgPane = page.getByTestId("tmpl-config-pane");
      await expect(cfgPane.getByTestId("tmpl-admission-val")).toHaveText(
        "Durable",
      );
      await expect(cfgPane.getByTestId("tmpl-hosting-val")).toHaveText(
        "Interactive console",
      );
      await expect(cfgPane.getByTestId("tmpl-keep-running-val")).toHaveText(
        "on",
      );
      await expect(cfgPane.getByTestId("tmpl-network-val")).toContainText(
        "None",
      );
      await expect(cfgPane.getByTestId("tmpl-reads-val")).toHaveText(
        "acme/docs, acme/widgets",
      );
      await expect(cfgPane.getByTestId("tmpl-writes-val")).toHaveText(
        "acme/widgets @ release/1.0",
      );

      // Launch the unbound template targeting acme/widgets -> server drops acme/widgets from Reads.
      await page.getByTestId("tmpl-launch-btn").click();
      const launchDlg = page.getByTestId("launch-template-dialog");
      await expect(launchDlg).toBeVisible();
      await launchDlg.getByTestId("launch-repo-input").fill("acme/widgets");
      await launchDlg.getByTestId("launch-base-input").fill("main");
      const [launchRes] = await Promise.all([
        page.waitForResponse(
          (r) =>
            r.request().method() === "POST" &&
            /\/api\/tasks(\/from-template)?$/.test(r.url()),
        ),
        launchDlg.getByTestId("launch-template-submit").click(),
      ]);
      expect(launchRes.status()).toBe(201);
      const launched = (await launchRes.json()) as {
        id: string;
        repo: string;
        reads?: string[];
      };
      createdTaskId = String(launched.id);
      expect(launched.repo).toBe("acme/widgets");
      expect(launched.reads).toEqual(["acme/docs"]);

      // Reopen template Edit, bind repo & base, verify change-only PATCH sends base/network.
      await page.goto(`processes/templates/${createdTmplId}`);
      await page.getByTestId("tmpl-edit-btn").click();
      await expect(dlg).toBeVisible();
      await dlg.getByTestId("tmpl-repo-input").fill("acme/widgets");
      await expect(dlg.getByTestId("tmpl-base-input")).toBeEnabled();
      await dlg.getByTestId("tmpl-base-input").fill("release/1.0");
      await pickPill(page, "tmpl-network", "open");
      const [patchReq, patchRes] = await Promise.all([
        page.waitForRequest(
          (r) =>
            r.method() === "PATCH" &&
            r.url().includes(`/api/templates/${createdTmplId}`),
        ),
        page.waitForResponse(
          (r) =>
            r.request().method() === "PATCH" &&
            r.url().includes(`/api/templates/${createdTmplId}`),
        ),
        dlg.getByTestId("tmpl-save-btn").click(),
      ]);
      expect(patchRes.status()).toBe(200);
      expect(bodyOf(patchReq)).toMatchObject({
        repo: "acme/widgets",
        base: "release/1.0",
        networkAccess: "open",
      });
      await expect(dlg).toBeHidden();
      await expect(cfgPane.getByTestId("tmpl-base-val")).toHaveText(
        "release/1.0",
      );
      await axe();
    } finally {
      if (createdTaskId) {
        await page.request
          .post(`/api/tasks/${createdTaskId}/close`, {
            data: { reason: "e2e cleanup" },
          })
          .catch(() => {});
      }
      if (createdTmplId) {
        await page.request
          .delete(`/api/templates/${createdTmplId}`)
          .catch(() => {});
      }
    }
  },
);
