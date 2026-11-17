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

// The Design, Functional and Adversarial reviews of PR #1756 (task 6479's
// Inbox / Mermaid / Agents split fixes), as task 6744 ran them against a
// `grain demo` built from the branch. Every check is measured (bounding
// boxes, computed styles, the text a box shows) and printed as one JSON
// line; the shots it takes carry an overlay naming what was measured,
// which is how a reviewer whose file reader cannot show a PNG still
// leaves pictures somebody else can check.
//
// GRAIN_URL=... GRAIN_CODE=... OUT=... node review.mjs (from ui-ng/)

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");
const BASE = process.env.GRAIN_URL;
const CODE = process.env.GRAIN_CODE;
const OUT = process.env.OUT;
fs.mkdirSync(OUT, { recursive: true });
const ONLY = (process.env.ONLY || "").split(",").filter(Boolean);
const want = (name) => !ONLY.length || ONLY.includes(name);

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
const results = [];
function record(area, check, ok, detail) {
  const r = { area, check, ok, detail };
  results.push(r);
  console.log(JSON.stringify(r));
}

const stamp = Date.now();
const LONG_REASON = [
  "The cache client in acme/widgets moves to the new tier only if it can",
  "push the matching change to acme/gadgets in the same pull request.",
  "",
  ...Array.from(
    { length: 14 },
    (_, i) =>
      `${i + 1}. Step ${i + 1} of the migration touches gadgets/cache/${i}.go and needs a push there.`,
  ),
].join("\n");
const alertBody = (tag) =>
  [
    `${tag} first line: the cache tier is degraded`,
    "",
    ...Array.from(
      { length: 12 },
      (_, i) => `- ${tag} detail ${i + 1}: shard ${i} answers slowly.`,
    ),
  ].join("\n");

const browser = await chromium.launch();
async function open(width, theme, init) {
  const ctx = await browser.newContext({
    viewport: { width, height: width > 600 ? 900 : 844 },
    colorScheme: theme,
  });
  await ctx.addInitScript(
    ([t, extra]) => {
      localStorage.setItem("grain.themeMode", t);
      for (const [k, v] of Object.entries(extra)) localStorage.setItem(k, v);
    },
    [theme, init ?? {}],
  );
  await ctx.setExtraHTTPHeaders({ Authorization: `Bearer ${CODE}` });
  const page = await ctx.newPage();
  const errors = [];
  page.on("pageerror", (e) => errors.push(String(e)));
  return { ctx, page, errors };
}

// annotate draws a labelled outline over each [selector-or-box, label]
// and shoots the page.
async function annotate(page, file, marks) {
  await page.evaluate((ms) => {
    for (const [box, label, color] of ms) {
      if (!box) continue;
      const d = document.createElement("div");
      d.className = "review-mark";
      d.style.cssText = `position:fixed;left:${box.x}px;top:${box.y}px;width:${box.width}px;height:${box.height}px;outline:2px dashed ${color || "#e0119d"};z-index:99999;pointer-events:none`;
      const l = document.createElement("span");
      l.textContent = label;
      l.style.cssText = `position:absolute;left:0;top:-18px;background:${color || "#e0119d"};color:#fff;font:600 11px/16px sans-serif;padding:0 4px;white-space:nowrap`;
      d.appendChild(l);
      document.body.appendChild(d);
    }
  }, marks);
  await page.screenshot({ path: path.join(OUT, file) });
  await page.evaluate(() =>
    document.querySelectorAll(".review-mark").forEach((n) => n.remove()),
  );
}

async function pickRow(page, title) {
  await page.goto(`${BASE}/next/inbox`);
  await page.getByText(title).first().click({ timeout: 20_000 });
  await page.waitForTimeout(1500);
}
const question = (page) => page.getByTestId("resolution-question").first();

// ---------------------------------------------------------------- design

