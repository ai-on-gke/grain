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

// A key typed into a new provider connection's credential box and
// followed by Save connection is stored with it. Found in the 2026-11-04
// connections and infrastructure settings walkthrough: Save connection
// stored everything but the key, the pane then read "No unsaved
// changes" with the key still in the box, and only leaving asked about
// it. The demo runs with -secrets-store so the credential PUT reaches a
// real encrypted store; nothing is stubbed.

test.use({ demoArgs: ["-secrets-store"] });

test("Save connection stores a typed key, and leaving afterwards asks nothing", async ({
  page,
  request,
}) => {
  const credentialPuts = [];
  page.on("response", (response) => {
    if (
      response.request().method() === "PUT" &&
      /\/api\/agent-backends\/[^/]+\/credential$/.test(response.url())
    ) {
      credentialPuts.push(response.status());
    }
  });
  const dialogs = [];
  page.on("dialog", async (dialog) => {
    dialogs.push(dialog.message());
    await dialog.dismiss();
  });

  const before = new Set(
    (
      (await (await request.get("/api/settings")).json()).agentBackends || []
    ).map((b) => b.id),
  );
  try {
    await page.goto("/settings/models");
    await page.getByRole("button", { name: "New connection" }).click();
    const pane = page.locator(".overlay-pane");
    await pane.getByLabel(/Agent type/i).click();
    await page.getByRole("option", { name: "Codex" }).click();
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByLabel("Authentication method").click();
    await page.getByRole("option", { name: "OpenAI API key" }).click();

    const key = pane.getByLabel("OpenAI API key", { exact: true });
    await key.fill("sk-e2e-typed-credential");
    await expect(pane.getByText("No unsaved changes")).toHaveCount(0);

    await page.getByRole("button", { name: "Save connection" }).click();
    await expect(pane.getByText("No unsaved changes")).toBeVisible();
    await expect(key).toHaveValue("");
    expect(credentialPuts).toEqual([200]);

    await page.locator(".overlay-pane-back button").last().click();
    await expect(
      page.getByRole("heading", { name: "Provider Connections" }),
    ).toBeVisible();
    expect(dialogs).toEqual([]);
  } finally {
    const after =
      (await (await request.get("/api/settings")).json()).agentBackends || [];
    for (const backend of after) {
      if (!before.has(backend.id)) {
        await request.delete(
          `/api/deployment/agent-backends/${encodeURIComponent(backend.id)}`,
        );
      }
    }
  }
});
