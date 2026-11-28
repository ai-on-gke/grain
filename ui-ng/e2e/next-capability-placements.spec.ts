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

import type { APIRequestContext, Page } from "@playwright/test";
import { expect, test } from "./fixtures";

// The config-map server's config maps and secret maps, and the
// host-script server's scripts, on their servers' pages under
// /next/settings/capabilities/<id> in place of the schema form (task
// 6219, ported from the classic ConfigMapList and HostScriptList). Runs
// over `grain demo -secrets-store`, so a secret map's value really is
// written to the secret store, and Test really runs the demo's "ops"
// script. Every test puts the servers' configuration back.

test.use({ demoArgs: ["-secrets-store"] });

const CONFIG_MAP = "https://grain.dev/capabilities/config-map";
const HOST_SCRIPT = "https://grain.dev/capabilities/host-script";

// Each card sits on its server's own page, in place of the schema form.
const pagePath = (id: string) =>
  `/next/settings/capabilities/${encodeURIComponent(id)}`;

interface ServerRow {
  id: string;
  config: Record<string, unknown>;
}

async function serverConfig(request: APIRequestContext, id: string) {
  const res = await request.get("/api/capability-servers");
  const rows = (await res.json()) as ServerRow[];
  const row = rows.find((r) => r.id === id);
  if (!row) throw new Error(`no capability server ${id}`);
  return row.config;
}

// restore puts a server's stored config back and drops any secret the
// test made, so the worker's other tests see the demo as seeded.
async function restore(
  request: APIRequestContext,
  id: string,
  config: Record<string, unknown>,
  secretsBefore: string[],
) {
  await request.put("/api/capability-servers", { data: { id, config } });
  const listed = await (await request.get("/api/secrets")).json();
  for (const s of listed.secrets ?? []) {
    if (!secretsBefore.includes(s.name)) {
      await request.delete(`/api/secrets/${encodeURIComponent(s.name)}`);
    }
  }
}

async function secretNames(request: APIRequestContext): Promise<string[]> {
  const listed = await (await request.get("/api/secrets")).json();
  return (listed.secrets ?? []).map((s: { name: string }) => s.name);
}

async function confirmDialog(page: Page, button: string) {
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: button, exact: true }).click();
  await expect(dialog).toHaveCount(0);
}

async function noSidewaysScroll(page: Page) {
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
}

