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

// docs/test-plans/2026-11-16.md §7 items 2-4 on the real daemon: the
// human-required trigger hold on /next/task/<id>, Defer until across the
// display zone and its real wake, and the /next/fleet CI-wait card and
// publishing banner. Store edits (backdating) go through sqlite3, or psql
// with STORE=postgres.
import { execFileSync } from "node:child_process";
import {
  chromium, api, as, note, info, flush, shot, seeded, work,
  DESKTOP, PHONE,
} from "./lib.mjs";

const GHSIM = `http://${process.env.GHSIM || `127.0.0.1:${process.env.GHSIM_PORT || 4284}`}`;
const browser = await chromium.launch();
const only = (process.env.STEPS || "2,3,4a").split(",");
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const sql = (q) =>
  process.env.STORE === "postgres"
    ? execFileSync("psql", ["-q", "-h", "127.0.0.1", "-U", "grain", "-d", "grain_walk", "-c", q], { encoding: "utf8", env: { ...process.env, PGPASSWORD: "grain" } }).trim()
    : execFileSync("sqlite3", ["-cmd", ".timeout 5000", `${work}/data/store/grain.db`, q], { encoding: "utf8" }).trim();
const ts = (ms) => new Date(Date.now() + ms).toISOString().replace("T", " ").replace(/\.\d+Z$/, "+00:00");
async function step(name, fn) {
  try {
    await fn();
  } catch (e) {
    note(name, false, `threw ${String(e).split("\n")[0]}`);
  }
}
async function until(fn, secs) {
  const start = Date.now();
  for (;;) {
    if (await fn()) return (Date.now() - start) / 1000;
    if (Date.now() - start > secs * 1000) return null;
    await sleep(250);
  }
}

// 2. The trigger hold on the held change, admin and writer, both widths.
if (only.includes("2")) await step("2 trigger hold", async () => {
  const T = seeded.TRIG;
  const t = await api("GET", `/api/tasks/${T}`);
  const reviewId = (t.pathTriggerTasks?.[0] ?? "").split("=")[1];
  info("2 server", JSON.stringify({ pathTriggerHold: t.pathTriggerHold, pathTriggerTasks: t.pathTriggerTasks, gates: (t.mergeGates ?? []).map((g) => `${g.id}:${g.state}`) }));
  for (const [who, vp, label] of [
    ["alice@example.com", DESKTOP, "desktop-admin"],
    ["alice@example.com", PHONE, "phone-admin"],
    ["wendy@example.com", DESKTOP, "desktop-writer"],
  ]) {
    const { ctx, page } = await as(browser, who, vp);
    await page.goto(`task/${T}`);
    const hold = page.getByRole("region", { name: "Held by this repository's gating triggers" });
    const seen = await hold.waitFor({ timeout: 15000 }).then(() => true).catch(() => false);
    const text = seen ? (await hold.innerText()).replace(/\s+/g, " ") : "";
    info(`2 ${label} callout`, JSON.stringify(text));
    note(`2 ${label} names human: required and the trigger file`, /human: required/.test(text) && /\.grain\/triggers\/walk-declarations\.yml/.test(text), "");
    const link = hold.getByRole("link", { name: `Review task #${reviewId}` });
    note(`2 ${label} links the review task`, (await link.count()) === 1, `Review task #${reviewId}`);
    if (seen) {
      const box = await hold.boundingBox();
      note(`2 ${label} callout inside the viewport`, box.x >= 0 && box.x + box.width <= vp.width + 0.5, JSON.stringify(box));
    }
    const rule = hold.getByTestId("approval-rule");
    if (await rule.count()) info(`2 ${label} approval rule`, JSON.stringify(await rule.innerText()));
    await shot(page, `2-${label}-hold`);
    if (label === "desktop-admin" && (await link.count())) {
      await link.click();
      await page.waitForURL(new RegExp(`/next/task/${reviewId}(\\?|$)`), { timeout: 10000 });
      note("2 review link opens the review task", true, page.url().replace(/^.*\/next/, "/next"));
    }
    note(`2 ${label} no page errors`, page.errors.length === 0, JSON.stringify(page.errors));
    await ctx.close();
  }
});

