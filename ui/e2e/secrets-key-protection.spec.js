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

// design/design/secrets-key-protection.design.md, Phase 2: Settings ->
// State says how the secrets key is held. `grain demo`'s key is a
// plaintext file and it manages no state repository at all, so the
// pane's whole /api/state-repo answer is set here. A CryptoKey name is one long word, and on a phone it must wrap
// inside the pane rather than widen the page.
const KMS_KEY =
  "projects/example-project/locations/us-central1/keyRings/grain-secrets/cryptoKeys/secrets-key";

const STATUS = {
  available: true,
  mode: "internal",
  repository: "grain/config",
  branch: "main",
  dir: "/var/lib/grain/data/state-repo",
  head: "abcdef1234567890",
  schemaVersion: 16,
  buildSchemaVersion: 16,
  secretsPublicKey:
    "grain-secret-pub-v1:3q2+7w3q2+7w3q2+7w3q2+7w3q2+7w3q2+7w3q2+7w=",
  secretsKeyFile: "/var/lib/grain/data/secrets/secrets.key",
  secretsKeyProtection: "kms",
  secretsKmsKey: KMS_KEY,
  secretsKeyLastUnwrap: "2026-11-10T09:00:00Z",
};

for (const [name, viewport] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`the State pane names the KMS key that wraps the secrets key on a ${name} screen`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.route("**/api/state-repo", async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      await route.fulfill({ json: STATUS });
    });
    await page.goto("/settings/config-repo");

    const line = page.getByTestId("secrets-key-protection");
    await expect(line).toBeVisible();
    await expect(line).toContainText("wrapped by Cloud KMS key");
    await expect(line).toContainText(KMS_KEY);
    await expect(line).toContainText("last unwrapped");

    const lineBox = await line.boundingBox();
    const section = await page.getByTestId("settings-section").boundingBox();
    expect(lineBox.x).toBeGreaterThanOrEqual(section.x - 0.5);
    expect(lineBox.x + lineBox.width).toBeLessThanOrEqual(
      section.x + section.width + 0.5,
    );
    const code = await line.locator("code").first().boundingBox();
    expect(code.x + code.width).toBeLessThanOrEqual(
      lineBox.x + lineBox.width + 0.5,
    );
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
  });
}
