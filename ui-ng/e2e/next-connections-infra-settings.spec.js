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

import { generateKeyPairSync } from "node:crypto";
import { test, expect } from "./fixtures.js";

// Regression coverage from the 2026-11-13 connections & infrastructure
// settings walkthrough (docs/test-runs/2026-11-13-connections-
// infrastructure-settings.md): Sandboxes, Engine, CI, Caches, Git,
// Capabilities and settings deep links on /next/. Every test puts back
// what it changed, since tests in one file share the worker's demo.

async function getSettings(request) {
  const res = await request.get("/api/settings");
  expect(res.ok()).toBeTruthy();
  return res.json();
}

async function saveBar(page) {
  const btn = page.getByTestId("settings-save-btn");
  await expect(btn).toBeEnabled();
  const done = page.waitForResponse(
    (r) => r.url().endsWith("/api/settings") && r.request().method() === "POST",
  );
  await btn.click();
  return done;
}

test("Sandboxes offers the server's backend and network vocabulary and saves it", async ({
  page,
  request,
}) => {
  const before = await getSettings(request);
  await page.goto("/next/settings/sandboxes");
  const backend = page.getByTestId("select-sandbox-backend");
  const network = page.getByTestId("select-sandbox-network");
  // The stored choice shows as itself, not as the list's first entry.
  await expect(backend).toContainText("Local Docker containers");
  await expect(network).toContainText("Open");
  await expect(page.getByTestId("sandbox-backend-desc")).toContainText(
    "local Docker container",
  );

  await backend.click();
  const opts = page
    .getByTestId("select-sandbox-backend-menu")
    .getByRole("option");
  const values = await page
    .getByTestId("select-sandbox-backend-menu")
    .getByRole("option")
    .evaluateAll((els) =>
      els.map((e) => e.dataset.testid.replace(/^.*-opt-/, "")),
    );
  expect(values).toEqual(before.sandboxBackendOptions.map((o) => o.backend));
  // A backend this daemon cannot run is listed, disabled, with the reason.
  const none = page.getByTestId("select-sandbox-backend-opt-none");
  await expect(none).toHaveAttribute("aria-disabled", "true");
  await expect(none).toContainText("None (control plane only)");
  await expect(none).toContainText("-disable-native-sandboxes");
  await none.click({ force: true });
  await expect(opts.first()).toBeVisible(); // menu stays open, nothing chosen
  await page.getByTestId("select-sandbox-backend-opt-kubernetes").click();

  await network.click();
  await expect(
    page.getByTestId("select-sandbox-network-menu").getByRole("option"),
  ).toHaveText(["Open (full network)", "None (isolated)"]);
  await page.getByTestId("select-sandbox-network-opt-none").click();

  await page.getByTestId("select-overcommit-mode").click();
  await expect(
    page.getByTestId("select-overcommit-mode-menu").getByRole("option"),
  ).toHaveText(["Fixed multiplier", "Pressure-governed auto"]);
  await page.keyboard.press("Escape");

  // Zero-means-default boxes show the default as a placeholder.
  const cpus = page.getByTestId("input-sandbox-cpus");
  await expect(cpus).toHaveValue("");
  await expect(cpus).toHaveAttribute(
    "placeholder",
    String(before.sandboxCpusDefault),
  );
  await cpus.fill("3");
  try {
    const res = await saveBar(page);
    expect(res.status()).toBe(200);
    await page.reload();
    await expect(backend).toContainText("Kubernetes — container pod per run");
    await expect(network).toContainText("None (isolated)");
    await expect(cpus).toHaveValue("3");
  } finally {
    await request.post("/api/settings", {
      data: {
        sandboxBackend: before.sandboxBackend,
        networkAccess: before.networkAccess,
        sandboxCpus: before.sandboxCpus,
      },
    });
  }
});

test("Engine, CI and Caches show grain's defaults and reset a cleared box", async ({
  page,
  request,
}) => {
  const before = await getSettings(request);
  await request.post("/api/settings", {
    data: { maxMergesPerRepo: 7, maxWorkers: 6, pollInterval: "15s" },
  });
  try {
    await page.goto("/next/settings/engine");
    const poll = page.getByTestId("input-poll-interval");
    const workers = page.getByTestId("input-max-workers");
    const merges = page.locator("#e-mpr");
    await expect(poll).toHaveAttribute(
      "placeholder",
      before.pollIntervalDefault,
    );
    await expect(workers).toHaveAttribute("placeholder", "10");
    await expect(merges).toHaveAttribute("placeholder", "5");
    await expect(merges).toHaveValue("7");
    await merges.fill("");
    await merges.press("Tab");
    await workers.fill("");
    await workers.press("Tab");
    await poll.fill("");
    const res = await saveBar(page);
    expect(res.status()).toBe(200);
    expect(JSON.parse(res.request().postData())).toMatchObject({
      maxMergesPerRepo: 5,
      maxWorkers: 10,
      pollInterval: "",
    });
    const after = await getSettings(request);
    expect(after.maxMergesPerRepo).toBe(5);
    expect(after.maxWorkers).toBe(10);
    expect(after.pollInterval).toBe(before.pollIntervalDefault);

    await page.goto("/next/settings/ci");
    await expect(page.locator("#ci-cache-mb")).toHaveAttribute(
      "placeholder",
      String(before.ciCacheMbDefault),
    );
    await expect(page.locator("#ci-reserve")).toHaveAttribute(
      "placeholder",
      before.ciReserveAfterDefault,
    );
    await page.goto("/next/settings/caches");
    const pkg = page.locator("#c-pkg-gb");
    await expect(pkg).toHaveAttribute(
      "placeholder",
      String(before.packageCacheSizeGbDefault),
    );
    await pkg.fill("35");
    expect((await saveBar(page)).status()).toBe(200);
    await page.reload();
    await expect(pkg).toHaveValue("35");
    await pkg.fill("");
    const cleared = await saveBar(page);
    expect(JSON.parse(cleared.request().postData())).toEqual({
      packageCacheSizeGb: 0,
    });
    await page.reload();
    await expect(pkg).toHaveValue("");
  } finally {
    await request.post("/api/settings", {
      data: {
        maxMergesPerRepo: before.maxMergesPerRepo,
        maxWorkers: before.maxWorkers,
        pollInterval: before.pollInterval,
        packageCacheSizeGb: before.packageCacheSizeGb,
      },
    });
  }
});

