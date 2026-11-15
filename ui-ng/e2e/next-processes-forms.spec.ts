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

// pickFirstModel chooses the first model the catalog offers, if any, and
// returns it ("" on a deployment whose connections advertise none).
async function pickFirstModel(page: Page, testid: string): Promise<string> {
  await page.getByTestId(testid).click();
  await expect(page.getByTestId(`${testid}-panel`)).toBeVisible();
  const opt = page.getByTestId(`${testid}-opt`).first();
  if ((await opt.count()) === 0) {
    await page.keyboard.press("Escape");
    return "";
  }
  const model = (await opt.getAttribute("data-model")) ?? "";
  await opt.click();
  await expect(page.getByTestId(`${testid}-panel`)).toHaveCount(0);
  return model;
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
  try {
    await page.goto("processes/schedules?new=1");
    const dlg = page.getByTestId("schedule-dialog");
    await expect(dlg).toBeVisible();
    await page.getByTestId("sched-title-input").fill("E2E settings schedule");
    await page.getByTestId("sched-repo-input").fill("acme/widgets");

    const model = await pickFirstModel(page, "sched-model");
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
    if (model) await expect(preview).toContainText(model);

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
      ...(model ? { model } : {}),
    });
    createdId = String((await createRes.json()).id);
    await expect(dlg).toHaveCount(0);

    // The server kept them.
    const saved = await savedSchedule(page, createdId);
    expect(saved).toMatchObject({
      admission: "durable",
      hosting: "console",
      keepRunning: true,
      ...(model ? { model } : {}),
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
    const [changed] = await Promise.all([
      page.waitForRequest((r) => r.method() === "PATCH"),
      page.getByTestId("sched-save-btn").click(),
    ]);
    expect(bodyOf(changed)["admission"]).toBe("bypass");
    expect(bodyOf(changed)).not.toHaveProperty("hosting");
    await expect(page.getByTestId("schedule-dialog")).toHaveCount(0);
    await expect
      .poll(async () => (await savedSchedule(page, createdId))["admission"])
      .toBe("bypass");
  } finally {
    if (createdId) await page.request.delete(`/api/schedules/${createdId}`);
  }
});

test("a template's settings round-trip through create and a change-only edit", async ({
  page,
}) => {
  let createdId = "";
  try {
    await page.goto("processes/templates?new=template");
    const dlg = page.getByTestId("template-dialog");
    await expect(dlg).toBeVisible();
    await page.getByTestId("tmpl-name-input").fill("E2E settings template");
    await page.getByTestId("tmpl-title-input").fill("chore: e2e settings");
    const model = await pickFirstModel(page, "tmpl-model");
    await page.getByTestId("tmpl-keep-running").click();
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
      ...(model ? { model } : {}),
    });
    createdId = String((await createRes.json()).id);
    await expect(dlg).toHaveCount(0);

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
      // Admission and Hosting pills share one height.
      const a = (await page.getByTestId(ids[1]).boundingBox())!;
      const h = (await page.getByTestId(ids[2]).boundingBox())!;
      expect(Math.abs(a.height - h.height)).toBeLessThanOrEqual(1);
    }
  });
}
