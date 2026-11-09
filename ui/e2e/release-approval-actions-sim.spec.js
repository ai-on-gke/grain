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

// The GitHub Actions half of release approval, from the Inbox, over
// `grain demo -github-sim` (docs/test-runs/2026-11-04-release-approval.md,
// F2 and F3): a person's Approve of a patch release on acme/widgets -- a
// repository that never set a CI Mode, so an Actions one -- dispatches
// promote-release.yml through the simulated GitHub once, with a record
// that verifies against the deployment's key and binds the -pending
// tag's commit; a second press is refused; Reject dispatches a reject.
// Before, the demo had no forge to dispatch through and no key to sign
// with, so Approve answered "grain CI is not enabled" and then 500.

import crypto from "node:crypto";
import { expect, test } from "./fixtures.js";

// Its own worker: the extra (default-valued) flag keeps this file off
// the server release-from-trunk-approval-walkthrough.spec.js counts CI
// runs on.
test.use({
  demoArgs: ["-github-sim", "-fake-ci", "-default-target-repo", "acme/widgets"],
});
test.describe.configure({ mode: "serial" });
test.setTimeout(90_000);

const SIM = "/api/e2e/github-sim/repos/acme/widgets";

test("an Actions repo's pending patch release is approved from the Inbox through the sim, once", async ({
  page,
}) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  const ok = async (res, what) => {
    expect(res.ok(), `${what}: ${res.status()} ${await res.text()}`).toBe(true);
    return res.status() === 204 ? null : res.json();
  };
  const dispatches = async () =>
    ok(
      await page.request.get(
        `${SIM}/actions/workflows/promote-release.yml/dispatches`,
      ),
      "dispatches",
    );

  // The -pending tags the release train would have pushed, at main.
  const [main] = await ok(
    await page.request.get(`${SIM}/git/matching-refs/heads/main`),
    "main",
  );
  const commit = main.object.sha;
  for (const version of ["v2.9.1", "v2.10.0"]) {
    await ok(
      await page.request.post(`${SIM}/git/refs`, {
        data: { ref: `refs/tags/${version}-pending`, sha: commit },
      }),
      `tag ${version}-pending`,
    );
    await ok(
      await page.request.post("/api/notifications", {
        data: {
          subject: `release.pending:${version}`,
          severity: "warning",
          title: `${version} is ready to promote`,
          body: `${version} is staged as ${version}-pending on acme/widgets and waits for a person.`,
          repo: "acme/widgets",
          actions: ["approve-release", "reject-release"],
        },
      }),
      `raise ${version}`,
    );
  }
  const before = (await dispatches()).length;

  await page.goto("/inbox");
  const section = page.getByTestId("inbox-pending-releases");
  await expect(section).toBeVisible();
  await expect(section.locator(".inbox-group-toggle")).toHaveAttribute(
    "aria-expanded",
    "true",
  );
  const patchRow = section
    .getByTestId("inbox-pending-release")
    .filter({ hasText: "v2.9.1 is ready to promote" });
  await expect(patchRow).toBeVisible();
  page.once("dialog", (dialog) => dialog.accept());
  await patchRow.getByRole("button", { name: "Approve" }).click();
  await expect(patchRow).toHaveCount(0);

  let sent = await dispatches();
  expect(sent).toHaveLength(before + 1);
  const approve = sent[sent.length - 1];
  expect(approve).toMatchObject({
    workflow: "promote-release.yml",
    ref: "main",
    inputs: {
      version: "v2.9.1",
      decision: "approve",
      short_soak_ok: "false",
      allow_breaking: "false",
    },
  });
  const record = Buffer.from(approve.inputs.approval, "base64");
  expect(record.toString()).toContain(
    `repo: acme/widgets\nversion: v2.9.1\ndecision: approve\ncommit: ${commit}\n`,
  );
  const { publicKey } = await ok(
    await page.request.get("/api/release-approval-key"),
    "approval key",
  );
  expect(
    crypto.verify(
      "sha256",
      record,
      publicKey,
      Buffer.from(approve.inputs.approval_signature, "base64"),
    ),
    "the record verifies against the deployment's key",
  ).toBe(true);

  // A second press while the record is good is refused, and sends nothing.
  const again = await page.request.post(
    "/api/repos/acme/widgets/pending-releases/v2.9.1/approve",
    { data: {} },
  );
  expect(again.status()).toBe(400);
  expect(await again.text()).toContain("v2.9.1 was already answered");
  expect(await dispatches()).toHaveLength(before + 1);

  // Reject the minor release from the Inbox.
  const minorRow = section
    .getByTestId("inbox-pending-release")
    .filter({ hasText: "v2.10.0 is ready to promote" });
  await expect(minorRow).toBeVisible();
  page.once("dialog", (dialog) => dialog.accept());
  await minorRow.getByRole("button", { name: "Reject" }).click();
  await expect(minorRow).toHaveCount(0);
  sent = await dispatches();
  expect(sent).toHaveLength(before + 2);
  expect(sent[sent.length - 1].inputs).toMatchObject({
    version: "v2.10.0",
    decision: "reject",
  });

  // A pending release staged because it carries breaking changes outside
  // RELEASE_MAJOR, whose earlier promote-release.yml dispatch failed, is
  // listed under "Asked: approve a release" with the failed run and what to
  // set (GRAIN_RELEASE_APPROVAL_KEY from GET /api/release-approval-key),
  // shows BREAKING beside Approve breaking, asks a confirm naming the
  // breaking changes, and dispatches allow_breaking=true signed.
  await ok(
    await page.request.post(`${SIM}/git/refs`, {
      data: { ref: "refs/tags/v3.2.0-pending", sha: commit },
    }),
    "tag v3.2.0-pending",
  );
  await ok(
    await page.request.post("/api/notifications", {
      data: {
        subject: "release.pending:v3.2.0",
        severity: "critical",
        title: "v3.2.0 is ready to promote (approve dispatch failed)",
        body:
          "DISPATCH FAILED (gha:acme:widgets:42577215514): verify-release-approval: refused: the repository variable GRAIN_RELEASE_APPROVAL_KEY is not set\n" +
          "Remedy: set the repository variable GRAIN_RELEASE_APPROVAL_KEY on acme/widgets to the publicKey from GET /api/release-approval-key (or `grain release approval-key`), then approve or reject again.\n\n" +
          "v3.2.0 is staged as v3.2.0-pending on acme/widgets and waits for a person.\n\n" +
          "BREAKING CHANGES: v3.2.0 carries breaking changes since v3.1.1 and is not the major release RELEASE_MAJOR asks for; approving it needs allow_breaking: true.",
        repo: "acme/widgets",
        actions: ["approve-release", "reject-release"],
      },
    }),
    "raise v3.2.0 failed dispatch",
  );

  await page.reload();
  await expect(section).toBeVisible();
  const breakingRow = section
    .getByTestId("inbox-pending-release")
    .filter({ hasText: "v3.2.0 is ready to promote" });
  await expect(breakingRow).toBeVisible();
  const failureBanner = breakingRow.getByTestId(
    "inbox-release-dispatch-failure",
  );
  await expect(failureBanner).toContainText("gha:acme:widgets:42577215514");
  await expect(failureBanner).toContainText(
    "verify-release-approval: refused: the repository variable GRAIN_RELEASE_APPROVAL_KEY is not set",
  );
  await expect(failureBanner).toContainText("GET /api/release-approval-key");
  await failureBanner
    .getByRole("button", { name: "Show approval key" })
    .click();
  await expect(failureBanner.locator(".inbox-approval-key")).toContainText(
    "BEGIN PUBLIC KEY",
  );
  await expect(
    breakingRow.getByRole("checkbox", { name: /Allow breaking changes/i }),
  ).toBeChecked();
  const badge = breakingRow.getByTestId("inbox-release-soak");
  await expect(badge).toHaveText("BREAKING");
  const approveBreakingBtn = breakingRow.getByRole("button", {
    name: "Approve breaking",
  });
  await expect(approveBreakingBtn).toBeVisible();

  // Badge sits left of Approve breaking inside .inbox-actions and vertically centred.
  const actionsBox = await breakingRow.locator(".inbox-actions").boundingBox();
  const badgeBox = await badge.boundingBox();
  const btnBox = await approveBreakingBtn.boundingBox();
  expect(badgeBox.x).toBeGreaterThanOrEqual(actionsBox.x - 1);
  expect(badgeBox.x + badgeBox.width).toBeLessThanOrEqual(btnBox.x + 1);
  expect(
    Math.abs(badgeBox.y + badgeBox.height / 2 - (btnBox.y + btnBox.height / 2)),
  ).toBeLessThan(4);

  // On phone width, BREAKING takes its own right-aligned line above the
  // action buttons and the page does not overflow horizontally.
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(badge).toBeVisible();
  const phoneBadge = await badge.boundingBox();
  const phoneBtn = await approveBreakingBtn.boundingBox();
  expect(phoneBadge.y + phoneBadge.height).toBeLessThanOrEqual(phoneBtn.y + 2);
  expect(phoneBtn.x + phoneBtn.width).toBeLessThanOrEqual(390 + 1);
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  expect(overflow).toBeLessThanOrEqual(1);
  await page.setViewportSize({ width: 1440, height: 900 });

  let breakingDialog = "";
  page.once("dialog", (dialog) => {
    breakingDialog = dialog.message();
    dialog.accept();
  });
  await approveBreakingBtn.click();
  await expect(breakingRow).toHaveCount(0);
  expect(breakingDialog).toContain(
    "Approve v3.2.0 with BREAKING changes outside a major release?",
  );
  expect(breakingDialog).toContain(
    "v3.2.0 carries breaking changes since v3.1.1 and is not the major release RELEASE_MAJOR asks for",
  );

  sent = await dispatches();
  expect(sent).toHaveLength(before + 3);
  const breakingApprove = sent[sent.length - 1];
  expect(breakingApprove).toMatchObject({
    workflow: "promote-release.yml",
    ref: "main",
    inputs: {
      version: "v3.2.0",
      decision: "approve",
      short_soak_ok: "false",
      allow_breaking: "true",
    },
  });
  const breakingRecord = Buffer.from(breakingApprove.inputs.approval, "base64");
  expect(breakingRecord.toString()).toContain(
    `repo: acme/widgets\nversion: v3.2.0\ndecision: approve\ncommit: ${commit}\n`,
  );
  expect(breakingRecord.toString()).toContain("allow_breaking: true\n");
  expect(
    crypto.verify(
      "sha256",
      breakingRecord,
      publicKey,
      Buffer.from(breakingApprove.inputs.approval_signature, "base64"),
    ),
    "the allow_breaking record verifies against the deployment's key",
  ).toBe(true);
});
