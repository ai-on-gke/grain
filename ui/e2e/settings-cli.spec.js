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

// Settings → CLI (design/spec/external-cli/external-cli.spec.md#XC-32):
// the install line for this deployment's own /cli/install.sh, which the
// daemon serves; a login line naming this origin, which says when the
// external agent API has to be turned on first and links to the switch in
// Settings → Agents; and one `grain skill install` line per agent -- every
// command inside its card at desktop and phone width.
const VIEWPORTS = [
  { label: "desktop", width: 1440, height: 900 },
  { label: "phone", width: 390, height: 844 },
];

for (const viewport of VIEWPORTS) {
  test(`TestSettingsCLIPageRendersInstallLoginAndSkillCommands (${viewport.label})`, async ({
    page,
    request,
    demoServer,
  }) => {
    await page.setViewportSize(viewport);
    const before = await (await request.get("/api/settings")).json();
    try {
      const off = await request.put("/api/settings", {
        data: { externalAgentApi: false },
      });
      expect(off.ok()).toBe(true);

      await page.goto("/settings/cli");
      const install = page.getByTestId("cli-install-command");
      await expect(install).toHaveText(
        `curl -fsSL ${demoServer}/cli/install.sh | sh`,
      );
      await expect(page.getByTestId("cli-login-command")).toHaveText(
        `grain login ${demoServer} --name 127-0-0-1 --issue-token --scope-set grain/external-dev --expires 30d`,
      );
      for (const agent of ["jetski", "claude", "codex", "gemini"]) {
        await expect(page.getByTestId(`cli-skill-command-${agent}`)).toHaveText(
          `grain skill install --${agent}`,
        );
      }

      // Every command sits inside its card and the viewport.
      for (const id of [
        "cli-install-command",
        "cli-login-command",
        "cli-skill-command-gemini",
      ]) {
        const box = await page.getByTestId(id).boundingBox();
        const card = await page
          .getByTestId(id)
          .locator("xpath=ancestor::section[1]")
          .boundingBox();
        expect(box.x).toBeGreaterThanOrEqual(card.x);
        expect(box.x + box.width).toBeLessThanOrEqual(card.x + card.width);
        expect(card.x + card.width).toBeLessThanOrEqual(viewport.width);
      }

      // Nothing pushes the page sideways.
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth,
      );
      expect(overflow).toBeLessThanOrEqual(viewport.width);

      // The install line's URL is a script the daemon serves.
      const script = await request.get("/cli/install.sh");
      expect(script.ok()).toBe(true);
      expect(script.headers()["content-type"]).toBe(
        "text/x-shellscript; charset=utf-8",
      );
      expect(await script.text()).toContain("checksums.txt");

      // With the external agent API off, the page says so and links to
      // the switch that turns it on.
      const offNote = page.getByTestId("cli-external-agent-api-off");
      await expect(offNote).toBeVisible();
      await page.getByTestId("cli-external-agent-api-link").click();
      await expect(page).toHaveURL(/\/settings\/agents/);
      const toggle = page.locator('input[name="externalAgentApi"]');
      await expect(toggle).not.toBeChecked();
      await toggle.check();
      await expect(page.getByTestId("settings-status")).toHaveText(
        "All changes saved",
      );

      await page.goto("/settings/cli");
      await expect(page.getByTestId("cli-login-command")).toBeVisible();
      await expect(offNote).toHaveCount(0);
    } finally {
      await request.put("/api/settings", {
        data: { externalAgentApi: Boolean(before.externalAgentApi) },
      });
    }
  });
}
