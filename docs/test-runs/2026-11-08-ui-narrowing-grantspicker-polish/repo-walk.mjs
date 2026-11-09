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

// 2026-11-08 §7 item 3a: a repository removed from a restricted
// deployment's target list keeps counting the work still filed against it
// on /repos/<owner>/<name> (#893). Over a fresh consolerestserver: target
// acme/widgets + acme/legacy-service, file 3 open tasks and a standing
// session against Acme/Legacy-Service, drop acme/legacy-service from the
// list, then read /repos and /repos/ACME/legacy-service. Usage:
//   GRAIN_SERVER=http://127.0.0.1:<port> GRAIN_TOKEN=... node repo-walk.mjs [label]
import { chromium, AxeBuilder, VIEWPORTS, THEMES, recorder, signedIn, api, out, tag } from "./lib.mjs";

const S = process.env.GRAIN_SERVER;
const T = process.env.GRAIN_TOKEN;
const label = process.argv[2] || "run";
const rec = recorder(label);
const call = (m, p, b) => api(S, T, m, p, b);

const set = await call("PUT", "/api/deployment", { targetRepos: ["acme/widgets", "acme/legacy-service"] });
rec.step("target list set", { status: set.status });
const ids = [];
for (let i = 1; i <= 3; i++) {
  const r = await call("POST", "/api/tasks", { title: `Legacy service task ${i}`, repo: "Acme/Legacy-Service" });
  ids.push(r.body?.id);
  rec.step(`filed task ${i}`, { status: r.status, id: r.body?.id, repo: r.body?.repo, state: r.body?.state });
}
const sess = await call("POST", "/api/console-sessions", {
  title: "Legacy service session",
  writes: [{ repo: "Acme/Legacy-Service" }],
});
rec.step("standing session", { status: sess.status, body: JSON.stringify(sess.body).slice(0, 200) });
const drop = await call("PUT", "/api/deployment", { targetRepos: ["acme/widgets"] });
rec.check("acme/legacy-service removed from the target list", drop.status < 300, { status: drop.status });
const cfg = (await call("GET", "/api/config")).body;
rec.step("targetRepos now", { targetRepos: cfg.targetRepos });

const browser = await chromium.launch();
for (const vp of VIEWPORTS) {
  for (const theme of THEMES) {
    const t = tag(vp, theme);
    const ctx = await signedIn(browser, S, T, vp, theme);
    const page = await ctx.newPage();
    await page.goto("/repos");
    await page.waitForLoadState("networkidle").catch(() => {});
    await page.waitForTimeout(1500);
    const listed = await page.getByText(/legacy-service/i).count();
    rec.check(`/repos omits the removed repo ${t}`, listed === 0, { listed });

    await page.goto("/repos/ACME/legacy-service");
    const note = page.getByRole("link", { name: /still target this repo, which is no longer on the target list/ });
    await note.first().waitFor({ timeout: 15000 }).catch(() => {});
    const text = (await note.first().innerText().catch(() => "")).replace(/\s+/g, " ").trim();
    rec.check(`/repos/ACME/legacy-service says 3 tasks ${t}`,
      text === "3 tasks still target this repo, which is no longer on the target list", { text });
    const wrap = await note.first().evaluate((el) => {
      const box = el.getBoundingClientRect();
      const doc = document.documentElement;
      let p = el.parentElement;
      let worst = 0;
      while (p) {
        worst = Math.max(worst, p.scrollWidth - p.clientWidth);
        if (p === document.body) break;
        p = p.parentElement;
      }
      return { right: box.right, vw: doc.clientWidth, docOverflow: doc.scrollWidth - doc.clientWidth, worstAncestor: worst, lines: Math.round(box.height / 18) };
    }).catch((e) => ({ error: String(e) }));
    rec.check(`note wraps without overflow ${t}`, wrap.docOverflow <= 0 && wrap.right <= wrap.vw && wrap.worstAncestor <= 0, wrap);
    await page.screenshot({ path: `${out}removed-repo-${t}.png` });
    if (theme === "light" && vp.width === 1440) {
      const axe = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).disableRules(["color-contrast"]).analyze();
      rec.check(`axe removed-repo page ${t}`, axe.violations.length === 0, { violations: axe.violations.map((v) => `${v.id}:${v.nodes.length}`) });
    }
    if (vp.width === 1440 || theme === "light") {
      await note.first().click();
      await page.locator(".task-row").first().waitFor({ timeout: 15000 }).catch(() => {});
      await page.waitForTimeout(1000);
      const rows = await page.locator(".task-row").allInnerTexts();
      const legacy = rows.filter((r) => /Legacy service task/.test(r)).length;
      rec.check(`note links to the 3 filtered tasks ${t}`, rows.length === 3 && legacy === 3, {
        url: page.url().replace(S, ""),
        rows: rows.length,
        sessionRow: rows.some((r) => /Legacy service session/.test(r)),
      });
    }
    await ctx.close();
  }
}
await rec.save(`repo-${label}.json`);
await browser.close();
console.log(`failures: ${rec.result.failures.length}`, rec.result.failures);
