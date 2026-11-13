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

import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { accessCode } from "./accesscode.js";
import { expect, test } from "./fixtures.js";
import { chooseOption } from "./searchSelect.js";

// Layout of the Grants section (GrantsPicker.jsx,
// design/design/unified-grants.design.md) on the task pane, at desktop and phone
// widths: the held grants are one stack of sections inside it -- a grant
// with nothing to set on a single line -- "Add grant…" sits under it,
// with no legacy checklist beside it, and the menu stays on screen.

const KEY = "https://grain.dev/capabilities/cloud-key#cloud-key";
// grain demo's own capability server's method with arguments.
const CLOUD_KEY = "https://capabilities.demo.grain.example/cloud#cloud-key";

for (const [label, size] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`grants are one stack of sections (${label})`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize(size);
    const created = await request.post("/api/tasks", {
      data: {
        title: `E2E grants layout ${Date.now()}`,
        repo: "acme/widgets",
        capabilities: [
          "api:ci:read",
          "api:task-admin@acme/widgets",
          "api:merge:force",
          `${CLOUD_KEY}={"account":"dev","project":"acme-dev"}`,
        ],
      },
    });
    expect(created.ok()).toBeTruthy();
    const task = await created.json();
    await page.goto(`/tasks/${task.id}`);

    const section = page.getByTestId(`task-${task.id}-grants`);
    await section.scrollIntoViewIfNeeded();
    const sections = section.locator(".grants-held .grant-section[data-grant]");
    await expect(sections).toHaveCount(4);
    const box = await section.boundingBox();
    let previous = null;
    for (const s of await sections.all()) {
      const c = await s.boundingBox();
      expect(c.x).toBeGreaterThanOrEqual(box.x - 0.5);
      expect(c.x + c.width).toBeLessThanOrEqual(box.x + box.width + 0.5);
      if (previous)
        expect(c.y).toBeGreaterThanOrEqual(previous.y + previous.height - 0.5);
      previous = c;
      // The remove button sits at the right-hand end of the section header.
      const remove = await s.locator("button.grant-remove").boundingBox();
      expect(remove.y).toBeGreaterThanOrEqual(c.y - 0.5);
      expect(remove.y + remove.height).toBeLessThanOrEqual(
        c.y + c.height + 0.5,
      );
      expect(remove.x + remove.width).toBeGreaterThan(c.x + c.width - 48);
    }
    // Both a repository-limited API grant and a server method grant edit
    // their with: arguments inline inside their sections.
    const repoLimitedApi = section.locator(
      '.grants-held .grant-section[data-grant=\'https://grain.dev/capabilities/api#task-admin={"repo":"acme/widgets"}\']',
    );
    await expect(repoLimitedApi.getByLabel(/^repo/)).toHaveValue(
      "acme/widgets",
    );
    const args = section.locator(
      `.grants-held .grant-section[data-grant^="${CLOUD_KEY}="]`,
    );
    await expect(args.getByLabel(/^project/)).toHaveValue("acme-dev");
    for (const el of [args, args.getByLabel(/^project/)]) {
      const c = await el.boundingBox();
      expect(c.x).toBeGreaterThanOrEqual(box.x - 0.5);
      expect(c.x + c.width).toBeLessThanOrEqual(box.x + box.width + 0.5);
    }
    // A dangerous scope says so on its section.
    await expect(
      section
        .locator(
          '[data-grant="https://grain.dev/capabilities/api#merge:force"]',
        )
        .getByText(/Dangerous:/),
    ).toBeVisible();
    // Nothing in the section pushes the page sideways.
    expect(box.x + box.width).toBeLessThanOrEqual(size.width);

    // "Add grant…" is below the last grant, not beside it.
    const held = await section.locator(".grants-held").boundingBox();
    const add = section.getByRole("button", { name: "Add grant…" });
    const addBox = await add.boundingBox();
    expect(addBox.y).toBeGreaterThanOrEqual(held.y + held.height - 0.5);

    // There is no legacy checklist beside it any more.
    await expect(
      page.getByRole("combobox", { name: "Legacy capabilities" }),
    ).toHaveCount(0);

    // The menu opens on screen.
    await add.click();
    const menu = page.getByRole("menu", { name: "Grants to add" });
    await expect(menu).toBeVisible();
    const m = await menu.boundingBox();
    expect(m.x).toBeGreaterThanOrEqual(0);
    expect(m.x + m.width).toBeLessThanOrEqual(size.width);
    const search = await page.getByLabel("Search grants").boundingBox();
    expect(search.y + search.height).toBeLessThanOrEqual(m.y + 0.5);
    await page.keyboard.press("Escape");
  });
}

