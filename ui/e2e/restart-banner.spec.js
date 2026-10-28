import { test as base, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { pageErrors } from "./pageerrors.js";
import { signInContext, writeAccessCode } from "./accesscode.js";

// grain/task-3543 (docs/test-runs/2026-10-12-upgrade-interruption-restart.md,
// finding 1): a plain SIGTERM -- what `systemctl restart` sends -- has to
// read as "grain is restarting", not "cannot be reached". A daemon with
// nothing to drain exits within milliseconds of the signal, far inside
// the shell's three-second /api/config poll, so the page has to have
// heard it from the request it holds open (GET /api/restart-watch). Only
// a real daemon shows that: App.resilience.test.jsx can only hand the
// page an answer it never had to race for. This file drives its own
// `grain daemon` rather than the per-worker `grain demo`, which nothing
// here may kill out from under the other specs.
const test = base.extend(pageErrors);
test.setTimeout(120_000);

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

// freePort, as disconnection.spec.js has it: the daemon that replaces
// the stopped one listens on the very same address, so the page only
// ever learns that its origin answers again.
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

async function startDaemon(root, addr) {
  const bin = process.env.GRAIN_E2E_DEMO_BIN;
  if (!bin) throw new Error("global setup did not provide a Grain binary");
  // The run's access code as the daemon's own (accesscode.js), the same
  // across the restart, so the page's session outlives it.
  await writeAccessCode(path.join(root, "data", "local-api-token"));
  const proc = spawn(
    bin,
    [
      "daemon",
      "-data-dir",
      path.join(root, "data"),
      "-sandbox-dir",
      path.join(root, "sandboxes"),
      "-ui-addr",
      addr,
      "-sandbox-port-base=0",
      "-as",
      "alice",
      "-max-workers",
      "1",
      "-max-mergers",
      "0",
      "-github-host",
      "127.0.0.1:9",
      "-github-insecure-http",
    ],
    {
      cwd: root,
      env: process.env,
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  let output = "";
  const append = (chunk) => {
    output += chunk.toString();
  };
  proc.stdout.on("data", append);
  proc.stderr.on("data", append);

  const deadline = Date.now() + 30_000;
  while (!/serving the UI\/API on http:\/\//.test(output)) {
    if (proc.exitCode !== null) {
      throw new Error(`daemon exited ${proc.exitCode}:\n${output}`);
    }
    if (Date.now() > deadline) {
      proc.kill("SIGKILL");
      throw new Error(`the Grain daemon did not start:\n${output}`);
    }
    await sleep(100);
  }
  return { proc, output: () => output };
}

async function stopDaemon(daemon, signal) {
  if (!daemon || daemon.proc.exitCode !== null) return;
  const stopped = new Promise((resolve) => daemon.proc.once("exit", resolve));
  daemon.proc.kill(signal);
  await stopped;
}

test("a SIGTERM'd daemon reads as grain restarting, and the page picks back up when it is back", async ({
  page,
}) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "grain-e2e-restart-"));
  await fs.mkdir(path.join(root, "data"));
  await fs.mkdir(path.join(root, "sandboxes"));
  const addr = `127.0.0.1:${await freePort()}`;
  const url = `http://${addr}`;
  const banner = page.getByTestId("disconnected-banner");
  let daemon;
  try {
    daemon = await startDaemon(root, addr);
    await signInContext(page.context(), url);

    // The page has to be holding its watch when the signal lands: that
    // held request, not a poll, is what is under test.
    const watching = page.waitForRequest((req) =>
      req.url().endsWith("/api/restart-watch"),
    );
    await page.goto(url);
    await watching;
    await expect(banner).toHaveCount(0);

    await stopDaemon(daemon, "SIGTERM");
    daemon = null;

    await expect(banner).toBeVisible({ timeout: 20_000 });
    await expect(banner).toContainText(/grain is restarting/i);
    await expect(banner).not.toContainText(/cannot be reached/i);

    // The restart's other half: the same data, the same address, and the
    // page clears the banner by itself with no reload.
    daemon = await startDaemon(root, addr);
    await expect(banner).toHaveCount(0, { timeout: 45_000 });
  } finally {
    await stopDaemon(daemon, "SIGKILL");
    await fs.rm(root, { recursive: true, force: true });
  }
});
