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

// Classic parity for /next/ Settings -> Hub, Sandboxes, Engine and CI
// (task 5521, from the 2026-11-13 connections & infrastructure
// walkthrough, docs/test-runs/2026-11-13-connections-infrastructure-
// settings.md). Every test puts back what it changed: tests in one file
// share the worker's demo.

async function getSettings(request) {
  const res = await request.get("/api/settings");
  expect(res.ok()).toBeTruthy();
  return res.json();
}

async function restore(request, patch) {
  const res = await request.post("/api/settings", { data: patch });
  expect(res.ok()).toBeTruthy();
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

test("Hub shows heartbeat health, advertised scopes and a visible heartbeat result", async ({
  page,
}) => {
  await page.goto("/next/settings/registry");
  await expect(page.getByTestId("registry-status-pill")).toHaveText(
    "Connected",
  );
  await expect(page.getByTestId("registry-endpoint")).toContainText(
    "registry.grain.example.com · acme-widgets",
  );
  const summary = page.getByTestId("registry-summary");
  await expect(summary).toContainText("Last heartbeat");
  await expect(summary).toContainText("(every 15s)");
  await expect(summary).toContainText("health ok");
  await expect(summary).toContainText("awaiting reply");

  const row = page.getByTestId("registry-advertised-row");
  await expect(row).toHaveCount(1);
  await expect(row).toContainText("acme/widgets");
  await expect(row).toContainText("/ (whole repo)");
  await expect(row).toContainText("primary");
  await expect(row.getByTestId("registry-origin-chip")).toHaveText(
    "from repo config",
  );

  const hb = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/registry/heartbeat") &&
      r.request().method() === "POST",
  );
  await page.getByTestId("registry-heartbeat-btn").click();
  expect((await hb).ok()).toBeTruthy();
  await expect(page.getByTestId("registry-heartbeat-result")).toContainText(
    "Heartbeat delivered at",
  );
  await expect(page.getByTestId("registry-heartbeat-result")).toContainText(
    "connected",
  );
});

test("Hub reports the hub's error and scope conflicts as alerts", async ({
  page,
}) => {
  // The demo's hub is always healthy; this adds the two fields a refusing
  // hub fills in to the real GET /api/registry answer.
  await page.route("**/api/registry", async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    body.state = "error";
    body.lastError = "hub refused heartbeat: 401 bad registration token";
    body.scopeConflicts = [
      {
        repo: "acme/widgets",
        path_prefix: "pkg",
        conflicting_entry: "other-grain",
      },
    ];
    await route.fulfill({ response: res, json: body });
  });
  await page.goto("/next/settings/registry");
  await expect(page.getByTestId("registry-status-pill")).toHaveText(
    "Heartbeat error",
  );
  await expect(page.getByTestId("registry-last-error")).toContainText(
    "401 bad registration token",
  );
  await expect(page.getByTestId("registry-scope-conflicts")).toContainText(
    "acme/widgets (pkg) already claimed as primary by other-grain",
  );
});

