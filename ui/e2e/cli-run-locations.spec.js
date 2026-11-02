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

// Settings -> Models' "Where each agent CLI runs" card against the real
// `grain demo` server, none of grain's own APIs mocked. The demo resolves
// as a development daemon on the bare machine (pkg/demo's
// demoCLIRunConfig): every CLI on the host, with claude's flag set to the
// container a release before v2.3.0 could have written, which runs on the
// host with a warning. design/design/cli-run-location.design.md.

test("a bare-machine daemon offers only host, and a run location saves and survives a reload", async ({
  page,
}) => {
  await page.goto("/settings/models#cli-run");
  const card = page.getByTestId("cli-run-locations");
  await expect(card).toBeVisible();
  await expect(card).toContainText("This daemon runs on the bare machine");

  await expect(page.getByTestId("cli-run-claude-resolved")).toHaveText(
    /^host \(/,
  );
  await expect(page.getByTestId("cli-run-claude-warning")).toContainText(
    "set to container, but a daemon on the bare machine has no container of its own",
  );
  await expect(page.getByTestId("cli-run-antigravity-resolved")).toHaveText(
    "host (/opt/agy/bin/agy)",
  );

  // Container is not offered here, and the API refuses it.
  const claude = page.getByTestId("cli-run-claude");
  await claude.getByLabel("Runs on").click();
  await expect(page.getByRole("option")).toHaveText(["Default", "Host"]);
  const refused = await page.evaluate(async () => {
    const r = await fetch("/api/settings/cli-run", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ settings: { claude: { location: "container" } } }),
    });
    return { status: r.status, body: await r.text() };
  });
  expect(refused.status).toBe(400);
  expect(refused.body).toContain("container is not available");

  // Set claude to the host in Settings: the warning goes with the next
  // resolution, with no restart.
  await page.getByRole("option", { name: "Host", exact: true }).click();
  await card.getByRole("button", { name: "Save run locations" }).click();
  await expect(claude).toContainText("host set in Settings -> Models");
  await expect(page.getByTestId("cli-run-claude-warning")).toHaveCount(0);

  await page.reload();
  await expect(page.getByTestId("cli-run-claude-resolved")).toHaveText(
    /^host \(/,
  );
  const stored = await page.evaluate(async () => {
    const r = await fetch("/api/settings/cli-run");
    return (await r.json()).settings;
  });
  expect(stored).toEqual({ claude: { location: "host" } });

  // And back, so the demo's store is as the next test found it.
  await page.getByTestId("cli-run-claude").getByLabel("Runs on").click();
  await page.getByRole("option", { name: "Default" }).click();
  await card.getByRole("button", { name: "Save run locations" }).click();
  await expect(page.getByTestId("cli-run-claude-warning")).toBeVisible();
});

test("the run-location rows line up and nothing overflows", async ({
  page,
}) => {
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await page.goto("/settings/models#cli-run");
    const card = page.getByTestId("cli-run-locations");
    await expect(card).toBeVisible();
    const cardBox = await card.boundingBox();
    const rows = ["claude", "codex", "antigravity", "custom"];
    let lastBottom = -1;
    for (const fw of rows) {
      const row = page.getByTestId(`cli-run-${fw}`);
      const box = await row.boundingBox();
      // Inside the card, and stacked: each row starts below the last.
      expect(box.x).toBeGreaterThanOrEqual(cardBox.x - 1);
      expect(box.x + box.width).toBeLessThanOrEqual(
        cardBox.x + cardBox.width + 1,
      );
      expect(box.y).toBeGreaterThanOrEqual(lastBottom - 1);
      lastBottom = box.y + box.height;
      const select = await row.getByLabel("Runs on").boundingBox();
      expect(select.x + select.width).toBeLessThanOrEqual(
        cardBox.x + cardBox.width + 1,
      );
    }
    if (width === 1440) {
      // At desktop width the selects sit in one column.
      const xs = await Promise.all(
        rows.map(async (fw) =>
          Math.round(
            (
              await page
                .getByTestId(`cli-run-${fw}`)
                .getByLabel("Runs on")
                .boundingBox()
            ).x,
          ),
        ),
      );
      expect(new Set(xs).size).toBe(1);
    }
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow).toBe(false);
  }
});
