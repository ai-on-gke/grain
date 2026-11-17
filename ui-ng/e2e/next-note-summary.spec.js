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

import { test, expect } from "./fixtures.js";

// A task resting on a summarized comment_on_issue note (Task.noteSummary,
// PR #1218/#1515) reads as that note on /next/: the Inbox row shows the
// note's own line under a "Note" pill, not "Needs reply" over "Answered on
// the task itself.", and the task page names it. The 2026-11-15 §7
// walkthrough (docs/test-runs/2026-11-15-note-events-and-host-notice.md)
// found /next/ ignoring noteSummary while the classic UI used it.
//
// grain demo runs no agent, so nothing it does ends a run on a note: the
// task this files is shown resting on one by rewriting the server's own
// answers for that task alone, with the fields the daemon sends for a real
// note (state awaiting_reply, noteSummary, no questionSummary).

async function api(base, code, method, p, body) {
  const res = await fetch(`${base}${p}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    throw new Error(`${method} ${p}: ${res.status} ${await res.text()}`);
  }
  const txt = await res.text();
  return txt ? JSON.parse(txt) : null;
}

const SUMMARY = "Report: widgets README needs no change";

function restOnNote(value, id) {
  if (Array.isArray(value)) return value.map((v) => restOnNote(v, id));
  if (!value || typeof value !== "object") return value;
  const out = {};
  for (const [k, v] of Object.entries(value)) out[k] = restOnNote(v, id);
  if (out.id === id && typeof out.title === "string") {
    out.state = "awaiting_reply";
    out.noteSummary = SUMMARY;
    delete out.questionSummary;
    delete out.questionCommentId;
    delete out.awaitingAccess;
    delete out.turnStopped;
  }
  return out;
}

test("a task resting on a summarized note reads as a note in the Inbox and on its page", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const title = `Branchless report ${Date.now()}`;
  const { id } = await api(demoServer, accessCode, "POST", "/api/tasks", {
    title,
    description: "Report only.",
  });
  try {
    await page.route(/\/api\/tasks(\/|\?|$)/, async (route) => {
      if (route.request().method() !== "GET") return route.continue();
      const response = await route.fetch();
      const type = response.headers()["content-type"] || "";
      if (!type.includes("application/json")) {
        return route.fulfill({ response });
      }
      await route.fulfill({
        response,
        json: restOnNote(await response.json(), id),
      });
    });

    await page.goto("/next/inbox");
    const row = page.getByTestId("queue-row").filter({ hasText: title });
    await expect(row).toBeVisible();
    await expect(row).toContainText("Note");
    await expect(row).toContainText(SUMMARY);
    await expect(row).not.toContainText("Needs reply");
    await expect(row).not.toContainText("Answered on the task itself.");

    await row.click();
    await expect(page.getByTestId("resolution-question")).toContainText(
      SUMMARY,
    );

    await page.goto(`/next/task/${id}`);
    const note = page.getByTestId("note-summary");
    await expect(note).toContainText(`Note: ${SUMMARY}`);
    await expect(page.getByTestId("question")).toHaveCount(0);
    const box = await note.boundingBox();
    expect(box?.width).toBeLessThanOrEqual(page.viewportSize().width);
  } finally {
    await page.unroute(/\/api\/tasks(\/|\?|$)/);
    await fetch(`${demoServer}/api/tasks/${id}/close`, {
      method: "POST",
      headers: { Authorization: `Bearer ${accessCode}` },
    }).catch(() => {});
  }
});
