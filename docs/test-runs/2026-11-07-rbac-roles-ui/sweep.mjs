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

// docs/test-plans/2026-11-07.md §3 item 4, the interaction half: walk the
// role-gated UI as IAP-verified people of limited roles against the daemon
// docs/test-runs/2026-11-07-rbac-roles/walkthrough.sh leaves up with
// HOLD=1, and *use* each write surface rather than only reading it:
//
//   - `/` and the kanban board: drag the first row/card onto the second
//     (HTML5 drag), then tick two rows and read the bulk selection bar;
//   - task detail, the New Task drawer (/new), /repos, /processes (and the
//     first template, schedule and suite it lists), /settings,
//     /settings/access and /system;
//   - /inbox: Approve on a proposal, Grant on an access request and Reply
//     on an asked task. The access request is the one thing a held daemon
//     cannot make on its own (no run ever asks), so a real task's detail is
//     answered as parked on one; the Grant click itself goes to the real
//     server.
//
// Each visit records every 4xx/5xx the page drew (clicks and background
// polling, IDLE_MS idle), error toasts and console errors; empty toolbars
// (a row of controls drawn with nothing visible in it); refused-control
// wrappers that are not the size of their button or fall out of their
// siblings' row; and a Tab walk (focus lost to <body> mid-page, landing
// off-screen or on something invisible). A baseline session holding the
// access code (unlimited) visits the same pages, and every button present
// on both pages is compared by box, so a hidden or wrapped control that
// shifts its siblings shows up as a delta.
//
//   node sweep.mjs <proxy-base> <daemon-base> <access-code> <out> <email=label> ...
import { createRequire } from "node:module";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const uiDir = process.env.UI_DIR || path.resolve(here, "../../../ui");
const { chromium } = createRequire(path.join(uiDir, "package.json"))(
  "@playwright/test",
);

const [base, daemon, code, out, ...people] = process.argv.slice(2);
if (!base || !daemon || !code || !out || people.length === 0) {
  console.error(
    "usage: sweep.mjs <proxy-base> <daemon-base> <access-code> <out> <email=label> ...",
  );
  process.exit(2);
}
const IDLE_MS = Number(process.env.IDLE_MS || 3000);
const VIEWPORTS = [
  { name: "1440", width: 1440, height: 900 },
  { name: "390", width: 390, height: 844 },
];
const SCHEMES = (process.env.SCHEMES || "light,dark").split(",");
const ACCESS_REQUEST = {
  summary: "Grant write to acme/lib so the fix lands in both repositories",
  reason: "Role-gated UI sweep fixture.",
  createdAt: "2026-11-08T07:00:00Z",
  items: [
    { kind: "read", repo: "acme/docs" },
    { kind: "network", access: "open" },
  ],
  holds: { reads: [], writes: [], grants: [], networkAccess: "" },
  earlier: [],
};

await fs.mkdir(out, { recursive: true });
const browser = await chromium.launch();
const results = [];
const shots = [];

// The pages, each with what to do there. `do` runs after the page settles
// and before the idle; it returns a note for the report.
const PAGES = [
  { id: "root", url: "/", do: dragFirstOntoSecond },
  { id: "bulk", url: "/", do: bulkSelect },
  { id: "kanban", url: "/boards/@kanban", do: dragFirstOntoSecond },
  { id: "task", url: "/tasks/@proposed" },
  { id: "task-grant", url: "/tasks/@grant", stage: true, do: grant },
  { id: "new", url: "/new" },
  { id: "inbox-approve", url: "/inbox", do: inboxApprove },
  { id: "inbox-grant", url: "/inbox", stage: true, do: inboxGrant },
  { id: "inbox-reply", url: "/inbox", stage: true, do: inboxReply },
  { id: "repos", url: "/repos" },
  { id: "processes", url: "/processes" },
  { id: "template", url: "/processes", do: openRow("Weekly triage", 0) },
  { id: "schedule", url: "/processes", do: openRow("Weekly triage", 1) },
  { id: "suite", url: "/processes", do: openRow("nightly", 0) },
  { id: "settings", url: "/settings" },
  { id: "settings-access", url: "/settings/access" },
  { id: "system", url: "/system" },
];

async function enabled(loc) {
  return (await loc.count()) > 0 && (await loc.first().isEnabled());
}