// 3. Defer until at 390 px, under a display zone, and the real wake.
if (only.includes("3")) await step("3 defer until", async () => {
  const id = seeded.P3;
  const zone = process.env.ZONE || "America/Los_Angeles";
  await api("PUT", "/api/settings", { timeZone: zone }).catch((e) => info("3 zone", String(e)));
  const cfg = await api("GET", "/api/config");
  info("3 display zone", JSON.stringify(cfg.timeZone ?? null));
  const { ctx, page } = await as(browser, "alice@example.com", PHONE);
  await page.goto(`task/${id}`);
  await page.getByRole("button", { name: "Defer until…" }).click();
  const date = page.getByLabel("Wake on");
  const min = await date.getAttribute("min");
  const [y, m, d] = min.split("-").map(Number);
  const pick = new Date(Date.UTC(y, m - 1, d + 2)).toISOString().slice(0, 10);
  await date.fill(pick);
  const hint = await page.getByTestId("defer-until-hint").innerText();
  info("3 picker", JSON.stringify({ min, pick, hint }));
  const submit = page.getByRole("button", { name: "Defer until", exact: true });
  const db = await date.boundingBox(), sb = await submit.boundingBox();
  note("3 picker fits 390 px", db.x >= 0 && sb.x + sb.width <= 390.5 && db.x + db.width <= 390.5, JSON.stringify({ db, sb }));
  await shot(page, "3-phone-defer-picker");
  const [resp] = await Promise.all([
    page.waitForResponse((r) => r.url().endsWith(`/api/tasks/${id}/defer`) && r.request().method() === "POST"),
    submit.click(),
  ]);
  const sent = JSON.parse(resp.request().postData() || "{}").deferredUntil;
  const stored = (await api("GET", `/api/tasks/${id}`)).deferredUntil;
  // Start of the picked day in the display zone.
  const local = new Intl.DateTimeFormat("en-CA", { timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hourCycle: "h23" }).format(new Date(stored));
  info("3 stored", JSON.stringify({ sent, stored, inZone: local }));
  note("3 server stores the start of the picked day in the display zone", Date.parse(sent) === Date.parse(stored) && local.startsWith(pick) && /00:00$/.test(local), `${stored} is ${local} ${zone}`);
  await page.goto(`task/${id}`);
  const wake = await page.getByTestId("task-deferred-until").innerText().catch(() => "");
  info("3 page says", JSON.stringify(wake));
  await shot(page, "3-phone-deferred");
  // The real wake: bring the stored time to 3 s from now and watch the
  // live daemon (-poll-interval 500ms) pick it up.
  sql(`update task set deferred_until='${ts(3000)}' where id='${id}'`);
  const took = await until(async () => (await api("GET", `/api/tasks/${id}`)).state !== "deferred", 30);
  const after = await api("GET", `/api/tasks/${id}`);
  note("3 wakes on the live daemon", took !== null, `left deferred ${took}s after the store was set 3 s ahead; now ${after.state}`);
  note("3 no page errors", page.errors.length === 0, JSON.stringify(page.errors));
  await ctx.close();
});

// 4a. A resume_later(ci: my-branch) wait over an hour old shows on the
// Sandboxes tab, then drops when CI answers.
if (only.includes("4a")) await step("4a ci waits", async () => {
  const id = process.env.CIWAIT_ID || seeded.CIWAIT2 || seeded.CIWAIT;
  const ago = ts(-65 * 60 * 1000);
  sql(`update task set deferred_at='${ago}' where id='${id}'`);
  sql(`update task_suspension set created_at='${ago}' where task_id='${id}'`);
  sql(`update task_run set started_at='${ts(-66 * 60 * 1000)}', finished_at='${ago}' where task_id='${id}' and finished_at is not null`);
  const waits = async () => ((await api("GET", "/api/sandboxes")).ciWaits ?? []).map((w) => String(w.taskId));
  info("4a ciWaits after backdating", JSON.stringify(await waits()));
  for (const [vp, label] of [[PHONE, "phone"], [DESKTOP, "desktop"]]) {
    const { ctx, page } = await as(browser, "alice@example.com", vp);
    await page.goto("system");
    const card = page.getByRole("article", { name: "Waiting on CI" });
    // The card renders before the first /api/sandboxes answer: wait for
    // the row, not the card.
    const seen = await card.getByText(`#${id}`).first().waitFor({ timeout: 15000 }).then(() => true).catch(() => false);
    const text = (await card.innerText().catch(() => "")).replace(/\s+/g, " ");
    note(`4a ${label} card lists the wait`, seen && text.includes(`#${id}`), `${page.url().replace(/^.*\/next/, "/next")} ${JSON.stringify(text.slice(0, 300))}`);
    await shot(page, `4a-${label}-ci-waits`);
    if (label === "phone") {
      await ctx.close();
      continue;
    }
    // CI answers on the task's branch.
    const t = await api("GET", `/api/tasks/${id}`);
    const sha = execFileSync("git", ["--git-dir", `${work}/widgets.git`, "rev-parse", `refs/heads/${t.branch}`], { encoding: "utf8" }).trim();
    const now = new Date().toISOString();
    fs_write(`${work}/action-${id}.json`, '[{"kind": "comment_on_issue", "comment": "CI is green"}]');
    await fetch(`${GHSIM}/_sim/seed-actions`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ runs: [
        { id: 5001, name: "CI", path: ".github/workflows/ci.yml", headBranch: "main", headSha: "0000000000000000000000000000000000000001", event: "push", status: "completed", conclusion: "success" },
        { id: 6000 + Number(id), name: "CI", path: ".github/workflows/ci.yml", headBranch: t.branch, headSha: sha, event: "pull_request", status: "completed", conclusion: "success", createdAt: now, updatedAt: now },
      ] }),
    });
    const woke = await until(async () => (await api("GET", `/api/tasks/${id}`)).state !== "deferred", 60);
    note("4a CI answer wakes the task", woke !== null, `left deferred after ${woke}s; now ${(await api("GET", `/api/tasks/${id}`)).state}`);
    // The card polls every 10 s and on task.state.
    const dropped = await until(async () => !(await card.innerText().catch(() => "")).includes(`#${id}`), 15);
    note("4a card drops the wait without a reload", dropped !== null, `after ${dropped}s; server ciWaits ${JSON.stringify(await waits())}`);
    await shot(page, "4a-desktop-ci-waits-cleared");
    note("4a no page errors", page.errors.length === 0, JSON.stringify(page.errors));
    await ctx.close();
  }
});

import fs from "node:fs";
function fs_write(p, s) {
  fs.writeFileSync(p, s);
}

flush(`item2-4-${only.join("-")}`);
await browser.close();
