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

// docs/test-plans/2026-11-16.md §7 item 1: /next/tasks batch actions
// against the real daemon (seed.sh's rows, dispatch paused).
import {
  chromium, api, as, note, info, flush, shot, seeded, row, tick, accept, daemon,
  DESKTOP, PHONE,
} from "./lib.mjs";

const browser = await chromium.launch();
const Q = Object.keys(seeded)
  .filter((k) => /^Q\d+$/.test(k))
  .sort()
  .map((k) => seeded[k]);

async function step(name, fn) {
  try {
    await fn();
  } catch (e) {
    note(name, false, `threw ${String(e).split("\n")[0]}`);
  }
}

const only = (process.env.STEPS || "1a,1b,1c,1d,1e").split(",");

// 1a. Run selected now on 20 rows during the pause, Stop part-way.
if (only.includes("1a")) await step("1a run-now under pause", async () => {
  const { ctx, page } = await as(browser, "alice@example.com", DESKTOP);
  // Each run-now answer is held 400 ms, so the four-in-flight window is
  // wide enough for a person-speed Stop; the requests themselves are real.
  let inflight = 0, peak = 0, sent = 0;
  await page.route("**/api/tasks/*/run-now", async (r) => {
    sent++;
    inflight++;
    peak = Math.max(peak, inflight);
    await new Promise((res) => setTimeout(res, 400));
    try {
      await r.continue();
    } finally {
      inflight--;
    }
  });
  await page.goto("tasks?tab=backlog");
  await row(page, Q[0]).waitFor({ timeout: 20000 });
  let marked = 0;
  for (const id of Q) marked += await row(page, id).getByTestId("waits-run-now").count();
  note("1a waits-run-now marker", marked === Q.length, `${marked}/${Q.length} queued rows show "Paused — waits for Run now"`);
  const markText = await row(page, Q[0]).getByTestId("waits-run-now").innerText().catch(() => "");
  info("1a marker text", JSON.stringify(markText));
  for (const id of Q) await tick(page, id).check();
  const bar = page.getByTestId("batch-bar");
  await shot(page, "1a-desktop-20-selected");
  info("1a bar text", JSON.stringify((await bar.innerText()).replace(/\s+/g, " ")));
  await bar.getByTestId("batch-run-now").click();
  const dialog = page.getByTestId("confirm-dialog");
  await dialog.waitFor({ timeout: 5000 });
  info("1a confirm", JSON.stringify((await dialog.innerText()).replace(/\s+/g, " ")));
  await shot(page, "1a-desktop-confirm");
  const tickBox = dialog.getByTestId("confirm-multi-repo");
  if (await tickBox.count()) await tickBox.check();
  await dialog.getByRole("button", { name: /^Run/ }).last().click();
  await bar.getByTestId("batch-progress").waitFor({ timeout: 5000 });
  info("1a progress", JSON.stringify(await bar.getByTestId("batch-progress").innerText()));
  await shot(page, "1a-desktop-progress");
  await page.waitForTimeout(900);
  await bar.getByTestId("batch-stop").click();
  const msg = page.getByText(/Stopped after \d+ of 20/);
  await msg.waitFor({ timeout: 10000 });
  const text = await msg.first().innerText();
  await page.waitForTimeout(500);
  const m = /Stopped after (\d+) of 20; (\d+) left/.exec(text);
  const done = m ? Number(m[1]) : -1;
  const stillTicked = [];
  for (const id of Q) if (await tick(page, id).isChecked().catch(() => false)) stillTicked.push(id);
  note("1a four in flight", peak <= 4 && peak >= 2, `peak concurrent run-now requests ${peak}, sent ${sent}`);
  note("1a stop", m && done < 20 && sent === done, `"${text}"; requests sent ${sent}`);
  note("1a unsent stay ticked", stillTicked.length === 20 - done, `${stillTicked.length} rows still ticked (want ${20 - done})`);
  await shot(page, "1a-desktop-stopped");
  // Server truth: every row that was asked ran (the pause let it start)
  // and every row that was not is still queued, never run.
  let after = [];
  for (let i = 0; i < 60; i++) {
    after = await Promise.all(Q.map((id) => api("GET", `/api/tasks/${id}`)));
    if (after.filter((t) => (t.attempts ?? []).length > 0 && t.state === "closed").length >= done) break;
    await new Promise((r) => setTimeout(r, 500));
  }
  const ran = after.filter((t) => (t.attempts ?? []).length > 0).length;
  const waiting = after.filter((t) => t.state === "queued" && !(t.attempts ?? []).length).length;
  note("1a server agrees", ran === done && waiting === 20 - done, `${ran} rows ran under the pause (want ${done}), ${waiting} still queued and never run (want ${20 - done})`);
  await ctx.close();
});

