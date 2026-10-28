import { test as base, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import net from "node:net";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pageErrors } from "./pageerrors.js";
import { accessHeaders, signInContext, writeAccessCode } from "./accesscode.js";

// grain/task-26: when the daemon stops answering, the UI should say so
// and recover on its own -- not show a browser-level "failed fetch" and
// nothing else. The honest test for that is a disconnect: this file
// drives its own `grain demo` process (rather than the per-worker one
// e2e/fixtures.js starts, which every other spec shares and which
// nothing here may kill out from under them), stops it mid-session, and
// asserts on what the already-loaded page does about that -- then
// starts a fresh one on the same address and asserts the page notices
// by itself, with no reload.
const test = base.extend(pageErrors);
test.setTimeout(90_000);

const READY_MS = 30_000;

// freePort asks the kernel for a port and gives it back closed, so both
// the first `grain demo` and the one that replaces it after a restart
// can be told to listen on the exact same address -- which is the whole
// point: the page never learns of a new origin, only that the old one
// answers again.
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

// startDemo starts the demo on addr with the run's access code
// (accesscode.js) and signs context in to it, so the page reaching it
// carries the session across every restart: the code is the same each
// time, and a session is signed with the code alone.
async function startDemo(addr, tmpDir, context) {
  const bin = process.env.GRAIN_E2E_DEMO_BIN;
  if (!bin) {
    throw new Error(
      "GRAIN_E2E_DEMO_BIN is unset: e2e/global-setup.js builds `grain` and " +
        "puts its path there, so this suite has to run through " +
        "playwright.config.js rather than with its globalSetup overridden.",
    );
  }
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
    const timer = setTimeout(() => {
      reject(
        new Error(
          `grain demo did not start within ${READY_MS}ms. Output:\n${output}`,
        ),
      );
    }, READY_MS);
    const done = (fn, arg) => {
      clearTimeout(timer);
      fn(arg);
    };
    const read = (chunk) => {
      output += chunk;
      if (/at http:\/\/127\.0\.0\.1:\d+/.test(output)) done(resolve);
    };
    proc.stdout.on("data", read);
    proc.stderr.on("data", read);
    proc.on("error", (err) => done(reject, err));
    proc.on("exit", (code, signal) =>
      done(
        reject,
        new Error(
          `grain demo exited (code ${code}, signal ${signal}) before it was ` +
            `listening. Output:\n${output}`,
        ),
      ),
    );
  });

  const res = await fetch(`http://${addr}/api/config`, {
    headers: accessHeaders(),
  });
  if (!res.ok) {
    proc.kill("SIGKILL");
    throw new Error(`GET http://${addr}/api/config: ${res.status}`);
  }
  await signInContext(context, `http://${addr}`);
  return proc;
}

async function stopDemo(proc) {
  if (!proc || proc.exitCode !== null) return;
  const stopped = new Promise((resolve) => proc.once("exit", resolve));
  proc.kill("SIGKILL");
  await stopped;
}

test("shows the disconnected banner when the daemon stops answering, and recovers on its own when it returns", async ({
  page,
}) => {
  const port = await freePort();
  const addr = `127.0.0.1:${port}`;
  const url = `http://${addr}`;
  const tmpDir = await fs.mkdtemp(
    path.join(os.tmpdir(), "grain-e2e-disconnect-"),
  );
  let proc;
  try {
    proc = await startDemo(addr, tmpDir, page.context());

    await page.goto(url);
    await expect(page.getByText("Bump the Go toolchain to 1.24")).toBeVisible();

    await stopDemo(proc);
    proc = null;

    // The main poll is every three seconds (App.jsx's POLL_INTERVAL_MS);
    // this is real wall-clock time, not a fake clock, so the banner is
    // worth a generous wait rather than the UI's own interval.
    await expect(page.getByText(/cannot be reached/i)).toBeVisible({
      timeout: 20_000,
    });
    await expect(page.getByText(/retrying automatically/i)).toBeVisible();
    // The stale list from before the daemon went away is still exactly
    // what it was -- nothing was blanked out just because the next poll
    // could not confirm it.
    await expect(page.getByText("Bump the Go toolchain to 1.24")).toBeVisible();

    proc = await startDemo(addr, tmpDir, page.context());

    // No reload anywhere in this test: the same page notices by itself.
    await expect(page.getByText(/cannot be reached/i)).not.toBeVisible({
      timeout: 45_000,
    });
    await expect(page.getByText("Bump the Go toolchain to 1.24")).toBeVisible();
  } finally {
    await stopDemo(proc);
    await fs.rm(tmpDir, { recursive: true, force: true });
  }
});

