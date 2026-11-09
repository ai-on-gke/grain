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

// docs/test-plans/2026-11-08.md §4, the browser halves, in Playwright
// Chromium against the real daemon env.sh starts, signed in as alice
// through iapsim's IAP proxy (its /_iapsim/login cookie):
//
//   attribution  /tasks/<id> renders "Director for alice@example.com" on
//                the comment, the close transition and the filed task's
//                author, at 1440x900 and 390x844, light and dark, with no
//                horizontal overflow (item 1, after item1_on_behalf_of.sh).
//   race         two tabs on /session/director: tabA adds grants, tabB
//                (never reloaded) clicks Adopt shipped defaults and adds a
//                grant from a stale snapshot -- 409 "stale", one automatic
//                re-read and retry -- and tabA's grants survive; then a
//                double conflict (every PUT from tabB made stale by a REST
//                write just before it reaches the daemon) shows the error
//                and loses nothing (item 3b).
//
//   cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ../docs/test-runs/2026-11-08-attribution-external-rails/ui_walkthrough.mjs \
//     attribution|race PROXY DAEMON CODE OUT_DIR [TARGET CLOSED FILED]
import { createRequire } from "node:module";
import { mkdirSync, writeFileSync } from "node:fs";

const require = createRequire(`${process.cwd()}/`);
const { chromium } = require("@playwright/test");

