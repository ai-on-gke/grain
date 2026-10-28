import os from "node:os";
import { expect, test } from "./fixtures.js";

// The UI over plain http from a host that is not localhost, which is how
// the Cloudtop installer advertises it (http://<host>:4242/). Browsers
// count that as an insecure context and take crypto.randomUUID away
// there, and Team settings' "New connection" called it: the click threw
// "crypto.randomUUID is not a function" and opened nothing
// (grain/task-4696, found in
// docs/test-runs/2026-10-18-single-tenant-candidate-qualification.md).
// Every other spec loads 127.0.0.1, which is a secure context, so none
// of them could see it.
//
// The host is this machine's own name, which is what the installer's
// URL carries and one of the names pkg/ui's HostGuard always answers to.
// It is mapped to loopback in Chromium's own resolver, so the page is
// served by this worker's demo while its origin is a name the browser
// does not trust the way it trusts localhost.
const INSECURE_HOST = os.hostname();

test.use({
  launchOptions: {
    args: [`--host-resolver-rules=MAP ${INSECURE_HOST} 127.0.0.1`],
  },
  baseURL: async ({ demoServer }, use) => {
    const url = new URL(demoServer);
    url.hostname = INSECURE_HOST;
    await use(url.origin);
  },
  // The access session the fixture signs in with is a cookie for
  // 127.0.0.1, which this origin would not send; the same cookie for
  // this host.
  storageState: async ({ accessSession }, use) => {
    await use({
      ...accessSession,
      cookies: accessSession.cookies.map((c) => ({
        ...c,
        domain: INSECURE_HOST,
      })),
    });
  },
});

test("New connection opens from an insecure http origin", async ({ page }) => {
  await page.goto("/admin");
  // The premise, asserted rather than assumed: if this origin ever
  // counted as secure, the test below would pass without testing
  // anything.
  expect(await page.evaluate(() => window.isSecureContext)).toBe(false);
  expect(await page.evaluate(() => typeof window.crypto.randomUUID)).toBe(
    "undefined",
  );

  await page.getByTestId("settings-nav-models").click();
  await page.getByRole("button", { name: "New connection" }).click();
  await expect(
    page.locator(".overlay-pane").getByLabel(/Agent type/i),
  ).toBeVisible();
});
