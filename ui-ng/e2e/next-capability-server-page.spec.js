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

// A capability server's own page on /next/
// (/settings/capabilities/<encoded id>, the classic serverPath): opened
// from the servers table, (i) popovers that open on click and keys and
// close on Escape, the schema-driven server settings form (the demo
// cloud server's project, region and write-only apiToken; gcp's keyless
// and key permission-set modes; figma's accounts), connection edits with
// host placement and rename, server-owned secrets, the holders of its
// grants and the Remove confirm that lists them. Runs over `grain demo
// -secrets-store`, so secrets really are stored; every test puts back
// what it changed.

test.use({ demoArgs: ["-secrets-store"] });

const CLOUD = "https://capabilities.demo.grain.example/cloud";
const GCP = "https://grain.dev/capabilities/gcp";
const FIGMA = "https://grain.dev/capabilities/figma";
const GITHUB = "https://grain.dev/capabilities/github";

const pagePath = (id) =>
  `/next/settings/capabilities/${encodeURIComponent(id)}`;

// onPageOf waits for the address to be id's page: the router leaves a
// ":" unescaped, so the path is compared decoded.
async function onPageOf(page, id) {
  await expect
    .poll(() => decodeURIComponent(new URL(page.url()).pathname))
    .toBe(`/next/settings/capabilities/${id}`);
}

async function servers(request) {
  const r = await request.get("/api/capability-servers");
  expect(r.ok()).toBeTruthy();
  return r.json();
}

async function serverEntry(request, id) {
  return (await servers(request)).find((s) => s.id === id);
}

// shoot saves a look at the page for the review (not an assertion):
// SHOTS=<dir> names where.
async function shoot(page, name) {
  const dir = process.env.SHOTS;
  if (!dir) return;
  await page.screenshot({
    path: `${dir}/${name}-${test.info().project.name}.png`,
    fullPage: true,
  });
}

test("a server's page opens from the table, with popovers that keys open and Escape closes @dark @phone", async ({
  page,
}) => {
  await page.goto("/next/settings/capabilities");
  await page.getByTestId(`open-cap-server-${CLOUD}`).click();
  await onPageOf(page, CLOUD);
  const card = page.getByTestId("capability-server-page");
  await expect(card).toHaveAttribute("data-capability-server", CLOUD);
  await expect(page.getByTestId("capability-server-id")).toHaveText(CLOUD);
  await expect(page.getByTestId("capability-server-state")).toHaveText(
    "enabled",
  );
  await expect(page.getByTestId("capability-server-methods")).toContainText(
    "cloud-key",
  );

  // (i) opens on click, and closes on Escape with focus back on it.
  const info = page.getByTestId("info-capability-id");
  const pop = page.getByTestId("info-capability-id-pop");
  await expect(pop).toHaveCount(0);
  await info.click();
  await expect(pop).toBeVisible();
  await expect(info).toHaveAttribute("aria-expanded", "true");
  await page.keyboard.press("Escape");
  await expect(pop).toHaveCount(0);
  await expect(info).toBeFocused();
  // Enter and Space open it from the keyboard too; a click elsewhere
  // closes it.
  await page.keyboard.press("Enter");
  await expect(pop).toBeVisible();
  await page.keyboard.press("Space");
  await expect(pop).toHaveCount(0);
  await page.keyboard.press("Space");
  await expect(pop).toBeVisible();
  // The popover stays inside the viewport, phone included.
  const box = await pop.boundingBox();
  const vw = page.viewportSize().width;
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(vw);
  await page.getByTestId("capability-server-name").click();
  await expect(pop).toHaveCount(0);

  // Nothing on the page scrolls sideways.
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth + 1,
  );
  expect(overflow).toBeFalsy();
  await shoot(page, "cloud");

  // Back goes to the list, at the servers card.
  await page.getByTestId("capability-server-back").click();
  await expect(page).toHaveURL(
    /\/next\/settings\/capabilities#capability-servers$/,
  );
  await expect(page.getByTestId("card-capability-servers")).toBeVisible();
});