test("Hub catalog fields and Scope overrides save and read back", async ({
  page,
  request,
}) => {
  const before = await getSettings(request);
  try {
    await page.goto("/next/settings/registry");
    // OIDC audience applies to Workload OIDC only.
    await expect(page.getByTestId("input-hub-oidc-audience")).toBeDisabled();
    await page.getByTestId("select-hub-auth-mode").click();
    await page.getByTestId("select-hub-auth-mode-opt-oidc").click();
    await expect(page.getByTestId("input-hub-oidc-audience")).toBeEnabled();
    await page
      .getByTestId("input-hub-oidc-audience")
      .fill("https://hub.example.test");

    await expect(page.getByTestId("select-hub-host-kind")).toContainText(
      "Workstation",
    );
    await page.getByTestId("select-hub-host-kind").click();
    await page.getByTestId("select-hub-host-kind-opt-kubernetes").click();
    await page.getByTestId("input-hub-purpose").fill("Parity walk");
    const listed = page.getByTestId("switch-hub-listed");
    await expect(listed).toHaveAttribute("aria-checked", "true");
    await listed.click();
    await expect(listed).toHaveAttribute("aria-checked", "false");

    // A malformed repository is refused by name and adds nothing.
    await page.getByTestId("input-scope-rule-repo").fill("not-a-repo");
    await page.getByTestId("add-scope-rule").click();
    await expect(page.getByTestId("scope-rule-error")).toContainText(
      '"not-a-repo"',
    );
    await expect(page.getByTestId("registry-scope-rule")).toHaveCount(0);

    await page.getByTestId("input-scope-rule-repo").fill("acme/widgets");
    await page.getByTestId("input-scope-rule-prefix").fill("/pkg/api/");
    await page.getByTestId("add-scope-rule").click();
    await page.getByTestId("input-scope-rule-repo").fill("acme/extra");
    await page.getByTestId("select-scope-rule-mode").click();
    await page.getByTestId("select-scope-rule-mode-opt-exclude").click();
    await page.getByTestId("add-scope-rule").click();
    await expect(page.getByTestId("registry-scope-rule")).toHaveCount(2);
    await expect(page.getByTestId("registry-scope-rule").nth(0)).toContainText(
      "pkg/api/",
    );
    await expect(page.getByTestId("registry-scope-rule").nth(1)).toContainText(
      "excluded",
    );

    const res = await saveBar(page);
    expect(res.ok()).toBeTruthy();
    const sent = res.request().postDataJSON().hub;
    expect(sent).toMatchObject({
      authMode: "oidc",
      oidcAudience: "https://hub.example.test",
      hostKind: "kubernetes",
      purpose: "Parity walk",
      listed: false,
      scopes: [
        { repo: "acme/widgets", pathPrefix: "pkg/api", role: "primary" },
        { repo: "acme/extra", pathPrefix: "", exclude: true },
      ],
    });

    await page.reload();
    await expect(page.getByTestId("input-hub-oidc-audience")).toHaveValue(
      "https://hub.example.test",
    );
    await expect(page.getByTestId("select-hub-host-kind")).toContainText(
      "Kubernetes",
    );
    await expect(page.getByTestId("input-hub-purpose")).toHaveValue(
      "Parity walk",
    );
    await expect(page.getByTestId("switch-hub-listed")).toHaveAttribute(
      "aria-checked",
      "false",
    );
    await expect(page.getByTestId("registry-scope-rule")).toHaveCount(2);
    // The override shows in what the daemon advertises, marked as one.
    await expect(
      page
        .getByTestId("registry-advertised-row")
        .filter({ hasText: "pkg/api/" })
        .getByTestId("registry-origin-chip"),
    ).toHaveText("override");

    // Removing a rule stages the shorter list.
    await page
      .getByRole("button", { name: "Remove scope rule for acme/extra" })
      .click();
    await expect(page.getByTestId("registry-scope-rule")).toHaveCount(1);
    const res2 = await saveBar(page);
    expect(res2.request().postDataJSON().hub.scopes).toEqual([
      { repo: "acme/widgets", pathPrefix: "pkg/api", role: "primary" },
    ]);
  } finally {
    await restore(request, { hub: before.hub });
  }
});

test("Hub registration token says so when the daemon has no secret store", async ({
  page,
}) => {
  // grain demo has no secrets directory: GET /api/secrets is
  // {"enabled": false} and a PUT is refused, so the box is not offered.
  await page.goto("/next/settings/registry");
  await expect(page.getByTestId("input-hub-token")).toBeDisabled();
  await expect(page.getByTestId("registry-token-no-store")).toContainText(
    "no secret store",
  );
});