test("Config maps and secret maps: add, list where they land, edit and remove @dark", async ({
  page,
  request,
  axe,
}) => {
  const before = await serverConfig(request, CONFIG_MAP);
  const secretsBefore = await secretNames(request);
  try {
    await page.goto(pagePath(CONFIG_MAP));
    const card = page.getByTestId("card-config-maps");
    await expect(card).toBeVisible();
    await expect(page.getByTestId("configure-btn")).toHaveCount(0);
    await expect(
      card.getByRole("heading", { name: "Config maps and secret maps" }),
    ).toBeVisible();
    await expect(card.getByTestId("config-maps-empty")).toBeVisible();

    await card.getByRole("button", { name: "Edit maps" }).click();
    const editor = card.getByTestId("config-map-editor");
    await editor.getByRole("button", { name: "Add config map" }).click();
    const cm = editor.getByRole("group", { name: "Config map 1" });
    await cm.getByLabel("Name", { exact: true }).fill("walk-tools");
    const file = editor.getByRole("group", {
      name: "Config map walk-tools file 1",
    });
    await file.getByLabel("Key", { exact: true }).fill("run.sh");
    // The group is named after its key once it has one.
    const runSh = editor.getByRole("group", {
      name: "Config map walk-tools file run.sh",
    });
    await runSh.getByLabel("Executable").check();
    await expect(runSh.getByLabel("Mode")).toHaveAttribute(
      "placeholder",
      "0755",
    );
    await runSh.getByLabel("File content").fill("echo hello\n");

    await editor.getByRole("button", { name: "Add secret map" }).click();
    const sm = editor.getByRole("group", { name: "Secret map 1" });
    await sm.getByLabel("Name", { exact: true }).fill("walk-deploy");
    const key = editor.getByRole("group", {
      name: "Secret map walk-deploy file 1",
    });
    await key.getByLabel("Key", { exact: true }).fill("token");
    const token = editor.getByRole("group", {
      name: "Secret map walk-deploy file token",
    });
    const save = editor.getByRole("button", { name: "Save maps" });
    // A new secret key needs its value before Save will send it.
    await expect(token.getByText("Type this key's value.")).toBeVisible();
    await expect(save).toBeDisabled();
    const value = token.getByLabel("Secret value");
    await expect(value).toHaveAttribute("type", "password");
    await value.fill("walk-secret-value");
    await expect(save).toBeEnabled();
    expect(await axe()).toEqual([]);

    const put = page.waitForRequest(
      (r) =>
        r.url().endsWith("/api/capability-servers") && r.method() === "PUT",
    );
    await save.click();
    const body = (await put).postDataJSON();
    expect(body.config.config.config_maps).toEqual([
      {
        name: "walk-tools",
        data: { "run.sh": { content: "echo hello\n", executable: true } },
      },
    ]);
    expect(JSON.parse(body.configSecrets.secret_values)).toEqual({
      "walk-deploy/token": "walk-secret-value",
    });
    await expect(editor).toHaveCount(0);

    const tools = card.getByRole("listitem", { name: "Config map walk-tools" });
    await expect(tools).toContainText("/home/debian/.config-map/walk-tools");
    await expect(tools).toContainText("run.sh");
    const deploy = card.getByRole("listitem", {
      name: "Secret map walk-deploy",
    });
    await expect(deploy).toContainText("token: set");
    await expect(page.locator("body")).not.toContainText("walk-secret-value");

    // Editing keeps the stored value: the box is blank and says so.
    await card.getByRole("button", { name: "Edit maps" }).click();
    await expect(
      editor
        .getByRole("group", { name: "Secret map walk-deploy file token" })
        .getByLabel("Secret value"),
    ).toHaveValue("");
    await editor
      .getByRole("button", {
        name: "Remove Config map walk-tools",
        exact: true,
      })
      .click();
    await editor.getByRole("button", { name: "Save maps" }).click();
    // Dropping a saved map asks first; Cancel keeps the editor open.
    await expect(page.getByRole("dialog")).toContainText(
      "config map walk-tools",
    );
    await confirmDialog(page, "Cancel");
    await expect(editor).toBeVisible();
    await editor.getByRole("button", { name: "Save maps" }).click();
    await confirmDialog(page, "Save and remove");
    await expect(tools).toHaveCount(0);
    await expect(deploy).toContainText("token: set");
    await noSidewaysScroll(page);
  } finally {
    await restore(request, CONFIG_MAP, before, secretsBefore);
  }
});

test("Host scripts: Test runs the saved script with each argument as one @phone", async ({
  page,
}) => {
  await page.goto(pagePath(HOST_SCRIPT));
  const card = page.getByTestId("card-host-scripts");
  await expect(card).toBeVisible();
  await expect(page.getByTestId("configure-btn")).toHaveCount(0);
  const ops = card.getByRole("listitem", { name: "Script ops" });
  await expect(ops).toContainText("Demo operations script");
  await expect(ops.getByRole("list", { name: "Limits of ops" })).toContainText(
    "timeout 60s",
  );

  await ops.getByRole("button", { name: "Test ops" }).click();
  const form = ops.getByRole("form", { name: "Test ops" });
  await form.getByRole("button", { name: "Add argument" }).click();
  await form.getByRole("textbox", { name: "Argument 1" }).fill("a b");
  await form.getByRole("button", { name: "Add argument" }).click();
  await form.getByRole("textbox", { name: "Argument 2" }).fill("$(id)");
  const post = page.waitForRequest((r) =>
    r.url().endsWith("/api/capability-servers/test-script"),
  );
  await form.getByRole("button", { name: "Run ops" }).click();
  expect((await post).postDataJSON()).toEqual({
    id: HOST_SCRIPT,
    script: "ops",
    argv: ["a b", "$(id)"],
  });
  const result = form.getByTestId("host-script-result");
  await expect(result).toContainText("exit 0");
  // No shell read the arguments: $(id) came back as typed.
  await expect(result.getByLabel("stdout")).toHaveText("a b $(id)");

  // The Run button and the output stay inside the card on a phone.
  const cardBox = (await card.boundingBox())!;
  for (const el of [form.getByRole("button", { name: "Run ops" }), result]) {
    const box = (await el.boundingBox())!;
    expect(box.x).toBeGreaterThanOrEqual(cardBox.x);
    expect(box.x + box.width).toBeLessThanOrEqual(
      cardBox.x + cardBox.width + 0.5,
    );
  }
  await noSidewaysScroll(page);
});

