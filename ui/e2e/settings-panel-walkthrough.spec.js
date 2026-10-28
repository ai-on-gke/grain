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

// Walkthrough: the Settings view behind the gear, and System behind the
// icon beside it (docs/design/settings-panel.md, stage (a)), against the
// real `grain demo` server with none of grain's own APIs mocked. The
// run is recorded in docs/test-runs/2026-10-20-settings-panel.md.

async function openFromGear(page) {
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Tasks" })).toBeVisible();
  await page.getByTestId("top-bar-settings").click();
  await expect(
    page.getByRole("heading", { name: "General", level: 1 }),
  ).toBeVisible();
  await expect(page).toHaveURL(/\/settings\/general$/);
}

async function section(page, id, title) {
  await page.getByTestId(`settings-nav-${id}`).click();
  await expect(
    page.getByRole("heading", { name: title, level: 1 }),
  ).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`/settings/${id}$`));
}

// A single-line box commits on blur: Tab out of it and wait for the
// header to say the PUT came back.
async function commitBox(page, label, value) {
  const box = page.getByLabel(label, { exact: true });
  await box.fill(value);
  await box.press("Tab");
  await expect(page.getByTestId("settings-status")).toHaveText(
    "All changes saved",
  );
}

test("the gear opens Settings and the System icon opens Health", async ({
  page,
}) => {
  await openFromGear(page);
  await expect(page.getByTestId("top-bar-settings")).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  await page.getByTestId("top-bar-system").click();
  await expect(page.getByRole("tab", { name: "Health" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await expect(page.getByTestId("system-health")).toBeVisible();
  await expect(page).toHaveURL(/\/system$/);
  // The sidebar is flat and carries neither Memory nor System.
  await expect(page.getByTestId("team-section")).toHaveCount(0);
  await expect(page.getByRole("navigation", { name: "System" })).toHaveCount(0);
});

test("edits one setting in each section, and every one survives a reload", async ({
  page,
}) => {
  test.setTimeout(180_000);
  const initial = await (await page.request.get("/api/settings")).json();
  try {
    await openFromGear(page);

    // General
    await commitBox(page, "Environment name", "walkthrough");
    // Access: what this section has to show is how this browser is signed in.
    await section(page, "access", "Access");
    await expect(
      page.getByText("Signed in with the access code."),
    ).toBeVisible();
    // Agents: a number box, and the standing instructions on their Save.
    await section(page, "agents", "Agents");
    await commitBox(page, "Max agent turns", "40");
    const instructions = page.getByLabel("Standing instructions for every run");
    await instructions.fill("Use conventional commits.");
    await expect(page.getByTestId("settings-status")).toHaveText(
      "Unsaved changes in Standing instructions",
    );
    await page.getByTestId("promptExtension-save").click();
    await expect(page.getByTestId("settings-status")).toHaveText(
      "All changes saved",
    );
    // Memory
    await section(page, "memory", "Memory");
    await page
      .getByLabel("Memory about acme/widgets")
      .fill("Run make frontend before the e2e suite.");
    await page
      .getByRole("button", { name: "Save acme/widgets memory" })
      .click();
    await expect(page.getByText("edited here").first()).toBeVisible();
    // Tasks
    await section(page, "tasks", "Tasks");
    const depth = page.getByLabel("Max unreviewed follow-up depth");
    await depth.fill("4");
    await depth.press("Tab");
    // Repositories is an index: a row per repo, linking to its page.
    await section(page, "repositories", "Repositories");
    await expect(
      page.getByRole("link", { name: "acme/widgets" }).first(),
    ).toBeVisible();
    // Legacy capabilities
    await section(page, "credentials", "Legacy capabilities");
    await commitBox(page, "GCP project", "walkthrough-project");
    // Executors: a join token is a setting.
    await section(page, "executors", "Executors");
    await page.getByRole("button", { name: "Issue Token" }).click();
    await page.getByLabel("Token Name / Description").fill("walkthrough-token");
    await page.getByRole("button", { name: "Generate Token" }).click();
    await expect(page.getByText("walkthrough-token").first()).toBeVisible();
    await page.keyboard.press("Escape");
    // Engine, CI, Sandboxes, Caches
    await section(page, "engine", "Engine");
    await commitBox(page, "Poll interval", "45s");
    await section(page, "ci", "CI");
    await commitBox(page, "CI jobs per run", "3");
    await section(page, "sandboxes", "Sandboxes");
    await commitBox(page, "CPU overcommit", "1.5");
    await section(page, "caches", "Caches");
    const actionsCache = page.getByLabel("Actions cache max size (GiB)", {
      exact: true,
    });
    if (await actionsCache.count()) {
      await commitBox(page, "Actions cache max size (GiB)", "7");
    }
    // Preferences: this browser's own.
    await section(page, "preferences", "Preferences");
    await page.getByRole("radio", { name: "Dark" }).check();

    await page.reload();
    await expect(
      page.getByRole("heading", { name: "Preferences", level: 1 }),
    ).toBeVisible();
    await expect(page.getByRole("radio", { name: "Dark" })).toBeChecked();
    await page.goto("/settings/general");
    await expect(
      page.getByLabel("Environment name", { exact: true }),
    ).toHaveValue("walkthrough");
    await page.goto("/settings/agents");
    await expect(page.getByLabel("Max agent turns")).toHaveValue("40");
    await expect(
      page.getByLabel("Standing instructions for every run"),
    ).toHaveValue("Use conventional commits.");
    await page.goto("/settings/memory");
    await expect(page.getByLabel("Memory about acme/widgets")).toHaveValue(
      "Run make frontend before the e2e suite.",
    );
    await page.goto("/settings/tasks");
    await expect(page.getByLabel("Max unreviewed follow-up depth")).toHaveValue(
      "4",
    );
    await page.goto("/settings/credentials");
    await expect(page.getByLabel("GCP project", { exact: true })).toHaveValue(
      "walkthrough-project",
    );
    await page.goto("/settings/executors");
    await expect(page.getByText("walkthrough-token").first()).toBeVisible();
    await page.goto("/settings/engine");
    await expect(page.getByLabel("Poll interval", { exact: true })).toHaveValue(
      "45s",
    );
    await page.goto("/settings/ci");
    await expect(page.getByLabel("CI jobs per run")).toHaveValue("3");
    await page.goto("/settings/sandboxes");
    await expect(page.getByLabel("CPU overcommit")).toHaveValue("1.5");
  } finally {
    await page.request.put("/api/settings", {
      data: {
        environmentName: initial.environmentName ?? "",
        maxAgentTurns: initial.maxAgentTurns ?? 30,
        promptExtension: initial.promptExtension ?? "",
        maxAutoSubmitDepth: initial.maxAutoSubmitDepth || 2,
        gcpProject: initial.gcpProject ?? "",
        pollInterval: initial.pollInterval || "30s",
        ciMaxJobsPerRun: initial.ciMaxJobsPerRun || 4,
        sandboxCpuOvercommit: initial.sandboxCpuOvercommit || 2,
      },
    });
  }
});

test("every old path lands where §3.5 says, and Back skips it", async ({
  page,
}) => {
  test.setTimeout(300_000);
  for (const [old, now] of [
    ["/admin", "/settings/general"],
    ["/admin/general", "/settings/general"],
    ["/admin/agents", "/settings/agents"],
    ["/admin/models", "/settings/models"],
    ["/admin/git", "/settings/git"],
    ["/admin/workflow", "/settings/tasks"],
    ["/admin/access", "/settings/access"],
    ["/admin/accounts", "/settings/access"],
    ["/admin/engine", "/settings/engine"],
    ["/admin/sandbox", "/settings/sandboxes"],
    ["/admin/executors", "/settings/executors"],
    ["/admin/integrations", "/settings/credentials"],
    ["/admin/state", "/settings/config-repo"],
    ["/admin/upgrade", "/settings/upgrade"],
    ["/system/upgrade", "/settings/upgrade"],
    ["/admin/logs", "/system/logs"],
    ["/admin/sandboxes", "/system/sandboxes"],
    ["/admin/caches", "/system/caches"],
    ["/admin/top", "/system/top"],
    ["/admin/pipeline", "/system/pipeline"],
    ["/admin/power", "/system/power"],
    ["/admin/nonsense", "/settings/general"],
    ["/system/general", "/settings/general"],
    ["/system/agents", "/settings/agents"],
    ["/system/models", "/settings/models"],
    ["/system/git", "/settings/git"],
    ["/system/workflow", "/settings/tasks"],
    ["/system/access", "/settings/access"],
    ["/system/accounts", "/settings/access"],
    ["/system/engine", "/settings/engine"],
    ["/system/sandbox", "/settings/sandboxes"],
    ["/system/executors", "/settings/executors"],
    ["/system/integrations", "/settings/credentials"],
    ["/system/state", "/settings/config-repo"],
    ["/system/nonsense", "/system"],
    ["/settings", "/settings/general"],
    ["/memory", "/settings/memory"],
    ["/sandboxes", "/system/sandboxes"],
    ["/caches", "/system/caches"],
    ["/cache", "/system/caches"],
    ["/actions-cache", "/system/caches"],
    ["/metrics?window=30d", "/system/metrics?window=30d"],
  ]) {
    await page.goto("/");
    await expect(page.getByRole("heading", { name: "Tasks" })).toBeVisible();
    await page.goto(old);
    await expect
      .poll(() => new URL(page.url()).pathname + new URL(page.url()).search)
      .toBe(now);
    await page.goBack();
    await expect
      .poll(() => new URL(page.url()).pathname, { message: `Back from ${old}` })
      .toBe("/");
  }
});

test("filtering 'overcommit' finds CPU overcommit and focuses it", async ({
  page,
}) => {
  await openFromGear(page);
  await page.getByLabel("Filter settings").fill("overcommit");
  await page.getByRole("button", { name: "CPU overcommit, Sandboxes" }).click();
  await expect(
    page.getByRole("heading", { name: "Sandboxes", level: 1 }),
  ).toBeVisible();
  await expect(page.getByLabel("CPU overcommit")).toBeFocused();
  await expect(page).toHaveURL(/\/settings\/sandboxes#sandboxCpuOvercommit$/);
});

test("a toggle autosaves and survives a reload", async ({ page }) => {
  await openFromGear(page);
  const toggle = page.getByRole("checkbox", {
    name: /Show closed tasks by default/,
  });
  const was = await toggle.isChecked();
  await toggle.setChecked(!was);
  await expect(page.getByTestId("settings-status")).toHaveText(
    "All changes saved",
  );
  await page.reload();
  await expect(
    page.getByRole("checkbox", { name: /Show closed tasks by default/ }),
  ).toBeChecked({ checked: !was });
});

// A box the server refused, put back to what it holds, is not failing
// any more: the header stops saying so and the banner goes, without
// another request.
test("a refused box put back to its saved value reads saved again", async ({
  page,
}) => {
  await page.goto("/settings/engine");
  const box = page.getByLabel("Poll interval", { exact: true });
  const stored = await box.inputValue();
  const status = page.getByTestId("settings-status");
  await expect(status).toHaveText("All changes saved");

  await box.fill("banana");
  await box.press("Tab");
  await expect(status).toHaveText("Couldn't save Poll interval");
  const alert = page.getByRole("alert").filter({ hasText: "invalid duration" });
  await expect(alert).toBeVisible();

  let puts = 0;
  page.on("request", (req) => {
    if (req.method() === "PUT" && req.url().endsWith("/api/settings")) puts++;
  });
  await box.fill(stored);
  await box.press("Tab");
  await expect(status).toHaveText("All changes saved");
  await expect(page.getByRole("button", { name: "Retry" })).toHaveCount(0);
  await expect(alert).toHaveCount(0);
  expect(puts).toBe(0);
});

test("an edited standing instruction asks before leaving, and Discard clears it", async ({
  page,
}) => {
  await page.goto("/settings/agents");
  const box = page.getByLabel("Standing instructions for every run");
  const stored = await box.inputValue();
  await box.fill(`${stored} Lint first.`);
  await expect(page.getByTestId("settings-dirty-dot")).toBeVisible();

  // The guard has to ask: check its dialog came up, not just that the
  // page is still on Agents, which it is before any navigation lands too.
  // The click waits on an open dialog, so it is answered from a handler.
  let asked = "";
  page.once("dialog", (dialog) => {
    asked = dialog.type();
    return dialog.dismiss();
  });
  await page.getByTestId("settings-nav-engine").click();
  expect(asked).toBe("confirm");
  await expect(
    page.getByRole("heading", { name: "Agents", level: 1 }),
  ).toBeVisible();

  await page.getByTestId("promptExtension-discard").click();
  await expect(box).toHaveValue(stored);
  await expect(page.getByTestId("settings-status")).toHaveText(
    "All changes saved",
  );

  // Memory also marks the section dirty in the header and nav, and guards
  // both section switches and the top-bar System button.
  await page.getByTestId("settings-nav-memory").click();
  await expect(
    page.getByRole("heading", { name: "Memory", level: 1 }),
  ).toBeVisible();
  const memBox = page.getByLabel("Memory carried by every run");
  const memStored = await memBox.inputValue();
  await memBox.fill(`${memStored} Always check logs.`);
  await expect(page.getByTestId("settings-status")).toHaveText(
    "Unsaved changes in Memory",
  );
  await expect(page.getByTestId("settings-dirty-dot")).toBeVisible();

  let memAskedNav = "";
  page.once("dialog", (dialog) => {
    memAskedNav = dialog.type();
    return dialog.dismiss();
  });
  await page.getByTestId("settings-nav-engine").click();
  expect(memAskedNav).toBe("confirm");
  await expect(
    page.getByRole("heading", { name: "Memory", level: 1 }),
  ).toBeVisible();

  let memAskedSystem = "";
  page.once("dialog", (dialog) => {
    memAskedSystem = dialog.type();
    return dialog.dismiss();
  });
  await page.getByTestId("top-bar-system").click();
  expect(memAskedSystem).toBe("confirm");
  await expect(
    page.getByRole("heading", { name: "Memory", level: 1 }),
  ).toBeVisible();

  await page
    .getByRole("button", { name: "Revert this deployment's memory" })
    .click();
  await expect(page.getByTestId("settings-status")).toHaveText(
    "All changes saved",
  );
  await expect(page.getByTestId("settings-dirty-dot")).toHaveCount(0);

  await page.getByTestId("settings-nav-engine").click();
  await expect(
    page.getByRole("heading", { name: "Engine", level: 1 }),
  ).toBeVisible();
});

// §2.3: Repositories is an index -- a row per repository with its base
// branch, declarations, CI mode and Git connection -- and each fact is a
// link to the tab of the repo's own page that changes it, at that tab's
// path. Back returns to the index.
test("Repositories lists each repo's settings and links into its tab", async ({
  page,
}) => {
  await openFromGear(page);
  await section(page, "repositories", "Repositories");
  const row = page.locator(
    '[data-testid="settings-repo-row"][data-repo="acme/widgets"]',
  );
  await expect(row).toBeVisible();
  await expect(
    page.getByRole("table", { name: "Repositories" }).getByRole("columnheader"),
  ).toHaveText([
    "Repository",
    "Base branch",
    "Declarations",
    "CI mode",
    "Git connection",
  ]);
  const ci = row.getByTestId("settings-repo-ci");
  await expect(ci).toHaveText(/^(GitHub CI|Grain CI|No CI)$/);
  await expect(row.getByTestId("settings-repo-declarations")).toHaveText(
    /^(Read|Not read)$/,
  );
  await expect(row.getByTestId("settings-repo-base")).not.toHaveText("…");
  await expect(row.getByTestId("settings-repo-git")).not.toHaveText("…");
  // What the row says is what the repo's own page says.
  const defaults = await (
    await page.request.get("/api/repos/acme/widgets/declarations")
  ).json();
  await expect(row.getByTestId("settings-repo-declarations")).toHaveText(
    defaults.declarations ? "Read" : "Not read",
  );

  await ci.getByRole("link").click();
  await expect(page).toHaveURL(/\/repos\/acme\/widgets\/settings\/ci$/);
  await expect(
    page.getByRole("tab", { name: "CI", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await expect(page.locator("#repo-tabpanel-ci")).toBeVisible();
  // The tab's path is a link of its own: a reload stays on it.
  await page.reload();
  await expect(
    page.getByRole("tab", { name: "CI", exact: true }),
  ).toHaveAttribute("aria-selected", "true");
  await page.goBack();
  await expect(page).toHaveURL(/\/settings\/repositories$/);
  await expect(row).toBeVisible();

  // Declarations open the Advanced tab, where its switch is.
  await row.getByTestId("settings-repo-declarations").getByRole("link").click();
  await expect(page).toHaveURL(/\/repos\/acme\/widgets\/settings\/advanced$/);
  await expect(page.locator("#repo-tabpanel-advanced")).toBeVisible();
});

test("pinned Settings shortcut highlights on desktop and Back returns to Tasks", async ({
  page,
}) => {
  await page.goto("/");
  await page.evaluate(() => {
    localStorage.setItem(
      "grain.pinned",
      JSON.stringify([
        { kind: "view", id: "inbox" },
        { kind: "view", id: "settings" },
      ]),
    );
  });
  await page.reload();
  await expect(page.getByRole("heading", { name: "Tasks" })).toBeVisible();

  const pinnedSettings = page.locator(".MuiListItemButton-root", {
    hasText: "Settings",
  });
  await pinnedSettings.click();
  await expect(page).toHaveURL(/\/settings\/general$/);
  await expect(pinnedSettings).toHaveClass(/Mui-selected/);

  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.getByRole("heading", { name: "Tasks" })).toBeVisible();
});

test("Memory editor tracks dirty notes and documents, and cleans up unfinished model and git drafts", async ({
  page,
}) => {
  await page.request.put("/api/memory/documents", {
    data: {
      repo: "acme/widgets",
      name: "architecture",
      summary: "Widget architecture",
      text: "Initial architecture notes.",
    },
  });

  await page.goto("/settings/memory");
  const noteBox = page.getByLabel("Memory about acme/widgets");
  const initialNote = await noteBox.inputValue();
  await noteBox.fill(`${initialNote} Updated.`);
  await expect(page.getByTestId("settings-status")).toHaveText(
    "Unsaved changes in Memory",
  );
  await expect(page.getByTestId("settings-dirty-dot")).toBeVisible();
  await page
    .getByRole("button", { name: "Revert acme/widgets memory" })
    .click();
  await expect(page.getByTestId("settings-status")).toHaveText(
    "All changes saved",
  );
  await expect(page.getByTestId("settings-dirty-dot")).toHaveCount(0);

  await page
    .getByRole("button", { name: "Open architecture (acme/widgets)" })
    .click();
  const docBox = page.getByLabel("Document: architecture");
  await docBox.fill("Updated architecture notes.");
  await expect(page.getByTestId("settings-status")).toHaveText(
    "Unsaved changes in Memory",
  );
  await expect(page.getByTestId("settings-dirty-dot")).toBeVisible();
  await page
    .getByRole("button", { name: "Save document architecture (acme/widgets)" })
    .click();
  await expect(page.getByTestId("settings-status")).toHaveText(
    "All changes saved",
  );
  await expect(page.getByTestId("settings-dirty-dot")).toHaveCount(0);

  page.once("dialog", (d) => d.accept());
  await page
    .getByRole("button", { name: "Delete architecture (acme/widgets)" })
    .click();
  await expect(
    page.getByRole("button", { name: "Delete architecture (acme/widgets)" }),
  ).toHaveCount(0);

  // Leaving an unfinished new model connection via Back deletes it.
  await page.goto("/settings/general");
  await section(page, "models", "Models");
  const beforeBackends = (
    await (await page.request.get("/api/settings")).json()
  ).agentBackends.length;
  await page.getByRole("button", { name: "New connection" }).click();
  await page.getByLabel("Agent type").click();
  await page.getByRole("option", { name: /Claude/i }).click();
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByLabel("Authentication method")).toBeVisible();
  page.once("dialog", (d) => d.accept());
  await page.goBack();
  await expect(
    page.getByRole("heading", { name: "General", level: 1 }),
  ).toBeVisible();
  await expect
    .poll(
      async () =>
        (await (await page.request.get("/api/settings")).json()).agentBackends
          .length,
    )
    .toBe(beforeBackends);

  // Cancelling New git connection clears draft host and token inputs.
  await section(page, "git", "Git");
  await page.getByTestId("add-connection-button").click();
  await page.getByLabel("Host").fill("git.example.com");
  await page.getByLabel("Token (PAT)").fill("ghp_draft_token");
  await page.getByRole("button", { name: "Cancel" }).click();
  await page.getByTestId("add-connection-button").click();
  await expect(page.getByLabel("Host")).toHaveValue("");
  await expect(page.getByLabel("Token (PAT)")).toHaveValue("");
});

test.describe("on a phone", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  test("lists the sections, opens one, and goes back to the list", async ({
    page,
  }) => {
    await page.goto("/");
    await page.getByTestId("phone-settings").click();
    await expect(page.getByTestId("settings-nav")).toBeVisible();
    await expect(page).toHaveURL(/\/settings$/);
    await page.getByTestId("settings-nav-preferences").click();
    await expect(
      page.getByRole("heading", { name: "Preferences", level: 1 }),
    ).toBeVisible();
    await expect(page.getByTestId("settings-nav")).toHaveCount(0);
    await page.getByTestId("settings-back").click();
    await expect(page.getByTestId("settings-nav")).toBeVisible();
  });
});

test.describe("on a tablet", () => {
  test.use({ viewport: { width: 768, height: 1024 } });

  test("picks a section from the Section select", async ({ page }) => {
    await page.goto("/settings/general");
    await expect(page.getByTestId("settings-nav")).toHaveCount(0);
    await page.getByRole("combobox", { name: /^Section/ }).click();
    await page.getByRole("option", { name: "Engine" }).click();
    await expect(
      page.getByRole("heading", { name: "Engine", level: 1 }),
    ).toBeVisible();
    await expect(page).toHaveURL(/\/settings\/engine$/);
  });
});