test("the schema form configures a server: field refusals, write-only secrets and their own stored secret", async ({
  page,
  request,
}) => {
  const before = await serverEntry(request, CLOUD);
  try {
    await page.goto(pagePath(CLOUD));
    await page.getByTestId("configure-btn").click();
    const form = page.getByTestId("capability-server-config");
    await expect(form).toBeVisible();
    // apiToken is write-only: a password box with a "not set" chip.
    const token = form.getByTestId("cfg-apiToken");
    await expect(token).toHaveAttribute("type", "password");
    await expect(form.getByTestId("secret-chip-apiToken")).toHaveText(
      "not set",
    );

    // A value the server's schema refuses is shown on its field.
    await form.getByTestId("cfg-project").fill("BAD");
    await page.getByTestId("save-config-btn").click();
    await expect(form.getByTestId("cfg-error-project")).toContainText(
      "does not match",
    );

    await form.getByTestId("cfg-project").fill("walk-project");
    await form.getByTestId("cfg-region").click();
    await page.getByRole("option", { name: "eu" }).click();
    await token.fill("walk-token-value");
    // Test tries the values, typed secret included, without saving.
    await page.getByTestId("test-config-btn").click();
    await expect(form.getByTestId("capability-test-result")).toContainText(
      "The server answered",
    );
    const put = page.waitForRequest(
      (r) =>
        r.url().endsWith("/api/capability-servers") && r.method() === "PUT",
    );
    await page.getByTestId("save-config-btn").click();
    const body = (await put).postDataJSON();
    expect(body.config.config).toMatchObject({
      project: "walk-project",
      region: "eu",
    });
    // The typed value goes as configSecrets, never into the entry.
    expect(body.configSecrets).toEqual({ apiToken: "walk-token-value" });
    expect(JSON.stringify(body.config)).not.toContain("walk-token-value");
    await expect(form).toHaveCount(0);
    await expect(page.locator("body")).not.toContainText("walk-token-value");

    // The server's own secret is listed on its page, set, with Replace.
    const secrets = page.getByTestId("capability-server-secrets");
    await expect(secrets).toContainText("$secret:capability-server-");
    await expect(secrets).toContainText("set");
    const replace = secrets.locator('[data-testid^="replace-owned-"]').first();
    await replace.click();
    await page.getByTestId("owned-secret-value").fill("walk-token-2");
    const putSecret = page.waitForResponse(
      (r) =>
        /\/api\/secrets\/capability-server-/.test(r.url()) &&
        r.request().method() === "PUT",
    );
    await page.getByTestId("owned-secret-save").click();
    expect((await putSecret).ok()).toBeTruthy();

    // Settings → Capabilities' stored secrets link back to this page.
    await page.getByTestId("capability-server-back").click();
    const link = page
      .getByTestId("stored-secrets")
      .getByRole("link", { name: CLOUD });
    await expect(link).toBeVisible();
    await link.click();
    await onPageOf(page, CLOUD);
    await page.getByTestId("configure-btn").click();
    await expect(page.getByTestId("secret-chip-apiToken")).toHaveText("set");
    await shoot(page, "cloud-configure");
  } finally {
    await request.put("/api/capability-servers", {
      data: { id: CLOUD, config: before.config },
    });
  }
});

test("gcp's permission sets say keyless or key mode, and key-only fields follow the choice @dark @phone", async ({
  page,
}) => {
  await page.goto(pagePath(GCP));
  await expect(page.getByTestId("gcp-modes")).toContainText("keyless");
  await page.getByTestId("configure-btn").click();
  const form = page.getByTestId("capability-server-config");
  const sets = form.getByTestId("cfg-permission_sets");
  await expect(sets).toBeVisible();
  // The demo's sets store no credential: neither is shown as token.
  await expect(form.getByTestId("gcp-mode-0")).toContainText(
    "No credential mode stored",
  );
  // A new set starts keyless, with no key pool to size.
  const n = await sets.locator("fieldset.cap-item").count();
  await form.getByTestId("add-permission_sets").click();
  // The new set is the last one: wait for it to render before reading it.
  await expect(sets.locator("fieldset.cap-item")).toHaveCount(n + 1);
  const item = form.getByTestId(`cfg-permission_sets-${n}`);
  await expect(form.getByTestId(`gcp-mode-${n}`)).toContainText("Keyless");
  await expect(
    item.getByTestId(`cfg-permission_sets.${n}.credential`),
  ).toContainText("token (recommended, no keys)");
  await expect(
    item.getByTestId(`cfg-permission_sets.${n}.pool_max`),
  ).toHaveCount(0);
  // Key mode draws pool_max and says what it does.
  await item.getByTestId(`cfg-permission_sets.${n}.credential`).click();
  await page
    .getByRole("option", { name: "key (service-account key pool)" })
    .click();
  await expect(form.getByTestId(`gcp-mode-${n}`)).toContainText("Key mode");
  await expect(
    item.getByTestId(`cfg-permission_sets.${n}.pool_max`),
  ).toBeVisible();
  // The admin credential is asked for only when it is configured.
  await expect(form.getByTestId("cfg-admin_credential")).toHaveCount(0);
  // Deprecated fields are never drawn.
  await expect(form.locator('[data-field="accounts"]')).toHaveCount(0);
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth + 1,
  );
  expect(overflow).toBeFalsy();
  await shoot(page, "gcp");
  // Cancel saves nothing.
  await form.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(form).toHaveCount(0);
});

