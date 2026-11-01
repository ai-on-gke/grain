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

// App.jsx now keeps the address bar in sync with which sub-page is
// showing (bwsalmon/agents#548) via plain history.pushState/popstate
// rather than a routing library, and pkg/ui/server.go's own spaHandler
// falls back to index.html for any path that isn't a real static asset
// so a direct hit on one of these URLs -- not just a click starting
// from "/" -- gets a real answer. Both halves only matter together
// against a real server: jsdom component tests never issue a real HTTP
// request for "/repos" the way a bookmark or a page reload does, so
// this suite (see tasks.spec.js's own doc comment for why it's the one
// exception to "jsdom, not a browser") is the only place a regression
// in either half would show up.

test("loads directly into each sidebar sub-page from its URL", async ({
  page,
}) => {
  await page.goto("/repos");
  await expect(page.locator(".repo-list")).toBeVisible();

  await page.goto("/schedules");
  await expect(page.getByRole("heading", { name: "Processes" })).toBeVisible();

  await page.goto("/templates");
  await expect(page.getByRole("heading", { name: "Processes" })).toBeVisible();

  await page.goto("/suites");
  await expect(page.getByRole("heading", { name: "Processes" })).toBeVisible();

  // grain's own CI, a section of the rail since grain/task-354. Located
  // by the pane's own class rather than by a heading, for the reason
  // /repos above is: what it says inside depends on the deployment --
  // this one runs no CI engine, so the pane says so -- and the pane
  // being served at all is what this test is about.
  await page.goto("/ci");
  await expect(page.locator(".ci-panel")).toBeVisible();

  // Logs remains a System diagnostic, while sandboxes is once again a
  // first-class destination. The retired /debug path still falls back.
  await page.goto("/logs");
  await expect(page.locator(".task-row").first()).toBeVisible();

  await page.goto("/sandboxes");
  await expect(page.locator(".sandbox-health-panel")).toBeVisible();
  // The plain demo wires no sandbox backend, so this is the pane's own
  // "not available" state, read off a server that really has none.
  await expect(
    page
      .locator(".sandbox-health-panel")
      .getByText(/not available/i)
      .first(),
  ).toBeVisible();

  // The caches page, combining the proxy and actions caches. Its
  // heading is on screen whether or not this deployment runs a cache,
  // which is what makes it the thing to assert here.
  await page.goto("/caches");
  await expect(page.getByRole("heading", { name: "Caches" })).toBeVisible();

  await page.goto("/debug");
  await expect(page.locator(".task-row").first()).toBeVisible();

  // The old System overlay's settings tabs are Settings sections now,
  // and /admin is Settings → General (design/design/settings-panel.design.md §3.5).
  await page.goto("/admin");
  await expect(
    page.getByRole("heading", { name: "General", level: 1 }),
  ).toBeVisible();
  await expect(page).toHaveURL(/\/settings\/general$/);

  await page.goto("/system");
  await expect(
    page.getByRole("heading", { name: "System", exact: true }),
  ).toBeVisible();

  // Metrics is a System tab, and its old address still reaches it.
  await page.goto("/metrics");
  await expect(page.getByRole("heading", { name: "Metrics" })).toBeVisible();
  await expect(page).toHaveURL(/\/system\/metrics$/);
});

test("keeps a standing session through direct links, reload, Back, and Forward", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.locator(".task-row").first()).toBeVisible();

  // Reach it through the Sessions panel. The session id belongs in the
  // address so a deployment with more than one reopens the one clicked.
  await page.getByRole("button", { name: /^Sessions/ }).click();
  await expect(page).toHaveURL(/\/sessions$/);
  await page.locator(".template-row", { hasText: "Director" }).first().click();
  await expect(page).toHaveURL(/\/session\/director$/);
  await expect(page.locator(".console-session-page")).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Director", exact: true }),
  ).toBeVisible();

  // A hard request for the URL must restore the same pane, not normalize
  // the unrecognized path back to the task list.
  await page.reload();
  await expect(page).toHaveURL(/\/session\/director$/);
  await expect(page.locator(".console-session-page")).toBeVisible();

  await page.getByRole("button", { name: /^Tasks/ }).click();
  await expect(page).toHaveURL(/\/(?:\?.*)?$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/session\/director$/);
  await expect(page.locator(".console-session-page")).toBeVisible();
  await page.goForward();
  await expect(page).toHaveURL(/\/(?:\?.*)?$/);
  await expect(page.locator(".task-list")).toBeVisible();
});

