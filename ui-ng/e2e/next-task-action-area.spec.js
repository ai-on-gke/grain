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

// The action area (shared/task-action-area.component.ts): one place per
// task that says what is needed, lists its merge gates and offers every
// action its state allows -- the same on the task page and in the Inbox
// inspector. The demo seeds no task waiting on Submit behind gates, so a
// real task's list row and detail are answered with each combination on
// top (as next-human-review-findings-hold.spec.js does).

async function fileTask(baseURL, code, title) {
  const res = await fetch(`${baseURL}/api/tasks`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ title, approved: true }),
  });
  if (!res.ok) throw new Error(`filing: ${res.status} ${await res.text()}`);
  return (await res.json()).id;
}

const CI = (state, detail = "") => ({ id: "ci", label: "CI", state, detail });
const SECURITY = (state) => ({
  id: "security-review",
  label: "Security review",
  state,
  ...(state !== "passed" ? { reviewTask: "9002" } : {}),
});
const REVIEW_FINDINGS = {
  id: "review",
  label: "Code review",
  state: "findings",
  reviewTask: "9001",
  overridable: true,
  acceptable: true,
};
const APPROVAL = { id: "approval", label: "Approval", state: "needed" };
const FINDING = {
  gate: "review",
  reviewTask: "9001",
  summary: "Close leaks the lease when the context is cancelled",
  at: "2026-11-15T08:00:00Z",
};

const CASES = {
  "findings plus CI running": {
    mergeGates: [CI("running"), SECURITY("passed"), REVIEW_FINDINGS, APPROVAL],
    reviewFindings: [FINDING],
  },
  "findings only": {
    mergeGates: [CI("passed"), SECURITY("passed"), REVIEW_FINDINGS, APPROVAL],
    reviewFindings: [FINDING],
  },
  "CI failed": {
    mergeGates: [CI("failing", "Tests failed"), APPROVAL],
  },
  "security hold": {
    mergeGates: [CI("passed"), SECURITY("running"), APPROVAL],
    humanReviewHold: [
      {
        source: "requested",
        category: "security",
        reason: "Touches the token refresh path",
      },
    ],
  },
  "auto-merge turned off after a review push": {
    autoMerge: false,
    mergeGates: [CI("running"), REVIEW_FINDINGS, APPROVAL],
    reviewFindings: [FINDING],
    transitions: [
      { state: "auto_merge_on", at: "2026-11-15T07:00:00Z" },
      {
        state: "auto_merge_off",
        at: "2026-11-15T08:00:00Z",
        reason: "the code review pushed fixes that need a fresh review",
      },
    ],
  },
};

// overlay answers the task's detail, and its row in the task list, as
// waiting on Submit with the given gates.
async function overlay(page, id, extra) {
  const shape = {
    state: "awaiting_submit",
    reason: { reason: "awaiting_submit/not_auto_merge", control: "Submit" },
    pullRequest: "acme/widgets#42",
    autoMerge: false,
    ...extra,
  };
  await page.route(new RegExp(`/api/tasks/${id}(\\?.*)?$`), async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    const res = await route.fetch({ headers });
    const detail = await res.json();
    await route.fulfill({ response: res, json: { ...detail, ...shape } });
  });
  await page.route(
    (url) => url.pathname === "/api/tasks",
    async (route) => {
      if (route.request().method() !== "GET") return route.continue();
      const headers = { ...route.request().headers() };
      delete headers["if-none-match"];
      const res = await route.fetch({ headers });
      const body = await res.json();
      const patch = (t) =>
        String(t.id) === String(id)
          ? { ...t, ...shape, transitions: undefined }
          : t;
      const json = Array.isArray(body)
        ? body.map(patch)
        : { ...body, tasks: (body.tasks ?? []).map(patch) };
      await route.fulfill({ response: res, json });
    },
  );
}

async function actionIds(area) {
  return area
    .locator("[data-action]")
    .evaluateAll((els) => els.map((e) => e.getAttribute("data-action")).sort());
}

