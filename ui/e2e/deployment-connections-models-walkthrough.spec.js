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

// docs/test-plans/2026-10-04.md §1, step 2, as a person sees it: the
// deployment's admin sets up its connections and default model in
// Settings > Models & connections, which the single-tenant reset (stage
// 3e-ii) moved there from Team settings. The editor saves through the
// deployment's routes (3e-iv): PUT
// /api/deployment/agent-backends/{backendId} for a connection and PUT
// /api/deployment for defaultModel. The report this walkthrough first
// ran for is docs/test-runs/2026-10-04-connections-and-models.md.

test.setTimeout(180_000);

// A connection's card: the nearest box around its heading that also says
// what it advertises.
const card = (page, name) =>
  page
    .getByRole("heading", { name, exact: true })
    .locator(
      "xpath=ancestor::div[contains(normalize-space(.), 'Advertises:')][1]",
    );

async function addConnection(page, name, models) {
  await page.getByRole("button", { name: "New connection" }).click();
  await expect(
    page.getByRole("heading", { name: "New connection" }),
  ).toBeVisible();
  await page
    .locator(".overlay-pane")
    .getByLabel(/Agent type/i)
    .click();
  await page.getByRole("option", { name: "Claude" }).click();
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByLabel("Authentication method").click();
  await page.getByRole("option", { name: "Claude Code OAuth token" }).click();
  const nameField = page
    .locator(".overlay-pane")
    .getByLabel("Name", { exact: true });
  await nameField.fill(name);
  await nameField.blur();
  // A Claude connection advertises every model its provider lists unless
  // pinned; this one is pinned to the models typed in.
  await page
    .getByRole("button", { name: "Pin to a fixed list instead" })
    .click();
  const advertised = page.getByLabel("Advertised models");
  for (const m of models) {
    await advertised.fill(m);
    await advertised.press("Enter");
  }
  const save = page.getByRole("button", { name: "Save connection" });
  await expect(save).toBeEnabled();
  await save.click();
  await expect(save).toBeDisabled();
  await page.locator(".overlay-pane-back button").last().click();
  await expect(
    page.getByRole("heading", { name: "Provider Connections" }),
  ).toBeVisible();
  await expect(card(page, name)).toBeVisible();
}

test.describe("The deployment's connections and models in Settings", () => {
  test("sets up two connections and a default model, and every value survives a reload", async ({
    page,
    request,
  }) => {
    const failed = [];
    page.on("response", (r) => {
      if (r.status() >= 500)
        failed.push(`${r.request().method()} ${r.url()} -> ${r.status()}`);
    });
    page.on("dialog", (d) => d.accept());

    // What the demo starts with, put back at the end: this worker's
    // server is shared with the specs after this one.
    const before = await (await request.get("/api/deployment")).json();

    await page.goto("/settings/models");
    await expect(
      page.getByRole("heading", { name: "Models", level: 1 }),
    ).toBeVisible();
    await expect(
      page.getByText("The deployment's provider connections."),
    ).toBeVisible();
    await addConnection(page, "Deployment One", ["model-a", "model-b"]);
    await addConnection(page, "Deployment Two", ["model-a"]);

    // The default model, chosen from what the connections serve.
    await page.getByLabel("Default model").click();
    await page.getByRole("option", { name: "model-a", exact: true }).click();
    await expect
      .poll(
        async () =>
          (await (await request.get("/api/deployment")).json()).defaultModel,
      )
      .toBe("model-a");

    // Every value survives a reload, read back from the server.
    await page.reload();
    await expect(page.getByLabel("Default model")).toHaveValue(/model-a/);
    const one = card(page, "Deployment One");
    await expect(one).toBeVisible();
    await expect(one.getByText("Advertises: model-a, model-b")).toBeVisible();
    await expect(
      card(page, "Deployment Two").getByText("Advertises: model-a"),
    ).toBeVisible();
    const config = await (await request.get("/api/config")).json();
    const mine = (config.agentBackends || []).filter((b) =>
      ["Deployment One", "Deployment Two"].includes(b.name),
    );
    expect(mine.map((b) => b.name).sort()).toEqual([
      "Deployment One",
      "Deployment Two",
    ]);
    expect(mine.find((b) => b.name === "Deployment One").models).toEqual([
      "model-a",
      "model-b",
    ]);
    expect(mine.find((b) => b.name === "Deployment Two").models).toEqual([
      "model-a",
    ]);

    // Disabling one is saved too.
    await one.getByRole("button", { name: "Disable" }).click();
    await expect(one.getByRole("button", { name: "Enable" })).toBeVisible();
    await page.reload();
    await expect(
      card(page, "Deployment One").getByRole("button", { name: "Enable" }),
    ).toBeVisible();
    await expect(
      card(page, "Deployment Two").getByRole("button", { name: "Disable" }),
    ).toBeVisible();

    // Put the demo back as it was.
    for (const b of mine) {
      const res = await request.delete(
        `/api/deployment/agent-backends/${encodeURIComponent(b.id)}`,
      );
      expect(res.ok(), await res.text()).toBe(true);
    }
    const restored = await request.put("/api/deployment", {
      data: { defaultModel: before.defaultModel || "" },
    });
    expect(restored.ok(), await restored.text()).toBe(true);

    expect(failed).toEqual([]);
  });
});
