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

// /next/ repository pages on a deployment whose configuration an external
// config repository holds (GET /api/config's configRepoOnly). Walking
// docs/test-plans/2026-11-16.md §8 item 2 against a real daemon
// (docs/test-runs/2026-11-16-next-repository-pages.md), every save there
// answered 409 config_managed_externally, but the Work ownership card kept
// its switch and On/Off/Say nothing buttons live -- each click a refusal --
// and /next/repos kept offering Add repo. Both now say where to edit
// instead, as the qualification plan beside them already did.

import { test, expect } from "./fixtures.js";

test.use({ demoArgs: ["-github-sim", "-qualification"] });

const managed = async (page) => {
  await page.route("**/api/config", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({
      response: res,
      json: { ...body, configRepoOnly: true },
    });
  });
};

test("work ownership is read-only and says where to edit it @phone @dark", async ({
  page,
  axe,
}) => {
  await managed(page);
  await page.goto("/next/repos/acme/widgets/releases");
  const card = page.getByTestId("card-repo-work-ownership");
  const note = card.getByTestId("work-ownership-config-managed");
  await expect(note).toContainText(
    "acme/widgets's work ownership is managed in the config repository",
  );
  await expect(note).toContainText("tables/repo_config.json");
  const sw = card.getByRole("switch", {
    name: "Standing work on this deployment",
  });
  await expect(sw).toBeDisabled();
  await expect(sw).toHaveAttribute("aria-describedby", "work-ownership-denied");
  await expect(card.getByTestId("work-item-triage")).toBeVisible();
  const live = await card.locator("button:not(:disabled)").count();
  expect(live).toBe(0);
  await expect(
    page.getByTestId("qualification-plan-config-managed"),
  ).toBeVisible();

  // The note sits inside the card on a phone.
  const vw = page.viewportSize().width;
  const box = await note.boundingBox();
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(vw + 0.5);
  expect(await axe()).toEqual([]);
});

test("work ownership stays editable without a config repository", async ({
  page,
}) => {
  await page.goto("/next/repos/acme/widgets/releases");
  const card = page.getByTestId("card-repo-work-ownership");
  await expect(card.getByTestId("work-item-triage")).toBeVisible();
  await expect(card.getByTestId("work-ownership-config-managed")).toHaveCount(
    0,
  );
  await expect(
    card.getByRole("switch", { name: "Standing work on this deployment" }),
  ).toBeEnabled();
});

test("/next/repos offers no Add repo and says where repos are added @phone", async ({
  page,
}) => {
  await managed(page);
  await page.goto("/next/repos");
  await expect(page.getByTestId("repos-config-managed")).toContainText(
    "managed in the config repository (tables/grain_config.json, target_repos)",
  );
  await expect(page.getByRole("form", { name: "Add repo" })).toHaveCount(0);
  await expect(
    page.locator(".repos-name", { hasText: "acme/widgets" }),
  ).toBeVisible();
});
