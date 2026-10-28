import { spawn } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import http from "node:http";
import { tmpdir } from "node:os";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { signIn, writeAccessCode } from "./accesscode.js";

// §5 of docs/test-plans/2026-10-06.md: every model a connection's
// provider lists is advertised (645822463), every Model picker searches
// them (ef877ca0b, 2781a4e9a), and templates that share a name are told
// apart in the template pickers (a620f1e51). The report is
// docs/test-runs/2026-10-07-discovered-models-pickers.md.
//
// Its own demo, started with -model-catalog pointed at the stub below --
// Anthropic's GET /v1/models, the one boundary stubbed -- so a Claude
// connection saved with no models advertises what the stub lists, live,
// the way a deployment's does (grain/task-4320).
test.setTimeout(240_000);

const MODELS = [
  "claude-opus-5-5",
  "claude-sonnet-5-5",
  "claude-haiku-4-5",
  "claude-fable-5-1",
  "claude-opus-5",
  "claude-sonnet-5",
  "claude-opus-4-1",
  "claude-sonnet-4-5",
  "claude-3-7-sonnet",
  "claude-3-5-haiku",
  "claude-opus-4",
  "claude-sonnet-4",
];
// "co41" is only claude-opus-4-1 in that list: c, o, 4, 1 in order.
const FRAGMENT = "co41";
const PICKED = "claude-opus-4-1";

