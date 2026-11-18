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

// The Inbox's Alerts group offers Clear all, and Clear these once the view
// is filtered, against a real `grain demo`: it counts with a dry run,
// confirms with that count and a reason, and clears only the counted ids
// through POST /api/notifications/clear so an alert raised while the dialog
// is open stays open. The button sits in its group's head and the dialog fits
// a phone.

import { test, expect } from "./fixtures.js";

// A distinct demoArgs gives this spec a demo server of its own so the
// unfiltered Clear all test never clears alerts another spec reads.
test.use({ demoArgs: ["-open=false", "-as", "next-inbox-clear-alerts"] });

async function api(baseURL, code, method, path, data) {
  const res = await fetch(`${baseURL}${path}`, {
    method,
    headers: {
      Authorization: `Bearer ${code}`,
      "Content-Type": "application/json",
    },
    body: data === undefined ? undefined : JSON.stringify(data),
  });
  if (!res.ok)
    throw new Error(`${method} ${path}: ${res.status} ${await res.text()}`);
  const text = await res.text();
  return text ? JSON.parse(text) : null;
}

test("Clear these clears only the filtered alert, with a reason", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const key = `e2e-clear-${Date.now()}`;
  const raised = await api(
    demoServer,
    accessCode,
    "POST",
    "/api/notifications",
    {
      subject: `ops.${key}:disk`,
      title: `Disk full on ${key}`,
      severity: "warning",
    },
  );
  const id = raised.notification.id;
  try {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/next/inbox");
    const group = page.getByTestId("inbox-group-notification");
    await expect(
      group.getByRole("button", { name: /^Clear all \d+ alerts?$/ }),
    ).toBeVisible();

    await page.getByRole("searchbox", { name: "Filter this view" }).fill(key);
    const clear = group.getByRole("button", { name: "Clear these 1 alert" });
    await expect(clear).toBeVisible();
    // The button sits in the group's head row, on its right.
    const head = await group.locator(".queue-group-head").boundingBox();
    const btn = await clear.boundingBox();
    expect(btn.y).toBeGreaterThanOrEqual(head.y);
    expect(btn.y + btn.height).toBeLessThanOrEqual(head.y + head.height + 0.5);
    expect(btn.x + btn.width).toBeLessThanOrEqual(head.x + head.width);

    await clear.click();
    const dialog = page.getByRole("dialog", { name: "Clear these 1 alert?" });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText(
      "None of them is expected back on its own.",
    );
    const accept = dialog.getByRole("button", { name: "Clear 1" });
    await expect(accept).toBeDisabled();
    await dialog
      .getByLabel("Reason (kept in the audit log)")
      .fill("e2e: disk resized");
    await expect(accept).toBeEnabled();
    await accept.click();
    await expect(dialog).toHaveCount(0);
    await expect(page.getByText("Cleared 1 alert")).toBeVisible();
    await expect(group).toHaveCount(0);

    const open = await api(
      demoServer,
      accessCode,
      "GET",
      "/api/notifications?open=1",
    );
    const list = Array.isArray(open)
      ? open
      : (open.items ?? open.notifications ?? []);
    expect(list.map((n) => n.id)).not.toContain(id);
  } finally {
    await api(
      demoServer,
      accessCode,
      "POST",
      `/api/notifications/${id}/clear`,
    ).catch(() => {});
  }
});

