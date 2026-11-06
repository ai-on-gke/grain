// §10 item 5: Blocked and answered states over the demo.
// Usage: GRAIN_SERVER=... GRAIN_TOKEN=... node states-walk.mjs
import { createRequire } from "node:module";
import fs from "node:fs/promises";
const require = createRequire(new URL("../../../ui/package.json", import.meta.url));
const { chromium } = require("@playwright/test");
const S = process.env.GRAIN_SERVER;
const T = process.env.GRAIN_TOKEN;
const out = new URL(".", import.meta.url).pathname;
const api = async (method, path, body) => {
  const res = await fetch(S + path, {
    method,
    headers: { Authorization: `Bearer ${T}`, "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  return { status: res.status, body: await res.json().catch(() => null) };
};
const result = {};
const live = (await api("GET", "/api/tasks")).body;
const asked = live.filter((t) => t.state === "awaiting_reply" && !t.interactive);
const full = await Promise.all(asked.map(async (t) => (await api("GET", `/api/tasks/${t.id}`)).body));
const question = full.find((t) => t.questionSummary);
const answer = full.find((t) => !t.questionSummary && !t.pendingSecret && !t.awaitingAccess && !t.turnStopped);
const blockers = live.filter((t) => t.title.startsWith("seed ") && t.state === "queued").slice(1, 3);
const filed = await api("POST", "/api/tasks", {
  title: "walk: blocked on two open tasks",
  description: "depends on two open tasks",
  dependsOn: blockers.map((t) => t.id),
  approved: true,
});
const blocked = filed.body;
result.setup = { question: question?.id, answer: answer?.id, blockers: blockers.map((t) => t.id), blocked: blocked?.id, filedStatus: filed.status };
console.log(result.setup);
await new Promise((r) => setTimeout(r, 4000)); // a reconcile tick
result.blockedApi = (await api("GET", `/api/tasks/${blocked.id}`)).body;
result.blockedApi = { blocked: result.blockedApi.blocked, blockedBy: result.blockedApi.blockedBy, state: result.blockedApi.state };

const browser = await chromium.launch();
const ctx = await browser.newContext({ baseURL: S, viewport: { width: 1440, height: 900 } });
await ctx.request.post("/api/auth/access-code", { data: { code: T } });
const page = await ctx.newPage();
// Everything a row says about a task: its text plus every tooltip-ish attribute in it.
const rowSays = async (title) =>
  page.evaluate((title) => {
    const row = [...document.querySelectorAll(".task-row")].find((r) => r.textContent.includes(title));
    if (!row) return null;
    const attrs = [...row.querySelectorAll("*")].flatMap((el) =>
      ["title", "aria-label", "data-tooltip"].map((a) => el.getAttribute(a)).filter(Boolean),
    );
    return { text: row.innerText.slice(0, 200), attrs };
  }, title);
const hoverTip = async (selector) => {
  const el = page.locator(selector).first();
  if (!(await el.count())) return null;
  await el.hover();
  const tip = page.getByRole("tooltip").first();
  return (await tip.waitFor({ timeout: 2000 }).then(() => tip.innerText(), () => null));
};
try {
  // The list.
  await page.goto("/");
  await page.locator(".task-row").first().waitFor();
  result.listBlocked = await rowSays(blocked.title);
  result.listBlockedTooltip = await hoverTip(
    `.task-row:has-text("walk: blocked on two open tasks") :text("Blocked")`,
  );
  result.listAnswer = await rowSays(answer.title);
  result.listQuestion = await rowSays(question.title);
  await page.screenshot({ path: `${out}/states-list.png` });
  // The panes.
  for (const [k, t] of [["paneBlocked", blocked], ["paneAnswer", answer], ["paneQuestion", question]]) {
    await page.goto(`/tasks/${t.id}`);
    await page.getByText(t.title.slice(0, 25)).first().waitFor();
    await page.waitForTimeout(500);
    const text = await page.locator("body").innerText();
    result[k] = {
      blockedLine: await page.locator(".detail-blocked").first().innerText().catch(() => null),
      answered: text.includes("Answered in a comment"),
      askedQuestion: text.includes("Asked a question"),
      badgeTitles: await page.$$eval("[title]", (els) =>
        els.map((e) => e.getAttribute("title")).filter((x) => /Answered in a comment|Asked a question|Blocked on/.test(x)),
      ),
    };
    await page.screenshot({ path: `${out}/states-${k}.png` });
  }
  // The inbox.
  await page.goto("/inbox");
  await page.waitForTimeout(1500);
  const inbox = await page.locator("main").innerText();
  const near = (title) => {
    const i = inbox.indexOf(title.slice(0, 25));
    return i < 0 ? null : inbox.slice(Math.max(0, i - 200), i + 400);
  };
  result.inboxAnswer = near(answer.title);
  result.inboxQuestion = near(question.title);
  await page.screenshot({ path: `${out}/states-inbox.png`, fullPage: true });
} finally {
  await api("POST", `/api/tasks/${blocked.id}/close`);
  await browser.close();
}
await fs.writeFile(`${out}/states-walk.json`, JSON.stringify(result, null, 2));
console.log(JSON.stringify(result, null, 1));