for (const [name, extra] of Object.entries(CASES)) {
  test(`${name}: says what's needed and offers a control that works`, async ({
    page,
    demoServer,
    accessCode,
  }) => {
    const id = await fileTask(demoServer, accessCode, `${name} ${Date.now()}`);
    await overlay(page, id, extra);
    await page.goto(`task/${id}`);

    const area = page.getByTestId("task-action-area");
    await expect(area).toBeVisible();
    const sentence = area.getByTestId("whats-needed");
    await expect(sentence).toContainText(/What's needed:\s*\S+/);

    // The gates are listed in the area, on the Conversation tab.
    await expect(page.getByTestId("task-tab-conversation")).toHaveAttribute(
      "aria-current",
      "page",
    );
    const gates = area.getByTestId("merge-gates-list");
    await expect(gates).toBeVisible();
    for (const g of extra.mergeGates) {
      await expect(gates.getByTestId(`merge-gate-${g.id}`)).toContainText(
        g.label,
      );
    }

    // At least one enabled control, or a Submit shown off with its reason.
    const enabled = area.locator("[data-action]:enabled");
    const submit = area.getByTestId("act-submit");
    if ((await enabled.count()) === 0) {
      await expect(submit).toBeDisabled();
      await expect(area.getByTestId("submit-off-reason")).not.toBeEmpty();
    } else {
      await expect(enabled.first()).toBeVisible();
    }

    // Nothing in the header offers them any more.
    await expect(
      page.locator(".task-header [data-testid=act-submit]"),
    ).toHaveCount(0);
    await expect(page.getByTestId("act-more")).toHaveCount(0);

    const findings = extra.mergeGates.some((g) => g.state === "findings");
    if (findings) {
      await expect(sentence).toContainText("Code review left 1 finding");
      await expect(area.getByTestId("gate-review-send-back")).toBeEnabled();
      await expect(
        area.getByTestId("gate-review-accept-findings"),
      ).toBeEnabled();
      await expect(submit).toHaveCount(0);
      await expect(gates.getByTestId("merge-gate-link-review")).toHaveAttribute(
        "href",
        /\/task\/9001$/,
      );
    }
    if (name === "findings plus CI running") {
      await expect(sentence).toContainText("CI is still running");
    }
    if (name === "CI failed") {
      await expect(sentence).toContainText("CI is failing");
      await expect(area.getByTestId("gate-ci-send-back")).toBeEnabled();
      await expect(submit).toHaveCount(0);
    }
    if (name === "security hold") {
      await expect(sentence).toContainText("Security review is still running");
      await expect(sentence).toContainText("human-review hold");
      await expect(page.getByTestId("human-review-hold")).toBeVisible();
    }
    if (name.startsWith("auto-merge")) {
      await expect(area.getByTestId("auto-merge-off")).toHaveText(
        "Auto-merge was turned off because the code review pushed fixes that need a fresh review. Submit again after Code review is settled.",
      );
    }
  });
}

test("Send findings back posts the findings and re-runs the task", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const id = await fileTask(demoServer, accessCode, `Send back ${Date.now()}`);
  await overlay(page, id, CASES["findings only"]);
  const posts = [];
  for (const path of ["comments", "run-now"]) {
    await page.route(`**/api/tasks/${id}/${path}`, async (route) => {
      if (route.request().method() !== "POST") return route.continue();
      posts.push({ path, body: route.request().postDataJSON() });
      await route.fulfill({ json: { id, state: "queued" } });
    });
  }
  await page.goto(`task/${id}`);
  await page.getByTestId("gate-review-send-back").click();
  await expect
    .poll(() => posts.map((p) => p.path))
    .toEqual(["comments", "run-now"]);
  expect(posts[0].body.body).toContain(FINDING.summary);
  expect(posts[0].body.body).toContain("review task #9001");
});

test("the task page and the Inbox inspector offer the same actions", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const title = `Same set ${Date.now()}`;
  const id = await fileTask(demoServer, accessCode, title);
  await overlay(page, id, CASES["findings plus CI running"]);

  await page.goto(`task/${id}`);
  const onPage = page.getByTestId("task-action-area");
  await expect(onPage.getByTestId("whats-needed")).toBeVisible();
  const pageSet = await actionIds(onPage);
  const pageSentence = await onPage.getByTestId("whats-needed").textContent();
  expect(pageSet.length).toBeGreaterThan(0);

  await page.goto("inbox");
  await page
    .getByTestId("queue-row")
    .filter({ hasText: title })
    .locator(".row-main")
    .click();
  const inspector = page.getByTestId("next-inbox-inspector");
  const inInbox = inspector.getByTestId("task-action-area");
  await expect(inInbox.getByTestId("merge-gates-list")).toBeVisible();
  await expect(inInbox.getByTestId("whats-needed")).toHaveText(pageSentence);
  expect(await actionIds(inInbox)).toEqual(pageSet);
  await expect(
    page.getByTestId("queue-row").filter({ hasText: title }),
  ).toContainText("Fix or accept 1 review finding");
});

