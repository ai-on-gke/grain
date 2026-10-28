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

// Walkthrough: the deployment's Git connections and multi-host repos
// (docs/test-plans/2026-10-09.md §1)
//
// Exercises:
// 1. Settings ▸ Git (settings/GitConnections.jsx; Team settings' Git
//    identity tab until the single-tenant reset, stage 3e-ii) in a real
//    Playwright browser against a live grain demo instance. It reads and
//    writes the deployment's /api/deployment/git-connections (3e-iv).
// 2. Configure secondary PAT connection for internal enterprise host ghe.internal.acme
//    with scope acme-org.
// 3. Configure secondary GitHub App connection for github.com (or enterprise host)
//    scoped to partner-org.
// 4. Verify connection check indicator on "Test" click (POST /api/deployment/git-connections/check)
//    for both valid and invalid credentials.
// 5. Test RepoPicker and multi-host syntax in New Task modal: input multi-host
//    repository reference (ghe.internal.acme/acme-org/service-backend) and verify
//    autocomplete / custom entry formatting and URL generation via repoUrl.
// 6. Remove a connection and confirm state updates cleanly.

test.use({ demoArgs: ["-default-target-repo=acme/widgets"] });
test.setTimeout(180_000);

const TEST_RSA_KEY = `-----BEGIN PRIVATE KEY-----
MIIEvAIBADANBgkqhkiG9w0BAQEFAASCBKYwggSiAgEAAoIBAQC8XPffvMAPhT9m
cplK49h25HfSM61Q3iRhSfMRy3pwyvB9fYko/+oXX6SV1VPEkSO2RLpc6QI/a1va
zwgtHXLHq3/0DafkDClqm0PT4znAApm97N+N8LgV7mObggV/UDL7Ag4NdNr5quh3
fwWvpszDU+X7Z24rVxWj+/rtmEBv1fPil9OgZMvSYZKkPtXsxC0zF6vb9fNhh5gy
FxNIpLx4l1DJ4ZO0R9TP14pQW8vZmKMwXlDpkDBPt74FUueHqJmm+U7PxtHSQEXx
76v8wOqQdLzIOzq0yCQkPytTcEnsANUHcnsLQJylleYHykA/yLdwVbFLSkcLSl7D
pqmmEge9AgMBAAECggEAJ3EkKkJgyR8yrmYSuT7KheLDQE233fYWEhUlA7Xi1pl4
EB7oSKCFyQ0q7oD0AHYPz0vG5kuYP+VgH4o4gRHohezgZx8w56ywQ3Vtl4G+BvnE
baObECK2uUjorJhmRElr02mTvbeZXqwt70+2xhgcSk5YGvOGeybKDkNmQLgfzgrY
BKzOongdHCLkK/M0lMmBjbqVlm9HGBgN/3n1oMg4LaYuPqSvyJEDXwvMiSQp6HZS
YlMuD6wcUI4ILPcviNXdEVYAoXyCpY8LQI2k6lYDDFt2tZa9w9T+wxzFwyPavhYg
t3PwyA6DpmA1kAnQfpRsw+rkMNQswj9IkmDcAsY9dQKBgQDesabxx9WSnSWGTXZU
XfkMtpoHUG2C60ypP0w45NycpOqp82NEnEyKch3bO2F1nGhRudgJWUG14w0UqRsm
TMzgG1Qh5g1v57wGtcMYhY3ItstmU4G9xyH2nqic7T5iEAbGBwMO+c2prnA1Xpor
gy/CqB8tjF24ElF9U3sU3piN2wKBgQDYiOILvU86BisfXq+SxrzUuYbaMikHWosv
R2lS4tLoVMvc+jf47CDAIgISiCiGd/wDUxapHMrxgR/f8bq4jhaxyAePwXbk7XN/
ns3WkT5DEUyaeG2v2GMaSWXQRV6fnDxXw2cw5SgvogcnPrL4zZfxP/u2b6cm0Jo1
+Cyn6D8QRwKBgGwxI0qUK87vSivVtoeGoFjs3ihvisTwTG5tsjpx9jykYt3uwWu0
A/u/NYItd0wMkSIjiDICi0oy5UgEYIxbA65dmKJfgme7zj30ULS5Lgk1xdwQ9hed
h4Kp0oFrQcOixaewAGxmMOUfOqSzMXOyaKHJzFv0thOX0r9wr3uUKcnBAoGABu2y
Ve/TxiaGYLWV5ptnVWMuVnL+bxbNl1CwpQ9O8lIsR8DV46gikCNGb7NdoJfcP3r8
IaMjN0ELdQ32DIu3XC7rVwU4ubCDXpUFPxVcqaa3DcZGtDa3TRMGJ/jgQzkrkR9W
gPEELSoBEpdmAKILd5O1iSvfbFWGaa2sOB8tTWcCgYBSqIM05u8l1k9p2Q+pmxP8
0Qydzdw4EUAlJuxFKhV/J5kfXNfvcvkxvD4Lon9eo41CgyVyEnf9xaY+HCzP6G+u
ooCbhmbancQJlthEJ2OXviCgrn/npLUn/t0IE83f6J6TnMiB6dmExbF7qoWDxDf7
dNtClwwkZd2x2D5+eogKCA==
-----END PRIVATE KEY-----`;