test("figma's accounts offer Test and Connect Figma with the OAuth callback to register", async ({
  page,
  request,
}) => {
  const before = await serverEntry(request, FIGMA);
  await page.goto(pagePath(FIGMA));
  await expect(page.getByTestId("figma-no-accounts")).toBeVisible();
  try {
    const r = await request.put("/api/capability-servers", {
      data: {
        id: FIGMA,
        config: {
          ...before.config,
          config: {
            ...(before.config.config || {}),
            accounts: [
              { name: "design", auth_mode: "oauth", client_id: "walk-client" },
            ],
          },
        },
      },
    });
    expect(r.ok(), await r.text()).toBeTruthy();
    await page.reload();
    const row = page.locator('[data-figma-account="design"]');
    await expect(row).toBeVisible();
    await expect(page.getByTestId("figma-connect-design")).toBeVisible();
    await expect(page.getByTestId("figma-test-design")).toBeVisible();
    await expect(row.getByTestId("figma-oauth-help")).toContainText(
      "/settings/capabilities",
    );
    await shoot(page, "figma");
  } finally {
    await request.put("/api/capability-servers", {
      data: { id: FIGMA, config: before.config },
    });
  }
});

test("an in-process server lists who holds its grants, each linked to its page", async ({
  page,
}) => {
  await page.goto(pagePath(GITHUB));
  await expect(page.getByTestId("capability-server-name")).toHaveText(
    "github server",
  );
  // Nothing to edit on a server grain runs in its own process.
  await expect(page.getByTestId("edit-connection-btn")).toHaveCount(0);
  await expect(page.getByTestId("capability-server-manage")).toHaveCount(0);
  const usage = page.getByTestId("capability-usage");
  // Tasks keep their classic page until /next/ is the default.
  const task = usage.getByRole("link", { name: /^Task #\d+$/ }).first();
  await expect(task).toHaveAttribute("href", /^\/tasks\/\d+$/);
  // Defaults and schedules link to their /next/ pages.
  await expect(
    usage.getByRole("link", { name: "Deployment defaults" }),
  ).toHaveAttribute("href", "/next/settings/tasks");
  await shoot(page, "github");
});

test("connection edits place a server on a host, rename it after a confirm, and Remove lists what holds it", async ({
  page,
  request,
}) => {
  const bin = process.env.GRAIN_E2E_DEMO_BIN;
  expect(bin).toBeTruthy();
  const id = "https://walk.example/capabilities/page-walk";
  const renamed = "https://walk.example/capabilities/page-walk-2";
  let taskId = "";
  try {
    const put = await request.put("/api/capability-servers", {
      data: {
        id,
        config: { command: bin, args: ["capability", "config-map"] },
      },
    });
    expect(put.ok(), await put.text()).toBeTruthy();
    await expect
      .poll(async () => (await serverEntry(request, id))?.status?.state, {
        timeout: 15000,
      })
      .toBe("attached");
    const t = await request.post("/api/tasks", {
      data: {
        title: "Page walk holder",
        repo: "acme/widgets",
        description: "holds a grant",
        grants: [`${id}#config-map`],
      },
    });
    expect(t.ok(), await t.text()).toBeTruthy();
    taskId = String((await t.json()).id);

    await page.goto(pagePath(id));
    await expect(page.getByTestId("capability-usage")).toContainText(
      `Task #${taskId}`,
    );

    // Host placement: a named host is offered whatever the host agent
    // beside the daemon says, and the entry becomes run: hostd.
    await page.getByTestId("edit-connection-btn").click();
    await page.getByTestId("cs-edit-run").click();
    await page.getByRole("option", { name: /^host/ }).click();
    await page.getByTestId("cs-edit-hostd").click();
    await page.getByRole("option", { name: /build-runner/ }).click();
    const save = page.waitForRequest(
      (r) =>
        r.url().endsWith("/api/capability-servers") && r.method() === "PUT",
    );
    await page.getByTestId("save-connection-btn").click();
    expect((await save).postDataJSON().config).toMatchObject({
      command: bin,
      run: "hostd",
      hostd: "build-runner",
    });
    // grain demo routes no placed server to a host, so it refuses the
    // save; the refusal is shown on the form, which stays open.
    await expect(page.getByTestId("cs-edit-error")).toContainText(
      "does not route placed capability servers",
    );
    await expect(page.getByTestId("capability-server-runs")).toContainText(
      "in the daemon's container",
    );

    // Renaming asks first, listing the grants left naming the old ID.
    await page.getByTestId("cs-edit-run").click();
    await page.getByRole("option", { name: /^container/ }).click();
    await page.getByTestId("cs-edit-id").fill(renamed);
    await page.getByTestId("save-connection-btn").click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("Rename this capability server?");
    await expect(dialog.getByTestId("confirm-items")).toContainText(
      `Task #${taskId}`,
    );
    await dialog.getByRole("button", { name: "Rename" }).click();
    await onPageOf(page, renamed);
    await expect(page.getByTestId("capability-server-id")).toHaveText(renamed);

    // A task holding a grant under the new ID, for Remove to list.
    const t2 = await request.post("/api/tasks", {
      data: {
        title: "Page walk holder 2",
        repo: "acme/widgets",
        description: "holds a grant",
        grants: [`${renamed}#config-map`],
      },
    });
    expect(t2.ok(), await t2.text()).toBeTruthy();

    // Bind a secret stores it and points an env var of the entry at it.
    await page.getByTestId("bind-secret").locator("summary").click();
    await page.getByTestId("bind-name").fill("WALK_TOKEN");
    await page.getByTestId("bind-secret-name").fill("walk-page-token");
    await page.getByTestId("bind-value").fill("walk-bound-value");
    await page.getByTestId("bind-secret-btn").click();
    await expect(
      page.getByTestId("server-secret-walk-page-token"),
    ).toContainText("set");
    await expect(page.locator("body")).not.toContainText("walk-bound-value");

    const usedBy = async (name) => {
      const j = await (await request.get("/api/secrets")).json();
      const sec = (Array.isArray(j) ? j : j.secrets || []).find(
        (x) => x.name === name,
      );
      return sec?.capabilityServers || [];
    };
    expect(await usedBy("walk-page-token")).toEqual([renamed]);

    // Remove asks first and names what holds it, linked to its page.
    await page.reload();
    await page.getByTestId("remove-server-btn").click();
    await expect(dialog).toContainText("Remove capability server?");
    const holder = dialog
      .getByTestId("confirm-items")
      .getByRole("link")
      .first();
    await expect(holder).toHaveAttribute("href", /^\/tasks\/\d+$/);
    await shoot(page, "remove-confirm");
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(dialog).toHaveCount(0);
    await page.getByTestId("remove-server-btn").click();
    await dialog.getByRole("button", { name: "Remove" }).click();
    await expect(page).toHaveURL(
      /\/next\/settings\/capabilities#capability-servers$/,
    );
    await expect(page.getByTestId(`cap-server-row-${renamed}`)).toHaveCount(0);
    // A secret bound under a name of the person's own choosing outlives
    // the server (grain removes only the ones it made for it), but the
    // list has read secrets again: it no longer says the removed server
    // uses it.
    const row = page.getByTestId("secret-row-walk-page-token");
    await expect(row).toBeVisible();
    await expect(row).not.toContainText(renamed);
    expect(await usedBy("walk-page-token")).toEqual([]);
  } finally {
    for (const s of [id, renamed]) {
      await request.post("/api/capability-servers/remove", { data: { id: s } });
    }
    await request.delete("/api/secrets/walk-page-token");
  }
});

test("a usage read that fails says so rather than that nothing grants the server", async ({
  page,
}) => {
  await page.route("**/api/capability-servers/usage**", (r) =>
    r.fulfill({ status: 500, body: "usage store down" }),
  );
  await page.goto(pagePath(CLOUD));
  await expect(page.getByTestId("capability-usage-unknown")).toContainText(
    "could not be read",
  );
  await expect(page.getByTestId("capability-usage-empty")).toHaveCount(0);
  // Remove's confirm says the same, from the page and from the table.
  await page.getByTestId("remove-server-btn").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("could not be read");
  await expect(dialog).not.toContainText("Nothing grants");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await page.goto("/next/settings/capabilities");
  await page.getByTestId(`remove-cap-server-${CLOUD}`).click();
  await expect(dialog).toContainText("could not be read");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toHaveCount(0);
});

test("array-item validation refusals render inline and service_account defaults person_granted on @phone", async ({
  page,
}) => {
  const HOST_SCRIPT = "https://grain.dev/capabilities/host-script";
  await page.goto(pagePath(HOST_SCRIPT));
  await page.getByTestId("configure-btn").click();
  const form = page.getByTestId("capability-server-config");
  const idx = await form
    .getByTestId("cfg-scripts")
    .locator("fieldset.cap-item")
    .count();
  await form.getByTestId("add-scripts").click();
  await form.getByTestId(`cfg-scripts.${idx}.name`).fill("walk-deploy");
  await form.getByTestId(`cfg-scripts.${idx}.script`).fill("/bin/echo");
  // Leave required scripts[idx].description blank: server returns bracket-keyed
  // fields["scripts[<idx>].description"], which must surface inline on the field.
  await form.getByTestId("save-config-btn").click();
  await expect(
    form.getByTestId(`cfg-error-scripts.${idx}.description`),
  ).toContainText("required");

  // On gcp, setting service_account defaults person_granted to checked, and
  // toggling it off sends explicit person_granted: false.
  await page.goto(pagePath(GCP));
  await page.getByTestId("configure-btn").click();
  const gcpForm = page.getByTestId("capability-server-config");
  const sets = gcpForm.getByTestId("cfg-permission_sets");
  const n = await sets.locator("fieldset.cap-item").count();
  await gcpForm.getByTestId("add-permission_sets").click();
  const item = gcpForm.getByTestId(`cfg-permission_sets-${n}`);
  const pg = item.getByTestId(`cfg-permission_sets.${n}.person_granted`);
  await expect(pg).toHaveAttribute("aria-checked", "false");
  await item
    .getByTestId(`cfg-permission_sets.${n}.service_account`)
    .fill("ci-bot@demo-project.iam.gserviceaccount.com");
  await expect(pg).toHaveAttribute("aria-checked", "true");
  await pg.click();
  await expect(pg).toHaveAttribute("aria-checked", "false");
  let probeBody = null;
  await page.route("**/api/capability-servers/test", async (route) => {
    probeBody = route.request().postDataJSON();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ ok: true, methods: [] }),
    });
  });
  await gcpForm.getByTestId("test-config-btn").click();
  await expect.poll(() => probeBody !== null).toBe(true);
  expect(probeBody.config.config.permission_sets[n].person_granted).toBe(false);
});