test("a limited role holding tasks:write is not offered Accept findings or told to accept", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const title = `Limited findings ${Date.now()}`;
  const id = await fileTask(demoServer, accessCode, title);
  await overlay(page, id, CASES["findings plus CI running"]);
  await page.route(
    (url) => url.pathname === "/api/config",
    async (route) => {
      if (route.request().method() !== "GET") return route.continue();
      const headers = { ...route.request().headers() };
      delete headers["if-none-match"];
      const res = await route.fetch({ headers });
      const body = await res.json();
      await route.fulfill({
        response: res,
        json: {
          ...body,
          access: {
            role: "team/engineer",
            roles: ["team/engineer"],
            full: false,
            scopes: [
              "boards:read",
              "ci:read",
              "dispatch:write",
              "repos:read",
              "schedules:read",
              "settings:read",
              "suites:read",
              "tasks:read",
              "tasks:write",
              "templates:read",
            ],
          },
        },
      });
    },
  );

  await page.goto(`task/${id}`);
  const onPage = page.getByTestId("task-action-area");
  await expect(onPage.getByTestId("gate-review-send-back")).toBeVisible();
  await expect(onPage.getByTestId("gate-review-accept-findings")).toHaveCount(
    0,
  );
  await expect(onPage.getByTestId("act-force-merge")).toHaveCount(0);
  await expect(onPage.getByTestId("whats-needed")).toContainText(
    "Code review left 1 finding. Fix it (send it back to the agent) to continue. CI is still running.",
  );

  await page.goto("inbox");
  const inboxRow = page.getByTestId("queue-row").filter({ hasText: title });
  await expect(inboxRow).toContainText("Fix 1 review finding.");
  await expect(inboxRow).not.toContainText("or accept");
  await inboxRow.locator(".row-main").click();
  const inInbox = page
    .getByTestId("next-inbox-inspector")
    .getByTestId("task-action-area");
  await expect(inInbox.getByTestId("gate-review-send-back")).toBeVisible();
  await expect(inInbox.getByTestId("gate-review-accept-findings")).toHaveCount(
    0,
  );
  await expect(inInbox.getByTestId("whats-needed")).toContainText(
    "Code review left 1 finding. Fix it (send it back to the agent) to continue. CI is still running.",
  );

  await page.goto("tasks?tab=merging");
  await expect(page.getByTestId(`next-step-${id}`)).toHaveText(
    "Fix 1 review finding",
  );
});

test("the action area fits its column, gates and buttons inside it @phone @dark", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const id = await fileTask(demoServer, accessCode, `Fits ${Date.now()}`);
  await overlay(page, id, CASES["auto-merge turned off after a review push"]);
  await page.goto(`task/${id}`);
  const area = page.getByTestId("task-action-area");
  await expect(area.getByTestId("auto-merge-off")).toBeVisible();
  expect(
    await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    ),
  ).toBeLessThanOrEqual(0);
  const box = await area.boundingBox();
  const viewport = page.viewportSize();
  expect(box.x).toBeGreaterThanOrEqual(0);
  expect(box.x + box.width).toBeLessThanOrEqual(viewport.width + 0.5);
  // The sentence leads the area, the gates follow it, then the buttons.
  const sentence = await area.getByTestId("whats-needed").boundingBox();
  const gates = await area.getByTestId("merge-gates-list").boundingBox();
  expect(sentence.y).toBeLessThan(gates.y);
  const inside = (b) => {
    expect(b.x).toBeGreaterThanOrEqual(box.x - 0.5);
    expect(b.x + b.width).toBeLessThanOrEqual(box.x + box.width + 0.5);
    expect(b.y).toBeGreaterThanOrEqual(box.y - 0.5);
    expect(b.y + b.height).toBeLessThanOrEqual(box.y + box.height + 0.5);
  };
  for (const el of await area
    .locator(".merge-gate-row, .merge-gate-row button")
    .all()) {
    inside(await el.boundingBox());
  }
  // The button row stays inside the area. On a phone-width column it is
  // one line that scrolls sideways, so a button further along is reached
  // by scrolling the row, and lands inside it.
  const row = area.getByTestId("task-actions");
  inside(await row.boundingBox());
  for (const el of await row.locator("button").all()) {
    await el.scrollIntoViewIfNeeded();
    const b = await el.boundingBox();
    const r = await row.boundingBox();
    expect(b.x).toBeGreaterThanOrEqual(r.x - 0.5);
    expect(b.x + b.width).toBeLessThanOrEqual(r.x + r.width + 0.5);
    expect(b.y).toBeGreaterThanOrEqual(r.y - 0.5);
    expect(b.y + b.height).toBeLessThanOrEqual(r.y + r.height + 0.5);
  }
  // The area sits above the tabs, so it is in view on every tab.
  const tabs = await page.locator(".task-tabs").boundingBox();
  expect(box.y + box.height).toBeLessThanOrEqual(tabs.y + 0.5);
});