// docs/test-plans/2026-10-05.md §8: the test above shows the banner comes
// and goes. These pin how many of it there are, and what else the page
// does while the daemon is gone -- a real `grain demo` killed and
// restarted on the same address, never context.setOffline -- on the task
// list and on a session pane, with and without a reader who asked for
// reduced motion (18b95ac0c). The banner is DisconnectedBanner; the toast
// that must not leak is ErrorBanner, an MUI Alert with role="alert".
const banner = (page) => page.getByTestId("disconnected-banner");

// transitionSeconds is every duration in el's computed transition, so a
// reduced-motion run can say no transition is left to run on it.
async function transitionSeconds(locator) {
  const raw = await locator.evaluate(
    (el) => getComputedStyle(el).transitionDuration,
  );
  return raw.split(",").map((d) => {
    const value = parseFloat(d);
    return d.trim().endsWith("ms") ? value / 1000 : value;
  });
}

async function fileTask(url, title) {
  const res = await fetch(`${url}/api/tasks`, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...accessHeaders() },
    body: JSON.stringify({ title, repo: "acme/widgets" }),
  });
  if (!res.ok) throw new Error(`POST /api/tasks: ${res.status}`);
}

for (const motion of ["no-preference", "reduce"]) {
  for (const [where, route, ready] of [
    ["the task list", "/", "Bump the Go toolchain to 1.24"],
    ["a session pane", "/session/director", null],
  ]) {
    test(`killing the daemon under ${where} (motion: ${motion}) shows exactly one banner and no toast, and a restart clears it`, async ({
      page,
    }) => {
      test.setTimeout(150_000);
      const port = await freePort();
      const addr = `127.0.0.1:${port}`;
      const url = `http://${addr}`;
      const tmpDir = await fs.mkdtemp(
        path.join(os.tmpdir(), "grain-e2e-disconnect-"),
      );
      let proc;
      try {
        proc = await startDemo(addr, tmpDir, page.context());
        await page.emulateMedia({ reducedMotion: motion });
        await page.goto(url + route);
        if (ready) {
          await expect(page.getByText(ready)).toBeVisible();
        } else {
          await expect(
            page.getByRole("textbox", { name: "Message" }),
          ).toBeVisible();
        }
        expect(
          await page.evaluate(
            () => matchMedia("(prefers-reduced-motion: reduce)").matches,
          ),
        ).toBe(motion === "reduce");
        // A task only the first daemon has: `grain demo` seeds a fresh
        // store on every start, so its disappearing after the restart is
        // the list having been read again from the new one.
        const firstOnly = "Filed on the demo that is about to be killed";
        if (ready) {
          await fileTask(url, firstOnly);
          await expect(page.getByText(firstOnly)).toBeVisible({
            timeout: 15_000,
          });
        }
        await page.evaluate(() => {
          window.__noReload = true;
        });
        await expect(banner(page)).toHaveCount(0);

        await stopDemo(proc);
        proc = null;

        await expect(banner(page)).toBeVisible({ timeout: 20_000 });
        await expect(banner(page)).toHaveAttribute("role", "status");
        await expect(banner(page)).toContainText(
          "This deployment cannot be reached: the connection to it failed.",
        );
        if (motion === "reduce") {
          for (const s of await transitionSeconds(banner(page))) {
            expect(s).toBe(0);
          }
        }
        // Several more failed ticks of both polls: still one banner, and
        // nothing toasted for any of them.
        await page.waitForTimeout(7_000);
        await expect(banner(page)).toHaveCount(1);
        await expect(page.getByRole("alert")).toHaveCount(0);
        await expect(page.locator(".MuiSnackbar-root")).toHaveCount(0);
        await expect(page.getByText(/failed to fetch/i)).toHaveCount(0);

        proc = await startDemo(addr, tmpDir, page.context());

        await expect(banner(page)).toHaveCount(0, { timeout: 45_000 });
        await expect(page.getByRole("alert")).toHaveCount(0);
        if (ready) {
          await expect(page.getByText(firstOnly)).toHaveCount(0, {
            timeout: 15_000,
          });
          await expect(page.getByText(ready)).toBeVisible();
        } else {
          await expect(
            page.getByRole("textbox", { name: "Message" }),
          ).toBeVisible();
        }
        expect(await page.evaluate(() => window.__noReload)).toBe(true);
      } finally {
        await stopDemo(proc);
        await fs.rm(tmpDir, { recursive: true, force: true });
      }
    });
  }

  test(`a gateway answering 503 is "cannot be reached" and a 500 is not (motion: ${motion})`, async ({
    page,
  }) => {
    test.setTimeout(150_000);
    const port = await freePort();
    const addr = `127.0.0.1:${port}`;
    const url = `http://${addr}`;
    const tmpDir = await fs.mkdtemp(
      path.join(os.tmpdir(), "grain-e2e-disconnect-"),
    );
    let proc;
    try {
      proc = await startDemo(addr, tmpDir, page.context());
      await page.emulateMedia({ reducedMotion: motion });
      await page.goto(url);
      await expect(
        page.getByText("Bump the Go toolchain to 1.24"),
      ).toBeVisible();
      await page.evaluate(() => {
        window.__noReload = true;
      });

      // Something in front of the daemon answering for it. Not a mock of
      // grain's API: the daemon is real and running, and this is the
      // gateway's answer in its place.
      const api = `${url}/api/**`;
      await page.route(api, (r) =>
        r.fulfill({ status: 503, contentType: "text/plain", body: "" }),
      );
      await expect(banner(page)).toBeVisible({ timeout: 20_000 });
      await expect(banner(page)).toContainText(
        "a gateway in front of it reports it is unavailable (503 Service Unavailable)",
      );
      await page.waitForTimeout(7_000);
      await expect(banner(page)).toHaveCount(1);
      await expect(page.getByRole("alert")).toHaveCount(0);
      await page.unroute(api);
      await expect(banner(page)).toHaveCount(0, { timeout: 45_000 });

      // A 500 is grain answering: the poll stays quiet and no banner
      // says it cannot be reached.
      await page.route(api, (r) =>
        r.fulfill({
          status: 500,
          contentType: "application/json",
          body: JSON.stringify({ error: "store exploded" }),
        }),
      );
      await page.waitForTimeout(9_000);
      await expect(banner(page)).toHaveCount(0);
      await expect(page.getByRole("alert")).toHaveCount(0);
      await page.unroute(api);
      expect(await page.evaluate(() => window.__noReload)).toBe(true);
    } finally {
      await stopDemo(proc);
      await fs.rm(tmpDir, { recursive: true, force: true });
    }
  });
}

