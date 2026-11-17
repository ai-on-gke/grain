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

// Settings → Access's Access code card under /next/: the mode the demo
// daemon really reports, the recommendation to switch to recovery only
// when recovery would take effect (a verified viewer, and somebody with a
// full role who can sign in through a verifier), the host command beside
// its copy button at desktop and phone widths; and Settings → Audit
// listing what the access code changed as an access event with its
// Access code badge.

const DESKTOP = { width: 1440, height: 900 };
const PHONE = { width: 390, height: 844 };

const within = (inner, outer) => {
  expect(inner.x).toBeGreaterThanOrEqual(outer.x - 1);
  expect(inner.x + inner.width).toBeLessThanOrEqual(outer.x + outer.width + 1);
  expect(inner.y).toBeGreaterThanOrEqual(outer.y - 1);
  expect(inner.y + inner.height).toBeLessThanOrEqual(
    outer.y + outer.height + 1,
  );
};

const COMMAND =
  "sudo -u grain grain access-code -data-dir /var/lib/grain/data mode recovery";

const modeView = (over) => ({
  mode: "on",
  viewerVerified: true,
  verifiedAdminCanSignIn: true,
  recoveryInForce: false,
  account: "grain",
  command: COMMAND,
  onCommand: COMMAND.replace(/recovery$/, "on"),
  ...over,
});

test("the Access code card shows the demo daemon's own mode", async ({
  page,
}) => {
  await page.goto("/next/settings/access");
  const card = page.getByTestId("card-access-code-mode");
  await expect(card).toBeVisible();
  await expect(page.getByTestId("access-code-mode")).toHaveText("On");
  // Signed in with the code itself, so no recommendation yet -- but the
  // command that changes the mode is always there.
  await expect(page.getByTestId("access-code-recommend-recovery")).toHaveCount(
    0,
  );
  await expect(page.getByTestId("access-code-recovery-later")).toBeVisible();
  await expect(page.getByTestId("access-code-mode-command")).toContainText(
    /^sudo -u \S+ .*grain access-code.* mode recovery$/,
  );
});

test("a verified viewer is not told to switch when nobody with a full role can sign in", async ({
  page,
}) => {
  await page.route("**/api/access/access-code", (route) =>
    route.fulfill({ json: modeView({ verifiedAdminCanSignIn: false }) }),
  );
  await page.goto("/next/settings/access");
  await expect(page.getByTestId("access-code-mode")).toHaveText("On");
  await expect(page.getByTestId("access-code-recommend-recovery")).toHaveCount(
    0,
  );
  await expect(page.getByTestId("access-code-recovery-later")).toBeVisible();
});

test("recovery mode says whether it is in force and offers the way back", async ({
  page,
}) => {
  await page.route("**/api/access/access-code", (route) =>
    route.fulfill({
      json: modeView({ mode: "recovery", recoveryInForce: true }),
    }),
  );
  await page.goto("/next/settings/access");
  await expect(page.getByTestId("access-code-mode")).toHaveText("Recovery");
  await expect(page.getByTestId("access-code-recovery-state")).toHaveText(
    "in force",
  );
  await expect(page.getByTestId("access-code-mode-command")).toHaveText(
    COMMAND.replace(/recovery$/, "on"),
  );
});

for (const [name, viewport] of [
  ["desktop", DESKTOP],
  ["phone", PHONE],
]) {
  test(`the Access code card recommends recovery and fits a ${name} screen`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.route("**/api/access/access-code", (route) =>
      route.fulfill({ json: modeView() }),
    );
    await page.goto("/next/settings/access");

    const card = page.getByTestId("card-access-code-mode");
    await expect(card).toBeVisible();
    const recommend = page.getByTestId("access-code-recommend-recovery");
    await expect(recommend).toContainText("recovery");
    const cmd = page.getByTestId("access-code-mode-command");
    await expect(cmd).toHaveText(COMMAND);

    const cardBox = await card.boundingBox();
    const cmdBox = await cmd.boundingBox();
    const copyBox = await page
      .getByTestId("copy-access-code-command")
      .boundingBox();
    within(await recommend.boundingBox(), cardBox);
    within(cmdBox, cardBox);
    within(copyBox, cardBox);
    // The copy button sits to the right of the command, level with its
    // top, at both widths.
    expect(copyBox.x).toBeGreaterThanOrEqual(cmdBox.x + cmdBox.width - 1);
    expect(Math.abs(copyBox.y - cmdBox.y)).toBeLessThanOrEqual(8);
    const pillBox = await page.getByTestId("access-code-mode").boundingBox();
    within(pillBox, cardBox);
    expect(pillBox.y).toBeLessThan((await recommend.boundingBox()).y);

    // Between the allowlist and the external agent tokens.
    const allowBox = await page
      .getByTestId("card-rbac-principals")
      .boundingBox();
    const tokensBox = await page
      .getByTestId("card-external-agent-tokens")
      .boundingBox();
    expect(cardBox.y).toBeGreaterThan(allowBox.y);
    expect(cardBox.y).toBeLessThan(tokensBox.y);

    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth + 1,
    );
    expect(overflow, "the page scrolls sideways").toBe(false);
  });
}

test("Settings → Audit lists what the access code changed as access", async ({
  page,
}) => {
  const row = {
    id: 987654,
    at: new Date().toISOString(),
    actor: "access-code:alice@example.com",
    actorKind: "person",
    surface: "access-code",
    summary: "PATCH /api/access/principals/user:bob@example.com",
  };
  // config-audit answers first: the row it shares with access history
  // must still be an access event, not a config change.
  await page.route("**/api/deployment/config-audit*", (route) =>
    route.fulfill({ json: [row] }),
  );
  await page.route("**/api/access/history*", async (route) => {
    await new Promise((r) => setTimeout(r, 300));
    await route.fulfill({ json: [row] });
  });
  await page.goto("/next/settings/audit");
  const audit = page.getByTestId("card-audit-viewer");
  const ours = audit
    .getByTestId("audit-row")
    .filter({ hasText: "user:bob@example.com" });
  await expect(ours).toHaveCount(1);
  await expect(ours.locator(".status-pill.neutral")).toHaveText("access");
  await expect(ours.getByTestId("audit-access-code-use")).toHaveText(
    "Access code",
  );
});