// dragFirstOntoSecond drags the first draggable row (or card) onto the
// second, the gesture `/` and a kanban column offer for reordering.
async function dragFirstOntoSecond(page) {
  const rows = page.locator("main [draggable=true]");
  const n = await rows.count();
  if (n < 2) return `draggable=${n}`;
  await rows
    .nth(0)
    .dragTo(rows.nth(1), { timeout: 5000 })
    .catch(() => {});
  return `draggable=${n} dragged`;
}

async function bulkSelect(page) {
  const boxes = page.locator("main input[type=checkbox]:visible");
  const n = await boxes.count();
  for (let i = 0; i < Math.min(2, n); i++) {
    await boxes
      .nth(i)
      .check({ timeout: 3000 })
      .catch(() => {});
  }
  // BatchActionsBar: the fixed Paper that says "N selected".
  const bar = page.locator(".MuiPaper-root", { hasText: /^\d+ selected/ }).last();
  if (!(await bar.count())) return `checkboxes=${n} no bar`;
  const info = await bar.evaluate((el) => {
    const out = { enabled: [], refused: [], overflow: el.scrollWidth - el.clientWidth };
    for (const b of el.querySelectorAll("button")) {
      const r = b.getBoundingClientRect();
      if (r.width === 0) continue;
      const name = (b.getAttribute("aria-label") || b.textContent).trim();
      (b.disabled ? out.refused : out.enabled).push(name);
    }
    return out;
  });
  return `checkboxes=${n} bar: enabled [${info.enabled.join(", ")}] refused [${info.refused.join(", ")}] overflow ${info.overflow}px`;
}

function group(page, title) {
  return page.locator(".inbox-group", { has: page.getByText(title) });
}

async function inboxApprove(page) {
  const b = group(page, "Approve a proposal")
    .locator(".inbox-item")
    .first()
    .getByRole("button", { name: "Approve", exact: true });
  if (!(await b.count())) return "no Approve";
  if (!(await enabled(b))) return "Approve disabled";
  await b.click();
  return "Approve clicked";
}

// grant answers the access request card on the page, if it is offered.
async function grant(page) {
  const b = page.getByRole("button", { name: /^Grant (all|\d)/ }).first();
  await b.waitFor({ timeout: 3000 }).catch(() => {});
  if (!(await b.count())) return "no Grant";
  if (!(await enabled(b))) return "Grant disabled";
  await b.click();
  return "Grant clicked";
}

// openAsked opens the Inbox's responder on the row for id: Reply is what
// opens it, for an access request as for a question.
async function openAsked(page, id) {
  const row = page.locator(".inbox-item", { hasText: tasks.find((t) => t.id === id)?.title || "-" }).first();
  const reply = row.getByRole("button", { name: "Reply", exact: true });
  if (!(await reply.count())) return "no Reply";
  if (!(await enabled(reply))) return "Reply disabled";
  await reply.click();
  return null;
}

async function inboxGrant(page) {
  const opened = await openAsked(page, grantId);
  if (opened) return opened;
  return `Reply opened; ${await grant(page)}`;
}

async function inboxReply(page) {
  const opened = await openAsked(page, replyId);
  if (opened) return opened;
  const box = page.getByLabel(`Reply to ${replyId}`);
  await box.waitFor({ timeout: 3000 }).catch(() => {});
  if (!(await box.count())) return "Reply opened; no box";
  await box.fill("sweep reply");
  const send = page.getByRole("button", { name: /^Send/ }).first();
  if (!(await enabled(send))) return "Reply opened; Send disabled";
  await send.click();
  return "Reply sent";
}

function openRow(text, nth) {
  return async (page) => {
    const row = page.getByText(text, { exact: true }).nth(nth);
    if (!(await row.count())) return `no ${text}#${nth}`;
    await row.click({ timeout: 3000 }).catch(() => {});
    await page.waitForTimeout(800);
    return `opened ${text}#${nth}`;
  };
}

