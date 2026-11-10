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

// A promote-release.yml run that fails after a person's Approve, driven
// through `grain demo -github-sim -fake-ci` end to end
// (docs/test-runs/2026-11-10-release-train.md §5's follow-up): the Inbox's
// Approve dispatches the run, the sim finishes that same run with
// conclusion failure and verify-release-approval's refusal in its log
// (POST /api/e2e/github-sim/.../actions/runs/{id}/complete), and the
// release-pending reconciler, polling every few seconds while a decision
// is in flight, marks the decision failed and raises release.pending
// again -- critical, under "Asked", with the run, the reason and the
// GET /api/release-approval-key remedy -- in the Inbox and the alerts bell
// alike. Approve then dispatches again at once rather than being refused
// for the 24 hours an answered release is held.
// release-approval-actions-sim.spec.js seeds that failed state as a
// notification; this one reaches it the way a deployment does.

import { expect, test } from "./fixtures.js";
import { alertRow, alertsBell, closeAlerts, openAlerts } from "./alerts.js";

// -reconcile runs the release-pending reconciler against the sim, as the
// daemon's loop does; it is also what keeps this file on its own server.
test.use({
  demoArgs: [
    "-github-sim",
    "-fake-ci",
    "-default-target-repo",
    "acme/widgets",
    "-reconcile",
    "1s",
  ],
});
test.setTimeout(120_000);

const SIM = "/api/e2e/github-sim/repos/acme/widgets";
const REFUSAL =
  "verify-release-approval: refused: the repository variable GRAIN_RELEASE_APPROVAL_KEY is not set, so no approval from grain's Inbox can be verified: set it to the deployment's key (`grain release approval-key`)";

