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
import { test, expect } from "./fixtures.js";

// What the 2026-11-14 connections & infrastructure walkthrough
// (docs/test-runs/2026-11-14-connections-infrastructure-settings.md) found
// /next/ still lacked after task 5767 (G7, G8): the sandbox commitment
// note on Engine, and on Sandboxes the auto overcommit bounds, the disk
// overcommit and floor, the Kubernetes card and the custom image card.
// Every test that saves puts back what it changed.

type Json = Record<string, unknown>;

const COMMITMENT = {
  level: "warning",
  workers: 4,
  mergers: 1,
  ciJobs: 2,
  runs: 7,
  hostCpus: 8,
  hostMemoryMb: 32768,
  cpusPerSandbox: 4,
  memoryMbPerSandbox: 8192,
  cpusCommitted: 28,
  memoryMbCommitted: 57344,
  cpuRatio: 3.5,
  memoryShare: 1.75,
  budget: {
    mode: "auto",
    cpus: 16,
    cpusBounded: true,
    cpuOvercommit: 2,
    memoryMb: 28672,
    memoryBounded: true,
    memoryOvercommit: 1,
    reserveMemoryMb: 4096,
    reserveIsDefault: true,
    overcommit: {
      cpuOvercommit: 2.5,
      memoryOvercommit: 1.25,
      last: { cpuSome: 12.5, memorySome: 0.5 },
    },
  },
};

const OVERCOMMIT_AUTO = {
  mode: "auto",
  cpuMin: 1,
  cpuMax: 6,
  memoryMin: 1,
  memoryMax: 2,
  cpuPressureTarget: 25,
  memoryPressureTarget: 5,
};

// withSettings serves GET /api/settings with `patch` laid over what the
// demo answers, for state `grain demo` cannot be put in (a host to commit,
// volumes with a floor, a Kubernetes Granule pin).
async function withSettings(page: Page, patch: Json): Promise<void> {
  await page.route("**/api/settings", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    await route.fulfill({
      response: res,
      json: { ...(await res.json()), ...patch },
    });
  });
}

async function getSettings(request: APIRequestContext): Promise<Json> {
  const res = await request.get("/api/settings");
  expect(res.ok()).toBeTruthy();
  return (await res.json()) as Json;
}

async function save(page: Page): Promise<Json> {
  const btn = page.getByTestId("settings-save-btn");
  await expect(btn).toBeEnabled();
  const posted = page.waitForRequest(
    (r) => r.url().endsWith("/api/settings") && r.method() === "POST",
  );
  const done = page.waitForResponse(
    (r) => r.url().endsWith("/api/settings") && r.request().method() === "POST",
  );
  await btn.click();
  const body = (await posted).postDataJSON() as Json;
  expect((await done).status()).toBe(200);
  return body;
}

async function chooseAuto(page: Page): Promise<void> {
  await page.getByTestId("select-overcommit-mode").click();
  await page.getByTestId("select-overcommit-mode-opt-auto").click();
}

test("Engine states what the sandbox commitment comes to", async ({ page }) => {
  await withSettings(page, { sandboxCommitment: COMMITMENT });
  await page.goto("/next/settings/engine");
  const note = page.getByTestId("sandbox-commitment-note");
  await expect(note).toBeVisible();
  await expect(note).toHaveAttribute("data-level", "warning");
  await expect(note).toContainText(
    "Committed if every slot is full: 4 workers + 1 merger + 2 CI jobs × 4 vCPU = 28 vCPU committed against the 8 this host has (3.5×), held against a 16 vCPU sandbox budget",
  );
  await expect(note).toContainText("(grain's own reserve)");
  await expect(note).toContainText("Fewer workers, or a smaller sandbox");
  // It sits inside the Engine card.
  const card = await page.getByTestId("card-engine").boundingBox();
  const box = await note.boundingBox();
  expect(box!.x).toBeGreaterThanOrEqual(card!.x);
  expect(box!.x + box!.width).toBeLessThanOrEqual(card!.x + card!.width + 0.5);
});

