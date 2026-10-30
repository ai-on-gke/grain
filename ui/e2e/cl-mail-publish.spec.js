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

// Mail and Publish replies, pressed by a person on the demo's CL tasks
// (docs/test-plans/2026-10-29.md §1, gap 2): each writes an entry in the
// task's conversation and its button goes disabled at once. The presses
// change the demo's remembered CL state, which cl-review-layout.spec.js
// reads on the worker's shared demo, so this spec starts its own.

import { test as base, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import net from "node:net";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pageErrors } from "./pageerrors.js";
import { signInContext, writeAccessCode } from "./accesscode.js";

const test = base.extend(pageErrors);
test.setTimeout(90_000);

async function freePort() {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.on("error", reject);
    srv.listen(0, "127.0.0.1", () => {
      const { port } = srv.address();
      srv.close(() => resolve(port));
    });
  });
}

async function startDemo(addr, tmpDir) {
  const bin = process.env.GRAIN_E2E_DEMO_BIN;
  if (!bin)
    throw new Error(
      "GRAIN_E2E_DEMO_BIN is unset: run through playwright.config.js",
    );
  const codeFile = path.join(tmpDir, "access-code");
  await writeAccessCode(codeFile);
  const proc = spawn(
    bin,
    ["demo", "-addr", addr, "-open=false", "-access-code-file", codeFile],
    {
      stdio: ["ignore", "pipe", "pipe"],
      env: { ...process.env, TMPDIR: tmpDir },
    },
  );
  let output = "";
  await new Promise((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error(`grain demo did not start:\n${output}`)),
      30_000,
    );
    const read = (chunk) => {
      output += chunk;
      if (/at http:\/\/127\.0\.0\.1:\d+/.test(output)) {
        clearTimeout(timer);
        resolve();
      }
    };
    proc.stdout.on("data", read);
    proc.stderr.on("data", read);
    proc.on("exit", (code) => {
      clearTimeout(timer);
      reject(new Error(`grain demo exited (${code}):\n${output}`));
    });
  });
  return proc;
}

async function stopDemo(proc) {
  if (!proc || proc.exitCode !== null) return;
  const stopped = new Promise((resolve) => proc.once("exit", resolve));
  proc.kill("SIGKILL");
  await stopped;
}

async function taskID(page, url, title) {
  const tasks = await (await page.request.get(`${url}/api/tasks`)).json();
  const list = Array.isArray(tasks) ? tasks : tasks.tasks || [];
  return list.find((t) => t.title === title)?.id;
}

test("Mail and Publish replies record the press and go disabled", async ({
  page,
}) => {
  const addr = `127.0.0.1:${await freePort()}`;
  const url = `http://${addr}`;
  const tmpDir = await fs.mkdtemp(path.join(os.tmpdir(), "grain-e2e-cl-mail-"));
  let proc;
  try {
    proc = await startDemo(addr, tmpDir);
    await signInContext(page.context(), url);

    // The draft CL needs mailing: Mail is offered, and pressing it
    // mails the CL, says so in the conversation and takes Mail away.
    const draft = await taskID(page, url, "Document the depot retry budget");
    await page.goto(`${url}/tasks/${draft}`);
    const mail = page.getByTestId("cl-mail");
    await expect(mail).toBeEnabled();
    await expect(page.getByTestId("cl-publish")).toBeDisabled();
    await mail.click();
    await expect(page.getByText("Mailed cl/612400103.").first()).toBeVisible();
    await expect(mail).toBeDisabled();
    await expect(page.getByTestId("cl-review-badge")).toHaveAttribute(
      "data-state",
      "in_review",
    );

    // The failed CL's run left two drafts: Publish replies (2) publishes
    // them, says so, and goes disabled with nothing left to publish.
    const failed = await taskID(
      page,
      url,
      "Port the retry budget to the depot",
    );
    await page.goto(`${url}/tasks/${failed}`);
    const publish = page.getByTestId("cl-publish");
    await expect(publish).toBeEnabled();
    await expect(publish).toHaveText("Publish replies (2)");
    await expect(page.getByTestId("cl-mail")).toBeDisabled();
    await publish.click();
    await expect(
      page.getByText("Published 2 replies on cl/612400001.").first(),
    ).toBeVisible();
    await expect(publish).toBeDisabled();
    await expect(publish).toHaveText("Publish replies");
  } finally {
    await stopDemo(proc);
    await fs.rm(tmpDir, { recursive: true, force: true });
  }
});
