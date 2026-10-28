import { test as base, expect } from "@playwright/test";
import { execFileSync } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pageErrors } from "./pageerrors.js";
import { signInContext } from "./accesscode.js";
import {
  activeSandbox,
  eventually,
  startDaemon,
  taskByTitle,
  taskDetail,
} from "./realdaemon.js";

// This file drives its own daemon rather than the per-worker `grain demo`
// e2e/fixtures.js starts, so it takes Playwright's own `test` -- with the
// one fixture every spec here wants regardless of whose server it is
// talking to.
const test = base.extend(pageErrors);

// Section 3 of docs/test-plans/2026-09-13.md is deliberately not another
// component test. This boots the real daemon over its on-disk SQLite store and
// controller-owned config repository, drives its built frontend in Chromium,
// pushes through the live authenticated Git proxy, and restarts the process
// over the same data. The model provider is the one simulated boundary: the
// stub keeps the run's local e2e sandbox (a directory, see
// cmd/grain/local_sandboxes_e2e.go) alive while the test makes the reviewable
// edit in that task's checkout, then emits the stream shape Grain receives
// from agy. The built-in config check likewise runs through Grain CI, in the
// daemon itself, with nothing stubbed.
test.setTimeout(120_000);

test("takes grain/config through its native pull request and restart", async ({
  page,
}) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "grain-config-e2e-"));
  const dataDir = path.join(root, "data");
  const sandboxDir = path.join(root, "sandboxes");
  await fs.mkdir(dataDir);
  await fs.mkdir(sandboxDir);
  await fs.writeFile(path.join(root, "gemini-key"), "test-only-key\n", {
    mode: 0o600,
  });

  let daemon;
  try {
    daemon = await startDaemon(root, dataDir, sandboxDir, [
      "-backup-interval",
      "24h",
      "-backup-google-drive-folder",
      "backup-one",
    ]);
    await signInContext(page.context(), daemon.url);
    // Configure a real scheduled backup destination without waiting for its
    // 24-hour first tick. The restart below replaces Drive with GCS; neither
    // destination is allowed to become the repository agents clone.
    page.on("dialog", (dialog) => dialog.accept());

    // The repository list carries the one config repository there is,
    // grain/config, and no per-team grain/config-<team> beside it. It is
    // opened by its own address below, where its page cannot remove it.
    await page.goto(`${daemon.url}/repos`);
    await expect(
      page.getByText("grain/config", { exact: true }).first(),
    ).toBeVisible();
    await expect(page.getByText("grain/config-default")).toHaveCount(0);

    // The Clone URL field is offered only once "Other…" is picked from
    // the Add repo picker (or for a host no Git connection covers).
    const repo = page.getByRole("combobox", { name: "Repo to add" });
    const clone = page.getByPlaceholder("Clone URL (GitHub by default)");
    const pickOther = async () => {
      await repo.click();
      await page.getByRole("option", { name: "Other…" }).click();
    };
    await pickOther();
    await repo.fill("acme/widgets");
    await clone.fill("ssh://backup-one.example/acme/widgets.git");
    await page.getByRole("button", { name: "Add repo" }).click();
    await expect(page.getByText("acme/widgets", { exact: true })).toBeVisible();

    // Re-adding the same ordinary repo replaces its optional non-GitHub
    // checkout address rather than adding another row. The controller-owned
    // config repository remains a distinct, protected row.
    await pickOther();
    await repo.fill("acme/widgets");
    await clone.fill("ssh://backup-two.example/acme/widgets.git");
    await page.getByRole("button", { name: "Add repo" }).click();
    await expect(page.getByText("acme/widgets", { exact: true })).toHaveCount(
      1,
    );

    await page.goto(`${daemon.url}/repos/grain/config`);
    await expect(
      page.getByRole("heading", { name: "grain/config" }),
    ).toBeVisible();
    await expect(page.getByRole("button", { name: /^Remove/ })).toHaveCount(0);

    // Seed one settings row for the task to change, and force the same
    // state-manager export the daemon's timer uses before branching.
    const template = await page.request.post(`${daemon.url}/api/templates`, {
      data: {
        name: "Before native merge",
        title: "Config lifecycle fixture",
        description: "Changed through grain/config.",
        repo: "",
        base: "",
        autoMerge: false,
        networkAccess: "",
        capabilities: [],
        reads: [],
        writes: [],
        customFields: {},
      },
    });
    expect(template.status()).toBe(201);
    const synced = await page.request.post(`${daemon.url}/api/state-repo/sync`);
    expect(synced.ok()).toBeTruthy();

    const exportedRepoConfig = JSON.parse(
      await fs.readFile(
        path.join(dataDir, "state-repo", "tables", "repo_config.json"),
        "utf8",
      ),
    );
    expect(exportedRepoConfig).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          owner: "acme",
          name: "widgets",
          clone_url: "ssh://backup-two.example/acme/widgets.git",
        }),
      ]),
    );

    // A grain/config target is its write checkout and, necessarily, readable
    // by the run. Leave auto-merge off so the native PR has to be submitted
    // from its task page after the diff has been reviewed.
    await page.goto(daemon.url);
    await page.getByRole("button", { name: "+ New task" }).click();
    const title = `Native config lifecycle ${Date.now()}`;
    await page.getByLabel("Title").fill(title);
    await page.getByLabel(/^Repos/).selectOption("grain/config");
    await page.getByLabel("Auto-merge once checks pass").uncheck();
    await page.getByRole("button", { name: "Create task" }).click();

    const task = await eventually(
      () => taskByTitle(page.request, daemon.url, title),
      "the browser-created config task did not appear",
    );
    expect(task.repo).toBe("grain/config");
    const sandbox = await eventually(
      () => activeSandbox(sandboxDir),
      "the config task did not reach a live host sandbox",
    );
    const checkout = path.join(sandbox, "work");
    const gitEnv = { ...process.env, HOME: sandbox };

    // This is the authenticated address Grain installed for this live run.
    // Neither adding nor replacing the ordinary repo's optional remote may
    // move the intrinsic config checkout off its stable local alias.
    const origin = execFileSync(
      "git",
      ["-C", checkout, "remote", "get-url", "origin"],
      { env: gitEnv, encoding: "utf8" },
    ).trim();
    const alias = new URL(origin);
    expect(alias.protocol).toBe("http:");
    expect(alias.hostname).toBe("127.0.0.1");
    expect(alias.pathname).toBe("/grain/config.git");

    // Templates are exported as one nested document in the format a
    // repository writes its own declarations in, not as a file of rows
    // (pkg/staterepo/decl.go), so the edit a reviewer would make here is
    // a line of YAML.
    const templateFile = path.join(checkout, "settings", "templates.yml");
    const document = await fs.readFile(templateFile, "utf8");
    expect(document.match(/^ {2}- id:/gm)).toHaveLength(1);
    const named = document.match(/^ {4}name: (.+)$/m);
    expect(named, `no template name to rename in:\n${document}`).not.toBeNull();
    const previousName = named[1].replace(/^["']|["']$/g, "");
    await fs.writeFile(
      templateFile,
      document.replace(named[0], '    name: "After native merge"'),
    );

    execFileSync("git", ["-C", checkout, "add", "settings/templates.yml"], {
      env: gitEnv,
    });
    execFileSync(
      "git",
      [
        "-C",
        checkout,
        "-c",
        "user.name=grain config e2e",
        "-c",
        "user.email=grain-config-e2e@localhost",
        "commit",
        "-m",
        "Rename the lifecycle fixture",
        "-m",
        "Exercise the controller-native review and persistence path.",
      ],
      { env: gitEnv },
    );
    execFileSync("git", ["-C", checkout, "push", "origin", "HEAD"], {
      env: gitEnv,
    });
    await fs.writeFile(path.join(sandbox, "agent-finish"), "");

    const awaitingSubmit = await eventually(async () => {
      const detail = await taskDetail(page.request, daemon.url, task.id);
      return detail.pullRequest &&
        detail.state === "awaiting_submit" &&
        detail.ci?.conclusion === "success"
        ? detail
        : null;
    }, "the pushed config branch did not become a validated native pull request");
    expect(awaitingSubmit.ci.conclusion).toBe("success");

    // A freshly started daemon is a new deployment, so its task branches
    // carry its deployment id, then the slug of the task's title:
    // grain/<8 hex>/task-<id>-<slug> (grain/task-3091, #4031). The title's
    // trailing timestamp would take the slug past model.MaxBranchSlugLen,
    // so BranchSlug cuts at the word boundary before it.
    const branch = awaitingSubmit.branch;
    expect(branch).toMatch(
      new RegExp(`^grain/[0-9a-f]{8}/task-${task.id}-native-config-lifecycle$`),
    );

    // grain/config's branches and CI runs come from this controller, not
    // GitHub (grain/task-473): the branch pickers' listing names the
    // branch the run just pushed, the CI listing names the run that
    // validated it, and the repo page's CI tab shows that run.
    const listed = await page.request.get(
      `${daemon.url}/api/repos/grain/config/github-branches`,
    );
    expect(listed.status()).toBe(200);
    expect(await listed.json()).toEqual(
      expect.arrayContaining(["main", branch]),
    );
    const runs = await page.request.get(
      `${daemon.url}/api/ci/runs?repo=grain%2Fconfig`,
    );
    expect(runs.status()).toBe(200);
    const configRuns = (await runs.json()).runs ?? [];
    expect(configRuns.length).toBeGreaterThan(0);
    expect(configRuns.every((run) => run.repo === "grain/config")).toBe(true);
    // The repo page's own CI runs (RepoRuns) are on its Overview tab.
    await page.goto(`${daemon.url}/repos/grain/config`);
    await expect(
      page.getByRole("button", { name: /runs for this repo/ }),
    ).toBeVisible();
    expect(daemon.output()).not.toMatch(/repos\/grain\/config\/branches/);

    const comparison = `/repos/grain/config/compare/main...${branch}`;
    // The page draws the controller's own patch side by side now
    // (DiffView.jsx) rather than printing it, so what is asserted is what
    // a reviewer sees: the file the commit touched, and both sides of the
    // line it changed, the old one beside the new one.
    const nativeDiffShown = async () => {
      const changes = page.getByLabel("Branch changes");
      await expect(changes).toContainText("settings/templates.yml");
      await expect(changes).toContainText(previousName);
      await expect(changes).toContainText("After native merge");
    };

    await page.goto(`${daemon.url}${comparison}`);
    await expect(page.getByRole("heading", { name: branch })).toBeVisible();
    await nativeDiffShown();

    // A hard reload and a brand-new direct navigation both preserve the
    // comparison query and ask the real server for the same native diff.
    await page.reload();
    await nativeDiffShown();
    await page.goto(`${daemon.url}/`);
    await page.goto(`${daemon.url}${comparison}`);
    await nativeDiffShown();

    // Grain CI has already validated the config tree. The native forge adapter
    // intentionally reports an empty, settled check list, so Submit must merge
    // on the next reconciliation rather than waiting out the two-minute check
    // registration grace used for GitHub.
    await page.goto(`${daemon.url}/tasks/${task.id}`);
    await expect(page.getByRole("button", { name: "Submit" })).toBeVisible();
    const submittedAt = Date.now();
    await page.getByRole("button", { name: "Submit" }).click();

    await eventually(
      async () => {
        const detail = await taskDetail(page.request, daemon.url, task.id);
        return detail.state === "closed" ? detail : null;
      },
      "the submitted native pull request did not merge",
      20_000,
    );
    expect(Date.now() - submittedAt).toBeLessThan(20_000);

    await eventually(async () => {
      const response = await page.request.get(`${daemon.url}/api/templates`);
      const templates = await response.json();
      return templates.some((item) => item.name === "After native merge");
    }, "the merged configuration was not imported");

    // And the pull request still shows what it changed now that it has
    // landed (grain/task-375). The two branch tips no longer differ at
    // all -- main contains the branch, so their merge base is the
    // branch's own tip -- and this page used to go blank at exactly the
    // moment somebody reads back what a finished task did. The diff is
    // rebuilt from the merge commit instead, and the page says so.
    await page.goto(`${daemon.url}${comparison}`);
    await expect(
      page.getByRole("main").getByText("Merged", { exact: true }),
    ).toBeVisible();
    await nativeDiffShown();

    await daemon.stop();
    daemon = await startDaemon(root, dataDir, sandboxDir, [
      "-backup-interval",
      "24h",
      "-backup-gcs-bucket",
      "backup-two",
    ]);

    // Reopen through a fresh process and a fresh browser navigation: the
    // setting came from merged main, not from transient React or SQLite state.
    await page.goto(`${daemon.url}/templates`);
    await expect(
      page.getByText("After native merge", { exact: true }),
    ).toBeVisible();
    const mainFile = execFileSync(
      "git",
      [
        "-C",
        path.join(dataDir, "state-repo"),
        "show",
        "main:settings/templates.yml",
      ],
      { encoding: "utf8" },
    );
    expect(mainFile.match(/^ {2}- id:/gm)).toHaveLength(1);
    expect(mainFile).toContain("After native merge");

    // Replacing the scheduled backup destination changes where the combined
    // database/config archive will go, not the repository identity. A fresh
    // task after the restart must still receive this daemon's authenticated
    // grain/config alias as its writable origin.
    const existingSandboxes = new Set(await fs.readdir(sandboxDir));
    await page.goto(daemon.url);
    await page.getByRole("button", { name: "+ New task" }).click();
    const aliasTitle = `Alias after backup replacement ${Date.now()}`;
    await page.getByLabel("Title").fill(aliasTitle);
    await page.getByLabel(/^Repos/).selectOption("grain/config");
    await page.getByRole("button", { name: "Create task" }).click();
    await eventually(
      () => taskByTitle(page.request, daemon.url, aliasTitle),
      "the post-restart config task did not appear",
    );
    const replacementSandbox = await eventually(
      () => activeSandbox(sandboxDir, existingSandboxes),
      "the replacement backup changed the config checkout alias",
    );
    const replacementOrigin = execFileSync(
      "git",
      [
        "-C",
        path.join(replacementSandbox, "work"),
        "remote",
        "get-url",
        "origin",
      ],
      {
        env: { ...process.env, HOME: replacementSandbox },
        encoding: "utf8",
      },
    ).trim();
    const replacementAlias = new URL(replacementOrigin);
    expect(replacementAlias.protocol).toBe(alias.protocol);
    expect(replacementAlias.hostname).toBe(alias.hostname);
    expect(replacementAlias.pathname).toBe(alias.pathname);
    expect(replacementOrigin).not.toContain("backup-one");
    expect(replacementOrigin).not.toContain("backup-two");
    await fs.writeFile(path.join(replacementSandbox, "agent-finish"), "");
  } catch (err) {
    if (daemon) err.message += `\n\nDaemon output:\n${daemon.output()}`;
    throw err;
  } finally {
    if (daemon) await daemon.stop();
    await fs.rm(root, { recursive: true, force: true });
  }
});
