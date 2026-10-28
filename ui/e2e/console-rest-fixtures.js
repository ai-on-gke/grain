import { test as base, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pageErrors } from "./pageerrors.js";
import { signIn } from "./accesscode.js";

// One consolerestserver per worker: a real store, UI server and
// orchestrator with a single standing console session, whose agent
// provider is the only thing scripted (consolerestserver/main.go).
// console-resting-notes-walkthrough.spec.js is its one user.

const READY_MS = 60_000;
const STOP_MS = 15_000;

async function startConsoleRestServer(tmpDir) {
  const bin = process.env.GRAIN_E2E_CONSOLE_REST_BIN;
  if (!bin) {
    throw new Error(
      "GRAIN_E2E_CONSOLE_REST_BIN is unset: e2e/global-setup.js must build " +
        "the console session fixture before this spec runs.",
    );
  }
  const proc = spawn(bin, [], {
    stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env, TMPDIR: tmpDir },
  });
  let output = "";
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(
      () =>
        reject(
          new Error(
            `Console session fixture did not start within ${READY_MS}ms. Output:\n${output}`,
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
        /grain console rest e2e serving at (http:\/\/127\.0\.0\.1:\d+)/,
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
          `Console session fixture exited (code ${code}, signal ${signal}) before it was ready. Output:\n${output}`,
        ),
      ),
    );
  });
  return { url, proc };
}

export const test = base.extend({
  ...pageErrors,
  consoleRestServer: [
    async ({}, use) => {
      const tmpDir = await fs.mkdtemp(
        path.join(os.tmpdir(), "grain-console-rest-browser-"),
      );
      const server = await startConsoleRestServer(tmpDir);
      const stopped = new Promise((resolve) =>
        server.proc.once("exit", resolve),
      );
      try {
        await use(server.url);
      } finally {
        server.proc.kill("SIGTERM");
        const graceful = await Promise.race([
          stopped.then(() => true),
          new Promise((resolve) => setTimeout(() => resolve(false), STOP_MS)),
        ]);
        if (!graceful) {
          server.proc.kill("SIGKILL");
          await stopped;
        }
        await fs.rm(tmpDir, { recursive: true, force: true });
      }
    },
    { scope: "worker" },
  ],
  baseURL: async ({ consoleRestServer }, use) => {
    await use(consoleRestServer);
  },
  // The fixture server serves the run's access code (accesscode.js), as
  // every grain daemon serves one: each page and request signs in with it.
  storageState: async ({ consoleRestServer }, use) => {
    await use(await signIn(consoleRestServer));
  },
});

export { expect };