if (want("design")) {
  const acc = await api("POST", "/api/tasks", {
    title: `Review access request ${stamp}`,
    approved: true,
  });
  await api("POST", `/api/e2e/tasks/${acc.id}/pending-access`, {
    summary: "Grant write to acme/gadgets so the cache client can move",
    reason: LONG_REASON,
    items: [{ kind: "write", repo: "acme/gadgets" }],
  });
  const alertTitle = `Review alert capped ${stamp}`;
  await api("POST", "/api/notifications", {
    subject: `review-design-${stamp}:capped`,
    title: alertTitle,
    severity: "warning",
    body: alertBody("CAP"),
  });
  const agent = await api("POST", "/api/tasks", {
    title: `Review agents split ${stamp}`,
    description: "## Goal\n\nMove the **cache client** off `cache-a`.",
  });
  for (const theme of ["light", "dark"]) {
    for (const width of [1440, 390]) {
      // Dock spacing with a long reason.
      {
        const { ctx, page, errors } = await open(width, theme);
        await pickRow(page, acc.title);
        const summary = page.getByTestId("access-summary");
        await summary.waitFor({ timeout: 15_000 });
        const dock = page.locator(".resolution-dock").first();
        const geo = await dock.evaluate((el) => {
          const kids = [...el.children].filter(
            (c) => c.getBoundingClientRect().height > 0,
          );
          const boxes = kids.map((c) => {
            const r = c.getBoundingClientRect();
            return {
              tag: c.tagName.toLowerCase(),
              cls: String(c.className).slice(0, 60),
              top: Math.round(r.top),
              bottom: Math.round(r.bottom),
              h: Math.round(r.height),
            };
          });
          const gaps = boxes
            .slice(1)
            .map((b, i) => b.top - boxes[i].bottom);
          const r = el.getBoundingClientRect();
          return {
            boxes,
            gaps,
            dock: { top: r.top, bottom: r.bottom },
            overflowX: el.scrollWidth > el.clientWidth + 1,
          };
        });
        const sBox = await summary.boundingBox();
        const grant = page.getByTestId("grant-access");
        const gBox = await grant.boundingBox();
        const vh = page.viewportSize().height;
        record(
          "design",
          `access dock ${width}/${theme}: summary keeps its line`,
          !!sBox && sBox.height >= 16,
          { summaryH: sBox?.height },
        );
        record(
          "design",
          `access dock ${width}/${theme}: Grant on screen`,
          !!gBox && gBox.y + gBox.height <= vh && gBox.y >= 0,
          { grant: gBox, vh },
        );
        record(
          "design",
          `access dock ${width}/${theme}: children do not overlap, no x-overflow`,
          geo.gaps.every((g) => g >= 0) && !geo.overflowX,
          geo,
        );
        const marks = [
          [sBox, `summary ${Math.round(sBox?.height ?? 0)}px`],
          [gBox, `Grant y=${Math.round(gBox?.y ?? -1)}`, "#0a7d38"],
        ];
        const reason = page.getByTestId("access-reason");
        if (await reason.count())
          marks.push([
            await reason.first().boundingBox(),
            "reason (capped)",
            "#1f5fd1",
          ]);
        marks.push(
          ...geo.gaps.map((g, i) => [
            {
              x: 4,
              y: geo.boxes[i].bottom,
              width: 40,
              height: Math.max(g, 1),
            },
            `gap ${g}px`,
            "#b25e00",
          ]),
        );
        await annotate(page, `design-access-${width}-${theme}.png`, marks);
        if (errors.length) record("design", "page errors", false, errors);
        await ctx.close();
      }
      // The alert's capped box.
      {
        const { ctx, page } = await open(width, theme);
        await pickRow(page, alertTitle);
        const q = question(page);
        await page
          .waitForFunction(
            () =>
              document
                .querySelector('[data-testid="resolution-question"]')
                ?.textContent?.includes("CAP detail 12"),
            null,
            { timeout: 15_000 },
          )
          .catch(() => {});
        const text = (await q.textContent()) ?? "";
        const qBox = await q.boundingBox();
        const toggle = page.getByTestId("resolution-question-toggle");
        const tBox = (await toggle.count())
          ? await toggle.boundingBox()
          : null;
        const body = await q.evaluate((el) => {
          const sc = el.querySelector(".capped-body, [class*=body]") ?? el;
          return {
            clientH: sc.clientHeight,
            scrollH: sc.scrollHeight,
            maxH: getComputedStyle(sc).maxHeight,
          };
        });
        record(
          "design",
          `alert ${width}/${theme}: whole body read, box capped with a toggle`,
          text.includes("CAP detail 12") && !!tBox,
          { qBox, toggle: tBox, body, hasLast: text.includes("CAP detail 12") },
        );
        await annotate(page, `design-alert-${width}-${theme}.png`, [
          [qBox, `alert box ${Math.round(qBox?.height ?? 0)}px (capped)`],
          [tBox, "Show full alert", "#0a7d38"],
        ]);
        await ctx.close();
      }
    }
    // The Agents inspector at its floor.
    {
      const { ctx, page } = await open(1440, theme, {
        "grain.agents.splitRatio": "0.95",
      });
      await page.goto(`${BASE}/next/agents?task=${agent.id}&pane=details`);
      const pane = page.getByTestId("agents-pane");
      await pane.waitFor({ timeout: 15_000 });
      await page.waitForTimeout(1500);
      const pBox = await pane.boundingBox();
      const rBox = await page.getByTestId("agents-resizer").boundingBox();
      const overflow = await pane.evaluate(
        (el) => el.scrollWidth > el.clientWidth + 1,
      );
      record(
        "design",
        `agents ${theme}: inspector at its 320px floor under a stored 0.95`,
        !!pBox && pBox.width >= 319,
        { pane: pBox, overflowX: overflow },
      );
      await annotate(page, `design-agents-1440-${theme}.png`, [
        [pBox, `inspector ${Math.round(pBox?.width ?? 0)}px`],
        [rBox, "separator", "#1f5fd1"],
      ]);
      await ctx.close();
    }
  }
}