test("Hub registration token is write-only and stays editable under a config-repo lock", async ({
  page,
}) => {
  // Stubs, because grain demo has neither a secret store nor a config-repo
  // lock: the real GET /api/settings answer gains settingsLocked (and
  // hubTokenSet once the token is stored), GET /api/secrets says the store
  // is on, and the token PUT/DELETE answer as a daemon with one does.
  let tokenSet = false;
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const body = await res.json();
    // The UI keys its lock on configRepoOnly (what the server derives
    // from settingsLocked plus a config repo), as classic Settings does.
    body.configRepoOnly = true;
    body.settingsLocked = true;
    body.hubTokenSet = tokenSet;
    await route.fulfill({ response: res, json: body });
  });
  await page.route("**/api/secrets", (route) =>
    route.fulfill({ json: { enabled: true, secrets: [] } }),
  );
  const sent = [];
  await page.route(
    "**/api/secrets/registry-registration-token/value",
    (route) => {
      const req = route.request();
      sent.push({ method: req.method(), body: req.postDataJSON() });
      tokenSet = req.method() === "PUT";
      return route.fulfill({ json: { enabled: true, secrets: [] } });
    },
  );

  await page.goto("/next/settings/registry");
  await expect(page.getByTestId("input-hub-purpose")).toBeDisabled();
  await expect(page.getByTestId("select-hub-host-kind")).toBeDisabled();
  await expect(page.getByTestId("add-scope-rule")).toBeDisabled();

  const token = page.getByTestId("input-hub-token");
  await expect(token).toBeEnabled();
  await expect(token).toHaveAttribute("type", "password");
  await expect(token).toHaveAttribute("autocomplete", "new-password");
  await expect(page.getByTestId("registry-token-state")).toHaveText("not set");
  await token.fill("grc_parity_walk_token");
  await page.getByTestId("save-hub-token").click();
  await expect(page.getByTestId("registry-token-state")).toHaveText("set");
  await expect(token).toHaveValue("");
  expect(sent).toEqual([
    { method: "PUT", body: { value: "grc_parity_walk_token" } },
  ]);

  // Clearing asks first; Cancel keeps it.
  await page.getByTestId("clear-hub-token").click();
  await page.getByRole("button", { name: "Cancel" }).click();
  expect(sent).toHaveLength(1);
  await page.getByTestId("clear-hub-token").click();
  await page.getByRole("button", { name: "Clear token" }).click();
  await expect(page.getByTestId("registry-token-state")).toHaveText("not set");
  expect(sent.map((x) => x.method)).toEqual(["PUT", "DELETE"]);
});

test("Sandboxes auto-overcommit bounds refuse an out-of-range bound by name and save", async ({
  page,
  request,
}) => {
  const before = await getSettings(request);
  try {
    await page.goto("/next/settings/sandboxes");
    await expect(page.getByTestId("overcommit-auto-bounds")).toHaveCount(0);
    await page.getByTestId("select-overcommit-mode").click();
    await page.getByTestId("select-overcommit-mode-opt-auto").click();
    const cpuMax = page.getByTestId("input-overcommit-cpuMax");
    await expect(cpuMax).toHaveValue("6");
    await expect(page.getByTestId("input-overcommit-cpuMin")).toHaveValue("1");

    await cpuMax.fill("-2");
    await expect(page.getByTestId("error-overcommit-cpuMax")).toContainText(
      "Auto CPU maximum (x) must be 0 (grain's default) or more",
    );
    await expect(page.getByTestId("settings-save-btn")).toBeDisabled();
    // Back to fixed hides the bounds; a refusal there no longer holds Save.
    await page.getByTestId("select-overcommit-mode").click();
    await page.getByTestId("select-overcommit-mode-opt-fixed").click();
    await expect(page.getByTestId("overcommit-auto-bounds")).toHaveCount(0);
    await expect(page.getByTestId("settings-save-btn")).toHaveCount(0);
    await page.getByTestId("select-overcommit-mode").click();
    await page.getByTestId("select-overcommit-mode-opt-auto").click();
    await expect(page.getByTestId("error-overcommit-cpuMax")).toHaveCount(0);
    await expect(cpuMax).toHaveValue("6");
    await page.getByTestId("input-overcommit-cpuPressureTarget").fill("150");
    await expect(
      page.getByTestId("error-overcommit-cpuPressureTarget"),
    ).toContainText("at most 100");
    await page.getByTestId("input-overcommit-cpuPressureTarget").fill("30");

    // A minimum above its maximum is the server's to refuse, by name.
    await cpuMax.fill("4");
    await page.getByTestId("input-overcommit-cpuMin").fill("5");
    const bad = await saveBar(page);
    expect(bad.status()).toBe(400);
    await expect(page.getByTestId("settings-error-banner")).toContainText(
      "cpuMin 5.00 is above cpuMax 4.00",
    );

    await page.getByTestId("input-overcommit-cpuMin").fill("2");
    const ok = await saveBar(page);
    expect(ok.ok()).toBeTruthy();
    expect(ok.request().postDataJSON().sandboxOvercommit).toMatchObject({
      mode: "auto",
      cpuMin: 2,
      cpuMax: 4,
      cpuPressureTarget: 30,
    });
    await page.reload();
    await expect(page.getByTestId("input-overcommit-cpuMax")).toHaveValue("4");
    await expect(page.getByTestId("input-overcommit-cpuMin")).toHaveValue("2");
  } finally {
    await restore(request, { sandboxOvercommit: before.sandboxOvercommit });
  }
});

