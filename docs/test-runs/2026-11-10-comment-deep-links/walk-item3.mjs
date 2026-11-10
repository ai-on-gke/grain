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

// docs/test-plans/2026-11-10.md §5 item 3 against ui/e2e/consolerestserver
// (the real store, ui.Server, orchestrator and host sandboxes; only the
// agent is scripted, and its tool calls go through a real `grain
// mcpserver`). Copy it into ui/e2e/ (it imports ./accesscode.js), start
// the server with GRAIN_E2E_DEMO_BIN pointing at a built grain (or the
// sandbox's own `grain` CLI is found instead and has no mcpserver) and
// GRAIN_E2E_ACCESS_CODE set, then from ui/:
//   node e2e/walk-item3.mjs <server url> <access code>
import { chromium } from "@playwright/test";
import { signIn } from "./accesscode.js";

const [url, code] = process.argv.slice(2);
const H = {
  Authorization: `Bearer ${code}`,
  "Content-Type": "application/json",
};
async function api(method, p, body) {
  const r = await fetch(`${url}${p}`, {
    method,
    headers: H,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await r.text();
  if (!r.ok) throw new Error(`${method} ${p}: ${r.status} ${text}`);
  return text ? JSON.parse(text) : null;
}
async function until(what, fn, ms = 90_000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 500));
  }
}
const out = {};

// T1: a long conversation, then a run that asks a question.
const t1 = await api("POST", "/api/tasks", {
  title: "Walk item 3 asker",
  body: "Ask which retry budget to use.",
  repo: "acme/widgets",
  approved: false,
});
for (let i = 0; i < 60; i++) {
  await api("POST", `/api/tasks/${t1.id}/comments`, {
    body: `Earlier discussion line ${i}.`,
  });
}
await api("POST", "/e2e/script-runs", {
  taskId: t1.id,
  turns: [
    {
      mcpCalls: [
        {
          name: "ask_question",
          arguments: {
            summary: "Retry budget: 3 or 5?",
            question: "Should the retry budget be **3** or **5**?",
          },
        },
      ],
      finalText: "asked",
    },
  ],
});
await api("POST", `/api/tasks/${t1.id}/approve`, {});
await until("T1 to park", async () => {
  const t = await api("GET", `/api/tasks/${t1.id}`);
  return t.state === "awaiting_reply" ? t : null;
});
const d1 = await api("GET", `/api/tasks/${t1.id}`);
const question = [...d1.comments]
  .reverse()
  .find((c) => /retry budget be/.test(c.body));
const inbox = await api("GET", "/api/inbox");
const item = (inbox.items || inbox.questions || inbox)
  .flat?.()
  .find?.((i) => i.task === t1.id || i.Task === t1.id);
out.question = {
  commentId: question?.id,
  author: question?.author,
  authorKind: question?.authorKind,
};
out.inboxRaw = JSON.stringify(inbox).slice(0, 600);
out.inboxItem = item || null;

// T2: an agent comment linking to T1's question, written root-relative.
const t2 = await api("POST", "/api/tasks", {
  title: "Walk item 3 linker",
  body: "Point at the question.",
  repo: "acme/widgets",
  approved: false,
});
const link = `/tasks/${t1.id}#comment=${question.id}`;
await api("POST", "/e2e/script-runs", {
  taskId: t2.id,
  turns: [
    {
      mcpCalls: [
        {
          name: "comment_on_issue",
          arguments: { comment: `Blocked on [see](${link}) first.` },
        },
      ],
      finalText: "done",
    },
  ],
});
await api("POST", `/api/tasks/${t2.id}/approve`, {});
const d2 = await until("T2's comment", async () => {
  const t = await api("GET", `/api/tasks/${t2.id}`);
  return t.comments?.some((c) => c.body.includes(link)) ? t : null;
});
const agentComment = d2.comments.find((c) => c.body.includes(link));
out.agentComment = {
  id: agentComment.id,
  author: agentComment.author,
  authorKind: agentComment.authorKind,
  body: agentComment.body,
  state: d2.state,
};

// The browser.
const state = await signIn(url, code);
const browser = await chromium.launch();
const context = await browser.newContext({
  storageState: state,
  viewport: { width: 1440, height: 900 },
});
const page = await context.newPage();
const errors = [];
page.on("console", (m) => m.type() === "error" && errors.push(m.text()));
page.on("pageerror", (e) => errors.push(String(e)));
const notFound = [];
page.on("response", (r) => {
  if (r.status() === 404) notFound.push(new URL(r.url()).pathname);
});

// The inbox item's own path, as `grain inbox` and agents read it.
const inboxPath = out.inboxItem?.path || out.inboxItem?.Path;
if (inboxPath) {
  await page.goto(`${url}${inboxPath}`);
  const el = page.locator(`[data-comment-id="${question.id}"]`).first();
  await el.waitFor({ state: "visible", timeout: 15_000 });
  out.inboxPathLanding = await el.evaluate((n) => {
    const r = n.getBoundingClientRect();
    return {
      inViewport: r.top >= 0 && r.bottom <= innerHeight,
      highlighted: n.classList.contains("comment-highlight"),
      focused: document.activeElement === n,
      text: n.innerText.slice(0, 120),
    };
  });
}

// The web Inbox: what its question row offers to open.
await page.goto(`${url}/inbox`);
await page.getByText("Walk item 3 asker").first().waitFor({ timeout: 15_000 });
const row = page
  .locator("li, .inbox-row, [data-task-id]")
  .filter({ hasText: "Walk item 3 asker" })
  .first();
out.webInboxRow = {
  links: await row
    .locator("a")
    .evaluateAll((as) => as.map((a) => a.getAttribute("href"))),
  buttons: await row.getByRole("button").allInnerTexts(),
};
await page.screenshot({ path: "/tmp/item3-inbox.png" });

// Clicking the task's title in the web Inbox.
await page.getByText("Walk item 3 asker").first().click();
await page.waitForTimeout(2500);
out.webInboxClick = {
  url: page.url(),
  questionInViewport: await page
    .locator(`[data-comment-id="${question.id}"]`)
    .first()
    .evaluate((n) => {
      const r = n.getBoundingClientRect();
      return r.top >= 0 && r.bottom <= innerHeight && r.height > 0;
    })
    .catch(() => "absent"),
};

// The agent-written link renders as an in-app link and is followed in
// place.
await page.goto(`${url}/tasks/${t2.id}`);
await page.evaluate(() => (window.__noReload = true));
const a = page.getByRole("link", { name: "see" }).first();
out.agentLink = {
  href: await a.getAttribute("href"),
  target: await a.getAttribute("target"),
};
await a.click();
const landed = page.locator(`[data-comment-id="${question.id}"]`).first();
await landed.waitFor({ state: "visible", timeout: 15_000 });
await page.waitForTimeout(200);
out.agentLinkLanding = await landed.evaluate((n) => {
  const r = n.getBoundingClientRect();
  return {
    url: location.pathname + location.hash,
    inViewport: r.top >= 0 && r.bottom <= innerHeight,
    highlighted: n.classList.contains("comment-highlight"),
    focused: document.activeElement === n,
    noReload: window.__noReload === true,
  };
});
await page.screenshot({ path: "/tmp/item3-landing.png" });
out.errors = errors;
// The console's "Failed to load resource" lines name no URL: these are
// the requests behind them.
out.notFound = notFound;
console.log(JSON.stringify(out, null, 1));
await browser.close();
