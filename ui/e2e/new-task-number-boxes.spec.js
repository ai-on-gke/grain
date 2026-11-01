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

// New task's number boxes accept exponent notation, as every
// type="number" input does: "1e3" is a valid Task priority of 1000 and
// "8e3" a valid 8000 MiB of memory. The form read them with parseInt,
// which stops at the "e", and filed priority 1 and 8 MiB without a word
// (docs/test-runs/2026-11-01-task-intake-inbox-lifecycle.md, bug 2).

test("New task files a number box's exponent entry as the number it is", async ({
  page,
}) => {
  await page.goto("/new");
  const dialog = page.getByRole("dialog");
  await dialog.getByLabel(/^Title/).fill("Number boxes read whole");
  await dialog.getByLabel(/^Repos/).selectOption("none");
  await dialog.getByLabel("Task priority").fill("1e3");
  await dialog.getByRole("button", { name: /advanced options/i }).click();
  await dialog.getByLabel(/^memory \(mib\)/i).fill("8e3");

  const created = page.waitForResponse(
    (response) =>
      /\/api\/tasks$/.test(response.url()) &&
      response.request().method() === "POST",
    { timeout: 15_000 },
  );
  await dialog.getByRole("button", { name: "Create task" }).click();
  const response = await created;
  expect(response.status()).toBe(201);
  const sent = JSON.parse(response.request().postData());
  expect(sent.priority).toBe(1000);
  expect(sent.sandboxMemoryMb).toBe(8000);

  const task = await response.json();
  try {
    const filed = await (
      await page.request.get(`/api/tasks/${task.id}`)
    ).json();
    expect(filed.priority).toBe(1000);
  } finally {
    await page.request.post(`/api/tasks/${task.id}/close`, { data: {} });
  }
});
