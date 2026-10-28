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

import { spawn } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { startGitHubStub } from "./githubstub.js";
import { signIn, writeAccessCode } from "./accesscode.js";

// §4 of docs/test-plans/2026-10-05.md: a GitHub credential decides what
// tasks can reach (grain/task-1995). Since stage 1 of the single-tenant
// reset (docs/single-tenant-reset.md) that credential is the deployment's
// git connection, held on the default team's row, and since stage 3e-ii
// it is tested and listed from Settings > Git connections, which the walk
// checks agree with what filing a task checks. Its own demo, started with
// -github-api pointed at githubstub.js -- the one boundary stubbed -- so
// the Test buttons, GET /api/deployment/reachable-repos, the New task
// pickers and CreateTask's refusal all ask the same fake GitHub, and the
// walk can change what a token reaches between steps.
test.setTimeout(180_000);

const PUSH = "ghp_walkPushApp7Qx2Lm4Np6Rt8";
const NOREACH = "ghp_walkNoReach9Zk1Yj3Wh5";
const SSO = "ghp_walkSso4Rt8Uv0Xa2Cd4";
const LIMITED = "ghp_walkRateLimited2Mn5Bq7";
const BAD = "ghp_walkRevoked0000Ff1Gh3";
const TOKENS = [PUSH, NOREACH, SSO, LIMITED, BAD];

// What PUSH reaches, as the per-repo row of the agreement table.
const REPOS = {
  "acme/app": "push",
  "acme/widgets": "push",
  "acme/lib": "read",
  "acme/docs": "read",
  "acme/private": "none",
};