test("one search on the new task overlay reaches both an API scope and a server method", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: "+ New task" }).click();
  await chooseOption(page.getByLabel(/^Repos/), "none");
  const section = page.getByTestId("new-task-grants");
  await section.getByRole("button", { name: "Add grant…" }).click();
  await page.getByLabel("Search grants").fill("ci:read");
  await page.getByRole("menuitemcheckbox", { name: "ci:read" }).click();
  await page.getByLabel("Search grants").fill("cloud-key");
  await page.getByRole("menuitem", { name: /cloud-key/ }).click();
  // cloud-key needs arguments: it is a draft section inline, with its
  // form, and removing the draft grants nothing.
  const held = section.locator(".grants-held");
  const draft = held.locator("[data-grant-draft]");
  await expect(draft).toBeVisible();
  await expect(draft.getByLabel(/^project/)).toBeVisible();
  await draft.getByRole("button", { name: "Remove cloud-key draft" }).click();
  await expect(draft).toHaveCount(0);
  await expect(
    held.locator('[data-grant="https://grain.dev/capabilities/api#ci:read"]'),
  ).toBeVisible();
  await expect(held.locator(`[data-grant^="${KEY}"]`)).toHaveCount(0);
});

async function startSeededBareServer(dataDir) {
  const helper = process.env.GRAIN_E2E_DEFAULTS_BIN;
  if (!helper) throw new Error("GRAIN_E2E_DEFAULTS_BIN is unset");
  const proc = spawn(
    helper,
    [
      "-addr",
      "127.0.0.1:0",
      "-data-dir",
      dataDir,
      "-seed-bare-grant",
      "1=self-debug",
      "-seed-bare-grant",
      "1=api:tasks:read",
    ],
    { stdio: ["ignore", "pipe", "pipe"] },
  );
  let output = "";
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(
      () =>
        reject(new Error(`defaults server did not start. Output:\n${output}`)),
      60_000,
    );
    const finish = (fn, value) => {
      clearTimeout(timer);
      fn(value);
    };
    const read = (chunk) => {
      output += chunk;
      const match = output.match(
        /defaults server at (http:\/\/127\.0\.0\.1:\d+)/,
      );
      if (match) finish(resolve, match[1]);
    };
    proc.stdout.on("data", read);
    proc.stderr.on("data", read);
    proc.on("error", (err) => finish(reject, err));
    proc.on("exit", (code, signal) =>
      finish(
        reject,
        new Error(
          `defaults server exited (code ${code}, signal ${signal}). Output:\n${output}`,
        ),
      ),
    );
  });
  return {
    url,
    async stop() {
      const stopped = new Promise((resolve) => proc.once("exit", resolve));
      proc.kill("SIGKILL");
      await stopped;
    },
  };
}