test("Sandboxes disk floor: a blank minimum resets to grain's default", async ({
  page,
  request,
}) => {
  const before = await getSettings(request);
  try {
    await page.goto("/next/settings/sandboxes");
    const min = page.getByTestId("input-disk-floor-min");
    await expect(min).toHaveAttribute(
      "placeholder",
      String(before.sandboxDiskFloorMinMbDefault),
    );
    await min.fill("0");
    await page.getByTestId("input-disk-floor-share").fill("0.2");
    let res = await saveBar(page);
    expect(res.request().postDataJSON()).toMatchObject({
      sandboxDiskFloorMinMb: 0,
      sandboxDiskFloorShare: 0.2,
    });
    await page.reload();
    await expect(page.getByTestId("input-disk-floor-min")).toHaveValue("0");

    await page.getByTestId("input-disk-floor-min").fill("");
    await page.getByTestId("input-disk-floor-min").blur();
    res = await saveBar(page);
    expect(res.request().postDataJSON().sandboxDiskFloorMinMb).toBe(
      before.sandboxDiskFloorMinMbDefault,
    );
    expect((await getSettings(request)).sandboxDiskFloorMinMb).toBe(
      before.sandboxDiskFloorMinMbDefault,
    );
  } finally {
    await restore(request, {
      sandboxDiskFloorMinMb: before.sandboxDiskFloorMinMb,
      sandboxDiskFloorShare: before.sandboxDiskFloorShare,
    });
  }
});

test("Sandboxes Kubernetes card shows defaults, validates the node selector and saves", async ({
  page,
  request,
}) => {
  const before = await getSettings(request);
  try {
    await page.goto("/next/settings/sandboxes");
    const card = page.getByTestId("card-kubernetes");
    await expect(card.getByTestId("input-k8sKubectl")).toHaveAttribute(
      "placeholder",
      before.k8sKubectlDefault,
    );
    await expect(card.getByTestId("input-k8sSandboxImage")).toHaveAttribute(
      "placeholder",
      before.k8sSandboxImageDefault,
    );
    await expect(card.getByTestId("input-k8sWorkspace")).toHaveAttribute(
      "placeholder",
      before.k8sWorkspaceDefault,
    );
    await expect(card.getByTestId("input-k8sPriorityClassCi")).toHaveValue(
      before.k8sPriorityClassCi,
    );
    await expect(card).toContainText("Read at daemon startup.");

    const sel = card.getByTestId("input-k8sNodeSelector");
    await sel.fill("pool=sandboxes, bogus");
    await expect(card.getByTestId("error-k8sNodeSelector")).toContainText(
      '"bogus" must be key=value',
    );
    await expect(page.getByTestId("settings-save-btn")).toBeDisabled();
    // Discard puts back the stored list, not the typed text.
    await page.getByTestId("settings-discard-btn").click();
    await expect(sel).toHaveValue((before.k8sNodeSelector ?? []).join(", "));
    await expect(card.getByTestId("error-k8sNodeSelector")).toHaveCount(0);
    await sel.fill("pool=sandboxes, zone=a");
    await card.getByTestId("input-k8sKubectl").fill("/usr/local/bin/kubectl");
    await card.getByTestId("input-k8sServiceAccount").fill("grain-sandbox");
    await card.getByTestId("input-k8sPriorityClassAgent").fill("walk-agent");
    await card.getByTestId("switch-k8s-privileged").click();
    const res = await saveBar(page);
    expect(res.ok()).toBeTruthy();
    expect(res.request().postDataJSON()).toMatchObject({
      k8sNodeSelector: ["pool=sandboxes", "zone=a"],
      k8sKubectl: "/usr/local/bin/kubectl",
      k8sServiceAccount: "grain-sandbox",
      k8sPriorityClassAgent: "walk-agent",
      k8sPrivileged: !before.k8sPrivileged,
    });
    await page.reload();
    await expect(
      page.getByTestId("card-kubernetes").getByTestId("input-k8sNodeSelector"),
    ).toHaveValue("pool=sandboxes, zone=a");
    await expect(
      page.getByTestId("card-kubernetes").getByTestId("input-k8sKubectl"),
    ).toHaveValue("/usr/local/bin/kubectl");

    // The server's own checks are reported by name.
    await page
      .getByTestId("card-kubernetes")
      .getByTestId("input-k8sWorkspace")
      .fill("relative/dir");
    const bad = await saveBar(page);
    expect(bad.status()).toBe(400);
    // ...under the field's label rather than its JSON key.
    await expect(page.getByTestId("settings-error-banner")).toContainText(
      "Kubernetes workspace directory:",
    );
  } finally {
    await restore(request, {
      k8sNodeSelector: before.k8sNodeSelector ?? [],
      k8sKubectl: before.k8sKubectl,
      k8sServiceAccount: before.k8sServiceAccount,
      k8sPriorityClassAgent: before.k8sPriorityClassAgent,
      k8sPrivileged: before.k8sPrivileged,
      k8sWorkspace: before.k8sWorkspace,
    });
  }
});

