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

const MISMATCH_MSG =
  "this entry resolves to github.com; did you mean git.forge.example/experimental/grain?";

async function routeHostMismatch(page) {
  await page.route("**/api/config", async (route) => {
    const res = await route.fetch();
    const json = await res.json();
    json.targetRepos = [
      ...(json.targetRepos || ["acme/widgets"]),
      "experimental/grain",
    ];
    await route.fulfill({ response: res, json });
  });
  await page.route("**/api/deployment/reachable-repos*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        available: true,
        repos: [
          { repo: "acme/widgets", push: true },
          { repo: "git.forge.example/experimental/grain", push: true },
        ],
        errors: [],
        connections: [
          {
            credential: "github-token",
            host: "github.com",
            ok: true,
            repoCount: 1,
          },
          {
            credential: "forge-token",
            host: "git.forge.example",
            ok: true,
            repoCount: 1,
          },
        ],
        hostMismatches: [
          {
            repo: "experimental/grain",
            resolvesTo: "github.com",
            candidates: ["git.forge.example/experimental/grain"],
            message: MISMATCH_MSG,
          },
        ],
        total: 2,
        truncated: false,
      }),
    });
  });
  await page.route("**/api/sandboxes*", async (route) => {
    const res = await route.fetch();
    const json = await res.json();
    json.targetRepoHostWarnings = [
      {
        repo: "experimental/grain",
        resolvesTo: "github.com",
        candidates: ["git.forge.example/experimental/grain"],
        message: MISMATCH_MSG,
      },
    ];
    await route.fulfill({ response: res, json });
  });
}

async function assertFitsViewport(page, alert) {
  await expect(alert).toBeVisible();
  await expect(alert).toContainText(`experimental/grain: ${MISMATCH_MSG}`);
  const box = await alert.boundingBox();
  expect(box).not.toBeNull();
  const vp = page.viewportSize();
  expect(box.width).toBeGreaterThan(100);
  expect(box.height).toBeGreaterThan(24);
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(vp.width + 1);
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(1);
}

for (const [label, viewport] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`surfaces hostless repo mismatch warning cleanly at ${label} width across Repos, Settings → Repositories, and System → Health`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await routeHostMismatch(page);

    for (const url of [
      "/repos",
      "/settings/repositories",
      "/system/health",
      "/system/sandboxes",
    ]) {
      await page.goto(url);
      const alert = page.getByTestId("repo-host-mismatch-alert").first();
      await assertFitsViewport(page, alert);
    }
  });
}