test("a promote-release.yml run that fails after Approve is marked failed, raised again and retried at once", async ({
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
  const openRelease = async () => {
    const open = await ok(
      await page.request.get("/api/notifications?open=1"),
      "notifications",
    );
    return open.find((n) => n.subject === "release.pending:v2.9.1") || null;
  };
  const shoot = async (name) => {
    if (process.env.RELEASE_DISPATCH_FAILURE_SHOTS)
      await page.screenshot({
        path: `${process.env.RELEASE_DISPATCH_FAILURE_SHOTS}/${name}.png`,
        fullPage: true,
      });
  };

  // v2.9.1-pending, as the release train would have pushed it at main,
  // and the notification it raises.
  const [main] = await ok(
    await page.request.get(`${SIM}/git/matching-refs/heads/main`),
    "main",
  );
  await ok(
    await page.request.post(`${SIM}/git/refs`, {
      data: { ref: "refs/tags/v2.9.1-pending", sha: main.object.sha },
    }),
    "tag v2.9.1-pending",
  );
  await ok(
    await page.request.post("/api/notifications", {
      data: {
        subject: "release.pending:v2.9.1",
        severity: "warning",
        title: "v2.9.1 is ready to promote",
        body: "v2.9.1 is staged as v2.9.1-pending on acme/widgets and waits for a person.",
        repo: "acme/widgets",
        actions: ["approve-release", "reject-release"],
      },
    }),
    "raise v2.9.1",
  );
  const before = (await dispatches()).length;

  // Approve from the Inbox: one dispatch, its run queued in the sim.
  await page.goto("/inbox");
  const section = page.getByTestId("inbox-pending-releases");
  const row = section
    .getByTestId("inbox-pending-release")
    .filter({ hasText: "v2.9.1 is ready to promote" });
  await expect(row).toBeVisible();
  await shoot("1-pending");
  page.once("dialog", (dialog) => dialog.accept());
  await row.getByRole("button", { name: "Approve" }).click();
  await expect(row).toHaveCount(0);
  let sent = await dispatches();
  expect(sent).toHaveLength(before + 1);
  const approve = sent[sent.length - 1];
  expect(approve.inputs).toMatchObject({
    version: "v2.9.1",
    decision: "approve",
  });
  expect(approve.runId).toBeGreaterThan(0);
  const run = await ok(
    await page.request.get(`${SIM}/actions/runs/${approve.runId}`),
    "the dispatched run",
  );
  expect(run).toMatchObject({
    event: "workflow_dispatch",
    status: "queued",
    head_branch: "main",
  });
  // While the run is queued, nothing is raised again.
  expect(await openRelease()).toBeNull();

  // The sim finishes that run as GitHub would with the repository
  // variable unset: the approve job refused, the run a failure.
  const completed = await ok(
    await page.request.post(`${SIM}/actions/runs/${approve.runId}/complete`, {
      data: {
        conclusion: "failure",
        jobs: [
          {
            name: "approve the pending release",
            log:
              "2026-11-10T11:30:01.0000000Z promote-release: approving v2.9.1\n" +
              `2026-11-10T11:30:02.0000000Z ##[error]${REFUSAL}\n` +
              "2026-11-10T11:30:02.1000000Z ##[error]Process completed with exit code 1.\n",
          },
        ],
      },
    }),
    "complete the run",
  );
  expect(completed).toMatchObject({
    id: approve.runId,
    status: "completed",
    conclusion: "failure",
  });
  const failedAt = Date.now();
  const runRef = `gha:acme:widgets:${approve.runId}`;

  // Within seconds -- the reconciler's 5s poll while a decision is in
  // flight, not its 10-minute idle one -- the decision is marked failed
  // and release.pending is open again, critical, naming the run, the
  // refusal and the remedy. (The title's "approve dispatch failed" and
  // the failure block are written in the same pass that marks the stored
  // decision failed; the retry below is what proves the decision is no
  // longer holding the release.)
  let raised = null;
  await expect
    .poll(
      async () => {
        raised = await openRelease();
        return raised?.title || "";
      },
      { timeout: 20_000, intervals: [250] },
    )
    .toBe("v2.9.1 is ready to promote (approve dispatch failed)");
  const reRaisedIn = Date.now() - failedAt;
  expect(reRaisedIn, "re-raised within seconds").toBeLessThan(15_000);
  test.info().annotations.push({
    type: "re-raised after",
    description: `${reRaisedIn} ms`,
  });
  expect(raised.severity).toBe("critical");
  expect(raised.body).toContain(`DISPATCH FAILED (${runRef}): ${REFUSAL}`);
  expect(raised.body).toContain(
    "Remedy: set the repository variable GRAIN_RELEASE_APPROVAL_KEY on acme/widgets to the publicKey from GET /api/release-approval-key",
  );
  expect(raised.body).toContain(
    `Run URL: https://github.example/acme/widgets/actions/runs/${approve.runId}`,
  );

  // Under Asked in the Inbox, with the run id, the reason and the remedy.
  await page.reload();
  await expect(section).toBeVisible();
  await expect(row).toBeVisible();
  const failure = row.getByTestId("inbox-release-dispatch-failure");
  await expect(failure).toContainText(runRef);
  await expect(failure).toContainText(REFUSAL);
  await expect(failure).toContainText("GET /api/release-approval-key");
  await shoot("2-failed-inbox");

  // The bell says the same: the same release, critical, the same run and
  // reason, and the same Approve.
  await expect(alertsBell(page)).toHaveAttribute("data-severity", "critical");
  await openAlerts(page);
  const alert = alertRow(
    page,
    "v2.9.1 is ready to promote (approve dispatch failed)",
  );
  await expect(alert).toHaveCount(1);
  await expect(alert).toContainText(`DISPATCH FAILED (${runRef})`);
  await expect(alert).toContainText("verify-release-approval: refused");
  await expect(alert.getByRole("button", { name: "Approve" })).toBeVisible();
  await shoot("3-failed-bell");
  await closeAlerts(page);

  // Retry: Approve again dispatches again now, not after the 24 hours an
  // answered release is otherwise held.
  page.once("dialog", (dialog) => dialog.accept());
  await row.getByRole("button", { name: "Approve" }).click();
  await expect(row).toHaveCount(0);
  sent = await dispatches();
  expect(sent).toHaveLength(before + 2);
  const retry = sent[sent.length - 1];
  expect(retry.inputs).toMatchObject({
    version: "v2.9.1",
    decision: "approve",
  });
  expect(retry.runId).not.toBe(approve.runId);
  // And that retry holds the release as the first Approve did: a third
  // press is refused while its run is in flight.
  const again = await page.request.post(
    "/api/repos/acme/widgets/pending-releases/v2.9.1/approve",
    { data: {} },
  );
  expect(again.status()).toBe(400);
  expect(await again.text()).toContain("v2.9.1 was already answered");
  expect(await openRelease()).toBeNull();
  await shoot("4-retried");
});