test("Engine says nothing about a commitment the server could not work out", async ({
  page,
}) => {
  // `grain demo` reads no host, so it sends no sandboxCommitment.
  await page.goto("/next/settings/engine");
  await expect(page.getByTestId("card-engine")).toBeVisible();
  await expect(page.getByTestId("sandbox-commitment-note")).toHaveCount(0);
});

test("Sandboxes: auto bounds refuse an out-of-range value by its label and save whole", async ({
  page,
  request,
}) => {
  const before = await getSettings(request);
  await page.goto("/next/settings/sandboxes");
  await expect(page.getByTestId("overcommit-auto-bounds")).toHaveCount(0);
  await chooseAuto(page);
  const bounds = page.getByTestId("overcommit-auto-bounds");
  await expect(bounds).toBeVisible();

  const cpuMin = page.getByLabel("Auto CPU minimum");
  await expect(cpuMin).toHaveValue("1");
  await cpuMin.fill("0.1");
  await expect(page.getByTestId("error-oc-cpuMin")).toHaveText(
    "Auto CPU minimum must be a number of at least 0.25.",
  );
  await expect(cpuMin).toHaveAttribute("aria-invalid", "true");
  await expect(page.getByTestId("settings-save-btn")).toBeDisabled();

  const target = page.getByLabel("CPU pressure target (%)");
  await target.fill("150");
  await expect(page.getByTestId("error-oc-cpuPressureTarget")).toHaveText(
    "CPU pressure target (%) must be a number from 0.5 to 100.",
  );
  await target.fill("30");
  await expect(page.getByTestId("error-oc-cpuPressureTarget")).toHaveCount(0);

  // A minimum above its maximum is refused on both boxes of the pair,
  // and clears when either is put right.
  await cpuMin.fill("8");
  await expect(page.getByTestId("error-oc-cpuMin")).toHaveText(
    "Auto CPU minimum (8) is above Auto CPU maximum (6).",
  );
  await expect(page.getByTestId("error-oc-cpuMax")).toHaveText(
    "Auto CPU minimum (8) is above Auto CPU maximum (6).",
  );
  await page.getByLabel("Auto CPU maximum").fill("10");
  await expect(page.getByTestId("error-oc-cpuMin")).toHaveCount(0);
  await expect(page.getByTestId("error-oc-cpuMax")).toHaveCount(0);

  try {
    const body = await save(page);
    expect(body["sandboxOvercommit"]).toEqual({
      mode: "auto",
      cpuMin: 8,
      cpuMax: 10,
      memoryMin: 1,
      memoryMax: 2,
      cpuPressureTarget: 30,
      memoryPressureTarget: 5,
    });
    await page.reload();
    await expect(page.getByLabel("Auto CPU maximum")).toHaveValue("10");
    // A box left blank goes back to grain's default.
    await page.getByLabel("Auto CPU maximum").fill("");
    await page.getByLabel("Auto CPU maximum").blur();
    await expect(page.getByLabel("Auto CPU maximum")).toHaveValue("6");
  } finally {
    await request.post("/api/settings", {
      data: { sandboxOvercommit: before["sandboxOvercommit"] },
    });
  }
});

test("Sandboxes shows live auto factors from the server", async ({ page }) => {
  await withSettings(page, {
    sandboxCommitment: COMMITMENT,
    sandboxOvercommit: { ...OVERCOMMIT_AUTO },
  });
  await page.goto("/next/settings/sandboxes");
  await expect(page.getByTestId("auto-overcommit-effective")).toHaveText(
    "Auto overcommit is active — live effective factors: CPU 2.50×, memory 1.25× (PSI: CPU 12.5%, memory 0.5%).",
  );
  await expect(
    page
      .getByTestId("card-sandbox-overcommit")
      .getByTestId("sandbox-commitment-note"),
  ).toBeVisible();
});

