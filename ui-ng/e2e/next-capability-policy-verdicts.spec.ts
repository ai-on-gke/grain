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

import type { APIRequestContext, Locator, Page } from "@playwright/test";
import { expect, test } from "./fixtures";

// The capability policy's verdict on every /next/ capability picker
// (task 6220, from the 2026-11-13 connections walkthrough, where a
// capability a Deny rule refused showed nothing in the New task sheet).
// A real policy is put through PUT /api/capability-policy over `grain
// demo`'s cloud capability server:
//
//   1. Deny cloud#cloud-viewer, everywhere.
//   2. Admit cloud#cloud-key only on acme/other-*, so it may be refused
//      for agent tasks at large and is refused on acme/widgets.
//
// cloud-viewer is granted before the policy lands -- as the deployment's
// and acme/widgets' default and on a template -- so each picker holds a
// grant the policy now denies. Every test puts back what it changed.

const CLOUD = "https://capabilities.demo.grain.example/cloud";
const VIEWER = `${CLOUD}#cloud-viewer`;
const KEY = `${CLOUD}#cloud-key`;
const POLICY_HREF = "/next/settings/capabilities#capability-policy";
const SHOTS = process.env["GRAIN_UI_SHOTS"];

async function json(
  req: Promise<{
    ok(): boolean;
    json(): Promise<unknown>;
    text(): Promise<string>;
  }>,
) {
  const res = await req;
  if (!res.ok()) throw new Error(await res.text());
  return (await res.json()) as Record<string, unknown>;
}

async function withPolicy(
  request: APIRequestContext,
  body: (ids: { template: string }) => Promise<void>,
) {
  const settings = await json(request.get("/api/settings"));
  const defaults = (settings["defaultCapabilities"] as string[]) ?? [];
  const policy = await json(request.get("/api/capability-policy"));
  const repoCaps = await json(
    request.get("/api/repos/acme/widgets/capabilities"),
  );
  const repoDefaults = (repoCaps["defaultCapabilities"] as string[]) ?? [];
  let template = "";
  try {
    await json(
      request.put("/api/settings", {
        data: { defaultCapabilities: [...defaults, `${VIEWER}={}`] },
      }),
    );
    await json(
      request.put("/api/repos/acme/widgets/capabilities", {
        data: { defaultCapabilities: [...repoDefaults, `${VIEWER}={}`] },
      }),
    );
    const t = await json(
      request.post("/api/templates", {
        data: {
          name: "policy-verdicts",
          title: "chore: policy verdicts",
          description: "Held grants the policy restricts.",
          capabilities: [`${VIEWER}={}`],
        },
      }),
    );
    template = String(t["id"]);
    await json(
      request.put("/api/capability-policy", {
        data: {
          policy: {
            rules: [
              { capability: VIEWER, deny: true },
              { capability: KEY, repos: ["acme/other-*"] },
            ],
          },
        },
      }),
    );
    await body({ template });
  } finally {
    await request.put("/api/capability-policy", { data: policy });
    if (template) await request.delete(`/api/templates/${template}`);
    await request.put("/api/repos/acme/widgets/capabilities", {
      data: { defaultCapabilities: repoDefaults },
    });
    await request.put("/api/settings", {
      data: { defaultCapabilities: defaults },
    });
  }
}

// heldVerdict is the verdict link on the held row for ref inside picker.
function heldVerdict(picker: Locator, ref: string): Locator {
  return picker
    .getByRole("listitem")
    .filter({ has: picker.page().locator(`span[title^="${ref}"]`) })
    .getByRole("link");
}

async function expectLinksToPolicy(link: Locator) {
  await expect(link).toHaveAttribute("href", POLICY_HREF);
}

async function shoot(page: Page, name: string) {
  if (!SHOTS) return;
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    await page.screenshot({ path: `${SHOTS}/${name}-${scheme}.png` });
  }
  await page.emulateMedia({ colorScheme: null });
}