test("Git connections: host kind, GitHub App IDs as strings, Remove asks first", async ({
  page,
}) => {
  await page.goto("/next/settings/git");
  const host = page.getByTestId("input-git-conn-host");
  const rows = page.locator('table[aria-label="Git connections"] tbody tr');

  // A plain git host cannot take a GitHub App.
  await host.fill("git.walk.example.test");
  await page.getByTestId("select-git-conn-kind").click();
  await page.getByTestId("select-git-conn-kind-opt-git").click();
  await page.getByTestId("select-git-conn-type").click();
  await expect(
    page.getByTestId("select-git-conn-type-opt-app"),
  ).toHaveAttribute("aria-disabled", "true");
  await page.keyboard.press("Escape");

  // A GitHub (Enterprise) host can, and the IDs reach the server as the
  // strings AppCredentialInput takes.
  const { privateKey } = generateKeyPairSync("rsa", {
    modulusLength: 2048,
    privateKeyEncoding: { type: "pkcs1", format: "pem" },
    publicKeyEncoding: { type: "spki", format: "pem" },
  });
  await host.fill("ghe.walk.example.test");
  await page.getByTestId("select-git-conn-kind").click();
  await page.getByTestId("select-git-conn-kind-opt-github").click();
  await page.getByTestId("select-git-conn-type").click();
  await page.getByTestId("select-git-conn-type-opt-app").click();
  await page.getByTestId("input-git-app-id").fill("123");
  await page.getByTestId("input-git-app-install-id").fill("456");
  await page.getByTestId("input-git-app-private-key").fill(privateKey);
  const saved = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/deployment/git-connections") &&
      r.request().method() === "POST",
  );
  await page.getByTestId("save-git-conn-btn").click();
  const res = await saved;
  expect(JSON.parse(res.request().postData())).toMatchObject({
    host: "ghe.walk.example.test",
    kind: "github",
    type: "app",
    app: { appId: "123", installationId: "456" },
  });
  expect(res.status()).toBe(200);
  const row = rows.filter({ hasText: "ghe.walk.example.test" });
  await expect(row).toContainText("GitHub");
  await expect(row).toContainText("app");

  // Remove asks first; Cancel keeps the connection.
  await row.getByRole("button", { name: "Remove" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Remove Git connection?");
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(row).toHaveCount(1);
  await row.getByRole("button", { name: "Remove" }).click();
  await dialog.getByRole("button", { name: "Remove" }).click();
  await expect(row).toHaveCount(0);
  await page.reload();
  await expect(rows.filter({ hasText: "ghe.walk.example.test" })).toHaveCount(
    0,
  );
});

test("Capabilities shows each server's state, tests only connectable servers, and honours #capability-policy", async ({
  page,
  request,
}) => {
  const servers = await (await request.get("/api/capability-servers")).json();
  const needs = servers.find(
    (s) => s.status?.state === "needs-config" && !s.config?.disabled,
  );
  const inProcess = servers.find((s) => s.inProcess);
  await page.goto("/next/settings/capabilities#capability-policy");
  // The deep link lands on the policy card, inside the viewport.
  const policy = page.getByTestId("card-capability-policy");
  await expect(policy).toBeInViewport();

  if (needs) {
    await expect(page.getByTestId("cap-server-state-" + needs.id)).toHaveText(
      "needs-configuring",
    );
  }
  expect(inProcess).toBeTruthy();
  await expect(page.getByTestId("test-cap-server-" + inProcess.id)).toHaveCount(
    0,
  );

  // A server that refuses the probe says why.
  const id = "https://capabilities.walk.example.test/refuses";
  await request.put("/api/capability-servers", {
    data: { id, config: { serverUrl: "http://127.0.0.1:9/mcp" } },
  });
  try {
    await page.reload();
    await page.getByTestId("test-cap-server-" + id).click();
    const notice = page.getByTestId("cap-test-notice");
    await expect(notice).toContainText("failed —");
    await expect(notice).toContainText("connection refused");
    await expect(notice).toHaveClass(/warn/);

    // Remove asks first; Cancel keeps the server.
    const row = page.getByTestId("cap-server-row-" + id);
    await page.getByTestId("remove-cap-server-" + id).click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("Remove capability server?");
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(row).toHaveCount(1);
    await page.getByTestId("remove-cap-server-" + id).click();
    await dialog.getByRole("button", { name: "Remove" }).click();
    await expect(row).toHaveCount(0);
    await page.reload();
    await expect(page.getByTestId("cap-server-row-" + id)).toHaveCount(0);
  } finally {
    await request.post("/api/capability-servers/remove", { data: { id } });
  }

  // An old link lands on the card that replaced its page.
  await page.goto("/next/settings/secrets");
  await expect(page.locator("#other-secrets")).toBeInViewport();
});

test("Hosts says host, not hostd", async ({ page }) => {
  await page.goto("/next/settings/hosts");
  await expect(page.getByTestId("card-hostds-list")).toBeVisible();
  const text = await page.locator("main").innerText();
  expect(text).not.toMatch(/\bhostds?\b/i);
});