for (const [label, size] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`task pane shows a held bare capability id as a removable GrantLine (${label})`, async ({
    page,
  }) => {
    const dataDir = await fs.mkdtemp(
      path.join(os.tmpdir(), "grain-bare-grant-"),
    );
    const server = await startSeededBareServer(dataDir);
    try {
      await page.setViewportSize(size);
      await page.goto(`${server.url}/tasks/1`);

      const section = page.getByTestId("task-1-grants");
      await section.scrollIntoViewIfNeeded();
      const bareLine = section.locator('.grant-line[data-grant="self-debug"]');
      await expect(bareLine).toBeVisible();
      await expect(bareLine).toContainText(
        "Retired alias of api#self-debug + grain#source",
      );
      await expect(
        bareLine.getByText("retired alias", { exact: true }),
      ).toBeVisible();

      const sectionBox = await section.boundingBox();
      const lineBox = await bareLine.boundingBox();
      expect(lineBox.x).toBeGreaterThanOrEqual(sectionBox.x - 0.5);
      expect(lineBox.x + lineBox.width).toBeLessThanOrEqual(
        sectionBox.x + sectionBox.width + 0.5,
      );
      expect(lineBox.height).toBeLessThanOrEqual(44);
      expect(sectionBox.x + sectionBox.width).toBeLessThanOrEqual(size.width);

      const labelBox = await bareLine.locator(".grant-label").boundingBox();
      const badgeBox = await bareLine.locator(".grant-badge").boundingBox();
      expect(
        Math.abs(
          badgeBox.y + badgeBox.height / 2 - (labelBox.y + labelBox.height / 2),
        ),
      ).toBeLessThan(4);

      const removeBtn = bareLine.getByRole("button", {
        name: "Remove self-debug (retired alias)",
      });
      const removeBox = await removeBtn.boundingBox();
      expect(removeBox.y).toBeGreaterThanOrEqual(lineBox.y - 0.5);
      expect(removeBox.y + removeBox.height).toBeLessThanOrEqual(
        lineBox.y + lineBox.height + 0.5,
      );
      expect(removeBox.x + removeBox.width).toBeGreaterThan(
        lineBox.x + lineBox.width - 48,
      );

      await removeBtn.click();
      await expect(bareLine).toHaveCount(0);

      await page.reload();
      const reloaded = page.getByTestId("task-1-grants");
      await expect(
        reloaded.locator('.grant-line[data-grant="api:tasks:read"]'),
      ).toBeVisible();
      await expect(
        reloaded.locator('.grant-line[data-grant="self-debug"]'),
      ).toHaveCount(0);

      const detailRes = await fetch(`${server.url}/api/tasks/1`, {
        headers: { Authorization: `Bearer ${accessCode()}` },
      });
      expect(detailRes.ok).toBeTruthy();
      const detail = await detailRes.json();
      expect(detail.capabilities).toEqual(["api:tasks:read"]);
    } finally {
      await server.stop();
      await fs.rm(dataDir, { recursive: true, force: true });
    }
  });
}

for (const [label, size] of [
  ["desktop", { width: 1440, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`grain/* role presets and repo#credential draft stay aligned within the pane (${label})`, async ({
    page,
    request,
  }) => {
    await page.setViewportSize(size);
    const created = await request.post("/api/tasks", {
      data: {
        title: `E2E grain presets & credential layout ${Date.now()}`,
        noRepo: true,
        capabilities: [],
      },
    });
    expect(created.ok()).toBeTruthy();
    const task = await created.json();
    await page.goto(`/tasks/${task.id}`);

    const section = page.getByTestId(`task-${task.id}-grants`);
    await section.getByRole("button", { name: "Add grant…" }).click();
    const menu = page.getByRole("menu", { name: "Grants to add" });
    await page.getByLabel("Search grants").fill("grain/operator");

    const opRow = menu.locator('[data-api-grant="api:grain/operator"]');
    await expect(opRow).toBeVisible();
    const menuBox = await menu.boundingBox();
    const rowBox = await opRow.boundingBox();
    expect(rowBox.x).toBeGreaterThanOrEqual(menuBox.x - 0.5);
    expect(rowBox.x + rowBox.width).toBeLessThanOrEqual(
      menuBox.x + menuBox.width + 0.5,
    );
    expect(rowBox.height).toBeLessThanOrEqual(160);

    const codeBox = await opRow.locator("code").boundingBox();
    const badgeBox = await opRow.locator(".api-dangerous").boundingBox();
    expect(
      Math.abs(
        badgeBox.y + badgeBox.height / 2 - (codeBox.y + codeBox.height / 2),
      ),
    ).toBeLessThan(4);

    await page
      .getByLabel("Search grants")
      .fill("https://grain.dev/capabilities/repo#credential");
    await menu
      .locator('[data-method="https://grain.dev/capabilities/repo#credential"]')
      .click();

    const draft = section.locator(
      '[data-method="https://grain.dev/capabilities/repo#credential"] [data-grant-draft]',
    );
    await expect(draft).toBeVisible();
    const draftBox = await draft.boundingBox();
    expect(draftBox.x).toBeGreaterThanOrEqual(0);
    expect(draftBox.x + draftBox.width).toBeLessThanOrEqual(size.width);

    const select = draft.locator("[id*='-args-'][id$='-credential']").first();
    const selectBox = await select.boundingBox();
    expect(selectBox.x).toBeGreaterThanOrEqual(draftBox.x - 0.5);
    expect(selectBox.x + selectBox.width).toBeLessThanOrEqual(
      draftBox.x + draftBox.width + 0.5,
    );
  });
}
