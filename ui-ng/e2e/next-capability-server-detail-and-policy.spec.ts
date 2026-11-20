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
//   G2 -- what the per-server page (next-capability-server-page.spec.js
//         covers the rest) adds for the built-in servers: host-script's
//         Test runner and config-map's file placements, and a pasted,
//         unencoded server URL opening the page.
// Every change is a throwaway entry and is put back in finally.

test.use({ demoArgs: ["-secrets-store"] });

const GCP_ID = "https://grain.dev/capabilities/gcp";
const GCP_KEY_REF = `${GCP_ID}#gcp-key`;
const HOST_SCRIPT_ID = "https://grain.dev/capabilities/host-script";
const CONFIG_MAP_ID = "https://grain.dev/capabilities/config-map";

const detail = (id: string) =>
  `/next/settings/capabilities/${encodeURIComponent(id)}`;

async function noOverflow(page: Page) {
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth - window.innerWidth,
    ),
  ).toBeLessThanOrEqual(0);
}

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
    await page.keyboard.press("Escape");

    // Named by hand, it is noted with a link back to the policy, and
    // refused rather than attached.
    await page.getByTestId("nt-grants-mode-raw").click();
    await page.getByTestId("nt-grants-raw-input").fill(`${GCP_KEY_REF}={}`);
    const note = page
      .getByTestId("nt-grants-policy-notes")
      .locator(`li[data-policy-grant^="${GCP_KEY_REF}"]`);
    await expect(note).toHaveAttribute("data-policy-verdict", "denied");
    await expect(note.getByRole("link")).toHaveAttribute(
      "href",
      /\/settings\/capabilities#capability-policy$/,
    );
    await page.getByTestId("nt-grants-attach").click();
    await expect(page.getByTestId("nt-grants-error")).toContainText(
      /capability policy/i,
    );
  } finally {
    await request.put("/api/capability-policy", {
      data: before.policy ? { policy: before.policy } : before,
    });
  }
});
test("G2: a pasted, unencoded server URL opens its page and survives a reload", async ({
  page,
}) => {
  await page.goto(`/next/settings/capabilities/${GCP_ID}`);
  await expect(page.getByTestId("capability-server-page")).toBeVisible();
  await expect(page.getByTestId("capability-server-methods")).toContainText(
    "gcp-key",
  );
  await page.reload();
  await expect(page.getByTestId("capability-server-methods")).toContainText(
    "gcp-key",
  );
});

test("G2: host-script's Scripts card runs the demo's ops script @phone @dark", async ({
  page,
  axe,
}) => {
  await page.goto(detail(HOST_SCRIPT_ID));
  const card = page.getByTestId("capability-server-builtin");
  await expect(card).toContainText("Scripts");
  const list = card.getByTestId("host-script-list");
  await expect(list).toContainText("ops");
  await noOverflow(page);
  expect(await axe()).toEqual([]);

  await list.getByRole("button", { name: "Test", exact: true }).click();
  const panel = page.getByTestId("host-script-test");
  await panel.getByRole("button", { name: /Add argument/ }).click();
  await panel.locator('[data-host-script-arg="0"]').fill("hello-e2e");
  await panel.getByRole("button", { name: "Run ops" }).click();
  await expect(page.getByTestId("host-script-exit")).toContainText("0");
  await expect(page.getByTestId("host-script-stdout")).toContainText(
    "hello-e2e",
  );
  await noOverflow(page);
});

test("G2: config-map's File placements card lists each map's files and directory", async ({
  page,
  request,
}) => {
  const servers = await (await request.get("/api/capability-servers")).json();
  const orig = servers.find(
    (s: { id: string }) => s.id === CONFIG_MAP_ID,
  )?.config;
  expect(orig).toBeTruthy();
  try {
    const put = await request.put("/api/capability-servers", {
      data: {
        id: CONFIG_MAP_ID,
        config: {
          ...orig,
          config: {
            ...(orig.config ?? {}),
            config_maps: [
              {
                name: "e2e-cm",
                dir: "/home/debian/e2e-cm",
                data: { "hello.txt": { content: "ok-from-e2e" } },
              },
            ],
          },
        },
      },
    });
    expect(put.ok(), await put.text()).toBeTruthy();
    await page.goto(detail(CONFIG_MAP_ID));
    const list = page.getByTestId("config-map-list");
    await expect(list).toContainText("e2e-cm");
    await expect(list).toContainText("/home/debian/e2e-cm");
    await expect(list).toContainText("hello.txt");
  } finally {
    await request.put("/api/capability-servers", {
      data: { id: CONFIG_MAP_ID, config: orig },
    });
  }
});

test("G1: New task's grants picker has no serious axe findings @phone @dark", async ({
  page,
  axe,
}) => {
  await openNewTaskGrants(page);
  expect(await axe()).toEqual([]);
});
