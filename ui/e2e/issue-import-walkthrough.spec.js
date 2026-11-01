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

// The GitHub issue importer walked end to end over `grain demo
// -github-sim` (docs/test-plans/2026-10-30.md section 1, recorded in
// docs/test-runs/2026-10-31-github-issue-import.md): the Agents tab's
// switch and label driving a real import of the sim's issues, its counts,
// a second pass importing nothing, the imported tasks on the list, the
// inbox and the board, approval, closing, an issue labelled after the
// fact, an upstream source and queue-at-once mode. The sim's issues are
// opened, labelled and closed through the demo's loopback-only
// /api/e2e/github-sim hook. No -reconcile, so only "Import issues now"
// imports and every count is the button's own.

import { expect, test } from "./fixtures.js";

test.use({ demoArgs: ["-github-sim", "-fake-ci"] });

const SIM = "/api/e2e/github-sim/repos";

async function sim(page, method, path, data) {
  const res = await page.request.fetch(`${SIM}/${path}`, { method, data });
  expect(res.ok(), `${method} ${path}: ${res.status()}`).toBe(true);
  return res.json();
}

async function importedTasks(page) {
  const res = await (await page.request.get("/api/tasks")).json();
  const list = Array.isArray(res) ? res : res.tasks || [];
  return list.filter((t) => t.imported);
}

async function importNow(page, section) {
  const done = page.waitForResponse(
    (r) =>
      r.url().endsWith("/import-issues") && r.request().method() === "POST",
  );
  await section.getByRole("button", { name: "Import issues now" }).click();
  await done;
  return section.getByRole("status");
}

async function expectInside(inner, outer) {
  const a = await inner.boundingBox();
  const b = await outer.boundingBox();
  expect(a && b).toBeTruthy();
  expect(a.x).toBeGreaterThanOrEqual(b.x - 1);
  expect(a.x + a.width).toBeLessThanOrEqual(b.x + b.width + 1);
}

