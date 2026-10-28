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

import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { expect, test } from "./fixtures.js";

// Host scripts end to end (docs/host-scripts.md): a real
// `grain capability host-script` server -- the binary global-setup.js
// built, spawned over stdio by grain demo's own registry -- added in
// Settings → Capability servers, a script defined in its configuration
// form (multi-line script and usage, an env row with a $env: reference),
// tested from its card with an argv holding a space and $(…), and then
// granted on a new task through the with: {script} select. Nothing of
// grain's is stubbed.

const ID = "https://capabilities.e2e.grain.example/host-scripts";

function serverCard(page) {
  return page.locator(`[data-capability-server="${ID}"]`);
}

test("defines a host script, tests it with an argv, and grants it on a task", async ({
  page,
  request,
}) => {
  const bin = process.env.GRAIN_E2E_DEMO_BIN;
  expect(bin, "global-setup.js builds grain").toBeTruthy();
  const dataDir = mkdtempSync(path.join(tmpdir(), "grain-e2e-host-script-"));
  try {
    await page.goto("/settings/capabilities#capability-servers");
    await page.getByTestId("capability-server-add-button").click();
    const add = page.getByTestId("capability-server-add");
    await add.getByLabel(/Capability ID/).fill(ID);
    await add.getByLabel(/^Command/).fill(bin);
    await add
      .getByLabel(/^Arguments, one per line/)
      .fill(
        [
          "capability",
          "host-script",
          "-listen",
          "127.0.0.1:0",
          "-data-dir",
          dataDir,
        ].join("\n"),
      );
    await add.getByRole("button", { name: "Add server" }).click();
    const card = serverCard(page);
    await expect(card.getByTestId("capability-server-state")).toHaveText(
      "attached",
      { timeout: 30_000 },
    );
    await expect(card.getByTestId("host-script-list")).toContainText(
      "No scripts yet",
    );

    // The configuration is a form, not a JSON editor: one card per
    // script, with its script and usage as multi-line text.
    await card.getByRole("button", { name: "Edit server settings" }).click();
    const config = card.getByTestId("capability-server-config");
    await expect(config.getByLabel("Arguments (JSON)")).toHaveCount(0);
    await config.getByRole("button", { name: "Add scripts" }).click();
    const item = config.getByRole("group", { name: /^scripts 1/ });
    await item.getByLabel(/^name/).fill("echo-args");
    await item
      .getByLabel(/^description/)
      .fill("Prints each argument on a line of its own.");
    const usage = item.getByLabel(/^usage/);
    await expect(usage).toHaveJSProperty("tagName", "TEXTAREA");
    await usage.fill("echo-args ARG...\n  Each ARG is printed as <ARG>.");
    const body = item.getByLabel(/^script/);
    await expect(body).toHaveJSProperty("tagName", "TEXTAREA");
    await body.fill(
      [
        'for a in "$@"; do',
        "  printf '<%s>\\n' \"$a\"",
        "done",
        'echo "greeting=$GREETING" >&2',
        "exit 3",
      ].join("\n"),
    );
    await item.getByLabel(/^timeout/).fill("10");
    await item.getByLabel(/^max_args/).fill("8");
    const env = item.locator("[data-arg-map='scripts[0].env']");
    await env.getByRole("button", { name: "Add entry" }).click();
    await env.getByLabel("Name", { exact: true }).fill("GREETING");
    const value = env.getByLabel("Value", { exact: true });
    await expect(value).toHaveAttribute("placeholder", "$env:DEPLOY_TOKEN");
    await value.fill("hello there");
    await config.getByRole("button", { name: "Save server settings" }).click();
    await expect(config).toHaveCount(0);

    const saved = (
      await (await request.get("/api/capability-servers")).json()
    ).find((s) => s.id === ID);
    expect(saved.config.config.scripts).toEqual([
      expect.objectContaining({
        name: "echo-args",
        timeout: 10,
        max_args: 8,
        env: { GREETING: "hello there" },
        usage: "echo-args ARG...\n  Each ARG is printed as <ARG>.",
      }),
    ]);

    // The card lists the script with its usage.
    const row = card.locator("[data-host-script='echo-args']");
    await expect(row).toContainText(
      "Prints each argument on a line of its own.",
    );
    await expect(row.getByTestId("host-script-usage")).toHaveText(
      "echo-args ARG...\n  Each ARG is printed as <ARG>.",
    );
    await expect(row.getByTestId("host-script-limits")).toContainText(
      "timeout 10s",
    );

    // Test it: each argument is passed exactly, a space and $(…) included.
    await row.getByRole("button", { name: "Test" }).click();
    const tester = row.getByTestId("host-script-test");
    await tester.getByRole("button", { name: "Add argument" }).click();
    await tester.getByRole("button", { name: "Add argument" }).click();
    await tester.getByRole("textbox", { name: "Argument 1" }).fill("two words");
    await tester
      .getByRole("textbox", { name: "Argument 2" })
      .fill("$(echo injected)");
    await tester.getByRole("button", { name: "Run echo-args" }).click();
    const result = tester.getByTestId("host-script-result");
    await expect(result.getByTestId("host-script-exit")).toHaveText("exit 3");
    await expect(result.getByTestId("host-script-stdout")).toHaveText(
      "<two words>\n<$(echo injected)>\n",
    );
    await expect(result.getByTestId("host-script-stderr")).toHaveText(
      "greeting=hello there\n",
    );
    await expect(result.getByTestId("host-script-duration")).toHaveText(
      /^\d+ ms$/,
    );
    // At phone width the row, its argument field and its output stay
    // inside the card: nothing runs off its right edge.
    // (The phone layout is a different tree, so the tester starts over.)
    await page.setViewportSize({ width: 390, height: 844 });
    // The server's page is in the path, so it stays open; the list's
    // row is there only if it did not.
    const listRow = page.locator(`[data-capability-server-row="${ID}"]`);
    if (await listRow.isVisible()) await listRow.click();
    if (!(await tester.isVisible())) {
      await row.getByRole("button", { name: "Test" }).click();
      await tester.getByRole("button", { name: "Add argument" }).click();
    }
    await tester
      .getByRole("textbox", { name: "Argument 1" })
      .fill(
        "an-argument-long-enough-to-need-wrapping-at-phone-width-0123456789",
      );
    await tester.getByRole("button", { name: "Run echo-args" }).click();
    await expect(result.getByTestId("host-script-stdout")).toContainText(
      "0123456789",
    );
    const cardBox = await card.boundingBox();
    for (const part of [
      row,
      tester.getByRole("textbox", { name: "Argument 1" }),
      result.getByTestId("host-script-stdout"),
    ]) {
      const box = await part.boundingBox();
      expect(box.x).toBeGreaterThanOrEqual(cardBox.x);
      expect(box.x + box.width).toBeLessThanOrEqual(
        cardBox.x + cardBox.width + 0.5,
      );
    }
    await page.setViewportSize({ width: 1280, height: 720 });

    // Grant it on a new task: with: {script} is a select of the
    // configured scripts.
    await page.goto("/");
    const title = `E2E host script grant ${Date.now()}`;
    await page.getByRole("button", { name: "+ New task" }).click();
    await page.getByLabel("Title").fill(title);
    await page.getByLabel(/^Repos/).selectOption("none");
    const grants = page.getByTestId("new-task-grants");
    await grants.getByRole("button", { name: "Add grant…" }).click();
    await page.getByRole("menuitem", { name: /host-script/ }).click();
    // The method is a section inline; picking the script saves it.
    const section = grants.locator(
      `[data-method="${ID}#host-script"] .grant-section`,
    );
    await expect(section).toHaveCount(1);
    await section.locator("[id*='-args-'][id$='-script']").first().click();
    await page.getByRole("option", { name: "echo-args", exact: true }).click();
    await expect(
      grants.locator(`[data-grant^="${ID}#host-script="]`),
    ).toHaveAttribute("data-grant", /"script":"echo-args"/);
    await page.getByRole("button", { name: "Create task" }).click();

    await expect
      .poll(async () => {
        const tasks = await (await request.get("/api/tasks")).json();
        const list = Array.isArray(tasks) ? tasks : tasks.tasks || [];
        const caps = list.find((t) => t.title === title)?.capabilities;
        return caps ? caps.filter((c) => c.startsWith(ID)) : null;
      })
      .toEqual([`${ID}#host-script={"script":"echo-args"}`]);
  } finally {
    rmSync(dataDir, { recursive: true, force: true });
  }
});
