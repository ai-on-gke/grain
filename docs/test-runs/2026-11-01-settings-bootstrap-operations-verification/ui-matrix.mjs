// Throwaway: drives a live grain daemon for the settings verification
// run. Not committed.
import { chromium } from "playwright";
import fs from "node:fs";
import { execSync } from "node:child_process";

const H = "/home/debian/live";
const BASE = "http://127.0.0.1:18731";
const TOK = fs.readFileSync(`${H}/.grain/local-api-token`, "utf8").trim();
const REPO = `${H}/.grain/state-repo`;
const out = [];
const rec = (axis, what, ok, note = "") => {
  out.push({ axis, what, ok, note });
  console.log(`${ok ? "PASS" : "FAIL"} [${axis}] ${what} ${note}`);
};
const api = async (method, path, body) => {
  const r = await fetch(BASE + path, {
    method,
    headers: { Authorization: `Bearer ${TOK}`, "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const t = await r.text();
  let j;
  try { j = JSON.parse(t); } catch { j = t; }
  return [r.status, j];
};
const exported = () => JSON.parse(fs.readFileSync(`${REPO}/tables/grain_config.json`, "utf8"))[0];

const browser = await chromium.launch();
const ctx = await browser.newContext({ extraHTTPHeaders: { Authorization: `Bearer ${TOK}` }, viewport: { width: 1280, height: 900 } });
const page = await ctx.newPage();
const puts = [];
page.on("request", (r) => { if (r.method() !== "GET") puts.push(`${r.method()} ${new URL(r.url()).pathname} ${r.postData() || ""}`); });

async function open(section) {
  await page.goto(`${BASE}/settings/${section}`);
  await page.locator('[data-testid="settings-section"]').waitFor();
  await page.waitForTimeout(800);
}
async function setText(label, value) {
  const f = page.getByLabel(label, { exact: true });
  await f.fill(String(value));
  await f.blur();
  await page.waitForTimeout(1500);
}
async function disabled(label) {
  return page.getByLabel(label, { exact: true }).isDisabled();
}

// ---- (a) unlocked UI edits export to grain/config
await api("PUT", "/api/settings", { settingsLocked: false });
await open("engine");
await setText("Poll interval", "27s");
await setText("Merge batch size", "6");
await setText("Merge gather window", "2m");
await setText("Max worker agents", "9");
let e = exported();
rec("a-ui-engine", "pollInterval → poll_interval_ms 27000", e.poll_interval_ms === 27000, `got ${e.poll_interval_ms}`);
rec("a-ui-engine", "mergeBatchSize → merge_batch_size 6", e.merge_batch_size === 6, `got ${e.merge_batch_size}`);
rec("a-ui-engine", "mergeGatherWindow → 120000ms", e.merge_gather_window_ms === 120000, `got ${e.merge_gather_window_ms}`);
rec("a-ui-engine", "maxWorkers → max_workers 9", e.max_workers === 9, `got ${e.max_workers}`);

await open("caches");
const proxyBox = page.getByRole("checkbox", { name: /Cache apt, pip/ });
if (!(await proxyBox.isChecked())) { await proxyBox.click(); await page.waitForTimeout(1500); }
await setText("Proxy cache max size (GiB)", "15");
const actBox = page.getByRole("checkbox", { name: /Keep what CI jobs cache/ });
if (!(await actBox.isChecked())) { await actBox.click(); await page.waitForTimeout(1500); }
const actSize = page.getByLabel(/Actions cache max size/);
if (await actSize.count()) { await actSize.fill("11"); await actSize.blur(); await page.waitForTimeout(1500); }
e = exported();
rec("a-ui-caches", "packageCacheEnabled → true", [true, 1, undefined].includes(e.package_cache_enabled), `got ${e.package_cache_enabled}`);
rec("a-ui-caches", "packageCacheSizeGb → 15", e.package_cache_size_gb === 15, `got ${e.package_cache_size_gb}`);
rec("a-ui-caches", "actionsCacheSizeGb → 11", e.actions_cache_size_gb === 11, `got ${e.actions_cache_size_gb}`);

await open("sandboxes");
await setText("Sandbox vCPUs", "4");
await setText("Session sandbox idle TTL", "40m");
e = exported();
rec("a-ui-sandboxes", "sandboxCpus → 4", e.sandbox_cpus === 4, `got ${e.sandbox_cpus}`);
rec("a-ui-sandboxes", "sessionSandboxIdleTtl → 2400000ms", e.session_sandbox_idle_ttl_ms === 2400000, `got ${e.session_sandbox_idle_ttl_ms}`);

await open("credentials");
await setText("GitHub host", "ghe.example.com");
e = exported();
rec("a-ui-credentials", "githubHost → github_host", JSON.stringify(e).includes("ghe.example.com"), "");

// sync round trip keeps the UI's edits (no clobber by the next import)
await api("POST", "/api/state-repo/sync");
await page.waitForTimeout(1000);
const [, s1] = await api("GET", "/api/settings");
rec("a-persist", "UI edits survive POST /api/state-repo/sync", s1.pollInterval === "27s" && s1.packageCacheSizeGb === 15 && s1.githubHost === "ghe.example.com", `${s1.pollInterval} ${s1.packageCacheSizeGb} ${s1.githubHost}`);

// Preferences: localStorage only, no server write.
await open("preferences");
const before = puts.length;
const lsBefore = await page.evaluate(() => JSON.stringify(localStorage));
const dark = page.getByRole("radio", { name: /dark/i }).first();
if (await dark.count()) { await dark.click(); } else { await page.getByLabel(/Appearance|Theme/i).first().click(); await page.getByRole("option", { name: /dark/i }).first().click(); }
await page.waitForTimeout(800);
const lsAfter = await page.evaluate(() => JSON.stringify(localStorage));
const prefWrites = puts.slice(before).filter((x) => x.includes("/api/settings") || x.includes("/api/config"));
rec("a-ui-preferences", "appearance → localStorage changed", lsAfter !== lsBefore, "");
rec("a-ui-preferences", "no server write for preferences", prefWrites.length === 0, prefWrites.join("; "));
const repoGrep = execSync(`grep -rli "appearance\\|timelineOrder\\|pinned" ${REPO}/tables || true`).toString().trim();
rec("a-ui-preferences", "preferences absent from grain/config", repoGrep === "", repoGrep);

// (b) a grain/config commit reaches the UI
const d = JSON.parse(fs.readFileSync(`${REPO}/tables/grain_config.json`, "utf8"));
d[0].poll_interval_ms = 44000;
d[0].actions_cache_size_gb = 13;
fs.writeFileSync(`${REPO}/tables/grain_config.json`, JSON.stringify(d, null, 2) + "\n");
execSync(`/home/debian/grain state check ${REPO}`);
execSync(`git -C ${REPO} -c user.name=v -c user.email=v@e commit -qam "verif: poll 44s"`);
await api("POST", "/api/state-repo/sync");
await open("engine");
rec("b-repo-ui", "Engine shows poll interval from grain/config (44s)", (await page.getByLabel("Poll interval", { exact: true }).inputValue()) === "44s", await page.getByLabel("Poll interval", { exact: true }).inputValue());
await open("caches");
const asz = page.getByLabel(/Actions cache max size/);
rec("b-repo-ui", "Caches shows actions cache size from grain/config (13)", (await asz.count()) && (await asz.inputValue()) === "13", (await asz.count()) ? await asz.inputValue() : "field hidden");

// ---- (c) locked
await api("PUT", "/api/settings", { settingsLocked: true });
await open("engine");
for (const l of ["Poll interval", "Max merges per repo branch", "Merge batch size", "Merge gather window"]) rec("c-ui-locked", `Engine ${l} disabled`, await disabled(l));
rec("c-ui-locked", "Engine Max worker agents editable", !(await disabled("Max worker agents")));
await setText("Max worker agents", "6");
const [, s2] = await api("GET", "/api/settings");
rec("c-ui-locked", "Max worker agents saves while locked", s2.maxWorkers === 6, `got ${s2.maxWorkers}`);
await open("caches");
rec("c-ui-locked", "Caches proxy checkbox disabled", await page.getByRole("checkbox", { name: /Cache apt, pip/ }).isDisabled());
rec("c-ui-locked", "Caches shows config-managed notice", (await page.getByTestId("config-managed-notice").count()) === 1);
await open("sandboxes");
rec("c-ui-locked", "Sandboxes idle TTL disabled", await disabled("Session sandbox idle TTL"));
rec("c-ui-locked", "Sandboxes vCPUs editable", !(await disabled("Sandbox vCPUs")));
await open("credentials");
rec("c-ui-locked", "Legacy capabilities GitHub host disabled", await disabled("GitHub host"));
await open("models");
rec("c-ui-locked", "Models has no config-managed notice", (await page.getByTestId("config-managed-notice").count()) === 0);
rec("c-ui-locked", "Models has no disabled fieldset", (await page.locator("[data-settings-readonly]").count()) === 0);
await open("executors");
rec("c-ui-locked", "Executors has no disabled fieldset", (await page.locator("[data-settings-readonly]").count()) === 0);
await open("preferences");
rec("c-ui-locked", "Preferences has no disabled fieldset", (await page.locator("[data-settings-readonly]").count()) === 0);
await open("upgrade");
let upgradePosted = null;
await page.route("**/api/upgrade", (r) => {
  if (r.request().method() === "POST") { upgradePosted = r.request().postData(); return r.fulfill({ status: 202, body: "{}" }); }
  return r.continue();
});
rec("c-ui-locked", "Upgrade has no disabled fieldset", (await page.locator("[data-settings-readonly]").count()) === 0);
const upBtn = page.getByRole("button", { name: /upgrade/i }).first();
rec("c-ui-locked", "Upgrade button present", (await upBtn.count()) > 0, (await upBtn.count()) ? `enabled=${await upBtn.isEnabled()}` : "");
await page.screenshot({ path: `${H}/upgrade-locked.png` });
await open("engine");
await page.screenshot({ path: `${H}/engine-locked.png`, fullPage: false });
await open("caches");
await page.screenshot({ path: `${H}/caches-locked.png` });

// unlock from the UI (General's lock toggle)
await open("general");
const lock = page.getByRole("checkbox", { name: /Lock settings in UI/i });
rec("c-ui-locked", "lock toggle enabled while locked", await lock.isEnabled());
await lock.click();
await page.waitForTimeout(1500);
const [, cfg] = await api("GET", "/api/config");
rec("c-ui-unlock", "unlock from UI → configRepoOnly false", !cfg.configRepoOnly);
await open("engine");
rec("c-ui-unlock", "Engine poll interval editable again", !(await disabled("Poll interval")));

// ---- (d) external mode
execSync(`rm -rf ${H}/ext2.git && git clone -q --bare ${REPO} ${H}/ext2.git`);
await api("POST", "/api/state-repo/remote", { remote: `file://${H}/ext2.git`, branch: "main" });
for (const sec of ["models", "sandboxes", "executors", "caches", "engine"]) {
  await open(sec);
  rec("d-ui-external", `${sec} read-only with banner`, (await page.getByTestId("external-config-repo-banner").count()) === 1 && (await page.locator("[data-settings-readonly]").count()) > 0);
}
for (const sec of ["preferences", "upgrade", "config-repo"]) {
  await open(sec);
  rec("d-ui-external", `${sec} stays editable`, (await page.getByTestId("external-config-repo-banner").count()) === 0);
}
await open("credentials");
rec("d-ui-external", "Legacy capabilities GitHub host disabled", await disabled("GitHub host"));
await api("POST", "/api/state-repo/remote", { none: true });

await browser.close();
fs.writeFileSync(`${H}/ui-matrix.json`, JSON.stringify(out, null, 1));
console.log("FAILS:", out.filter((x) => !x.ok).length, "of", out.length);
