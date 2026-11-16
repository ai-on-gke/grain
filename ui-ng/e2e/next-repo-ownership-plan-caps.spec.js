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

// The four things the /next/ repository page lacked next to the classic
// Repo page (parity gaps of the 2026-11-14 repository walkthrough,
// docs/test-runs/2026-11-14-repository-release-memory-ui.md): the
// work-ownership card and the qualification-plan editor on Releases, the
// CI capabilities card on CI & Workflows, and the issue-import interval
// on Overview -- each over a real `grain demo` with its GitHub
// simulation, each read back through the API, and each putting the
// demo's state back in `finally` (the demo is shared by the worker).

import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-github-sim", "-qualification"] });

const R = "/next/repos/acme/widgets";
const API = "/api/repos/acme/widgets";
const NIGHTLY = "repo:schedule:acme:widgets:nightly";

const read = async (page, path) => {
  const res = await page.request.get(`${API}/${path}`);
  expect(res.ok()).toBe(true);
  return res.json();
};

const put = async (page, path, data) => {
  const res = await page.request.put(`${API}/${path}`, { data });
  expect(res.ok(), await res.text()).toBe(true);
};

test("work ownership says what was said and what it resolves to, and saves one item", async ({
  page,
}) => {
  try {
    await page.goto(`${R}/releases`);
    const card = page.getByTestId("card-repo-work-ownership");
    const row = card.getByTestId(`work-item-${NIGHTLY}`);
    await expect(row).toContainText("Nightly Repo Maintenance Sweep");
    await expect(row).toContainText("Nobody has said");
    await expect(row).toContainText("declared in .grain/schedules/nightly.yml");

    await row
      .getByRole("button", { name: "Nightly Repo Maintenance Sweep: off" })
      .click();
    await expect(row).toContainText("Off, set here");
    await expect(row).toContainText("Not run");
    await expect(
      row.getByRole("button", { name: "Nightly Repo Maintenance Sweep: off" }),
    ).toHaveAttribute("aria-pressed", "true");
    const stored = (await read(page, "work-ownership")).items.find(
      (it) => it.item === NIGHTLY,
    );
    expect(stored).toMatchObject({ override: "off", runs: false });

    // The repository default is its own switch; an item that says off
    // stays off when the default is on, and one that says nothing follows.
    const sw = card.getByRole("switch", {
      name: "Standing work on this deployment",
    });
    await expect(sw).toHaveAttribute("aria-checked", "true");
    await sw.click();
    await expect(sw).toHaveAttribute("aria-checked", "false");
    await expect(card.getByTestId("work-item-triage")).toContainText("Not run");
    expect((await read(page, "work-ownership")).default).toBe("off");
  } finally {
    await put(page, "work-ownership", { default: "" });
    await put(page, "work-ownership", { item: NIGHTLY, state: "" });
  }
});

test("the qualification plan edits in place, refuses a bad repeat beside it, guards and saves", async ({
  page,
}) => {
  const before = await read(page, "qualification-plan");
  try {
    await page.goto(`${R}/releases`);
    const card = page.getByTestId("card-repo-qualification-plan");
    await expect(
      card.getByTestId("qualification-item-template-0"),
    ).toContainText("Qualify");
    const save = card.getByTestId("save-qualification-plan");
    await expect(save).toBeDisabled();

    await card.getByRole("button", { name: "Add item" }).click();
    await card.getByRole("button", { name: "Item 2 template" }).click();
    // Built-in templates are not offered.
    await expect(
      page.getByRole("option", { name: /Set up a GKE sandbox cluster/ }),
    ).toHaveCount(0);
    await page.getByRole("option", { name: /security-audit/ }).click();
    await card
      .getByRole("group", { name: "Item 2 depends on" })
      .getByRole("button", { name: "Qualify" })
      .click();

    const repeat = card.getByTestId("qualification-item-repeat-1");
    await repeat.fill("0");
    await expect(repeat).toHaveAttribute("aria-invalid", "true");
    await expect(card).toContainText("Repeat must be a whole number");
    await expect(save).toBeDisabled();
    await repeat.fill("2");
    await expect(repeat).not.toHaveAttribute("aria-invalid", "true");

    // Leaving with the plan unsaved asks first; Cancel keeps the edit.
    await page.getByTestId("repo-tab-branches").click();
    const dialog = page.getByTestId("confirm-dialog");
    await expect(dialog).toContainText("qualification plan");
    await page.getByTestId("confirm-cancel").click();
    await expect(page).toHaveURL(/\/releases$/);
    await expect(repeat).toHaveValue("2");

    await save.click();
    await expect(save).toBeDisabled();
    await expect
      .poll(async () => (await read(page, "qualification-plan")).items)
      .toEqual([
        expect.objectContaining({ templateId: "template-qualify", repeat: 1 }),
        expect.objectContaining({
          templateId: "template-2",
          repeat: 2,
          dependsOn: ["template-qualify"],
        }),
      ]);

    // Removing Qualify drops the dependency on it from the other item,
    // rather than leaving one the server would refuse.
    await card.getByRole("button", { name: /^Remove item 1/ }).click();
    await save.click();
    await expect
      .poll(async () => (await read(page, "qualification-plan")).items)
      .toEqual([
        expect.objectContaining({ templateId: "template-2", repeat: 2 }),
      ]);
    const only = (await read(page, "qualification-plan")).items[0];
    expect(only.dependsOn ?? []).toEqual([]);

    await page.reload();
    await expect(
      page
        .getByTestId("card-repo-qualification-plan")
        .getByTestId("qualification-item-template-0"),
    ).toContainText("security-audit");
  } finally {
    await put(page, "qualification-plan", {
      requireApproval: before.requireApproval,
      autoPromote: before.autoPromote,
      items: before.items.map((it) => ({
        templateId: it.templateId,
        repeat: it.repeat,
        dependsOn: it.dependsOn ?? [],
      })),
    });
  }
});