test("Sandboxes: disk overcommit and the disk floor show defaults, refuse, and reset", async ({
  page,
  request,
}) => {
  const before = await getSettings(request);
  await page.goto("/next/settings/sandboxes");
  const disk = page.getByLabel("Disk overcommit (x)");
  const share = page.getByLabel("Disk floor (share of each volume)");
  const min = page.getByLabel("Disk floor minimum (MiB)");
  await expect(disk).toHaveValue(String(before["sandboxDiskOvercommit"]));
  await expect(disk).toHaveAttribute(
    "placeholder",
    String(before["sandboxDiskOvercommitDefault"]),
  );
  await expect(share).toHaveAttribute(
    "placeholder",
    String(before["sandboxDiskFloorShareDefault"]),
  );
  await expect(min).toHaveAttribute(
    "placeholder",
    String(before["sandboxDiskFloorMinMbDefault"]),
  );

  await share.fill("1.5");
  await expect(page.getByTestId("error-sb-floor-share")).toHaveText(
    "Disk floor (share of each volume) must be a number from 0 to 1.",
  );
  await expect(page.getByTestId("settings-save-btn")).toBeDisabled();
  await share.fill("0.2");
  await expect(page.getByTestId("error-sb-floor-share")).toHaveCount(0);
  await min.fill("2.5");
  await expect(page.getByTestId("error-sb-floor-min")).toHaveText(
    "Disk floor minimum (MiB) must be a whole number of at least 0.",
  );
  await min.fill("0");
  await disk.fill("0");

  try {
    const body = await save(page);
    expect(body).toMatchObject({
      sandboxDiskFloorShare: 0.2,
      sandboxDiskFloorMinMb: 0,
      sandboxDiskOvercommit: 0,
    });
    await page.reload();
    // 0 is a value of its own here, so it shows as 0, not as blank.
    await expect(disk).toHaveValue("0");
    await expect(min).toHaveValue("0");
    // A box left blank goes back to grain's default.
    await disk.fill("");
    await disk.blur();
    await expect(disk).toHaveValue(
      String(before["sandboxDiskOvercommitDefault"]),
    );
    await min.fill("");
    await min.blur();
    await expect(min).toHaveValue(
      String(before["sandboxDiskFloorMinMbDefault"]),
    );
    const reset = await save(page);
    expect(reset).toMatchObject({
      sandboxDiskOvercommit: before["sandboxDiskOvercommitDefault"],
      sandboxDiskFloorMinMb: before["sandboxDiskFloorMinMbDefault"],
    });
  } finally {
    await request.post("/api/settings", {
      data: {
        sandboxDiskOvercommit: before["sandboxDiskOvercommit"],
        sandboxDiskFloorShare: before["sandboxDiskFloorShare"],
        sandboxDiskFloorMinMb: before["sandboxDiskFloorMinMb"],
      },
    });
  }
});

test("Sandboxes says what the disk floor keeps free on each volume", async ({
  page,
}) => {
  await withSettings(page, {
    sandboxDiskFloors: [
      {
        path: "/var/lib/grain",
        totalMB: 453632,
        floorMB: 45363,
        reclaimMB: 68044,
      },
    ],
  });
  await page.goto("/next/settings/sandboxes");
  await expect(page.getByTestId("disk-floor-note")).toHaveText(
    "Kept free on this deployment's own volumes: /var/lib/grain (443.0 GB) holds 44.3 GB free, sweeping back to 66.4 GB. Never more than 25% of a volume is kept free, whatever is set here.",
  );
});

