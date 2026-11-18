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

// The Inbox reads a selected alert's whole body from its family
// (GET /api/notifications?open=1&family=): the polled list carries only
// each body's first line. Switching to another alert of the same family
// shows that one's body and none of the first's; a family read that
// fails leaves the first line; and an alert raised again with a new body
// never goes on showing the previous raise's body -- which, keyed by id
// alone, it did for good once the re-read failed (task 6744's
// Adversarial review of docs/test-runs/2026-11-16-next-markdown.md).

async function api(baseURL, code, method, path, data) {
  const res = await fetch(`${baseURL}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: data === undefined ? undefined : JSON.stringify(data),
  });
  if (!res.ok)
    throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
  const text = await res.text();
  return text ? JSON.parse(text) : null;
}

const body = (tag) =>
  [
    `${tag} first line: the cache tier is degraded`,
    "",
    ...Array.from({ length: 6 }, (_, i) => `- ${tag} detail ${i + 1}`),
  ].join("\n");

const isFamilyRead = (u) =>
  u.pathname.endsWith("/api/notifications") && u.searchParams.has("family");

test("an alert's whole body follows the selection, a failed read and a re-raise", async ({
  page,
  demoServer,
  accessCode,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const stamp = Date.now();
  const fam = `alert-body-${stamp}`;
  const raise = (k, b, severity = "warning") =>
    api(demoServer, accessCode, "POST", "/api/notifications", {
      subject: `${fam}:${k}`,
      title: `Alert body ${k} ${stamp}`,
      severity,
      body: b,
    });
  const ids = [];
  try {
    ids.push((await raise("a", body("ALPHA"))).notification.id);
    ids.push((await raise("b", body("BETA"))).notification.id);
    let failReads = false;
    await page.route(isFamilyRead, (route) =>
      failReads
        ? route.fulfill({ status: 500, body: "family read failed" })
        : route.continue(),
    );
    const question = page.getByTestId("resolution-question").first();

    await page.goto("inbox");
    await page.getByText(`Alert body a ${stamp}`).first().click();
    await expect(question).toContainText("ALPHA detail 6");

    // Another alert in the same family.
    await page.getByText(`Alert body b ${stamp}`).first().click();
    await expect(question).toContainText("BETA detail 6");
    await expect(question).not.toContainText("ALPHA");

    // Raised again with a new body while its family can no longer be
    // read: the new raise's first line, not the old raise's body. It
    // rises to critical so the re-raise is an event (one at the same
    // severity only bumps its count and waits for the 60s poll).
    failReads = true;
    await raise("b", body("GAMMA"), "critical");
    await expect(question).toContainText("GAMMA first line", {
      timeout: 20_000,
    });
    await expect(question).not.toContainText("BETA");
    await expect(question).not.toContainText("GAMMA detail");

    // Readable again: the next selection reads the new raise whole.
    failReads = false;
    await page.getByText(`Alert body a ${stamp}`).first().click();
    await expect(question).toContainText("ALPHA detail 6");
    await page.getByText(`Alert body b ${stamp}`).first().click();
    await expect(question).toContainText("GAMMA detail 6");
  } finally {
    for (const id of ids) {
      await fetch(`${demoServer}/api/notifications/${id}/clear`, {
        method: "POST",
        headers: { Authorization: `Bearer ${accessCode}` },
      }).catch(() => {});
    }
  }
});
