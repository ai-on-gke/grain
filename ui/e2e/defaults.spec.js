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
import { execFile, spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";
import { pageErrors } from "./pageerrors.js";
import { accessCode, signInContext } from "./accesscode.js";

// Playwright's own `test`, for the same reason config.spec.js takes it:
// this file starts the deployment each of its tests talks to. The one
// fixture it does want is the page-error guard, which is about the
// browser rather than about whose server is behind it.
const test = base.extend(pageErrors);

const run = promisify(execFile);
const READY_MS = 60_000;

async function startDeployment(dataDir, configuredDefault) {
  const helper = process.env.GRAIN_E2E_DEFAULTS_BIN;
  if (!helper) throw new Error("GRAIN_E2E_DEFAULTS_BIN is unset");
  const args = ["-addr", "127.0.0.1:0", "-data-dir", dataDir];
  if (configuredDefault !== undefined) {
    args.push("-default-filters", configuredDefault);
  }
  const proc = spawn(helper, args, { stdio: ["ignore", "pipe", "pipe"] });
  let output = "";
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(
      () =>
        reject(new Error(`defaults server did not start. Output:\n${output}`)),
      READY_MS,
    );
    const finish = (fn, value) => {
      clearTimeout(timer);
      fn(value);
    };
    const read = (chunk) => {
      output += chunk;
      const match = output.match(
        /defaults server at (http:\/\/127\.0\.0\.1:\d+)/,
      );
      if (match) finish(resolve, match[1]);
    };
    proc.stdout.on("data", read);
    proc.stderr.on("data", read);
    proc.on("error", (err) => finish(reject, err));
    proc.on("exit", (code, signal) =>
      finish(
        reject,
        new Error(
          `defaults server exited (code ${code}, signal ${signal}). Output:\n${output}`,
        ),
      ),
    );
  });
  return {
    proc,
    url,
    async stop() {
      const stopped = new Promise((resolve) => proc.once("exit", resolve));
      proc.kill("SIGKILL");
      await stopped;
    },
  };
}

async function cliList(url, ...args) {
  const bin = process.env.GRAIN_E2E_DEMO_BIN;
  const { stdout } = await run(bin, [
    "-server",
    url,
    "-token",
    accessCode(),
    "list",
    ...args,
  ]);
  return stdout;
}

async function visibleTitles(page) {
  await expect(page.locator(".task-list")).toBeVisible();
  return page.locator(".task-title").allTextContents();
}

// Section 8 of docs/test-plans/2026-09-13.md: one persistent deployment is
// observed through a real browser and the real CLI, stopped, and opened again.
// The policy deliberately selects proposed work, proving it replaces the
// built-in state condition rather than being stacked underneath it. The seeded
// live chat is never a row in the browser (taskFilters.js's isSessionTask),
// whatever the policy says; the CLI still lists it where nothing leaves it out.
test("deployment task-list defaults persist and explicit web and CLI queries win", async ({
  page,
}) => {
  const dataDir = await fs.mkdtemp(
    path.join(os.tmpdir(), "grain-defaults-e2e-"),
  );
  const proposedDefault = "f=state:any:proposed";
  let deployment;
  try {
    deployment = await startDeployment(dataDir, proposedDefault);
    await signInContext(page.context(), deployment.url);

    await page.goto(deployment.url);
    await expect
      .poll(() => visibleTitles(page))
      .toEqual(["Proposed background"]);
    expect(await cliList(deployment.url)).toContain("Proposed background");
    expect(await cliList(deployment.url)).not.toContain("Queued background");

    await page.goto(`${deployment.url}/?f=state:any:queued`);
    await expect.poll(() => visibleTitles(page)).toEqual(["Queued background"]);
    const overridden = await cliList(
      deployment.url,
      "-filter",
      "state:any:queued",
    );
    expect(overridden).toContain("Queued background");
    expect(overridden).not.toContain("Proposed background");

    await deployment.stop();
    deployment = await startDeployment(dataDir);
    await page.goto(deployment.url);
    await expect
      .poll(() => visibleTitles(page))
      .toEqual(["Proposed background"]);
    expect(await cliList(deployment.url)).toContain("Proposed background");

    // Empty is an explicit deployment policy, not a missing value. Restart once
    // to save it and once more to prove no startup default fills it back in.
    await deployment.stop();
    deployment = await startDeployment(dataDir, "");
    await page.goto(deployment.url);
    await expect
      .poll(async () => (await visibleTitles(page)).sort())
      .toEqual(
        [
          "Closed background",
          "Deferred background",
          "Proposed background",
          "Queued background",
        ].sort(),
      );
    const cleared = await cliList(deployment.url);
    for (const title of [
      "Closed background",
      "Deferred background",
      "Proposed background",
      "Queued background",
      "Queued live chat",
    ]) {
      expect(cleared).toContain(title);
    }

    await page.goto(`${deployment.url}/?f=state:any:closed`);
    await expect.poll(() => visibleTitles(page)).toEqual(["Closed background"]);
    const closed = await cliList(deployment.url, "-filter", "state:any:closed");
    expect(closed).toContain("Closed background");
    expect(closed).not.toContain("Queued background");

    await deployment.stop();
    deployment = await startDeployment(dataDir);
    await page.goto(deployment.url);
    await expect
      .poll(async () => (await visibleTitles(page)).sort())
      .toEqual(
        [
          "Closed background",
          "Deferred background",
          "Proposed background",
          "Queued background",
        ].sort(),
      );
    expect(await cliList(deployment.url)).toContain("Queued live chat");
  } finally {
    if (deployment?.proc.exitCode === null) await deployment.stop();
    await fs.rm(dataDir, { recursive: true, force: true });
  }
});
