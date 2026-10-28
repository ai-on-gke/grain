import { test as base, expect } from "@playwright/test";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pageErrors } from "./pageerrors.js";
import { alertRow, alertsBell, openAlerts } from "./alerts.js";
import { startDaemon } from "./realdaemon.js";
import { signIn } from "./accesscode.js";

// The "no isolation" alert (src/alerts.js, config.hostSandboxes): a daemon
// started with -host-sandboxes runs every task in a plain directory on its
// own machine, and says so to everybody who opens its UI -- the operator
// on loopback and a signed-in plain member alike, since it is a fact about
// where anything either of them files will run. A daemon on any other
// backend says nothing of the kind.
//
// It drives its own `grain daemon` (e2e/realdaemon.js) rather than the
// per-worker `grain demo`, because demo has no running backend to report
// (ui.Config.RunningConfig is wired by pkg/daemon only). -ui-addr is
// loopback there, which is what lets -host-sandboxes start without
// -allow-unisolated-host-sandboxes (pkg/daemon's CheckHostSandboxes).
// Nothing is stubbed beyond what realdaemon.js already stubs (the agent
// CLI and docker): no task is dispatched here.
const test = base.extend(pageErrors);
test.setTimeout(120_000);

const noIsolation = (page) => alertRow(page, "No isolation");

async function ok(res) {
  expect(res.ok(), `${res.url()}: ${res.status()} ${await res.text()}`).toBe(
    true,
  );
  return res;
}

async function withDaemon(args, fn) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "grain-e2e-host-"));
  const dataDir = path.join(root, "data");
  const sandboxDir = path.join(root, "sandboxes");
  await fs.mkdir(dataDir);
  await fs.mkdir(sandboxDir);
  await fs.writeFile(path.join(root, "gemini-key"), "test-only-key\n", {
    mode: 0o600,
  });
  let daemon;
  try {
    daemon = await startDaemon(root, dataDir, sandboxDir, args);
    await fn(daemon);
  } catch (err) {
    if (daemon) err.message += `\n\nDaemon output:\n${daemon.output()}`;
    throw err;
  } finally {
    if (daemon) await daemon.stop();
    await fs.rm(root, { recursive: true, force: true });
  }
}

// operatorPage is a browser on loopback, signed in with the access code.
// It names nobody: grain has no users, and the UI sends no X-Grain-User
// (docs/design/settings-panel.md §6.1).
async function operatorPage(browser, url) {
  const context = await browser.newContext({
    baseURL: url,
    storageState: await signIn(url),
  });
  return { context, page: await context.newPage() };
}

test("a host-sandbox daemon warns everyone that its runs have no isolation", async ({
  browser,
}) => {
  await withDaemon(["-host-sandboxes"], async (daemon) => {
    const { context, page } = await operatorPage(browser, daemon.url);
    const config = await (
      await ok(await context.request.get("/api/config"))
    ).json();
    expect(config.hostSandboxes).toBe(true);

    await page.goto("/");
    await expect(page.getByRole("main")).toBeVisible();
    // The bell carries it before anybody opens the panel: a warning.
    await expect(alertsBell(page)).toHaveAttribute("data-severity", "warning");
    await openAlerts(page);
    const row = noIsolation(page);
    await expect(row).toBeVisible();
    await expect(row).toContainText(
      "plain directory on the machine this daemon runs on",
    );
    await expect(row).toContainText("-container-sandboxes");
    await expect(row).toContainText("Deployment");
    await context.close();
  });
});

test("a daemon on another backend says nothing about isolation", async ({
  browser,
}) => {
  await withDaemon(["-disable-native-sandboxes"], async (daemon) => {
    const { context, page } = await operatorPage(browser, daemon.url);
    const config = await (
      await ok(await context.request.get("/api/config"))
    ).json();
    expect(config.hostSandboxes).toBeUndefined();

    await page.goto("/");
    await expect(page.getByRole("main")).toBeVisible();
    await openAlerts(page);
    await expect(noIsolation(page)).toHaveCount(0);
    await context.close();
  });
});
