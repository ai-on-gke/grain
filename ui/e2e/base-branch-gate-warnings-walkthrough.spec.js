import { test, expect } from "./fixtures.js";

// docs/test-plans/2026-10-11.md §10, walked in
// docs/test-runs/2026-10-12-base-gate-warnings.md: 7b93861a0 took
// unjudged base-branch gates out of the Inbox and the rail's count and
// put them on the repository's own page and its Releases. These drive a
// real unjudged gate on `grain demo -github-sim` through both, judge it
// from the page.
//
// The gate is the one unjudged finding a demo can produce in real time
// (pkg/ui/basebranchgate.go): acme/widgets' default branch watched while
// its CI Mode is GitHub's, so grain CI never enrols it. Red base CI is a
// judged branch, and the other unjudged finding needs a run two hours
// old.
test.setTimeout(120_000);
// A server of its own (a demoArgs no other file uses): these watch
// acme/widgets' default branch and flip its CI Mode, both of which
// other files on a shared -github-sim server would count.
test.use({ demoArgs: ["-github-sim", "-open=false"] });

const repo = "acme/widgets";
const gradeRadio = /Make grain CI the gate for acme\/widgets/;

async function makeUnjudgedGate(request) {
  // Setup through the endpoints the repo page's own switches write.
  const mode = await request.put(`/api/repos/${repo}/ci-mode`, {
    data: { ciMode: "github" },
  });
  expect(mode.ok(), await mode.text()).toBeTruthy();
  const watch = await request.put(`/api/repos/${repo}/watch-default-branch`, {
    data: { watchDefaultBranch: true },
  });
  expect(watch.ok(), await watch.text()).toBeTruthy();
  const gates = await (await request.get("/api/base-branch-gates")).json();
  expect(gates.gates).toEqual([
    expect.objectContaining({ repo, judged: false, reason: "grain-ci-off" }),
  ]);
}

// The rail's chip says exactly the rows the page shows: no gate row, and
// no gate in the number.
async function expectInboxWithoutGate(page) {
  await page.goto("/inbox");
  await expect(page.getByRole("heading", { name: "Inbox" })).toBeVisible();
  await expect(
    page.getByText(/Nothing is testing|nothing is judging/),
  ).toHaveCount(0);
  await expect(page.locator(".inbox-gate-repo")).toHaveCount(0);
  const rows = await page.locator(".inbox-item").count();
  await expect(
    page.getByRole("button", { name: /^Inbox/ }),
  ).toHaveAccessibleName(rows ? new RegExp(`^Inbox\\s*${rows}$`) : /^Inbox$/);
}

const headerGate = (page) =>
  page.locator(".repo-page-header .MuiAlert-root", {
    hasText: /Nothing is testing/i,
  });
// The release pane further down said the same sentence again until
// finding 7 of the walkthrough; an unjudged gate is the header's alone.
const paneGate = (page) =>
  page.locator(".repo-page-body .MuiAlert-root", {
    hasText: "Nothing is testing",
  });
const releasesGate = (page) =>
  page.locator("main .MuiAlert-root", { hasText: "Nothing is testing" });

test("an admin finds an unjudged gate on the repo and its Releases, not the inbox, and judging it clears it", async ({
  page,
}) => {
  await makeUnjudgedGate(page.request);
  await expectInboxWithoutGate(page);

  await page.goto(`/repos/${repo}`);
  await expect(headerGate(page)).toHaveCount(1);
  // Read as the server wrote it: the header lowercased it before
  // (".repo-page-header" is text-transform: lowercase), and the sentence
  // named a capability no picker will attach.
  const said = await headerGate(page).innerText();
  expect(said).toContain("Nothing is testing acme/widgets's default branch,");
  expect(said).toContain("grain CI is not enabled");
  expect(said).toContain('CI Mode to "Use Grain CI"');
  expect(said).not.toContain("Attaching the grain-ci capability");
  await expect(page.getByText(/Nothing is testing/i)).toHaveCount(1);
  await expect(paneGate(page)).toHaveCount(0);

  // Releases: the same warning, and its way back to where it is fixed.
  await page.getByRole("button", { name: "Releases", exact: true }).click();
  await expect(page).toHaveURL(/\/repos\/acme\/widgets\/releases/);
  await expect(releasesGate(page)).toHaveCount(1);
  await releasesGate(page)
    .getByRole("button", { name: "Open repository settings" })
    .click();
  await expect(page).toHaveURL(/\/repos\/acme\/widgets$/);

  // The overview's link is to CI Mode, the switch that judges the branch.
  await headerGate(page).getByRole("button", { name: "Go to CI Mode" }).click();
  const radio = page.getByRole("radio", { name: gradeRadio });
  await expect(radio).toBeInViewport();
  await radio.click();
  await expect(radio).toBeChecked();

  // Cleared at once rather than on the shell's minute-long gate poll.
  await expect(headerGate(page)).toHaveCount(0, { timeout: 10_000 });
  await expect(paneGate(page)).toHaveCount(0);
  const gates = await (await page.request.get("/api/base-branch-gates")).json();
  expect(gates.gates).toEqual([]);
  await page.getByRole("button", { name: "Releases", exact: true }).click();
  await expect(page).toHaveURL(/\/repos\/acme\/widgets\/releases/);
  await expect(page.getByText(/has no releases yet/)).toBeVisible();
  await expect(releasesGate(page)).toHaveCount(0);
  await expectInboxWithoutGate(page);
});