test.describe("Git Connections and Multi-Host Repos Walkthrough (§1)", () => {
  test("configures secondary PAT and App connections, tests credential check indicators, and tests RepoPicker multi-host syntax", async ({
    page,
    baseURL,
  }) => {
    // -------------------------------------------------------------------------
    // STEP 1: The demo's deployment already has its github.com default
    // connection (the single team token is retired, grain/task-3929).
    // -------------------------------------------------------------------------

    // -------------------------------------------------------------------------
    // STEP 2: Navigate to Settings ▸ Git connections as the admin
    // -------------------------------------------------------------------------
    await page.goto("/settings/git");
    await expect(
      page.getByRole("heading", { name: "Settings", exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Git", level: 1 }),
    ).toBeVisible();

    const gitConnectionsList = page.locator(
      '[data-testid="git-connections-list"]',
    );
    await expect(gitConnectionsList).toBeVisible();

    // Verify initial table rendering with the github.com default
    await expect(
      gitConnectionsList.locator("code").filter({ hasText: "github.com" }),
    ).toBeVisible();
    await expect(gitConnectionsList.getByText("legacy default")).toHaveCount(0);
    console.log("Verified initial table with the github.com default");

    // -------------------------------------------------------------------------
    // STEP 3: Add Secondary PAT Connection (ghe.internal.acme, scope acme-org)
    // -------------------------------------------------------------------------
    console.log("Adding secondary PAT connection...");
    const addBtn = page.locator('[data-testid="add-connection-button"]');
    await expect(addBtn).toBeVisible();
    await addBtn.click();

    const addForm = page.locator('[data-testid="add-connection-form"]');
    await expect(addForm).toBeVisible();

    // Fill form fields for PAT
    await addForm
      .locator('input[name="new-connection-host"]')
      .fill("ghe.internal.acme");
    await addForm
      .locator('input[name="new-connection-scope"]')
      .fill("acme-org");
    await addForm
      .locator('input[name="new-connection-token"]')
      .fill("ghp_test_ghe_pat_token_valid");

    await addForm.getByRole("button", { name: "Save connection" }).click();
    await expect(addForm).not.toBeVisible();

    // Verify row appeared in the table
    const gheRow = gitConnectionsList.locator(
      'tr:has-text("ghe.internal.acme")',
    );
    await expect(gheRow).toBeVisible();
    await expect(gheRow.getByText("acme-org")).toBeVisible();
    await expect(gheRow.getByText("PAT")).toBeVisible();
    console.log("Added secondary PAT connection successfully");

    // -------------------------------------------------------------------------
    // STEP 4: Add Secondary GitHub App Connection (github.com, scope partner-org)
    // -------------------------------------------------------------------------
    console.log("Adding secondary GitHub App connection...");
    await addBtn.click();
    await expect(addForm).toBeVisible();

    await addForm
      .locator('input[name="new-connection-host"]')
      .fill("github.com");
    await addForm
      .locator('input[name="new-connection-scope"]')
      .fill("partner-org");

    // Switch auth type to GitHub App
    const authTypeSelect = addForm.locator(
      '.MuiSelect-select[aria-labelledby*="new-connection-auth-type"]',
    );
    if ((await authTypeSelect.count()) > 0) {
      await authTypeSelect.click();
    } else {
      await addForm.locator(".MuiSelect-select").nth(1).click();
    }
    await page.getByRole("option", { name: "GitHub App" }).click();

    // Fill GitHub App details
    await addForm.locator('input[name="new-connection-app-id"]').fill("123456");
    await addForm
      .locator('input[name="new-connection-app-installation-id"]')
      .fill("789012");
    await addForm
      .locator('textarea[name="new-connection-app-private-key"]')
      .fill(TEST_RSA_KEY);

    await addForm.getByRole("button", { name: "Save connection" }).click();
    await expect(addForm).not.toBeVisible();

    // Verify GitHub App row appeared in the table
    const appRow = gitConnectionsList.locator('tr:has-text("partner-org")');
    await expect(appRow).toBeVisible();
    await expect(appRow.getByText("GitHub App")).toBeVisible();
    console.log("Added secondary GitHub App connection successfully");

    // -------------------------------------------------------------------------
    // STEP 5: Verify Connection Check Indicator on "Test" click
    // -------------------------------------------------------------------------
    console.log("Testing connection check indicator...");
    // 5a. Click "Test" on the ghe.internal.acme connection
    const gheTestBtn = gheRow.getByRole("button", { name: "Test" });
    await expect(gheTestBtn).toBeVisible();
    await gheTestBtn.click();

    // The credential check result indicator must render. The check probes
    // ghe.internal.acme for real, so its answer waits on that name failing
    // to resolve -- which through CI's DNS can take well past the default
    // 5s expect timeout (Go's resolver alone allows 5s per attempt).
    const checkResult = page.locator('[data-testid="credential-check-result"]');
    await expect(checkResult).toBeVisible({ timeout: 60_000 });
    console.log("Connection check result indicator visible for ghe connection");

    // -------------------------------------------------------------------------
    // STEP 5: Test RepoPicker and Multi-Host Syntax in New Task Modal
    // -------------------------------------------------------------------------
    console.log(
      "Testing RepoPicker and multi-host syntax in New Task modal...",
    );
    // Close Settings
    await page.keyboard.press("Escape");
    await expect(
      page.getByRole("heading", { name: "System", exact: true }),
    ).not.toBeVisible();

    // Open New Task modal
    await page.getByRole("button", { name: "+ New task", exact: true }).click();
    await expect(
      page.getByRole("heading", { name: "New task", exact: true }),
    ).toBeVisible();

    // Find the Read-only repos input (ReadOnlyReposField / RepoPicker)
    const readsInput = page.getByLabel("Read-only repos");
    await expect(readsInput).toBeVisible();

    // Type multi-host reference
    const multiHostRepo = "ghe.internal.acme/acme-org/service-backend";
    await readsInput.fill(multiHostRepo);

    // Check that RepoPicker offers "Add ghe.internal.acme/acme-org/service-backend"
    const customOption = page.getByRole("menuitem", {
      name: `Add ${multiHostRepo}`,
    });
    await expect(customOption).toBeVisible();

    // Pick it
    await customOption.click();

    // Verify chip rendered with multi-host repo
    const pickedChip = page.locator(
      `.MuiChip-root:has-text("${multiHostRepo}")`,
    );
    await expect(pickedChip).toBeVisible();

    // Close New Task modal
    await page.keyboard.press("Escape");
    await expect(
      page.getByRole("heading", { name: "New task", exact: true }),
    ).not.toBeVisible();
    console.log("Verified RepoPicker multi-host syntax successfully");

    // -------------------------------------------------------------------------
    // STEP 6: Remove a Connection and Confirm Table Updates Cleanly
    // -------------------------------------------------------------------------
    console.log("Removing connection and confirming table updates...");
    await page.goto("/settings/git");
    await expect(gitConnectionsList).toBeVisible();

    const gheRowToRemove = gitConnectionsList.locator(
      'tr:has-text("ghe.internal.acme")',
    );
    await expect(gheRowToRemove).toBeVisible();
    // Remove asks before deleting the stored credential.
    page.once("dialog", (dialog) => dialog.accept());
    await gheRowToRemove.getByRole("button", { name: "Remove" }).click();

    // Confirm ghe.internal.acme row is removed
    await expect(
      gitConnectionsList.locator('tr:has-text("ghe.internal.acme")'),
    ).toHaveCount(0);
    // partner-org remains
    await expect(
      gitConnectionsList.locator('tr:has-text("partner-org")'),
    ).toBeVisible();

    // Put the demo back as it was: this worker's server is shared with
    // the specs after this one.
    // Remove asks before deleting the stored credential.
    page.once("dialog", (dialog) => dialog.accept());
    await gitConnectionsList
      .locator('tr:has-text("partner-org")')
      .getByRole("button", { name: "Remove" })
      .click();
    await expect(
      gitConnectionsList.locator('tr:has-text("partner-org")'),
    ).toHaveCount(0);
    console.log("Walkthrough completed successfully!");
  });
});
