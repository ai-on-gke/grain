// Browser pass: sign in with the access code, visit every page, and look
// for team, account, admin or hub UI and for calls to removed routes.
import { chromium } from "/home/debian/work/ui/node_modules/playwright/index.mjs";
import fs from "node:fs";

const base = process.argv[2];
const code = fs.readFileSync(process.argv[3], "utf8").trim();
const pages = ["/", "/inbox", "/processes", "/ci", "/boards", "/templates", "/suites/runs", "/sessions", "/memory",
  "/repos", "/repos/acme/widgets", "/repos/acme/widgets/releases", "/settings", "/settings/general", "/settings/tasks",
  "/settings/models", "/settings/git", "/settings/credentials", "/settings/config-repo", "/settings/ci", "/settings/agents",
  "/settings/sandboxes", "/settings/executors", "/settings/memory", "/settings/access", "/settings/engine",
  "/system", "/system/upgrade", "/system/logs", "/system/caches", "/system/metrics", "/system/state", "/system/accounts",
  "/system/access", "/system/models", "/system/git", "/t/default/tasks", "/teams", "/admin", "/hub", "/login", "/register"];
const bad = [/switch teams?/i, /team settings/i, /\bteam home\b/i, /\bnew team\b/i, /\bthis team\b/i, /team default/i,
  /\bteam tab\b/i, /admin mode/i, /\bsign up\b/i, /\bregister\b/i, /\bpassword\b/i, /\bsilo hub\b/i, /\bhub status\b/i,
  /\bteam'?s\b/i, /\bteams\b/i, /\bmembers?\b/i];

const browser = await chromium.launch();
const ctx = await browser.newContext();
const page = await ctx.newPage();
const removedCalls = [];
page.on("request", (r) => {
  const u = new URL(r.url());
  if (/^\/api\/(teams|users|hub|auth\/login|auth\/register)/.test(u.pathname)) removedCalls.push(u.pathname);
});
const r0 = await page.goto(base + "/");
const status0 = r0.status();
await page.fill("input", code.toLowerCase().replace(/-/g, " "));
await page.keyboard.press("Enter");
await page.waitForTimeout(1500);
const out = { status0, pages: {} };
for (const p of pages) {
  const resp = await page.goto(base + p);
  await page.waitForTimeout(900);
  const text = await page.evaluate(() => document.body.innerText);
  const hits = [];
  for (const re of bad) {
    const m = text.match(new RegExp(`.{0,40}${re.source}.{0,40}`, re.flags));
    if (m) hits.push(m[0].replace(/\s+/g, " "));
  }
  out.pages[p] = { status: resp ? resp.status() : null, url: new URL(page.url()).pathname, hits };
}
out.removedCalls = [...new Set(removedCalls)];
console.log(JSON.stringify(out, null, 1));
await browser.close();
