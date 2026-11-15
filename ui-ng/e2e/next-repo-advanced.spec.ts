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

// The /next/ repository page's Advanced tab over a real `grain demo`
// (2026-11-14 repository walkthrough, item 4,
// docs/test-runs/2026-11-14-repository-release-memory-ui.md): the
// .grain/ declarations switch, trigger files, extra push refs, Ports,
// and Remove repository with its refusals -- the config repo cannot be
// removed, and a removal that strands work names it before it is made.
//
// `-github-sim -qualification` declares templates, a suite, schedules
// and a push list for acme/widgets, and strands its tasks, schedules
// and qualification plan when it comes off the allowlist. Trigger files
// and paired branches come from `-sensitive-hold -mechanical-port`, in
// next-repo-advanced-ports.spec.ts. The demo has no config repo, so that refusal
// is shown by naming acme/widgets as one in /api/config, the way the
// role-gating test names a role.

import type { Page } from "@playwright/test";
import { test, expect } from "./fixtures.js";

const R = "/next/repos/acme/widgets/advanced";
const SCRATCH = "acme/e2e-advanced-scratch";

const declarationsOf = async (page: Page) => {
  const res = await page.request.get("/api/repos/acme/widgets/declarations");
  expect(res.ok()).toBe(true);
  return res.json();
};

const targetsOf = async (page: Page): Promise<string[]> => {
  const res = await page.request.get("/api/config");
  expect(res.ok()).toBe(true);
  return (await res.json()).targetRepos ?? [];
};

// patchConfig answers /api/config with the real body plus patch.
const patchConfig = (page: Page, patch: Record<string, unknown>) =>
  page.route(
    (url) => url.pathname === "/api/config",
    async (route) => {
      const response = await route.fetch();
      await route.fulfill({
        response,
        json: { ...(await response.json()), ...patch },
      });
    },
  );

// checkLayout checks the layout the tab rests on: nothing pushes the
// page wider than the viewport, every card sits inside it, and neither
// table scrolls sideways.
const checkLayout = async (page: Page) => {
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
  const width = page.viewportSize()!.width;
  for (const id of [
    "declarations",
    "triggers",
    "push-refs",
    "ports",
    "remove",
  ]) {
    const box = await page.getByTestId(`card-repo-${id}`).boundingBox();
    expect(box, id).not.toBeNull();
    expect(box!.x, id).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width, id).toBeLessThanOrEqual(width + 0.5);
  }
  // Paths and ref patterns break inside their cells, so even the four
  // port columns fit a phone without scrolling.
  for (const name of ["Extra push refs table", "Port directions table"]) {
    const wrap = page.getByRole("region", { name });
    if ((await wrap.count()) === 0) continue;
    const [scroll, client] = await wrap.evaluate((el) => [
      el.scrollWidth,
      el.clientWidth,
    ]);
    expect(scroll, name).toBeLessThanOrEqual(client);
  }
};

test.use({ demoArgs: ["-github-sim", "-qualification"] });