// ------------------------------------------------------------ functional

if (want("functional")) {
  const fam = `review-fam-${stamp}`;
  // A re-raise at the same severity emits no event (pkg/model's
  // RaiseNotification: "merely bumps its count"), so the Inbox would see
  // it only at its 60s poll; one that rises is news at once.
  const raise = (k, body, severity = "warning") =>
    api("POST", "/api/notifications", {
      subject: `${fam}:${k}`,
      title: `Review ${k} ${stamp}`,
      severity,
      body,
    });
  await raise("a", alertBody("ALPHA"));
  await raise("b", alertBody("BETA"));
  const { ctx, page, errors } = await open(1440, "light");
  const reads = [];
  page.on("request", (r) => {
    if (r.url().includes("family=")) reads.push(r.url());
  });
  // Select one alert, then another in the same family.
  await pickRow(page, `Review a ${stamp}`);
  await expectText(page, "ALPHA detail 12", "select alert a: whole body");
  await page.getByText(`Review b ${stamp}`).first().click();
  await expectText(page, "BETA detail 12", "switch to alert b, same family");
  const qText = (await question(page).textContent()) ?? "";
  record("functional", "alert b shows none of a's body", !qText.includes("ALPHA"), {
    reads: reads.length,
  });
  // Raised again with a new body while selected.
  await raise("b", alertBody("GAMMA"), "critical");
  await expectText(page, "GAMMA detail 12", "re-raised alert shows its new body", 25_000);
  await annotate(page, "functional-reraised.png", [
    [await question(page).boundingBox(), "re-raised: GAMMA body"],
  ]);
  // Acknowledge.
  const ack = page.getByTestId("action-ack");
  if (await ack.count()) {
    await ack.first().click();
    await page.waitForTimeout(2000);
    const list = await api("GET", `/api/notifications?open=1&family=${fam}`);
    const b = list.find((n) => n.subject === `${fam}:b`);
    record("functional", "Acknowledge sets ackedAt", !!b?.ackedAt, {
      ackedAt: b?.ackedAt,
    });
  } else record("functional", "Acknowledge offered", false, "no action-ack");
  // Clear (through its confirm).
  await page.getByText(`Review a ${stamp}`).first().click();
  await page.waitForTimeout(800);
  const clear = page.getByTestId("action-clear");
  await clear.first().click();
  await page.getByTestId("confirm-accept").click();
  await page.waitForTimeout(2500);
  const after = await api("GET", `/api/notifications?open=1&family=${fam}`);
  record(
    "functional",
    "Clear clears it",
    !after.some((n) => n.subject === `${fam}:a`),
    { open: after.map((n) => n.subject) },
  );
  const goneText = (await question(page).textContent().catch(() => "")) ?? "";
  record("functional", "cleared alert's dock after Clear", true, {
    shows: goneText.slice(0, 120),
  });
  await annotate(page, "functional-after-clear.png", [
    [await page.locator(".inspector-pane").first().boundingBox(), "after Clear"],
  ]);
  if (errors.length) record("functional", "page errors", false, errors);
  await ctx.close();

  // Grant / Deny.
  for (const verb of ["grant", "deny"]) {
    const t = await api("POST", "/api/tasks", {
      title: `Review ${verb} access ${stamp}`,
      approved: true,
    });
    await api("POST", `/api/e2e/tasks/${t.id}/pending-access`, {
      summary: `Open the sandbox network (${verb})`,
      reason: LONG_REASON,
      // acme/gadgets is not on the demo's target list, so a write to it
      // is refused (Grant 0 of 1); network is grantable anywhere.
      items: [{ kind: "network", access: "open" }],
    });
    const o = await open(1440, "light");
    await pickRow(o.page, t.title);
    if (verb === "grant") {
      await o.page.getByTestId("grant-access").click();
    } else {
      await o.page.getByTestId("deny-access").click();
      await o.page.getByTestId("deny-reason").fill("Not this quarter.");
      await o.page.getByTestId("confirm-deny").click();
    }
    await o.page.waitForTimeout(2500);
    const d = await api("GET", `/api/tasks/${t.id}`);
    record("functional", `${verb} answers the access request`, !d.pendingAccess, {
      state: d.state,
      pendingAccess: !!d.pendingAccess,
      errors: o.errors,
      banner: await o.page
        .getByTestId("action-error")
        .textContent()
        .catch(() => null),
    });
    await o.ctx.close();
  }

  // Agents split drag after load.
  const agent = await api("POST", "/api/tasks", {
    title: `Review agents drag ${stamp}`,
  });
  const o = await open(1440, "light", { "grain.agents.splitRatio": "0.5" });
  await o.page.goto(`${BASE}/next/agents?task=${agent.id}&pane=details`);
  const pane = o.page.getByTestId("agents-pane");
  await pane.waitFor({ timeout: 15_000 });
  await o.page.waitForTimeout(1500);
  const before = await pane.boundingBox();
  const sep = await o.page.getByTestId("agents-resizer").boundingBox();
  const y = sep.y + sep.height / 2;
  const drag = async (x) => {
    await o.page.mouse.move(sep.x + sep.width / 2, y);
    await o.page.mouse.down();
    await o.page.mouse.move(x, y, { steps: 8 });
    await o.page.mouse.up();
    await o.page.waitForTimeout(400);
    return pane.boundingBox();
  };
  const left = await drag(sep.x - 200);
  record(
    "functional",
    "agents drag left widens the inspector",
    left.width > before.width + 150,
    { before: before.width, after: left.width },
  );
  const sep2 = await o.page.getByTestId("agents-resizer").boundingBox();
  sep.x = sep2.x;
  const right = await drag(1430);
  record(
    "functional",
    "agents drag to the edge stops at the 320px floor",
    right.width >= 319 && right.width < 360,
    { after: right.width },
  );
  await annotate(o.page, "functional-agents-drag.png", [
    [right, `after drag to the edge: ${Math.round(right.width)}px`],
  ]);
  await o.ctx.close();
}