async function startDemo(githubHostPort) {
  const dir = mkdtempSync(path.join(tmpdir(), "grain-reach-"));
  const codeFile = path.join(dir, "access-code");
  await writeAccessCode(codeFile);
  const proc = spawn(
    process.env.GRAIN_E2E_DEMO_BIN,
    [
      "demo",
      "-addr",
      "127.0.0.1:0",
      "-open=false",
      "-github-api",
      githubHostPort,
      "-access-code-file",
      codeFile,
    ],
    { stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, TMPDIR: dir } },
  );
  let output = "";
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error(`grain demo did not start:\n${output}`)),
      60_000,
    );
    const read = (chunk) => {
      output += chunk;
      const at = output.match(/at http:\/\/127\.0\.0\.1:(\d+)/);
      if (at) {
        clearTimeout(timer);
        resolve(`http://127.0.0.1:${at[1]}`);
      }
    };
    proc.stdout.on("data", read);
    proc.stderr.on("data", read);
    proc.on("exit", (code) => {
      clearTimeout(timer);
      reject(new Error(`grain demo exited ${code}:\n${output}`));
    });
  });
  return {
    url,
    stop: () => {
      proc.kill();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

test.describe("the deployment's GitHub credential reach (§4)", () => {
  let stub;
  let demo;

  test.beforeAll(async () => {
    stub = await startGitHubStub({
      // The demo's own deployment credential, which its default team
      // keeps: it reaches the seeded repo.
      ghp_demo_secret_token_acme_widgets: {
        repos: { "acme/widgets": "push" },
      },
      [PUSH]: {
        login: "octo-dev",
        repos: Object.fromEntries(
          Object.entries(REPOS).filter(([, a]) => a !== "none"),
        ),
      },
      [NOREACH]: { login: "octo-limited", repos: { "acme/lib": "read" } },
      [SSO]: { sso: true },
      [LIMITED]: { rateLimited: true },
    });
    demo = await startDemo(stub.hostPort);
  });

  test.afterAll(async () => {
    demo?.stop();
    await stub?.close();
  });

  test("Test buttons, agreement, pickers and parking", async ({
    browser,
    playwright,
  }) => {
    // The demo keeps no accounts: the deployment's owner is whoever shows
    // its access code (accesscode.js).
    const session = await signIn(demo.url);
    const admin = await playwright.request.newContext({
      baseURL: demo.url,
      storageState: session,
    });

    // The deployment's connections are its default team's (stage 1 of
    // the reset): the demo's own token, set aside so the walk starts with
    // none and adds its own through Settings.
    const clearDeployment = async () => {
      const current = await (await admin.get("/api/deployment")).json();
      for (const c of current.gitConnections || []) {
        const removed = await admin.delete(
          `/api/deployment/git-connections/${encodeURIComponent(c.credential)}`,
        );
        expect(removed.status(), await removed.text()).toBe(200);
      }
    };
    await clearDeployment();
    // And no repos attached, so the Test and the listing say everything
    // the token reaches rather than only the demo's seeded target.
    const unrestricted = await admin.put("/api/deployment", {
      data: { targetRepos: [] },
    });
    expect(unrestricted.status(), await unrestricted.text()).toBe(200);

    // Every response the browser sees, kept to assert no token is in one.
    const bodies = [];
    const page = await browser.newPage({
      baseURL: demo.url,
      storageState: session,
    });
    page.on("response", async (res) => {
      if (!res.url().includes("/api/")) return;
      try {
        bodies.push(`${res.url()} ${await res.text()}`);
      } catch {
        // A response with no body to read (a redirect, a 204).
      }
    });
    const noTokenAnywhere = async () => {
      const html = await page.content();
      for (const t of TOKENS) {
        expect(html, `DOM carries ${t.slice(0, 8)}…`).not.toContain(t);
        for (const b of bodies) {
          expect(b, `a response carries ${t.slice(0, 8)}…`).not.toContain(t);
        }
      }
    };

    // ---- 1. Test buttons, as the deployment's admin ----
    // The deployment's git access is its connections (grain/task-3929): a token
    // is tried before it is saved through the connections check, then
    // saved as a connection and tested from its row.
    const tryUnsaved = async (token) => {
      const res = await admin.post("/api/deployment/git-connections/check", {
        data: { host: "github.com", type: "pat", value: token },
      });
      const text = await res.text();
      expect(res.status(), text).toBe(200);
      expect(text).not.toContain(token);
      return JSON.parse(text);
    };

    // A bad (revoked) token, unsaved.
    expect((await tryUnsaved(BAD)).error).toContain(
      "GitHub rejected the token (bad credentials): it is wrong, revoked or expired",
    );
    // SSO-blocked.
    expect((await tryUnsaved(SSO)).error).toContain(
      "has not been authorized for this organization's SAML single sign-on",
    );
    // Rate limited.
    expect((await tryUnsaved(LIMITED)).error).toContain(
      "GitHub is rate limiting this token; try again later",
    );
    // None of those saved anything.
    const team = await (await admin.get("/api/deployment")).json();
    expect(team.gitCredential || "").toBe("");
    expect(team.gitConnections || []).toEqual([]);

    // A PAT that pushes, unsaved: who it is, its scopes, its expiry.
    const unsaved = await tryUnsaved(PUSH);
    expect(unsaved.ok).toBe(true);
    expect(unsaved.saved).toBe(false);
    expect(unsaved.login).toBe("octo-dev");
    expect(unsaved.scopes).toEqual(["repo", "workflow"]);
    expect(unsaved.expires).toBe("2026-12-31 00:00:00 UTC");

    // Saved, then tested as saved: the verdicts are the reach listing's.
    await page.goto("/settings/git");
    const connectionsList = page.getByTestId("git-connections-list");
    await page.getByTestId("add-connection-button").click();
    const form = page.getByTestId("add-connection-form");
    await form.locator('input[name="new-connection-host"]').fill("github.com");
    await form.locator('input[name="new-connection-token"]').fill(PUSH);
    await form.getByRole("button", { name: "Save connection" }).click();
    await expect(form).toHaveCount(0);
    const testButton = connectionsList.getByRole("button", { name: "Test" });
    await expect(testButton).toHaveCount(1);
    await testButton.click();
    const result = page.getByTestId("credential-check-result");
    await expect(
      result.getByTestId("credential-check-repo-acme/app"),
    ).toHaveText(/✓ acme\/app read, push/);
    await expect(
      result.getByTestId("credential-check-repo-acme/lib"),
    ).toHaveText(/✗ acme\/lib read only/);
    await expect(
      result.getByTestId("credential-check-repo-acme/private"),
    ).toHaveCount(0);
    await noTokenAnywhere();

    // swapToDeployment replaces the deployment's connection with another
    // token, the way step 4 changes what it reaches.
    const swapToDeployment = async (value) => {
      await clearDeployment();
      const added = await admin.post("/api/deployment/git-connections", {
        data: { type: "pat", host: "github.com", value },
      });
      expect(added.status(), await added.text()).toBe(200);
    };

    // ---- 2. Agreement over five repos ----
    const saved = await (
      await admin.post("/api/deployment/git-connections/check", {
        data: {},
      })
    ).json();
    expect(saved.fromReach).toBe(true);
    const reach = await (
      await admin.get("/api/deployment/reachable-repos")
    ).json();
    expect(reach.available).toBe(true);
    const agreement = [];
    let n = 0;
    for (const [repo, access] of Object.entries(REPOS)) {
      const verdict = (saved.repos || []).find((r) => r.repo === repo);
      const listed = (reach.repos || []).find((r) => r.repo === repo);
      const other = repo === "acme/app" ? "acme/widgets" : "acme/app";
      const file = async (extra) => {
        n++;
        const res = await admin.post("/api/tasks", {
          data: {
            title: `agreement ${n} ${repo}`,
            description: "§4 agreement",
            ...extra,
          },
        });
        return { status: res.status(), body: await res.json() };
      };
      const asTarget = await file({ repo });
      const asRead = await file({ repo: other, reads: [repo] });
      const asWrite = await file({ repo: other, writes: [{ repo }] });
      const row = {
        repo,
        stub: access,
        check: verdict
          ? verdict.push
            ? "push"
            : verdict.read
              ? "read"
              : "none"
          : "absent",
        reachable: listed ? (listed.push ? "push" : "read") : "absent",
        target: asTarget.status,
        read: asRead.status,
        write: asWrite.status,
      };
      agreement.push(row);
      const push = access === "push";
      const read = access !== "none";
      expect(row.check, JSON.stringify(row)).toBe(read ? access : "absent");
      expect(row.reachable, JSON.stringify(row)).toBe(read ? access : "absent");
      expect(row.target, JSON.stringify(row)).toBe(push ? 201 : 400);
      expect(row.read, JSON.stringify(row)).toBe(read ? 201 : 400);
      expect(row.write, JSON.stringify(row)).toBe(push ? 201 : 400);
      for (const filed of [asTarget, asRead, asWrite]) {
        if (filed.status === 201) {
          expect(filed.body.state, JSON.stringify(row)).not.toBe(
            "awaiting_reply",
          );
        } else {
          // The refusal names the credential that was checked: the
          // deployment's.
          expect(filed.body.error).toContain("the deployment's");
          expect(filed.body.error).toContain("Settings → Git");
          expect(filed.body.error).not.toContain(PUSH);
        }
      }
    }
    console.log(`agreement: ${JSON.stringify(agreement, null, 1)}`);

    // ---- 3. New task pickers ----
    // The dialog names no team to file into: there is one deployment.
    await page.goto("/");
    await page.getByRole("button", { name: "+ New task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
    await expect(page.getByLabel("Target team")).toHaveCount(0);
    const repoSelect = page.locator('select[name="repo"]');
    await expect(
      repoSelect.locator("option", { hasText: "acme/app" }),
    ).toHaveCount(1);
    const targets = await repoSelect.locator("option").allTextContents();
    console.log(`target picker: ${JSON.stringify(targets)}`);
    for (const pushable of ["acme/app", "acme/widgets"]) {
      expect(targets).toContain(pushable);
    }
    for (const not of ["acme/lib", "acme/docs", "acme/private"]) {
      expect(targets).not.toContain(not);
    }
    // The reads picker offers what the credential can read.
    const readsInput = page.getByLabel("Read-only repos");
    await readsInput.click();
    await expect(
      page.getByRole("menuitem", { name: "acme/lib" }),
    ).toBeVisible();
    const readOffered = await page.getByRole("menuitem").allTextContents();
    console.log(`reads picker: ${JSON.stringify(readOffered)}`);
    for (const readable of ["acme/lib", "acme/docs"]) {
      expect(readOffered).toContain(readable);
    }
    expect(readOffered).not.toContain("acme/private");
    await page.keyboard.press("Escape");

    // A typed repo the team cannot push to: the warning, then the 400.
    await repoSelect.selectOption({ label: "Other…" });
    const typed = page.locator('input[name="repo"]');
    await typed.fill("acme/lib");
    await expect(page.getByTestId("repo-unreachable")).toContainText(
      "The deployment's GitHub credential cannot push to acme/lib",
    );
    await page.getByLabel("Title").fill("typed unreachable repo");
    const refused = page.waitForResponse(
      (r) => r.url().endsWith("/api/tasks") && r.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Create task" }).click();
    const refusal = await refused;
    expect(refusal.status()).toBe(400);
    const refusalText = (await refusal.json()).error;
    console.log(`typed-repo 400: ${refusalText}`);
    await expect(page.getByText(refusalText, { exact: false })).toBeVisible();
    await page.keyboard.press("Escape");

    // ---- 4. Parking: schedule Run now and a hand-started suite ----
    // The deployment's credential swapped for one that reaches nothing
    // it targets; a changed token is listed afresh at once.
    await swapToDeployment(NOREACH);
    const parkedIDs = [];
    const ran = await admin.post("/api/schedules/sched-2/run", { data: {} });
    expect(ran.status(), await ran.text()).toBeLessThan(300);
    const suiteRun = await admin.post("/api/suite-runs", {
      data: {
        suiteId: "suite-1",
        repo: "acme/widgets",
        base: "main",
      },
    });
    expect(suiteRun.status(), await suiteRun.text()).toBeLessThan(300);
    const tasks = await (await admin.get("/api/tasks")).json();
    const list = Array.isArray(tasks) ? tasks : tasks.tasks || [];
    for (const t of list) {
      if (t.state === "awaiting_reply" && Number(t.id) > 20) {
        parkedIDs.push(t.id);
      }
    }
    console.log(`parked: ${JSON.stringify(parkedIDs)}`);
    expect(parkedIDs.length).toBeGreaterThanOrEqual(2);
    for (const id of parkedIDs) {
      const detail = await (await admin.get(`/api/tasks/${id}`)).text();
      expect(detail).toContain(
        "This task was filed, but it cannot be dispatched yet: ",
      );
      expect(detail).toContain("cannot push to acme/widgets");
      expect(detail).toContain(
        "Once the deployment's credential can reach it, reply here to have it dispatched.",
      );
      expect(detail).not.toContain(NOREACH);
    }

    // Grant access on "GitHub", then reply: the task leaves the park.
    stub.tokens[NOREACH].repos["acme/widgets"] = "push";
    const replied = await admin.post(`/api/tasks/${parkedIDs[0]}/comments`, {
      data: { body: "Access granted on GitHub." },
    });
    expect(replied.status(), await replied.text()).toBeLessThan(300);
    const after = await (await admin.get(`/api/tasks/${parkedIDs[0]}`)).json();
    console.log(`after reply: ${after.state}`);
    expect(after.state).not.toBe("awaiting_reply");

    // The saved Test right after the grant: the listing is cached.
    const stale = await (
      await admin.post("/api/deployment/git-connections/check", {
        data: {},
      })
    ).json();
    console.log(`Test right after grant: ${JSON.stringify(stale.repos)}`);

    await noTokenAnywhere();
    await page.close();
  });
});
