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
// Tasks and the Director drawer more
// (docs/test-runs/2026-11-14-next-heavy-data.md). The demo server answers
// as the access code, so the role is put on /api/config and the spec
// asserts on the requests the UI makes.
const SCOPES = [
  "tasks:read",
  "settings:read",
  "secrets:read",
  "deployment:read",
  "capabilities:read",
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
];

test("a repo-limited viewer's pages fire no deployment-wide reads it would be refused", async ({
  page,
}) => {
  test.setTimeout(120_000);
  await page.route(
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
  const fired = [];
  page.on("request", (r) => {
    const u = new URL(r.url());
    if (r.method() === "GET" && DEPLOYMENT_WIDE.includes(u.pathname)) {
      fired.push(`${u.pathname}${u.search}`);
    }
  });

  for (const p of [
    "/next/inbox",
    "/next/workstreams",
    "/next/tasks",
    "/next/telemetry",
    "/next/settings",
    "/next/settings/models",
    "/next/context",
  ]) {
    await page.goto(p);
    await page.waitForLoadState("networkidle").catch(() => {});
    await page.waitForTimeout(500);
  }
  // The Director drawer reads sessions and models.
  await page.getByTestId("director-trigger").click();
  await page.waitForTimeout(1_000);
  await expect(page.getByTestId("director-drawer")).toBeVisible();

  expect(fired).toEqual([]);
});
