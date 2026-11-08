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

// Item 4 of docs/test-plans/2026-11-07.md §3: walk the UI in a real
// Chromium as verified (IAP-signed) people of different roles, against the
// daemon walkthrough.sh leaves up with HOLD=1. Every page is visited at
// 1440x900 and 390x844, light and dark. For each visit it records:
//
//   - every 403 (and any other 4xx/5xx) the page or its background polling
//     drew, while the page sat idle for IDLE_MS;
//   - whether an error toast (ErrorBanner's filled MUI Alert) showed;
//   - horizontal overflow (document wider than the viewport);
//   - bounding-box stability: header/nav/main and the first buttons of the
//     page, measured on arrival and again after IDLE_MS of polling;
//   - keyboard navigation: Tab moves focus through distinct, on-screen
//     elements;
//   - the write controls offered: enabled vs disabled buttons named for a
//     write (New task, Edit, Approve, ...).
//
//   node browser-walkthrough.mjs <proxy-base> <out-dir> <email=label> ...
//
// It writes results.json, a screenshot per visit and contact-sheet.png.
import { createRequire } from "node:module";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const uiDir = process.env.UI_DIR || path.resolve(here, "../../../ui");
const { chromium } = createRequire(path.join(uiDir, "package.json"))(
  "@playwright/test",
);

const [base, out, ...people] = process.argv.slice(2);
if (!base || !out || people.length === 0) {
  console.error(
    "usage: browser-walkthrough.mjs <base> <out> <email=label> ...",
  );
  process.exit(2);
}
const IDLE_MS = Number(process.env.IDLE_MS || 4000);
const VIEWPORTS = [
  { name: "1440", width: 1440, height: 900 },
  { name: "390", width: 390, height: 844 },
];
const SCHEMES = ["light", "dark"];
const PAGES = (
  process.env.PAGES ||
  "/,/boards,/boards/@first,/tasks/@first,/@bulk,/new,/inbox,/repos,/processes,/settings,/settings/access,/system"
)
  .split(",")
  .filter(Boolean);
// Buttons whose name says they write. A viewer must see none enabled.
const WRITE_NAMES =
  /^(\+ New task|New task|New board|Edit|Approve|Approve all.*|Defer|Decline|Reply|Acknowledge|Investigate|Run now|Merge|Force merge|Save|Add|Allow|Rotate|Delete|Cancel task|Retry|Dispatch|Bulk edit|Apply)$/i;

await fs.mkdir(out, { recursive: true });
const browser = await chromium.launch();
const results = [];
const shots = [];

async function firstId(ctx, api, pick) {
  const r = await ctx.request.get(`${base}${api}`);
  if (!r.ok()) return null;
  return pick(await r.json());
}

async function measure(page) {
  return page.evaluate(() => {
    const sel = [
      "header",
      "nav",
      "main",
      "[data-testid=page-header]",
      "main button",
    ];
    const boxes = [];
    for (const s of sel) {
      const els = Array.from(document.querySelectorAll(s)).slice(0, 12);
      els.forEach((el, i) => {
        const b = el.getBoundingClientRect();
        if (b.width === 0 && b.height === 0) return;
        boxes.push({
          key: `${s}#${i}`,
          x: b.x,
          y: b.y,
          w: b.width,
          h: b.height,
        });
      });
    }
    return boxes;
  });
}

function maxShift(a, b) {
  const byKey = new Map(b.map((x) => [x.key, x]));
  let worst = { d: 0, key: "" };
  for (const x of a) {
    const y = byKey.get(x.key);
    if (!y) continue;
    const d = Math.max(
      Math.abs(x.x - y.x),
      Math.abs(x.y - y.y),
      Math.abs(x.w - y.w),
      Math.abs(x.h - y.h),
    );
    if (d > worst.d) worst = { d: Math.round(d * 10) / 10, key: x.key };
  }
  return worst;
}

async function tabWalk(page, presses) {
  const seen = [];
  for (let i = 0; i < presses; i++) {
    await page.keyboard.press("Tab");
    seen.push(
      await page.evaluate(() => {
        const el = document.activeElement;
        if (!el || el === document.body) return null;
        const b = el.getBoundingClientRect();
        const name =
          el.getAttribute("aria-label") ||
          (el.textContent || "").trim().slice(0, 30) ||
          el.tagName;
        return {
          name,
          visible:
            b.width > 0 &&
            b.height > 0 &&
            b.bottom > 0 &&
            b.right > 0 &&
            b.top < window.innerHeight &&
            b.left < window.innerWidth,
        };
      }),
    );
  }
  const focused = seen.filter(Boolean);
  return {
    distinct: new Set(focused.map((f) => f.name)).size,
    offscreen: focused.filter((f) => !f.visible).length,
  };
}

