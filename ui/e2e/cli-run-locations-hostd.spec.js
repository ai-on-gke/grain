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

// Settings -> Models' "Where each agent CLI runs" card in the one install
// shape: a daemon in the release container with grain hostd beside it,
// against the real `grain demo -host-executor=hostd` server -- whose hostd
// is a real one, on a throwaway allowlist offering agy and the
// corp-search capability (pkg/demo's startDemoHostd). None of grain's own
// APIs are mocked. design/design/cli-run-location.design.md, "Container mode".

test.use({ demoArgs: ["-host-executor=hostd"] });

test("behind hostd, host is offered only for what hostd's allowlist names, saying why", async ({
  page,
}) => {
  await page.goto("/settings/models#cli-run");
  const card = page.getByTestId("cli-run-locations");
  await expect(card).toBeVisible();

  const status = page.getByTestId("cli-run-hostd-status");
  const reachable = await page.evaluate(async () => {
    const r = await fetch("/api/settings/cli-run");
    return !!(await r.json()).hostExecutor?.hostd?.reachable;
  });
  if (!reachable) {
    // hostd will not run here (as root, say): the card says so.
    await expect(status).toContainText("not");
    return;
  }
  await expect(status).toContainText(
    "offering capability corp-search, cli agy",
  );

  // agy is on the allowlist and set to host: it runs through hostd, and
  // the row names the host binary hostd's allowlist entry runs.
  await expect(page.getByTestId("cli-run-antigravity-resolved")).toHaveText(
    "host (/opt/agy/bin/agy, through grain hostd)",
  );
  await expect(
    page.getByTestId("cli-run-antigravity-host-unavailable"),
  ).toHaveCount(0);

  // claude is not, so its row says why host would fail.
  await expect(page.getByTestId("cli-run-claude-resolved")).toHaveText(
    "container (the daemon's own)",
  );
  await expect(
    page.getByTestId("cli-run-claude-host-unavailable"),
  ).toContainText('offers no cli entry "claude"');

  // Set claude to host anyway: the next run's resolution is an error
  // naming the missing entry, rather than a run that fails mid-way.
  const claude = page.getByTestId("cli-run-claude");
  await claude.getByLabel("Runs on").click();
  await page.getByRole("option", { name: "Host", exact: true }).click();
  await card.getByRole("button", { name: "Save run locations" }).click();
  await expect(claude.getByRole("alert")).toContainText(
    'offers no cli entry "claude"',
  );

  // And back, so the demo's store is as the next test found it.
  await claude.getByLabel("Runs on").click();
  await page.getByRole("option", { name: "Default" }).click();
  await card.getByRole("button", { name: "Save run locations" }).click();
  await expect(page.getByTestId("cli-run-claude-resolved")).toHaveText(
    "container (the daemon's own)",
  );

  // Nothing in the card runs off a phone's width.
  await page.setViewportSize({ width: 390, height: 900 });
  // agy's long host path wraps beside its "host" chip, not onto a line
  // of its own below it: the chip sits level with the text's first line,
  // not centred on the wrapped block. Waited for first: the save above
  // reloads the card, which renders nothing for a moment.
  const chipEl = page.getByTestId("cli-run-antigravity-location");
  const lineEl = page.getByTestId("cli-run-antigravity-resolved");
  await expect(chipEl).toBeVisible();
  await expect(lineEl).toBeVisible();
  const chip = await chipEl.boundingBox();
  const line = await lineEl.boundingBox();
  expect(chip.x + chip.width).toBeLessThanOrEqual(line.x);
  expect(chip.y).toBeLessThan(line.y + line.height);
  expect(chip.y + chip.height).toBeGreaterThan(line.y);
  expect(Math.abs(chip.y - line.y)).toBeLessThanOrEqual(4);
  // Laid out, whether or not it is scrolled into view: the measure below
  // is the element's box, which needs no scrolling.
  await expect(
    page.getByTestId("cli-run-claude-host-unavailable"),
  ).toBeVisible();
  // Measured through locators, which wait for each element to be
  // attached: saving reloads the settings page's sections, and the card
  // renders nothing for the moment its own reload takes, so a one-shot
  // querySelector could land in that gap.
  const cardBox = await card.boundingBox();
  const overflow = [];
  for (const id of [
    "cli-run-hostd-status",
    "cli-run-claude-host-unavailable",
  ]) {
    const box = await page.getByTestId(id).boundingBox();
    if (box.x + box.width > cardBox.x + cardBox.width + 1) overflow.push(id);
  }
  expect(overflow).toEqual([]);
});
