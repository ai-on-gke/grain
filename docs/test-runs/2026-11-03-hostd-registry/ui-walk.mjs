// §1 item 5: Settings -> Hostds in a real browser against a running
// grain daemon (env.sh's start_daemon). Registers a hostd, checks the
// token is shown once and gone on reload, edits its owner and labels,
// rotates and revokes it, and shoots each step at 1440 and 390 px in
// light and dark. Run from ui/:
//
//   PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ../docs/test-runs/2026-11-03-hostd-registry/ui-walk.mjs URL CODE OUTDIR
import { createRequire } from "node:module";
import fs from "node:fs";
import path from "node:path";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");

const [url, code, out] = process.argv.slice(2);
fs.mkdirSync(out, { recursive: true });
const results = [];
const check = (name, ok, detail = "") => {
  results.push(`${ok ? "PASS" : "FAIL"} ${name}${detail ? ` -- ${detail}` : ""}`);
};

const browser = await chromium.launch();
const signedIn = async (opts) => {
  const ctx = await browser.newContext(opts);
  const res = await ctx.request.post(`${url}/api/auth/access-code`, { data: { code } });
  if (!res.ok()) throw new Error(`sign-in: ${res.status()} ${await res.text()}`);
  return ctx;
};
const overflow = (page) =>
  page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

const name = `ui-${Date.now() % 100000}`;
const ctx = await signedIn({ viewport: { width: 1440, height: 900 } });
const page = await ctx.newPage();
const errors = [];
page.on("pageerror", (e) => errors.push(String(e)));
await page.goto(`${url}/settings/hostds`);
await page.getByTestId("hostds-table").waitFor();

// Register.
await page.getByLabel("Name").fill(name);
await page.getByLabel("Owner", { exact: true }).last().fill("owner-ui@example.com");
await page.getByLabel("Labels").last().fill("gpu=yes, zone=ui");
await page.getByRole("button", { name: "Add hostd" }).click();
const issued = page.getByTestId("hostd-issued-token");
await issued.waitFor();
const tokenText = (await issued.locator("code").innerText()).trim();
check("register shows the token", /^ghd_[A-Za-z0-9_-]{20,}$/.test(tokenText), tokenText.slice(0, 10) + "…");
await page.screenshot({ path: `${out}/01-registered-token-shown-1440.png`, fullPage: true });
const row = page.getByTestId(`hostd-row-${name}`);
check("new row lists owner and labels", /owner-ui@example\.com/.test(await row.innerText()) && /zone=ui/.test(await row.innerText()));
check("row shows only the token prefix", (await row.innerText()).includes(tokenText.slice(0, 10)) && !(await row.innerText()).includes(tokenText));

// Reload: the token is gone, and the API never returns it.
await page.reload();
await page.getByTestId("hostds-table").waitFor();
check("token gone on reload", (await page.getByTestId("hostd-issued-token").count()) === 0);
const listed = await (await ctx.request.get(`${url}/api/hostds`)).text();
check("GET /api/hostds never carries the token", !listed.includes(tokenText));

// Edit owner and labels.
await row.getByRole("button", { name: "Edit" }).click();
await row.getByLabel("Owner").fill("owner-ui2@example.com");
await row.getByLabel("Labels").fill("gpu=no");
await page.screenshot({ path: `${out}/02-editing-1440.png`, fullPage: true });
await row.getByRole("button", { name: "Save" }).click();
await page.waitForFunction((n) => {
  const r = document.querySelector(`[data-testid="hostd-row-${n}"]`);
  return r && r.innerText.includes("owner-ui2@example.com") && r.innerText.includes("gpu=no");
}, name);
check("edit saved owner and labels", true);

// Rotate: a new token, shown once, differing from the first.
await row.getByRole("button", { name: "Rotate" }).click();
await page.waitForFunction((t) => {
  const c = document.querySelector('[data-testid="hostd-issued-token"] code');
  return c && c.innerText.trim() !== t;
}, tokenText);
const rotated = (await issued.locator("code").innerText()).trim();
check("rotate shows a new token", rotated !== tokenText && rotated.startsWith("ghd_"));
await page.screenshot({ path: `${out}/03-rotated-1440.png`, fullPage: true });

// Revoke.
await row.getByRole("button", { name: "Revoke" }).click();
await page.waitForFunction((n) => {
  const r = document.querySelector(`[data-testid="hostd-row-${n}"]`);
  return r && r.innerText.includes("Revoked");
}, name);
check("revoke marks the row Revoked and hides its actions",
  (await row.getByRole("button", { name: "Rotate" }).count()) === 0 && (await row.getByRole("button", { name: "Edit" }).count()) === 0);
check("revoke clears the shown token", (await page.getByTestId("hostd-issued-token").count()) === 0);
check("no page errors", errors.length === 0, errors.join("; "));
await ctx.close();

// The page at both widths and themes, with overflow and the rows' text.
for (const [w, h] of [[1440, 900], [390, 844]]) {
  for (const scheme of ["light", "dark"]) {
    const c = await signedIn({ viewport: { width: w, height: h }, colorScheme: scheme });
    await c.addInitScript((s) => localStorage.setItem("grain.themeMode", s), scheme);
    const p = await c.newPage();
    await p.goto(`${url}/settings/hostds`);
    await p.getByTestId("hostds-table").waitFor();
    await p.screenshot({ path: `${out}/hostds-${w}-${scheme}.png`, fullPage: true });
    const of = await overflow(p);
    check(`page has no horizontal overflow at ${w} ${scheme}`, of <= 0, `${of}px`);
    const rows = await p.getByTestId("hostds-table").locator("tbody tr").allInnerTexts();
    results.push(`  rows at ${w} ${scheme}: ` + rows.map((r) => r.replace(/\s+/g, " ").trim()).join(" | "));
    await c.close();
  }
}

// Where the retired Settings -> Executors lands.
const c = await signedIn({ viewport: { width: 1440, height: 900 } });
const p = await c.newPage();
await p.goto(`${url}/settings/executors`);
await p.waitForTimeout(1500);
results.push(`  /settings/executors -> ${new URL(p.url()).pathname}: ${(await p.locator("main").innerText().catch(() => "")).slice(0, 200).replace(/\s+/g, " ")}`);
await p.screenshot({ path: `${out}/settings-executors-1440.png` });
await c.close();

await browser.close();
fs.writeFileSync(`${out}/results.txt`, results.join("\n") + "\n");
console.log(results.join("\n"));