for (const person of people) {
  const [email, label] = person.split("=");
  for (const vp of VIEWPORTS) {
    for (const scheme of SCHEMES) {
      const ctx = await browser.newContext({
        viewport: { width: vp.width, height: vp.height },
        colorScheme: scheme,
      });
      // Sign in at the IAP stand-in: every request after this carries a
      // freshly signed X-Goog-IAP-JWT-Assertion for this person.
      await ctx.request.get(
        `${base}/_iapsim/login?email=${encodeURIComponent(email)}&next=/api/auth/whoami`,
      );
      const page = await ctx.newPage();
      await page.goto(
        `${base}/_iapsim/login?email=${encodeURIComponent(email)}&next=/api/auth/whoami`,
      );
      const whoami = JSON.parse(await page.locator("body").innerText());
      const cfg = await (await ctx.request.get(`${base}/api/config`)).json();
      whoami.roles = cfg.access?.full ? ["(full)"] : cfg.access?.roles;
      const boardId = await firstId(ctx, "/api/boards", (j) => {
        const all = Array.isArray(j) ? j : j.boards || [];
        return (all.find((b) => b.kind === "kanban") || all[0])?.id;
      });
      const taskId = await firstId(
        ctx,
        "/api/tasks?limit=5",
        (j) => (Array.isArray(j) ? j : j.tasks || [])[0]?.id,
      );
      let bad = [];
      const consoleErrors = [];
      page.on("response", (r) => {
        if (r.status() >= 400 && r.url().startsWith(base)) {
          bad.push({
            status: r.status(),
            method: r.request().method(),
            path: new URL(r.url()).pathname,
          });
        }
      });
      page.on("console", (m) => {
        if (m.type() === "error") consoleErrors.push(m.text().slice(0, 200));
      });
      for (const p of PAGES) {
        let target = p
          .replace("/boards/@first", boardId ? `/boards/${boardId}` : "")
          .replace("/tasks/@first", taskId ? `/tasks/${taskId}` : "");
        if (target === "") continue;
        const bulk = target === "/@bulk";
        if (bulk) target = "/";
        bad = [];
        consoleErrors.length = 0;
        const res = await page.goto(`${base}${target}`, {
          waitUntil: "domcontentloaded",
        });
        await page
          .locator("main, [role=main]")
          .first()
          .waitFor({ timeout: 15000 })
          .catch(() => {});
        await page.waitForTimeout(1200);
        let bulkNote = null;
        if (bulk) {
          // Bulk edit: select the first two rows' checkboxes, if offered,
          // and read the bulk bar's controls.
          const boxes = page.locator(
            "main input[type=checkbox]:visible, main [role=checkbox]:visible",
          );
          const n = await boxes.count();
          for (let i = 0; i < Math.min(2, n); i++) {
            await boxes
              .nth(i)
              .click({ trial: false })
              .catch(() => {});
          }
          bulkNote = `${n} selectable rows offered`;
        }
        const before = await measure(page);
        await page.waitForTimeout(IDLE_MS);
        const after = await measure(page);
        const shift = maxShift(before, after);
        const toast = await page
          .locator(".MuiSnackbar-root .MuiAlert-filledError")
          .allInnerTexts();
        const overflow = await page.evaluate(
          () => document.documentElement.scrollWidth - window.innerWidth,
        );
        const writes = await page.evaluate(
          (re) => {
            const rx = new RegExp(re.source, re.flags);
            const out = { enabled: [], disabled: [] };
            for (const b of document.querySelectorAll(
              "button, [role=button]",
            )) {
              const name = (
                b.getAttribute("aria-label") ||
                b.textContent ||
                ""
              ).trim();
              if (!rx.test(name)) continue;
              const r = b.getBoundingClientRect();
              if (r.width === 0 || r.height === 0) continue;
              const off =
                b.disabled || b.getAttribute("aria-disabled") === "true";
              (off ? out.disabled : out.enabled).push(name);
            }
            return out;
          },
          { source: WRITE_NAMES.source, flags: WRITE_NAMES.flags },
        );
        const slug = `${label}-${vp.name}-${scheme}-${(p.replace(/[^a-z0-9]+/gi, "_") || "root").replace(/^_|_$/g, "") || "root"}`;
        const file = path.join(out, `${slug}.png`);
        await page.screenshot({ path: file });
        shots.push({ file, name: slug });
        const keys = await tabWalk(page, 12);
        results.push({
          who: email,
          label,
          principal: whoami.principal,
          roles: whoami.roles || null,
          viewport: vp.name,
          scheme,
          page: p,
          url: target,
          status: res ? res.status() : null,
          errors: bad,
          forbidden: bad.filter((b) => b.status === 403),
          toast,
          overflow,
          shift,
          keys,
          writes,
          bulk: bulkNote,
          consoleErrors: [...consoleErrors],
        });
        console.log(
          `${slug}: ${bad.filter((b) => b.status === 403).length}x403 toast=${toast.length} overflow=${overflow} shift=${shift.d}@${shift.key} tab=${keys.distinct}/${keys.offscreen} writes+=${writes.enabled.join("|")}`,
        );
      }
      await ctx.close();
    }
  }
}

// One contact sheet per person and width keeps each image readable.
const sheet = path.join(out, "contact-sheet.png");
{
  const pick = shots.filter((s) =>
    /-(light|dark)-(root|boards|tasks_first|new|settings_access|system)$/.test(
      s.name,
    ),
  );
  const cells = await Promise.all(
    pick.map(async (shot) => {
      const data = (await fs.readFile(shot.file)).toString("base64");
      return `<figure><img src="data:image/png;base64,${data}"><figcaption>${shot.name}</figcaption></figure>`;
    }),
  );
  const page = await browser.newPage({
    viewport: { width: 2000, height: 800 },
  });
  await page.setContent(`<!doctype html><style>
    body { margin: 0; padding: 12px; background: #888; font: 13px sans-serif;
      display: flex; flex-wrap: wrap; gap: 12px; align-items: flex-start; }
    figure { margin: 0; }
    img { display: block; max-width: 360px; max-height: 420px; border: 1px solid #333; }
    figcaption { padding-top: 4px; }
  </style>${cells.join("")}`);
  await page.screenshot({ path: sheet, fullPage: true });
  await page.close();
}
await fs.writeFile(
  path.join(out, "results.json"),
  JSON.stringify(results, null, 2),
);
await browser.close();
