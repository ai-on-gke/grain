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

// Regression scenario from the 2026-11-19 repository walkthrough
// (docs/test-runs/2026-11-19-repository-release-memory-ui.md): on a
// config-repo-managed deployment (-external-config-repo), /next/repos
// locks Add repo and the Releases tab's Work ownership card renders
// read-only with its explanation instead of letting clicks fail with 409.

import { test, expect } from "./fixtures.js";

test.use({
  demoArgs: [
    "-external-config-repo=https://github.com/acme/grain-config.git",
    "-github-sim",
    "-qualification",
  ],
});

test("locks Add repo on /next/repos and Work ownership on Releases with explanations @phone @dark", async ({
  page,
  axe,
}) => {
  await page.goto("/next/repos");
  await expect(page.locator("form.repos-add")).toHaveCount(0);
  const listNote = page.getByTestId("repos-config-managed");
  await expect(listNote).toBeVisible();
  await expect(listNote).toContainText("managed in the config repository");
  const vw = page.viewportSize()!.width;
  const listNoteBox = await listNote.boundingBox();
  expect(listNoteBox).not.toBeNull();
  expect(listNoteBox!.x).toBeGreaterThanOrEqual(0);
  expect(listNoteBox!.x + listNoteBox!.width).toBeLessThanOrEqual(vw + 0.5);
  expect(await axe()).toEqual([]);

  await page.goto("/next/repos/acme/widgets/releases");
  const woCard = page.getByTestId("card-repo-work-ownership");
  const woHint = page.getByTestId("work-ownership-config-managed");
  await expect(woHint).toBeVisible();
  await expect(woHint).toContainText("managed in the config repository");
  await expect(
    page.getByTestId("switch-work-ownership-default"),
  ).toBeDisabled();
  const pills = woCard.locator("button.repo-toggle-pill");
  const count = await pills.count();
  expect(count).toBeGreaterThan(0);
  for (let i = 0; i < count; i++) {
    await expect(pills.nth(i)).toBeDisabled();
  }
  expect(await axe()).toEqual([]);
});
