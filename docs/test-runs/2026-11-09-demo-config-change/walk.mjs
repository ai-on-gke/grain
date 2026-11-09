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

// Walkthrough harness for docs/test-runs/2026-11-09-demo-config-change.md:
// drives the config-change card of a running `grain demo -config-change`
// in Chromium and prints what the API says after each step, as JSON
// lines. Run it from the repository root with ui/'s node_modules
// installed:
//
//   URL=http://127.0.0.1:4291 CODE=<access code> STEP=<step> \
//     PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node docs/test-runs/2026-11-09-demo-config-change/walk.mjs
//
// STEP is one of:
//   layout   measure both cards at 1440x900 and 390x844, light and dark
//   approve  Approve & Apply the poll-interval proposal, then reload
//   reject   Reject the prompt-extension proposal with a reason
//   both     approve both proposals (ORDER=prompt-first flips the order)
//   approve-stale  Approve the poll-interval proposal after main moved
//   viewer   draw the card as a grain/viewer (GET /api/config's access)
import { createRequire } from "node:module";

const require = createRequire(
  new URL("../../../ui/package.json", import.meta.url),
);
const { chromium } = require("@playwright/test");

const URL_ = process.env.URL;
const CODE = process.env.CODE;
const STEP = process.env.STEP || "layout";
const POLL = "Ease off polling between reconcile cycles";
const PROMPT = "Remind every run to run the unit tests";
const auth = { Authorization: `Bearer ${CODE}` };

const log = (step, what) => console.log(JSON.stringify({ step, ...what }));

async function api(path, init = {}) {
  const res = await fetch(URL_ + path, {
    ...init,
    headers: {
      ...auth,
      "Content-Type": "application/json",
      ...(init.headers || {}),
    },
  });
  const text = await res.text();
  let body = text;
  try {
    body = JSON.parse(text);
  } catch {}
  return { status: res.status, body };
}

async function task(title) {
  const { body } = await api("/api/tasks");
  const list = Array.isArray(body) ? body : body.tasks;
  const t = list.find((x) => x.title === title);
  return (await api(`/api/tasks/${t.id}`)).body;
}

async function newPage(
  browser,
  { width = 1440, height = 900, theme = "light" } = {},
) {
  const context = await browser.newContext({
    viewport: { width, height },
    colorScheme: theme,
  });
  const res = await fetch(`${URL_}/api/auth/access-code`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ code: CODE }),
  });
  const host = new URL(URL_).hostname;
  await context.addCookies(
    res.headers.getSetCookie().map((h) => {
      const [pair] = h.split(";");
      const at = pair.indexOf("=");
      return {
        name: pair.slice(0, at),
        value: pair.slice(at + 1),
        domain: host,
        path: "/",
      };
    }),
  );
  await context.addInitScript((t) => {
    try {
      localStorage.setItem("grain.themeMode", t);
    } catch {}
  }, theme);
  return context.newPage();
}

function card(page) {
  return page.getByRole("region", {
    name: "Config change waiting for your approval",
  });
}

async function decide(page, t, verb, reason) {
  await page.goto(`/tasks/${t.id}`.replace(/^/, URL_));
  const c = card(page);
  await c.waitFor();
  const done = page.waitForResponse((r) =>
    r.url().endsWith(`/config-change/${verb}`),
  );
  if (verb === "approve") {
    await c.getByRole("button", { name: "Approve & Apply" }).click();
  } else {
    await c.getByRole("button", { name: "Reject", exact: true }).click();
    await c.getByLabel("Why are you rejecting it?").fill(reason);
    await c.getByRole("button", { name: "Reject change" }).click();
  }
  const r = await done;
  await page.waitForTimeout(800);
  return {
    status: r.status(),
    body: await r.text().catch(() => ""),
    cardCount: await c.count(),
  };
}