const [mode, proxy, daemon, code, outDir, ...ids] = process.argv.slice(2);
mkdirSync(outDir, { recursive: true });
const rest = (method, path, body) =>
  fetch(`${daemon}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: body ? JSON.stringify(body) : undefined,
  }).then(async (r) => ({ status: r.status, body: await r.json() }));
const director = async () =>
  (await rest("GET", "/api/console-sessions")).body.sessions.find(
    (s) => s.id === "director",
  );

const results = [];
const check = (id, desc, ok, got) => {
  results.push({ id, desc, ok: Boolean(ok), got });
  console.log(`${ok ? "PASS" : "FAIL"}\t${id}\t${desc}\t${JSON.stringify(got)}`);
};

const browser = await chromium.launch();
async function signedIn(viewport, colorScheme = "light", who = "alice") {
  const context = await browser.newContext({ viewport, colorScheme });
  const page = await context.newPage();
  await page.goto(
    `${proxy}/_iapsim/login?email=${who}@example.com&next=/`,
  );
  return { context, page };
}

async function attribution() {
  const [target, closed, filed] = ids;
  for (const [name, width, height] of [
    ["desktop", 1440, 900],
    ["phone", 390, 844],
  ]) {
    for (const scheme of ["light", "dark"]) {
      const { context, page } = await signedIn({ width, height }, scheme);
      const errors = [];
      page.on("pageerror", (e) => errors.push(String(e)));
      const shot = async (what) =>
        page.screenshot({
          path: `${outDir}/${what}-${name}-${scheme}.png`,
          fullPage: false,
        });
      for (const [what, id, text] of [
        ["comment", target, "Director for alice@example.com"],
        ["close", closed, "Director for alice@example.com"],
        ["filed", filed, "Director for alice@example.com"],
      ]) {
        await page.goto(`${proxy}/tasks/${id}`);
        const label = page.getByText(text, { exact: false }).first();
        let visible = false;
        try {
          await label.waitFor({ timeout: 15000 });
          await label.scrollIntoViewIfNeeded();
          visible = await label.isVisible();
        } catch {
          visible = false;
        }
        const box = visible ? await label.boundingBox() : null;
        const overflow = await page.evaluate(
          () => document.documentElement.scrollWidth > window.innerWidth,
        );
        await shot(`task-${what}`);
        check(
          `ui1.${what}.${name}.${scheme}`,
          `/tasks/${id} shows "${text}" inside the viewport, no horizontal overflow`,
          visible && box && box.x >= 0 && box.x + box.width <= width + 0.5 && !overflow,
          { visible, box, overflow },
        );
      }
      check(`ui1.errors.${name}.${scheme}`, "no page errors", errors.length === 0, errors);
      await context.close();
    }
  }
}

async function openDirector(page) {
  await page.goto(`${proxy}/session/director`);
  const toggle = page.getByRole("button", { name: /Director capabilities/ });
  await toggle.first().waitFor({ timeout: 15000 });
  if (!(await page.getByRole("button", { name: /Add grant/ }).first().isVisible())) {
    await toggle.first().click();
  }
  await page.getByRole("button", { name: /Add grant/ }).first().waitFor({ timeout: 15000 });
}

// addGrant opens the session pane's GrantsPicker and ticks the first
// unheld, non-dangerous Grain API scope whose id is not in skip; it
// returns that scope and the statuses of the PUTs the click made.
async function addGrant(page, skip) {
  const puts = [];
  const onResponse = (r) => {
    if (r.request().method() === "PUT" && r.url().includes("/api/console-sessions/director")) {
      puts.push(r.status());
    }
  };
  page.on("response", onResponse);
  await page.getByRole("button", { name: /Add grant/ }).first().click();
  const items = page.locator('[role="menuitemcheckbox"][data-api-kind="scope"][aria-checked="false"]');
  await items.first().waitFor({ timeout: 10000 });
  let chosen = null;
  for (const item of await items.all()) {
    const id = await item.getAttribute("data-api-grant");
    const dangerous = (await item.locator(".api-dangerous").count()) > 0;
    if (!dangerous && !skip.includes(id)) {
      chosen = id;
      await item.click();
      break;
    }
  }
  // Each pick saves at once; wait for the save (and any retry) to settle.
  await page.waitForTimeout(2500);
  await page.keyboard.press("Escape");
  page.off("response", onResponse);
  return { chosen, puts };
}

const holds = (caps, scope) =>
  caps.some((c) => c === `api:${scope}` || c === scope || c.startsWith(`api:${scope}@`));

async function race() {
  // A shipped default the director lacks, so tabB has a banner to click:
  // the first held one whose removal the pane offers back.
  let d = await director();
  let shippedDrop = null;
  for (const c of [...d.capabilities].reverse()) {
    await rest("PUT", "/api/console-sessions/director", {
      capabilities: d.capabilities.filter((x) => x !== c),
      expectCapabilities: d.capabilities,
    });
    const now = await director();
    if ((now.missingShippedDefaults || []).includes(c)) {
      shippedDrop = c;
      d = now;
      break;
    }
    await rest("PUT", "/api/console-sessions/director", { capabilities: d.capabilities, expectCapabilities: now.capabilities });
  }
  check("ui3.0", `director now lacks the shipped default ${shippedDrop}`, shippedDrop !== null, d.missingShippedDefaults);

  const vp = { width: 1440, height: 900 };
  const A = await signedIn(vp, "light", "alice");
  const B = await signedIn(vp, "light", "bob");
  await openDirector(A.page);
  await openDirector(B.page);
  const banner = B.page.getByTestId("adopt-shipped-defaults-banner");
  check("ui3.1", "tabB shows the Adopt shipped defaults banner", await banner.isVisible(), null);

  const a1 = await addGrant(A.page, []);
  d = await director();
  check("ui3.2", `tabA added ${a1.chosen}`, a1.chosen && holds(d.capabilities, a1.chosen) && a1.puts.every((s) => s === 200), { puts: a1.puts });
  await A.page.screenshot({ path: `${outDir}/race-tabA-after-add.png` });

  // tabB, not reloaded, adopts the shipped defaults.
  const adoptPut = B.page.waitForResponse((r) => r.request().method() === "PUT" && r.url().includes("/api/console-sessions/director"));
  await banner.getByRole("button", { name: "Adopt shipped defaults" }).click();
  const adoptReq = (await adoptPut).request();
  const adoptBody = JSON.parse(adoptReq.postData() || "{}");
  await banner.waitFor({ state: "detached", timeout: 15000 });
  d = await director();
  check("ui3.3", "tabB's Adopt sends adoptShippedDefaults:true and no list", adoptBody.adoptShippedDefaults === true && !("capabilities" in adoptBody), adoptBody);
  check("ui3.4", `after tabB's Adopt: ${shippedDrop} back AND tabA's ${a1.chosen} kept`, d.capabilities.includes(shippedDrop) && holds(d.capabilities, a1.chosen), d.capabilities);

  // tabA adds another, so tabB's copy is stale again.
  const a2 = await addGrant(A.page, [a1.chosen]);
  d = await director();
  check("ui3.5", `tabA (its copy predating tabB's Adopt) added ${a2.chosen}`, a2.chosen && holds(d.capabilities, a2.chosen), { puts: a2.puts });

  // tabB adds a grant. Whichever tab saved from a copy older than the
  // session (tabA after tabB's Adopt; tabB if it has not refetched since
  // tabA's second save) is refused 409 "stale", re-reads and retries once.
  const b1 = await addGrant(B.page, [a1.chosen, a2.chosen]);
  d = await director();
  const retried = [a2.puts, b1.puts].filter((p) => JSON.stringify(p) === "[409,200]").length;
  const clean = [a2.puts, b1.puts].every((p) => JSON.stringify(p) === "[200]" || JSON.stringify(p) === "[409,200]");
  check("ui3.6", "a stale save is refused 409 and retried once automatically (200), never failing outright", retried >= 1 && clean, { tabA: a2.puts, tabB: b1.puts });
  check("ui3.7", `after tabB's retry: ${b1.chosen} added, tabA's ${a1.chosen} and ${a2.chosen} kept, shipped defaults kept`,
    holds(d.capabilities, b1.chosen) && holds(d.capabilities, a1.chosen) && holds(d.capabilities, a2.chosen) && d.capabilities.includes(shippedDrop),
    d.capabilities);
  await B.page.screenshot({ path: `${outDir}/race-tabB-after-retry.png` });

  // Double conflict: every PUT tabB sends is made stale by a REST write
  // that lands just before it.
  let n = 0;
  const sneaked = [];
  await B.page.route("**/api/console-sessions/director", async (route) => {
    if (route.request().method() !== "PUT") return route.continue();
    n++;
    const cur = await director();
    const extra = `api:releases:read@acme/widgets`;
    const next = n === 1 ? [...cur.capabilities, extra] : cur.capabilities.filter((c) => c !== extra);
    sneaked.push((await rest("PUT", "/api/console-sessions/director", { capabilities: next, expectCapabilities: cur.capabilities })).status);
    return route.continue();
  });
  const before = (await director()).capabilities;
  const b2 = await addGrant(B.page, [a1.chosen, a2.chosen, b1.chosen]);
  await B.page.unroute("**/api/console-sessions/director");
  d = await director();
  const alert = B.page.getByText(/was changed by somebody else after you opened it/).first();
  const alertVisible = await alert.isVisible().catch(() => false);
  await B.page.screenshot({ path: `${outDir}/race-tabB-double-conflict.png` });
  check("ui3.8", "double conflict: both of tabB's PUTs refused 409, no third try", JSON.stringify(b2.puts) === "[409,409]", { puts: b2.puts, sneaked });
  check("ui3.9", "double conflict: the refusal is shown in tabB", alertVisible, alertVisible ? await alert.innerText() : null);
  check("ui3.10", `double conflict: tabB's ${b2.chosen} not saved, nothing tabA or the REST writer saved is lost`,
    !holds(d.capabilities, b2.chosen) && before.every((c) => d.capabilities.includes(c) || c === "api:releases:read@acme/widgets"),
    { before, after: d.capabilities });
  await A.context.close();
  await B.context.close();
}

try {
  if (mode === "attribution") await attribution();
  else if (mode === "race") await race();
  else throw new Error(`unknown mode ${mode}`);
} finally {
  await browser.close();
  writeFileSync(`${outDir}/${mode}-results.json`, JSON.stringify(results, null, 2));
}
process.exitCode = results.some((r) => !r.ok) ? 1 : 0;
