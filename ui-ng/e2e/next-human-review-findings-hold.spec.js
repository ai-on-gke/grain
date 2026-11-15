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

// The human-review hold gate card on a hold of review findings
// (source security-findings), which Release hold never clears
// (model.ErrFindingsRequireRereviewOrOverride): no Release hold on
// findings alone, the reason said in words, and the review-gate
// override offered from the card. The demo seeds no findings hold, so
// the task's detail is answered with one on top of a real task.

const FINDINGS = {
  source: "security-findings",
  category: "security",
  reason: "Security review left 2 findings on the token refresh path",
  reviewTask: "9001",
};
const REQUESTED = {
  source: "requested",
  category: "product-decision",
  reason: "Changing the default tier changes what new customers are billed",
};
const FINDINGS_ERROR =
  "review findings cannot be cleared by Submit or Release hold: fix the findings for a clean re-review, or override the review gate under merge:force with a reason";

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

async function holdWith(page, id, hold, extra = {}) {
  await page.route(new RegExp(`/api/tasks/${id}(\\?.*)?$`), async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    // A poll's If-None-Match would get a bodiless 304 to rewrite.
    const headers = { ...route.request().headers() };
    delete headers["if-none-match"];
    const res = await route.fetch({ headers });
    const detail = await res.json();
    await route.fulfill({
      response: res,
      json: {
        ...detail,
        state: "awaiting_submit",
        pullRequest: "acme/widgets#42",
        humanReviewHold: hold,
        mergeGates: [
          {
            id: "security-review",
            label: "Security review",
            state: "findings",
            reviewTask: "9001",
          },
        ],
        ...extra,
      },
    });
  });
}

test("a findings-only hold offers the gate override, not Release hold @phone @dark", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const id = await fileTask(
    demoServer,
    accessCode,
    `Findings hold ${Date.now()}`,
  );
  await holdWith(page, id, [FINDINGS]);
  await page.goto(`task/${id}`);

  const card = page.getByTestId("human-review-hold");
  await expect(card).toBeVisible();
  await expect(card.getByTestId("release-hold-btn")).toHaveCount(0);
  const note = card.getByTestId("hold-findings-note");
  await expect(note).toContainText("cannot be released directly");
  await expect(note).toContainText("merge:force");
  await expect(note).not.toContainText("other reasons");

  // The override sits in the card head's action slot, inside the card.
  const override = card.getByTestId("hold-force-merge-open");
  await expect(override).toBeVisible();
  const cardBox = await card.boundingBox();
  const btnBox = await override.boundingBox();
  expect(btnBox.x).toBeGreaterThanOrEqual(cardBox.x);
  expect(btnBox.x + btnBox.width).toBeLessThanOrEqual(
    cardBox.x + cardBox.width + 0.5,
  );

  await override.click();
  const dialog = page.getByTestId("force-merge-dialog");
  await expect(dialog).toBeVisible();
  await expect(
    dialog.getByTestId("override-gate-security-review"),
  ).toBeVisible();
  await dialog.getByTestId("force-merge-cancel").click();
  await expect(dialog).toHaveCount(0);
});

test("a mixed hold keeps Release hold and shows the server's reason on refusal", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const id = await fileTask(demoServer, accessCode, `Mixed hold ${Date.now()}`);
  await holdWith(page, id, [FINDINGS, REQUESTED]);
  // The server's own 409 for findings, so the page has a refusal to show.
  await page.route(`**/api/tasks/${id}/human-review/release`, (route) =>
    route.fulfill({
      status: 409,
      json: { error: FINDINGS_ERROR, code: "review_findings_hold" },
    }),
  );
  await page.goto(`task/${id}`);

  const card = page.getByTestId("human-review-hold");
  await expect(card.getByTestId("hold-findings-note")).toContainText(
    "other reasons only",
  );
  await card.getByTestId("release-hold-btn").click();
  await expect(page.getByTestId("task-action-error")).toContainText(
    "override the review gate under merge:force",
  );
  await expect(page.getByTestId("task-action-error")).not.toContainText(
    "internal server error",
  );
});

test("distinguishes absent mergeGates from an overridden security-review gate @phone @dark", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const noGatesId = await fileTask(
    demoServer,
    accessCode,
    `No gates hold ${Date.now()}`,
  );
  await holdWith(page, noGatesId, [FINDINGS], {
    pullRequest: "",
    mergeGates: [],
  });
  await page.goto(`task/${noGatesId}`);

  const card1 = page.getByTestId("human-review-hold");
  await expect(card1).toBeVisible();
  await expect(card1.getByTestId("hold-force-merge-open")).toHaveCount(0);
  const note1 = card1.getByTestId("hold-findings-note");
  await expect(note1).toContainText("cannot be released directly");
  await expect(note1).not.toContainText("has been overridden");

  const overriddenId = await fileTask(
    demoServer,
    accessCode,
    `Overridden hold ${Date.now()}`,
  );
  await holdWith(page, overriddenId, [FINDINGS], {
    mergeGates: [
      {
        id: "security-review",
        label: "Security review",
        state: "overridden",
        reviewTask: "9001",
      },
      {
        id: "review",
        label: "Code review",
        state: "running",
        reviewTask: "9002",
      },
    ],
  });
  await page.goto(`task/${overriddenId}`);

  const card2 = page.getByTestId("human-review-hold");
  await expect(card2).toBeVisible();
  await expect(card2.getByTestId("hold-force-merge-open")).toHaveCount(0);
  const note2 = card2.getByTestId("hold-findings-note");
  await expect(note2).toContainText("has been overridden");

  const cardBox = await card2.boundingBox();
  const noteBox = await note2.boundingBox();
  expect(noteBox.x).toBeGreaterThanOrEqual(cardBox.x);
  expect(noteBox.x + noteBox.width).toBeLessThanOrEqual(
    cardBox.x + cardBox.width + 0.5,
  );
  const overflow = await page.evaluate(
    () => document.documentElement.scrollWidth - window.innerWidth,
  );
  expect(overflow).toBeLessThanOrEqual(0);
});
