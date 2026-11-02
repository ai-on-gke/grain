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

import { test, expect } from "./fixtures.js";

// The rail's CI page opens a run from the repo name on its row. Every row
// of one repo shows the same "acme/widgets", so before the 2026-11-02
// deployment-operations walkthrough a keyboard or screen-reader user met
// one identically named button per run. Each now says which run it opens.
// Nothing is stubbed: the -observability demo's real pkg/ci service runs
// both, and /api/e2e/ci/finish releases the step its workflow holds.

test.use({ demoArgs: ["-observability"] });

test("each CI run row's open control names its own run", async ({ page }) => {
  const refs = ["walk/names-a", "walk/names-b"];
  const ids = [];
  for (const ref of refs) {
    const res = await page.request.post("/api/ci/runs", {
      data: { repo: "acme/widgets", ref },
    });
    expect(res.ok()).toBe(true);
    ids.push((await res.json()).id);
  }
  try {
    await page.goto("/ci");
    const open = (ref) =>
      page.getByRole("button", {
        name: new RegExp(`^acme/widgets ${ref}, \\w+, started `),
      });
    for (const ref of refs) await expect(open(ref)).toHaveCount(1);
    // The visible text is unchanged: the repo, which leads the name.
    await expect(open(refs[1])).toHaveText("acme/widgets");

    await open(refs[1]).focus();
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(new RegExp(`/ci/${ids[1]}$`));
  } finally {
    // Release the held steps until both runs have a verdict, so the
    // worker's demo is idle again. The second run's sandbox comes up only
    // once the first has gone, so a release is refused (409) in between.
    await expect
      .poll(
        async () => {
          await page.request.post("/api/e2e/ci/finish");
          const done = await Promise.all(
            ids.map(async (id) => {
              const res = await page.request.get(`/api/ci/runs/${id}`);
              return Boolean((await res.json()).conclusion);
            }),
          );
          return done.every(Boolean);
        },
        { timeout: 60_000 },
      )
      .toBe(true);
  }
});
