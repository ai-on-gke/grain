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

// The /next/ Processes schedule and template forms carry the settings the
// classic forms do (task 6180): Admission, Hosting, Keep running, Grants
// and a searchable Model picker. Each round-trips through create and
// edit, and an edit PATCHes only what changed.

import type { Page, Request } from "@playwright/test";
import { expect, test } from "./fixtures";

test.use({
  demoArgs: ["-schedules", "-github-sim", "-fake-ci", "-scheduled-workflows"],
});
test.describe.configure({ mode: "serial" });

async function pickPill(page: Page, testid: string, value: string) {
  await page.getByTestId(testid).click();
  await page.getByTestId(`${testid}-opt-${value}`).click();
}

// The demo seeds no model connection, so each test adds one advertising
// MODEL (disabled, so the rotation never dispatches to it -- a picker
// still offers it) and puts the saved list back afterwards.
const MODEL = "e2e-forms-model";

async function seedModelConnection(page: Page): Promise<() => Promise<void>> {
  const before = await (await page.request.get("/api/settings")).json();
  const prev = (before.agentBackends ?? []) as unknown[];
  const res = await page.request.post("/api/settings", {
    data: {
      agentBackends: [
        ...prev,
        {
          id: "e2e-forms",
          name: "E2E forms",
          framework: "claude",
          models: [MODEL],
          disabled: true,
        },
      ],
    },
  });
  expect(res.status(), await res.text()).toBeLessThan(300);
  return async () => {
    await page.request.post("/api/settings", {
      data: { agentBackends: prev },
    });
  };
}

// pickModel chooses model from the searchable picker, typing to find it.
async function pickModel(page: Page, testid: string, model: string) {
  await page.getByTestId(testid).click();
  await expect(page.getByTestId(`${testid}-panel`)).toBeVisible();
  await page.getByTestId(`${testid}-search`).fill(model.slice(0, 8));
  await page
    .locator(`[data-testid="${testid}-opt"][data-model="${model}"]`)
    .click();
  await expect(page.getByTestId(`${testid}-panel`)).toHaveCount(0);
  await expect(page.getByTestId(testid)).toContainText(model);
}

// savedTemplate is the template as the server lists it.
async function savedTemplate(
  page: Page,
  id: string,
): Promise<Record<string, unknown>> {
  const all = (await (await page.request.get("/api/templates")).json()) as {
    id: string;
  }[];
  return (all.find((t) => t.id === id) ?? {}) as Record<string, unknown>;
}

// savedSchedule is the schedule as the server lists it.
async function savedSchedule(
  page: Page,
  id: string,
): Promise<Record<string, unknown>> {
  const all = (await (await page.request.get("/api/schedules")).json()) as {
    id: string;
  }[];
  return (all.find((s) => s.id === id) ?? {}) as Record<string, unknown>;
}

function bodyOf(req: Request): Record<string, unknown> {
  return (req.postDataJSON() ?? {}) as Record<string, unknown>;
}

