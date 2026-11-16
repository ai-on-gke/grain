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

// Settings -> Capabilities as the classic page had it (G3/G4 of the
// 2026-11-14 connections & infrastructure walkthrough): built-ins listed
// first, state chips in host wording with (i) popovers a click or a key
// opens, a server's own page at /settings/capabilities/<id> rather than
// the Inbox, its retired names, a Remove that lists what still grants it,
// and the grant picker's capability-policy verdicts.

import { test, expect } from "./fixtures.js";

const API = "https://grain.dev/capabilities/api";
const FIGMA = "https://grain.dev/capabilities/figma";
const CLOUD = "https://capabilities.demo.grain.example/cloud";

const serverPage = (id) =>
  `/next/settings/capabilities/${encodeURIComponent(id)}`;

test("lists the in-process built-ins first, then the built-ins, then the deployment's own", async ({
  page,
}) => {
  await page.goto("/next/settings/capabilities");
  const rows = page.locator('[data-testid^="cap-server-row-"]');
  await expect(rows.first()).toBeVisible();
  const kinds = await rows.evaluateAll((els) =>
    els.map((e) => e.getAttribute("data-kind")),
  );
  const rank = { "in-process": 0, "built-in": 1, custom: 2 };
  const ranks = kinds.map((k) => rank[k]);
  expect(ranks).toEqual([...ranks].sort((a, b) => a - b));
  expect(kinds[0]).toBe("in-process");
  expect(kinds).toContain("built-in");
  expect(kinds.at(-1)).toBe("custom");
});

test("state chips say host, not hostd, and their (i) opens on click, Enter and Space", async ({
  page,
}) => {
  await page.route("**/api/capability-servers", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const rows = await res.json();
    await route.fulfill({
      response: res,
      json: rows.map((s) =>
        s.id === CLOUD
          ? {
              ...s,
              config: { ...s.config, command: "cloud-mcp", run: "host" },
              status: { ...s.status, state: "needs-hostd:mac-mini" },
            }
          : s,
      ),
    });
  });
  await page.goto("/next/settings/capabilities");
  await expect(page.getByTestId(`cap-server-state-${FIGMA}`)).toHaveText(
    "needs-configuring",
  );
  const placed = page.getByTestId(`cap-server-state-${CLOUD}`);
  await expect(placed).toHaveText("needs host mac-mini");
  await expect(placed).not.toContainText("hostd");

  const info = page.getByTestId(`cap-server-state-info-${CLOUD}`);
  const panel = page.getByTestId(`cap-server-state-info-${CLOUD}-panel`);
  await expect(info).toHaveAttribute("aria-expanded", "false");
  await info.click();
  await expect(panel).toBeVisible();
  await expect(panel).toContainText("host this server is placed on");
  await expect(panel).not.toContainText("hostd");
  await expect(info).toHaveAttribute("aria-expanded", "true");
  // Escape closes it and gives focus back to its button.
  await page.keyboard.press("Escape");
  await expect(panel).toHaveCount(0);
  await expect(info).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(panel).toBeVisible();
  await page.keyboard.press("Enter");
  await expect(panel).toHaveCount(0);
  await page.keyboard.press("Space");
  await expect(panel).toBeVisible();
  // A click anywhere else closes it.
  await page.getByTestId("card-capability-servers").locator("h3").click();
  await expect(panel).toHaveCount(0);
});

test("a server's name opens its own page, which loads directly and goes back", async ({
  page,
}) => {
  await page.goto("/next/settings/capabilities");
  await page.getByTestId(`cap-server-link-${API}`).click();
  // The ID is one path segment, its slashes encoded.
  await expect
    .poll(() => decodeURIComponent(new URL(page.url()).pathname))
    .toBe(`/next/settings/capabilities/${API}`);
  expect(new URL(page.url()).pathname).toContain("%2F");
  const detail = page.getByTestId("capability-server-detail");
  await expect(detail).toBeVisible();
  await expect(page.getByTestId("capability-server-title")).toHaveText(
    "api server",
  );
  await expect(page.getByTestId("capability-server-kind")).toHaveText(
    "in-process",
  );
  await expect(page.getByTestId("capability-server-in-process")).toBeVisible();
  // An in-process server has nothing to disable, test or remove.
  await expect(page.getByTestId("capability-server-actions")).toHaveCount(0);
  await expect(
    page.getByTestId("capability-server-methods").locator("li").first(),
  ).toBeVisible();
  await expect(page.getByTestId("capability-server-usage-count")).toContainText(
    "granted by",
  );

  await page.getByTestId("capability-servers-back").click();
  await expect(page).toHaveURL(/\/next\/settings\/capabilities$/);
  await expect(page.getByTestId("card-capability-servers")).toBeVisible();

  // Loaded straight from the address bar it is the server's page, not
  // the Inbox the walkthrough found.
  await page.goto(serverPage(FIGMA));
  await expect(page.getByTestId("capability-server-title")).toHaveText(
    "figma server",
  );
  await expect(page.getByTestId("capability-server-state")).toHaveText(
    "needs-configuring",
  );
  await expect(page.getByTestId("settings-section-heading")).toBeVisible();

  await page.goto(serverPage("https://nothing.example/none"));
  await expect(page.getByTestId("capability-server-unknown")).toContainText(
    "https://nothing.example/none",
  );
});

test("a server's page names its retired IDs as deprecated", async ({
  page,
}) => {
  await page.route("**/api/capability-servers", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const res = await route.fetch();
    const rows = await res.json();
    await route.fulfill({
      response: res,
      json: rows.map((s) =>
        s.id === CLOUD
          ? {
              ...s,
              status: {
                ...s.status,
                aliases: ["https://capabilities.demo.grain.example/old-cloud"],
              },
            }
          : s,
      ),
    });
  });
  await page.goto(serverPage(CLOUD));
  const notice = page.getByTestId("capability-server-aliases");
  await expect(notice).toContainText("old-cloud");
  await expect(notice).toContainText("deprecated");
  await expect(notice).toContainText(CLOUD);
});

