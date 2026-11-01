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

import path from "node:path";
import { test, expect } from "./fixtures.js";

// Settings -> Models' "Where each agent CLI runs" card on a deployment
// with no host executor, against the real `grain demo` server resolving
// as deploy/kubernetes/grain.yaml's daemon would (-host-executor=
// kubernetes, pkg/demo's demoCLIRunConfig). None of grain's own APIs are
// mocked. design/design/cli-run-location.design.md, "Deployments with no host
// executor".
//
// Screenshots of the card are written, in light and dark, only when
// CLI_RUN_SCREENSHOTS names a directory to put them in.

test.use({ demoArgs: ["-host-executor=kubernetes"] });

const CLIS = ["claude", "codex", "antigravity", "custom"];

async function setMode(page, mode) {
  await page.addInitScript(
    ([key, value]) => window.localStorage.setItem(key, value),
    ["grain.themeMode", mode],
  );
}

test("Kubernetes offers no host, says why, and the API refuses it", async ({
  page,
}) => {
  await page.goto("/settings/models#cli-run");
  const card = page.getByTestId("cli-run-locations");
  await expect(card).toBeVisible();
  await expect(page.getByTestId("cli-run-no-host")).toContainText(
    "This deployment has no host executor (Kubernetes)",
  );

  for (const fw of CLIS) {
    await expect(page.getByTestId(`cli-run-${fw}-resolved`)).toHaveText(
      "container (the daemon's own)",
    );
    await page.getByTestId(`cli-run-${fw}`).getByLabel("Runs on").click();
    await expect(page.getByRole("option")).toHaveText(["Default", "Container"]);
    await page.keyboard.press("Escape");
    await expect(page.getByRole("option")).toHaveCount(0);
  }

  const refused = await page.evaluate(async () => {
    const r = await fetch("/api/settings/cli-run", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ settings: { claude: { location: "host" } } }),
    });
    return { status: r.status, body: await r.text() };
  });
  expect(refused.status).toBe(400);
  expect(refused.body).toContain("host is not available");

  // Container saves, and the row says where the next run goes.
  const claude = page.getByTestId("cli-run-claude");
  await claude.getByLabel("Runs on").click();
  await page.getByRole("option", { name: "Container", exact: true }).click();
  await card.getByRole("button", { name: "Save run locations" }).click();
  await expect(claude).toContainText(
    "container set in Settings -> Models: a subprocess in the daemon's own container",
  );

  // And back, so the demo's store is as the next test found it.
  await claude.getByLabel("Runs on").click();
  await page.getByRole("option", { name: "Default" }).click();
  await card.getByRole("button", { name: "Save run locations" }).click();
  await expect(claude).toContainText(
    "container the default: a subprocess in the daemon's own container",
  );
});

for (const mode of ["light", "dark"]) {
  test(`the no-host notice sits above the rows, inside the card (${mode})`, async ({
    page,
  }) => {
    await setMode(page, mode);
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto("/settings/models#cli-run");
    const card = page.getByTestId("cli-run-locations");
    await expect(card).toBeVisible();
    const notice = page.getByTestId("cli-run-no-host");
    await expect(notice).toBeVisible();

    const cardBox = await card.boundingBox();
    const noticeBox = await notice.boundingBox();
    const firstRow = await page.getByTestId("cli-run-claude").boundingBox();
    expect(noticeBox.x).toBeGreaterThanOrEqual(cardBox.x - 1);
    expect(noticeBox.x + noticeBox.width).toBeLessThanOrEqual(
      cardBox.x + cardBox.width + 1,
    );
    expect(noticeBox.y + noticeBox.height).toBeLessThanOrEqual(firstRow.y + 1);
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow).toBe(false);

    const dir = process.env.CLI_RUN_SCREENSHOTS;
    if (dir) {
      await card.scrollIntoViewIfNeeded();
      await card.screenshot({
        path: path.join(dir, `settings-models-cli-run-kubernetes-${mode}.png`),
      });
    }
  });
}