test("the issue importer imports, dedups and queues the sim's issues", async ({
  page,
}) => {
  test.setTimeout(120_000);
  await page.setViewportSize({ width: 1440, height: 900 });
  // The sim's issues: #1 is the demo's own seed (labelled grain-agent);
  // a new labelled one, an unlabelled one, a labelled one already
  // closed, and a labelled pull request -- which GitHub's issues listing
  // carries too, and the importer must skip.
  const labelled = await sim(page, "POST", "acme/widgets/issues", {
    title: "Add a dark-mode toggle",
    body: "The settings page needs one.",
    labels: ["grain-agent"],
  });
  const unlabelled = await sim(page, "POST", "acme/widgets/issues", {
    title: "Document the CLI flags",
  });
  const closed = await sim(page, "POST", "acme/widgets/issues", {
    title: "Old closed request",
    labels: ["grain-agent"],
  });
  await sim(page, "PATCH", `acme/widgets/issues/${closed.number}`, {
    state: "closed",
  });

  try {
    await page.goto("/repos/acme/widgets/settings/agents");
    const section = page.getByTestId("repo-issue-import");
    await expect(section).toBeVisible();
    const enable = section.getByLabel(
      "Import open GitHub issues as tasks in acme/widgets",
    );
    // The label typed first goes with the switch (F2): the first pass
    // reads only issues carrying it.
    await section.getByLabel("Label").fill("grain-agent");
    const saved = page.waitForResponse(
      (r) => r.url().endsWith("/settings") && r.request().method() === "PUT",
    );
    await enable.click();
    expect((await saved).request().postDataJSON()).toMatchObject({
      issueImportEnabled: true,
      issueImportLabel: "grain-agent",
    });
    await expect(enable).toBeChecked();
    await expect(section.getByText(/labelled “grain-agent”/)).toBeVisible();

    let status = await importNow(page, section);
    await expect(status).toHaveText(
      "Read 2 open issues on acme/widgets: imported 2, 0 already imported.",
    );
    status = await importNow(page, section);
    await expect(status).toHaveText(
      "Read 2 open issues on acme/widgets: imported 0, 2 already imported.",
    );
    let tasks = await importedTasks(page);
    const titleOf = (n, title) => `[#${n}] ${title}`;
    const dark = tasks.find(
      (t) => t.title === titleOf(labelled.number, "Add a dark-mode toggle"),
    );
    const seeded = tasks.find((t) => t.origin === "acme/widgets#1");
    expect(dark, "the labelled issue's task").toBeTruthy();
    expect(seeded, "the demo's seeded issue's task").toBeTruthy();
    expect(dark.awaitingImportApproval).toBe(true);
    expect(dark.issue).toBe(labelled.number);
    for (const n of [unlabelled.number, closed.number, 9000]) {
      expect(tasks.find((t) => t.origin === `acme/widgets#${n}`)).toBeFalsy();
    }

    // The list, filtered to imported issues; the inbox; the board.
    await page.goto("/?f=origin:any:imported");
    const row = page.locator(".task-row", { hasText: dark.title });
    await expect(row).toHaveCount(1);
    await expect(row.getByRole("img", { name: "Imported" })).toBeVisible();
    await expect(row.locator(".chip-imported")).toHaveAttribute(
      "title",
      `imported from GitHub issue acme/widgets#${labelled.number}`,
    );
    await page.goto("/inbox");
    await expect(page.getByText(dark.title).first()).toBeVisible();
    const boards = await (await page.request.get("/api/boards")).json();
    const board = boards.find((b) => b.name === "Board") || boards[0];
    await page.goto(`/boards/${board.id}`);
    const card = page.locator(".board-card", { hasText: dark.title });
    await expect(card).toBeVisible();
    await expect(card.locator(".chip-imported")).toBeVisible();

    // Approve one from its pane: it leaves Imported, the chip stays.
    await page.goto("/?f=origin:any:imported");
    await row.click();
    await page.getByRole("button", { name: "Approve", exact: true }).click();
    await expect(row.getByRole("img", { name: "Imported" })).toHaveCount(0);
    await expect(row.locator(".chip-imported")).toBeVisible();
    // Close the other; label the unlabelled issue after the fact.
    expect(
      (await page.request.post(`/api/tasks/${seeded.id}/close`)).ok(),
    ).toBe(true);
    await sim(page, "POST", `acme/widgets/issues/${unlabelled.number}/labels`, {
      labels: ["grain-agent"],
    });
    await page.goto("/repos/acme/widgets/settings/agents");
    status = await importNow(page, section);
    await expect(status).toHaveText(
      "Read 3 open issues on acme/widgets: imported 1, 2 already imported.",
    );
    tasks = await importedTasks(page);
    expect(tasks.find((t) => t.id === dark.id).state).toBe("queued");
    expect(tasks.find((t) => t.id === seeded.id).state).toBe("closed");
    expect(
      tasks.filter((t) => t.origin === "acme/widgets#1"),
      "a closed import is never filed again",
    ).toHaveLength(1);
    expect(
      tasks.find((t) => t.origin === `acme/widgets#${unlabelled.number}`),
    ).toBeTruthy();

    // An upstream source: the demo's second simulated repository.
    await section.getByLabel("Source repository").fill("acme-upstream/widgets");
    await section.getByLabel("Label").fill("");
    await section.getByRole("button", { name: "Save" }).click();
    await expect(
      section.getByText(/reads the open issues of acme-upstream\/widgets/),
    ).toBeVisible();
    status = await importNow(page, section);
    await expect(status).toHaveText(
      "Read 1 open issue on acme-upstream/widgets: imported 1, 0 already imported.",
    );
    const upstream = (await importedTasks(page)).find(
      (t) => t.origin === "acme-upstream/widgets#7",
    );
    expect(upstream.title).toBe(
      "[acme-upstream/widgets#7] Widgets render blank on narrow screens",
    );
    expect(upstream.issue ?? 0, "no Task.Issue for an upstream issue").toBe(0);
    expect(upstream.awaitingImportApproval).toBe(true);

    // Queue at once: the trust sentence is on the page before the box
    // is ticked, and what it imports then is queued, not Imported.
    const queue = section.getByLabel(
      "Queue imported issues in acme/widgets without approval",
    );
    await expect(
      section.getByText(
        /Turn it on only if you trust everybody who can open an issue on acme-upstream\/widgets/,
      ),
    ).toBeVisible();
    await queue.click();
    await expect(queue).toBeChecked();
    await sim(page, "POST", "acme-upstream/widgets/issues", {
      title: "Queue me at once",
    });
    status = await importNow(page, section);
    await expect(status).toHaveText(/imported 1, 1 already imported\.$/);
    const queued = (await importedTasks(page)).find((t) =>
      t.title.endsWith("Queue me at once"),
    );
    expect(queued.state).toBe("queued");
    expect(queued.awaitingImportApproval ?? false).toBe(false);

    // The section, and the outcome line, still fit a phone.
    await page.setViewportSize({ width: 390, height: 844 });
    await expectInside(status, section);
    await expectInside(
      section.getByRole("button", { name: "Import issues now" }),
      section,
    );
    const overflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth -
        document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
  } finally {
    // The worker's demo is shared: leave the importer off again.
    await page.request.put("/api/repos/acme/widgets/settings", {
      data: {
        issueImportEnabled: false,
        issueImportApproved: false,
        issueImportRepo: "",
        issueImportLabel: "",
      },
    });
  }
});
