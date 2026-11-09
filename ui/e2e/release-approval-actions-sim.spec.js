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

  // Stage v3.2.0-pending with breaking changes and a failed promote-release.yml
  // dispatch: the Inbox shows the failed run, GRAIN_RELEASE_APPROVAL_KEY +
  // GET /api/release-approval-key, lets the operator inspect the key, and
  // approves with allow_breaking=true.
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
          "v3.2.0 is staged as v3.2.0-pending on acme/widgets.\n\n" +
          "Release notes (docs/releases/v3.2.0.md):\n\n# v3.2.0\n\n## Breaking changes\n\n- feat!: drop v1 protocol",
        repo: "acme/widgets",
        actions: ["approve-release", "reject-release"],
      },
    }),
    "raise v3.2.0 failed dispatch",
  );
  await page.reload();
  const v320Row = section
    .getByTestId("inbox-pending-release")
    .filter({ hasText: "v3.2.0 is ready to promote" });
  await expect(v320Row).toBeVisible();
  const failureBanner = v320Row.getByTestId("inbox-release-dispatch-failure");
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
    v320Row.getByRole("checkbox", { name: /Allow breaking changes/i }),
  ).toBeChecked();

  // Verify the expanded key and failure banner fit inside a 390px phone viewport without horizontal scroll.
  await page.setViewportSize({ width: 390, height: 844 });
  const { scrollWidth, clientWidth } = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
  expect(scrollWidth).toBeLessThanOrEqual(clientWidth + 1);
  await page.setViewportSize({ width: 1440, height: 900 });

  page.once("dialog", (dialog) => dialog.accept());
  await v320Row.getByRole("button", { name: "Approve" }).click();
  await expect(v320Row).toHaveCount(0);
  sent = await dispatches();
  expect(sent).toHaveLength(before + 3);
  expect(sent[sent.length - 1].inputs).toMatchObject({
    version: "v3.2.0",
    decision: "approve",
    allow_breaking: "true",
    short_soak_ok: "false",
  });
});