test("a schedule's settings round-trip through create and a change-only edit", async ({
  page,
}) => {
  let createdId = "";
  const restore = await seedModelConnection(page);
  try {
    await page.goto("processes/schedules?new=1");
    const dlg = page.getByTestId("schedule-dialog");
    await expect(dlg).toBeVisible();
    await page.getByTestId("sched-title-input").fill("E2E settings schedule");
    await page.getByTestId("sched-repo-input").fill("acme/widgets");

    const model = MODEL;
    await pickModel(page, "sched-model", model);
    await page.getByTestId("sched-keep-running").click();
    await pickPill(page, "sched-admission", "durable");
    await expect(page.getByTestId("sched-admission-help")).toContainText(
      "no wall-clock deadline",
    );
    await pickPill(page, "sched-hosting", "console");

    // The draft preview reflects every setting.
    await page.getByTestId("sched-preview-composition-btn").click();
    const preview = dlg.getByTestId("composition-preview");
    await expect(preview).toContainText("admission");
    await expect(preview).toContainText("durable");
    await expect(preview).toContainText("hosting");
    await expect(preview).toContainText("console");
    await expect(preview).toContainText("keepRunning");
    await expect(preview).toContainText(model);

    const [createReq, createRes] = await Promise.all([
      page.waitForRequest(
        (r) => r.method() === "POST" && /\/api\/schedules$/.test(r.url()),
      ),
      page.waitForResponse(
        (r) =>
          r.request().method() === "POST" && /\/api\/schedules$/.test(r.url()),
      ),
      page.getByTestId("sched-save-btn").click(),
    ]);
    expect(createRes.status()).toBeLessThan(300);
    expect(bodyOf(createReq)).toMatchObject({
      admission: "durable",
      hosting: "console",
      keepRunning: true,
      model,
    });
    createdId = String((await createRes.json()).id);
    await expect(dlg).toHaveCount(0);

    // The server kept them.
    const saved = await savedSchedule(page, createdId);
    expect(saved).toMatchObject({
      admission: "durable",
      hosting: "console",
      keepRunning: true,
      model,
    });

    // Reopened, the form is seeded from the saved row; a save with no
    // change PATCHes none of the settings.
    await page.goto(`processes/schedules/${createdId}`);
    await page.getByTestId("sched-edit-btn").click();
    await expect(page.getByTestId("sched-admission")).toContainText("Durable");
    await expect(page.getByTestId("sched-hosting")).toContainText(
      "Interactive console",
    );
    await expect(page.getByTestId("sched-keep-running")).toHaveAttribute(
      "aria-checked",
      "true",
    );
    await expect(page.getByTestId("sched-model")).toContainText(model);
    const [noop] = await Promise.all([
      page.waitForRequest((r) => r.method() === "PATCH"),
      page.getByTestId("sched-save-btn").click(),
    ]);
    for (const k of [
      "admission",
      "hosting",
      "keepRunning",
      "model",
      "capabilities",
    ]) {
      expect(bodyOf(noop), k).not.toHaveProperty(k);
    }
    await expect(page.getByTestId("schedule-dialog")).toHaveCount(0);

    await page.getByTestId("sched-edit-btn").click();
    await pickPill(page, "sched-admission", "bypass");
    // "Use default" clears the schedule's own model.
    await page.getByTestId("sched-model-clear").click();
    const [changed] = await Promise.all([
      page.waitForRequest((r) => r.method() === "PATCH"),
      page.getByTestId("sched-save-btn").click(),
    ]);
    expect(bodyOf(changed)["admission"]).toBe("bypass");
    expect(bodyOf(changed)["model"]).toBe("");
    expect(bodyOf(changed)).not.toHaveProperty("hosting");
    await expect(page.getByTestId("schedule-dialog")).toHaveCount(0);
    await expect
      .poll(async () => {
        const row = await savedSchedule(page, createdId);
        return [row["admission"], row["model"] ?? ""];
      })
      .toEqual(["bypass", ""]);
  } finally {
    if (createdId) await page.request.delete(`/api/schedules/${createdId}`);
    await restore();
  }
});

