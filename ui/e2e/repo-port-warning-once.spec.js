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

// docs/test-runs/2026-11-11-repository-release-memory-ui.md: a
// repository's own `port-types` entry naming an unpaired branch was
// warned about twice on its Advanced tab -- once in the declarations
// status, which carries the repository's own port-types findings, and
// again under the Ports table, which drew every portWarnings entry. The
// Ports table now draws only what the status above it has not said.

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { test, expect } from "./fixtures.js";

test.use({
  demoArgs: [
    "-github-sim",
    "-fake-ci",
    "-scheduled-workflows",
    "-reconcile",
    "500ms",
    "-as",
    "port-warning-once",
  ],
});
test.describe.configure({ mode: "serial" });
test.setTimeout(120_000);

const SETTINGS = `port-branches: [main, release/1.0]
port-types:
  main->release/1.0: [fix, docs]
  main->nonexistent: [fix]
`;
const WARNING =
  "Warning: port-types in .grain/settings.yml: main->nonexistent names nonexistent, which is not a paired branch";

function git(dir, args) {
  return execFileSync("git", args, {
    cwd: dir,
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
    env: {
      ...process.env,
      GIT_AUTHOR_NAME: "walk",
      GIT_AUTHOR_EMAIL: "walk@example.com",
      GIT_COMMITTER_NAME: "walk",
      GIT_COMMITTER_EMAIL: "walk@example.com",
    },
  });
}

async function declareSettings(page) {
  const res = await page.request.get("/api/e2e/scheduled-workflows");
  expect(res.ok()).toBe(true);
  const { bare } = await res.json();
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "grain-walk-ports-"));
  try {
    git(dir, ["clone", "-q", "--branch", "main", `file://${bare}`, "repo"]);
    const repo = path.join(dir, "repo");
    const file = path.join(repo, ".grain", "settings.yml");
    fs.mkdirSync(path.dirname(file), { recursive: true });
    if (fs.existsSync(file) && fs.readFileSync(file, "utf8") === SETTINGS) {
      return;
    }
    fs.writeFileSync(file, SETTINGS);
    git(repo, ["add", "-A"]);
    git(repo, ["commit", "-qm", "declare port-types"]);
    git(repo, ["push", "-q", "origin", "main"]);
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

for (const [name, viewport] of [
  ["desktop", { width: 1280, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`a declared port-types warning is said once on the Advanced tab on a ${name} screen`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await declareSettings(page);

    // Switching declaration reading off and on again asks for a fresh
    // read on the next cycle rather than waiting out the poll interval.
    await page.goto("/repos/acme/widgets/settings/advanced");
    const box = page.getByRole("checkbox", {
      name: "Read acme/widgets's own declarations",
    });
    if (await box.isChecked()) {
      await box.click();
      await expect(box).not.toBeChecked();
    }
    await box.click();
    await expect(box).toBeChecked();

    const ports = page.locator(".repo-ports");
    await expect(async () => {
      await page.reload();
      await expect(ports).toBeVisible({ timeout: 2_000 });
      await expect(
        page.getByText(/^Read at [0-9a-f]{12}:/).first(),
      ).toBeVisible({ timeout: 2_000 });
    }).toPass({ timeout: 60_000 });

    await expect(
      ports.getByRole("row", { name: /main->nonexistent/ }),
    ).toContainText("nothing (not a paired branch)");
    const panel = page.locator("[role=tabpanel]:not([hidden])");
    await expect(panel.getByText(WARNING)).toHaveCount(1);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
  });
}