test("Remove on a server's page lists what still grants it and asks first", async ({
  page,
  request,
}) => {
  const id = `https://capabilities.e2e.example/throwaway-${Date.now().toString(36)}`;
  const made = await request.put("/api/capability-servers", {
    data: { id, config: { serverUrl: "http://127.0.0.1:9/mcp" } },
  });
  expect(made.ok()).toBeTruthy();
  await page.route("**/api/capability-servers/usage", async (route) => {
    const res = await route.fetch();
    const rows = await res.json();
    await route.fulfill({
      response: res,
      json: [
        ...rows,
        {
          id,
          holders: [
            {
              kind: "template",
              id: "nightly",
              name: "Nightly sweep",
              repo: "acme/widgets",
              grant: `${id}#token`,
            },
          ],
        },
      ],
    });
  });
  try {
    await page.goto(serverPage(id));
    await expect(page.getByTestId("capability-server-kind")).toHaveText(
      "custom",
    );
    await expect(page.getByTestId("capability-server-usage")).toContainText(
      "Template “Nightly sweep” on acme/widgets",
    );
    await page.getByTestId("capability-server-remove").click();
    const dialog = page.getByRole("dialog");
    await expect(dialog).toContainText("1 grant still names");
    await expect(dialog.getByTestId("confirm-items")).toContainText(
      "Nightly sweep",
    );
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(page.getByTestId("capability-server-detail")).toBeVisible();

    await page.getByTestId("capability-server-remove").click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Remove" })
      .click();
    await expect(page).toHaveURL(/\/next\/settings\/capabilities$/);
    await expect(page.getByTestId(`cap-server-row-${id}`)).toHaveCount(0);
  } finally {
    await request.post("/api/capability-servers/remove", { data: { id } });
  }
});

test("a server's page and its popovers fit the screen @phone @dark", async ({
  page,
}) => {
  await page.goto(serverPage("https://grain.dev/capabilities/github"));
  const card = page.getByTestId("capability-server-detail");
  await expect(card).toBeVisible();
  const box = await card.boundingBox();
  for (const id of [
    "capability-server-title",
    "capability-server-chips",
    "capability-server-methods",
    "capability-server-usage",
  ]) {
    const b = await page.getByTestId(id).boundingBox();
    expect(b.x, id).toBeGreaterThanOrEqual(box.x - 0.5);
    expect(b.x + b.width, id).toBeLessThanOrEqual(box.x + box.width + 0.5);
  }
  const info = page.getByTestId("capability-server-summary-info");
  await info.click();
  const panel = await page
    .getByTestId("capability-server-summary-info-panel")
    .boundingBox();
  const vw = page.viewportSize().width;
  expect(panel.x).toBeGreaterThanOrEqual(0);
  expect(panel.x + panel.width).toBeLessThanOrEqual(vw);
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
  await page.screenshot({ path: test.info().outputPath("server-page.png") });
});

test("the server list fits a phone without sideways scroll @phone @dark", async ({
  page,
  axe,
}) => {
  await page.goto("/next/settings/capabilities");
  await expect(page.getByTestId(`cap-server-row-${API}`)).toBeVisible();
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
  expect(await axe()).toEqual([]);
});

test("the New task grant picker shows the capability policy's verdicts and links the policy", async ({
  page,
}) => {
  const DENIED = "https://grain.dev/capabilities/gcp#gcp-key";
  const MAYBE = "https://grain.dev/capabilities/github#read-issues";
  const REFUSED = "https://grain.dev/capabilities/grain#sql";
  await page.route("**/api/capability-methods**", (route) =>
    route.fulfill({
      json: {
        items: [
          {
            ref: MAYBE,
            capability: "https://grain.dev/capabilities/github",
            method: "read-issues",
            policy: { task: "may_be_refused" },
          },
          {
            ref: DENIED,
            capability: "https://grain.dev/capabilities/gcp",
            method: "gcp-key",
            policy: { task: "denied" },
          },
          {
            ref: REFUSED,
            capability: "https://grain.dev/capabilities/grain",
            method: "sql",
            policyRefusal:
              "the capability policy denies grain#sql to acme/widgets",
          },
        ],
      },
    }),
  );
  await page.goto("/next/inbox");
  await page.getByTestId("rail-new-task").click();
  await expect(page.getByTestId("new-task-sheet")).toBeVisible();
  await page.getByTestId("caps-options").click();
  await page.getByTestId("nt-grants-add-open").click();
  await page.getByTestId("nt-grants-mode-method").click();
  // The sheet has a repository chosen, so the listing judged this one
  // run: the method the policy refuses it is listed disabled with the
  // policy's reason, and the builder links the policy.
  await page.getByTestId("nt-grants-method-ref").click();
  const refused = page.getByTestId(`nt-grants-method-ref-opt-${REFUSED}`);
  await expect(refused).toHaveAttribute("aria-disabled", "true");
  await expect(refused).toContainText("denies grain#sql");
  await page.keyboard.press("Escape");
  const note = page.getByTestId("nt-grants-method-policy");
  await expect(note).toContainText("1 method is refused or may be refused");
  const link = note.getByRole("link", { name: "Settings → Capabilities" });
  await expect(link).toHaveAttribute(
    "href",
    /\/settings\/capabilities#capability-policy$/,
  );

  await link.click();
  await expect(page).toHaveURL(
    /\/next\/settings\/capabilities#capability-policy$/,
  );
  await expect(page.getByTestId("card-capability-policy")).toBeVisible();
});
