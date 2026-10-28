import { test, expect } from "./fixtures.js";

// Comprehensive E2E test verifying form inputs in:
// 1. Deployment & Engine Settings (Settings, numeric limits, ModelBackendPage, SecretField)
// 2. System Diagnostics & Operations (Logs search filter & source selector, PipelineControl reason prompt, Power actions)
// 3. Navigation Shell & Interactive Consoles (Theme toggles, Timezone selector, Customize Pinned Nav, TaskChat composer, New Session dialog)

test.use({ demoArgs: ["-observability"] });

async function getStoredSettings(page) {
  const res = await page.request.get("/api/settings");
  expect(res.ok()).toBe(true);
  return res.json();
}

test.describe("Deployment Settings, System Operations, and Console Sessions Inputs Walkthrough", () => {
  test("1. Deployment and Engine Settings: numeric limits, out-of-range bounds, and dirty-state tracking", async ({
    page,
  }) => {
    // The CI job cache's bounds are on Settings → CI, split from Engine.
    await page.goto("/settings/ci");
    await expect(
      page.getByRole("heading", { name: "Settings", exact: true }),
    ).toBeVisible();

    // Exercise numeric input: ciCacheEntries
    const entriesBox = page.getByLabel(/^ci job cache entries$/i);
    await expect(entriesBox).toBeVisible();
    await entriesBox.fill("420");
    let saveResponse = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/settings") && r.request().method() === "PUT",
    );
    await entriesBox.press("Tab");
    expect((await saveResponse).status()).toBe(200);
    await expect
      .poll(async () => (await getStoredSettings(page)).ciCacheEntries)
      .toBe(420);

    // Negative / bad numeric input refusal: ciCacheEntries must be >= 0
    await entriesBox.fill("-5");
    await entriesBox.press("Tab");
    await expect(
      page
        .getByText("CI job cache entries must be at least 0; -5 was not saved.")
        .first(),
    ).toBeVisible();
    await expect(entriesBox).toHaveValue("420");

    // Out-of-range refusal: maxMergesPerRepo must be at least 1, on
    // Settings → Engine.
    await page.getByTestId("settings-nav-engine").click();
    const mergesBox = page.getByLabel("Max merges per repo");
    await expect(mergesBox).toBeVisible();
    const mergesBefore = await mergesBox.inputValue();
    await mergesBox.fill("0");
    await mergesBox.press("Tab");
    await expect(
      page.getByText(
        "Max merges per repo branch must be at least 1; 0 was not saved.",
      ),
    ).toBeVisible();
    await expect(mergesBox).toHaveValue(mergesBefore);

    // Verify Sandbox limits tab numeric inputs
    await page.getByTestId("settings-nav-sandboxes").click();
    const cpusBox = page.getByLabel(/^sandbox vcpus$/i);
    const memBox = page.getByLabel(/^sandbox memory \(mib\)$/i);
    const diskBox = page.getByLabel(/^sandbox disk \(gib\)$/i);
    await expect(cpusBox).toBeVisible();
    await expect(memBox).toBeVisible();
    await expect(diskBox).toBeVisible();

    // Fill valid values
    await cpusBox.fill("4");
    saveResponse = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/settings") && r.request().method() === "PUT",
    );
    await cpusBox.press("Tab");
    expect((await saveResponse).status()).toBe(200);
    await expect
      .poll(async () => (await getStoredSettings(page)).sandboxCpus)
      .toBe(4);

    // Deployment ID input verification: deployment-wide, so on the System
    // pane's own Agents & Task Branches tab rather than in a team's
    // settings (grain/task-3091).
    await page.getByTestId("settings-nav-agents").click();
    await expect(
      page.getByRole("heading", { name: "Task branches" }),
    ).toBeVisible();
    const depIdBox = page.getByLabel("Deployment ID");
    await expect(depIdBox).toBeVisible();

    // Out-of-range / invalid format refusal: uppercase letters
    await depIdBox.fill("INVALID_UPPER");
    await depIdBox.press("Enter");
    await expect(page.getByText(/Deployment ID may contain/i)).toBeVisible();
    await expect(depIdBox).toHaveValue("");

    // Valid deployment ID
    await depIdBox.fill("valid-dep-42");
    await depIdBox.press("Enter");
    await expect
      .poll(async () => (await getStoredSettings(page)).deploymentId)
      .toBe("valid-dep-42");

    // On to Settings -> Models & connections, which draws none of the
    // General tab's fields.
    await page.getByTestId("settings-nav-models").click();
    await expect(page.getByLabel("Deployment ID")).toHaveCount(0);

    // Model backend selection & dirty-state warning dialog verification
    const addConnBtn = page.getByRole("button", { name: "+ Add connection" });
    if (await addConnBtn.isVisible()) {
      await addConnBtn.click();
      const modelNameInput = page.getByLabel("Name");
      await expect(modelNameInput).toBeVisible();
      await modelNameInput.fill("Test Unsaved Connection");

      // Verify unsaved dirty state when trying to close via Escape
      let dialogPrompted = false;
      page.once("dialog", async (dialog) => {
        dialogPrompted = true;
        expect(dialog.message()).toMatch(/unsaved changes/i);
        await dialog.dismiss();
      });
      await page.keyboard.press("Escape");
      expect(dialogPrompted).toBe(true);
      await expect(modelNameInput).toBeVisible();

      // Cancel button exits without dirty warning
      await page.getByRole("button", { name: "Cancel" }).click();
    }

    // Leave Settings by the rail: it is a view, not a pane
    await page.getByRole("button", { name: /^Tasks \d+/ }).click();
    await expect(
      page.getByRole("heading", { name: "Settings", exact: true }),
    ).toHaveCount(0);
  });

  test("2. SecretField and GitHub integration inputs: masking, setting, and clear confirmation", async ({
    page,
  }) => {
    // Navigate to Integrations & Auth in Admin settings
    await page.goto("/settings/credentials");
    await expect(
      page.getByRole("heading", { name: "Settings", exact: true }),
    ).toBeVisible();

    // Form inputs in SecretsPanel (Secret, Key, Value)
    await page.getByLabel("Secret", { exact: true }).fill("test-auth-sec");
    await page.getByLabel("Key", { exact: true }).fill("api-token");

    const valInput = page.getByLabel("Value", { exact: true });
    await expect(valInput).toHaveAttribute("type", "password");
    await valInput.fill("dummy-secret-fake-1234");

    // Set secret submits to backend
    await page.getByRole("button", { name: "Set secret" }).click();

    const secretRow = page
      .locator(".secret-row")
      .filter({ hasText: "test-auth-sec" });
    await expect(secretRow).toBeVisible();
    await expect(secretRow).toContainText("api-token");

    // Secret value is masked / never rendered in plain text
    await expect(page.getByText("dummy-secret-fake-1234")).toHaveCount(0);

    // Delete secret confirmation dialog
    let confirmPrompted = false;
    page.once("dialog", async (dialog) => {
      confirmPrompted = true;
      expect(dialog.message()).toMatch(/Delete the secret test-auth-sec/i);
      await dialog.accept();
    });

    await secretRow.getByRole("button", { name: "Delete secret" }).click();
    expect(confirmPrompted).toBe(true);
    await expect(secretRow).toHaveCount(0);
  });

  test("3. System Diagnostics & Operations: Logs search filter, source selector, PipelineControl reason prompt, and Power confirmation", async ({
    page,
  }) => {
    // Navigate to System Diagnostics (Logs)
    await page.goto("/system/logs");
    await expect(
      page.getByRole("heading", { name: "System", exact: true }),
    ).toBeVisible();

    // Verify Log source selector and search filter textfield
    const sourceSelect = page.getByLabel("Source");
    const searchLogsInput = page.getByLabel("Search logs");
    await expect(sourceSelect).toBeVisible();
    await expect(searchLogsInput).toBeVisible();

    // Type in log filter
    await searchLogsInput.fill("observability");
    await expect(searchLogsInput).toHaveValue("observability");

    // Change source selector
    await sourceSelect.click();
    const sourceOptions = page.getByRole("option");
    if ((await sourceOptions.count()) > 1) {
      await sourceOptions.nth(1).click();
    } else {
      await page.keyboard.press("Escape");
    }

    // Switch to Pipeline tab in Admin overlay (PipelineControl)
    await page.getByRole("tab", { name: "Pipeline" }).click();
    const reasonInput = page.getByLabel("Reason (optional)");
    const pauseBtn = page.getByRole("button", { name: "Pause pipeline" });
    await expect(reasonInput).toBeVisible();
    await expect(pauseBtn).toBeVisible();

    // Fill pause reason
    await reasonInput.fill("Automated maintenance check");

    // Handle pause confirmation prompt
    let pausePrompted = false;
    page.once("dialog", async (dialog) => {
      pausePrompted = true;
      expect(dialog.message()).toMatch(/Pause the pipeline\?/i);
      await dialog.accept();
    });

    const pauseRes = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/pipeline-pause") &&
        r.request().method() === "POST",
    );
    await pauseBtn.click();
    expect(pausePrompted).toBe(true);
    expect((await pauseRes).status()).toBe(200);

    // Verify paused alert text reflects reason
    await expect(
      page.getByText(/Reason: Automated maintenance check/i),
    ).toBeVisible();
    const resumeBtn = page.getByRole("button", { name: "Resume pipeline" });
    await expect(resumeBtn).toBeVisible();

    // Resume pipeline with confirmation prompt
    let resumePrompted = false;
    page.once("dialog", async (dialog) => {
      resumePrompted = true;
      expect(dialog.message()).toMatch(/Resume the pipeline\?/i);
      await dialog.accept();
    });

    const resumeRes = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/pipeline-pause") &&
        r.request().method() === "DELETE",
    );
    await resumeBtn.click();
    expect(resumePrompted).toBe(true);
    expect((await resumeRes).status()).toBe(200);
    await expect(
      page.getByRole("button", { name: "Pause pipeline" }),
    ).toBeVisible();

    // Switch to Power tab in Admin overlay
    await page.getByRole("tab", { name: "Power" }).click();
    const restartBtn = page.getByRole("button", { name: "Restart grain" });
    if (await restartBtn.isVisible()) {
      // Stub the /api/host/restart endpoint to avoid killing the test runner server
      await page.route("**/api/host/restart", async (route) => {
        await route.fulfill({ status: 200, json: { ok: true } });
      });

      let restartConfirmed = false;
      page.once("dialog", async (dialog) => {
        restartConfirmed = true;
        expect(dialog.message()).toMatch(/Restart grain\?/i);
        await dialog.accept();
      });

      await restartBtn.click();
      expect(restartConfirmed).toBe(true);
      await expect(page.getByText(/Restart requested/i)).toBeVisible();
    }
  });

  test("4. Navigation Shell, Preferences, and Interactive Consoles: Theme radios, Timezone selector, Customize Pinned Nav, and Chat Composer", async ({
    page,
  }) => {
    // This browser's preferences: Settings → Preferences, behind the gear
    await page.goto("/");
    await page.getByTestId("top-bar-settings").click();
    await page.getByTestId("settings-nav-preferences").click();
    await expect(
      page.getByRole("heading", { name: "Settings", exact: true }),
    ).toBeVisible();

    // Timezone selector input (native select inside TextField)
    const tzSelect = page.locator('select[name="userTimeZone"]');
    await expect(tzSelect).toBeVisible();
    await tzSelect.selectOption("America/New_York");
    expect(
      await page.evaluate(() => localStorage.getItem("grain.timeZone")),
    ).toBe("America/New_York");

    // Appearance Theme radios: Light, Dark, Auto
    const lightRadio = page.getByRole("radio", { name: "Light", exact: true });
    const darkRadio = page.getByRole("radio", { name: "Dark", exact: true });
    const autoRadio = page.getByRole("radio", { name: "Auto", exact: true });
    await expect(lightRadio).toBeVisible();
    await expect(darkRadio).toBeVisible();
    await expect(autoRadio).toBeVisible();

    await darkRadio.click();
    expect(
      await page.evaluate(() => localStorage.getItem("grain.themeMode")),
    ).toBe("dark");
    await lightRadio.click();
    expect(
      await page.evaluate(() => localStorage.getItem("grain.themeMode")),
    ).toBe("light");

    // Customize sidebar button inside user settings
    const customizeNavBtn = page
      .getByTestId("settings-section")
      .getByRole("button", { name: "Customize sidebar" });
    await expect(customizeNavBtn).toBeVisible();
    await customizeNavBtn.click();

    // Verify Customize Pinned Navigation Overlay inputs
    await expect(
      page.getByRole("heading", { name: "Customize sidebar" }),
    ).toBeVisible();
    const addKindSelect = page.getByLabel("Type");
    await expect(addKindSelect).toBeVisible();

    // Save and close Customize Pinned Navigation
    await page.getByRole("button", { name: "Save" }).click();
    await page.keyboard.press("Escape"); // Close user Settings

    // Interactive Console Sessions / Standing session composer
    // Open standing Director session from sidebar or deep link
    await page.goto("/session/director");
    const composer = page.locator(".cs-message-input__content-editor");
    await expect(composer).toBeVisible();

    // Type text into composer
    await composer.fill("Hello Director from automated test session");
    await expect(composer).toHaveText(
      "Hello Director from automated test session",
    );

    // Verify Shift+Enter enters a newline rather than submitting
    await composer.press("Shift+Enter");
    await composer.type("Second line of input");
    await expect(composer).toContainText("Second line of input");

    // Verify + New session opens an empty chat with a Session name field
    // and a Pin to sidebar toggle (NewSessionPage.jsx): the name is the
    // session's title, and the first message creates the session under it.
    await page.goto("/sessions");
    const newSessionBtn = page
      .getByRole("button", { name: "+ New session" })
      .first();
    await expect(newSessionBtn).toBeVisible();
    await newSessionBtn.click();

    await expect(page).toHaveURL(/\/session\/new$/);
    await expect(page.getByRole("heading", { name: "New chat" })).toBeVisible();
    const sessionName = page.getByLabel("Session name");
    await expect(sessionName).toBeVisible();
    await expect(sessionName).toHaveValue("");
    const newComposer = page.getByRole("textbox", { name: "Message" });
    await expect(newComposer).toBeVisible();

    // Nothing to send or name: Send and Create session are disabled, and
    // nothing has been created.
    const sendBtn = page.getByRole("button", { name: "Send" });
    await expect(sendBtn).toBeDisabled();
    await expect(
      page.getByRole("button", { name: "Create session" }),
    ).toBeDisabled();

    await sessionName.fill("Automated ops session");
    await expect(
      page.getByRole("button", { name: "Create session" }),
    ).toBeEnabled();
    const pinToggle = page.getByLabel("Pin to sidebar");
    await pinToggle.check();

    // The first message starts the session.
    await newComposer.fill("Automated ops session: summarise the queue");
    await expect(sendBtn).toBeEnabled();

    const createSessionRes = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/console-sessions") &&
        r.request().method() === "POST",
    );
    await sendBtn.click();
    const createdRes = await createSessionRes;
    expect(createdRes.status()).toBe(201);
    expect(createdRes.request().postDataJSON().title).toBe(
      "Automated ops session",
    );
    // The session the message made is open, under the name typed, pinned
    // to the rail, with the message already in its conversation.
    await expect(page).toHaveURL(/\/session\/automated-ops-session/);
    const createdId = (await createdRes.json()).created;
    expect(
      await page.evaluate(() =>
        JSON.parse(localStorage.getItem("grain.pinned") || "[]"),
      ),
    ).toContainEqual({ kind: "session", id: createdId });
    await expect(
      page
        .locator(".cs-message")
        .filter({ hasText: "Automated ops session: summarise the queue" }),
    ).toBeVisible();
  });
});