// layout reads, in the page, what a refused control can break: empty
// toolbars, gate wrappers that do not wrap their button tightly or that
// fall out of their row, and every button's box by name for the baseline
// comparison.
async function layout(page) {
  return page.evaluate(() => {
    const visible = (el) => {
      const b = el.getBoundingClientRect();
      const cs = getComputedStyle(el);
      return (
        b.width > 0 &&
        b.height > 0 &&
        cs.visibility !== "hidden" &&
        cs.display !== "none" &&
        Number(cs.opacity) > 0
      );
    };
    const empty = [];
    const bars = document.querySelectorAll(
      "[role=toolbar], [data-testid=page-header] > *, [class*=actions], [class*=toolbar], .MuiCardActions-root, .MuiDialogActions-root",
    );
    for (const el of bars) {
      if (!visible(el)) continue;
      const kids = Array.from(el.querySelectorAll("*")).filter(
        (k) => visible(k) && (k.matches("button, a, input, [role=button], svg") || (k.childElementCount === 0 && k.textContent.trim())),
      );
      if (kids.length === 0) {
        const b = el.getBoundingClientRect();
        empty.push(`${el.className || el.getAttribute("role")}`.slice(0, 60) + ` ${Math.round(b.width)}x${Math.round(b.height)}`);
      }
    }
    const gates = [];
    for (const g of document.querySelectorAll(".access-gate")) {
      if (!visible(g)) continue;
      const inner = g.firstElementChild;
      const gb = g.getBoundingClientRect();
      const ib = inner.getBoundingClientRect();
      const name = (inner.getAttribute("aria-label") || inner.textContent || "").trim().slice(0, 30);
      const loose = Math.max(Math.abs(gb.width - ib.width), Math.abs(gb.height - ib.height));
      // The nearest visible sibling in a row: a refused control must sit
      // on its line (same vertical centre).
      let offRow = 0;
      const parent = g.parentElement;
      const pcs = getComputedStyle(parent);
      if (pcs.display.includes("flex") && !pcs.flexDirection.startsWith("column") && pcs.flexWrap === "nowrap") {
        for (const s of parent.children) {
          if (s === g || !visible(s)) continue;
          const sb = s.getBoundingClientRect();
          offRow = Math.max(offRow, Math.abs(sb.y + sb.height / 2 - (gb.y + gb.height / 2)));
        }
      }
      gates.push({ name, scope: g.dataset.accessDenied, loose: Math.round(loose), offRow: Math.round(offRow) });
    }
    const boxes = {};
    const counts = {};
    for (const b of document.querySelectorAll("button, [role=button], [role=tab]")) {
      if (!visible(b)) continue;
      const name = (b.getAttribute("aria-label") || b.textContent || "").trim().slice(0, 40);
      if (!name) continue;
      counts[name] = (counts[name] || 0) + 1;
      const r = b.getBoundingClientRect();
      boxes[name] = { x: r.x, y: r.y + window.scrollY, w: r.width, h: r.height };
    }
    for (const [k, c] of Object.entries(counts)) if (c > 1) delete boxes[k];
    const overflow = document.documentElement.scrollWidth - window.innerWidth;
    return { empty, gates, boxes, overflow };
  });
}

async function tabWalk(page, presses) {
  await page.locator("body").click({ position: { x: 1, y: 1 } }).catch(() => {});
  const seen = [];
  for (let i = 0; i < presses; i++) {
    await page.keyboard.press("Tab");
    seen.push(
      await page.evaluate(() => {
        const el = document.activeElement;
        if (!el || el === document.body) return { lost: true };
        // A MUI checkbox or switch focuses its input, which is drawn
        // transparent over the visible control: judge the control.
        const shown = (el.matches("input") && el.closest(".MuiButtonBase-root")) || el;
        const b = shown.getBoundingClientRect();
        const cs = getComputedStyle(shown);
        const name = (el.getAttribute("aria-label") || (el.textContent || "").trim().slice(0, 30) || el.tagName);
        return {
          name,
          gate: el.classList.contains("access-gate"),
          offscreen: !(b.bottom > 0 && b.right > 0 && b.top < window.innerHeight && b.left < window.innerWidth),
          invisible: b.width === 0 || b.height === 0 || cs.visibility === "hidden" || Number(cs.opacity) === 0,
          disabled: el.disabled === true,
        };
      }),
    );
  }
  // Losing focus to <body> once at the end of the page's tab order is the
  // browser wrapping round; losing it before then is a broken stop.
  const firstLost = seen.findIndex((s) => s.lost);
  const afterLost = firstLost >= 0 ? seen.slice(firstLost + 1).filter((s) => !s.lost) : [];
  const lostMid = firstLost >= 0 && afterLost.length > 0 && firstLost < presses - 1 && seen.slice(0, firstLost).length < 3;
  const focused = seen.filter((s) => !s.lost);
  return {
    distinct: new Set(focused.map((f) => f.name)).size,
    offscreen: focused.filter((f) => f.offscreen).map((f) => f.name),
    invisible: focused.filter((f) => f.invisible).map((f) => f.name),
    disabled: focused.filter((f) => f.disabled).map((f) => f.name),
    gates: focused.filter((f) => f.gate).length,
    lostMid,
  };
}

