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

import { test as base, expect } from "@playwright/test";
import { execFileSync } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pageErrors } from "./pageerrors.js";
import { signInContext } from "./accesscode.js";
import { eventually, startDaemon } from "./realdaemon.js";

const test = base.extend(pageErrors);

test.setTimeout(180_000);

const VIEWPORTS = [
  { name: "desktop", width: 1280, height: 800 },
  { name: "phone", width: 390, height: 844 },
];

const REPO_MANAGED_SETTINGS_ROUTES = [
  "/settings/general",
  "/settings/models",
  "/settings/git",
  "/settings/repositories",
  "/settings/sandboxes",
  "/settings/caches",
  "/settings/ci",
];

test("switches between internal grain/config and external read-only IaC config repository modes", async ({
  page,
}) => {
  const bin = process.env.GRAIN_E2E_DEMO_BIN;
  expect(bin, "GRAIN_E2E_DEMO_BIN must be set by globalSetup").toBeTruthy();

  const root = await fs.mkdtemp(
    path.join(os.tmpdir(), "grain-config-iac-modes-e2e-"),
  );
  const dataDir = path.join(root, "data");
  const sandboxDir = path.join(root, "sandboxes");
  const externalBareRepo = path.join(root, "external-config.git");
  const externalWorkTree = path.join(root, "external-work");
  await fs.mkdir(dataDir);
  await fs.mkdir(sandboxDir);
  await fs.writeFile(path.join(root, "gemini-key"), "test-only-key\n", {
    mode: 0o600,
  });

  let daemon;
  try {
    daemon = await startDaemon(root, dataDir, sandboxDir);
    await signInContext(page.context(), daemon.url);
    page.on("dialog", (dialog) => dialog.accept());

    // Extract the live git proxy URL from daemon startup logs and register a
    // sandbox token so we can verify local grain/config Git hosting across mode transitions.
    const gitProxyUrl = await eventually(() => {
      const m = daemon
        .output()
        .match(/the git proxy is serving at (http:\/\/127\.0\.0\.1:\d+)/);
      return m ? m[1] : null;
    }, "git proxy URL did not appear in daemon output");

    const secretsDir = path.join(dataDir, "secrets");
    await fs.mkdir(secretsDir, { recursive: true });
    await fs.writeFile(
      path.join(secretsDir, "sandbox-tokens.json"),
      JSON.stringify({ "sandbox-e2e": "e2e-proxy-token" }),
      { mode: 0o600 },
    );
    const authedConfigGitUrl =
      gitProxyUrl.replace("http://", "http://x-access-token:e2e-proxy-token@") +
      "/grain/config.git";

    // Seed a repository (acme/widgets) and an initial template in internal mode.
    const addRepoResp = await page.request.put(
      `${daemon.url}/api/deployment/repos/acme/widgets/config`,
      { data: { maxConcurrent: 2 } },
    );
    expect(addRepoResp.ok()).toBeTruthy();

    const seedTemplateResp = await page.request.post(
      `${daemon.url}/api/templates`,
      {
        data: {
          name: "Internal Seed Template",
          title: "Seeded in internal mode",
          description: "Initial template exported to internal grain/config.",
          repo: "",
          base: "",
          autoMerge: false,
          networkAccess: "",
          capabilities: [],
          reads: [],
          writes: [],
          customFields: {},
        },
      },
    );
    expect(seedTemplateResp.status()).toBe(201);

    // -------------------------------------------------------------------------
    // Step 1: Internal mode baseline
    // -------------------------------------------------------------------------
    for (const vp of VIEWPORTS) {
      await page.setViewportSize({ width: vp.width, height: vp.height });
      await page.goto(`${daemon.url}/settings/config-repo`);
      await expect(
        page.getByRole("heading", { name: "Config repository", exact: true }),
      ).toBeVisible();
      await expect(
        page.locator(".MuiChip-root", { hasText: "grain/config" }),
      ).toBeVisible();
      await expect(
        page.getByRole("button", { name: "Export now" }),
      ).toBeVisible();
      await expect(
        page.getByRole("button", { name: "Use external config repository" }),
      ).toBeVisible();
      await expect(page.getByTestId("external-config-repo-banner")).toHaveCount(
        0,
      );
    }

    // Trigger an export via the UI button "Export now".
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.getByRole("button", { name: "Export now" }).click();

    const internalStatusResp = await page.request.get(
      `${daemon.url}/api/state-repo`,
    );
    expect(internalStatusResp.ok()).toBeTruthy();
    const internalStatus = await internalStatusResp.json();
    expect(internalStatus.available).toBe(true);
    expect(internalStatus.mode).toBe("internal");
    expect(internalStatus.repository).toBe("grain/config");
    expect(internalStatus.remote).toBeFalsy();
    expect(internalStatus.head).toMatch(/^[0-9a-f]{40}$/);

    // Verify CLI commands `grain state status` and `grain state repo` in Internal mode.
    const cliStatusInternal = execFileSync(
      bin,
      ["state", "-data-dir", dataDir, "status"],
      { encoding: "utf8" },
    );
    expect(cliStatusInternal).toContain(
      "repository: grain/config (controller local)",
    );
    expect(cliStatusInternal).toContain(
      "mode:       internal -- grain/config is hosted on this host.",
    );
    expect(cliStatusInternal).toContain("branch:     main");

    const cliRepoInternal = execFileSync(
      bin,
      ["state", "-data-dir", dataDir, "repo"],
      { encoding: "utf8" },
    );
    expect(cliRepoInternal).toContain(
      "mode:       internal -- grain/config is hosted on this host.",
    );

    // Create a topic branch in internal state-repo so we can verify native branch comparison
    // and local Git proxy access in internal vs. external mode.
    const internalRepoDir = path.join(dataDir, "state-repo");
    execFileSync("git", [
      "-C",
      internalRepoDir,
      "branch",
      "topic/internal-check",
      "HEAD",
    ]);
    const compareInternalResp = await page.request.get(
      `${daemon.url}/api/repos/grain/config/compare?base=main&head=topic/internal-check`,
    );
    expect(compareInternalResp.status()).toBe(200);

    // Seed the external bare Git repository from the initial internal grain/config export.
    execFileSync("git", [
      "init",
      "--bare",
      "--initial-branch=main",
      externalBareRepo,
    ]);
    execFileSync("git", ["clone", internalRepoDir, externalWorkTree]);
    execFileSync("git", [
      "-C",
      externalWorkTree,
      "config",
      "user.name",
      "iac-operator",
    ]);
    execFileSync("git", [
      "-C",
      externalWorkTree,
      "config",
      "user.email",
      "iac-operator@example.com",
    ]);
    // Remove internal workflow file before pushing to external bare repo.
    await fs.rm(
      path.join(
        externalWorkTree,
        ".github",
        "workflows",
        "grain-state-check.yml",
      ),
      { force: true },
    );
    execFileSync("git", [
      "-C",
      externalWorkTree,
      "remote",
      "set-url",
      "origin",
      externalBareRepo,
    ]);
    execFileSync("git", [
      "-C",
      externalWorkTree,
      "push",
      "-u",
      "origin",
      "main",
    ]);
    const initialExternalSha = execFileSync(
      "git",
      ["--git-dir", externalBareRepo, "rev-parse", "refs/heads/main"],
      { encoding: "utf8" },
    ).trim();

    // -------------------------------------------------------------------------
    // Step 2: Switch to External IaC mode
    // -------------------------------------------------------------------------
    await page.goto(`${daemon.url}/settings/config-repo`);
    await page
      .getByRole("textbox", { name: "Repository URL", exact: true })
      .fill(externalBareRepo);
    await page
      .getByRole("button", { name: "Use external config repository" })
      .click();

    await expect(page.getByRole("button", { name: "Pull now" })).toBeVisible();
    await expect(
      page.getByRole("button", {
        name: "Switch to internal config repository",
      }),
    ).toBeVisible();
    await expect(
      page.getByText("External repository (Infrastructure as Code):").first(),
    ).toBeVisible();

    // Also verify `/settings/config-repo` at phone viewport (390x844).
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(page.getByRole("button", { name: "Pull now" })).toBeVisible();
    await expect(
      page.getByRole("button", {
        name: "Switch to internal config repository",
      }),
    ).toBeVisible();

    const externalStatusResp = await page.request.get(
      `${daemon.url}/api/state-repo`,
    );
    expect(externalStatusResp.ok()).toBeTruthy();
    const externalStatus = await externalStatusResp.json();
    expect(externalStatus.mode).toBe("external");
    expect(externalStatus.remote).toBe(externalBareRepo);
    expect(externalStatus.branch).toBe("main");
    expect(externalStatus.head).toBe(initialExternalSha);

    const cliStatusExternal = execFileSync(
      bin,
      ["state", "-data-dir", dataDir, "status"],
      { encoding: "utf8" },
    );
    expect(cliStatusExternal).toContain(
      `repository: ${externalBareRepo} (external config repository, read-only IaC mode)`,
    );
    expect(cliStatusExternal).toContain(
      `mode:       external (read-only IaC) -- ${externalBareRepo} (branch main)`,
    );

    const cliRepoExternal = execFileSync(
      bin,
      ["state", "-data-dir", dataDir, "repo"],
      { encoding: "utf8" },
    );
    expect(cliRepoExternal).toContain(
      `mode:       external (read-only IaC) -- ${externalBareRepo} (branch main)`,
    );

    // -------------------------------------------------------------------------
    // Step 3: Read-only UI and mutable exceptions
    // -------------------------------------------------------------------------
    for (const vp of VIEWPORTS) {
      await page.setViewportSize({ width: vp.width, height: vp.height });
      for (const route of REPO_MANAGED_SETTINGS_ROUTES) {
        await page.goto(`${daemon.url}${route}`);
        const banner = page.getByTestId("external-config-repo-banner");
        await expect(banner, `${route} at ${vp.name}`).toBeVisible();
        await expect(banner).toContainText(
          "Configuration is read-only and managed in",
        );
        await expect(banner).toContainText(externalBareRepo);
        // Repo-managed cards wrap their controls in a disabled fieldset.
        await expect(
          page
            .locator("section[data-settings-card] fieldset[disabled]")
            .first(),
          `expected disabled fieldset on ${route} at ${vp.name}`,
        ).toBeAttached();
      }

      // Mutable exception 1: /settings/preferences remains interactive and has no banner.
      await page.goto(`${daemon.url}/settings/preferences`);
      await expect(page.getByTestId("external-config-repo-banner")).toHaveCount(
        0,
      );
      await expect(
        page.locator("section[data-settings-card] fieldset[disabled]"),
      ).toHaveCount(0);

      // Mutable exception 2: /settings/config-repo remains interactive and has no banner.
      await page.goto(`${daemon.url}/settings/config-repo`);
      await expect(page.getByTestId("external-config-repo-banner")).toHaveCount(
        0,
      );
      await expect(
        page.getByRole("button", { name: "Pull now" }),
      ).toBeEnabled();
      await expect(
        page.getByRole("button", {
          name: "Switch to internal config repository",
        }),
      ).toBeEnabled();

      // Mutable exception 3: /settings/credentials (CredentialsSection) keeps
      // repo-managed host/GCP inputs disabled while SecretsPanel remains interactive.
      await page.goto(`${daemon.url}/settings/credentials`);
      await expect(
        page.getByRole("textbox", { name: "GitHub host" }),
      ).toBeDisabled();
      const setSecretBtn = page.getByRole("button", { name: "Set secret" });
      await expect(setSecretBtn).toBeVisible();
      await expect(setSecretBtn).toBeEnabled();
    }

    // Verify storing an encrypted secret in CredentialsSection works while in External mode.
    await page.setViewportSize({ width: 1280, height: 800 });
    await page.goto(`${daemon.url}/settings/credentials`);
    await page
      .getByRole("textbox", { name: "Secret", exact: true })
      .fill("iac-mode-secret");
    await page
      .getByRole("textbox", { name: "Key", exact: true })
      .fill("api-token");
    await page.getByLabel("Value", { exact: true }).fill("super-secret-value");
    await page.getByRole("button", { name: "Set secret" }).click();
    await expect(
      page.locator(".secret-name", { hasText: "iac-mode-secret" }),
    ).toBeVisible();

    // -------------------------------------------------------------------------
    // Step 4: API mutator refusals & local `grain/config` hosting disabled
    // -------------------------------------------------------------------------
    const mutatorRequests = [
      {
        label: "PUT /api/settings",
        run: () =>
          page.request.put(`${daemon.url}/api/settings`, {
            data: { environmentName: "blocked" },
          }),
      },
      {
        label: "POST /api/templates",
        run: () =>
          page.request.post(`${daemon.url}/api/templates`, {
            data: { name: "blocked-tmpl", title: "Blocked" },
          }),
      },
      {
        label: "POST /api/schedules",
        run: () =>
          page.request.post(`${daemon.url}/api/schedules`, {
            data: {
              name: "blocked-sched",
              title: "Blocked",
              body: "Blocked",
              recurrence: { kind: "every_n_hours", everyNHours: 1 },
            },
          }),
      },
      {
        label: "DELETE /api/schedules/sched-1",
        run: () => page.request.delete(`${daemon.url}/api/schedules/sched-1`),
      },
      {
        label: "POST /api/suites",
        run: () =>
          page.request.post(`${daemon.url}/api/suites`, {
            data: {
              name: "blocked-suite",
              templateIds: ["template-1"],
              mode: "count",
              count: 1,
            },
          }),
      },
      {
        label: "POST /api/boards",
        run: () =>
          page.request.post(`${daemon.url}/api/boards`, {
            data: { name: "blocked-board" },
          }),
      },
      {
        label: "POST /api/console-sessions",
        run: () =>
          page.request.post(`${daemon.url}/api/console-sessions`, {
            data: { title: "Blocked Session" },
          }),
      },
      {
        label: "PUT /api/deployment/repos/acme/widgets/config",
        run: () =>
          page.request.put(
            `${daemon.url}/api/deployment/repos/acme/widgets/config`,
            { data: { maxConcurrent: 5 } },
          ),
      },
      {
        label: "PUT /api/repos/acme/widgets/settings",
        run: () =>
          page.request.put(`${daemon.url}/api/repos/acme/widgets/settings`, {
            data: { maxConcurrent: 5 },
          }),
      },
      {
        label: "PUT /api/deployment/agent-backends/claude",
        run: () =>
          page.request.put(
            `${daemon.url}/api/deployment/agent-backends/claude`,
            { data: { type: "claude-cli", label: "Claude" } },
          ),
      },
      {
        label: "POST /api/deployment/git-connections",
        run: () =>
          page.request.post(`${daemon.url}/api/deployment/git-connections`, {
            data: { host: "github.com", type: "pat", token: "ghp_blocked" },
          }),
      },
    ];

    for (const req of mutatorRequests) {
      const resp = await req.run();
      expect(resp.status(), `${req.label} should return 409 Conflict`).toBe(
        409,
      );
      const body = await resp.json();
      expect(body.error, `${req.label} error payload`).toContain("read-only");
      expect(body.error, `${req.label} error payload`).toContain(
        externalBareRepo,
      );
    }

    // Verify local `grain/config` Git proxy hosting is disabled in External mode.
    let gitProxyErr = "";
    try {
      execFileSync("git", ["ls-remote", authedConfigGitUrl], {
        env: { ...process.env, GIT_TERMINAL_PROMPT: "0" },
        encoding: "utf8",
        stdio: ["ignore", "pipe", "pipe"],
      });
    } catch (err) {
      gitProxyErr = `${err.stdout || ""}${err.stderr || ""}`;
    }
    expect(gitProxyErr).toContain(
      "local config repository is disabled when an external config repository is configured",
    );

    // Verify native `grain/config` branch comparison endpoint is refused in External mode.
    const compareExternalResp = await page.request.get(
      `${daemon.url}/api/repos/grain/config/compare?base=main&head=topic/internal-check`,
    );
    expect(compareExternalResp.status()).toBe(502);
    const compareExternalBody = await compareExternalResp.json();
    expect(compareExternalBody.code).toBe("upstream_unavailable");
    expect(daemon.output()).toContain(
      "native comparison is not available for grain/config",
    );

    // -------------------------------------------------------------------------
    // Step 5: Pulling external IaC changes without daemon export commits
    // -------------------------------------------------------------------------
    // Commit Change 1 to external Git repository: add a template in settings/templates.yml
    // and update max_concurrent in tables/grain_config.json.
    const templatesYmlPath = path.join(
      externalWorkTree,
      "settings",
      "templates.yml",
    );
    const currentTemplatesYml = await fs.readFile(templatesYmlPath, "utf8");
    const seedIdx = currentTemplatesYml.indexOf("  - id: template-1");
    expect(seedIdx).toBeGreaterThan(-1);
    const seedEntry = currentTemplatesYml.slice(seedIdx);
    const entry1 = seedEntry
      .replace("id: template-1", "id: template-iac-1")
      .replace("name: Internal Seed Template", "name: External IaC Template v1")
      .replace("title: Seeded in internal mode", "title: External IaC Task v1");
    const updatedTemplatesYml1 =
      currentTemplatesYml.trimEnd() + "\n" + entry1.trimEnd() + "\n";
    await fs.writeFile(templatesYmlPath, updatedTemplatesYml1);

    const grainConfigJsonPath = path.join(
      externalWorkTree,
      "tables",
      "grain_config.json",
    );
    const grainConfigRows1 = JSON.parse(
      await fs.readFile(grainConfigJsonPath, "utf8"),
    );
    grainConfigRows1[0].environment_name = "iac-prod-v1";
    await fs.writeFile(
      grainConfigJsonPath,
      JSON.stringify(grainConfigRows1, null, 2) + "\n",
    );

    execFileSync("git", ["-C", externalWorkTree, "add", "-A"]);
    execFileSync("git", [
      "-C",
      externalWorkTree,
      "commit",
      "-m",
      "Add External IaC Template v1 and set environment_name=iac-prod-v1",
    ]);
    execFileSync("git", ["-C", externalWorkTree, "push", "origin", "main"]);
    const change1Sha = execFileSync(
      "git",
      ["--git-dir", externalBareRepo, "rev-parse", "refs/heads/main"],
      { encoding: "utf8" },
    ).trim();

    // Click "Pull now" in /settings/config-repo.
    await page.goto(`${daemon.url}/settings/config-repo`);
    await page.getByRole("button", { name: "Pull now" }).click();

    await eventually(async () => {
      const st = await (
        await page.request.get(`${daemon.url}/api/state-repo`)
      ).json();
      return st.head === change1Sha ? st : null;
    }, "daemon did not advance state-repo head to change1Sha after Pull now");

    const templatesAfterPull1 = await (
      await page.request.get(`${daemon.url}/api/templates`)
    ).json();
    expect(
      templatesAfterPull1.some((t) => t.name === "External IaC Template v1"),
    ).toBe(true);

    const settingsAfterPull1 = await (
      await page.request.get(`${daemon.url}/api/settings`)
    ).json();
    expect(settingsAfterPull1.environmentName).toBe("iac-prod-v1");

    // Verify in UI at /processes that the pulled template is rendered.
    await page.goto(`${daemon.url}/processes`);
    await expect(
      page.getByText("External IaC Template v1").first(),
    ).toBeVisible();

    // Commit Change 2 to external Git repository and sync via `grain state sync` CLI.
    const entry2 = seedEntry
      .replace("id: template-1", "id: template-iac-2")
      .replace("name: Internal Seed Template", "name: CLI Synced IaC Template")
      .replace("title: Seeded in internal mode", "title: CLI Synced IaC Task");
    const updatedTemplatesYml2 =
      updatedTemplatesYml1.trimEnd() + "\n" + entry2.trimEnd() + "\n";
    await fs.writeFile(templatesYmlPath, updatedTemplatesYml2);
    grainConfigRows1[0].environment_name = "iac-prod-v2";
    await fs.writeFile(
      grainConfigJsonPath,
      JSON.stringify(grainConfigRows1, null, 2) + "\n",
    );
    execFileSync("git", ["-C", externalWorkTree, "add", "-A"]);
    execFileSync("git", [
      "-C",
      externalWorkTree,
      "commit",
      "-m",
      "Add CLI Synced IaC Template and set environment_name=iac-prod-v2",
    ]);
    execFileSync("git", ["-C", externalWorkTree, "push", "origin", "main"]);
    const change2Sha = execFileSync(
      "git",
      ["--git-dir", externalBareRepo, "rev-parse", "refs/heads/main"],
      { encoding: "utf8" },
    ).trim();

    const cliSyncOut = execFileSync(
      bin,
      ["state", "-data-dir", dataDir, "sync"],
      { encoding: "utf8" },
    );
    expect(cliSyncOut).toContain(
      `applied ${change2Sha} from ${externalBareRepo}`,
    );

    const templatesAfterPull2 = await (
      await page.request.get(`${daemon.url}/api/templates`)
    ).json();
    expect(
      templatesAfterPull2.some((t) => t.name === "CLI Synced IaC Template"),
    ).toBe(true);
    const settingsAfterPull2 = await (
      await page.request.get(`${daemon.url}/api/settings`)
    ).json();
    expect(settingsAfterPull2.environmentName).toBe("iac-prod-v2");

    // Verify `git log` on the external bare repository contains ONLY the operator's commits
    // since switching to External mode, and zero daemon-generated export commits.
    const externalLogSinceSwitch = execFileSync(
      "git",
      [
        "--git-dir",
        externalBareRepo,
        "log",
        "--format=%s",
        `${initialExternalSha}..refs/heads/main`,
      ],
      { encoding: "utf8" },
    )
      .trim()
      .split("\n");
    expect(externalLogSinceSwitch).toEqual([
      "Add CLI Synced IaC Template and set environment_name=iac-prod-v2",
      "Add External IaC Template v1 and set environment_name=iac-prod-v1",
    ]);

    // -------------------------------------------------------------------------
    // Step 6: Switch back to Internal mode
    // -------------------------------------------------------------------------
    await page.goto(`${daemon.url}/settings/config-repo`);
    await page
      .getByRole("button", {
        name: "Switch to internal config repository",
      })
      .click();

    await expect(
      page.getByRole("button", { name: "Export now" }),
    ).toBeVisible();
    await expect(
      page.locator(".MuiChip-root", { hasText: "grain/config" }),
    ).toBeVisible();

    // Verify CLI `grain state repo` switching to external and back via `-none`.
    const cliSetExt = execFileSync(
      bin,
      ["state", "-data-dir", dataDir, "repo", externalBareRepo],
      { encoding: "utf8" },
    );
    expect(cliSetExt).toContain("read-only IaC");

    const cliSetNone = execFileSync(
      bin,
      ["state", "-data-dir", dataDir, "repo", "-none"],
      { encoding: "utf8" },
    );
    expect(cliSetNone).toContain(
      `switched from external config repository ${externalBareRepo} to the internal grain/config repository.`,
    );

    // Confirm banner disappears on /settings/general and inputs are editable again.
    await page.goto(`${daemon.url}/settings/general`);
    await expect(
      page.locator("section[data-settings-card]").first(),
    ).toBeAttached();
    await expect(page.getByTestId("external-config-repo-banner")).toHaveCount(
      0,
    );
    await expect(
      page.locator("section[data-settings-card] fieldset[disabled]"),
    ).toHaveCount(0);

    // Confirm PUT /api/settings succeeds again in Internal mode and
    // commits changes to the internal `grain/config` repository.
    const headBeforeExport = execFileSync(
      "git",
      ["-C", internalRepoDir, "rev-parse", "HEAD"],
      { encoding: "utf8" },
    ).trim();
    const putSettingsInternalResp = await page.request.put(
      `${daemon.url}/api/settings`,
      { data: { environmentName: "internal-restored" } },
    );
    expect(putSettingsInternalResp.status()).toBe(200);
    const updatedSettingsInternal = await putSettingsInternalResp.json();
    expect(updatedSettingsInternal.environmentName).toBe("internal-restored");

    await page.goto(`${daemon.url}/settings/config-repo`);
    await page.getByRole("button", { name: "Export now" }).click();

    await eventually(() => {
      const headAfter = execFileSync(
        "git",
        ["-C", internalRepoDir, "rev-parse", "HEAD"],
        { encoding: "utf8" },
      ).trim();
      return headAfter !== headBeforeExport ? headAfter : null;
    }, "Export now did not create a new commit in internal state-repo");

    const exportedGrainConfig = JSON.parse(
      await fs.readFile(
        path.join(internalRepoDir, "tables", "grain_config.json"),
        "utf8",
      ),
    );
    expect(exportedGrainConfig[0].environment_name).toBe("internal-restored");

    // Confirm local `grain/config` Git hosting works again via local Git proxy
    // (using a task scoped to grain/config, or verifying that the disabled 404 is gone).
    let gitProxyInternalOut = "";
    try {
      gitProxyInternalOut = execFileSync(
        "git",
        ["ls-remote", authedConfigGitUrl],
        {
          env: { ...process.env, GIT_TERMINAL_PROMPT: "0" },
          encoding: "utf8",
          stdio: ["ignore", "pipe", "pipe"],
        },
      );
    } catch (err) {
      gitProxyInternalOut = `${err.stdout || ""}${err.stderr || ""}`;
    }
    expect(gitProxyInternalOut).not.toContain(
      "local config repository is disabled when an external config repository is configured",
    );
    expect(gitProxyInternalOut).toContain("is not in scope for this sandbox");

    // Re-create the topic branch in the internal repo and verify native compare works again.
    execFileSync("git", [
      "-C",
      internalRepoDir,
      "branch",
      "-f",
      "topic/internal-check",
      "HEAD",
    ]);
    const compareBackInternalResp = await page.request.get(
      `${daemon.url}/api/repos/grain/config/compare?base=main&head=topic/internal-check`,
    );
    expect(compareBackInternalResp.status()).toBe(200);
  } finally {
    await daemon?.stop();
    await fs.rm(root, { recursive: true, force: true });
  }
});