test("New task sheet marks a held denied grant and lists refused methods disabled, linking to the policy @phone @dark", async ({
  page,
  request,
  axe,
}, testInfo) => {
  await withPolicy(request, async () => {
    await page.goto("tasks");
    const phone = (page.viewportSize()?.width ?? 1440) < 600;
    await page.getByTestId(phone ? "phone-new-task" : "rail-new-task").click();
    const sheet = page.getByTestId("new-task-sheet");
    await expect(sheet).toBeVisible();
    await sheet.getByTestId("caps-options").click();
    const picker = sheet.getByTestId("nt-grants");

    const denied = heldVerdict(picker, VIEWER);
    await expect(denied).toHaveText("denied by policy");
    await expect(denied).toHaveAccessibleName(
      /capability policy denies .*cloud-viewer.* open the Capability policy/,
    );
    await expectLinksToPolicy(denied);
    // The verdict sits on the same line as the grant it is about.
    const chip = picker
      .getByRole("listitem")
      .filter({ has: page.locator(`span[title^="${VIEWER}"]`) });
    const chipBox = await chip.boundingBox();
    const linkBox = await denied.boundingBox();
    expect(chipBox && linkBox).toBeTruthy();
    expect(linkBox!.y).toBeGreaterThanOrEqual(chipBox!.y - 1);
    expect(linkBox!.y + linkBox!.height).toBeLessThanOrEqual(
      chipBox!.y + chipBox!.height + 1,
    );

    await picker.getByTestId("nt-grants-add-open").click();
    await picker.getByTestId("nt-grants-mode-method").click();
    await picker.getByTestId("nt-grants-method-ref").click();
    const keyOpt = page.getByTestId(`nt-grants-method-ref-opt-${KEY}`);
    await expect(keyOpt).toHaveAttribute("aria-disabled", "true");
    await expect(keyOpt).toContainText("denied by policy");
    await expect(
      page.getByTestId(`nt-grants-method-ref-opt-${VIEWER}`),
    ).toHaveAttribute("aria-disabled", "true");
    await shoot(page, `new-task-${testInfo.project.name}`);
    await page.keyboard.press("Escape");

    // The verdict badges are checked for contrast with the rest.
    expect(await axe()).toEqual([]);
    // Nothing in the picker runs past the sheet's edge.
    const sheetBox = (await sheet.boundingBox())!;
    for (const box of await picker
      .locator("a, li, button")
      .evaluateAll((els) => els.map((e) => e.getBoundingClientRect().right))) {
      expect(box).toBeLessThanOrEqual(sheetBox.x + sheetBox.width + 1);
    }

    // Following the verdict lands on the policy card.
    await denied.click();
    await expect(page).toHaveURL(
      /\/next\/settings\/capabilities#capability-policy$/,
    );
    await expect(page.getByTestId("card-capability-policy")).toBeInViewport();
  });
});

test("Template and schedule pickers show the verdict for agent tasks at large @phone @dark", async ({
  page,
  request,
}, testInfo) => {
  await withPolicy(request, async ({ template }) => {
    await page.goto(`processes/templates/${template}`);
    await page.getByTestId("tmpl-edit-btn").click();
    const dlg = page.getByRole("dialog", { name: "Edit template" });
    const picker = dlg.getByTestId("tmpl-grants");
    const denied = heldVerdict(picker, VIEWER);
    await expect(denied).toHaveText("denied by policy");
    await expect(denied).toHaveAttribute(
      "title",
      "denied by the capability policy for agent tasks",
    );
    await expectLinksToPolicy(denied);

    // No bound repository: cloud-key is admitted somewhere, so it may be
    // refused, and can still be chosen.
    await picker.getByTestId("tmpl-grants-add-open").click();
    await picker.getByTestId("tmpl-grants-mode-method").click();
    await picker.getByTestId("tmpl-grants-method-ref").click();
    const keyOpt = page.getByTestId(`tmpl-grants-method-ref-opt-${KEY}`);
    await expect(keyOpt).not.toHaveAttribute("aria-disabled", "true");
    await expect(keyOpt).toContainText("may be refused");
    await keyOpt.click();
    const note = picker.getByTestId("tmpl-grants-method-verdict");
    await expect(note).toContainText(
      "may be refused by the capability policy for agent tasks",
    );
    await expectLinksToPolicy(note.getByRole("link"));
    await shoot(page, `template-${testInfo.project.name}`);

    // Removing the denied grant and saving sends the edited list.
    const patch = page.waitForRequest(
      (r) =>
        r.method() === "PATCH" &&
        r.url().endsWith(`/api/templates/${template}`),
    );
    await picker
      .getByRole("button", { name: `Remove grant ${VIEWER}={}` })
      .click();
    await dlg.getByRole("button", { name: "Save template" }).click();
    expect((await patch).postDataJSON().capabilities).toEqual([]);
    await expect(dlg).toBeHidden();

    // A new prompt schedule on acme/widgets judges that repository.
    await page.goto("processes/schedules?new=schedule");
    const sdlg = page.getByRole("dialog");
    const spicker = sdlg.getByTestId("sched-grants");
    await spicker.getByTestId("sched-grants-add-open").click();
    await spicker.getByTestId("sched-grants-mode-method").click();
    await spicker.getByTestId("sched-grants-method-ref").click();
    const skey = page.getByTestId(`sched-grants-method-ref-opt-${KEY}`);
    await expect(skey).toHaveAttribute("aria-disabled", "true");
    await expect(skey).toContainText("acme/widgets");
  });
});

test("Repository and deployment default capabilities show the verdict @phone", async ({
  page,
  request,
}, testInfo) => {
  await withPolicy(request, async () => {
    await page.goto("repos/acme/widgets/agents");
    const repoPicker = page.getByTestId("repo-default-grants");
    const repoDenied = heldVerdict(repoPicker, VIEWER);
    await expect(repoDenied).toHaveText("denied by policy");
    await expect(repoDenied).toHaveAccessibleName(/on acme\/widgets/);
    await expectLinksToPolicy(repoDenied);
    await repoDenied.scrollIntoViewIfNeeded();
    await shoot(page, `repo-defaults-${testInfo.project.name}`);
    // Nothing leaves the viewport sideways on a phone.
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);

    await page.goto("settings/agents");
    const depPicker = page.getByTestId("deployment-default-grants");
    const depDenied = heldVerdict(depPicker, VIEWER);
    await expect(depDenied).toHaveText("denied by policy");
    await expectLinksToPolicy(depDenied);
    await depDenied.scrollIntoViewIfNeeded();
    await shoot(page, `deployment-defaults-${testInfo.project.name}`);
  });
});
