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

import type { Page } from "@playwright/test";
import { expect, test } from "./fixtures";

// Task 6522 (gaps G1 and G2 of the 2026-11-16 connections & infrastructure
// walkthrough), over a real `grain demo -secrets-store`:
//   G1 -- a capability policy Deny rule shows in New task's grants picker.
//   G2 -- /next/settings/capabilities/<id>, the per-server page, for the
//         demo's gcp, host-script, figma, config-map, custom and in-process
//         servers.
// Every change is a throwaway entry and is put back in finally.

test.use({ demoArgs: ["-secrets-store"] });

const GCP_ID = "https://grain.dev/capabilities/gcp";
const GCP_KEY_REF = `${GCP_ID}#gcp-key`;
const HOST_SCRIPT_ID = "https://grain.dev/capabilities/host-script";
const FIGMA_ID = "https://grain.dev/capabilities/figma";
const CONFIG_MAP_ID = "https://grain.dev/capabilities/config-map";
const CLOUD_ID = "https://capabilities.demo.grain.example/cloud";

const detail = (id: string) =>
  `/next/settings/capabilities/${encodeURIComponent(id)}`;

async function openNewTaskGrants(page: Page) {
  await page.goto("/next/inbox");
  const vpWidth = page.viewportSize()?.width ?? 1440;
  await page
    .getByTestId(vpWidth < 600 ? "phone-new-task" : "rail-new-task")
    .click();
  await expect(page.getByTestId("new-task-sheet")).toBeVisible();
  await page.getByTestId("caps-options").click();
  await page.getByTestId("nt-grants-add-open").click();
  await page.getByTestId("nt-grants-mode-method").click();
}

test("G1: a Deny rule marks gcp#gcp-key refused in New task's grants picker", async ({
  page,
  request,
}) => {
  const before = await (await request.get("/api/capability-policy")).json();
  try {
    await page.goto("/next/settings/capabilities#capability-policy");
    await page.getByTestId("input-policy-capability").fill(GCP_KEY_REF);
    await page.getByTestId("select-policy-effect").click();
    await page.getByRole("option", { name: /deny/i }).first().click();
    await page.getByTestId("add-policy-rule-btn").click();
    const put = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/capability-policy") &&
        r.request().method() === "PUT",
    );
    await page.getByTestId("save-cap-policy-btn").click();
    expect((await put).ok()).toBeTruthy();

    await openNewTaskGrants(page);
    await page.getByTestId("nt-grants-method-ref").click();
    const opt = page
      .getByRole("option")
      .filter({ hasText: GCP_KEY_REF })
      .first();
    await expect(opt).toBeVisible();
    await expect(opt).toHaveAttribute("aria-disabled", "true");
    await expect(opt).toContainText(/capability policy/i);
  } finally {
    await request.put("/api/capability-policy", {
      data: before.policy ? { policy: before.policy } : before,
    });
  }
});

test("G2: server rows open the per-server page; an encoded URL survives a reload; (i) opens by click, Enter and Space", async ({
  page,
}) => {
  await page.goto("/next/settings/capabilities");
  await page.getByTestId(`open-cap-server-${GCP_ID}`).click();
  await expect(page.getByTestId("capability-server-detail-page")).toBeVisible();
  await expect(page.getByTestId("capability-server-methods")).toContainText(
    "gcp-key",
  );

  await page.goto(detail(GCP_ID));
  await page.reload();
  await expect(page.getByTestId("capability-server-detail-page")).toBeVisible();
  await expect(page.getByTestId("capability-server-methods")).toContainText(
    "gcp-key",
  );

  await page.getByTestId("capability-edit-settings-btn").click();
  const form = page.getByTestId("capability-server-config");
  await expect(form).toBeVisible();
  await expect(form.locator("select")).toHaveCount(0);

  const info = form.getByTestId("info-popover").first();
  const tip = page.locator('[role="tooltip"]');
  await info.click();
  await expect(tip).toHaveCount(1);
  await info.click();
  await expect(tip).toHaveCount(0);

  // Move focus away, then Tab-style focus: focus opens, Enter pins rather
  // than closing it, a second Enter closes.
  await page.getByTestId("capability-servers-back").focus();
  await info.focus();
  await expect(tip).toHaveCount(1);
  await info.press("Enter");
  await expect(tip).toHaveCount(1);
  await info.press("Enter");
  await expect(tip).toHaveCount(0);
  await info.press(" ");
  await expect(tip).toHaveCount(1);
  await page.keyboard.press("Escape");
  await expect(tip).toHaveCount(0);

  await page.getByTestId("capability-servers-back").click();
  await expect(page).toHaveURL(/\/next\/settings\/capabilities$/);
});

test("G2: host-script Test runs the demo's ops script", async ({ page }) => {
  await page.goto(detail(HOST_SCRIPT_ID));
  const list = page.getByTestId("host-script-list").last();
  await expect(list).toContainText("ops");
  await list.getByRole("button", { name: "Test", exact: true }).click();
  const panel = page.getByTestId("host-script-test");
  await panel.getByRole("button", { name: /Add argument/ }).click();
  await panel.locator('[data-host-script-arg="0"]').fill("hello-e2e");
  await panel.getByRole("button", { name: "Run ops" }).click();
  await expect(page.getByTestId("host-script-exit")).toContainText("0");
  await expect(page.getByTestId("host-script-stdout")).toContainText(
    "hello-e2e",
  );
});