test("a template's settings round-trip through create and a change-only edit", async ({
  page,
}) => {
  let createdId = "";
  const restore = await seedModelConnection(page);
  try {
    await page.goto("processes/templates?new=template");
    const dlg = page.getByTestId("template-dialog");
    await expect(dlg).toBeVisible();
    await page.getByTestId("tmpl-name-input").fill("E2E settings template");
    await page.getByTestId("tmpl-title-input").fill("chore: e2e settings");
    const model = MODEL;
    await pickModel(page, "tmpl-model", model);
    await page.getByTestId("tmpl-keep-running").click();
    // A grant made in the picker's builder (an agent API scope set, the
    // first the server offers) is held and saved with the template.
    await page.getByTestId("tmpl-grants-add-open").click();
    await page.getByTestId("tmpl-grants-mode-api").click();
    await page.getByTestId("tmpl-grants-attach").click();
    await expect(page.getByTestId("tmpl-grants-error")).toHaveCount(0);
    await expect(page.getByTestId("held-grant-0")).toBeVisible();
    await pickPill(page, "tmpl-admission", "bypass");
    await pickPill(page, "tmpl-hosting", "console");
    const [createReq, createRes] = await Promise.all([
      page.waitForRequest(
        (r) => r.method() === "POST" && /\/api\/templates$/.test(r.url()),
      ),
      page.waitForResponse(
        (r) =>
          r.request().method() === "POST" && /\/api\/templates$/.test(r.url()),
      ),
      page.getByTestId("tmpl-save-btn").click(),
    ]);
    expect(createRes.status()).toBeLessThan(300);
    expect(bodyOf(createReq)).toMatchObject({
      admission: "bypass",
      hosting: "console",
      keepRunning: true,
      model,
    });
    const grants = bodyOf(createReq)["capabilities"] as string[];
    expect(grants).toHaveLength(1);
    createdId = String((await createRes.json()).id);
    await expect(dlg).toHaveCount(0);
    const savedT = await savedTemplate(page, createdId);
    expect(savedT["model"]).toBe(model);
    expect((savedT["capabilities"] as string[]).length).toBe(1);

    // The saved template's composition says what every task it files gets.
    const comp = page.getByTestId("composition-preview").first();
    await expect(comp).toContainText("bypass");
    await expect(comp).toContainText("console");

    await page.getByTestId("tmpl-edit-btn").click();
    await expect(page.getByTestId("tmpl-admission")).toContainText(
      "Bypass queue",
    );
    await pickPill(page, "tmpl-hosting", "unattended");
    const [patch] = await Promise.all([
      page.waitForRequest((r) => r.method() === "PATCH"),
      page.getByTestId("tmpl-save-btn").click(),
    ]);
    expect(bodyOf(patch)["hosting"]).toBe("unattended");
    for (const k of ["admission", "keepRunning", "model", "capabilities"]) {
      expect(bodyOf(patch), k).not.toHaveProperty(k);
    }
  } finally {
    if (createdId) await page.request.delete(`/api/templates/${createdId}`);
    await restore();
  }
});

for (const tag of ["@phone", "@dark"]) {
  test(`the settings fit the schedule and template dialogs ${tag}`, async ({
    page,
  }) => {
    for (const [url, dialog, ids] of [
      [
        "processes/schedules?new=1",
        "schedule-dialog",
        ["sched-model", "sched-admission", "sched-hosting", "sched-grants"],
      ],
      [
        "processes/templates?new=template",
        "template-dialog",
        ["tmpl-model", "tmpl-admission", "tmpl-hosting", "tmpl-grants"],
      ],
    ] as const) {
      await page.goto(url);
      const dlg = page.getByTestId(dialog);
      await expect(dlg).toBeVisible();
      const box = (await dlg.boundingBox())!;
      const vw = page.viewportSize()!.width;
      expect(box.x).toBeGreaterThanOrEqual(0);
      expect(box.x + box.width).toBeLessThanOrEqual(vw + 0.5);
      for (const id of ids) {
        const el = page.getByTestId(id);
        await el.scrollIntoViewIfNeeded();
        const b = (await el.boundingBox())!;
        expect(b.x, id).toBeGreaterThanOrEqual(box.x - 0.5);
        expect(b.x + b.width, id).toBeLessThanOrEqual(box.x + box.width + 0.5);
      }
      // Nothing in the dialog overflows it sideways.
      expect(
        await dlg.evaluate((d) => d.scrollWidth - d.clientWidth),
      ).toBeLessThanOrEqual(0);
      // No two settings overlap: measured in one pass (no scrolling in
      // between), each control's field against every other's.
      const overlaps = await dlg.evaluate(
        (d, testids) => {
          const rects = testids.map((t) => {
            const el = d.querySelector(`[data-testid="${t}"]`)!;
            const f = el.closest(".field") ?? el;
            return { t, r: f.getBoundingClientRect() };
          });
          const out: string[] = [];
          for (let i = 0; i < rects.length; i++) {
            for (let j = i + 1; j < rects.length; j++) {
              const a = rects[i].r;
              const b = rects[j].r;
              const x = Math.min(a.right, b.right) - Math.max(a.left, b.left);
              const y = Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top);
              if (x > 1 && y > 1) out.push(`${rects[i].t}/${rects[j].t}`);
            }
          }
          return out;
        },
        ids as readonly string[],
      );
      expect(overlaps).toEqual([]);
      // Admission and Hosting pills share one height.
      const a = (await page.getByTestId(ids[1]).boundingBox())!;
      const h = (await page.getByTestId(ids[2]).boundingBox())!;
      expect(Math.abs(a.height - h.height)).toBeLessThanOrEqual(1);
    }
  });
}
