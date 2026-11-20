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

// End-to-end coverage for the /next/ Processes parity controls added for
// task 6916 (from the 2026-11-17 Processes walkthrough, task 6591):
//   1. Rail Processes count badge + direct "+ New template" / "+ New suite"
//      buttons on narrowed tabs.
//   2. Template "New task from this template" (carrying Task priority into
//      New task sheet, or opening Launch for parameterized templates),
//      phone-safe dialog footer buttons, and Launch's interactive live-chat note.
//   3. Schedule Reads, Writes, Network access and Custom fields (create,
//      draft preview, change-only PATCH, unsaved guard) + Configuration pane
//      listing Admission, Hosting and Keep running beside Model.
//   4. Suite searchable Templates combobox + inline "+ New template" stacking,
//      Escape listbox close, and zero-shift role gating when suites:write or
//      templates:write is refused.

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
  "rail Processes count badge loads off-page, updates live, and narrowed tabs open + New template / + New suite directly",
  { tag: ["@dark"] },
  async ({ page, axe }) => {
    const [sRes, tRes, stRes] = await Promise.all([
      page.request.get("/api/schedules"),
      page.request.get("/api/templates"),
      page.request.get("/api/suites"),
    ]);
    const initialExpected =
      ((await sRes.json()) as unknown[]).length +
      ((await tRes.json()) as unknown[]).length +
      ((await stRes.json()) as unknown[]).length;
    expect(initialExpected).toBeGreaterThan(0);

    await page.goto("inbox");
    const badge = page.getByTestId("rail-processes-count");
    await expect(badge).toBeVisible();
    await expect(badge).toHaveText(String(initialExpected));

    // Navigating onto Templates shows + New template directly (no New ▾ menu).
    await page.getByTestId("rail-processes").click();
    await page.getByTestId("proc-tab-templates").click();
    await expect(page.getByTestId("proc-new-menu-btn")).toHaveCount(0);
    await expect(page.getByTestId("proc-new-template-btn")).toHaveText(
      "+ New template",
    );

    const stamp = Date.now();
    let createdId: string | undefined;
    try {
      await page.getByTestId("proc-new-template-btn").click();
      const tmplDlg = page.getByTestId("template-dialog");
      await expect(tmplDlg).toBeVisible();
      await tmplDlg
        .getByTestId("tmpl-name-input")
        .fill(`parity-count-${stamp}`);
      await tmplDlg.getByTestId("tmpl-title-input").fill("chore: parity count");
      await tmplDlg.getByTestId("tmpl-save-btn").click();
      await expect(tmplDlg).toBeHidden();

      const afterTemplates = (
        (await (await page.request.get("/api/templates")).json()) as {
          id: string;
          name: string;
        }[]
      ).find((x) => x.name === `parity-count-${stamp}`);
      createdId = afterTemplates?.id;
      expect(afterTemplates).toBeTruthy();

      await expect
        .poll(async () => Number(await badge.textContent()))
        .toBeGreaterThanOrEqual(initialExpected + 1);

      // Suites tab shows + New suite directly; Schedules tab keeps New ▾.
      await page.getByTestId("proc-tab-suites").click();
      await expect(page.getByTestId("proc-new-menu-btn")).toHaveCount(0);
      await expect(page.getByTestId("proc-new-suite-btn")).toHaveText(
        "+ New suite",
      );

      await page.getByTestId("proc-tab-schedules").click();
      await expect(page.getByTestId("proc-new-menu-btn")).toHaveText("New ▾");
      await axe();
    } finally {
      if (createdId) {
        await page.request.delete(`/api/templates/${createdId}`);
      }
    }
  },
);