// The deployment's connections are Settings sections, and the open
// section is part of the path (grain/task-1359), so a reload, a copied
// link or Back reopens the section that was showing.
test("keeps the open connections section of Settings through reload, a direct link, and Back", async ({
  page,
}) => {
  const open = (name) =>
    expect(page.getByRole("heading", { name, level: 1 })).toBeVisible();
  await page.goto("/admin");
  await open("General");

  await page.getByTestId("settings-nav-models").click();
  await expect(page).toHaveURL("/settings/models");
  await page.reload();
  await open("Models");

  await page.getByTestId("settings-nav-git").click();
  await expect(page).toHaveURL("/settings/git");
  await page.goBack();
  await expect(page).toHaveURL("/settings/models");
  await open("Models");

  await page.goto("/admin/workflow");
  await open("Tasks");
  await expect(page).toHaveURL("/settings/tasks");
});

// A repo's own page is two URL segments deep (grain/task-111), which is
// the first path here the SPA fallback has to answer for that could
// plausibly be mistaken for a real static asset path.
test("loads directly into a repo's own page, and its releases pane, from the URL", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: /^Repos/ }).click();
  const repoName = await page.locator(".repo-list-name").first().innerText();

  await page.goto(`/repos/${repoName}`);
  await expect(page.getByRole("heading", { name: repoName })).toBeVisible();

  await page.goto(`/repos/${repoName}/releases`);
  await expect(
    page.getByRole("heading", { name: `${repoName} releases` }),
  ).toBeVisible();
});

// A pull request is a page under its repository, named by the two refs
// it compares (grain/task-375). App's URL synchronizer must leave that
// path alone, and a cold load of it must land on the comparison rather
// than on the repo page it sits under.
test("preserves a native pull request deep link", async ({ page }) => {
  const path = "/repos/acme/widgets/compare/main...grain%2Ftask-6";
  await page.goto(path);
  // Headed by the branch being compared, whatever the diff behind it
  // turns out to be: this deployment has no credential for acme/widgets,
  // so the comparison itself may well fail to load.
  await expect(
    page.getByRole("heading", { name: "grain/task-6" }),
  ).toBeVisible();
  await expect(page).toHaveURL(
    "/repos/acme/widgets/compare/main...grain/task-6",
  );

  await page.reload();
  await expect(page).toHaveURL(
    "/repos/acme/widgets/compare/main...grain/task-6",
  );
});

// The spelling links kept from before that page existed: the repo page
// with the two revisions in its query. It still opens the same
// comparison -- and `base` is also a legacy task-list filter, so an App
// that read it as one would rewrite the URL and drop `head` on the way.
test("opens a pull request written the old way, on its own path", async ({
  page,
}) => {
  await page.goto("/repos/acme/widgets?base=main&head=grain%2Ftask-6");
  await expect(
    page.getByRole("heading", { name: "grain/task-6" }),
  ).toBeVisible();
  await expect(page).toHaveURL(
    "/repos/acme/widgets/compare/main...grain/task-6",
  );
});

test("deep-links to a task's detail overlay, including after a hard reload", async ({
  page,
}) => {
  await page.goto("/");
  await page.locator(".task-row").first().click();
  const heading = page.locator(".detail-header h2");
  await expect(heading).toBeVisible();
  const title = await heading.innerText();
  await expect(page).toHaveURL(/\/tasks\/[^/]+$/);

  // The URL alone -- not a click -- is what has to reopen the same
  // task: a real GET for it has to fall back to index.html server-side,
  // then App.jsx's own mount effect has to notice /tasks/:id and fetch
  // that task's detail before anything else has rendered.
  await page.reload();
  await expect(
    page.locator(".detail-header h2", { hasText: title }),
  ).toBeVisible();
});

