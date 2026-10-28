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

import { expect, test } from "./fixtures.js";

// The repository page's "Merge when green and clean" switch
// (model.RepoConfig.MergeOnGreen, grain/task-545) in a real browser: it
// sits in the Merge queue tab with its sentence beside the tick, at
// desktop and phone widths, and saving it reaches the server and the
// Overview's summary row.
const repo = "acme/widgets";
const label = `Merge pull requests into ${repo} when green and clean`;

for (const [name, viewport] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`the merge-on-green tick sits beside its sentence (${name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto(`/repos/${repo}/settings/merge-queue`);
    const box = page.getByLabel(label);
    await expect(box).toBeVisible();
    await expect(box).not.toBeChecked();
    const text = page.getByText(/^Merge when green and clean\./);
    await expect(text).toBeVisible();

    const tick = await box.locator("..").boundingBox();
    const sentence = await text.boundingBox();
    // Beside it, on the right, starting on the tick's own line.
    expect(sentence.x).toBeGreaterThanOrEqual(tick.x + tick.width - 1);
    expect(Math.abs(sentence.y - tick.y)).toBeLessThan(tick.height);
    // And nothing runs off the side of the page.
    expect(sentence.x + sentence.width).toBeLessThanOrEqual(viewport.width);
  });
}

test("turning merge-on-green on saves it and shows it on the overview", async ({
  page,
}) => {
  try {
    await page.goto(`/repos/${repo}/settings/merge-queue`);
    // click(), not check(): the box is controlled and only ticks once
    // the PUT answers, and check() reads it back the moment the click
    // lands -- under a loaded CI host it saw the old state and threw.
    // toBeChecked() below is what waits for the save.
    await page.getByLabel(label).click();
    await expect(page.getByLabel(label)).toBeChecked();
    const saved = await (
      await page.request.get(`/api/repos/${repo}/merge-on-green`)
    ).json();
    expect(saved.mergeOnGreen).toBe(true);

    await page.goto(`/repos/${repo}`);
    await expect(
      page.getByText("Merge when green", { exact: true }),
    ).toBeVisible();
  } finally {
    await page.request.put(`/api/repos/${repo}/merge-on-green`, {
      data: { mergeOnGreen: false },
    });
  }
});
