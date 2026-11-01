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

// Settings → Capabilities → API scope sets (docs/agent-api.md, "Scope
// sets") against grain demo, which declares docs' own ops/triage example
// (pkg/demo). The card lists grain's built-ins and the config-repo set
// with what it resolves to, and its rows keep their chips inside the
// card at desktop and phone widths.

for (const [label, viewport] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`lists built-in and config-repo scope sets inside the card (${label})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/settings/capabilities#api-scope-sets");
    const triage = page.getByTestId("api-scope-set-ops/triage");
    await expect(triage).toBeVisible();
    await expect(
      triage.getByText("config repo · tables/api_scope_set.json"),
    ).toBeVisible();
    await expect(
      triage.getByText("tasks:write @ bwsalmon/grain"),
    ).toBeVisible();
    await expect(triage.getByText("tasks:admin", { exact: false })).toHaveCount(
      1, // only in "denies tasks:admin", never as a resolved chip
    );
    await expect(triage.getByText("requires a person")).toHaveCount(0);
    const builtin = page.getByTestId("api-scope-set-grain/deployment-ops");
    await expect(builtin.getByText("built-in")).toBeVisible();
    await expect(builtin.getByText("requires a person")).toBeVisible();

    // Every chip of the row sits inside the row: nothing overflows the
    // card at this width.
    const row = await triage.boundingBox();
    for (const chip of await triage.locator(".MuiChip-root").all()) {
      const box = await chip.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(row.x - 0.5);
      expect(box.x + box.width).toBeLessThanOrEqual(row.x + row.width + 0.5);
    }
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(0);
  });
}

test("surfaces retired in_process and api_scopes.presets catalogue entries in Settings -> Capabilities", async ({
  page,
  request,
}) => {
  const before = await (await request.get("/api/capability-policy")).json();
  try {
    const put = await request.put("/api/capability-policy", {
      data: {
        policy: {
          catalogue: {
            inProcess: {
              memory: { enabled: false },
            },
            apiScopes: {
              presets: {
                "release-notes": {
                  restrictions: {
                    repos: ["acme/*"],
                    requiresPerson: true,
                  },
                },
              },
            },
          },
        },
      },
    });
    expect(put.ok()).toBe(true);

    const refused = await request.post("/api/tasks", {
      data: {
        title: "Check retired in_process refusal",
        noRepo: true,
        capabilities: ["api:memory"],
      },
    });
    expect(refused.status()).toBe(400);
    expect(await refused.text()).toContain("in_process.memory");

    for (const viewport of [
      { width: 1440, height: 900 },
      { width: 390, height: 844 },
    ]) {
      await page.setViewportSize(viewport);
      await page.goto("/settings/capabilities");

      const policyAlert = page.getByTestId("policy-catalogue-entries");
      await expect(policyAlert).toBeVisible();
      await expect(policyAlert).toContainText(
        "api:grain/memory — disabled (retired in_process.memory → move to api_scopes.presets.memory)",
      );
      await expect(policyAlert).toContainText(
        "api:grain/release-notes — repos acme/* · requires a person (api_scopes.presets.release-notes)",
      );

      const memory = page.getByTestId("api-scope-set-grain/memory");
      await expect(memory).toBeVisible();
      await expect(memory.getByText("disabled", { exact: true })).toBeVisible();
      await expect(
        memory.getByText("in_process.memory", { exact: true }),
      ).toBeVisible();
      await expect(
        memory.getByTestId("api-scope-set-catalogue-grain/memory"),
      ).toHaveText(
        "settings/capabilities.yml · retired in_process.memory (move to api_scopes.presets.memory): disabled",
      );

      const rn = page.getByTestId("api-scope-set-grain/release-notes");
      await expect(rn).toBeVisible();
      await expect(
        rn.getByText("requires a person", { exact: true }),
      ).toBeVisible();
      await expect(rn.getByText("restricted", { exact: true })).toBeVisible();
      await expect(
        rn.getByTestId("api-scope-set-catalogue-grain/release-notes"),
      ).toHaveText(
        "settings/capabilities.yml · api_scopes.presets.release-notes: repos acme/* · requires a person",
      );

      for (const rowEl of [memory, rn]) {
        const row = await rowEl.boundingBox();
        for (const chip of await rowEl.locator(".MuiChip-root").all()) {
          const box = await chip.boundingBox();
          expect(box.x).toBeGreaterThanOrEqual(row.x - 0.5);
          expect(box.x + box.width).toBeLessThanOrEqual(
            row.x + row.width + 0.5,
          );
        }
      }
      const overflow = await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
      expect(overflow).toBeLessThanOrEqual(0);
    }
  } finally {
    await request.put("/api/capability-policy", {
      data: {
        policy: {
          ...(before.policy || {}),
          catalogue: before.policy?.catalogue || {},
        },
      },
    });
  }
});