test.describe("over -github-sim -qualification", () => {
  test("the Advanced tab has its own address and shows the declarations, push refs and empty cards @phone @dark", async ({
    page,
    axe,
  }) => {
    const before = (await declarationsOf(page)).declarations;
    try {
      await page.goto("/next/repos/acme/widgets");
      await page.getByRole("button", { name: "Advanced" }).click();
      await expect(page).toHaveURL(/\/next\/repos\/acme\/widgets\/advanced$/);
      await expect(
        page.getByRole("button", { name: "Advanced" }),
      ).toHaveAttribute("aria-current", "page");

      const decl = page.getByTestId("card-repo-declarations");
      await expect(decl.getByTestId("declarations-status")).toHaveText(
        /Read at abc123commit: 1 template\(s\), 1 suite\(s\), 1 schedule\(s\), 0 trigger\(s\)/,
      );
      await expect(
        page.getByRole("table", { name: "Extra push refs" }).getByRole("row", {
          name: /\.grain\/templates\/nightly\.yml refs\/tags\/nightly-\* \(force\)/,
        }),
      ).toBeVisible();
      await expect(page.getByTestId("triggers-empty")).toHaveText(
        "acme/widgets declares no path triggers.",
      );
      await expect(page.getByTestId("ports-summary")).toContainText(
        "No branches are paired",
      );
      await checkLayout(page);
      expect(await axe()).toEqual([]);

      // The switch is saved as it moves, and reads back after a reload.
      const sw = page.getByRole("switch", {
        name: "Read acme/widgets's own declarations",
      });
      await expect(sw).toHaveAttribute("aria-checked", String(before));
      await sw.click();
      await expect
        .poll(async () => (await declarationsOf(page)).declarations)
        .toBe(!before);
      await page.reload();
      await expect(
        page.getByRole("switch", {
          name: "Read acme/widgets's own declarations",
        }),
      ).toHaveAttribute("aria-checked", String(!before));
    } finally {
      await page.request.put("/api/repos/acme/widgets/declarations", {
        data: { declarations: before },
      });
    }
  });

  test("Remove names the tasks, schedules and qualification plan it strands, and Cancel keeps the repo", async ({
    page,
  }) => {
    const before = await targetsOf(page);
    expect(before).toEqual(["acme/widgets"]);
    const dialog = page.getByRole("dialog", {
      name: "Remove acme/widgets from target repos?",
    });
    try {
      // The only allowed repo: removing it unrestricts the deployment,
      // so nothing is stranded and the confirm says what it does instead.
      await page.goto(R);
      await page.getByRole("button", { name: "Remove" }).click();
      await expect(dialog).toContainText(
        "An empty allowlist is what means unrestricted",
      );
      await expect(dialog).toContainText(
        "Tasks already filed there are not affected.",
      );
      await dialog.getByRole("button", { name: "Cancel" }).click();
      await expect(dialog).toBeHidden();

      // With another repo allowed, the removal strands acme/widgets's
      // work, as POST /api/repos/plan words it.
      const add = await page.request.post("/api/repos", {
        data: { repo: SCRATCH },
      });
      expect(add.ok()).toBe(true);
      await page.reload();
      await page.getByRole("button", { name: "Remove" }).click();
      await expect(dialog).toContainText(/\d+ tasks still in play there/);
      await expect(dialog).toContainText(/3 schedules firing into it/);
      await expect(dialog).toContainText("release-qualification plan");
      await expect(dialog).not.toContainText("unrestricted");
      await dialog.getByRole("button", { name: "Cancel" }).click();
      await expect(dialog).toBeHidden();
      await expect(page).toHaveURL(/\/advanced$/);
      expect(await targetsOf(page)).toEqual(["acme/widgets", SCRATCH]);
    } finally {
      const now = await targetsOf(page);
      if (now.includes(SCRATCH)) {
        await page.request.delete(`/api/repos/${SCRATCH}`);
      }
      if (!now.includes("acme/widgets")) {
        await page.request.post("/api/repos", {
          data: { repo: "acme/widgets" },
        });
      }
    }
  });

  test("Remove takes a repo off the target list and goes to /next/repos", async ({
    page,
  }) => {
    const add = await page.request.post("/api/repos", {
      data: { repo: SCRATCH },
    });
    expect(add.ok()).toBe(true);
    try {
      await page.goto(`/next/repos/${SCRATCH}/advanced`);
      await page.getByRole("button", { name: "Remove" }).click();
      const dialog = page.getByRole("dialog", {
        name: `Remove ${SCRATCH} from target repos?`,
      });
      await expect(dialog).toContainText(
        "Tasks already filed there are not affected.",
      );
      await dialog.getByRole("button", { name: "Remove" }).click();
      await expect(page).toHaveURL(/\/next\/repos$/);
      await expect.poll(() => targetsOf(page)).toEqual(["acme/widgets"]);
    } finally {
      if ((await targetsOf(page)).includes(SCRATCH)) {
        await page.request.delete(`/api/repos/${SCRATCH}`);
      }
    }
  });

  test("the config repo and a repo off the allowlist offer no Remove", async ({
    page,
  }) => {
    await patchConfig(page, { configRepo: "acme/widgets" });
    await page.goto(R);
    const card = page.getByTestId("card-repo-remove");
    await expect(card).toContainText(
      "acme/widgets is this deployment's own configuration repository and cannot be removed here.",
    );
    await expect(card.getByRole("button", { name: "Remove" })).toHaveCount(0);

    await page.unrouteAll();
    await page.goto("/next/repos/acme/gadgets/advanced");
    await expect(page.getByTestId("card-repo-remove")).toContainText(
      "acme/gadgets is not one of this deployment's target repos",
    );
    await expect(
      page.getByTestId("card-repo-remove").getByRole("button", {
        name: "Remove",
      }),
    ).toHaveCount(0);
  });

  test("a role without repos:write sees Remove and the switch disabled with the reason", async ({
    page,
  }) => {
    await patchConfig(page, {
      access: {
        role: "grain/viewer",
        roles: ["grain/viewer"],
        full: false,
        scopes: ["repos:read", "tasks:read"],
      },
    });
    await page.goto(R);
    const remove = page
      .getByTestId("card-repo-remove")
      .getByRole("button", { name: "Remove" });
    await expect(remove).toBeDisabled();
    await expect(remove).toHaveAccessibleDescription(
      /Your role, grain\/viewer, cannot remove repositories/,
    );
    const sw = page.getByRole("switch", {
      name: "Read acme/widgets's own declarations",
    });
    await expect(sw).toBeDisabled();
    await expect(sw).toHaveAccessibleDescription(
      /cannot switch declarations on or off/,
    );
  });
});