async function newContext(vp, scheme, extra = {}) {
  return browser.newContext({
    viewport: { width: vp.width, height: vp.height },
    colorScheme: scheme,
    ...extra,
  });
}

// ids: the first kanban board, a proposed task and the asked task.
const admin = await browser.newContext({
  extraHTTPHeaders: { Authorization: `Bearer ${code}` },
});
const boards = await (await admin.request.get(`${daemon}/api/boards`)).json();
const kanban = (Array.isArray(boards) ? boards : boards.boards || []).find((b) => b.kind === "kanban")?.id;
const tl = await (await admin.request.get(`${daemon}/api/tasks`)).json();
const tasks = Array.isArray(tl) ? tl : tl.tasks || [];
const proposals = tasks.filter((t) => t.state === "proposed").map((t) => t.id);
const proposed = proposals[0];
const grantId = proposals[proposals.length - 2];
const replyId = proposals[proposals.length - 1];
await admin.close();

function resolve(url) {
  return url
    .replace("@kanban", kanban || "")
    .replace("@proposed", proposed || "")
    .replace("@grant", grantId || "");
}

// stage presents two real proposed tasks, on this page alone, the way a
// run parks: one asked a question (Reply) and one asked for access
// (Grant). Only GET responses are rewritten; a Reply or a Grant still goes
// to the real server, whose answer is what gets recorded.
async function stage(page) {
  const patch = (t) =>
    t.id === grantId
      ? { ...t, state: "awaiting_reply", awaitingAccess: true, pendingAccess: ACCESS_REQUEST, comments: t.comments || [] }
      : t.id === replyId
        ? {
            ...t,
            state: "awaiting_reply",
            questionSummary: "Which base branch?",
            comments: [{ id: "c1", body: "Which base branch should this land on?", author: "agent", authorKind: "agent", createdAt: "2026-11-08T07:00:00Z" }],
          }
        : t;
  await page.route(
    (u) => u.pathname === "/api/tasks" || u.pathname === `/api/tasks/${grantId}` || u.pathname === `/api/tasks/${replyId}`,
    async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      const headers = { ...route.request().headers() };
      delete headers["if-none-match"];
      const response = await route.fetch({ headers });
      const body = await response.json();
      const json = Array.isArray(body)
        ? body.map(patch)
        : body.tasks
          ? { ...body, tasks: body.tasks.map(patch) }
          : patch(body);
      await route.fulfill({ response, json });
    },
  );
}

