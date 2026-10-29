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

// The CI pane's Shared faults section over the demo's seeded GitHub CI jobs
// (pkg/demo/demo_githubci.go): GET /api/ci/faults serves the 3-branch GitHub
// shared fault with source: "github", and /ci renders the card labelled with
// its source at both desktop and phone widths without overflowing the page.

for (const [name, viewport] of [
  ["desktop", { width: 1280, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  for (const scheme of ["light", "dark"]) {
    test(`serves and renders GitHub CI shared faults labelled by source (${name}, ${scheme})`, async ({
      page,
    }) => {
      await page.setViewportSize(viewport);
      await page.emulateMedia({ colorScheme: scheme });
      const res = await page.request.get("/api/ci/faults");
      expect(res.ok()).toBe(true);
      const body = await res.json();
      const ghFault = (body.faults || []).find((f) => f.source === "github");
      expect(ghFault).toBeTruthy();
      expect(ghFault.repo).toBe("acme/widgets");
      expect(ghFault.jobName).toBe("CI / e2e");
      expect(ghFault.step).toBe("Run go test ./...");
      expect(ghFault.refs).toEqual([
        "feature/audit-log",
        "feature/webhook-retry",
        "feature/dark-mode",
      ]);
      expect(ghFault.paths).toEqual(["pkg/auth/session.go"]);

      await page.goto("/ci");

      const section = page.getByTestId("ci-shared-faults-section");
      await expect(section).toBeVisible();
      const card = page.getByTestId("ci-shared-fault-github");
      await expect(card).toBeVisible();
      const sourceChip = card.getByTestId("ci-fault-source-github");
      await expect(sourceChip).toHaveText("GitHub CI");
      await expect(card).toContainText(
        "acme/widgets · CI / e2e (Run go test ./...)",
      );
      await expect(card).toContainText(
        "failing the same way on 3 branches right now",
      );
      await expect(card).toContainText(
        "Branches: feature/audit-log, feature/webhook-retry, feature/dark-mode",
      );
      await expect(card).toContainText("Common files: pkg/auth/session.go");

      const [cardBox, chipBox] = await Promise.all([
        card.boundingBox(),
        sourceChip.boundingBox(),
      ]);
      expect(cardBox.x).toBeGreaterThanOrEqual(0);
      expect(cardBox.x + cardBox.width).toBeLessThanOrEqual(viewport.width + 1);
      expect(chipBox.x).toBeGreaterThanOrEqual(cardBox.x);
      expect(chipBox.x + chipBox.width).toBeLessThanOrEqual(
        cardBox.x + cardBox.width + 1,
      );

      const pageWidth = await page.evaluate(
        () => document.documentElement.scrollWidth,
      );
      expect(pageWidth).toBeLessThanOrEqual(viewport.width + 1);
    });
  }
}

// grain/task-1520: the box folds behind its header, and the choice is
// this browser's -- a reload opens /ci with it still folded, the header
// still saying how many, and no "new" badge for faults it already held.
// At a phone's width as well as a desktop's (2026-10-28 test plan §7).
for (const [name, viewport] of [
  ["desktop", { width: 1280, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`collapses the shared faults box and keeps it collapsed across a reload (${name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    await page.goto("/ci");
    const toggle = page.getByTestId("ci-shared-faults-toggle");
    await expect(toggle).toHaveAttribute("aria-expanded", "true");
    await expect(toggle).toHaveText(/\d+ shared failures?/);
    const card = page.getByTestId("ci-shared-fault-github");
    await expect(card).toBeVisible();

    await toggle.click();
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    await expect(card).toBeHidden();

    await page.reload();
    const again = page.getByTestId("ci-shared-faults-toggle");
    await expect(again).toHaveAttribute("aria-expanded", "false");
    await expect(page.getByTestId("ci-shared-fault-github")).toBeHidden();
    await expect(page.getByTestId("ci-shared-faults-new")).toHaveCount(0);

    // Keyboard opens it again.
    await again.focus();
    await page.keyboard.press("Enter");
    await expect(again).toHaveAttribute("aria-expanded", "true");
    await expect(page.getByTestId("ci-shared-fault-github")).toBeVisible();
  });
}

// A fault the collapsed box has not seen is counted on its header, the
// chip sitting on the header's line and inside the page at both widths.
for (const [name, viewport] of [
  ["desktop", { width: 1280, height: 900 }],
  ["phone", { width: 390, height: 844 }],
]) {
  test(`marks faults the collapsed box has not seen as new (${name})`, async ({
    page,
  }) => {
    await page.setViewportSize(viewport);
    // Collapsed when nothing had been seen: every fault is new.
    await page.addInitScript(() =>
      localStorage.setItem(
        "grain.ci.sharedFaults.collapsed",
        JSON.stringify({ seen: [] }),
      ),
    );
    await page.goto("/ci");
    const toggle = page.getByTestId("ci-shared-faults-toggle");
    await expect(toggle).toHaveAttribute("aria-expanded", "false");
    const badge = page.getByTestId("ci-shared-faults-new");
    await expect(badge).toHaveText(/^\d+ new$/);
    await expect(page.getByTestId("ci-shared-fault-github")).toBeHidden();

    const [toggleBox, badgeBox] = await Promise.all([
      toggle.boundingBox(),
      badge.boundingBox(),
    ]);
    const mid = (b) => b.y + b.height / 2;
    expect(Math.abs(mid(toggleBox) - mid(badgeBox))).toBeLessThanOrEqual(3);
    expect(badgeBox.x).toBeGreaterThan(toggleBox.x + toggleBox.width);
    expect(badgeBox.x + badgeBox.width).toBeLessThanOrEqual(viewport.width);
  });
}
