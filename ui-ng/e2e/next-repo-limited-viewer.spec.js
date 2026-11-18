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

// A role holding its read scopes only on some repositories is refused
// every deployment-wide read (pkg/ui access_roles.go authorizeRoleCall),
// so /next/ must not fire them: before this guard every page logged a
// 403 for the shell's notifications and approvals poll, and Settings,
// Tasks, Sessions and the Director drawer more
// (docs/test-runs/2026-11-14-next-heavy-data.md). The demo server answers
// as the access code, so the role is put on /api/config and the spec
// asserts on the requests the UI makes — including cold loads and cached
// reloads before /api/config has settled.
const SCOPES = [
  "tasks:read",
  "settings:read",
  "secrets:read",
  "deployment:read",
  "capabilities:read",
  "sandboxes:read",
  "boards:read",
  "sessions:read",
  "repos:read",
];
const DEPLOYMENT_WIDE = [
  "/api/notifications",
  "/api/local-script-approvals",
  "/api/settings",
  "/api/deployment",
  "/api/agent-keys",
  "/api/usage",
  "/api/boards",
  "/api/agent-models",
  "/api/console-sessions",
  "/api/persistent-sandboxes",
  "/api/hostds",
];

function routeRepoLimitedConfig(page) {
  return page.route(
    (url) => url.pathname === "/api/config",
    async (route) => {
      const res = await route.fetch();
      const body = await res.json();
      body.access = {
        role: "widgets/viewer",
        roles: ["widgets/viewer"],
        full: false,
        scopes: SCOPES,
        repoLimits: Object.fromEntries(
          SCOPES.map((s) => [s, ["acme/widgets"]]),
        ),
      };
      await route.fulfill({ response: res, json: body });
    },
  );
}

function recordDeploymentWideGets(page) {
  const fired = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (r.method() === "GET" && DEPLOYMENT_WIDE.includes(u.pathname)) {
      fired.push(`${u.pathname}${u.search}`);
    }
  });
  return fired;
}

test("a repo-limited viewer's pages fire no deployment-wide reads it would be refused, including cold reload of /next/tasks", async ({
  page,
  axe,
}) => {
  test.setTimeout(120_000);
  await routeRepoLimitedConfig(page);
  const fired = recordDeploymentWideGets(page);

  // Start directly on /next/tasks (cold load before any /api/config is cached),
  // then reload with cached bundles to cover the fast-reload race on loadBoards().
  await page.goto("/next/tasks");
  await page.waitForLoadState("networkidle").catch(() => {});
  await page.waitForTimeout(500);
  await page.reload();
  await page.waitForLoadState("networkidle").catch(() => {});
  await page.waitForTimeout(500);

  for (const p of [
    "/next/inbox",
    "/next/workstreams",
    "/next/telemetry",
    "/next/settings",
    "/next/settings/models",
    "/next/settings/sandboxes",
    "/next/settings/hostds",
    "/next/context",
  ]) {
    await page.goto(p);
    await page.waitForLoadState("networkidle").catch(() => {});
    await page.waitForTimeout(500);
    if (p === "/next/settings/hostds") {
      const card = page.getByTestId("card-hostds-list");
      const banner = page.getByTestId("hostds-restricted");
      await expect(banner).toContainText(
        "holds capabilities:read only on acme/widgets, and this reads the whole deployment",
      );
      const cardBox = await card.boundingBox();
      const bannerBox = await banner.boundingBox();
      expect(cardBox).not.toBeNull();
      expect(bannerBox).not.toBeNull();
      expect(bannerBox.x).toBeGreaterThanOrEqual(cardBox.x);
      expect(bannerBox.x + bannerBox.width).toBeLessThanOrEqual(
        cardBox.x + cardBox.width + 1,
      );
      expect(await axe()).toEqual([]);

      await page.setViewportSize({ width: 390, height: 844 });
      const phoneCardBox = await card.boundingBox();
      const phoneBannerBox = await banner.boundingBox();
      expect(phoneCardBox).not.toBeNull();
      expect(phoneBannerBox).not.toBeNull();
      expect(phoneBannerBox.x + phoneBannerBox.width).toBeLessThanOrEqual(
        phoneCardBox.x + phoneCardBox.width + 1,
      );
      const docOverflow = await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      );
      expect(docOverflow).toBeLessThanOrEqual(0);
      await page.setViewportSize({ width: 1440, height: 900 });
    }
  }
  // The Director drawer reads sessions and models.
  await page.getByTestId("director-trigger").click();
  await page.waitForTimeout(1_000);
  await expect(page.getByTestId("director-drawer")).toBeVisible();

  expect(fired).toEqual([]);
});

test("cold load and reload of /next/sessions fire no deployment-wide reads for a repo-limited viewer", async ({
  page,
}) => {
  await routeRepoLimitedConfig(page);
  const fired = recordDeploymentWideGets(page);

  await page.goto("/next/sessions");
  await page.waitForLoadState("networkidle").catch(() => {});
  await page.waitForTimeout(500);
  await expect(page.getByText("No sessions yet.")).toBeVisible();

  await page.reload();
  await page.waitForLoadState("networkidle").catch(() => {});
  await page.waitForTimeout(500);
  await expect(page.getByText("No sessions yet.")).toBeVisible();

  expect(fired).toEqual([]);
});

test("sessions skips GET /api/agent-models when a limited role lacks settings:read", async ({
  page,
}) => {
  await page.route(
    (url) => url.pathname === "/api/config",
    async (route) => {
      const res = await route.fetch();
      const body = await res.json();
      body.access = {
        role: "sessions-only",
        roles: ["sessions-only"],
        full: false,
        scopes: ["tasks:read", "sessions:read"],
        repoLimits: {},
      };
      await route.fulfill({ response: res, json: body });
    },
  );
  const agentModelGets = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (r.method() === "GET" && u.pathname === "/api/agent-models") {
      agentModelGets.push(u.pathname);
    }
  });

  await page.goto("/next/sessions");
  await page.waitForLoadState("networkidle").catch(() => {});
  await page.waitForTimeout(500);

  expect(agentModelGets).toEqual([]);
});

test("/next/tasks fetches GET /api/boards only once across repeated /api/config refreshes", async ({
  page,
}) => {
  let configGets = 0;
  let boardGets = 0;
  page.on("request", (r) => {
    if (r.method() !== "GET") return;
    const { pathname } = new URL(r.url());
    if (pathname === "/api/config") configGets += 1;
    if (pathname === "/api/boards") boardGets += 1;
  });

  await page.goto("/next/tasks");
  await page.waitForLoadState("networkidle").catch(() => {});
  await expect.poll(() => boardGets).toBe(1);
  const initialConfigGets = configGets;

  // Wait past AppComponent's 5 s NavigationEnd refresh cooldown and switch
  // tabs within /next/tasks twice (>5 s apart) via Angular's router links so
  // refreshDispatchStatus() re-reads /api/config and calls access.setConfig(cfg)
  // twice while TasksStore stays started.
  const tabNav = page.getByRole("navigation", { name: "Task views" });
  await page.waitForTimeout(5_200);
  await tabNav.getByRole("link", { name: /^All\b/ }).click();
  await expect.poll(() => configGets).toBeGreaterThan(initialConfigGets);
  const midConfigGets = configGets;

  await page.waitForTimeout(5_200);
  await tabNav.getByRole("link", { name: /^Running\b/ }).click();
  await expect.poll(() => configGets).toBeGreaterThan(midConfigGets);
  await page.waitForTimeout(300);

  expect(boardGets).toBe(1);
});