async function startCatalogStub() {
  const state = { down: false, calls: 0 };
  const server = http.createServer((req, res) => {
    state.calls++;
    if (!req.url.startsWith("/v1/models")) {
      res.writeHead(404).end();
      return;
    }
    if (state.down) {
      res.writeHead(503).end("upstream unavailable");
      return;
    }
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(
      JSON.stringify({
        data: MODELS.map((id) => ({ id, display_name: id, type: "model" })),
        has_more: false,
      }),
    );
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  return {
    state,
    hostPort: `127.0.0.1:${server.address().port}`,
    close: () => new Promise((resolve) => server.close(resolve)),
  };
}

async function startDemo(catalogHostPort) {
  const dir = mkdtempSync(path.join(tmpdir(), "grain-models-"));
  const codeFile = path.join(dir, "access-code");
  await writeAccessCode(codeFile);
  const proc = spawn(
    process.env.GRAIN_E2E_DEMO_BIN,
    [
      "demo",
      "-addr",
      "127.0.0.1:0",
      "-open=false",
      "-model-catalog",
      catalogHostPort,
      // The deployment's ten-minute retry, shortened so a walk can see it.
      "-model-discovery-interval",
      "1s",
      "-access-code-file",
      codeFile,
    ],
    { stdio: ["ignore", "pipe", "pipe"], env: { ...process.env, TMPDIR: dir } },
  );
  let output = "";
  const url = await new Promise((resolve, reject) => {
    const timer = setTimeout(
      () => reject(new Error(`grain demo did not start:\n${output}`)),
      60_000,
    );
    const read = (chunk) => {
      output += chunk;
      const at = output.match(/at http:\/\/127\.0\.0\.1:(\d+)/);
      if (at) {
        clearTimeout(timer);
        resolve(`http://127.0.0.1:${at[1]}`);
      }
    };
    proc.stdout.on("data", read);
    proc.stderr.on("data", read);
    proc.on("exit", (code) => {
      clearTimeout(timer);
      reject(new Error(`grain demo exited ${code}:\n${output}`));
    });
  });
  return {
    url,
    stop: () => {
      proc.kill();
      rmSync(dir, { recursive: true, force: true });
    },
  };
}

// pickByTyping chooses a model in one ModelSelect with the keyboard
// only: a fragment, the best match highlighted, Enter. Then it empties
// the text and leaves the field, which must not lose the choice -- the
// picker chooses only among advertised models, and an empty box is not
// one.
async function pickByTyping(page, input) {
  await input.click();
  await input.fill(FRAGMENT);
  const listbox = page.getByRole("listbox");
  await expect(listbox.getByRole("option")).toHaveCount(1);
  await expect(listbox.getByRole("option").first()).toHaveText(PICKED);
  await input.press("Enter");
  await expect(input).toHaveValue(PICKED);
  await input.fill("");
  await input.press("Tab");
  await expect(input).toHaveValue(PICKED);
}

test.describe("discovered models and searchable pickers (§5)", () => {
  // One demo for the whole walk, which later steps build on: a failure
  // would otherwise restart the worker onto a fresh demo with none of
  // the connections the first step saved.
  test.describe.configure({ mode: "serial" });
  let stub;
  let demo;
  let api;
  let page;
  const conn = "claude-discovered";

  test.beforeAll(async ({ browser, playwright }) => {
    stub = await startCatalogStub();
    demo = await startDemo(stub.hostPort);
    // The demo keeps no accounts: the deployment's owner is whoever shows
    // its access code (accesscode.js).
    const session = await signIn(demo.url);
    api = await playwright.request.newContext({
      baseURL: demo.url,
      storageState: session,
    });
    const ctx = await browser.newContext({
      baseURL: demo.url,
      storageState: session,
    });
    page = await ctx.newPage();
    page.on("dialog", (d) => d.accept());
  });

  test.afterAll(async () => {
    await page?.context().close();
    await api?.dispose();
    demo?.stop();
    await stub?.close();
  });

  const listed = async (kind, id) =>
    ((await (await api.get(`/api/${kind}`)).json()) || []).find(
      (x) => x.id === id,
    );

  // What a connection advertises: /api/config's connection options carry
  // model.AgentBackend.SupportedModels, the list dispatch reads.
  const advertised = async (id) => {
    const config = await (await api.get("/api/config")).json();
    return (config.agentBackends || []).find((b) => b.id === id)?.models;
  };
  // What is stored for it: /api/settings carries the record itself.
  const stored = async (id) => {
    const settings = await (await api.get("/api/settings")).json();
    return (settings.agentBackends || []).find((b) => b.id === id);
  };

  test("a connection saved with no models advertises all twelve", async () => {
    // Through Team settings, as an admin adds a provider: Claude, an
    // OAuth token, and no advertised models typed in.
    await page.goto("/admin");
    await page.getByTestId("settings-nav-models").click();
    await page.getByRole("button", { name: "New connection" }).click();
    await page
      .locator(".overlay-pane")
      .getByLabel(/Agent type/i)
      .click();
    await page.getByRole("option", { name: "Claude" }).click();
    await page.getByRole("button", { name: "Continue" }).click();
    await page.getByLabel("Authentication method").click();
    await page.getByRole("option", { name: "Claude Code OAuth token" }).click();
    const nameField = page
      .locator(".overlay-pane")
      .getByLabel("Name", { exact: true });
    await nameField.fill("Claude discovered");
    await nameField.blur();
    const saved = page.waitForResponse(
      (r) =>
        /\/api\/deployment\/agent-backends\//.test(r.url()) &&
        r.request().method() === "PUT",
    );
    await page.getByRole("button", { name: "Save connection" }).click();
    const savedBody = await (await saved).json();
    expect(savedBody.models || []).toEqual([]);

    // Discovery runs on the save, not at the next tick.
    await expect.poll(() => advertised(savedBody.id)).toEqual(MODELS);
    // Kept under a fixed id for the steps below.
    const res = await api.put(`/api/deployment/agent-backends/${conn}`, {
      data: { name: "Claude discovered", framework: "claude" },
    });
    expect(res.status(), await res.text()).toBe(200);
    await expect.poll(() => advertised(conn)).toEqual(MODELS);
    // Live, not saved (grain/task-4320): nothing was written into the
    // connection's own models; the provider's list is kept beside them.
    const record = await stored(conn);
    expect(record.models || []).toEqual([]);
    expect(record.discovered.models).toEqual(MODELS);

    // The Models page lists every one of them.
    await page.goto("/admin");
    await page.getByTestId("settings-nav-models").click();
    for (const m of MODELS) {
      await expect(
        page.getByText(m, { exact: true }).first(),
        `${m} on the Models page`,
      ).toBeVisible();
    }
    // And the Default model picker offers them all, with the demo's two
    // Gemini models and "None".
    await page.getByLabel("Default model").click();
    await expect(page.getByRole("listbox").getByRole("option")).toHaveCount(
      MODELS.length + 3,
    );
    await page.keyboard.press("Escape");

    // And so does the New task form's Model picker.
    await page.goto("/");
    await page.getByRole("button", { name: "+ New task" }).click();
    await page.getByRole("combobox", { name: "Model", exact: true }).click();
    const options = page.getByRole("listbox").getByRole("option");
    for (const m of MODELS) {
      await expect(
        options.filter({ hasText: new RegExp(`^${m}$`) }),
        `${m} in the New task picker`,
      ).toHaveCount(1);
    }
    await page.keyboard.press("Escape");
  });

  test("a model the provider adds or drops follows without a save", async () => {
    MODELS.push("claude-opus-6");
    await expect
      .poll(() => advertised(conn), { timeout: 15_000 })
      .toContain("claude-opus-6");
    MODELS.pop();
    await expect
      .poll(() => advertised(conn), { timeout: 15_000 })
      .toEqual(MODELS);
    expect((await stored(conn)).models || []).toEqual([]);
  });

  test("an unreachable provider is retried until it answers", async () => {
    stub.state.down = true;
    const res = await api.put("/api/deployment/agent-backends/claude-late", {
      data: { name: "Claude late", framework: "claude" },
    });
    expect(res.status(), await res.text()).toBe(200);
    const before = stub.state.calls;
    // Several 1s retries come and go while it is down; nothing is filled.
    await expect
      .poll(() => stub.state.calls, { timeout: 10_000 })
      .toBeGreaterThan(before + 2);
    // Until then it advertises nothing -- grain does not guess a model
    // name (task 4527) -- while a connection that has heard from the
    // provider keeps its last list.
    expect((await advertised("claude-late")) || []).toEqual([]);
    expect(await advertised(conn)).toEqual(MODELS);
    await expect
      .poll(async () => (await stored(conn)).discovered?.error || "")
      .not.toBe("");
    stub.state.down = false;
    await expect
      .poll(() => advertised("claude-late"), { timeout: 10_000 })
      .toEqual(MODELS);
  });

  test("New task: type, pick with the keyboard, clear, save", async () => {
    await page.goto("/");
    await page.getByRole("button", { name: "+ New task" }).click();
    await expect(page.getByRole("heading", { name: "New task" })).toBeVisible();
    await page.getByLabel("Title").fill(`Picker new task ${Date.now()}`);
    await page.getByLabel(/^Repos/).selectOption("acme/widgets");
    await pickByTyping(
      page,
      page.getByRole("combobox", { name: "Model", exact: true }),
    );
    const created = page.waitForResponse(
      (r) => r.url().endsWith("/api/tasks") && r.request().method() === "POST",
    );
    await page.getByRole("button", { name: "Create task" }).click();
    const body = await (await created).json();
    expect(body.model).toBe(PICKED);
    const stored = await (await api.get(`/api/tasks/${body.id}`)).json();
    expect(stored.model).toBe(PICKED);
  });

  test("Edit: type, pick, save; then back to the deployment default", async () => {
    const res = await api.post("/api/tasks", {
      data: { title: `Picker edit ${Date.now()}`, repos: ["acme/widgets"] },
    });
    expect(res.status(), await res.text()).toBe(201);
    const task = await res.json();
    await page.goto(`/tasks/${task.id}`);
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    const input = page.getByRole("combobox", { name: "Model", exact: true });
    await pickByTyping(page, input);
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect
      .poll(
        async () =>
          (await (await api.get(`/api/tasks/${task.id}`)).json()).model,
      )
      .toBe(PICKED);

    // "" is an option of its own, reached by typing too.
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    await input.click();
    await input.fill("deployment default");
    await input.press("Enter");
    await expect(input).toHaveValue(/^Deployment default/);
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await expect
      .poll(
        async () =>
          (await (await api.get(`/api/tasks/${task.id}`)).json()).model || "",
      )
      .toBe("");
  });

  test("Bulk edit: type, pick, apply to two tasks", async () => {
    const stamp = Date.now();
    const ids = [];
    for (const n of [1, 2]) {
      const res = await api.post("/api/tasks", {
        data: { title: `Picker bulk ${n} ${stamp}`, repos: ["acme/widgets"] },
      });
      expect(res.status(), await res.text()).toBe(201);
      ids.push((await res.json()).id);
    }
    await page.goto("/");
    for (const n of [1, 2]) {
      await page
        .locator(".task-row", { hasText: `Picker bulk ${n} ${stamp}` })
        .getByRole("checkbox")
        .check();
    }
    await page.getByRole("button", { name: "Bulk edit…", exact: true }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("checkbox", { name: /^Model/ }).check();
    await pickByTyping(
      page,
      dialog.getByRole("combobox", { name: "Model", exact: true }),
    );
    await dialog.getByRole("button", { name: "Apply to 2 tasks" }).click();
    await expect(dialog).toHaveCount(0);
    for (const id of ids) {
      await expect
        .poll(
          async () => (await (await api.get(`/api/tasks/${id}`)).json()).model,
        )
        .toBe(PICKED);
    }
  });

  test("Templates and schedules: type, pick, save", async () => {
    const tpl = await api.post("/api/templates", {
      data: {
        name: `picker-${Date.now()}`,
        title: "Template for the model picker",
        repo: "acme/widgets",
      },
    });
    expect(tpl.status(), await tpl.text()).toBe(201);
    const template = await tpl.json();
    await page.goto(`/templates/${template.id}`);
    const tplDialog = page.getByRole("dialog");
    await pickByTyping(
      page,
      tplDialog.getByRole("combobox", { name: "Model", exact: true }),
    );
    await tplDialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect
      .poll(async () => (await listed("templates", template.id))?.model)
      .toBe(PICKED);

    const sch = await api.post("/api/schedules", {
      data: {
        title: "Schedule for the model picker",
        repo: "acme/widgets",
        recurrence: { kind: "everyNHours", everyNHours: 24 },
      },
    });
    expect(sch.status(), await sch.text()).toBe(201);
    const schedule = await sch.json();
    await page.goto(`/schedules/${schedule.id}`);
    const schDialog = page.getByRole("dialog");
    await pickByTyping(
      page,
      schDialog.getByRole("combobox", { name: "Model", exact: true }),
    );
    await schDialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect
      .poll(async () => (await listed("schedules", schedule.id))?.model)
      .toBe(PICKED);
  });

  test("Team Default model: type, pick, saved on choosing", async () => {
    await page.goto("/admin");
    await page.getByTestId("settings-nav-models").click();
    await pickByTyping(
      page,
      page.getByRole("combobox", { name: "Default model" }),
    );
    await expect
      .poll(
        async () =>
          (await (await api.get("/api/deployment")).json()).defaultModel,
      )
      .toBe(PICKED);
  });

  test("a task whose model is no longer advertised shows it, marked", async () => {
    const res = await api.post("/api/tasks", {
      data: {
        title: `Picker retired ${Date.now()}`,
        repos: ["acme/widgets"],
        model: "claude-opus-4",
      },
    });
    expect(res.status(), await res.text()).toBe(201);
    const task = await res.json();
    // The operator curates claude-opus-4 out of every connection that
    // served it.
    for (const id of [conn, "claude-late"]) {
      const put = await api.put(`/api/deployment/agent-backends/${id}`, {
        data: {
          name: id,
          framework: "claude",
          models: MODELS.filter((m) => m !== "claude-opus-4"),
        },
      });
      expect(put.status(), await put.text()).toBe(200);
    }
    const others = (await (await api.get("/api/config")).json()).agentBackends
      .filter((b) => (b.models || []).includes("claude-opus-4"))
      .map((b) => b.id);
    for (const id of others) {
      const put = await api.put(`/api/deployment/agent-backends/${id}`, {
        data: {
          name: id,
          framework: "claude",
          models: MODELS.filter((m) => m !== "claude-opus-4"),
        },
      });
      expect(put.status(), await put.text()).toBe(200);
    }
    await page.goto(`/tasks/${task.id}`);
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    const input = page.getByRole("combobox", { name: "Model", exact: true });
    await expect(input).toHaveValue(
      "claude-opus-4 (no connection advertises it)",
    );
    // Its hidden input still carries the model itself, as a select's did.
    await expect(
      page.locator('input[type="hidden"][value="claude-opus-4"]'),
    ).toHaveCount(1);
    await page.keyboard.press("Escape");
  });

  test("templates named alike are labelled in all four pickers", async () => {
    const made = [];
    for (const title of ["Nightly build", "Nightly docs"]) {
      const res = await api.post("/api/templates", {
        data: { name: "nightly", title, repo: "acme/widgets" },
      });
      expect(res.status(), await res.text()).toBe(201);
      made.push(await res.json());
    }
    const labels = made.map((t) => `nightly — ${t.title} (${t.id})`);
    const expectBoth = async () => {
      const listbox = page.getByRole("listbox");
      for (const label of labels) {
        await expect(listbox.getByText(label, { exact: true })).toBeVisible();
      }
      await expect(listbox.getByText("nightly", { exact: true })).toHaveCount(
        0,
      );
      await page.keyboard.press("Escape");
    };

    // The schedule editor.
    await page.goto("/schedules");
    await page.getByRole("button", { name: /New schedule/ }).click();
    await page
      .getByRole("dialog")
      .getByRole("combobox", { name: "Template" })
      .click();
    await expectBoth();
    await page.keyboard.press("Escape");

    // The suite editor.
    await page.goto("/suites");
    await page.getByRole("button", { name: "+ New suite" }).click();
    await page
      .getByRole("dialog")
      .getByRole("combobox", { name: "Templates" })
      .click();
    const listbox = page.getByRole("listbox");
    for (const label of labels) {
      await expect(listbox.getByText(label, { exact: true })).toBeVisible();
    }
    await page.keyboard.press("Escape");
    await page.keyboard.press("Escape");

    // Use template.
    await page.goto("/templates");
    await page
      .locator("li, tr, .MuiCard-root, div", { hasText: made[0].title })
      .getByRole("button", { name: "Use…" })
      .first()
      .click();
    await page.getByRole("combobox", { name: "Template" }).click();
    await expectBoth();
    await page.keyboard.press("Escape");

    // The New task form's Review picker.
    await page.goto("/");
    await page.getByRole("button", { name: "+ New task" }).click();
    await page.getByText("Advanced options").click();
    await page.getByRole("combobox", { name: "Review" }).click();
    await expectBoth();
  });
});