// grain/task-139: a schedule, a template and a suite each open the same
// pane a task does (grain/task-94), so each is deep-linkable the same
// way -- /schedules/:id, /templates/:id, /suites/:id. `grain demo` seeds
// none of the three, so these file their own through the very endpoints
// the frontend posts to, then ask for the item's URL cold: a real GET
// the SPA fallback has to answer with index.html, and an App.jsx that
// has only the path to tell it which pane to open.
async function seedOne(page, path, data) {
  const res = await page.request.post(path, { data });
  expect(res.ok(), `POST ${path}: ${res.status()}`).toBe(true);
  return res.json();
}

test("deep-links to a schedule, a template and a suite from their own URLs", async ({
  page,
}) => {
  const stamp = Date.now();
  const template = await seedOne(page, "/api/templates", {
    name: `E2E deep-link template ${stamp}`,
    title: "Bump dependencies",
  });
  const suite = await seedOne(page, "/api/suites", {
    name: `E2E deep-link suite ${stamp}`,
    templateIds: [template.id],
    mode: "until_clean",
    maxPasses: 3,
  });
  const schedule = await seedOne(page, "/api/schedules", {
    title: `E2E deep-link schedule ${stamp}`,
    repo: "acme/widgets",
    recurrence: { kind: "everyNHours", everyNHours: 24 },
  });

  await page.goto(`/templates/${template.id}`);
  await expect(
    page.getByRole("heading", { name: "Edit template" }),
  ).toBeVisible();
  await expect(page.getByLabel("Name")).toHaveValue(
    `E2E deep-link template ${stamp}`,
  );

  await page.goto(`/suites/${suite.id}`);
  await expect(page.getByRole("heading", { name: "Edit suite" })).toBeVisible();
  await expect(page.getByLabel("Name")).toHaveValue(
    `E2E deep-link suite ${stamp}`,
  );

  await page.goto(`/schedules/${schedule.id}`);
  await expect(
    page.getByRole("heading", { name: "Edit schedule" }),
  ).toBeVisible();
  await expect(page.getByLabel(/^Title/)).toHaveValue(
    `E2E deep-link schedule ${stamp}`,
  );

  // An id no schedule answers to is not a pane: the list shows, and the
  // address bar is corrected back to it rather than left naming
  // something that isn't open.
  await page.goto("/schedules/sched-does-not-exist");
  await expect(page.getByRole("heading", { name: "Processes" })).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Edit schedule" }),
  ).toHaveCount(0);
  await expect(page).toHaveURL(/\/processes(\?kind=schedule)?$/);
});

test("opens a template into its own URL, and back closes the pane rather than the page", async ({
  page,
}) => {
  const name = `E2E back-out template ${Date.now()}`;
  const template = await seedOne(page, "/api/templates", {
    name,
    title: "Bump dependencies",
  });

  await page.goto("/templates");
  await page.locator(".process-row-template", { hasText: name }).click();
  await expect(
    page.getByRole("heading", { name: "Edit template" }),
  ).toBeVisible();
  await expect(page).toHaveURL(
    new RegExp(`/processes/templates/${template.id}$`),
  );

  // Back used to leave the templates page altogether, since the open
  // template was state nothing had pushed a history entry for.
  await page.goBack();
  await expect(page).toHaveURL(/\/processes(\?kind=template)?$/);
  await expect(
    page.getByRole("heading", { name: "Edit template" }),
  ).toHaveCount(0);
  await expect(
    page.locator(".process-row-template", { hasText: name }),
  ).toBeVisible();

  // ...and forward opens the same pane again.
  await page.goForward();
  await expect(page).toHaveURL(
    new RegExp(`/processes/templates/${template.id}$`),
  );
  await expect(
    page.getByRole("heading", { name: "Edit template" }),
  ).toBeVisible();
});