test("Sandboxes: the Kubernetes card shows defaults, refuses bad names, and saves priority classes", async ({
  page,
  request,
}) => {
  const before = await getSettings(request);
  await page.goto("/next/settings/sandboxes");
  const card = page.getByTestId("card-kubernetes");
  await expect(card.getByLabel("Kubernetes namespace")).toHaveAttribute(
    "placeholder",
    String(before["k8sNamespaceDefault"]),
  );
  await expect(card.getByLabel("Kubernetes sandbox pod image")).toHaveAttribute(
    "placeholder",
    String(before["k8sSandboxImageDefault"]),
  );
  await expect(card.getByLabel("Kubectl binary")).toHaveAttribute(
    "placeholder",
    String(before["k8sKubectlDefault"]),
  );
  const agent = card.getByLabel("Priority class for agent runs");
  await expect(agent).toHaveValue(String(before["k8sPriorityClassAgent"]));
  await expect(agent).toHaveAttribute("placeholder", "none");

  await card.getByLabel("Kubernetes namespace").fill("Bad NS");
  await expect(page.getByTestId("error-k8s-k8sNamespace")).toContainText(
    'Kubernetes namespace "Bad NS" is not a Kubernetes name',
  );
  await expect(page.getByTestId("settings-save-btn")).toBeDisabled();
  await card.getByLabel("Kubernetes namespace").fill("");
  await card.getByLabel("Kubernetes workspace directory").fill("relative/dir");
  await expect(page.getByTestId("error-k8s-k8sWorkspace")).toContainText(
    "is not an absolute path",
  );
  await card.getByLabel("Kubernetes workspace directory").fill("");
  await card.getByLabel("Kubernetes node selector").fill("pool");
  await card.getByLabel("Kubernetes node selector").blur();
  await expect(page.getByTestId("error-k8s-k8sNodeSelector")).toHaveText(
    'Kubernetes node selector entry "pool" must be key=value.',
  );
  await card
    .getByLabel("Kubernetes node selector")
    .fill("pool=sandboxes, zone = a");
  await card.getByLabel("Kubernetes node selector").blur();
  await agent.fill("e2e-agent");
  await card.getByLabel("Priority class for CI jobs").fill("");

  try {
    const body = await save(page);
    expect(body).toMatchObject({
      k8sNodeSelector: ["pool=sandboxes", "zone = a"],
      k8sPriorityClassAgent: "e2e-agent",
      k8sPriorityClassCi: "",
    });
    await page.reload();
    await expect(agent).toHaveValue("e2e-agent");
    // An empty priority class is "none", not the default.
    await expect(card.getByLabel("Priority class for CI jobs")).toHaveValue("");
    await expect(card.getByLabel("Kubernetes node selector")).toHaveValue(
      "pool=sandboxes, zone=a",
    );
  } finally {
    await request.post("/api/settings", {
      data: {
        k8sNodeSelector: before["k8sNodeSelector"] ?? [],
        k8sPriorityClassAgent: before["k8sPriorityClassAgent"],
        k8sPriorityClassCi: before["k8sPriorityClassCi"],
      },
    });
  }
});

test("Sandboxes: the custom image card states the contract and reverts", async ({
  page,
  request,
}) => {
  const before = await getSettings(request);
  await page.goto("/next/settings/sandboxes");
  const card = page.getByTestId("card-custom-sandbox-image");
  const contract = before["sandboxContract"] as Json;
  await expect(card.getByTestId("sandbox-contract-versions")).toContainText(
    `${String(contract["contractLabel"])} = ${String(contract["contract"])}`,
  );
  await expect(card.getByTestId("sandbox-image-dockerfile")).toContainText(
    `FROM ${String(before["sandboxBaseImage"])}`,
  );
  const box = card.getByLabel("Custom sandbox image (OCI reference)");
  await expect(box).toHaveAttribute(
    "placeholder",
    String(before["sandboxImageDefault"]),
  );
  await expect(card.getByTestId("sandbox-image-reset")).toHaveCount(0);

  await box.fill("registry.example/team/sandbox:v1");
  try {
    expect(await save(page)).toMatchObject({
      sandboxImage: "registry.example/team/sandbox:v1",
    });
    await page.reload();
    await card.getByTestId("sandbox-image-reset").click();
    await expect(box).toHaveValue("");
    expect(await save(page)).toMatchObject({ sandboxImage: "" });
  } finally {
    await request.post("/api/settings", {
      data: { sandboxImage: before["sandboxImage"] ?? "" },
    });
  }
});