test("CI capabilities toggle and persist", async ({ page }) => {
  const before = (await read(page, "ci-capabilities")).ciCapabilities ?? [];
  try {
    await page.goto(`${R}/ci`);
    const card = page.getByTestId("card-repo-ci-capabilities");
    const push = card.getByRole("button", { name: /Registry push/ });
    await expect(push).toHaveAttribute("aria-pressed", "false");
    await push.click();
    await expect(push).toHaveAttribute("aria-pressed", "true");
    await expect
      .poll(async () => (await read(page, "ci-capabilities")).ciCapabilities)
      .toEqual(["https://grain.dev/capabilities/release#registry-push={}"]);

    await page.reload();
    await expect(
      page
        .getByTestId("card-repo-ci-capabilities")
        .getByRole("button", { name: /Registry push/ }),
    ).toHaveAttribute("aria-pressed", "true");
  } finally {
    await put(page, "ci-capabilities", { ciCapabilities: before });
  }
});

test("the issue-import interval refuses under a minute beside its box, guards, and saves", async ({
  page,
}) => {
  const before = (await read(page, "settings")).issueImport;
  try {
    await page.goto(R);
    const box = page.getByLabel("Every (minutes)");
    await expect(box).toHaveValue("");
    await expect(box).toHaveAttribute(
      "placeholder",
      String(Math.round(before.effectiveIntervalSeconds / 60)),
    );

    await box.fill("0");
    await page.getByTestId("save-issue-import-btn").click();
    await expect(box).toHaveAttribute("aria-invalid", "true");
    await expect(page.getByTestId("import-interval-error")).toContainText(
      "at least 1",
    );
    await expect(page.getByTestId("repo-error-banner")).toHaveCount(0);
    expect((await read(page, "settings")).issueImport.intervalSeconds).toBe(
      before.intervalSeconds,
    );

    await box.fill("15");
    await expect(box).not.toHaveAttribute("aria-invalid", "true");
    await page.getByTestId("repo-tab-ci").click();
    await expect(page.getByTestId("confirm-dialog")).toContainText(
      "issue import",
    );
    await page.getByTestId("confirm-cancel").click();

    await page.getByTestId("save-issue-import-btn").click();
    await expect
      .poll(
        async () => (await read(page, "settings")).issueImport.intervalSeconds,
      )
      .toBe(900);
    await page.reload();
    await expect(page.getByLabel("Every (minutes)")).toHaveValue("15");
  } finally {
    await put(page, "settings", {
      issueImportIntervalSeconds: before.intervalSeconds,
    });
  }
});

// Tagged for the phone project (390 wide, touch) and the dark one, where
// axe also checks the new pills' contrast; the desktop run narrows itself.
test("the new cards fit a phone and pass axe @phone @dark", async ({
  page,
  axe,
}, testInfo) => {
  if (testInfo.project.name === "desktop") {
    await page.setViewportSize({ width: 390, height: 844 });
  }
  for (const [tab, ids] of [
    ["releases", ["card-repo-work-ownership", "card-repo-qualification-plan"]],
    ["ci", ["card-repo-ci-capabilities"]],
    ["", ["card-repo-operations"]],
  ]) {
    await page.goto(tab ? `${R}/${tab}` : R);
    for (const id of ids) {
      const card = page.getByTestId(id);
      await expect(card).toBeVisible();
      // Nothing in the card is wider than it, and every control sits
      // inside its box.
      const fit = await card.evaluate((el) => {
        const box = el.getBoundingClientRect();
        const outside = [...el.querySelectorAll("button, input")]
          .map((c) => c.getBoundingClientRect())
          .filter((r) => r.width > 0)
          .filter((r) => r.left < box.left - 1 || r.right > box.right + 1);
        return {
          overflow: el.scrollWidth - el.clientWidth,
          outside: outside.length,
        };
      });
      expect(fit, id).toEqual({ overflow: 0, outside: 0 });
    }
    expect(await axe()).toEqual([]);
  }
});