test(
  "Clear all and its dialog fit a phone",
  { tag: ["@phone"] },
  async ({ page }) => {
    const width = 390;
    await page.setViewportSize({ width, height: 844 });
    await page.goto("/next/inbox");
    const group = page.getByTestId("inbox-group-notification");
    const clear = group.getByRole("button", {
      name: /^Clear all \d+ alerts?$/,
    });
    await expect(clear).toBeVisible();
    const btn = await clear.boundingBox();
    expect(btn.x).toBeGreaterThanOrEqual(0);
    expect(btn.x + btn.width).toBeLessThanOrEqual(width);
    expect(
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth -
          document.documentElement.clientWidth,
      ),
      "no sideways scroll",
    ).toBeLessThanOrEqual(0);

    await clear.click();
    const dialog = page.getByRole("dialog", {
      name: /^Clear all \d+ alerts?\?$/,
    });
    await expect(dialog).toBeVisible();
    const box = await dialog.boundingBox();
    expect(box.x).toBeGreaterThanOrEqual(0);
    expect(box.x + box.width).toBeLessThanOrEqual(width);
    const reason = await dialog
      .getByLabel("Reason (kept in the audit log)")
      .boundingBox();
    expect(reason.x).toBeGreaterThanOrEqual(box.x);
    expect(reason.x + reason.width).toBeLessThanOrEqual(box.x + box.width);
    for (const name of ["Cancel", /^Clear \d+$/]) {
      const b = await dialog.getByRole("button", { name }).boundingBox();
      expect(b.x + b.width).toBeLessThanOrEqual(width);
    }
    // Nothing is cleared without a reason, and Cancel leaves everything.
    await expect(
      dialog.getByRole("button", { name: /^Clear \d+$/ }),
    ).toBeDisabled();
    await dialog.getByRole("button", { name: "Cancel" }).click();
    await expect(dialog).toHaveCount(0);
    await expect(clear).toBeVisible();
  },
);

test("Clear all clears only the alerts counted by the dry run and keeps a new alert open", async ({
  page,
  demoServer,
  accessCode,
}) => {
  const key = `e2e-clear-all-${Date.now()}`;
  const early = await api(
    demoServer,
    accessCode,
    "POST",
    "/api/notifications",
    {
      subject: `ops.${key}:early`,
      title: `Early warning on ${key}`,
      severity: "warning",
    },
  );
  const earlyId = early.notification.id;
  let lateId = null;
  try {
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.goto("/next/inbox");
    const group = page.getByTestId("inbox-group-notification");
    const clear = group.getByRole("button", {
      name: /^Clear all \d+ alerts?$/,
    });
    await expect(clear).toBeVisible();
    await clear.click();

    const dialog = page.getByRole("dialog", {
      name: /^Clear all \d+ alerts?\?$/,
    });
    await expect(dialog).toBeVisible();
    const accept = dialog.getByRole("button", { name: /^Clear \d+$/ });
    const acceptLabel = (await accept.textContent()).trim();
    const counted = Number(acceptLabel.replace(/^Clear\s+/, ""));
    expect(counted).toBeGreaterThanOrEqual(1);

    // Raise a critical alert after the dry run completed, while the
    // confirmation dialog is open.
    const late = await api(
      demoServer,
      accessCode,
      "POST",
      "/api/notifications",
      {
        subject: `ops.${key}:late-critical`,
        title: `Late critical on ${key}`,
        severity: "critical",
      },
    );
    lateId = late.notification.id;

    await dialog
      .getByLabel("Reason (kept in the audit log)")
      .fill("e2e: clear only counted alerts");
    await expect(accept).toBeEnabled();
    await accept.click();
    await expect(dialog).toHaveCount(0);
    await expect(
      page.getByText(new RegExp(`^Cleared ${counted} alerts?(;|$)`)),
    ).toBeVisible();

    const open = await api(
      demoServer,
      accessCode,
      "GET",
      "/api/notifications?open=1",
    );
    const list = Array.isArray(open)
      ? open
      : (open.items ?? open.notifications ?? []);
    const openIds = list.map((n) => n.id);
    expect(openIds).not.toContain(earlyId);
    expect(openIds).toContain(lateId);
    await expect(
      group.getByRole("button", { name: "Clear all 1 alert" }),
    ).toBeVisible();
    const lateRow = group.getByRole("button", {
      name: new RegExp(`Late critical on ${key}`),
    });
    await lateRow.scrollIntoViewIfNeeded();
    await expect(lateRow).toBeVisible();
  } finally {
    for (const id of [earlyId, lateId]) {
      if (id !== null) {
        await api(
          demoServer,
          accessCode,
          "POST",
          `/api/notifications/${id}/clear`,
        ).catch(() => {});
      }
    }
  }
});