test("Sandboxes offers to clear a Kubernetes Granule pin holding the same image, and says it is a mirror", async ({
  page,
}) => {
  const ref = "registry.example/team/guest";
  await withSettings(page, {
    sandboxBackend: "kubernetes-granule",
    sandboxImageBackend: "kubernetes-granule",
    sandboxImage: ref,
    k8sGranuleImage: ref,
    k8sGranuleImageMirror: ref,
    k8sGranuleImageEffective: "registry.example/team/guest:v7",
  });
  await page.goto("/next/settings/sandboxes");
  await expect(page.getByTestId("k8s-granule-image-mirror")).toHaveText(
    `Treated as mirror repository ${ref}, not booted; sandboxes boot registry.example/team/guest:v7. Clearing it keeps that mirror as sandbox_image_mirror.`,
  );
  const pin = page.getByTestId("sandbox-image-k8s-granule-pin");
  await expect(pin).toContainText("holds the same reference: " + ref);
  // A Granule VM backend shows Granule's guest contract and sandbox-base.
  await expect(page.getByTestId("sandbox-contract-versions")).toContainText(
    "dev.granule.guest.contract",
  );
  await page.getByTestId("sandbox-image-k8s-granule-pin-clear").click();
  await expect(pin).toHaveCount(0);
  await expect(
    page.getByLabel("Custom sandbox image (OCI reference)"),
  ).toHaveValue("");
  await expect(
    page
      .getByTestId("card-kubernetes")
      .getByLabel("Kubernetes Granule VM pod image"),
  ).toHaveValue("");
  // Both are staged, nothing is sent until Save.
  await expect(page.getByTestId("settings-save-btn")).toBeEnabled();
});

test("Sandboxes: the new cards fit their width at every size @phone @dark", async ({
  page,
}) => {
  await withSettings(page, {
    sandboxCommitment: COMMITMENT,
    sandboxOvercommit: { ...OVERCOMMIT_AUTO },
    sandboxImage:
      "registry.example/a-rather-long-team-name/custom-sandbox-image:2026-11-15",
  });
  await page.goto("/next/settings/sandboxes");
  await expect(page.getByTestId("overcommit-auto-bounds")).toBeVisible();
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
  for (const id of [
    "card-sandbox-overcommit",
    "card-kubernetes",
    "card-custom-sandbox-image",
  ]) {
    const card = (await page.getByTestId(id).boundingBox())!;
    for (const inner of [
      "sandbox-commitment-note",
      "overcommit-auto-bounds",
      "input-k8sKubectl",
      "sandbox-image-dockerfile",
      "sandbox-image-reset",
      "input-sandbox-image",
    ]) {
      const el = page.getByTestId(id).getByTestId(inner);
      if ((await el.count()) === 0) continue;
      const b = (await el.boundingBox())!;
      expect(b.x, `${inner} in ${id}`).toBeGreaterThanOrEqual(card.x);
      expect(b.x + b.width, `${inner} in ${id}`).toBeLessThanOrEqual(
        card.x + card.width + 0.5,
      );
    }
  }
  // Reset sits on the image box's own line, to its right.
  const input = (await page.getByTestId("input-sandbox-image").boundingBox())!;
  const reset = (await page.getByTestId("sandbox-image-reset").boundingBox())!;
  expect(reset.x).toBeGreaterThanOrEqual(input.x + input.width);
  expect(
    Math.abs(reset.y + reset.height / 2 - (input.y + input.height / 2)),
  ).toBeLessThan(2);
});
