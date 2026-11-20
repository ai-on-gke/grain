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

// Found in the 2026-11-18 §8 /next/ fixes walkthrough (finding F1; task 7059):
// the /next/processes schedule dialog offered editable Repository and Base
// branch inputs even when the selected template was bound to a repository, so
// edits to them appeared to save while the server replaced both with the
// template's binding. When a template is bound, Repository and Base are shown
// read-only beside a "Set by template <name>" note and omitted from PATCH.

import type { Request } from "@playwright/test";
import { expect, test } from "./fixtures";

function bodyOf(req: Request): Record<string, unknown> {
  return (req.postDataJSON() ?? {}) as Record<string, unknown>;
}

test("editing a template-bound schedule shows Repository and Base read-only and saves other changes cleanly @phone @dark", async ({
  page,
  axe,
  pageErrors,
}) => {
  const tmplRes = await page.request.post("/api/templates", {
    data: {
      name: "Bound default",
      title: "Bound default task",
      description: "Runs against the bound repository default branch.",
      repo: "acme/widgets",
      base: "",
    },
  });
  expect(tmplRes.ok(), await tmplRes.text()).toBe(true);
  const boundTmpl = (await tmplRes.json()) as { id: string; name: string };

  const unboundRes = await page.request.post("/api/templates", {
    data: {
      name: "Unbound audit",
      title: "Unbound audit task",
      description: "Can target any repository.",
    },
  });
  expect(unboundRes.ok(), await unboundRes.text()).toBe(true);
  const unboundTmpl = (await unboundRes.json()) as { id: string; name: string };

  const schedRes = await page.request.post("/api/schedules", {
    data: {
      templateId: boundTmpl.id,
      repo: "acme/widgets",
      recurrence: { kind: "everyNMinutes", everyNMinutes: 60 },
      overlap: "skip",
    },
  });
  expect(schedRes.ok(), await schedRes.text()).toBe(true);
  const boundSched = (await schedRes.json()) as { id: string };

  let createdNewId = "";
  try {
    await page.goto(`processes/schedules/${boundSched.id}`);
    await expect(page.getByTestId("sched-repo")).toHaveText("acme/widgets");
    await expect(page.getByTestId("sched-base")).toHaveCount(0);

    await page.getByTestId("sched-edit-btn").click();
    const dlg = page.getByRole("dialog", { name: "Edit schedule" });
    await expect(dlg).toBeVisible();

    // Bound template with empty base: read-only inputs show repo and
    // "Default branch", with no repo dropdown and a note naming the template.
    await expect(dlg.getByTestId("sched-repo-select")).toHaveCount(0);
    const repoInput = dlg.getByLabel("Repository (owner/name)");
    const baseInput = dlg.getByLabel("Base branch");
    await expect(repoInput).toHaveValue("acme/widgets");
    await expect(repoInput).toHaveAttribute("readonly", "");
    await expect(repoInput).not.toBeEditable();
    await expect(repoInput).toHaveAttribute(
      "aria-describedby",
      "sched-bound-target-note",
    );
    await expect(baseInput).toHaveValue("Default branch");
    await expect(baseInput).toHaveAttribute("readonly", "");
    await expect(baseInput).not.toBeEditable();
    await expect(baseInput).toHaveAttribute(
      "aria-describedby",
      "sched-bound-target-note",
    );

    const note = dlg.getByTestId("sched-bound-target-note");
    await expect(note).toBeVisible();
    await expect(note).toContainText("Set by template Bound default");

    // Layout check: both read-only inputs and the note stay inside the dialog
    // with zero sideways scroll; on desktop the two inputs align horizontally.
    const dlgBox = (await dlg.boundingBox())!;
    const repoBox = (await repoInput.boundingBox())!;
    const baseBox = (await baseInput.boundingBox())!;
    const noteBox = (await note.boundingBox())!;
    const vw = page.viewportSize()!.width;
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(vw);
    for (const b of [repoBox, baseBox, noteBox]) {
      expect(b.x).toBeGreaterThanOrEqual(dlgBox.x - 1);
      expect(b.x + b.width).toBeLessThanOrEqual(dlgBox.x + dlgBox.width + 1);
    }
    if (vw >= 1024) {
      expect(Math.abs(repoBox.y - baseBox.y)).toBeLessThanOrEqual(1);
      expect(Math.abs(repoBox.height - baseBox.height)).toBeLessThanOrEqual(1);
    }
    expect(noteBox.y).toBeGreaterThanOrEqual(
      Math.max(repoBox.y + repoBox.height, baseBox.y + baseBox.height) - 1,
    );

    // Accessibility check on the open dialog in desktop/phone/dark projects.
    const violations = await axe();
    expect(
      violations.map((v) => `${v.id} (${v.nodes.length}): ${v.help}`),
    ).toEqual([]);

    // Unchanged bound dialog closes on Escape with no discard prompt.
    await page.keyboard.press("Escape");
    await expect(dlg).toBeHidden();
    await expect(
      page.getByRole("dialog", { name: "Discard unsaved changes?" }),
    ).toHaveCount(0);

    // Editing another field (overlap) saves cleanly and omits repo/base from PATCH.
    await page.getByTestId("sched-edit-btn").click();
    await expect(dlg).toBeVisible();
    await dlg.getByRole("button", { name: "Allow overlap" }).click();
    const [patchReq, patchRes] = await Promise.all([
      page.waitForRequest(
        (r) =>
          r.method() === "PATCH" &&
          r.url().endsWith(`/api/schedules/${boundSched.id}`),
      ),
      page.waitForResponse(
        (r) =>
          r.request().method() === "PATCH" &&
          r.url().endsWith(`/api/schedules/${boundSched.id}`),
      ),
      dlg.getByTestId("sched-save-btn").click(),
    ]);
    expect(patchRes.status(), await patchRes.text()).toBe(200);
    const patchBody = bodyOf(patchReq);
    expect(patchBody).not.toHaveProperty("repo");
    expect(patchBody).not.toHaveProperty("base");
    expect(patchBody["overlap"]).toBe("allow");
    await expect(dlg).toBeHidden();
    await expect(page.getByTestId("sched-repo")).toHaveText("acme/widgets");
    await expect(page.getByTestId("sched-base")).toHaveCount(0);

    // Switching templates in the dialog toggles between bound (read-only) and
    // unbound (editable) target fields, and shows an explicit bound base too.
    await page.getByTestId("sched-edit-btn").click();
    await expect(dlg).toBeVisible();

    await dlg.getByTestId("sched-template-picker").click();
    await dlg.getByRole("option", { name: "release-qualification" }).click();
    await expect(dlg.getByLabel("Repository (owner/name)")).toHaveValue(
      "acme/widgets",
    );
    await expect(dlg.getByLabel("Base branch")).toHaveValue("main");
    await expect(dlg.getByLabel("Base branch")).not.toBeEditable();
    await expect(dlg.getByTestId("sched-bound-target-note")).toContainText(
      "Set by template release-qualification",
    );

    await dlg.getByTestId("sched-template-picker").click();
    await dlg.getByRole("option", { name: "Unbound audit" }).click();
    await expect(dlg.getByTestId("sched-bound-target-note")).toHaveCount(0);
    await expect(dlg.getByLabel("Repository (owner/name)")).toBeEditable();
    await expect(dlg.getByLabel("Base branch")).toBeEditable();

    // Switching back to the bound template locks them again.
    await dlg.getByTestId("sched-template-picker").click();
    await dlg.getByRole("option", { name: "Bound default" }).click();
    await expect(dlg.getByLabel("Base branch")).toHaveValue("Default branch");
    await expect(dlg.getByLabel("Base branch")).not.toBeEditable();
    await expect(dlg.getByTestId("sched-bound-target-note")).toContainText(
      "Set by template Bound default",
    );

    await page.keyboard.press("Escape");
    await expect(dlg).toBeHidden();

    // Creating a new schedule from a bound template shows the bound target
    // read-only, previews composition, and saves cleanly.
    await page.goto("processes/schedules?new=1");
    const newDlg = page.getByRole("dialog", {
      name: "New schedule or trigger",
    });
    await expect(newDlg).toBeVisible();
    await newDlg.getByTestId("sched-target-template").click();
    await newDlg.getByTestId("sched-template-picker").click();
    await newDlg.getByRole("option", { name: "Bound default" }).click();
    await expect(newDlg.getByLabel("Repository (owner/name)")).toHaveValue(
      "acme/widgets",
    );
    await expect(newDlg.getByLabel("Base branch")).toHaveValue(
      "Default branch",
    );
    await expect(newDlg.getByTestId("sched-bound-target-note")).toContainText(
      "Set by template Bound default",
    );

    // Draft composition preview resolves the bound target even though repo is sent empty.
    const [previewRes] = await Promise.all([
      page.waitForResponse(
        (r) =>
          r.request().method() === "POST" &&
          /\/api\/schedules\/composition$/.test(r.url()),
      ),
      newDlg.getByTestId("sched-preview-composition-btn").click(),
    ]);
    expect(previewRes.status(), await previewRes.text()).toBe(200);
    const previewBody = (await previewRes.json()) as {
      repo?: string;
      base?: string;
    };
    expect(previewBody.repo).toBe("acme/widgets");
    expect(previewBody.base ?? "").toBe("");

    const [createReq, createRes] = await Promise.all([
      page.waitForRequest(
        (r) => r.method() === "POST" && /\/api\/schedules$/.test(r.url()),
      ),
      page.waitForResponse(
        (r) =>
          r.request().method() === "POST" && /\/api\/schedules$/.test(r.url()),
      ),
      newDlg.getByTestId("sched-save-btn").click(),
    ]);
    expect(createRes.status(), await createRes.text()).toBe(201);
    const created = (await createRes.json()) as {
      id: string;
      repo: string;
      base?: string;
    };
    createdNewId = created.id;
    expect(bodyOf(createReq)["repo"]).toBe("");
    expect(bodyOf(createReq)).not.toHaveProperty("base");
    expect(created.repo).toBe("acme/widgets");
    expect(created.base ?? "").toBe("");
    await expect(newDlg).toBeHidden();
  } finally {
    if (createdNewId) {
      await page.request.delete(`/api/schedules/${createdNewId}`);
    }
    await page.request.delete(`/api/schedules/${boundSched.id}`);
    await page.request.delete(`/api/templates/${boundTmpl.id}`);
    await page.request.delete(`/api/templates/${unboundTmpl.id}`);
  }

  expect(pageErrors).toEqual([]);
});
