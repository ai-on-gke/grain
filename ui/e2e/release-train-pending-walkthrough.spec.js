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

// docs/test-plans/2026-11-10.md section 8 item 5: the Inbox's pending
// releases through the GitHub simulator -- a soak-era release under its
// minimum that also carries breaking changes is approved with Approve
// short soak & breaking, which confirms first and dispatches both
// options -- with the four kinds (normal, breaking, short soak & breaking,
// failed dispatch) rendered at 1440 and 390 px in light and dark.
// RELEASE_TRAIN_SHOTS=<dir> also writes the screenshots.

import { expect, test } from "./fixtures.js";

test.use({
  demoArgs: ["-github-sim", "-fake-ci", "-default-target-repo", "acme/widgets"],
});
test.setTimeout(120_000);

const SIM = "/api/e2e/github-sim/repos/acme/widgets";
const shots = process.env.RELEASE_TRAIN_SHOTS;

test("pending releases: short soak & breaking approves with both options; four kinds at both widths and themes", async ({
  page,
}) => {
  const ok = async (res, what) => {
    expect(res.ok(), `${what}: ${res.status()} ${await res.text()}`).toBe(true);
    return res.status() === 204 ? null : res.json();
  };
  const [main] = await ok(
    await page.request.get(`${SIM}/git/matching-refs/heads/main`),
    "main",
  );
  const pending = {
    "v4.1.0":
      "v4.1.0 is staged as v4.1.0-pending on acme/widgets and waits for a person.",
    "v4.2.0":
      "v4.2.0 is staged as v4.2.0-pending.\n\nBREAKING CHANGES: v4.2.0 carries breaking changes since v4.1.0 and is not the major release RELEASE_MAJOR asks for; approving it needs allow_breaking: true.",
    "v4.3.0":
      "v4.3.0 is staged as v4.3.0-pending.\nSoak: soaked 3h10m of 24h\nSHORT SOAK: v4.3.0 soaked 3h10m, under the 22h minimum; approving it needs short_soak_ok: true.\n\nBREAKING CHANGES: v4.3.0 carries breaking changes since v4.2.0; approving it needs allow_breaking: true.",
    "v4.4.0":
      "v4.4.0 is staged as v4.4.0-pending.\n\nDISPATCH FAILED (gha:acme:widgets:42577215514): verify-release-approval: refused: the repository variable GRAIN_RELEASE_APPROVAL_KEY is not set\nRemedy: set the repository variable GRAIN_RELEASE_APPROVAL_KEY on acme/widgets to the publicKey from GET /api/release-approval-key (or `grain release approval-key`), then approve or reject again.",
  };
  for (const [version, body] of Object.entries(pending)) {
    await ok(
      await page.request.post(`${SIM}/git/refs`, {
        data: { ref: `refs/tags/${version}-pending`, sha: main.object.sha },
      }),
      `tag ${version}-pending`,
    );
    await ok(
      await page.request.post("/api/notifications", {
        data: {
          subject: `release.pending:${version}`,
          severity: "warning",
          title: `${version} is ready to promote`,
          body,
          repo: "acme/widgets",
          actions: ["approve-release", "reject-release"],
        },
      }),
      `raise ${version}`,
    );
  }

  for (const scheme of ["light", "dark"]) {
    for (const width of [1440, 390]) {
      await page.emulateMedia({ colorScheme: scheme });
      await page.setViewportSize({ width, height: width > 400 ? 900 : 844 });
      await page.goto("/inbox");
      const section = page.getByTestId("inbox-pending-releases");
      // The worker's demo server is shared: count only this spec's rows.
      const rows = section
        .getByTestId("inbox-pending-release")
        .filter({ hasText: /v4\.[1-4]\.0 is ready to promote/ });
      await expect(rows).toHaveCount(4);
      for (const row of await rows.all()) {
        const box = await row.boundingBox();
        expect(
          box.x,
          `${scheme} ${width}: a row starts off-screen`,
        ).toBeGreaterThanOrEqual(0);
        expect(
          box.x + box.width,
          `${scheme} ${width}: a row overflows`,
        ).toBeLessThanOrEqual(width + 1);
      }
      if (shots) {
        await section.screenshot({
          path: `${shots}/inbox-pending-${width}-${scheme}.png`,
        });
      }
    }
  }

  await page.emulateMedia({ colorScheme: "light" });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/inbox");
  const row = page
    .getByTestId("inbox-pending-release")
    .filter({ hasText: "v4.3.0 is ready to promote" });
  await expect(row.getByTestId("inbox-release-soak")).toContainText(
    "SHORT SOAK",
  );
  await expect(row.getByTestId("inbox-release-soak")).toContainText("BREAKING");
  const approve = row.getByRole("button", {
    name: /Approve short soak & breaking/i,
  });
  await expect(approve).toBeVisible();
  const before = (
    await ok(
      await page.request.get(
        `${SIM}/actions/workflows/promote-release.yml/dispatches`,
      ),
      "dispatches",
    )
  ).length;
  let confirm = "";
  page.once("dialog", (dialog) => {
    confirm = dialog.message();
    dialog.accept();
  });
  await approve.click();
  await expect(row).toHaveCount(0);
  expect(confirm).toMatch(/breaking/i);
  expect(confirm).toMatch(/soak/i);
  const sent = await ok(
    await page.request.get(
      `${SIM}/actions/workflows/promote-release.yml/dispatches`,
    ),
    "dispatches",
  );
  expect(sent).toHaveLength(before + 1);
  expect(sent[sent.length - 1].inputs).toMatchObject({
    version: "v4.3.0",
    decision: "approve",
    short_soak_ok: "true",
    allow_breaking: "true",
  });
});