const tail = (id) => id.split("=")[0].split(/[:#/]/).pop();
const caps = async (id) => (await api("GET", `/api/tasks/${id}`)).capabilities ?? [];

// 1b. Submit and Retry merge on rows in the merge queue: TRIG is held by
// the human-required trigger (awaiting_submit, pull request open), CIWAIT
// rests deferred on its own open pull request.
if (only.includes("1b")) await step("1b submit / retry merge", async () => {
  const { ctx, page } = await as(browser, "alice@example.com", DESKTOP);
  const T = seeded.TRIG, W = seeded.CIWAIT;
  for (const tab of ["merging", "all"]) {
    await page.goto(`tasks?tab=${tab}`);
    if (await row(page, T).count() || (await row(page, T).waitFor({ timeout: 8000 }).then(() => true).catch(() => false))) {
      info("1b tab", `task ${T} listed on tab=${tab}`);
      break;
    }
  }
  await tick(page, T).check();
  const bar = page.getByTestId("batch-bar");
  await bar.getByTestId("batch-more").click();
  const opts = (await page.getByRole("option").allInnerTexts()).map((t) => t.trim());
  info("1b More options for the held change", JSON.stringify(opts));
  note("1b offers Submit and Retry merge", opts.some((o) => /^Submit/.test(o)) && opts.some((o) => /^Retry merge/.test(o)), JSON.stringify(opts));
  await shot(page, "1b-desktop-more-menu");
  await page.getByRole("option", { name: /^Submit/ }).click();
  const confirm = page.getByTestId("confirm-dialog");
  await confirm.waitFor();
  info("1b submit confirm", JSON.stringify((await confirm.innerText()).replace(/\s+/g, " ")));
  const [resp] = await Promise.all([
    page.waitForResponse((r) => r.url().endsWith(`/api/tasks/${T}/submit`)),
    accept(confirm, "Submit"),
  ]);
  const body = await resp.text();
  info("1b submit answer", `${resp.status()} ${body.slice(0, 300)}`);
  await page.waitForTimeout(1500);
  const toast = await page.locator("[role=status], [role=alert]").allInnerTexts();
  info("1b page said", JSON.stringify(toast.map((t) => t.replace(/\s+/g, " ")).filter(Boolean)));
  await shot(page, "1b-desktop-after-submit");
  const t = await api("GET", `/api/tasks/${T}`);
  info("1b server after submit", JSON.stringify({ state: t.state, mergeQueueState: t.mergeQueueState, submittedBy: t.submittedBy, gates: (t.mergeGates ?? []).map((g) => `${g.id}:${g.state}`) }));
  note("1b submit reaches the server", resp.ok() || resp.status() === 409, `POST submit answered ${resp.status()}`);

  // Retry merge on the CI-waiting row (an open pull request, deferred).
  await page.goto("tasks?tab=all");
  await row(page, W).waitFor({ timeout: 10000 });
  await tick(page, W).check();
  await bar.getByTestId("batch-more").click();
  const opts2 = (await page.getByRole("option").allInnerTexts()).map((x) => x.trim());
  info("1b More options for the CI-waiting row", JSON.stringify(opts2));
  if (opts2.some((o) => /^Retry merge/.test(o))) {
    // One row: Retry merge goes straight through (confirmRetryMerge asks
    // only for several).
    const [r2] = await Promise.all([
      page.waitForResponse((r) => r.url().endsWith(`/api/tasks/${W}/retry-merge`)),
      page.getByRole("option", { name: /^Retry merge/ }).click(),
    ]);
    info("1b retry answer", `${r2.status()} ${(await r2.text()).slice(0, 300)}`);
    await page.waitForTimeout(1500);
    info("1b page said", JSON.stringify((await page.locator("[role=status], [role=alert]").allInnerTexts()).map((t) => t.replace(/\s+/g, " ")).filter(Boolean)));
    const w = await api("GET", `/api/tasks/${W}`);
    info("1b server after retry", JSON.stringify({ state: w.state, mergeQueueState: w.mergeQueueState }));
    note("1b retry merge reaches the server", r2.status() < 500, `POST retry-merge answered ${r2.status()}`);
  } else {
    note("1b retry merge offered on the deferred PR row", false, JSON.stringify(opts2));
  }
  await ctx.close();
});

// 1c. Bulk edit across a role that can write only some rows: wendy holds
// tasks:write on acme/widgets only; N1..N3 are no-repo tasks.
if (only.includes("1c")) await step("1c partial-permission bulk edit", async () => {
  for (const [vp, label] of [[DESKTOP, "desktop"], [PHONE, "phone"]]) {
    const { ctx, page } = await as(browser, "wendy@example.com", vp);
    await page.goto("tasks?tab=backlog");
    const widgets = Q.slice(-4);
    await row(page, widgets[0]).waitFor({ timeout: 15000 });
    const sees = [];
    for (const k of ["N1", "N2", "N3", "P1"]) sees.push(`${k}:${await row(page, seeded[k]).count()}`);
    info(`1c ${label} writer sees`, sees.join(" "));
    const bar = page.getByTestId("batch-bar");
    await tick(page, widgets[0]).check();
    await tick(page, widgets[1]).check();
    await bar.getByTestId("batch-more").click();
    const own = (await page.getByRole("option").allInnerTexts()).map((x) => x.trim());
    await page.keyboard.press("Escape");
    info(`1c ${label} More on own rows`, JSON.stringify(own));
    note(`1c ${label} own rows offer Bulk edit`, own.some((o) => o.startsWith("Bulk edit")), JSON.stringify(own));
    if (label === "desktop") {
      await bar.getByTestId("batch-more").click();
      await page.getByRole("option", { name: /^Bulk edit/ }).click();
      const form = page.getByTestId("batch-edit-dialog");
      // F1: 9 is above what the role may set; the form says so and keeps
      // Apply off (before the fix it sent 9 and both rows were refused
      // "cannot set priority above 5 without tasks:admin").
      await form.getByTestId("bulk-priority").fill("9");
      const capErr = await form.getByTestId("bulk-priority-error").innerText().catch(() => "");
      const off = await form.getByTestId("batch-edit-apply").isDisabled();
      note("1c writer: priority above 5 refused beside the field", off && /needs tasks:admin/.test(capErr), JSON.stringify({ applyDisabled: off, error: capErr }));
      await shot(page, "1c-desktop-writer-bulk-edit");
      await form.getByTestId("bulk-priority").fill("4");
      const answers = [];
      page.on("response", (r) => { if (r.request().method() === "PATCH") answers.push(r); });
      await form.getByTestId("batch-edit-apply").click();
      await page.getByText(/Edited 2 tasks/).waitFor({ timeout: 10000 });
      for (const r of answers) info("1c writer PATCH", `${r.url().replace(/^.*\/api/, "/api")} ${r.status()}`);
      await shot(page, "1c-desktop-writer-after-edit");
      const pr = await Promise.all(widgets.slice(0, 2).map(async (id) => (await api("GET", `/api/tasks/${id}`)).priority));
      note("1c writer bulk edit stored", pr.every((p) => p === 4), `priorities ${JSON.stringify(pr)}`);
    } else {
      await bar.getByRole("button", { name: "Clear" }).click();
    }
    // A row outside the role's reach, if it is listed, takes the offer away.
    const n1 = row(page, seeded.N1);
    if (await n1.count()) {
      await tick(page, widgets[2]).check();
      await tick(page, seeded.N1).check();
      await bar.getByTestId("batch-more").click();
      const mixed = (await page.getByRole("option").allInnerTexts()).map((x) => x.trim());
      await page.keyboard.press("Escape");
      note(`1c ${label} mixed selection withholds Bulk edit`, !mixed.some((o) => o.startsWith("Bulk edit")), JSON.stringify(mixed));
      await shot(page, `1c-${label}-writer-mixed`);
    } else {
      info(`1c ${label}`, "no-repo rows are not listed for the writer at all, so no mixed selection can be made");
    }
    note(`1c ${label} no page errors`, page.errors.length === 0, JSON.stringify(page.errors));
    await ctx.close();
  }
  // The server is the backstop: the writer's PATCH on a no-repo task.
  const w = await fetch(`${daemon}/api/tasks/${seeded.N1}`, {
    method: "PATCH",
    headers: { "x-goog-iap-jwt-assertion": await (await import("./lib.mjs")).jwt("wendy@example.com"), "Content-Type": "application/json" },
    body: JSON.stringify({ priority: 9 }),
  });
  note("1c server refuses the writer outside its repo", w.status === 403, `${w.status} ${(await w.text()).slice(0, 200)}`);
});

// 1d. Capability attach/detach: what lands on the task is the canonical
// grant string.
if (only.includes("1d")) await step("1d capability attach", async () => {
  const { ctx, page } = await as(browser, "alice@example.com", DESKTOP);
  const x = seeded.N2, y = seeded.N3;
  const before = (await caps(x)).map(tail);
  const cfg = await api("GET", "/api/config");
  const cap = (cfg.capabilities ?? []).find((c) => c.ready !== false && c.id && !before.includes(tail(c.id)));
  info("1d capability", JSON.stringify({ id: cap?.id, name: cap?.name }));
  await page.goto("tasks?tab=backlog");
  await row(page, x).waitFor({ timeout: 15000 });
  await tick(page, x).check();
  await tick(page, y).check();
  const bar = page.getByTestId("batch-bar");
  await bar.getByTestId("batch-more").click();
  await page.getByRole("option", { name: "Capability…" }).click();
  const form = page.getByTestId("batch-edit-dialog");
  await form.getByTestId("bulk-capability").click();
  await page.getByRole("option", { name: cap.name || cap.id, exact: true }).click();
  await shot(page, "1d-desktop-capability-form");
  const [req] = await Promise.all([
    page.waitForRequest((r) => r.url().includes(`/api/tasks/${x}/capabilities`) && r.method() !== "GET"),
    form.getByTestId("batch-capability-attach").click(),
  ]);
  info("1d attach request", `${req.method()} ${req.url().replace(/^.*\/api/, "/api")} ${req.postData()}`);
  await page.waitForTimeout(1500);
  const cx = await caps(x), cy = await caps(y);
  const canon = (l) => l.filter((c) => tail(c) === tail(cap.id));
  info("1d stored", JSON.stringify({ x: cx, y: cy }));
  note("1d canonical grant strings land on both tasks", canon(cx).length === 1 && canon(cy).length === 1 && [...canon(cx), ...canon(cy)].every((c) => c.startsWith("https://grain.dev/capabilities/")), JSON.stringify([...canon(cx), ...canon(cy)]));
  // Detach again.
  await tick(page, x).check();
  await tick(page, y).check();
  await bar.getByTestId("batch-more").click();
  await page.getByRole("option", { name: "Capability…" }).click();
  await form.getByTestId("bulk-capability").click();
  await page.getByRole("option", { name: cap.name || cap.id, exact: true }).click();
  await form.getByTestId("batch-capability-detach").click();
  await accept(page.getByTestId("confirm-dialog"), "Detach");
  await page.waitForTimeout(1500);
  note("1d detach removes it", canon(await caps(x)).length === 0 && canon(await caps(y)).length === 0, JSON.stringify({ x: await caps(x), y: await caps(y) }));
  await ctx.close();
});

// 1e. Reopen on the closed tab, at phone width.
if (only.includes("1e")) await step("1e reopen", async () => {
  const { ctx, page } = await as(browser, "alice@example.com", PHONE);
  await page.goto("tasks?tab=closed");
  const a = seeded.C1, b = seeded.C2;
  await row(page, a).waitFor({ timeout: 15000 });
  await tick(page, a).check();
  await tick(page, b).check();
  const bar = page.getByTestId("batch-bar");
  await shot(page, "1e-phone-closed-selected");
  const btn = bar.getByRole("button", { name: /Reopen/ });
  info("1e bar", JSON.stringify((await bar.innerText()).replace(/\s+/g, " ")));
  await btn.click();
  const c = page.getByTestId("confirm-dialog");
  if (await c.waitFor({ timeout: 2000 }).then(() => true).catch(() => false)) await accept(c, await c.getByRole("button").last().innerText());
  await page.waitForTimeout(1500);
  const st = await Promise.all([a, b].map(async (id) => (await api("GET", `/api/tasks/${id}`)).state));
  note("1e reopen", st.every((s) => s !== "closed"), JSON.stringify(st));
  // Bar fits the phone width.
  const box = await bar.boundingBox().catch(() => null);
  info("1e bar box", JSON.stringify(box));
  await ctx.close();
});

flush("item1");
await browser.close();
