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

// A task closed with a reason and reopened, read back on its Details
// tab's History at phone width (2026-11-20 intake walkthrough). The
// reopen read as a raw lower-case "reopened" under "Closed", and the
// reason ran into the principal with no space ("(access code)— why").

async function api(baseURL, code, method, path, data) {
  const res = await fetch(`${baseURL}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: data === undefined ? undefined : JSON.stringify(data),
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${path}: ${res.status} ${text}`);
  return text ? JSON.parse(text) : null;
}

test("History names who closed and reopened a task, with the reason set apart @phone", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const task = await api(demoServer, accessCode, "POST", "/api/tasks", {
    title: `History labels ${Date.now()}`,
    approved: true,
  });
  const reason =
    "Superseded by the retry-budget rework, so nobody picks it up while the test is rewritten";
  await page.goto(`task/${task.id}`);
  await page.getByTestId("act-close").click();
  await page.getByTestId("close-reason").fill(reason);
  await page.getByTestId("close-yes").click();
  await page
    .getByRole("button", { name: /^Reopen/ })
    .first()
    .click();
  await expect
    .poll(
      async () =>
        (await api(demoServer, accessCode, "GET", `/api/tasks/${task.id}`))
          .state,
    )
    .toBe("queued");

  await page.goto(`task/${task.id}?tab=details`);
  const history = page.locator("ol.history");
  const reopened = history.locator("li").first();
  await expect(reopened).toContainText(/^\S+ Reopened · /);
  const closed = history.locator("li", { hasText: reason });
  await expect(closed).toContainText(/Closed · .+ — Superseded by/);
  expect(
    await closed.evaluate((el) => el.scrollWidth - el.clientWidth),
  ).toBeLessThanOrEqual(0);
});
