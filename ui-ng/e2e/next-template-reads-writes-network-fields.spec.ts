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

// Covers the /next/ Processes template dialog's Reads, Writes, Network access
// and Custom fields controls (found in the 2026-11-20 Processes walkthrough,
// docs/test-runs/2026-11-20-recurring-composed-ui.md):
//   - Creating an unbound template with all four, checking Configuration and
//     composition preview, and verifying dialog fit & accessibility.
//   - Reopening Edit (seeded values), change-only PATCH on save, and unsaved
//     edit guard when dismissing changes.
//   - Launching an unbound template against a repository listed in its Reads:
//     the target repository is dropped from the filed task's reads rather than
//     failing, while the remaining reads, writes, network and custom fields
//     carry through onto the filed task.
//   - Declared templates remain read-only (no edit controls, ?edit=1 ignored).

import type { Page, Request } from "@playwright/test";
import { expect, test } from "./fixtures";

test.use({
  demoArgs: ["-schedules", "-github-sim", "-fake-ci"],
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
  "templates support Reads, Writes, Network access and Custom fields, drop the target repo from Reads on unbound launch, and keep declared templates read-only",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe, pageErrors }) => {
    const stamp = Date.now();
    const tmplName = `e2e-tmpl-extras-${stamp}`;
    let createdTmplId = "";
    let filedTaskId = "";

    try {
      await page.goto("processes/templates");
      await page.getByTestId("proc-new-template-btn").click();
      const dlg = page.getByTestId("template-dialog");
      await expect(dlg).toBeVisible();

      // Leave bound repository blank (unbound template).
      await dlg.getByTestId("tmpl-name-input").fill(tmplName);
      await dlg
        .getByTestId("tmpl-title-input")
        .fill(`chore: cross-repo check ${stamp}`);
      await dlg
        .getByTestId("tmpl-desc-input")
        .fill("Check cross-repo contracts and update release branch.");

      // 1. Network access
      await pickPill(page, "tmpl-network", "none");

      // 2. Reads: include the future target repo (acme/widgets) and an extra
      // read-only repo (acme/docs). For an unbound template, acme/widgets is
      // offered in the picker and accepted.
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

      // 3. Writes: additional write target on release/1.0 (a real branch in
      // -github-sim, distinct from main).
      await dlg.getByTestId("tmpl-writes-repo-input").fill("acme/widgets");
      await dlg.getByTestId("tmpl-writes-base-input").fill("release/1.0");
      await dlg.getByTestId("tmpl-writes-add").click();
      await expect(dlg.getByTestId("tmpl-writes-chips")).toContainText(
        "acme/widgets @ release/1.0",
      );

      // 4. Custom fields (seeded by grain demo: priority, area, requested-by,
      // customer-facing).
      await expect(dlg.getByTestId("tmpl-custom-fields")).toBeVisible();
      await pickPill(page, "tmpl-cf-priority", "high");
      await pickPill(page, "tmpl-cf-area", "backend");
      await expect(dlg.getByTestId("tmpl-cf-chips-area")).toContainText(
        "backend",
      );
      await dlg.getByTestId("tmpl-cf-requested-by").fill("PlatOps");
      await pickPill(page, "tmpl-cf-customer-facing", "true");

      // The four control groups fit inside the dialog without sideways
      // overflow at desktop and phone widths.
      expect(
        await dlg.evaluate((d) => d.scrollWidth - d.clientWidth),
      ).toBeLessThanOrEqual(0);
      await axe();

      // Create the template and verify the POST payload.
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
      expect(createRes.status(), await createRes.text()).toBeLessThan(300);
      expect(bodyOf(createReq)).toMatchObject({
        networkAccess: "none",
        reads: ["acme/widgets", "acme/docs"],
        writes: [{ repo: "acme/widgets", base: "release/1.0" }],
        customFields: {
          priority: "high",
          area: ["backend"],
          "requested-by": "PlatOps",
          "customer-facing": "true",
        },
      });
      createdTmplId = String(((await createRes.json()) as { id: string }).id);
      await expect(dlg).toBeHidden();

      // Template pane's Configuration shows all four.
      const cfgPane = page.getByTestId("tmpl-config-pane");
      await expect(cfgPane.getByTestId("tmpl-network-val")).toContainText(
        "None",
      );
      await expect(cfgPane.getByTestId("tmpl-reads-val")).toHaveText(
        "acme/docs, acme/widgets",
      );
      await expect(cfgPane.getByTestId("tmpl-writes-val")).toHaveText(
        "acme/widgets @ release/1.0",
      );
      const cfVal = cfgPane.getByTestId("tmpl-custom-fields-val");
      await expect(cfVal).toContainText("priority: high");
      await expect(cfVal).toContainText("area: backend");
      await expect(cfVal).toContainText("requested-by: PlatOps");
      await expect(cfVal).toContainText("customer-facing: true");

      // Composition preview also shows all four.
      const comp = page.getByTestId("composition-preview").first();
      await expect(comp.locator('[data-field="network"]')).toContainText(
        "none",
      );
      await expect(comp.locator('[data-field="reads"]')).toContainText(
        "acme/docs, acme/widgets",
      );
      await expect(comp.locator('[data-field="writes"]')).toContainText(
        "acme/widgets (release/1.0)",
      );
      const compCf = comp.locator('[data-field="customFields"]');
      await expect(compCf).toContainText("priority: high");
      await expect(compCf).toContainText("area: backend");
      await expect(compCf).toContainText("requested-by: PlatOps");
      await expect(compCf).toContainText("customer-facing: true");

      // Reopen Edit: all four are seeded from the saved template, and a no-op
      // save omits unchanged fields from PATCH.
      await page.getByTestId("tmpl-edit-btn").click();
      await expect(dlg).toBeVisible();
      await expect(dlg.getByTestId("tmpl-network")).toContainText("None");
      await expect(dlg.getByTestId("tmpl-reads-chips")).toContainText(
        "acme/widgets",
      );
      await expect(dlg.getByTestId("tmpl-reads-chips")).toContainText(
        "acme/docs",
      );
      await expect(dlg.getByTestId("tmpl-writes-chips")).toContainText(
        "acme/widgets @ release/1.0",
      );
      await expect(dlg.getByTestId("tmpl-cf-priority")).toContainText("high");
      await expect(dlg.getByTestId("tmpl-cf-chips-area")).toContainText(
        "backend",
      );
      await expect(dlg.getByTestId("tmpl-cf-requested-by")).toHaveValue(
        "PlatOps",
      );
      await expect(dlg.getByTestId("tmpl-cf-customer-facing")).toContainText(
        "Yes",
      );

      const [noopPatch] = await Promise.all([
        page.waitForRequest((r) => r.method() === "PATCH"),
        dlg.getByTestId("tmpl-save-btn").click(),
      ]);
      for (const k of ["networkAccess", "reads", "writes", "customFields"]) {
        expect(bodyOf(noopPatch), k).not.toHaveProperty(k);
      }
      await expect(dlg).toBeHidden();

      // Modifying Network access marks the draft dirty and triggers the
      // unsaved-edit confirmation on Escape.
      await page.getByTestId("tmpl-edit-btn").click();
      await expect(dlg).toBeVisible();
      await pickPill(page, "tmpl-network", "open");
      await page.keyboard.press("Escape");
      await expect(page.getByTestId("confirm-dialog")).toBeVisible();
      await page.getByTestId("confirm-accept").click();
      await expect(dlg).toBeHidden();
      await expect(cfgPane.getByTestId("tmpl-network-val")).toContainText(
        "None",
      );

      // Editing only Custom fields PATCHes customFields and omits unchanged
      // networkAccess, reads and writes.
      await page.getByTestId("tmpl-edit-btn").click();
      await expect(dlg).toBeVisible();
      await pickPill(page, "tmpl-cf-area", "infra");
      await dlg.getByTestId("tmpl-cf-requested-by").fill("ReleaseOps");
      const [cfPatch, cfPatchRes] = await Promise.all([
        page.waitForRequest((r) => r.method() === "PATCH"),
        page.waitForResponse(
          (r) =>
            r.request().method() === "PATCH" &&
            /\/api\/templates\//.test(r.url()),
        ),
        dlg.getByTestId("tmpl-save-btn").click(),
      ]);
      expect(cfPatchRes.status(), await cfPatchRes.text()).toBeLessThan(300);
      const cfPatchBody = bodyOf(cfPatch);
      expect(cfPatchBody).toMatchObject({
        customFields: {
          priority: "high",
          area: ["backend", "infra"],
          "requested-by": "ReleaseOps",
          "customer-facing": "true",
        },
      });
      for (const k of ["networkAccess", "reads", "writes"]) {
        expect(cfPatchBody, k).not.toHaveProperty(k);
      }
      await expect(dlg).toBeHidden();
      await expect(cfVal).toContainText("area: backend, infra");
      await expect(cfVal).toContainText("requested-by: ReleaseOps");

      // Launch the unbound template against acme/widgets (also listed in
      // Reads): the server drops acme/widgets from reads rather than failing
      // and carries the other reads, writes, network and custom fields onto
      // the filed task.
      await page.getByTestId("tmpl-launch-btn").click();
      const launchDlg = page.getByTestId("launch-template-dialog");
      await expect(launchDlg).toBeVisible();
      await launchDlg.getByTestId("launch-repo-input").fill("acme/widgets");
      await launchDlg.getByTestId("launch-base-input").fill("main");

      const [launchRes] = await Promise.all([
        page.waitForResponse(
          (r) =>
            r.request().method() === "POST" && /\/api\/tasks$/.test(r.url()),
        ),
        launchDlg.getByTestId("launch-template-submit").click(),
      ]);
      expect(launchRes.status(), await launchRes.text()).toBeLessThan(300);
      const launched = (await launchRes.json()) as { id: string };
      filedTaskId = String(launched.id);
      await expect(page).toHaveURL(new RegExp(`/task/${filedTaskId}$`));

      const taskRes = await page.request.get(`/api/tasks/${filedTaskId}`);
      expect(taskRes.status()).toBe(200);
      const filedTask = (await taskRes.json()) as {
        repo?: string;
        base?: string;
        reads?: string[];
        writes?: { repo: string; base?: string }[];
        networkAccess?: string;
        customFields?: Record<string, string | string[]>;
      };
      expect(filedTask.repo).toBe("acme/widgets");
      expect(filedTask.base).toBe("main");
      expect(filedTask.reads).toEqual(["acme/docs"]);
      expect(filedTask.writes).toEqual([
        { repo: "acme/widgets", base: "release/1.0" },
      ]);
      expect(filedTask.networkAccess).toBe("none");
      expect(filedTask.customFields).toMatchObject({
        priority: "high",
        area: ["backend", "infra"],
        "requested-by": "ReleaseOps",
        "customer-facing": "true",
      });

      // Declared templates stay read-only: no Edit button and ?edit=1 opens
      // nothing.
      const allTemplates = (await (
        await page.request.get("/api/templates")
      ).json()) as { id: string; declaredIn?: { repo: string } }[];
      const declared = allTemplates.find((t) => Boolean(t.declaredIn));
      expect(declared).toBeDefined();
      await page.goto(
        `processes/templates/${encodeURIComponent(declared!.id)}?edit=1`,
      );
      await expect(page.getByTestId("tmpl-declared-banner")).toBeVisible();
      await expect(page.getByTestId("template-dialog")).toHaveCount(0);
      await expect(page.getByTestId("tmpl-edit-btn")).toHaveCount(0);
      await expect(page.getByTestId("tmpl-delete-btn")).toHaveCount(0);
      await axe();
    } finally {
      if (filedTaskId) {
        await page.request
          .post(`/api/tasks/${filedTaskId}/close`, {
            data: { reason: "e2e cleanup" },
          })
          .catch(() => undefined);
      }
      if (createdTmplId) {
        await page.request
          .delete(`/api/templates/${createdTmplId}`)
          .catch(() => undefined);
      }
    }
    expect(pageErrors).toEqual([]);
  },
);