test("startError, waiting, and configRefused render on the table and server page without overflow @phone", async ({
  page,
}) => {
  const GIT = "https://grain.dev/capabilities/git";
  await page.route("**/api/capability-servers", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    const resp = await route.fetch();
    const list = await resp.json();
    const patched = list.map((s) => {
      if (s.id === GIT) {
        return {
          ...s,
          status: {
            ...(s.status || {}),
            state: "failed",
            startError: "could not start: port 4254 in use",
            error: "listen tcp 127.0.0.1:4254: bind: address already in use",
          },
        };
      }
      if (s.id === GCP) {
        return {
          ...s,
          status: {
            ...(s.status || {}),
            state: "needs-config",
            waiting: "ADC renewal",
            reason: "gcloud auth application-default login expired",
          },
        };
      }
      return s;
    });
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(patched),
    });
  });

  await page.goto("/next/settings/capabilities");
  await expect(page.getByTestId(`cap-server-state-${GIT}`)).toHaveText(
    "failed (could not start: port 4254 in use)",
  );
  await expect(page.getByTestId(`cap-server-state-${GCP}`)).toHaveText(
    "waiting for ADC renewal",
  );

  await page.goto(pagePath(GIT));
  await expect(page.getByTestId("capability-server-state")).toHaveText(
    "failed (could not start: port 4254 in use)",
  );
  await expect(page.getByTestId("capability-server-state-help")).toContainText(
    "retries every minute",
  );
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth > window.innerWidth + 1,
  );
  expect(overflow).toBeFalsy();

  await page.goto(pagePath(GCP));
  await expect(page.getByTestId("capability-server-state")).toHaveText(
    "waiting for ADC renewal",
  );
  await expect(page.getByTestId("capability-server-waiting")).toContainText(
    "Waiting for ADC renewal",
  );
});