test(
  "templates carry Task priority into New task sheet, route parameterized templates to Launch, show interactive live-chat note, and keep footer buttons apart on phone",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe }) => {
    const stamp = Date.now();
    const noParamName = `parity-chat-${stamp}`;
    const paramName = `parity-param-${stamp}`;
    const createdIds: string[] = [];

    try {
      const r1 = await page.request.post("/api/templates", {
        data: {
          name: noParamName,
          title: "chore: interactive sweep",
          description: "Run interactive sweep.",
          priority: 1000,
          interactive: true,
        },
      });
      expect(r1.ok()).toBe(true);
      const tNoParam = (await r1.json()) as { id: string };
      createdIds.push(tNoParam.id);

      const r2 = await page.request.post("/api/templates", {
        data: {
          name: paramName,
          title: "chore: bump {{pkg}}",
          description: "Bump {{pkg}}.",
          fields: [{ name: "pkg", label: "Package", required: true }],
        },
      });
      expect(r2.ok()).toBe(true);
      const tParam = (await r2.json()) as { id: string };
      createdIds.push(tParam.id);

      await page.goto(`processes/templates/${tNoParam.id}`);
      await expect(page.getByTestId("tmpl-detail-title")).toHaveText(
        noParamName,
      );

      // Launch task dialog shows interactive live-chat note and hides Approved switch.
      await page.getByTestId("tmpl-launch-btn").click();
      const launchDlg = page.getByTestId("launch-template-dialog");
      await expect(launchDlg).toBeVisible();
      await expect(launchDlg.getByTestId("launch-interactive-note")).toHaveText(
        "This template files a live chat: the agent can talk with you while it works, and the chat opens as soon as the task is filed.",
      );
      await expect(launchDlg.getByRole("switch")).toHaveCount(0);
      await launchDlg.getByRole("button", { name: "Cancel" }).click();
      await expect(launchDlg).toBeHidden();

      // Edit dialog footer buttons stay 1-line tall and non-overlapping (desktop & phone).
      await page.getByTestId("tmpl-edit-btn").click();
      const tmplDlg = page.getByTestId("template-dialog");
      await expect(tmplDlg).toBeVisible();
      const newTaskBtn = tmplDlg.getByTestId("tmpl-dialog-new-task-btn");
      const cancelBtn = tmplDlg.getByRole("button", { name: "Cancel" });
      const saveBtn = tmplDlg.getByTestId("tmpl-save-btn");
      await expect(newTaskBtn).toBeVisible();

      const boxes = await Promise.all([
        newTaskBtn.boundingBox(),
        cancelBtn.boundingBox(),
        saveBtn.boundingBox(),
      ]);
      for (const b of boxes) {
        expect(b).not.toBeNull();
        expect(b!.height).toBeLessThanOrEqual(44);
      }
      for (let i = 0; i < boxes.length; i++) {
        for (let j = i + 1; j < boxes.length; j++) {
          const a = boxes[i]!;
          const b = boxes[j]!;
          const overlapX =
            Math.min(a.x + a.width, b.x + b.width) - Math.max(a.x, b.x);
          const overlapY =
            Math.min(a.y + a.height, b.y + b.height) - Math.max(a.y, b.y);
          expect(overlapX > 1 && overlapY > 1).toBe(false);
        }
      }

      // Clicking "New task from this template" opens New task sheet with priority 1000.
      await newTaskBtn.click();
      await expect(tmplDlg).toBeHidden();
      const sheet = page.getByTestId("new-task-sheet");
      await expect(sheet).toBeVisible();
      const ntPri = sheet.getByTestId("nt-priority");
      await expect(ntPri).toHaveValue("1000");
      await ntPri.fill("1e3");
      await ntPri.blur();
      await expect(ntPri).toHaveValue("1000");
      await page.keyboard.press("Escape");
      const discardBtn = page.getByTestId("confirm-accept");
      if (await discardBtn.isVisible()) {
        await discardBtn.click();
      }
      await expect(sheet).toBeHidden();

      // Parameterized template routes "New task from this template" to Launch dialog.
      await page.goto(`processes/templates/${tParam.id}`);
      await page.getByTestId("tmpl-new-task-btn").click();
      await expect(page.getByTestId("new-task-sheet")).toHaveCount(0);
      await expect(page.getByTestId("launch-template-dialog")).toBeVisible();
      await axe();
    } finally {
      for (const id of createdIds) {
        await page.request.delete(`/api/templates/${id}`);
      }
    }
  },
);

