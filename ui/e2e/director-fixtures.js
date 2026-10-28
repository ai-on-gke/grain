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
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pageErrors } from "./pageerrors.js";
import { accessHeaders, signIn } from "./accesscode.js";

const READY_MS = 60_000;
const STOP_MS = 15_000;

async function startDirectorServer(tmpDir, addr = "127.0.0.1:0") {
  const bin = process.env.GRAIN_E2E_DIRECTOR_BIN;
  if (!bin) {
    throw new Error(
      "GRAIN_E2E_DIRECTOR_BIN is unset: e2e/global-setup.js must build the " +
        "Director fixture before this spec runs.",
    );
  }
  const proc = spawn(bin, ["-root", tmpDir, "-addr", addr], {
    stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env, TMPDIR: tmpDir },
  });
  let output = "";
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(
      () =>
        reject(
          new Error(
            `Director fixture did not start within ${READY_MS}ms. Output:\n${output}`,
          ),
        ),
      READY_MS,
    );
    const done = (fn, value) => {
      clearTimeout(timer);
      fn(value);
    };
    const read = (chunk) => {
      output += chunk;
      const match = output.match(
        /grain director e2e serving at (http:\/\/127\.0\.0\.1:\d+)/,
      );
      if (match) done(resolve, match[1]);
    };
    proc.stdout.on("data", read);
    proc.stderr.on("data", read);
    proc.on("error", (err) => done(reject, err));
    proc.on("exit", (code, signal) =>
      done(
        reject,
        new Error(
          `Director fixture exited (code ${code}, signal ${signal}) before it was ready. Output:\n${output}`,
        ),
      ),
    );
  });
  const response = await fetch(`${url}/api/console-sessions`, {
    headers: accessHeaders(),
  });
  if (!response.ok) {
    proc.kill("SIGKILL");
    throw new Error(
      `GET ${url}/api/console-sessions: ${response.status}\n${output}`,
    );
  }
  return { url, proc, output: () => output };
}

async function stopDirectorServer(server) {
  if (!server || server.proc.exitCode !== null) return;
  const stopped = new Promise((resolve) => server.proc.once("exit", resolve));
  server.proc.kill("SIGTERM");
  const graceful = await Promise.race([
    stopped.then(() => true),
    new Promise((resolve) => setTimeout(() => resolve(false), STOP_MS)),
  ]);
  if (!graceful) {
    server.proc.kill("SIGKILL");
    await stopped;
  }
}

export const test = base.extend({
  ...pageErrors,
  directorInstance: [
    async ({}, use) => {
      const tmpDir = await fs.mkdtemp(
        path.join(os.tmpdir(), "grain-director-browser-"),
      );
      let current = await startDirectorServer(tmpDir);
      const addr = new URL(current.url).host;
      try {
        await use({
          url: current.url,
          async restart() {
            await stopDirectorServer(current);
            current = await startDirectorServer(tmpDir, addr);
            return current.url;
          },
        });
      } finally {
        await stopDirectorServer(current);
        await fs.rm(tmpDir, { recursive: true, force: true });
      }
    },
    { scope: "worker" },
  ],
  directorServer: [
    async ({ directorInstance }, use) => {
      await use(directorInstance.url);
    },
    { scope: "worker" },
  ],
  restartDirectorServer: async ({ directorInstance }, use) => {
    await use(() => directorInstance.restart());
  },
  baseURL: async ({ directorServer }, use) => {
    await use(directorServer);
  },
  // The fixture server serves the run's access code (accesscode.js), as
  // every grain daemon serves one: each page and request signs in with it.
  storageState: async ({ directorServer }, use) => {
    await use(await signIn(directorServer));
  },
});

export { expect };