test("Host scripts: add, edit and remove a script, refusing bad fields by name @dark", async ({
  page,
  request,
  axe,
}) => {
  const before = await serverConfig(request, HOST_SCRIPT);
  const secretsBefore = await secretNames(request);
  try {
    await page.goto(pagePath(HOST_SCRIPT));
    const card = page.getByTestId("card-host-scripts");
    await card.getByRole("button", { name: "Add script" }).click();
    const form = card.getByRole("form", { name: "New script" });
    await form.getByLabel("Name", { exact: true }).fill("ops");
    await expect(
      form.getByText("A script named ops is already configured."),
    ).toBeVisible();
    await form.getByLabel("Name", { exact: true }).fill("walk-greet");
    await form.getByLabel("Description").fill("Greets its first argument");
    await form.getByLabel("Script (bash)").fill('echo "hello $1"\n');
    await form.getByLabel("Timeout (seconds)").fill("0");
    await expect(
      form.getByText("Whole seconds from 1 to 86400."),
    ).toBeVisible();
    const save = form.getByRole("button", { name: "Save script" });
    await expect(save).toBeDisabled();
    await form.getByLabel("Timeout (seconds)").fill("20");
    await form.getByLabel("Max calls per grant").fill("4");
    await form.getByRole("switch", { name: "Approve each call" }).click();
    expect(await axe()).toEqual([]);
    await save.click();

    const greet = card.getByRole("listitem", { name: "Script walk-greet" });
    await expect(greet).toContainText("Greets its first argument");
    const limits = greet.getByRole("list", { name: "Limits of walk-greet" });
    await expect(limits).toContainText("timeout 20s");
    await expect(limits).toContainText("4 calls");
    await expect(limits).toContainText("approval per call");
    const stored = await serverConfig(request, HOST_SCRIPT);
    const scripts = (stored["config"] as { scripts: { name: string }[] })
      .scripts;
    expect(scripts.map((s) => s.name)).toEqual(["ops", "walk-greet"]);

    // Edit opens the saved values; a cleared limit goes back to the
    // server's default.
    await greet.getByRole("button", { name: "Edit walk-greet" }).click();
    const edit = card.getByRole("form", { name: "Edit walk-greet" });
    await expect(edit.getByLabel("Script (bash)")).toHaveValue(
      'echo "hello $1"\n',
    );
    await edit.getByLabel("Max calls per grant").fill("");
    await edit.getByLabel("Usage", { exact: true }).fill("walk-greet <name>");
    await edit.getByRole("button", { name: "Save script" }).click();
    await expect(greet.getByLabel("Usage", { exact: true })).toHaveText(
      "walk-greet <name>",
    );
    await expect(limits).not.toContainText("calls");

    // Test the edited script, which the server now runs.
    await greet.getByRole("button", { name: "Test walk-greet" }).click();
    const test = greet.getByRole("form", { name: "Test walk-greet" });
    await test.getByRole("button", { name: "Add argument" }).click();
    await test.getByRole("textbox", { name: "Argument 1" }).fill("world");
    await test.getByRole("button", { name: "Run walk-greet" }).click();
    await expect(test.getByLabel("stdout")).toHaveText("hello world");

    await greet.getByRole("button", { name: "Remove walk-greet" }).click();
    await expect(page.getByRole("dialog")).toContainText(
      "grain host-script walk-greet",
    );
    await confirmDialog(page, "Remove");
    await expect(greet).toHaveCount(0);
    await expect(
      card.getByRole("listitem", { name: "Script ops" }),
    ).toBeVisible();
  } finally {
    await restore(request, HOST_SCRIPT, before, secretsBefore);
  }
});