test("a role without global capabilities:read sees unreadable instead of no such server", async ({
  page,
}) => {
  let forbiddenCalls = 0;
  await page.route("**/api/config", async (route) => {
    const resp = await route.fetch();
    const cfg = await resp.json();
    cfg.access = {
      role: "repo-operator",
      roles: ["repo-operator"],
      full: false,
      scopes: ["tasks:read", "capabilities:read"],
      repoLimits: { "capabilities:read": ["acme/widgets"] },
    };
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(cfg),
    });
  });
  await page.route("**/api/capability-servers**", async (route) => {
    forbiddenCalls++;
    await route.fulfill({ status: 403, body: "forbidden" });
  });

  await page.goto(pagePath(CLOUD));
  await expect(page.getByTestId("capability-server-unreadable")).toContainText(
    "Your role cannot list capability servers",
  );
  await expect(page.getByTestId("capability-server-missing")).toHaveCount(0);
  expect(forbiddenCalls).toBe(0);
});

test("Connect Figma completes across Cross-Origin-Opener-Policy via BroadcastChannel", async ({
  page,
  request,
}) => {
  const before = await serverEntry(request, FIGMA);
  try {
    await request.put("/api/capability-servers", {
      data: {
        id: FIGMA,
        config: {
          ...before.config,
          config: {
            ...(before.config.config || {}),
            accounts: [
              { name: "design", auth_mode: "oauth", client_id: "walk-client" },
            ],
          },
        },
      },
    });
    await page.route(
      "**/api/capability-servers/figma-oauth-authorize",
      async (route) => {
        const req = route.request().postDataJSON();
        // Route through localhost (different origin from 127.0.0.1) so
        // Cross-Origin-Opener-Policy: same-origin severs popup.closed in real
        // Chromium before redirecting back to redirectUri.
        const bounce = new URL(req.redirectUri);
        bounce.hostname =
          bounce.hostname === "127.0.0.1" ? "localhost" : "127.0.0.1";
        bounce.pathname = "/__test-figma-coop-bounce";
        bounce.searchParams.set("back", req.redirectUri);
        bounce.searchParams.set("state", "coop-e2e-state");
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            account: "design",
            authorizeUrl: bounce.toString(),
            state: "coop-e2e-state",
          }),
        });
      },
    );
    await page
      .context()
      .route("**/__test-figma-coop-bounce**", async (route) => {
        const u = new URL(route.request().url());
        const back = new URL(u.searchParams.get("back"));
        back.searchParams.set("code", "coop-e2e-code");
        back.searchParams.set("state", u.searchParams.get("state"));
        await route.fulfill({
          status: 302,
          headers: {
            Location: back.toString(),
            "Cross-Origin-Opener-Policy": "same-origin",
          },
          body: "",
        });
      });
    await page.route(
      "**/api/capability-servers/figma-oauth-exchange",
      async (route) => {
        const body = route.request().postDataJSON();
        expect(body.code).toBe("coop-e2e-code");
        expect(body.state).toBe("coop-e2e-state");
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ account: "design", userId: "u-99" }),
        });
      },
    );

    await page.goto(pagePath(FIGMA));
    await page.getByTestId("figma-connect-design").click();
    await expect(page.getByTestId("figma-account-result")).toContainText(
      "Connected: the refresh token for design (Figma user u-99)",
    );
  } finally {
    await request.put("/api/capability-servers", {
      data: { id: FIGMA, config: before.config },
    });
  }
});

test("Remove confirm scrolls a long linked holder list inside the dialog on a phone @phone", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route("**/api/capability-servers/usage**", async (route) => {
    const holders = Array.from({ length: 18 }, (_, i) => ({
      kind: i % 3 === 0 ? "task" : i % 3 === 1 ? "template" : "schedule",
      id: `${100 + i}`,
      name: `Holder item ${i + 1}`,
      repo: "acme/widgets",
      grant: `${CLOUD}#cloud-key`,
    }));
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify([{ id: CLOUD, holders }]),
    });
  });
  await page.goto(pagePath(CLOUD));
  await page.getByTestId("remove-server-btn").click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toBeVisible();
  const list = dialog.getByTestId("confirm-items");
  const metrics = await list.evaluate((el) => ({
    scrollHeight: el.scrollHeight,
    clientHeight: el.clientHeight,
  }));
  expect(metrics.scrollHeight).toBeGreaterThan(metrics.clientHeight);
  const dBox = await dialog.boundingBox();
  expect(dBox.y).toBeGreaterThanOrEqual(0);
  expect(dBox.y + dBox.height).toBeLessThanOrEqual(844);
  await dialog.getByRole("button", { name: "Cancel" }).click();
});