// grain/task-317: how a task list is narrowed -- its search text, sort
// order and attribute filters -- rides in the query string, so a list
// narrowed down to what somebody actually cares about is a URL they can
// keep or send. The reload is the half only a real server can answer
// for: a GET for "/?q=..." has to reach index.html through the SPA
// fallback with its query intact, and App.jsx has to seed the toolbar
// from it before the list first renders.
test("keeps a narrowed task list in the URL, including after a hard reload", async ({
  page,
}) => {
  await page.goto("/");
  const rows = page.locator(".task-row");
  await expect(rows.first()).toBeVisible();
  const all = await rows.count();

  await page.getByPlaceholder("Search tasks…").fill("toolchain");
  // The search *and* the conditions the list opened narrowed by
  // (taskFilters.js's defaultFilters): a list nobody has touched lives
  // at "/", and from the first change onwards the query says the whole
  // of what the list is narrowed by, so the link reopens this list
  // rather than one narrowed by whatever the deployment's default is by
  // then.
  await expect(page).toHaveURL(
    /\/\?q=toolchain&f=state:none:deferred\|closed$/,
  );
  const narrowed = await rows.count();
  expect(narrowed).toBeGreaterThan(0);
  expect(narrowed).toBeLessThan(all);
  const title = await rows.first().locator(".task-title").innerText();

  await page.reload();
  await expect(page.getByPlaceholder("Search tasks…")).toHaveValue("toolchain");
  await expect(rows).toHaveCount(narrowed);
  await expect(rows.first().locator(".task-title")).toHaveText(title);
});

test("updates the URL when navigating the sidebar, and restores the previous page on back", async ({
  page,
}) => {
  await page.goto("/");
  await page.getByRole("button", { name: /^Repos/ }).click();
  await expect(page).toHaveURL(/\/repos$/);

  await page.getByRole("button", { name: /^Processes/ }).click();
  await expect(page).toHaveURL(/\/processes(\?kind=schedule)?$/);

  await page.goBack();
  await expect(page).toHaveURL(/\/repos$/);
  await expect(page.locator(".repo-list")).toBeVisible();

  await page.goBack();
  await expect(page).toHaveURL(/\/$/);
  await expect(page.locator(".task-row").first()).toBeVisible();
});

// grain/task-1432: a link to a schedule, template, suite or board that
// is not there -- deleted since it was copied, say -- lands on its list,
// says what it could not find, and leaves no history entry behind it:
// Back goes to the page the link was followed from rather than to the
// dead link, which would only correct itself all over again.
for (const [stale, list, message] of [
  ["/schedules/nope", /\/processes(\?kind=schedule)?$/, 'no schedule "nope"'],
  ["/templates/nope", /\/processes(\?kind=template)?$/, 'no template "nope"'],
  ["/suites/nope", /\/processes(\?kind=suite)?$/, 'no suite "nope"'],
  ["/boards/nope", /\/boards$/, 'no board "nope"'],
]) {
  test(`corrects a link to ${stale} in place, so Back leaves it`, async ({
    page,
  }) => {
    await page.goto("/inbox");
    await expect(page).toHaveURL(/\/inbox$/);

    await page.goto(stale);
    await expect(page).toHaveURL(list);
    await expect(page.getByText(message)).toBeVisible();

    await page.goBack();
    await expect(page).toHaveURL(/\/inbox$/);
  });
}

// The Home Screen icon is the one asset served to something that is not
// this app: iOS fetches it when a page is added to the Home Screen, over
// HTTP, from the path index.html names. Two things can go wrong that
// only a real request against a real build can see. The file has to
// survive `npm run build` into the served tree at the root path -- and
// if it does not, spaHandler answers with index.html rather than a 404,
// so the failure arrives as a 200 that iOS reads as "no icon" and
// replaces with a screenshot of the page. And the tag has to name the
// path the file is actually at, which no jsdom test can check because
// there is nothing behind the URL there.
test("serves the Home Screen icon at the path index.html names", async ({
  page,
  request,
}) => {
  await page.goto("/");
  const href = await page
    .locator('link[rel="apple-touch-icon"]')
    .getAttribute("href");
  // The root path is also where iOS looks with no tag at all, so naming
  // it here keeps the two answers the same file.
  expect(href).toBe("/apple-touch-icon.png");

  const response = await request.get(href);
  expect(response.status()).toBe(200);
  expect(response.headers()["content-type"]).toBe("image/png");

  // A real PNG rather than the SPA fallback's HTML under a .png name --
  // the signature, then the dimensions out of the IHDR that follows it.
  // 180 square is the @3x tile iOS asks for.
  const body = await response.body();
  expect([...body.subarray(0, 8)]).toEqual([
    0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
  ]);
  expect([body.readUInt32BE(16), body.readUInt32BE(20)]).toEqual([180, 180]);
});