test("Custom sandbox image card states the contract and reverts cleanly to the default", async ({
  page,
  request,
}) => {
  const before = await getSettings(request);
  try {
    await page.goto("/next/settings/sandboxes");
    const card = page.getByTestId("card-custom-sandbox-image");
    const input = card.getByTestId("input-sandbox-image");
    await expect(input).toHaveValue("");
    await expect(input).toHaveAttribute(
      "placeholder",
      before.sandboxImageDefault,
    );
    await expect(card.getByTestId("sandbox-image-dockerfile")).toContainText(
      "FROM docker.io/grainai/sandbox-k8s-base",
    );
    await expect(card.getByTestId("sandbox-contract-versions")).toContainText(
      `dev.grain.sandbox.contract = ${before.sandboxContractVersion}`,
    );
    await expect(card.getByTestId("sandbox-image-reset")).toHaveCount(0);

    await input.fill("registry.example/team/sandbox:v1");
    let res = await saveBar(page);
    expect(res.ok()).toBeTruthy();
    await page.reload();
    await expect(
      page
        .getByTestId("card-custom-sandbox-image")
        .getByTestId("input-sandbox-image"),
    ).toHaveValue("registry.example/team/sandbox:v1");

    await page.getByTestId("sandbox-image-reset").click();
    await expect(
      page
        .getByTestId("card-custom-sandbox-image")
        .getByTestId("input-sandbox-image"),
    ).toHaveValue("");
    res = await saveBar(page);
    expect(res.request().postDataJSON()).toEqual({ sandboxImage: "" });
    expect((await getSettings(request)).sandboxImage).toBe("");
  } finally {
    await restore(request, { sandboxImage: before.sandboxImage ?? "" });
  }
});

test("Engine shows the sandbox commitment note under Max merges", async ({
  page,
}) => {
  // The demo has no sandbox host to read, so it reports no commitment;
  // this adds one to the real GET /api/settings answer.
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const body = await res.json();
    body.sandboxCommitment = {
      level: "warning",
      workers: 10,
      mergers: 5,
      ciJobs: 4,
      runs: 19,
      cpusPerSandbox: 2,
      cpusCommitted: 38,
      hostCpus: 8,
      cpuRatio: 4.75,
      memoryMbPerSandbox: 8192,
      memoryMbCommitted: 155648,
      hostMemoryMb: 32768,
      memoryShare: 4.75,
    };
    await route.fulfill({ response: res, json: body });
  });
  await page.goto("/next/settings/engine");
  const note = page.getByTestId("sandbox-commitment-note");
  await expect(note).toHaveAttribute("data-level", "warning");
  await expect(note).toContainText(
    "10 workers + 5 mergers + 4 CI jobs × 2 vCPU = 38 vCPU committed against the 8 this host has (4.8×)",
  );
  await expect(note).toContainText("Fewer workers, or a smaller sandbox");
  // It sits under Max merges per repo branch, not elsewhere on the page.
  const mpr = await page.getByTestId("input-max-merges-per-repo").boundingBox();
  const box = await note.boundingBox();
  expect(box.y).toBeGreaterThan(mpr.y);
});

