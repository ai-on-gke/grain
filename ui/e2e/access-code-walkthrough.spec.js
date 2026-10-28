import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { test, expect } from "./fixtures.js";

// A single-tenant grain's access code (docs/access-code.md,
// grain/task-4343), walked through on a real `grain demo
// -access-code-file`, in a real browser, over this machine's own
// non-loopback address -- where the corp web proxy's requests come
// from too -- so nothing the server trusts about loopback can stand in
// for the code:
//
//   - without the code the UI is the "Enter access code" page, never
//     the app, and a wrong code is refused;
//   - the right code signs the browser in, and a reload keeps it in --
//     to the app itself, not a dialog over it: no sign-in or "create
//     account" dialog opens, and "+ New task" works (bug B1 of
//     docs/test-runs/2026-10-16-single-tenant-install-walkthrough.md,
//     which a check that #root rendered let through);
//   - `grain access-code rotate` signs it out, and the old code stops
//     working as a bearer token, while the new one signs in again.
//
// Nothing here is stubbed: the server, its token file and the CLI that
// rotates it are grain's own.
const dataDir = fs.mkdtempSync(
  path.join(os.tmpdir(), "grain-e2e-access-code-"),
);
const codeFile = path.join(dataDir, "local-api-token");

test.use({
  demoArgs: ["-addr", "0.0.0.0:0", "-access-code-file", codeFile],
  // Its subject is signing in, so its tests start signed out.
  signedIn: false,
});
test.describe.configure({ mode: "serial" });
test.setTimeout(120_000);

function externalAddress() {
  for (const addrs of Object.values(os.networkInterfaces())) {
    for (const a of addrs || []) {
      if (a.family === "IPv4" && !a.internal) return a.address;
    }
  }
  return "";
}

function externalURL(baseURL) {
  const external = externalAddress();
  return external ? `http://${external}:${new URL(baseURL).port}` : "";
}

function readCode() {
  return fs.readFileSync(codeFile, "utf8").trim();
}

const loginHeading = (page) =>
  page.getByRole("heading", { name: "Enter access code" });

// expectUsable checks the signed-in page is the app a person can work
// in: nothing modal is open over it -- /api/config reports an access
// session as `authenticated: false`, which once opened the un-dismissable
// "create account" dialog on every page -- and "+ New task" opens its
// form, which closes again.
async function expectUsable(page) {
  await expect(page.locator("#root")).toHaveCount(1);
  const newTask = page.getByRole("button", { name: "+ New task" });
  await expect(newTask).toBeVisible();
  await expect(page.locator("[role=dialog]")).toHaveCount(0);
  await newTask.click();
  const form = page.getByRole("dialog", { name: "New task" });
  await expect(form).toBeVisible();
  await expect(page.locator("[role=dialog]")).toHaveCount(1);
  await form.getByRole("button", { name: "Close dialog" }).click();
  await expect(page.locator("[role=dialog]")).toHaveCount(0);
}

async function signIn(page, code) {
  await page.getByLabel("Access code").fill(code);
  await page.getByRole("button", { name: "Continue" }).click();
}

test("the access code signs a browser in, survives a reload, and rotating it signs the browser out", async ({
  browser,
  baseURL,
}) => {
  const url = externalURL(baseURL);
  test.skip(!url, "this machine has no non-loopback IPv4 address");
  // A fresh context, so no grain.username is remembered in
  // localStorage: a remembered name hid bug B1.
  const context = await browser.newContext({ baseURL: url });
  const page = await context.newPage();

  // No code: the login page, not the app, and the API says 401.
  const first = await page.goto("/");
  expect(first.status()).toBe(401);
  expect(
    await page.evaluate(() => localStorage.getItem("grain.username")),
  ).toBeNull();
  await expect(loginHeading(page)).toBeVisible();
  await expect(page.locator("#root")).toHaveCount(0);
  const anonymous = await context.request.get("/api/tasks");
  expect(anonymous.status()).toBe(401);

  // A wrong code is refused, and says so without saying why.
  await signIn(page, "AAAA-BBBB-CCCC-DDDD-EEEE-FFFF-GGGG-HHHH");
  await expect(page.getByRole("alert")).toHaveText(
    "That access code was not accepted.",
  );
  await expect(loginHeading(page)).toBeVisible();

  // The right one, typed in lower case, is the app.
  const code = readCode();
  await signIn(page, code.toLowerCase());
  await expect(page).toHaveURL(`${url}/`);
  await expect(loginHeading(page)).toHaveCount(0);
  await expectUsable(page);
  const cookies = await context.cookies(url);
  const access = cookies.find((c) => c.name === "grain_access");
  expect(access, "the grain_access cookie").toBeTruthy();
  expect(access.httpOnly).toBe(true);
  expect(access.sameSite).toBe("Strict");
  expect(access.value).not.toContain(code);
  const signedIn = await context.request.get("/api/tasks");
  expect(signedIn.status()).toBe(200);

  // A reload keeps the browser in.
  const reloaded = await page.reload();
  expect(reloaded.status()).toBe(200);
  await expect(loginHeading(page)).toHaveCount(0);
  await expectUsable(page);

  // Rotating the code, with grain's own CLI, signs the browser out
  // within a second or so, and the old code is no bearer token either.
  const rotated = execFileSync(
    process.env.GRAIN_E2E_DEMO_BIN,
    ["access-code", "-data-dir", dataDir, "rotate"],
    { encoding: "utf8" },
  ).trim();
  expect(rotated).not.toBe(code);
  expect(readCode()).toBe(rotated);
  await expect
    .poll(async () => (await context.request.get("/api/tasks")).status(), {
      timeout: 10_000,
    })
    .toBe(401);
  await page.reload();
  await expect(loginHeading(page)).toBeVisible();
  const oldBearer = await context.request.get("/api/tasks", {
    headers: { Authorization: `Bearer ${code}` },
  });
  expect(oldBearer.status()).toBe(401);

  // The new code signs the browser back in.
  await signIn(page, rotated);
  await expect(loginHeading(page)).toHaveCount(0);
  await expectUsable(page);
  expect((await context.request.get("/api/tasks")).status()).toBe(200);
  await context.close();
});

test.afterAll(() => {
  fs.rmSync(dataDir, { recursive: true, force: true });
});
