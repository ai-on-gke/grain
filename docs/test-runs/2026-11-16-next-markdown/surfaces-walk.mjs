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

// §6 item 1 on the other [gMarkdown] surfaces: a session
// (/next/sessions/<id>, an interactive task's conversation) and a
// workstream (/next/workstreams/task:<id>, whose goal is the root task's
// description). Both carry the whole hostile corpus. Same listeners and
// probes as mermaid-walk.mjs; prints one line per surface, width and theme.
//
// GRAIN_URL=... GRAIN_CODE=... OUT=... node surfaces-walk.mjs (from ui-ng/)

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");
const BASE = process.env.GRAIN_URL;
const CODE = process.env.GRAIN_CODE;
const OUT = process.env.OUT || "/tmp/md-shots";
const HERE = path.dirname(new URL(import.meta.url).pathname);
fs.mkdirSync(OUT, { recursive: true });

async function api(method, p, body) {
  const res = await fetch(BASE + p, {
    method,
    headers: {
      Authorization: `Bearer ${CODE}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const txt = await res.text();
  if (!res.ok) throw new Error(`${method} ${p}: ${res.status} ${txt}`);
  return txt ? JSON.parse(txt) : null;
}

const corpus = JSON.parse(fs.readFileSync(path.join(HERE, "corpus.json")));
const all = Object.values(corpus).join("\n\n");
const task = await api("POST", "/api/tasks", {
  title: "Mermaid corpus session",
  description: all,
  interactive: true,
});
await api("POST", `/api/tasks/${task.id}/comments`, { body: all });
// A session is a console session over a task: the demo's Director
// (task SESSION_TASK, default 13) carries the corpus in its chat.
const sessionTask = process.env.SESSION_TASK || "13";
await api("POST", `/api/tasks/${sessionTask}/comments`, { body: all });
console.log("task", task.id, "session task", sessionTask);

const browser = await chromium.launch();
const surfaces = [
  {
    name: "session",
    route: `next/sessions/${process.env.SESSION_ID || "director"}`,
  },
  {
    name: "workstream",
    route: `next/workstreams/${encodeURIComponent(`task:${task.id}`)}`,
    expand: "Workstream goal",
  },
];
for (const width of [1440, 390]) {
  for (const theme of ["light", "dark"]) {
    for (const s of surfaces) {
      const ctx = await browser.newContext({
        viewport: { width, height: width > 600 ? 900 : 844 },
        colorScheme: theme,
      });
      await ctx.addInitScript(
        ([t]) => {
          localStorage.setItem("grain.themeMode", t);
          window.__pwned = 0;
          window.alert =
            window.confirm =
            window.prompt =
              () => {
                window.__pwned++;
              };
          document.addEventListener("securitypolicyviolation", (e) =>
            console.log(`CSP ${e.violatedDirective} ${e.blockedURI}`),
          );
        },
        [theme],
      );
      await ctx.setExtraHTTPHeaders({ Authorization: `Bearer ${CODE}` });
      const page = await ctx.newPage();
      const log = [];
      page.on("console", (m) => {
        if (
          m.type() === "error" ||
          m.type() === "warning" ||
          /CSP/.test(m.text())
        )
          log.push(`${m.type()}: ${m.text()}`);
      });
      page.on("pageerror", (e) => log.push(`pageerror: ${e.message}`));
      page.on("dialog", (d) => {
        log.push(`DIALOG: ${d.message()}`);
        d.dismiss();
      });
      await page.goto(`${BASE}/${s.route}`);
      if (s.expand) {
        await page
          .getByRole("button", { name: s.expand })
          .click({ timeout: 15_000 })
          .catch((e) => log.push(`expand: ${e.message.split("\n")[0]}`));
      }
      await page
        .waitForFunction(
          () => document.querySelectorAll(".mermaid-canvas svg").length >= 7,
          null,
          { timeout: 60_000 },
        )
        .catch(() => log.push("timeout waiting for 7 diagrams"));
      for (const n of await page.locator(".mermaid-canvas svg .node").all()) {
        await n.click({ timeout: 1000, force: true }).catch(() => {});
      }
      const probe = await page.evaluate(() => {
        const svgs = Array.from(
          document.querySelectorAll(".mermaid-canvas svg"),
        );
        return {
          blocks: document.querySelectorAll("[data-mermaid]").length,
          svgs: svgs.length,
          bad: svgs.flatMap((sv) =>
            Array.from(
              sv.querySelectorAll(
                "script, foreignObject, image, iframe, [onclick], [onerror], [onload]",
              ),
            ).map((e) => e.outerHTML.slice(0, 120)),
          ).length,
          errors: document.querySelectorAll(".mermaid-error").length,
          pwned: window.__pwned,
          overflow: document.documentElement.scrollWidth - window.innerWidth,
        };
      });
      await page.screenshot({ path: `${OUT}/${s.name}-${width}-${theme}.png` });
      console.log(
        `${s.name} ${width} ${theme}: ${JSON.stringify(probe)} log=${JSON.stringify(log)}`,
      );
      await ctx.close();
    }
  }
}
await browser.close();
