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

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { expect, test } from "./fixtures.js";

// A task's "Internal PR" link followed into a real diff
// (docs/test-runs/2026-10-15-task-intake-inbox-lifecycle.md, §4). A task
// on acme/widgets pushes a commit through grain's own git proxy, the task
// pane offers Internal PR once GitHub's refs have the branch, and the pull
// request page draws what the commit changed.
//
// No stub of grain's own API: `grain demo -github-sim -git-proxy` puts a
// simulated GitHub (pkg/github/githubsim) over a real bare repo behind
// the real GitHub client, and the compare endpoint reads it through the
// daemon's own comparison (daemon.GitHubRepoComparer). The only stand-in
// is GitHub itself. POST /api/e2e/git-proxy/runs is the demo's stand-in
// for dispatch, as in team-credential-audit-push.spec.js.
//
// A demo of its own (demoArgs no other file uses): the server caches
// GitHub's branch listing for five minutes, so a demo that had already
// listed acme/widgets would not see this push. For the same reason the
// task is filed on the default branch: filing one on a named base checks
// that base against the listing, which would cache it before the push.
// pkg/demo's TestGitHubSimDemoComparesAPushedBranch compares a branch
// off release/1.0.
test.use({
  demoArgs: [
    "-github-sim",
    "-git-proxy",
    "-default-target-repo",
    "acme/widgets",
  ],
});
test.setTimeout(120_000);

function git(dir, args) {
  try {
    return execFileSync("git", args, {
      cwd: dir,
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
      env: {
        ...process.env,
        HOME: dir,
        GIT_CONFIG_NOSYSTEM: "1",
        GIT_TERMINAL_PROMPT: "0",
        GIT_AUTHOR_NAME: "walk",
        GIT_AUTHOR_EMAIL: "walk@example.com",
        GIT_COMMITTER_NAME: "walk",
        GIT_COMMITTER_EMAIL: "walk@example.com",
      },
    });
  } catch (err) {
    throw new Error(`git ${args.join(" ")}: ${err.stderr || err.message}`);
  }
}

async function ok(res, what) {
  expect(res.ok(), `${what}: ${res.status()} ${await res.text()}`).toBe(true);
  return res.json();
}

test("a task's Internal PR link opens its pushed branch's real diff", async ({
  page,
  request,
}) => {
  const task = await ok(
    await request.post("/api/tasks", {
      data: {
        title: `Internal PR diff ${Date.now()}`,
        description: "Add a greeting to widgets.",
        repo: "acme/widgets",
        approved: true,
      },
    }),
    "file the task",
  );
  const run = await ok(
    await request.post("/api/e2e/git-proxy/runs", {
      data: { taskId: task.id },
    }),
    "start the task's run",
  );

  // The run's push: one commit on top of main.
  const work = fs.mkdtempSync(path.join(os.tmpdir(), "grain-internal-pr-"));
  try {
    const auth = `http.extraHeader=Authorization: Basic ${Buffer.from(
      `x:${run.token}`,
    ).toString("base64")}`;
    git(work, ["-c", auth, "clone", "-q", run.remote, "clone"]);
    const clone = path.join(work, "clone");
    fs.writeFileSync(
      path.join(clone, "greeting.go"),
      'package widgets\n\nconst Greeting = "hello from the branch"\n',
    );
    git(clone, ["add", "greeting.go"]);
    git(clone, ["commit", "-q", "-m", "Add a greeting"]);
    git(clone, ["-c", auth, "push", "-q", "origin", `HEAD:${run.branch}`]);
  } finally {
    fs.rmSync(work, { recursive: true, force: true });
  }

  await page.goto(`/tasks/${task.id}`);
  const link = page.getByRole("link", { name: "Internal PR" });
  await expect(link).toBeVisible();
  const href = await link.getAttribute("href");
  expect(href).toBe(`/repos/acme/widgets/compare/${run.branch}`);

  // The link opens in a new tab; following its href is the same page.
  const compared = page.waitForResponse(
    (response) =>
      response.url().includes("/api/repos/acme/widgets/compare?") &&
      response.request().method() === "GET",
  );
  await page.goto(href);
  const comparison = await compared;
  expect(comparison.status()).toBe(200);
  await expect(page.getByRole("heading", { name: run.branch })).toBeVisible();
  await expect(page.getByText("Into main")).toBeVisible();

  const changes = page.getByRole("region", { name: "Branch changes" });
  await expect(changes).toBeVisible();
  await expect(changes).toContainText("greeting.go");
  await expect(changes).toContainText("hello from the branch");
  await page.getByRole("button", { name: "Raw" }).click();
  await expect(changes).toContainText("diff --git a/greeting.go b/greeting.go");
});
