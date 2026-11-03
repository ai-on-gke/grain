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

test.setTimeout(120_000);

// A connection's card on Models & connections: its name's heading, up to
// the box that says what it advertises. The connection's name is on the
// models below the cards too, in each model's "Served by".
const connectionCard = (page, name) =>
  page
    .getByRole("heading", { name, exact: true })
    .locator(
      "xpath=ancestor::div[contains(normalize-space(.), 'Advertises:')][1]",
    );

test.describe("Walkthrough: Provider Connection Lifecycle, Key Verification and Agent Runtime Health", () => {
  test("exercises AI provider connection management, model selection, and live connection test diagnostics", async ({
    page,
  }) => {
    // Monitor console errors and 5xx responses
    const consoleErrors = [];
    page.on("console", (msg) => {
      if (msg.type() === "error") {
        consoleErrors.push(msg.text());
      }
    });

    const failedRequests = [];
    page.on("response", (response) => {
      if (response.status() >= 500) {
        failedRequests.push(
          `${response.request().method()} ${response.url()} -> ${response.status()}`,
        );
      }
    });

    // Accept confirmation dialogs automatically
    page.on("dialog", (dialog) => dialog.accept());

    // Grant clipboard permissions for copy command tests
    await page
      .context()
      .grantPermissions(["clipboard-read", "clipboard-write"]);

    // -----------------------------------------------------------------------
    // SETUP
    // -----------------------------------------------------------------------
    await page.goto("/");
    await expect(page.locator(".task-row").first()).toBeVisible();

    // Save initial settings for full restoration at end of walkthrough
    const initialSettingsRes = await page.request.get("/api/settings");
    const initialSettings = initialSettingsRes.ok()
      ? await initialSettingsRes.json()
      : null;
    const initialDeploymentRes = await page.request.get("/api/deployment");
    const initialDeployment = initialDeploymentRes.ok()
      ? await initialDeploymentRes.json()
      : null;

    try {
      // -----------------------------------------------------------------------
      // STEP 1: AI Provider Connection Management Lifecycle
      // -----------------------------------------------------------------------
      // Navigate to Settings -> Models & connections, the deployment's own
      // connections.
      await page.goto("/admin");
      await page.getByTestId("settings-nav-models").click();

      // Verify Provider Connections header & description
      await expect(
        page.getByRole("heading", { name: "Settings", exact: true }),
      ).toBeVisible();
      await expect(
        page.getByRole("heading", { name: "Provider Connections" }),
      ).toBeVisible();
      await expect(
        page.getByText(/A connection is a way to reach a provider/),
      ).toBeVisible();

      // 1.1 Verify initial seeded connection (Gemini 2.5 Pro)
      const initialGemini = connectionCard(page, "Gemini 2.5 Pro");
      await expect(initialGemini).toBeVisible();
      await expect(
        initialGemini.getByText("Antigravity", { exact: true }),
      ).toBeVisible();

      // 1.2 Create New Provider Connection (Antigravity with API key)
      await page.getByRole("button", { name: "New connection" }).click();
      await expect(
        page.getByRole("heading", { name: "New connection" }),
      ).toBeVisible();
      await expect(
        page.getByText("Choose an agent type to begin"),
      ).toBeVisible();

      // Select Agent Type: Antigravity
      await page
        .locator(".overlay-pane")
        .getByLabel(/Agent type/i)
        .click();
      await page.getByRole("option", { name: "Antigravity" }).click();
      await page.getByRole("button", { name: "Continue" }).click();

      // Configure Authentication: Gemini API key
      await expect(
        page.getByRole("heading", { name: "Authentication" }),
      ).toBeVisible();
      await page.getByLabel("Authentication method").click();
      await page.getByRole("option", { name: "Gemini API key" }).click();

      // Fill Connection Attributes
      const nameField = page
        .locator(".overlay-pane")
        .getByLabel("Name", { exact: true });
      await expect(nameField).toBeVisible();
      await nameField.fill("Gemini Production Cluster");
      await nameField.blur();

      const saveModelBtn = page.getByRole("button", {
        name: "Save connection",
      });
      if (await saveModelBtn.isEnabled()) {
        await saveModelBtn.click();
        await expect(saveModelBtn).toBeDisabled();
      }

      // Return to Connections tab
      await page.locator(".overlay-pane-back button").last().click();
      await expect(
        page.getByRole("heading", { name: "Provider Connections" }),
      ).toBeVisible();
      await expect(
        connectionCard(page, "Gemini Production Cluster"),
      ).toBeVisible();

      // 1.4 Connection Credential Reuse & Dependency Protections
      // Create base Codex connection
      await page.getByRole("button", { name: "New connection" }).click();
      await page
        .locator(".overlay-pane")
        .getByLabel(/Agent type/i)
        .click();
      await page.getByRole("option", { name: "Codex" }).click();
      await page.getByRole("button", { name: "Continue" }).click();

      await page.getByLabel("Authentication method").click();
      await page.getByRole("option", { name: "OpenAI API key" }).click();

      const codexPrimaryName = page
        .locator(".overlay-pane")
        .getByLabel("Name", { exact: true });
      await expect(codexPrimaryName).toBeVisible();
      await codexPrimaryName.fill("Codex Primary Core");
      await codexPrimaryName.blur();

      const saveCodex1Btn = page.getByRole("button", {
        name: "Save connection",
      });
      if (await saveCodex1Btn.isEnabled()) {
        await saveCodex1Btn.click();
        await expect(saveCodex1Btn).toBeDisabled();
      }

      await page.locator(".overlay-pane-back button").last().click();
      await expect(
        page.getByRole("heading", { name: "Provider Connections" }),
      ).toBeVisible();

      // Create secondary Codex connection reusing credentials from Primary
      await page.getByRole("button", { name: "New connection" }).click();
      await page
        .locator(".overlay-pane")
        .getByLabel(/Agent type/i)
        .click();
      await page.getByRole("option", { name: "Codex" }).click();
      await page.getByRole("button", { name: "Continue" }).click();

      await page.getByLabel("Authentication method").click();
      await page
        .getByRole("option", { name: /Reuse authentication from/i })
        .first()
        .click();

      const codexSecName = page
        .locator(".overlay-pane")
        .getByLabel("Name", { exact: true });
      await expect(codexSecName).toBeVisible();
      await codexSecName.fill("Codex Secondary Pool");
      await codexSecName.blur();

      const saveCodex2Btn = page.getByRole("button", {
        name: "Save connection",
      });
      if (await saveCodex2Btn.isEnabled()) {
        await saveCodex2Btn.click();
        await expect(saveCodex2Btn).toBeDisabled();
      }

      await page.locator(".overlay-pane-back button").last().click();
      await expect(
        page.getByRole("heading", { name: "Provider Connections" }),
      ).toBeVisible();

      // Verify Primary connection card cannot be removed while referenced
      const primaryCodexCard = connectionCard(page, "Codex Primary Core");
      await expect(primaryCodexCard).toBeVisible();
      const usedAuthBtn = primaryCodexCard.getByRole("button", {
        name: "Used for authentication",
      });
      await expect(usedAuthBtn).toBeDisabled();

      // 1.5 Enable/Disable Status Toggle
      const toggleTarget = connectionCard(page, "Codex Secondary Pool");
      await expect(toggleTarget).toBeVisible();

      const disableBtn = toggleTarget.getByRole("button", { name: "Disable" });
      await expect(disableBtn).toBeVisible();
      await disableBtn.click();

      // Verify "Disabled" chip is visible
      await expect(toggleTarget.getByText("Disabled")).toBeVisible();
      const enableBtn = toggleTarget.getByRole("button", { name: "Enable" });
      await expect(enableBtn).toBeVisible();
      await enableBtn.click();
      await expect(toggleTarget.getByText("Disabled")).not.toBeVisible();

      // 1.6 Remove Unreferenced Connection
      const removeBtn = toggleTarget.getByRole("button", { name: "Remove" });
      await expect(removeBtn).toBeVisible();
      await removeBtn.click();
      await expect(
        connectionCard(page, "Codex Secondary Pool"),
      ).not.toBeVisible();

      // Now that secondary is removed, primary is no longer referenced and can be deleted
      await expect(
        primaryCodexCard.getByRole("button", { name: "Remove" }),
      ).toBeVisible();
      await primaryCodexCard.getByRole("button", { name: "Remove" }).click();
      await expect(
        connectionCard(page, "Codex Primary Core"),
      ).not.toBeVisible();

      // -----------------------------------------------------------------------
      // STEP 2: Live Connection Test Diagnostics & Key Verification
      // -----------------------------------------------------------------------
      // GET /api/agent-models is grain passing on what the agent's own
      // vendor says: a daemon's lister runs `agy models` with the
      // connection's credential (agyModelLister in pkg/cli/daemon.go). The
      // demo has no binary and no key, so this stands in for that external
      // answer, a catalog first and then a refused key. It is stubbed in the
      // handler's own response shape (agentModelsResponse in
      // pkg/ui/agent_models.go).
      let simulateFailure = false;
      await page.route("**/api/agent-models*", async (route) => {
        if (simulateFailure) {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            json: {
              available: true,
              error:
                "AUTHENTICATION_ERROR: API key has expired or lacks quota for model catalog discovery",
            },
          });
        } else {
          await route.fulfill({
            status: 200,
            contentType: "application/json",
            json: {
              available: true,
              models: [
                {
                  id: "gemini-2.5-pro",
                  label: "Gemini 2.5 Pro",
                  family: "gemini-2.5",
                },
                {
                  id: "gemini-2.5-flash",
                  label: "Gemini 2.5 Flash",
                  family: "gemini-2.5",
                },
                {
                  id: "gemini-1.5-pro",
                  label: "Gemini 1.5 Pro",
                  family: "gemini-1.5",
                },
              ],
              efforts: ["low", "medium", "high"],
            },
          });
        }
      });

      // 2.1 Trigger Live Connection Test on Healthy Connection
      const geminiCard = connectionCard(page, "Gemini Production Cluster");
      await expect(geminiCard).toBeVisible();

      simulateFailure = false;
      const testBtn = geminiCard.getByRole("button", { name: "Test" });
      await expect(testBtn).toBeVisible();
      await testBtn.click();

      // Verify Success Alert with models count diagnostics
      await expect(
        geminiCard.getByText(/Connection valid \(3 models available\)/i),
      ).toBeVisible();

      // 2.2 Test Diagnostic Feedback on Failed Connection Check
      // Create connection configured to test failure
      await page.getByRole("button", { name: "New connection" }).click();
      await page
        .locator(".overlay-pane")
        .getByLabel(/Agent type/i)
        .click();
      await page.getByRole("option", { name: "Antigravity" }).click();
      await page.getByRole("button", { name: "Continue" }).click();

      await page.getByLabel("Authentication method").click();
      await page.getByRole("option", { name: "Gemini API key" }).click();

      const failNameField = page
        .locator(".overlay-pane")
        .getByLabel("Name", { exact: true });
      await expect(failNameField).toBeVisible();
      await failNameField.fill("Failing Diagnostic Backend");
      await failNameField.blur();

      const saveFailBtn = page.getByRole("button", { name: "Save connection" });
      if (await saveFailBtn.isEnabled()) {
        await saveFailBtn.click();
        await expect(saveFailBtn).toBeDisabled();
      }

      await page.locator(".overlay-pane-back button").last().click();

      const failingCard = connectionCard(page, "Failing Diagnostic Backend");
      await expect(failingCard).toBeVisible();

      simulateFailure = true;
      const failTestBtn = failingCard.getByRole("button", { name: "Test" });
      await failTestBtn.click();

      // Verify Error Alert diagnostics message
      const errorAlert = failingCard.locator(".MuiAlert-standardError");
      await expect(errorAlert).toBeVisible();
      await expect(
        errorAlert.getByText(/AUTHENTICATION_ERROR: API key has expired/i),
      ).toBeVisible();

      // Close/dismiss diagnostic alert
      const closeAlertBtn = errorAlert.locator('button[aria-label="Close"]');
      await expect(closeAlertBtn).toBeVisible();
      await closeAlertBtn.click();
      await expect(
        failingCard.getByText(/AUTHENTICATION_ERROR/),
      ).not.toBeVisible();

      // Clean up failing card
      await failingCard.getByRole("button", { name: "Remove" }).click();
      simulateFailure = false;

      // -----------------------------------------------------------------------
      // STEP 3: Advertised models, the team's Models tab, and a task's model
      // -----------------------------------------------------------------------
      // 3.1 A connection advertises models; it does not choose one. Its page
      // shows what the provider lists (the agent-models stub above stands in
      // for the provider), read-only, and keeps the advertised list editable.
      await geminiCard.getByRole("button", { name: "Edit" }).click();
      const discovered = page.getByLabel("Discovered models");
      await expect(discovered.getByText("gemini-1.5-pro")).toBeVisible();
      await expect(page.getByLabel(/reasoning effort/i)).toHaveCount(0);
      await page.getByRole("button", { name: "Refresh" }).click();
      await expect(discovered.getByText("gemini-2.5-flash")).toBeVisible();
      // Saved with no models, it advertises whatever its provider lists.
      // This demo's daemon cannot ask that provider itself (the stub above
      // answers the browser only), so the list is pinned to what it shows.
      await expect(page.getByLabel("Advertised models")).toContainText(
        "gemini-2.5-flash",
      );
      await page
        .getByRole("button", { name: "Pin to a fixed list instead" })
        .click();
      await page.getByRole("button", { name: "Save connection" }).click();
      await expect(page.getByText("No unsaved changes")).toBeVisible();
      await page.locator(".overlay-pane-back button").last().click();
      await expect(
        geminiCard.getByText(
          "Advertises: gemini-2.5-pro, gemini-2.5-flash, gemini-1.5-pro",
        ),
      ).toBeVisible();

      // 3.2 The team's default model is one its connections advertise, and
      // effort is the team's own per-model setting.
      await page.getByTestId("settings-nav-models").click();
      await expect(
        page.getByText(/Choose the model this deployment runs on/),
      ).toBeVisible();
      await page.getByLabel("Default model").click();
      await page
        .getByRole("option", { name: "gemini-1.5-pro", exact: true })
        .click();
      const proRow = page.getByTestId("model-gemini-1.5-pro");
      await expect(proRow.getByText("Default", { exact: true })).toBeVisible();
      await expect(
        proRow.getByText("Gemini Production Cluster (Enabled)"),
      ).toBeVisible();
      await proRow.getByLabel("Reasoning effort").click();
      await page.getByRole("option", { name: "high", exact: true }).click();
      await expect(proRow.getByLabel("Reasoning effort")).toHaveText("high");
      // gemini-2.5-pro is served by two connections, the seeded one too.
      await expect(
        page.getByTestId("model-gemini-2.5-pro").getByText("2 connections"),
      ).toBeVisible();

      const team = await (await page.request.get("/api/deployment")).json();
      expect(team.defaultModel).toBe("gemini-1.5-pro");
      expect(team.modelSettings).toContainEqual({
        model: "gemini-1.5-pro",
        effort: "high",
      });

      // 3.3 A task chooses its model, and may pin a connection that serves
      // it; there is no framework to choose.
      await page.goto("/");
      await page.getByRole("button", { name: "+ New task" }).click();
      await expect(
        page.getByRole("heading", { name: "New task" }),
      ).toBeVisible();
      await page.getByLabel("Title").fill(`Model choice ${Date.now()}`);
      await page.getByLabel(/^Repos/).selectOption("none");
      await expect(page.getByLabel("Agent framework")).toHaveCount(0);
      await page.getByLabel("Model", { exact: true }).click();
      await page
        .getByRole("option", { name: "gemini-2.5-flash", exact: true })
        .click();
      await page.getByRole("button", { name: "Advanced options" }).click();
      await page.getByLabel("Specific connection").click();
      await expect(
        page.getByRole("option", { name: "Infra Claude Dedicated · Claude" }),
      ).toHaveCount(0);
      await page
        .getByRole("option", {
          name: "Gemini Production Cluster · Antigravity",
        })
        .click();
      const filed = page.waitForResponse(
        (response) =>
          response.url().endsWith("/api/tasks") &&
          response.request().method() === "POST",
      );
      await page.getByRole("button", { name: "Create task" }).click();
      const filedResponse = await filed;
      expect(filedResponse.status()).toBeLessThan(300);
      const sent = filedResponse.request().postDataJSON();
      expect(sent.model).toBe("gemini-2.5-flash");
      expect(sent).not.toHaveProperty("agentFramework");
      const task = await filedResponse.json();
      expect(task.model).toBe("gemini-2.5-flash");
      expect(task.agentBackendId).toBe(sent.agentBackendId);

      // Reset back to main root page
      await page.goto("/");

      // Final sanity assertion: no server 5xx errors occurred
      expect(
        failedRequests,
        `Unexpected 5xx responses: ${failedRequests.join(", ")}`,
      ).toHaveLength(0);
    } finally {
      // Restore initial settings to avoid polluting shared test state
      if (initialSettings) {
        await page.request
          .put("/api/settings", {
            data: {
              agentBackends: initialSettings.agentBackends,
              defaultModel: initialSettings.defaultModel,
            },
          })
          .catch(() => {});
      }
      if (initialDeployment) {
        await page.request
          .put("/api/deployment", {
            data: {
              defaultModel: initialDeployment.defaultModel || "",
              modelSettings: initialDeployment.modelSettings || [],
            },
          })
          .catch(() => {});
      }
    }
  });
});