async function settingsAndRepo() {
  const s = await api("/api/settings");
  const repo = await api("/api/state-repo");
  const file = await api("/api/state-repo/file?path=tables/grain_config.json");
  const content = file.body.content || "";
  return {
    pollInterval: s.body.pollInterval,
    head: repo.body.head,
    pendingRestart: repo.body.pendingRestart ?? null,
    pollOnMain: (content.match(/"poll_interval_ms": \d+/) || [""])[0],
    promptOnMain: (content.match(/"prompt_extension": "[^"]*"/) || [""])[0],
  };
}

async function main() {
  const browser = await chromium.launch();
  try {
    if (STEP === "layout" || STEP === "viewer") {
      for (const title of [POLL, PROMPT]) {
        const t = await task(title);
        for (const [width, height] of [
          [1440, 900],
          [390, 844],
        ]) {
          for (const theme of ["light", "dark"]) {
            const page = await newPage(browser, { width, height, theme });
            if (STEP === "viewer") {
              await page.route(
                (u) => u.pathname === "/api/config",
                async (route) => {
                  const response = await route.fetch();
                  const json = {
                    ...(await response.json()),
                    access: {
                      role: "grain/viewer",
                      roles: ["grain/viewer"],
                      full: false,
                      scopes: ["settings:read", "tasks:read"],
                    },
                  };
                  await route.fulfill({ response, json });
                },
              );
            }
            await page.goto(`${URL_}/tasks/${t.id}`);
            const c = card(page);
            await c.waitFor();
            const geo = await c.evaluate((el) => {
              const r = el.getBoundingClientRect();
              const diff = el.querySelector(
                "pre, [aria-label='Proposed config change']",
              );
              const buttons = [...el.querySelectorAll("button")].map((b) => {
                const br = b.getBoundingClientRect();
                return {
                  text: b.textContent.trim(),
                  left: Math.round(br.left - r.left),
                  right: Math.round(r.right - br.right),
                  disabled: b.disabled,
                  gated: !!b.closest(".access-gate"),
                };
              });
              const overflowing = [...el.querySelectorAll("*")]
                .filter(
                  (n) =>
                    n.getBoundingClientRect().right > r.right + 1 &&
                    !n.closest("[aria-label='Proposed config change']"),
                )
                .map((n) => n.tagName + "." + n.className)
                .slice(0, 5);
              return {
                cardLeft: Math.round(r.left),
                cardRight: Math.round(r.right),
                cardWidth: Math.round(r.width),
                docScrollWidth: document.documentElement.scrollWidth,
                viewport: innerWidth,
                diffScrollWidth: diff ? diff.scrollWidth : null,
                diffClientWidth: diff ? diff.clientWidth : null,
                diffOverflowX: diff ? getComputedStyle(diff).overflowX : null,
                theme: document.documentElement.dataset.theme || null,
                buttons,
                overflowing,
              };
            });
            log(STEP, { title, width, height, theme: theme, ...geo });
            await page.context().close();
          }
        }
      }
      return;
    }
    const page = await newPage(browser);
    if (STEP === "approve" || STEP === "approve-stale") {
      const before = await settingsAndRepo();
      const t = await task(POLL);
      log(STEP, { before, pending: t.pendingConfigChange?.headSha });
      const r = await decide(page, t, "approve");
      const after = await settingsAndRepo();
      const shown = (await card(page).count())
        ? await card(page).innerText()
        : "";
      const alerts = await page.getByRole("alert").allInnerTexts();
      log(STEP, { response: r, after, cardText: shown.slice(0, 400), alerts });
      await page.reload();
      await page.waitForTimeout(1500);
      log(STEP, {
        afterReloadCardCount: await card(page).count(),
        pendingAfter: !!(await task(POLL)).pendingConfigChange,
      });
      return;
    }
    if (STEP === "reject") {
      const t = await task(PROMPT);
      const r = await decide(
        page,
        t,
        "reject",
        "Not every repository has unit tests",
      );
      const after = await task(PROMPT);
      const comments = (after.comments || []).map((c) => c.body).slice(-2);
      log(STEP, {
        response: { status: r.status, cardCount: r.cardCount },
        state: after.state,
        pending: !!after.pendingConfigChange,
        comments,
        repo: await settingsAndRepo(),
      });
      return;
    }
    if (STEP === "both") {
      const order =
        process.env.ORDER === "prompt-first" ? [PROMPT, POLL] : [POLL, PROMPT];
      for (const title of order) {
        const r = await decide(page, await task(title), "approve");
        log(STEP, {
          title,
          status: r.status,
          cardCount: r.cardCount,
          body: r.status === 200 ? "" : r.body,
        });
      }
      log(STEP, { repo: await settingsAndRepo() });
      return;
    }
    throw new Error(`unknown STEP ${STEP}`);
  } finally {
    await browser.close();
  }
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