test("G2: figma OAuth account shows Connect Figma and its callback URL; a PAT account's Test signs in", async ({
  page,
  request,
}) => {
  const servers = await (await request.get("/api/capability-servers")).json();
  const orig = servers.find((s: { id: string }) => s.id === FIGMA_ID)?.config;
  expect(orig).toBeTruthy();
  try {
    const res = await request.put("/api/capability-servers", {
      data: {
        id: FIGMA_ID,
        config: {
          ...orig,
          config: {
            ...(orig.config ?? {}),
            accounts: [
              { name: "e2e-oauth", auth_mode: "oauth", client_id: "e2e-cid" },
              { name: "e2e-pat", auth_mode: "pat" },
            ],
          },
        },
        configSecrets: {
          personal_access_tokens: JSON.stringify({ "e2e-pat": "figd_e2e" }),
          client_secrets: JSON.stringify({ "e2e-oauth": "e2e-secret" }),
        },
      },
    });
    expect(res.ok(), await res.text()).toBeTruthy();

    await page.goto(detail(FIGMA_ID));
    const oauth = page.locator('[data-figma-account="e2e-oauth"]');
    await expect(
      oauth.getByRole("button", { name: "Connect Figma" }),
    ).toBeVisible();
    await expect(oauth).toContainText("/settings/capabilities");
    await expect(oauth).toContainText("callback URL on the Figma OAuth app");

    const pat = page.locator('[data-figma-account="e2e-pat"]');
    await expect(
      pat.getByRole("button", { name: "Test", exact: true }),
    ).toBeEnabled({ timeout: 15000 });
    await pat.getByRole("button", { name: "Test", exact: true }).click();
    await expect(pat.getByTestId("figma-account-result")).toBeVisible();
  } finally {
    await request.put("/api/capability-servers", {
      data: { id: FIGMA_ID, config: orig },
    });
  }
});

test("G2: config-map placements save and list; custom, in-process and unknown servers", async ({
  page,
  request,
}) => {
  const servers = await (await request.get("/api/capability-servers")).json();
  const orig = servers.find(
    (s: { id: string }) => s.id === CONFIG_MAP_ID,
  )?.config;
  expect(orig).toBeTruthy();
  try {
    await page.goto(detail(CONFIG_MAP_ID));
    await page.getByTestId("capability-edit-settings-btn").click();
    const editor = page.getByTestId("config-map-editor");
    await editor.getByRole("button", { name: "Add config map" }).click();
    const entry = editor.getByTestId("config-map-entry-row").last();
    await entry.locator('input[id^="cm-name-"]').fill("e2e-cm");
    const file = entry.getByTestId("config-map-file-row").first();
    await file.locator('input[id^="cm-key-"]').fill("hello.txt");
    await file.locator("textarea").fill("ok-from-e2e");
    const put = page.waitForResponse(
      (r) =>
        r.url().endsWith("/api/capability-servers") &&
        r.request().method() === "PUT",
    );
    await page.getByTestId("save-server-settings-btn").click();
    expect((await put).ok()).toBeTruthy();
    await expect(page.getByTestId("config-map-list")).toContainText("e2e-cm");
    await expect(page.getByTestId("config-map-list")).toContainText(
      "hello.txt",
    );
  } finally {
    await request.put("/api/capability-servers", {
      data: { id: CONFIG_MAP_ID, config: orig },
    });
  }

  // The demo's own custom server: connection, edit form, Test.
  await page.goto(detail(CLOUD_ID));
  await expect(page.getByTestId("capability-server-connection")).toBeVisible();
  await page.getByTestId("capability-edit-connection-btn").click();
  await expect(page.getByTestId("capability-server-edit")).toBeVisible();
  await page.getByTestId("capability-server-test-btn").click();
  await expect(page.getByTestId("capability-test-result")).toBeVisible({
    timeout: 15000,
  });

  // An unknown id says so and links back, rather than falling to the Inbox.
  await page.goto(detail("https://no-such.example/missing"));
  await expect(page.getByTestId("capability-server-detail-page")).toContainText(
    "https://no-such.example/missing",
  );
  await expect(page.getByTestId("capability-servers-back")).toBeVisible();
});

test("G2 and G1 fit a phone and dark theme with no serious axe findings @phone @dark", async ({
  page,
  axe,
}) => {
  await page.goto(detail(GCP_ID));
  await expect(page.getByTestId("capability-server-methods")).toBeVisible();
  await page.getByTestId("capability-edit-settings-btn").click();
  await expect(page.getByTestId("capability-server-config")).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    ),
  ).toBeLessThanOrEqual(0);
  expect(await axe()).toEqual([]);

  await page.goto(detail(HOST_SCRIPT_ID));
  await expect(page.getByTestId("host-script-list").last()).toBeVisible();
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    ),
  ).toBeLessThanOrEqual(0);
  expect(await axe()).toEqual([]);

  await openNewTaskGrants(page);
  expect(await axe()).toEqual([]);
});
