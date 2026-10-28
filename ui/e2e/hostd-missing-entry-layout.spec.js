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

// Settings → Capability servers and System → Health saying that one of
// grain's own hostd allowlist entries is missing (HostdMissingNote.jsx).
// grain demo has no grain hostd, so what hostd offers is answered here
// and a run: host workspace row is added to the demo's own list; everything
// else is the demo's.

const WORKSPACE = "https://grain.dev/capabilities/workspace";
const ENTRY =
  "  grain-workspace:\n    command: /usr/local/bin/grain\n    args: [capability, workspace]\n    extra_args: true\n    env: [GRAIN_CAPABILITY_PROBE]\n    pass_env: [HTTP_PROXY, HTTPS_PROXY, NO_PROXY, http_proxy, https_proxy, no_proxy]\n";
const MISSING = {
  name: "grain-workspace",
  entry: ENTRY,
  command: "sudo systemctl start grain-hostd-allowlist.service",
};

async function stub(page) {
  await page.route("**/api/capability-servers/host-entries", (route) =>
    route.fulfill({
      json: {
        available: true,
        entries: ["grain-gcp", "grain-host-script", "host-git"],
        missing: [MISSING],
      },
    }),
  );
  await page.route(/\/api\/capability-servers$/, async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const listed = (await (await route.fetch()).json()).filter(
      (s) => s.id !== WORKSPACE,
    );
    listed.unshift({
      id: WORKSPACE,
      config: {
        command: "grain-workspace",
        run: "host",
        args: ["-listen", "127.0.0.1:4250"],
        builtin: "workspace",
      },
      secrets: [],
      builtin: {
        name: "workspace",
        id: WORKSPACE,
        summary: "Workspace directories",
      },
      status: {
        id: WORKSPACE,
        state: "needs-config",
        transport: "stdio",
        run: "host",
        builtin: "workspace",
        error:
          'capability server grain-workspace is set to run: host, but grain hostd\'s allowlist (/etc/grain/hostd.yaml) offers no capability entry "grain-workspace"',
      },
    });
    await route.fulfill({ json: listed });
  });
}

for (const [width, theme] of [
  [1280, "light"],
  [390, "light"],
  [1280, "dark"],
  [390, "dark"],
]) {
  test(`the missing grain-workspace entry is said in Settings at ${width}px (${theme}), inside its card`, async ({
    page,
  }) => {
    await page.emulateMedia({ colorScheme: theme });
    await page.setViewportSize({ width, height: 900 });
    await stub(page);
    await page.goto("/settings/capabilities#capability-servers");
    await page.locator(`[data-capability-server-row="${WORKSPACE}"]`).click();
    const card = page.locator(`[data-capability-server="${WORKSPACE}"]`);
    const note = card.getByTestId("hostd-entry-missing");
    await expect(note).toContainText("grain adds it automatically");
    await expect(note.getByTestId("hostd-entry-text")).toHaveText(ENTRY);
    await expect(
      card.getByTestId("capability-server-configure-unavailable"),
    ).toBeDisabled();
    // The note, and the entry text in it, stay inside the card: the long
    // pass_env line scrolls in its own box rather than widening the page.
    const c = await card.boundingBox();
    const n = await note.boundingBox();
    expect(n.x).toBeGreaterThanOrEqual(c.x - 1);
    expect(n.x + n.width).toBeLessThanOrEqual(c.x + c.width + 1);
    const scrollW = await page.evaluate(
      () => document.documentElement.scrollWidth,
    );
    expect(scrollW).toBeLessThanOrEqual(width + 1);
    await note.scrollIntoViewIfNeeded();
    await page.screenshot({
      path: `screenshots/after/hostd-missing-settings-${width}-${theme}.png`,
    });
  });
}

test("System → Health names grain's own missing hostd entries", async ({
  page,
}) => {
  await page.route("**/api/settings/cli-run", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const data = await (await route.fetch()).json();
    data.hostExecutor = {
      kind: "hostd",
      hostd: { reachable: true, missing: [MISSING] },
    };
    await route.fulfill({ json: data });
  });
  await page.goto("/system");
  const note = page.getByTestId("hostd-health-missing");
  await expect(note).toContainText("grain-workspace", { timeout: 30000 });
  await page.screenshot({ path: "screenshots/after/hostd-missing-health.png" });
});