test(
  "schedules support Reads, Writes, Network access and Custom fields with change-only PATCH, and Configuration pane lists Admission, Hosting and Keep running",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe }) => {
    const configRes = await page.request.get("/api/config");
    const cfg = (await configRes.json()) as {
      customFields?: { name: string; type: string; options?: string[] }[];
    };
    const hasPriorityCf = (cfg.customFields ?? []).some(
      (f) => f.name === "priority",
    );
    const hasRequestedByCf = (cfg.customFields ?? []).some(
      (f) => f.name === "requested-by",
    );

    let createdId = "";
    try {
      await page.goto("processes/schedules?new=1");
      const dlg = page.getByTestId("schedule-dialog");
      await expect(dlg).toBeVisible();

      await dlg.getByTestId("sched-title-input").fill("Parity nightly audit");
      await dlg
        .getByTestId("sched-desc-input")
        .fill("Audit dependencies and open follow-ups.");
      await dlg.getByTestId("sched-repo-input").fill("acme/widgets");
      await dlg.getByTestId("sched-keep-running").click();
      await pickPill(page, "sched-admission", "bypass");
      await pickPill(page, "sched-hosting", "console");
      await pickPill(page, "sched-network", "none");

      // Add a read repository and an extra write target.
      await dlg.getByTestId("sched-reads-input").fill("acme/docs");
      await dlg.getByTestId("sched-reads-add").click();
      await expect(dlg.getByTestId("sched-reads-chips")).toContainText(
        "acme/docs",
      );

      await dlg.getByTestId("sched-writes-repo-input").fill("acme/widgets");
      await dlg.getByTestId("sched-writes-base-input").fill("release/v1");
      await dlg.getByTestId("sched-writes-add").click();
      await expect(dlg.getByTestId("sched-writes-chips")).toContainText(
        "acme/widgets @ release/v1",
      );

      if (hasPriorityCf) {
        await pickPill(page, "sched-cf-priority", "high");
      }
      if (hasRequestedByCf) {
        await dlg.getByTestId("sched-cf-requested-by").fill("PlatOps");
      }

      // Preview composition reflects draft network, reads, writes, admission, hosting, keepRunning.
      const [prevReq] = await Promise.all([
        page.waitForRequest(
          (r) =>
            r.method() === "POST" &&
            /\/api\/schedules\/composition$/.test(r.url()),
        ),
        dlg.getByTestId("sched-preview-composition-btn").click(),
      ]);
      expect(bodyOf(prevReq)).toMatchObject({
        admission: "bypass",
        hosting: "console",
        keepRunning: true,
        networkAccess: "none",
        reads: ["acme/docs"],
        writes: [{ repo: "acme/widgets", base: "release/v1" }],
      });
      await expect(dlg.getByTestId("composition-preview")).toBeVisible();

      // Save schedule and verify Configuration pane lists Admission, Hosting and Keep running.
      const [createReq, createRes] = await Promise.all([
        page.waitForRequest(
          (r) => r.method() === "POST" && /\/api\/schedules$/.test(r.url()),
        ),
        page.waitForResponse(
          (r) =>
            r.request().method() === "POST" &&
            /\/api\/schedules$/.test(r.url()),
        ),
        dlg.getByTestId("sched-save-btn").click(),
      ]);
      expect(createRes.status(), await createRes.text()).toBeLessThan(300);
      expect(bodyOf(createReq)).toMatchObject({
        networkAccess: "none",
        reads: ["acme/docs"],
        writes: [{ repo: "acme/widgets", base: "release/v1" }],
      });
      createdId = String(((await createRes.json()) as { id: string }).id);
      await expect(dlg).toBeHidden();

      const cfgPane = page.getByTestId("sched-config-pane");
      await expect(cfgPane.getByTestId("sched-admission-val")).toHaveText(
        "Bypass queue",
      );
      await expect(cfgPane.getByTestId("sched-hosting-val")).toHaveText(
        "Interactive console",
      );
      await expect(cfgPane.getByTestId("sched-keep-running-val")).toHaveText(
        "on",
      );
      await expect(cfgPane.getByTestId("sched-network-val")).toContainText(
        "None",
      );
      await expect(cfgPane.getByTestId("sched-reads-val")).toHaveText(
        "acme/docs",
      );
      await expect(cfgPane.getByTestId("sched-writes-val")).toHaveText(
        "acme/widgets @ release/v1",
      );

      // Reopen and perform a no-op save -> PATCH omits unchanged reads/writes/network/customFields.
      await page.getByTestId("sched-edit-btn").click();
      await expect(page.getByTestId("schedule-dialog")).toBeVisible();
      const [noopPatch] = await Promise.all([
        page.waitForRequest((r) => r.method() === "PATCH"),
        page.getByTestId("sched-save-btn").click(),
      ]);
      for (const k of [
        "admission",
        "hosting",
        "keepRunning",
        "networkAccess",
        "reads",
        "writes",
        "customFields",
      ]) {
        expect(bodyOf(noopPatch), k).not.toHaveProperty(k);
      }
      await expect(page.getByTestId("schedule-dialog")).toBeHidden();

      // Unsaved network edit triggers Discard confirmation on Escape.
      await page.getByTestId("sched-edit-btn").click();
      await pickPill(page, "sched-network", "open");
      await page.keyboard.press("Escape");
      await expect(page.getByTestId("confirm-dialog")).toBeVisible();
      await page.getByTestId("confirm-accept").click();
      await expect(page.getByTestId("schedule-dialog")).toBeHidden();
      await axe();
    } finally {
      if (createdId) {
        await page.request.delete(`/api/schedules/${createdId}`);
      }
    }
  },
);