test("CI retention limits show the server's defaults and blank resets", async ({
  page,
  request,
}) => {
  const before = await getSettings(request);
  const keys = [
    "ciStepOutputMb",
    "ciStepOutputDays",
    "ciRunOutputKb",
    "ciArtifactRunMb",
    "ciArtifactFileMb",
    "ciArtifactDays",
    "ciCacheEntries",
    "ciCacheDays",
  ];
  try {
    await page.goto("/next/settings/ci");
    for (const k of keys) {
      await expect(page.getByTestId(`input-${k}`)).toHaveValue("");
      await expect(page.getByTestId(`input-${k}`)).toHaveAttribute(
        "placeholder",
        String(before[`${k}Default`]),
      );
    }
    await expect(page.getByTestId("input-ciAgeBoost")).toHaveAttribute(
      "placeholder",
      before.ciAgeBoostDefault,
    );
    await page.getByTestId("input-ciStepOutputMb").fill("-1");
    await page.getByTestId("input-ciArtifactFileMb").fill("12");
    await page.getByTestId("input-ciCacheEntries").fill("900");
    await page.getByTestId("input-ciAgeBoost").fill("90s");
    let res = await saveBar(page);
    expect(res.ok()).toBeTruthy();
    expect(res.request().postDataJSON()).toMatchObject({
      ciStepOutputMb: -1,
      ciArtifactFileMb: 12,
      ciCacheEntries: 900,
      ciAgeBoost: "90s",
    });
    await page.reload();
    await expect(page.getByTestId("input-ciStepOutputMb")).toHaveValue("-1");
    await expect(page.getByTestId("input-ciCacheEntries")).toHaveValue("900");
    await expect(page.getByTestId("input-ciAgeBoost")).toHaveValue("1m30s");

    await page.getByTestId("input-ciCacheEntries").fill("");
    res = await saveBar(page);
    expect(res.request().postDataJSON()).toEqual({ ciCacheEntries: 0 });
    await page.reload();
    await expect(page.getByTestId("input-ciCacheEntries")).toHaveValue("");
  } finally {
    const patch = { ciAgeBoost: before.ciAgeBoost ?? "" };
    for (const k of keys) patch[k] = before[k];
    await restore(request, patch);
  }
});

test("Hub and Sandboxes cards fit a phone without horizontal overflow @phone @dark", async ({
  page,
}) => {
  for (const path of [
    "/next/settings/registry",
    "/next/settings/sandboxes",
    "/next/settings/ci",
  ]) {
    await page.goto(path);
    await expect(page.locator(".s-card").first()).toBeVisible();
    const vw = page.viewportSize().width;
    const over = await page
      .locator("main .s-card")
      .evaluateAll(
        (cards, width) =>
          cards
            .filter((c) => c.getBoundingClientRect().right > width + 1)
            .map((c) => c.dataset.testid || c.id),
        vw,
      );
    expect(over, path).toEqual([]);
    // Nothing inside a card spills past its edge (long image refs and the
    // Dockerfile scroll inside their own boxes).
    const spill = await page.locator("main .s-card").evaluateAll((cards) =>
      cards.flatMap((c) => {
        const r = c.getBoundingClientRect();
        return [...c.querySelectorAll("input, button, .status-pill, pre")]
          .filter((el) => {
            const b = el.getBoundingClientRect();
            return (
              b.width > 0 && (b.right > r.right + 1 || b.left < r.left - 1)
            );
          })
          .map((el) => `${c.dataset.testid}: ${el.outerHTML.slice(0, 80)}`);
      }),
    );
    expect(spill, path).toEqual([]);
  }
});

test("Sandboxes: a cleared overcommit or floor share saves grain's default, a typed 0 stays 0", async ({
  page,
  request,
}) => {
  const before = await getSettings(request);
  const keys = [
    ["sb-cpu-oc", "sandboxCpuOvercommit"],
    ["sb-mem-oc", "sandboxMemoryOvercommit"],
    ["sb-floor-share", "sandboxDiskFloorShare"],
  ];
  try {
    await page.goto("/next/settings/sandboxes");
    // 0 is a value of its own here (unbounded / hold nothing back).
    for (const [id] of keys) await page.locator(`#${id}`).fill("0");
    let res = await saveBar(page);
    expect(res.ok()).toBeTruthy();
    for (const [, k] of keys) expect(res.request().postDataJSON()[k]).toBe(0);
    await page.reload();
    await expect(page.locator("#sb-cpu-oc")).toHaveValue("0");
    await expect(page.getByTestId("cpu-overcommit-unbounded")).toBeVisible();

    // A cleared box shows the default greyed, and that is what Save stores.
    for (const [id, k] of keys) {
      const box = page.locator(`#${id}`);
      await box.fill("");
      await box.blur();
      await expect(box).toHaveAttribute(
        "placeholder",
        String(before[`${k}Default`]),
      );
    }
    res = await saveBar(page);
    const sent = res.request().postDataJSON();
    const after = await getSettings(request);
    for (const [, k] of keys) {
      expect(sent[k]).toBe(before[`${k}Default`]);
      expect(after[k]).toBe(before[`${k}Default`]);
    }
  } finally {
    const patch = {};
    for (const [, k] of keys) patch[k] = before[k];
    await restore(request, patch);
  }
});
