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

import { test as base, expect } from "@playwright/test";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { pageErrors } from "./pageerrors.js";
import { signInContext } from "./accesscode.js";
import { eventually, startDaemon } from "./realdaemon.js";

const test = base.extend(pageErrors);

test.setTimeout(240_000);

// A person's way from a refused setting to a held grain/config pull
// request, on a real daemon with its own grain/config and settings
// locked (docs/test-plans/2026-10-30.md §2): the refusal dialog's
// Create Config PR files the task, its Submit merges it and the sync
// applies it -- and a refusal grain/config has moved past since says to
// make the change again, where the person can see it.

async function newTemplate(page, url, name) {
  await page.goto(`${url}/processes`);
  await page.getByRole("button", { name: "+ New", exact: true }).click();
  await page
    .getByTestId("new-process-kind")
    .getByRole("menuitem", { name: /template/i })
    .click();
  await page.getByLabel("Name", { exact: true }).fill(name);
  await page.getByLabel("Task title").first().fill("Say hello");
  await page.getByRole("button", { name: "Add template" }).click();
  const dialog = page.getByTestId("config-refusal-dialog");
  await expect(dialog).toBeVisible();
  return dialog;
}

async function templateNames(request, url) {
  const res = await request.get(`${url}/api/templates`);
  expect(res.ok()).toBeTruthy();
  return (await res.json()).map((t) => t.name);
}

// inView is whether locator's box starts inside the viewport: an alert
// pushed below a tall patch is one a person never sees.
async function expectInView(page, locator) {
  const box = await locator.boundingBox();
  const { height } = page.viewportSize();
  expect(box, "the alert has a box").toBeTruthy();
  expect(box.y).toBeGreaterThanOrEqual(0);
  expect(box.y + Math.min(box.height, 40)).toBeLessThanOrEqual(height);
}

test("a refused setting opens a held config pull request that Submit lands", async ({
  page,
}) => {
  const root = await fs.mkdtemp(
    path.join(os.tmpdir(), "grain-config-refusal-pr-e2e-"),
  );
  const dataDir = path.join(root, "data");
  const sandboxDir = path.join(root, "sandboxes");
  await fs.mkdir(dataDir);
  await fs.mkdir(sandboxDir);
  await fs.writeFile(path.join(root, "gemini-key"), "test-only-key\n", {
    mode: 0o600,
  });

  let daemon;
  try {
    // A merger, so a submitted grain/config pull request lands.
    daemon = await startDaemon(
      root,
      dataDir,
      sandboxDir,
      ["-max-mergers", "1"],
      {
        directConfigWrites: false,
      },
    );
    await signInContext(page.context(), daemon.url);
    const { url } = daemon;
    const locked = await page.request.put(`${url}/api/settings`, {
      data: { settingsLocked: true },
    });
    expect(locked.ok()).toBeTruthy();

    // Desktop: refused, opened, submitted, applied.
    await page.setViewportSize({ width: 1440, height: 900 });
    const dialog = await newTemplate(page, url, "Held template");
    await expect(dialog).toContainText(
      "changed with a config pull request there",
    );
    await dialog.getByRole("button", { name: "Create Config PR" }).click();
    const opened = dialog.getByTestId("config-refusal-opened");
    await expect(opened).toContainText("Opened config pull request");
    await expect(opened).toContainText("It waits for a person to submit it.");
    await expectInView(page, opened);
    expect(await templateNames(page.request, url)).not.toContain(
      "Held template",
    );
    // The template editor behind the dialog still holds the edit the
    // pull request now carries, and leaving it asks whether to discard
    // it (App.jsx's onPopState): yes.
    page.once("dialog", (d) => d.accept());
    await opened.getByRole("link", { name: /^task \d+$/ }).click();
    await expect(page).toHaveURL(/\/tasks\/\d+$/);
    const taskId = page.url().match(/\/tasks\/(\d+)$/)[1];

    await page.goto(`${url}/inbox`);
    await expect(
      page.getByText(`Asked you to submit grain/config#${taskId}`),
    ).toBeVisible();

    await page.goto(`${url}/tasks/${taskId}`);
    await page
      .getByRole("button", { name: /^submit$/i })
      .last()
      .click();
    await eventually(
      async () =>
        (await templateNames(page.request, url)).includes("Held template"),
      "the submitted config pull request was not applied",
      90_000,
    );

    // Phone: somebody else's change lands while the dialog is open, so
    // its patch no longer applies.
    await page.setViewportSize({ width: 390, height: 844 });
    const stale = await newTemplate(page, url, "Stale template");
    const other = await page.request.post(`${url}/api/templates`, {
      data: { name: "Other template", title: "Other" },
    });
    expect(other.status()).toBe(409);
    const pr = await page.request.post(`${url}/api/state-repo/pull-requests`, {
      data: { changes: (await other.json()).change },
    });
    expect(pr.status()).toBe(201);
    const otherTask = (await pr.json()).taskId;
    const submitted = await page.request.post(
      `${url}/api/tasks/${otherTask}/submit`,
    );
    expect(submitted.ok()).toBeTruthy();
    await eventually(
      async () =>
        (await templateNames(page.request, url)).includes("Other template"),
      "the other config pull request was not applied",
      90_000,
    );

    await stale.getByRole("button", { name: "Create Config PR" }).click();
    const error = stale.getByTestId("config-refusal-error");
    await expect(error).toContainText(
      "grain/config has changed since this was refused",
    );
    await expect(error).toContainText("make the change again");
    await expectInView(page, error);
    await expect(
      stale.getByRole("button", { name: "Create Config PR" }),
    ).toHaveCount(0);
    expect(await templateNames(page.request, url)).not.toContain(
      "Stale template",
    );
  } finally {
    await daemon?.stop();
    await fs.rm(root, { recursive: true, force: true });
  }
});