test(
  "suite dialog provides searchable Templates combobox, inline + New template stacking, and zero-shift role gating",
  { tag: ["@phone", "@dark"] },
  async ({ page, axe }) => {
    const stamp = Date.now();
    const inlineTmplName = `suite-inline-${stamp}`;
    let createdTmplId = "";
    let createdSuiteId = "";

    try {
      await page.goto("processes/suites");
      await page.getByTestId("proc-new-suite-btn").click();
      const suiteDlg = page.getByTestId("suite-dialog");
      await expect(suiteDlg).toBeVisible();

      // Open searchable Templates combobox, filter options, and verify Escape closes listbox only.
      const combo = suiteDlg.getByTestId("suite-templates-combobox");
      await combo.click();
      await expect(
        suiteDlg.getByTestId("suite-templates-listbox"),
      ).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(suiteDlg.getByTestId("suite-templates-listbox")).toHaveCount(
        0,
      );
      await expect(suiteDlg).toBeVisible();

      // Inline + New template stacks template-dialog over suite-dialog and preselects saved template.
      await suiteDlg.getByTestId("suite-new-template-btn").click();
      const tmplDlg = page.getByTestId("template-dialog");
      await expect(tmplDlg).toBeVisible();
      await expect(suiteDlg).toHaveAttribute("inert", "");

      await tmplDlg.getByTestId("tmpl-name-input").fill(inlineTmplName);
      await tmplDlg
        .getByTestId("tmpl-title-input")
        .fill("chore: inline suite pass");
      await tmplDlg
        .getByTestId("tmpl-desc-input")
        .fill("Verify inline suite template creation.");
      const [tRes] = await Promise.all([
        page.waitForResponse(
          (r) =>
            r.request().method() === "POST" &&
            /\/api\/templates$/.test(r.url()),
        ),
        tmplDlg.getByTestId("tmpl-save-btn").click(),
      ]);
      createdTmplId = String(((await tRes.json()) as { id: string }).id);
      await expect(tmplDlg).toBeHidden();
      await expect(suiteDlg).toBeVisible();
      await expect(suiteDlg).not.toHaveAttribute("inert");
      await expect(page).toHaveURL(/\/processes\/suites/);
      await expect(
        suiteDlg.getByTestId(`suite-selected-tmpl-${createdTmplId}`),
      ).toBeVisible();

      // Filter combobox by the newly created template's name + title words.
      await combo.fill(`${inlineTmplName} inline`);
      const listbox = suiteDlg.getByTestId("suite-templates-listbox");
      await expect(listbox.getByRole("option")).toHaveCount(1);
      await expect(
        listbox.getByTestId(`suite-tmpl-opt-${createdTmplId}`),
      ).toContainText("chore: inline suite pass");
      await page.keyboard.press("Escape");

      // Save the suite so we can compare allowed vs refused bounding boxes on Edit.
      await suiteDlg
        .getByTestId("suite-name-input")
        .fill(`Parity suite ${stamp}`);
      const [sRes] = await Promise.all([
        page.waitForResponse(
          (r) =>
            r.request().method() === "POST" && /\/api\/suites$/.test(r.url()),
        ),
        suiteDlg.getByTestId("suite-save-btn").click(),
      ]);
      createdSuiteId = String(((await sRes.json()) as { id: string }).id);
      await expect(suiteDlg).toBeHidden();

      // Measure allowed bounding boxes for + New template and Save suite.
      const measureBoxes = async () => {
        await page.goto(`processes/suites/${createdSuiteId}?edit=1`);
        const dlg = page.getByTestId("suite-dialog");
        await expect(dlg).toBeVisible();
        const newTmplBtn = dlg.getByTestId("suite-new-template-btn");
        const saveBtn = dlg.getByTestId("suite-save-btn");
        await expect(newTmplBtn).toBeVisible();
        await expect(saveBtn).toBeVisible();
        return {
          newTmpl: (await newTmplBtn.boundingBox())!,
          save: (await saveBtn.boundingBox())!,
          newTmplDisabled: await newTmplBtn.isDisabled(),
          saveDisabled: await saveBtn.isDisabled(),
        };
      };

      const allowed = await measureBoxes();
      expect(allowed.newTmplDisabled).toBe(false);
      expect(allowed.saveDisabled).toBe(false);

      // Role with templates:write ONLY (lacking suites:write): Save suite is disabled,
      // + New template stays enabled and in the exact same (x, y) spot.
      let mockedRole: "tmpl-only" | "viewer" | "repo-a-only" | null =
        "tmpl-only";
      const cfgMatcher = (url: URL) => url.pathname === "/api/config";
      await page.route(cfgMatcher, async (route) => {
        const res = await route.fetch();
        const json = (await res.json()) as Record<string, unknown>;
        if (mockedRole === "tmpl-only") {
          json["access"] = {
            role: "grain/template-author",
            roles: ["grain/template-author"],
            full: false,
            scopes: [
              "tasks:read",
              "schedules:read",
              "templates:read",
              "templates:write",
              "suites:read",
            ],
          };
        } else if (mockedRole === "viewer") {
          json["access"] = {
            role: "grain/viewer",
            roles: ["grain/viewer"],
            full: false,
            scopes: [
              "tasks:read",
              "schedules:read",
              "templates:read",
              "suites:read",
            ],
          };
        } else if (mockedRole === "repo-a-only") {
          json["access"] = {
            role: "walk/repo-a-editor",
            roles: ["walk/repo-a-editor"],
            full: false,
            scopes: [
              "tasks:read",
              "schedules:read",
              "templates:read",
              "suites:read",
              "suites:write",
            ],
            repoLimits: { "suites:write": ["acme/widgets"] },
          };
        }
        await route.fulfill({ response: res, json });
      });

      let tmplRepoAId = "";
      let tmplRepoBId = "";
      let multiRepoSuiteId = "";
      const tmplMatcher = (url: URL) => url.pathname === "/api/templates";
      try {
        const tmplOnly = await measureBoxes();
        expect(tmplOnly.newTmplDisabled).toBe(false);
        expect(tmplOnly.saveDisabled).toBe(true);
        expect(Math.abs(tmplOnly.newTmpl.x - allowed.newTmpl.x)).toBeLessThan(
          1.5,
        );
        expect(Math.abs(tmplOnly.newTmpl.y - allowed.newTmpl.y)).toBeLessThan(
          1.5,
        );
        expect(Math.abs(tmplOnly.save.y - allowed.save.y)).toBeLessThan(1.5);

        // Pure viewer role (lacking both suites:write and templates:write):
        // both buttons disabled in place with zero layout shift.
        mockedRole = "viewer";
        const viewer = await measureBoxes();
        expect(viewer.newTmplDisabled).toBe(true);
        expect(viewer.saveDisabled).toBe(true);
        expect(Math.abs(viewer.newTmpl.x - allowed.newTmpl.x)).toBeLessThan(
          1.5,
        );
        expect(Math.abs(viewer.newTmpl.y - allowed.newTmpl.y)).toBeLessThan(
          1.5,
        );
        expect(Math.abs(viewer.save.y - allowed.save.y)).toBeLessThan(1.5);

        // Repo-limited suites:write (acme/widgets only) editing a suite whose second
        // template targets a different repo (acme/other) disables Save and names
        // acme/other in its refusal tooltip.
        const rA = await page.request.post("/api/templates", {
          data: {
            name: `parity-repo-a-${stamp}`,
            title: "chore: repo a",
            description: "Repo A",
            repo: "acme/widgets",
          },
        });
        expect(rA.ok(), await rA.text()).toBe(true);
        tmplRepoAId = String(((await rA.json()) as { id: string }).id);
        const rB = await page.request.post("/api/templates", {
          data: {
            name: `parity-repo-b-${stamp}`,
            title: "chore: repo b",
            description: "Repo B",
          },
        });
        expect(rB.ok(), await rB.text()).toBe(true);
        tmplRepoBId = String(((await rB.json()) as { id: string }).id);
        const rS = await page.request.post("/api/suites", {
          data: {
            name: `Parity multi-repo ${stamp}`,
            templateIds: [tmplRepoAId, tmplRepoBId],
            mode: "count",
            count: 1,
          },
        });
        expect(rS.ok(), await rS.text()).toBe(true);
        multiRepoSuiteId = String(((await rS.json()) as { id: string }).id);

        await page.route(tmplMatcher, async (route) => {
          const res = await route.fetch();
          const list = (await res.json()) as Array<Record<string, unknown>>;
          const patched = list.map((t) =>
            t["id"] === tmplRepoBId ? { ...t, repo: "acme/other" } : t,
          );
          await route.fulfill({ response: res, json: patched });
        });

        mockedRole = "repo-a-only";
        await page.goto(`processes/suites/${multiRepoSuiteId}?edit=1`);
        const repoDlg = page.getByTestId("suite-dialog");
        await expect(repoDlg).toBeVisible();
        const repoSaveBtn = repoDlg.getByTestId("suite-save-btn");
        await expect(repoSaveBtn).toBeDisabled();
        const saveBox = (await repoSaveBtn.boundingBox())!;
        await page.mouse.move(
          saveBox.x + saveBox.width / 2,
          saveBox.y + saveBox.height / 2,
        );
        const tip = page.locator("#g-tip");
        await expect(tip).toBeVisible({ timeout: 3000 });
        await expect(tip).toContainText(
          "does not hold suites:write on acme/other",
        );
        await axe();
      } finally {
        await page.unroute(tmplMatcher);
        await page.unroute(cfgMatcher);
        if (multiRepoSuiteId) {
          await page.request.delete(`/api/suites/${multiRepoSuiteId}`);
        }
        if (tmplRepoBId) {
          await page.request.delete(`/api/templates/${tmplRepoBId}`);
        }
        if (tmplRepoAId) {
          await page.request.delete(`/api/templates/${tmplRepoAId}`);
        }
      }
    } finally {
      if (createdSuiteId) {
        await page.request.delete(`/api/suites/${createdSuiteId}`);
      }
      if (createdTmplId) {
        await page.request.delete(`/api/templates/${createdTmplId}`);
      }
    }
  },
);