async function visit(page, p, origin) {
  const res = await page.goto(`${origin}${resolve(p.url)}`, { waitUntil: "domcontentloaded" });
  await page.locator("main, [role=main]").first().waitFor({ timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(1200);
  return res;
}

// Baseline: the access code (unlimited) on the daemon itself, per viewport.
const baseline = {};
const baselineEmpty = {};
for (const vp of VIEWPORTS) {
  const ctx = await newContext(vp, "light", { extraHTTPHeaders: { Authorization: `Bearer ${code}` } });
  const page = await ctx.newPage();
  for (const p of PAGES) {
    // No clicks for the baseline -- it would answer what the people then
    // find -- except opening an overlay and ticking the bulk rows.
    if (p.stage) await stage(page);
    await visit(page, p, daemon);
    if (["template", "schedule", "suite"].includes(p.id)) await p.do(page);
    if (p.id === "bulk") await bulkSelect(page);
    const lay = await layout(page);
    baseline[`${vp.name}/${p.id}`] = lay.boxes;
    baselineEmpty[`${vp.name}/${p.id}`] = lay.empty.map((e) => e.split(" ")[0]);
    if (p.stage) await page.unrouteAll({ behavior: "ignoreErrors" });
  }
  await ctx.close();
}

for (const person of people) {
  const [email, label] = person.split("=");
  for (const vp of VIEWPORTS) {
    for (const scheme of SCHEMES) {
      const ctx = await newContext(vp, scheme);
      const page = await ctx.newPage();
      await page.goto(`${base}/_iapsim/login?email=${encodeURIComponent(email)}&next=/api/auth/whoami`);
      const cfg = await (await ctx.request.get(`${base}/api/config`)).json();
      const roles = cfg.access?.full ? ["(full)"] : cfg.access?.roles;
      let bad = [];
      const consoleErrors = [];
      page.on("response", (r) => {
        if (r.status() >= 400 && r.url().startsWith(base)) {
          bad.push({ status: r.status(), method: r.request().method(), path: new URL(r.url()).pathname });
        }
      });
      page.on("console", (m) => {
        if (m.type() === "error") consoleErrors.push(m.text().slice(0, 200));
      });
      for (const p of PAGES) {
        bad = [];
        consoleErrors.length = 0;
        if (p.stage) await stage(page);
        const res = await visit(page, p, base);
        // Clicks once per person (1440 light); every other visit reads.
        const acts = vp.name === "1440" && scheme === SCHEMES[0];
        let note = null;
        if (p.do && (acts || ["template", "schedule", "suite", "bulk"].includes(p.id))) {
          note = await p.do(page).catch((e) => `error: ${e.message.split("\n")[0]}`);
        }
        await page.waitForTimeout(IDLE_MS);
        const toast = await page.locator(".MuiSnackbar-root .MuiAlert-filledError, .MuiAlert-standardError:visible").allInnerTexts();
        const lay = await layout(page);
        const ref = baseline[`${vp.name}/${p.id}`] || {};
        const shifted = [];
        for (const [name, b] of Object.entries(lay.boxes)) {
          const a = ref[name];
          if (!a) continue;
          const d = Math.max(Math.abs(a.x - b.x), Math.abs(a.y - b.y), Math.abs(a.w - b.w), Math.abs(a.h - b.h));
          if (d > 2) shifted.push(`${name} ${Math.round(d)}px`);
        }
        const slug = `${label}-${vp.name}-${scheme}-${p.id}`;
        const file = path.join(out, `${slug}.png`);
        await page.screenshot({ path: file });
        shots.push({ file, name: slug });
        const keys = await tabWalk(page, 25);
        const forbidden = bad.filter((b) => b.status === 403);
        const r = {
          who: email, label, roles, viewport: vp.name, scheme, page: p.id,
          url: resolve(p.url), status: res ? res.status() : null, note,
          forbidden, errors: bad.filter((b) => b.status !== 403),
          toast, consoleErrors: [...consoleErrors],
          overflow: lay.overflow,
          // An empty bar the unlimited caller sees too is the page's own
          // layout; one only the limited role sees is a hidden control's.
          empty: lay.empty.filter((e) => !(baselineEmpty[`${vp.name}/${p.id}`] || []).includes(e.split(" ")[0])),
          emptyAlsoForAdmin: lay.empty.filter((e) => (baselineEmpty[`${vp.name}/${p.id}`] || []).includes(e.split(" ")[0])),
          gates: lay.gates,
          gateIssues: lay.gates.filter((g) => g.loose > 1 || g.offRow > 3),
          shifted, keys,
        };
        results.push(r);
        console.log(
          `${slug}: ${note || ""} 403=${forbidden.map((f) => f.method + " " + f.path).join(",")} toast=${toast.length} console=${consoleErrors.length} empty=${r.empty.length} gates=${lay.gates.length}/${r.gateIssues.length} shifted=${shifted.length} tab=${keys.distinct} off=${keys.offscreen.length} inv=${keys.invisible.length} lost=${keys.lostMid}`,
        );
        if (p.stage) await page.unrouteAll({ behavior: "ignoreErrors" });
      }
      await ctx.close();
    }
  }
}

const sheet = path.join(out, "contact-sheet.png");
{
  const pick = shots.filter((s) => new RegExp(process.env.SHEET || "-(root|task|task-grant|inbox-grant|template)$").test(s.name));
  const cells = await Promise.all(
    pick.map(async (shot) => {
      const data = (await fs.readFile(shot.file)).toString("base64");
      return `<figure><img src="data:image/png;base64,${data}"><figcaption>${shot.name}</figcaption></figure>`;
    }),
  );
  const page = await browser.newPage({ viewport: { width: 2000, height: 800 } });
  await page.setContent(`<!doctype html><style>
    body { margin: 0; padding: 12px; background: #888; font: 13px sans-serif;
      display: flex; flex-wrap: wrap; gap: 12px; align-items: flex-start; }
    figure { margin: 0; }
    img { display: block; max-width: 300px; max-height: 360px; border: 1px solid #333; }
    figcaption { padding-top: 4px; }
  </style>${cells.join("")}`);
  await page.screenshot({ path: sheet, fullPage: true });
  await page.close();
}
await fs.writeFile(path.join(out, "results.json"), JSON.stringify(results, null, 2));
await browser.close();