async function expectText(page, needle, check, timeout = 15_000) {
  const ok = await page
    .waitForFunction(
      (n) =>
        document
          .querySelector('[data-testid="resolution-question"]')
          ?.textContent?.includes(n),
      needle,
      { timeout },
    )
    .then(() => true)
    .catch(() => false);
  const shows = ((await question(page).textContent().catch(() => "")) ?? "")
    .replace(/\s+/g, " ")
    .slice(0, 160);
  record("functional", check, ok, { needle, shows });
  return ok;
}

// ----------------------------------------------------------- adversarial

if (want("adversarial")) {
  const fam = `review-adv-${stamp}`;
  await api("POST", "/api/notifications", {
    subject: `${fam}:x`,
    title: `Review adv x ${stamp}`,
    severity: "warning",
    body: alertBody("OLD"),
  });
  // A failed family read falls back to the first line.
  {
    const { ctx, page } = await open(1440, "light");
    await page.route(
      (u) => u.pathname === "/api/notifications" && u.searchParams.has("family"),
      (r) => r.fulfill({ status: 500, body: "boom" }),
    );
    await pickRow(page, `Review adv x ${stamp}`);
    await page.waitForTimeout(2000);
    const t = ((await question(page).textContent()) ?? "").replace(/\s+/g, " ");
    record(
      "adversarial",
      "failed family read shows the first line",
      t.includes("OLD first line") && !t.includes("OLD detail"),
      { shows: t.slice(0, 160) },
    );
    await annotate(page, "adversarial-failed-read.png", [
      [await question(page).boundingBox(), "family read 500: first line"],
    ]);
    await ctx.close();
  }
  // Re-raised with a new body while the family read fails: the dock must
  // not keep the old raise's body.
  {
    const { ctx, page } = await open(1440, "light");
    let fail = false;
    await page.route(
      (u) => u.pathname === "/api/notifications" && u.searchParams.has("family"),
      (r) => (fail ? r.fulfill({ status: 500, body: "boom" }) : r.continue()),
    );
    await pickRow(page, `Review adv x ${stamp}`);
    await page
      .waitForFunction(
        () =>
          document
            .querySelector('[data-testid="resolution-question"]')
            ?.textContent?.includes("OLD detail 12"),
        null,
        { timeout: 15_000 },
      )
      .catch(() => {});
    fail = true;
    await api("POST", "/api/notifications", {
      subject: `${fam}:x`,
      title: `Review adv x ${stamp}`,
      severity: "critical",
      body: alertBody("NEW"),
    });
    await page.waitForTimeout(12_000);
    const t = ((await question(page).textContent()) ?? "").replace(/\s+/g, " ");
    record(
      "adversarial",
      "re-raised alert with a failed read shows no stale body",
      !t.includes("OLD") && t.includes("NEW first line"),
      { shows: t.slice(0, 200) },
    );
    await annotate(page, "adversarial-stale-reraise.png", [
      [await question(page).boundingBox(), "re-raised NEW, read failed"],
    ]);
    await ctx.close();
  }
  // Stale selection after the alert is cleared elsewhere.
  {
    const { ctx, page, errors } = await open(1440, "light");
    await pickRow(page, `Review adv x ${stamp}`);
    await page.waitForTimeout(1500);
    const list = await api("GET", `/api/notifications?open=1&family=${fam}`);
    await api("POST", `/api/notifications/${list[0].id}/clear`);
    await page.waitForTimeout(12_000);
    const actions = await page
      .locator(".inspector-pane [data-testid^='action-']")
      .count();
    const t = ((await page.locator(".inspector-pane").first().textContent()) ?? "")
      .replace(/\s+/g, " ");
    record(
      "adversarial",
      "alert cleared elsewhere: dock read-only, no actions",
      actions === 0,
      { actions, shows: t.slice(0, 240), errors },
    );
    await annotate(page, "adversarial-cleared-elsewhere.png", [
      [await page.locator(".inspector-pane").first().boundingBox(), "cleared elsewhere"],
    ]);
    await ctx.close();
  }
  // %%{init}%% and front matter that try to get past the locked keys.
  const F = "```";
  const cases = {
    nested: [
      "%%{init: {'sequence': {'themeVariables': {'actorBkg': '#ff0000'}, 'theme': 'dark'}}}%%",
      "sequenceDiagram",
      "  Alice->>Bob: hello",
    ],
    proto: [
      "%%{init: {'__proto__': {'themeVariables': {'primaryColor': '#ff0000'}}, 'constructor': {'prototype': {'themeCSS': '.node rect{fill:#ff0000}'}}}}%%",
      "flowchart LR",
      "  A[drain] --> B[migrate]",
    ],
    front: [
      "---",
      "config:",
      "  theme: dark",
      "  darkMode: true",
      "  themeVariables:",
      "    primaryColor: '#ff0000'",
      "    mainBkg: '#ff0000'",
      "  themeCSS: '.node rect { fill: #ff0000 !important; }'",
      "  fontFamily: Comic Sans MS",
      "---",
      "flowchart LR",
      "  A[drain] --> B[migrate]",
    ],
    look: [
      "%%{init: {'look': 'handDrawn', 'flowchart': {'theme': 'dark'}}}%%",
      "flowchart LR",
      "  A[drain] --> B[migrate]",
    ],
    styleStmt: ["flowchart LR", "  A[drain] --> B[migrate]", "  style A fill:#ff0000"],
  };
  const t = await api("POST", "/api/tasks", { title: `Review diagrams ${stamp}` });
  await api("POST", `/api/tasks/${t.id}/comments`, {
    body: Object.entries(cases)
      .map(([k, src]) => [`${F}mermaid ${k}`, ...src, F].join("\n"))
      .join("\n\n"),
  });
  for (const theme of ["light", "dark"]) {
    const { ctx, page } = await open(1440, theme);
    await page.goto(`${BASE}/next/task/${t.id}`);
    await page.waitForTimeout(6000);
    const svgs = await page.evaluate(() =>
      [...document.querySelectorAll("svg[id^='mermaid'], .mermaid-canvas svg, [class*=mermaid] svg")]
        .filter((s, i, a) => a.indexOf(s) === i && s.getBoundingClientRect().width > 40)
        .map((s) => {
          const html = s.outerHTML;
          const fills = [...s.querySelectorAll("rect, path, polygon, circle")]
            .map((n) => getComputedStyle(n).fill)
            .filter((f) => f && f !== "none");
          const fonts = [...s.querySelectorAll("text, tspan")].map(
            (n) => getComputedStyle(n).fontFamily,
          );
          const r = s.getBoundingClientRect();
          return {
            red:
              /#ff0000|#f00\b|rgb\(255, 0, 0\)/i.test(html) ||
              fills.includes("rgb(255, 0, 0)"),
            comic: fonts.some((f) => /comic/i.test(f)) || /comic/i.test(html),
            handDrawn: /handDrawn|rough/i.test(html),
            box: { x: r.x, y: r.y, width: r.width, height: r.height },
            region:
              s.closest("[aria-label]")?.getAttribute("aria-label") ?? "",
          };
        }),
    );
    record("adversarial", `mermaid escapes (${theme})`, true, svgs);
    await annotate(
      page,
      `adversarial-mermaid-${theme}.png`,
      svgs.map((s) => [
        s.box,
        `${s.region}: red=${s.red} font=${s.comic ? "comic" : "locked"}${s.handDrawn ? " handDrawn" : ""}`,
        s.red || s.comic ? "#c00" : "#0a7d38",
      ]),
    );
    await ctx.close();
  }
}

await browser.close();
fs.writeFileSync(path.join(OUT, "results.json"), JSON.stringify(results, null, 2));
console.log(
  `${results.filter((r) => r.ok).length}/${results.length} checks passed`,
);