// Found in the 2026-10-15 intake walkthrough: MUI's Modal sets
// aria-hidden on everything outside an open pane, the banner included,
// so a screen reader in a task pane never heard the deployment go away.
// DisconnectedBanner's ModalStatusMirror carries the status into the
// pane; closing the pane gives the banner itself back.
test("a task pane open when the daemon stops answering still has the outage as its status", async ({
  page,
}) => {
  const port = await freePort();
  const addr = `127.0.0.1:${port}`;
  const url = `http://${addr}`;
  const tmpDir = await fs.mkdtemp(
    path.join(os.tmpdir(), "grain-e2e-disconnect-"),
  );
  let proc;
  try {
    proc = await startDemo(addr, tmpDir, page.context());
    await page.goto(`${url}/tasks/3`);
    const pane = page.getByRole("dialog");
    await expect(pane).toBeVisible();

    await stopDemo(proc);
    proc = null;

    await expect(banner(page)).toBeVisible({ timeout: 20_000 });
    const outage = page
      .getByRole("status")
      .filter({ hasText: "cannot be reached" });
    await expect(outage).toHaveCount(1);
    await expect(outage).toContainText(
      "This deployment cannot be reached: the connection to it failed.",
    );
    await expect(pane.getByRole("status")).toContainText("cannot be reached");
    await expect(pane).toMatchAriaSnapshot(`- status: /cannot be reached/`);

    await page.keyboard.press("Escape");
    await expect(pane).toHaveCount(0);
    await expect(outage).toHaveCount(1);
    await expect(page.getByRole("status")).toContainText("cannot be reached");
    await expect(page.getByTestId("disconnected-banner-mirror")).toHaveCount(0);
  } finally {
    await stopDemo(proc);
    await fs.rm(tmpDir, { recursive: true, force: true });
  }
});

// Found in the 2026-10-26 responsive navigation walkthrough: a task pane
// covers the shell banner -- all of it on a phone, all but the rail's
// sliver wider than that -- so a sighted reader in the pane saw nothing
// of the outage. The mirror is a visible strip at the top of the pane's
// paper, and the pane's own Back stays clear below it.
for (const [name, viewport] of [
  ["phone", { width: 390, height: 844 }],
  ["desktop", { width: 1440, height: 900 }],
]) {
  test(`a task pane on a ${name} shows the outage on top of itself`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    const port = await freePort();
    const addr = `127.0.0.1:${port}`;
    const tmpDir = await fs.mkdtemp(
      path.join(os.tmpdir(), "grain-e2e-disconnect-"),
    );
    let proc;
    try {
      proc = await startDemo(addr, tmpDir, page.context());
      await page.goto(`http://${addr}/tasks/4`);
      const pane = page.getByRole("dialog");
      await expect(pane).toBeVisible();
      const back = pane.getByRole("button", { name: /Back/ });
      await expect(back).toBeVisible();

      await stopDemo(proc);
      proc = null;

      await expect(banner(page)).toBeVisible({ timeout: 20_000 });
      const strip = pane.getByTestId("disconnected-banner-mirror");
      await expect(strip).toContainText("cannot be reached");
      await expect(strip).toBeVisible();

      const box = await strip.boundingBox();
      const paper = await pane.boundingBox();
      expect(box.y).toBeCloseTo(paper.y, 0);
      // Inside the paper's 1px left border.
      expect(Math.abs(box.x - paper.x)).toBeLessThanOrEqual(1);
      expect(box.height).toBeGreaterThan(20);
      // What is actually painted there is the strip, not the pane over it.
      const hit = await page.evaluate(
        ([x, y]) =>
          document
            .elementFromPoint(x, y)
            ?.closest('[data-testid="disconnected-banner-mirror"]') !== null,
        [box.x + box.width / 2, box.y + box.height / 2],
      );
      expect(hit).toBe(true);

      // Back is pushed down rather than covered, and still works.
      const backBox = await back.boundingBox();
      expect(backBox.y).toBeGreaterThanOrEqual(box.y + box.height - 1);
      await back.click();
      await expect(pane).toHaveCount(0);
      await expect(banner(page)).toBeVisible();

      // New task is a Drawer rather than a Dialog, and covers the banner
      // the same way.
      await page
        .getByRole("button", { name: /new task/i })
        .first()
        .click();
      const form = page.getByRole("dialog");
      await expect(form).toBeVisible();
      const formStrip = form.getByTestId("disconnected-banner-mirror");
      await expect(formStrip).toBeVisible();
      const formBox = await formStrip.boundingBox();
      expect(formBox.y).toBeCloseTo((await form.boundingBox()).y, 0);
      const close = form.getByRole("button", { name: "Close dialog" });
      expect((await close.boundingBox()).y).toBeGreaterThanOrEqual(
        formBox.y + formBox.height - 1,
      );
    } finally {
      await stopDemo(proc);
      await fs.rm(tmpDir, { recursive: true, force: true });
    }
  });
}
