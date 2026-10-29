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

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import CIPage, {
  ACTIVE_SANDBOXES_STORAGE_KEY,
  activeSandboxes,
  activeSandboxesSummary,
  cacheSummary,
  conclusionChip,
  formatBytes,
  formatDuration,
  jobSummary,
  progressChip,
  requesterLabel,
  retryRequest,
  RunDetail,
  SHARED_FAULTS_STORAGE_KEY,
  newSharedFaults,
  sharedFaultKey,
  sortJobs,
  spanOf,
} from "./CIPage.jsx";
import CIRunOverlay from "./CIRunOverlay.jsx";
import { formatDateTime } from "../time.js";
import { RUN_POLL_LIMIT, WINDOWED_RUN_POLL_LIMIT } from "../ciFilters.js";
import api from "../api.js";

vi.mock("../api.js", async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, default: vi.fn() };
});

const STARTED = "2026-09-07T12:00:00Z";

// isRunListing is "this call is the pane's poll of the runs list" --
// GET /api/ci/runs, which carries a query string now (the limit it is
// bounded by, and the window a narrowing asks to look back over:
// ciFilters.js's runsQuery, grain/task-289) and which has to stay
// distinguishable from GET /api/ci/runs/{id} beside it.
const isRunListing = (path) =>
  path === "/api/ci/runs" || path.startsWith("/api/ci/runs?");

// The two toolbar buttons these tests drive the narrowing through. They
// are NarrowingBar's own (NarrowingBar.test.jsx uses the same titles):
// the CI pane wears the toolbar every task view wears, over the CI
// vocabulary (ciFilters.js).
const CI_FILTER_BUTTON = "Add and edit the conditions this list is narrowed by";
const CI_SORT_BUTTON = "Choose the order this list is shown in";
// Go's zero time.Time as the browser used to receive it. grain does not
// send it any more -- ci.Run's timestamps are optional, so a time
// nobody has is a field that is not there -- and instantOf still
// refuses it, which is what these exercise.
const ZERO_TIME = "0001-01-01T00:00:00Z";

// CIPage is controlled: which run is open is App's state and the URL's
// (App.jsx's openCIRunId, /ci/<run>), so these tests hold it the way App
// does. `runId` is where the pane opens -- what a /ci/<run> link, or a
// task's own "Show the run", hands it.
function CIPane({ runId = null, ...props }) {
  const [open, setOpen] = useState(runId);
  return (
    <>
      <CIPage onOpenRun={setOpen} {...props} />
      {open && (
        <CIRunOverlay
          runId={open}
          onClose={() => setOpen(null)}
          onOpenRun={setOpen}
          showError={props.showError}
        />
      )}
    </>
  );
}

// failedRun is one finished run with one failing job in it, which is the
// case everybody who opens this pane is here for.
function failedRun() {
  return {
    id: "9f2c1a",
    repo: "acme/widgets",
    ref: "grain/task-1",
    commit: "0123456789abcdef0123",
    event: "push",
    requestedBy: "alice",
    startedAt: STARTED,
    finishedAt: "2026-09-07T12:01:23Z",
    conclusion: "failure",
    jobs: [
      {
        workflow: "tests",
        workflowPath: "tests.yml",
        jobId: "lint",
        name: "lint",
        conclusion: "success",
        cached: true,
        startedAt: STARTED,
        finishedAt: "2026-09-07T12:00:10Z",
        steps: [
          { name: "golangci-lint", conclusion: "success", output: "0 issues" },
        ],
      },
      {
        workflow: "tests",
        workflowPath: "tests.yml",
        jobId: "test",
        name: "go test",
        conclusion: "failure",
        sandbox: "ci-9f2c1a-tests-test",
        startedAt: STARTED,
        finishedAt: "2026-09-07T12:01:20Z",
        steps: [
          { name: "build", conclusion: "success", output: "ok" },
          {
            name: "go test ./...",
            conclusion: "failure",
            exitCode: 1,
            output: "--- FAIL: TestThing",
          },
          { name: "upload", conclusion: "skipped", exitCode: -1 },
        ],
      },
    ],
  };
}

function githubFault(digest) {
  return {
    source: "github",
    repo: "acme/widgets",
    jobId: `CI / ${digest}`,
    jobName: `CI / ${digest}`,
    digest,
    refs: ["grain/task-10", "grain/task-11"],
    runs: [],
  };
}

describe("CIPage", () => {
  afterEach(() => {
    api.mockReset();
    vi.useRealTimers();
    // The pane's narrowing is kept in this browser so that returning to
    // it does not reset the question (ciFilters.js). One test's question
    // is not the next one's.
    localStorage.clear();
  });

  it("lists the runs a deployment holds, newest first as the daemon sends them", async () => {
    api.mockResolvedValue({ enabled: true, runs: [failedRun()] });
    render(<CIPane showError={() => {}} />);

    expect(await screen.findByText("acme/widgets")).toBeInTheDocument();
    expect(screen.getByText("grain/task-1")).toBeInTheDocument();
    // The commit under the branch: a branch moves on, and what was
    // tested is the pair.
    expect(screen.getByText("0123456789ab")).toHaveAttribute(
      "title",
      "0123456789abcdef0123",
    );
    expect(screen.getByText("Failure")).toBeInTheDocument();
    expect(screen.getByText("push")).toBeInTheDocument();
    // How long it took, and how far it got, which is what a row is for.
    expect(screen.getByText("1m 23s")).toBeInTheDocument();
    expect(screen.getByText(/1 failure/)).toHaveTextContent("1 cache hit");
  });

  // grain/task-296: a red that is not this branch's own says so on the
  // branch's own row, and names the change that fixes it. Six branches
  // once failed with one proxy error and every reader of every row had
  // to work that out for themselves.
  it("says on a row when its red is a fault several branches share, and names the fix", async () => {
    const run = failedRun();
    api.mockResolvedValue({
      enabled: true,
      runs: [run],
      faults: [
        {
          repo: "acme/widgets",
          jobId: "go",
          jobName: "go",
          step: "go test ./...",
          digest: "d1",
          refs: [run.ref, "grain/task-7", "grain/task-9"],
          runs: [
            { runId: run.id, ref: run.ref, at: STARTED },
            { runId: "other-1", ref: "grain/task-7", at: STARTED },
            { runId: "other-2", ref: "grain/task-9", at: STARTED },
          ],
          paths: ["scripts/build.sh"],
          fixes: [
            {
              pullRequest: "acme/widgets#26",
              task: "281",
              title: "Point the mirror back at http",
              files: ["scripts/build.sh"],
            },
          ],
        },
      ],
    });
    render(<CIPane showError={() => {}} />);

    const note = await screen.findByText(/failing the same way on 2 other/);
    // The evidence -- which branches, which step -- is on the hover
    // rather than on the row, since six branch names would be unreadable
    // in the one table where six branches are the point.
    expect(note).toHaveAttribute(
      "title",
      expect.stringContaining("grain/task-7, grain/task-9"),
    );
    const fix = screen.getByRole("link", { name: "acme/widgets#26" });
    expect(fix).toHaveAttribute(
      "href",
      "https://github.com/acme/widgets/pull/26",
    );
    expect(screen.getByText(/touches scripts\/build\.sh/)).toBeInTheDocument();
    expect(screen.getByTestId("ci-fault-source-grain")).toHaveTextContent(
      "grain CI",
    );
  });

  it("shows GitHub CI shared faults labelled by source", async () => {
    api.mockResolvedValue({
      enabled: true,
      runs: [failedRun()],
      faults: [
        {
          source: "github",
          repo: "acme/widgets",
          jobId: "CI / test",
          jobName: "CI / test",
          step: "Run go test ./...",
          digest: "gh-d1",
          refs: ["grain/task-10", "grain/task-11", "grain/task-12"],
          runs: [
            { runId: "9001", ref: "grain/task-10", at: STARTED },
            { runId: "9002", ref: "grain/task-11", at: STARTED },
            { runId: "9003", ref: "grain/task-12", at: STARTED },
          ],
          paths: ["pkg/ci/sharedfault.go"],
          fixes: [
            {
              pullRequest: "acme/widgets#99",
              task: "99",
              title: "Fix GitHub normalizer",
              files: ["pkg/ci/sharedfault.go"],
            },
          ],
        },
      ],
    });
    render(<CIPane showError={() => {}} />);

    expect(
      await screen.findByTestId("ci-shared-faults-section"),
    ).toBeInTheDocument();
    expect(screen.getByTestId("ci-fault-source-github")).toHaveTextContent(
      "GitHub CI",
    );
    expect(
      screen.getByText(/acme\/widgets .* CI \/ test \(Run go test/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/failing the same way on 3 branches right now/),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "acme/widgets#99" }),
    ).toHaveAttribute("href", "https://github.com/acme/widgets/pull/99");
  });

  // grain/task-1760: the API names the task a shared fault is assigned
  // to (ownerTaskId, cifault.ResolveOwner) and the CLI and agents are told
  // it, but this page dropped it, so a reader could not tell who was
  // fixing the failure every branch was hitting.
  it("names and links the task a shared fault is assigned to", async () => {
    api.mockResolvedValue({
      enabled: true,
      runs: [],
      faults: [
        {
          ...githubFault("gh-owned"),
          ownerTaskId: "11",
          ownerRef: "grain/task-11",
        },
        githubFault("gh-unowned"),
      ],
    });
    const onOpenTask = vi.fn();
    render(<CIPane showError={() => {}} onOpenTask={onOpenTask} />);

    const owners = await screen.findAllByTestId("ci-fault-owner");
    expect(owners).toHaveLength(1);
    expect(owners[0]).toHaveTextContent("Assigned to task #11 (grain/task-11)");
    const link = screen.getByRole("link", { name: "task #11" });
    expect(link).toHaveAttribute("href", "/tasks/11");
    await userEvent.click(link);
    expect(onOpenTask).toHaveBeenCalledWith("11");
  });

  // grain/task-1520: the box folds away behind a header that still says
  // how many, and the choice outlives a reload and the page's polling.
  it("collapses the shared faults box and remembers it across a re-mount", async () => {
    api.mockResolvedValue({
      enabled: true,
      runs: [],
      faults: [githubFault("gh-d1"), githubFault("gh-d2")],
    });
    const user = userEvent.setup();
    const first = render(<CIPane showError={() => {}} />);
    const toggle = await screen.findByRole("button", {
      name: /2 shared failures/,
    });
    // A first-time visitor sees it open, as before.
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(toggle).toHaveAttribute("aria-controls", "ci-shared-faults-list");
    expect(localStorage.getItem(SHARED_FAULTS_STORAGE_KEY)).toBeNull();

    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    await waitFor(() =>
      expect(
        screen.getAllByTestId("ci-shared-fault-github")[0],
      ).not.toBeVisible(),
    );
    expect(
      JSON.parse(localStorage.getItem(SHARED_FAULTS_STORAGE_KEY)).seen,
    ).toHaveLength(2);
    expect(screen.queryByTestId("ci-shared-faults-new")).toBeNull();
    first.unmount();

    render(<CIPane showError={() => {}} />);
    const reopened = await screen.findByRole("button", {
      name: /2 shared failures/,
    });
    expect(reopened).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByTestId("ci-shared-faults-new")).toBeNull();

    // The header is a real button: Enter on it opens the box again.
    reopened.focus();
    await user.keyboard("{Enter}");
    expect(reopened).toHaveAttribute("aria-expanded", "true");
    expect(localStorage.getItem(SHARED_FAULTS_STORAGE_KEY)).toBeNull();
  });

  // Collapsing never hides a fresh failure: one that was not there when
  // the box was folded is counted on the header, and the box stays shut.
  it("marks a fault that arrived after the box was collapsed as new", async () => {
    localStorage.setItem(
      SHARED_FAULTS_STORAGE_KEY,
      JSON.stringify({ seen: [sharedFaultKey(githubFault("gh-d1"))] }),
    );
    api.mockResolvedValue({
      enabled: true,
      runs: [],
      faults: [githubFault("gh-d1"), githubFault("gh-d2")],
    });
    render(<CIPane showError={() => {}} />);

    const toggle = await screen.findByRole("button", {
      name: /2 shared failures/,
    });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(await screen.findByTestId("ci-shared-faults-new")).toHaveTextContent(
      "1 new",
    );

    // Opening it acknowledges them; folding it again starts afresh.
    const user = userEvent.setup();
    await user.click(toggle);
    expect(screen.queryByTestId("ci-shared-faults-new")).toBeNull();
    await user.click(toggle);
    expect(screen.queryByTestId("ci-shared-faults-new")).toBeNull();
  });

  it("newSharedFaults is the faults a collapsed box has not seen", () => {
    const a = githubFault("a");
    const b = { ...githubFault("b"), source: "grain" };
    expect(newSharedFaults([a, b], [sharedFaultKey(a)])).toEqual([b]);
    expect(newSharedFaults([a, b], [])).toEqual([a, b]);
    expect(newSharedFaults([], [sharedFaultKey(a)])).toEqual([]);
  });

  // Nothing is said about a run grain has drawn no conclusion about --
  // which is every run on a deployment that is merely red in one place.
  it("says nothing on a row that is in no shared fault", async () => {
    api.mockResolvedValue({ enabled: true, runs: [failedRun()], faults: [] });
    render(<CIPane showError={() => {}} />);

    expect(await screen.findByText("acme/widgets")).toBeInTheDocument();
    expect(screen.queryByText(/failing the same way/)).not.toBeInTheDocument();
  });

  // grain/task-372: the pane wears the jaws its rail entry has always
  // carried, and an open run says which branch it tested with the
  // branches figure -- a ref beside a repo name reads as either a
  // branch or a tag.
  it("heads the pane with the CI figure, and an open run's ref with the branch one", async () => {
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path)
          ? { enabled: true, runs: [failedRun()] }
          : failedRun(),
      ),
    );
    const { container } = render(<CIPane showError={() => {}} />);

    expect(await screen.findByText("acme/widgets")).toBeInTheDocument();
    const heading = screen.getByRole("heading", { name: "CI" });
    expect(heading.closest(".content-header")).toBeInTheDocument();
    expect(
      screen
        .getByRole("button", { name: "Start a run" })
        .closest(".content-header"),
    ).toBe(heading.closest(".content-header"));
    expect(
      screen.getByText("acme/widgets").closest(".standalone-page"),
    ).toBeInTheDocument();
    expect(container.querySelector('svg[data-glyph="ci"]')).toBeInTheDocument();
    expect(container.querySelector('svg[data-glyph="branches"]')).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "acme/widgets" }));

    expect(
      (await screen.findByText("← Back")).closest("div"),
    ).toBeInTheDocument();
    expect(
      screen
        .getAllByText(/grain\/task-1/)
        .find((node) => node.closest(".glyph-label"))
        .closest(".glyph-label")
        .querySelector('svg[data-glyph="branches"]'),
    ).toBeInTheDocument();
  });

  it("says a deployment runs no CI rather than showing an empty list", async () => {
    api.mockResolvedValue({ enabled: false });
    render(<CIPane showError={() => {}} />);

    expect(await screen.findByText(/not available/i)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    // The other empty: CI is wired here and nothing has run.
    api.mockResolvedValue({ enabled: true, runs: [] });
    render(<CIPane showError={() => {}} />);
    expect(await screen.findByText(/No CI runs yet/)).toBeInTheDocument();
  });

  it("opens a run onto its jobs, failures first, with the failing step's output showing", async () => {
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path)
          ? { enabled: true, runs: [failedRun()] }
          : failedRun(),
      ),
    );
    render(<CIPane showError={() => {}} />);

    await userEvent.click(
      await screen.findByRole("button", { name: "acme/widgets" }),
    );

    expect(await screen.findByText("go test")).toBeInTheDocument();
    expect(screen.getByText("Cache hit")).toBeInTheDocument();
    // Failures first: the job that failed is what the reader came for.
    const jobs = screen.getAllByRole("button", { name: /tests/ });
    expect(jobs[0]).toHaveTextContent("go test");
    // The failing step's output is on screen without another click, and
    // the green step's is behind one.
    expect(screen.getByText("--- FAIL: TestThing")).toBeInTheDocument();
    expect(screen.queryByText("0 issues")).not.toBeInTheDocument();
    expect(screen.getByText("exit 1")).toBeInTheDocument();
    expect(screen.getByText(/ci-9f2c1a-tests-test/)).toBeInTheDocument();
    expect(screen.getByText(/asked by alice/)).toBeInTheDocument();
    expect(
      screen.getByText(
        new RegExp(
          `started ${formatDateTime(failedRun().startedAt)} · completed ${formatDateTime(failedRun().finishedAt)}`,
        ),
      ),
    ).toBeInTheDocument();

    // And back to the listing.
    await userEvent.click(screen.getByRole("button", { name: /Back/ }));
    expect(await screen.findByRole("table")).toBeInTheDocument();
  });

  it("shows started and completed columns in the run list", async () => {
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path)
          ? { enabled: true, runs: [failedRun()] }
          : failedRun(),
      ),
    );
    render(<CIPane showError={() => {}} />);
    await screen.findByRole("table");

    expect(
      screen.getByRole("columnheader", { name: "Started" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("columnheader", { name: "Completed" }),
    ).toBeInTheDocument();
    expect(
      screen.getAllByText(formatDateTime(failedRun().startedAt)).length,
    ).toBeGreaterThan(0);
    expect(
      screen.getAllByText(formatDateTime(failedRun().finishedAt)).length,
    ).toBeGreaterThan(0);
  });

  // A run that could not get a job the environment it runs in: errored,
  // not failed, and the step that could not is what the reader needs open.
  // Reading "the cache was cold" out of a collapsed disclosure is exactly
  // the work this pane exists to save.
  it("shows the output of a step that could not fetch its job's environment", async () => {
    const cold = failedRun();
    cold.conclusion = "errored";
    cold.jobs[1].conclusion = "errored";
    cold.jobs[1].steps = [
      {
        name: "Resolve the sandbox guest",
        conclusion: "errored",
        exitCode: 1,
        output: "manifest for guest:inputs-deadbeef not found",
      },
      { name: "go test ./...", conclusion: "skipped", exitCode: -1 },
    ];
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path) ? { enabled: true, runs: [cold] } : cold,
      ),
    );
    render(<CIPane showError={() => {}} />);

    await userEvent.click(
      await screen.findByRole("button", { name: "acme/widgets" }),
    );

    expect(
      await screen.findByText("Resolve the sandbox guest"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("manifest for guest:inputs-deadbeef not found"),
    ).toBeInTheDocument();
  });

  it("offers a failing job's artifact as a download, and says why one was not kept", async () => {
    const withArtifacts = failedRun();
    withArtifacts.jobs[1].artifacts = [
      {
        run: "9f2c1a",
        job: "test",
        name: "playwright-test-results",
        stored: true,
        bytes: 3 * 1024 * 1024,
      },
      {
        run: "9f2c1a",
        job: "test",
        name: "core-dumps",
        stored: false,
        note: "280.0MB is over the 32.0MB a single artifact may be, so it was not kept",
      },
    ];
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path)
          ? { enabled: true, runs: [withArtifacts] }
          : withArtifacts,
      ),
    );
    render(<CIPane showError={() => {}} />);

    await userEvent.click(
      await screen.findByRole("button", { name: "acme/widgets" }),
    );

    // The link goes where the bytes are, which is the one route that
    // serves them.
    const link = await screen.findByRole("link", {
      name: "playwright-test-results",
    });
    expect(link).toHaveAttribute(
      "href",
      "/api/ci/runs/9f2c1a/artifacts/test/playwright-test-results",
    );
    expect(screen.getByText(/3\.0 MiB/)).toBeInTheDocument();
    // And the one that kept nothing says so rather than being absent,
    // which is what sends somebody looking for a bug in the feature.
    expect(screen.getByText(/over the 32\.0MB/)).toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: "core-dumps" }),
    ).not.toBeInTheDocument();
  });

  it("shows why a job is waiting before its sandbox is scheduled", async () => {
    const waiting = failedRun();
    waiting.conclusion = "";
    waiting.finishedAt = "";
    waiting.jobs = [
      {
        workflow: "tests",
        workflowPath: "tests.yml",
        jobId: "test",
        name: "go test",
        activity: "waiting for Gemini API keys (gemini-key grant)",
        activityAt: STARTED,
        steps: [],
      },
    ];
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path) ? { enabled: true, runs: [waiting] } : waiting,
      ),
    );
    render(<CIPane runId={waiting.id} showError={() => {}} />);

    expect(
      await screen.findByText("waiting for Gemini API keys (gemini-key grant)"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/^sandbox /)).not.toBeInTheDocument();
    expect(screen.getAllByText("Pending")).not.toHaveLength(0);
    expect(screen.queryByText("Running")).not.toBeInTheDocument();
  });

  it("distinguishes pending CI work from work that has started", () => {
    expect(progressChip({})).toEqual({ label: "Pending", color: "default" });
    expect(progressChip({ startedAt: STARTED })).toEqual({
      label: "Running",
      color: "info",
    });
    expect(progressChip({ startedAt: STARTED, jobs: [{}] }, true)).toEqual({
      label: "Pending",
      color: "default",
    });
    expect(
      progressChip(
        { startedAt: STARTED, jobs: [{ startedAt: STARTED }] },
        true,
      ),
    ).toEqual({ label: "Running", color: "info" });
  });

  // A run the daemon stopped in the middle of is kept rather than lost
  // (grain/task-303), so this pane has to say what it is: not a failure
  // of the branch, and not a verdict -- the jobs that did finish, and a
  // note that CI will be run again.
  it("says a run was interrupted rather than showing it as a plain error", async () => {
    const interrupted = {
      ...failedRun(),
      conclusion: "cancelled",
      interrupted: true,
      error:
        "this run was interrupted: the grain daemon running it stopped before it finished",
    };
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path)
          ? { enabled: true, runs: [interrupted] }
          : interrupted,
      ),
    );
    render(<CIPane showError={() => {}} />);

    await userEvent.click(
      await screen.findByRole("button", { name: "acme/widgets" }),
    );
    expect(await screen.findByText(/was interrupted/)).toBeInTheDocument();
    expect(screen.getByText(/put through CI again/)).toBeInTheDocument();
    // And what it did get through is still there, which is the point of
    // keeping such a run at all.
    expect(screen.getByText("go test")).toBeInTheDocument();
  });

  it("offers to cancel a run that is still going, and not one that is over", async () => {
    const live = {
      id: "abc",
      repo: "acme/widgets",
      ref: "main",
      startedAt: STARTED,
      jobs: [
        {
          jobId: "test",
          name: "test",
          workflowPath: "tests.yml",
          startedAt: STARTED,
        },
      ],
    };
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path) ? { enabled: true, runs: [live] } : live,
      ),
    );
    render(<CIPane showError={() => {}} />);
    await userEvent.click(
      await screen.findByRole("button", { name: "acme/widgets" }),
    );

    // A run with no conclusion is still going and says so rather than
    // leaving a blank where a verdict goes.
    expect(await screen.findAllByText("Running")).not.toHaveLength(0);
    const cancel = screen.getByRole("button", { name: /Cancel run/ });
    // The prompt is the confirmation as well as the question: what is
    // typed is the note, and dismissing it leaves the run alone.
    vi.spyOn(window, "prompt").mockReturnValue("  wrong branch  ");
    await userEvent.click(cancel);
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/ci/runs/abc", {
        method: "DELETE",
        body: JSON.stringify({ reason: "wrong branch" }),
      }),
    );
  });

  // The note is optional in both directions: somebody who has nothing to
  // say still stops the run, and somebody who dismisses the prompt has
  // not asked for anything at all.
  it("cancels with no note, and not at all when the prompt is dismissed", async () => {
    const live = {
      id: "abc",
      repo: "acme/widgets",
      ref: "main",
      startedAt: STARTED,
      jobs: [
        {
          jobId: "test",
          name: "test",
          workflowPath: "tests.yml",
          startedAt: STARTED,
        },
      ],
    };
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path) ? { enabled: true, runs: [live] } : live,
      ),
    );
    render(<CIPane showError={() => {}} />);
    await userEvent.click(
      await screen.findByRole("button", { name: "acme/widgets" }),
    );
    expect(await screen.findAllByText("Running")).not.toHaveLength(0);

    const cancel = screen.getByRole("button", { name: /Cancel run/ });
    vi.spyOn(window, "prompt").mockReturnValue(null);
    await userEvent.click(cancel);
    expect(api).not.toHaveBeenCalledWith(
      "/api/ci/runs/abc",
      expect.objectContaining({ method: "DELETE" }),
    );

    window.prompt.mockReturnValue("");
    await userEvent.click(cancel);
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/ci/runs/abc", {
        method: "DELETE",
        body: JSON.stringify({ reason: "" }),
      }),
    );
  });

  // And whoever opens the run afterwards is told what the person who
  // stopped it said, rather than only that it stopped.
  it("shows the note left on a run somebody cancelled", async () => {
    const cancelled = {
      id: "abc",
      repo: "acme/widgets",
      ref: "main",
      startedAt: STARTED,
      finishedAt: "2026-09-07T12:00:30Z",
      conclusion: "cancelled",
      cancelReason: "the base branch is being reverted",
      jobs: [],
    };
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path) ? { enabled: true, runs: [cancelled] } : cancelled,
      ),
    );
    render(<CIPane showError={() => {}} />);
    await userEvent.click(
      await screen.findByRole("button", { name: "acme/widgets" }),
    );

    expect(
      await screen.findByText(/the base branch is being reverted/),
    ).toBeInTheDocument();
  });

  // Which build of grain produced the run, which is a different
  // question from which commit it tested: a run that behaves unlike the
  // code somebody is reading is a deployment running older code at least
  // as often as it is a bug, and this is the only thing on the page that
  // can say which (ci.Build, grain/task-388).
  it("says which build of grain produced the run, and nothing when it cannot", async () => {
    const built = {
      ...failedRun(),
      build: {
        revision: "acc97d35b0f1e2d3c4b5a69788776655443322110",
        committedAt: "2026-09-18T11:22:33Z",
      },
    };
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path) ? { enabled: true, runs: [built] } : built,
      ),
    );
    const { unmount } = render(<CIPane showError={() => {}} />);
    await userEvent.click(
      await screen.findByRole("button", { name: "acme/widgets" }),
    );
    expect(
      await screen.findByText(/grain build acc97d35b0f1/),
    ).toBeInTheDocument();
    // The whole revision, and when that commit was made, on the hover:
    // it is the value somebody hands to git, and the line above is
    // already three facts long.
    expect(screen.getByText(/grain build acc97d35b0f1/)).toHaveAttribute(
      "title",
      expect.stringContaining("acc97d35b0f1e2d3c4b5a69788776655443322110"),
    );
    unmount();

    // A run recorded before grain wrote this down says nothing rather
    // than "unknown".
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path)
          ? { enabled: true, runs: [failedRun()] }
          : failedRun(),
      ),
    );
    render(<CIPane showError={() => {}} />);
    await userEvent.click(
      await screen.findByRole("button", { name: "acme/widgets" }),
    );
    expect(await screen.findByText(/asked by alice/)).toBeInTheDocument();
    expect(screen.queryByText(/grain build/)).not.toBeInTheDocument();
  });

  // A task's own runs record the task's bare id as who asked (grain's
  // orchestrator does), which the heading used to print as "asked by
  // 3214". Found by the 2026-10-12 deployment operations walkthrough.
  it("names a task that asked for a run as a task", async () => {
    expect(requesterLabel("3214")).toBe("task 3214");
    expect(requesterLabel("alice")).toBe("alice");
    expect(requesterLabel("merge-batch:acme/widgets:main")).toBe(
      "merge-batch:acme/widgets:main",
    );
    const taskRun = { ...failedRun(), requestedBy: "3214" };
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path) ? { enabled: true, runs: [taskRun] } : taskRun,
      ),
    );
    render(<CIPane showError={() => {}} />);
    await userEvent.click(
      await screen.findByRole("button", { name: "acme/widgets" }),
    );
    expect(await screen.findByText(/asked by task 3214/)).toBeInTheDocument();
  });

  // grain/task-354: the question this pane is opened on a quiet day to
  // ask -- what is CI running right now, and on which of this
  // deployment's machines.
  it("lists the sandboxes CI is holding right now, above the runs", async () => {
    const live = {
      id: "abc",
      repo: "acme/widgets",
      ref: "main",
      startedAt: STARTED,
      jobs: [
        // On a machine: this is the row.
        {
          workflow: "tests",
          workflowPath: "tests.yml",
          jobId: "test",
          name: "go test",
          sandbox: "cabc-0",
          startedAt: STARTED,
        },
        // Queueing for one of this deployment's CI slots: started
        // nothing, holding nothing.
        {
          workflow: "tests",
          workflowPath: "tests.yml",
          jobId: "lint",
          name: "lint",
        },
        // Answered out of the cache, so it never had a machine at all.
        {
          workflow: "tests",
          workflowPath: "tests.yml",
          jobId: "build",
          name: "build",
          cached: true,
          conclusion: "success",
          startedAt: STARTED,
        },
      ],
    };
    api.mockResolvedValue({ enabled: true, runs: [live, failedRun()] });
    render(<CIPane showError={() => {}} />);

    expect(await screen.findByText("Active sandboxes (1)")).toBeInTheDocument();
    // Collapsed by default (grain/task-989): the summary, not the table.
    const toggle = screen.getByRole("button", { name: /Active sandboxes/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(
      screen.getByText(/^1 CI sandbox running · longest /),
    ).toBeInTheDocument();
    expect(screen.queryByText("cabc-0")).not.toBeInTheDocument();
    await userEvent.setup().click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("cabc-0")).toBeInTheDocument();
    expect(screen.getByText("go test")).toBeInTheDocument();
    // The finished run's own job had a sandbox too, and has given it
    // back -- a name in this table means a machine that exists.
    expect(screen.queryByText("ci-9f2c1a-tests-test")).not.toBeInTheDocument();
    expect(screen.queryByText("lint")).not.toBeInTheDocument();
    expect(screen.queryByText("build")).not.toBeInTheDocument();
  });

  it("says so when no CI job is on a machine", async () => {
    api.mockResolvedValue({ enabled: true, runs: [failedRun()] });
    render(<CIPane showError={() => {}} />);

    expect(
      await screen.findByText(/No CI job is on a machine right now/),
    ).toBeInTheDocument();
    // The runs are still there: "nothing is running" is an answer, not
    // an empty pane.
    expect(screen.getByText("Runs")).toBeInTheDocument();
    expect(screen.getByText("acme/widgets")).toBeInTheDocument();
  });

  it("counts a sandbox as active on the run's word, not the job's alone", () => {
    const job = { jobId: "a", workflowPath: "w.yml", sandbox: "c1-0" };
    // A run the daemon was killed in the middle of is read back as
    // cancelled, so its jobs are not still holding machines -- whatever
    // the jobs themselves were left saying.
    expect(
      activeSandboxes([
        { id: "1", conclusion: "cancelled", interrupted: true, jobs: [job] },
      ]),
    ).toEqual([]);
    // Longest-running first: the job that has been on a machine for an
    // hour is the row worth acting on.
    const older = { ...job, jobId: "old", startedAt: "2026-09-07T11:00:00Z" };
    const newer = { ...job, jobId: "new", startedAt: "2026-09-07T12:00:00Z" };
    expect(
      activeSandboxes([{ id: "1", jobs: [newer, older] }]).map(
        (a) => a.job.jobId,
      ),
    ).toEqual(["old", "new"]);
  });

  it("summarises the active sandboxes in one line, longest first", () => {
    const now = Date.parse("2026-09-07T12:23:00Z");
    const run = { id: "1", ref: "grain/049f1e09/task-955" };
    const active = [
      {
        run,
        job: { jobId: "go", sandbox: "c1-0", startedAt: STARTED },
      },
      {
        run: { id: "2", ref: "main" },
        job: {
          jobId: "lint",
          sandbox: "c2-0",
          startedAt: "2026-09-07T12:20:00Z",
        },
      },
    ];
    expect(activeSandboxesSummary(active, now)).toBe(
      "2 CI sandboxes running · longest 23m 0s (go, task-955)",
    );
    expect(activeSandboxesSummary(active.slice(1), now)).toBe(
      "1 CI sandbox running · longest 3m 0s (lint, main)",
    );
  });

  it("remembers whether the active sandboxes were left open", async () => {
    const live = {
      id: "abc",
      repo: "acme/widgets",
      ref: "main",
      startedAt: STARTED,
      jobs: [
        {
          workflow: "tests",
          workflowPath: "tests.yml",
          jobId: "test",
          name: "go test",
          sandbox: "cabc-0",
          startedAt: STARTED,
        },
      ],
    };
    api.mockResolvedValue({ enabled: true, runs: [live] });
    const user = userEvent.setup();
    const first = render(<CIPane showError={() => {}} />);
    const toggle = await screen.findByRole("button", {
      name: /Active sandboxes \(1\)/,
    });
    expect(toggle).toHaveAttribute("aria-controls", "ci-active-sandboxes");
    await user.click(toggle);
    expect(localStorage.getItem(ACTIVE_SANDBOXES_STORAGE_KEY)).toBe("true");
    first.unmount();

    // A reload opens it the way it was left.
    render(<CIPane showError={() => {}} />);
    expect(await screen.findByText("cabc-0")).toBeInTheDocument();
    const reopened = screen.getByRole("button", { name: /Active sandboxes/ });
    expect(reopened).toHaveAttribute("aria-expanded", "true");

    // And closing it again is remembered as the default.
    await user.click(reopened);
    expect(screen.queryByText("cabc-0")).not.toBeInTheDocument();
    expect(localStorage.getItem(ACTIVE_SANDBOXES_STORAGE_KEY)).toBeNull();
  });

  // The pane asks the daemon for a page of what it remembers and narrows
  // in the browser, so that a filter answers as it is typed and a menu
  // of repositories still offers the repositories the filter is hiding
  // (ciFilters.js's repoNarrowedTo). How far back that page reaches is
  // the one thing it does ask the daemon -- the test below this one.
  it("narrows the runs it has rather than asking the daemon for a subset", async () => {
    api.mockImplementation((path) =>
      isRunListing(path)
        ? Promise.resolve({
            enabled: true,
            runs: [
              failedRun(),
              { ...failedRun(), id: "other", repo: "acme/gadgets" },
            ],
          })
        : Promise.resolve({ enabled: true }),
    );
    render(<CIPane config={{}} showError={() => {}} />);
    await screen.findByRole("table");

    await userEvent.click(screen.getByTitle(CI_FILTER_BUTTON));
    await userEvent.click(screen.getByRole("button", { name: "Filter by…" }));
    await userEvent.click(
      await screen.findByRole("menuitem", { name: "Repo" }),
    );
    await userEvent.click(screen.getByLabelText("Value for filter 1"));
    // Both repositories are on offer, including the one about to be
    // filtered out -- the menu is built from everything fetched.
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "acme/gadgets",
      "acme/widgets",
    ]);
    await userEvent.click(screen.getByRole("option", { name: "acme/gadgets" }));
    await userEvent.keyboard("{Escape}");
    await userEvent.click(screen.getByTitle(CI_FILTER_BUTTON));

    // The heading row and the one run that answers, with the chip under
    // the toolbar saying what the table is narrowed by.
    await waitFor(() => expect(screen.getAllByRole("row").length).toBe(2));
    expect(screen.getByText("Repo is any of acme/gadgets")).toBeInTheDocument();
    expect(screen.getAllByRole("row")[1].textContent).toContain("acme/gadgets");
    expect(screen.queryByText("acme/widgets")).toBeNull();
    // And the daemon was never asked a narrower question.
    for (const [path] of api.mock.calls) expect(path).not.toContain("repo=");
  });

  // The poll is a page, and the window somebody asks to look back over
  // is what widens it: the ordinary table costs the newest few hundred
  // runs every five seconds, and "started within the last 14 days" is
  // answered over the whole fortnight the deployment keeps rather than
  // over that page (grain/task-289).
  it("asks the daemon to reach back when the reader names a window", async () => {
    api.mockImplementation((path) =>
      isRunListing(path)
        ? Promise.resolve({ enabled: true, runs: [failedRun()] })
        : Promise.resolve({ enabled: true }),
    );
    render(<CIPane config={{}} showError={() => {}} />);
    await screen.findByRole("table");

    const polls = () =>
      api.mock.calls.map(([path]) => path).filter(isRunListing);
    expect(polls().at(-1)).toBe(`/api/ci/runs?limit=${RUN_POLL_LIMIT}`);

    await userEvent.click(screen.getByTitle(CI_FILTER_BUTTON));
    await userEvent.click(screen.getByRole("button", { name: "Filter by…" }));
    await userEvent.click(
      await screen.findByRole("menuitem", { name: "Started" }),
    );
    await userEvent.type(screen.getByLabelText("Value for filter 1"), "14");

    await waitFor(() => expect(polls().at(-1)).toContain("since="));
    expect(polls().at(-1)).toContain(`limit=${WINDOWED_RUN_POLL_LIMIT}`);
  });

  // An empty table under a toolbar reads as a pane that has broken. It
  // says which emptiness it is instead: nothing has run, or nothing
  // answers this question.
  it("says an empty answer is empty rather than looking broken", async () => {
    api.mockImplementation((path) =>
      isRunListing(path)
        ? Promise.resolve({ enabled: true, runs: [failedRun()] })
        : Promise.resolve({ enabled: true }),
    );
    render(<CIPane config={{}} showError={() => {}} />);
    await screen.findByRole("table");

    await userEvent.type(
      screen.getByPlaceholderText("Search runs…"),
      "nothing",
    );

    expect(await screen.findByText(/No CI run matches/)).toBeInTheDocument();
    expect(screen.getByText(/1 run is here/)).toBeInTheDocument();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("opens narrowed to the repo it was sent about, and lets it be unpicked", async () => {
    api.mockImplementation((path) =>
      isRunListing(path)
        ? Promise.resolve({ enabled: true, runs: [failedRun()] })
        : Promise.resolve({ enabled: true }),
    );
    // A repo page can send this pane somewhere neither the allowlist nor
    // the runs in view names, and a filter set to an option nobody
    // offers is one nobody can unpick.
    render(
      <CIPage initialRepo="acme/gadgets" config={{}} showError={() => {}} />,
    );

    expect(
      await screen.findByText("Repo is any of acme/gadgets"),
    ).toBeInTheDocument();
    expect(await screen.findByText(/No CI run matches/)).toBeInTheDocument();

    // It is where the question starts, not one the reader is stuck with.
    await userEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(await screen.findByRole("table")).toBeInTheDocument();
  });

  // The order is the other half of the question, and duration is the one
  // that tells a forty-minute timeout from a two-minute assertion.
  it("orders the runs by how long they took", async () => {
    const quick = {
      ...failedRun(),
      id: "quick",
      ref: "quick-branch",
      startedAt: STARTED,
      finishedAt: "2026-09-07T12:00:30Z",
    };
    const slow = {
      ...failedRun(),
      id: "slow",
      ref: "slow-branch",
      startedAt: STARTED,
      finishedAt: "2026-09-07T12:40:00Z",
    };
    api.mockImplementation((path) =>
      isRunListing(path)
        ? Promise.resolve({ enabled: true, runs: [quick, slow] })
        : Promise.resolve({ enabled: true }),
    );
    render(<CIPane config={{}} showError={() => {}} />);
    await screen.findByRole("table");

    // The pane opens sorted by Started, newest first, so this is the one
    // sort key changed rather than a second one added.
    await userEvent.click(screen.getByTitle(CI_SORT_BUTTON));
    await userEvent.click(screen.getByLabelText("Attribute for sort 1"));
    await userEvent.click(
      await screen.findByRole("option", { name: "Duration (minutes)" }),
    );

    await waitFor(() => {
      const refs = screen
        .getAllByRole("row")
        .slice(1)
        .map((row) => row.textContent);
      expect(refs[0]).toContain("slow-branch");
      expect(refs[1]).toContain("quick-branch");
    });
  });

  // Reading one job down a column of many commits is what separates a
  // flaky job from a broken change, and it needs that job's own result
  // and its own duration -- not the run's tally of a dozen others.
  it("reports the named job's own result and duration when narrowed to one", async () => {
    const run = {
      ...failedRun(),
      jobs: [
        {
          jobId: "go",
          name: "go tests",
          conclusion: "success",
          startedAt: STARTED,
          finishedAt: "2026-09-07T12:01:00Z",
          steps: [],
        },
        {
          jobId: "e2e",
          name: "ui e2e",
          conclusion: "failure",
          startedAt: STARTED,
          finishedAt: "2026-09-07T12:22:00Z",
          steps: [],
        },
      ],
    };
    api.mockImplementation((path) =>
      isRunListing(path)
        ? Promise.resolve({ enabled: true, runs: [run] })
        : Promise.resolve({ enabled: true }),
    );
    render(<CIPane config={{}} showError={() => {}} />);
    await screen.findByRole("table");

    await userEvent.click(screen.getByTitle(CI_FILTER_BUTTON));
    await userEvent.click(screen.getByRole("button", { name: "Filter by…" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Job" }));
    await userEvent.click(screen.getByLabelText("Value for filter 1"));
    await userEvent.click(screen.getByRole("option", { name: "ui e2e" }));
    await userEvent.keyboard("{Escape}");

    expect(
      await screen.findByText("ui e2e: failure in 22m 0s"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/2 jobs/)).toBeNull();
  });

  it("starts a run against a repo and a branch, and opens it", async () => {
    const started = {
      id: "new1",
      repo: "acme/widgets",
      ref: "grain/task-9",
      startedAt: STARTED,
      jobs: [],
    };
    api.mockImplementation((path, opts) => {
      if (path === "/api/ci/runs" && opts?.method === "POST")
        return Promise.resolve(started);
      if (path.startsWith("/api/ci/workflows?"))
        return Promise.resolve({
          workflows: [
            { path: ".github/workflows/ci.yml", name: "CI" },
            { path: ".github/workflows/images.yml", name: "Runner images" },
          ],
        });
      if (isRunListing(path))
        return Promise.resolve({ enabled: true, runs: [] });
      return Promise.resolve(started);
    });
    render(
      <CIPane
        config={{ targetRepos: ["acme/widgets"] }}
        showError={() => {}}
      />,
    );

    await userEvent.click(
      await screen.findByRole("button", { name: "Start a run" }),
    );
    await userEvent.selectOptions(
      screen.getByLabelText(/^Repo$/),
      "acme/widgets",
    );
    await userEvent.type(screen.getByLabelText("Branch"), "grain/task-9");
    await userEvent.click(
      screen.getByRole("button", { name: "Load workflows" }),
    );
    await userEvent.click(await screen.findByLabelText("Workflow"));
    await userEvent.click(screen.getByRole("option", { name: /CI —/ }));
    await userEvent.click(screen.getByRole("button", { name: "Start run" }));

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/ci/runs", {
        method: "POST",
        body: JSON.stringify({
          repo: "acme/widgets",
          branch: "grain/task-9",
          workflow: ".github/workflows/ci.yml",
        }),
      }),
    );
    // Straight onto the run that was just asked for, rather than back to
    // a table to go and find it in.
    expect(
      await screen.findByRole("button", { name: /Back/ }),
    ).toBeInTheDocument();
  });

  it("allows bypassing cache when starting a run", async () => {
    const started = {
      id: "new1",
      repo: "acme/widgets",
      ref: "grain/task-9",
      startedAt: STARTED,
      jobs: [],
    };
    api.mockImplementation((path, opts) => {
      if (path === "/api/ci/runs" && opts?.method === "POST")
        return Promise.resolve(started);
      if (path.startsWith("/api/ci/workflows?"))
        return Promise.resolve({
          workflows: [{ path: ".github/workflows/ci.yml", name: "CI" }],
        });
      if (isRunListing(path))
        return Promise.resolve({ enabled: true, runs: [] });
      return Promise.resolve(started);
    });
    render(
      <CIPane
        config={{ targetRepos: ["acme/widgets"] }}
        showError={() => {}}
      />,
    );

    await userEvent.click(
      await screen.findByRole("button", { name: "Start a run" }),
    );
    await userEvent.selectOptions(
      screen.getByLabelText(/^Repo$/),
      "acme/widgets",
    );
    await userEvent.type(screen.getByLabelText("Branch"), "grain/task-9");
    await userEvent.click(
      screen.getByRole("button", { name: "Load workflows" }),
    );
    await userEvent.click(await screen.findByLabelText("Workflow"));
    await userEvent.click(screen.getByRole("option", { name: /CI —/ }));
    await userEvent.click(screen.getByLabelText("Bypass cache"));
    await userEvent.click(screen.getByRole("button", { name: "Start run" }));

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/ci/runs", {
        method: "POST",
        body: JSON.stringify({
          repo: "acme/widgets",
          branch: "grain/task-9",
          workflow: ".github/workflows/ci.yml",
          recheck: true,
        }),
      }),
    );
  });

  it("keeps a refused start beside the form that provoked it", async () => {
    const refusal =
      'grain CI is not enabled for acme/widgets: set its CI Mode to "Use Grain CI" on the repo\'s page (Settings, Repos). Attaching the "grain-ci" capability does not turn it on by itself: the CI mode decides that, and grants "grain-ci" to the repo\'s tasks';
    const showError = vi.fn();
    api.mockImplementation((path, opts) =>
      path === "/api/ci/runs" && opts?.method === "POST"
        ? Promise.reject(new Error(refusal))
        : path.startsWith("/api/ci/workflows?")
          ? Promise.resolve({
              workflows: [{ path: ".github/workflows/ci.yml", name: "CI" }],
            })
          : Promise.resolve({ enabled: true, runs: [] }),
    );
    render(
      <CIPane
        config={{ targetRepos: ["acme/widgets"] }}
        showError={showError}
      />,
    );

    await userEvent.click(
      await screen.findByRole("button", { name: "Start a run" }),
    );
    await userEvent.selectOptions(
      screen.getByLabelText(/^Repo$/),
      "acme/widgets",
    );
    await userEvent.type(screen.getByLabelText("Branch"), "main");
    await userEvent.click(
      screen.getByRole("button", { name: "Load workflows" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Start run" }));

    // The refusal names the two clicks that turn grain CI on, and it is
    // only worth reading beside the repo it is about.
    expect(
      await screen.findByText(/is not enabled for acme\/widgets/),
    ).toBeInTheDocument();
    expect(showError).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Branch")).toHaveValue("main");
  });

  it("opens on the run it was sent to, and says so when that run has been forgotten", async () => {
    api.mockImplementation((path) =>
      isRunListing(path)
        ? Promise.resolve({ enabled: true, runs: [failedRun()] })
        : Promise.resolve(failedRun()),
    );
    const { unmount } = render(<CIPane runId="9f2c1a" showError={() => {}} />);

    // Straight into the run, not into the table it is one row of.
    expect(await screen.findByText("go test")).toBeInTheDocument();
    expect(screen.getByText("--- FAIL: TestThing")).toBeInTheDocument();
    unmount();

    // A run is remembered for a while and then forgotten, so a link to
    // one is a link that goes stale. The daemon's own account of that is
    // shown over the listing rather than left as a spinner that never
    // resolves -- and not as an app-wide error, since nothing is wrong.
    const showError = vi.fn();
    const forgotten = new Error('no CI run "9f2c1a" -- a run is remembered');
    api.mockImplementation((path) => {
      if (isRunListing(path))
        return Promise.resolve({ enabled: true, runs: [] });
      if (path === "/api/ci/cache")
        return Promise.resolve({ enabled: true, available: false });
      return Promise.reject(forgotten);
    });
    render(<CIPane runId="9f2c1a" showError={showError} />);

    expect(await screen.findByText(/is remembered/)).toBeInTheDocument();
    expect(showError).not.toHaveBeenCalled();
  });

  it("opens and follows the output of the step that is running", async () => {
    const live = {
      id: "abc",
      repo: "acme/widgets",
      ref: "main",
      startedAt: STARTED,
      jobs: [
        {
          workflow: "tests",
          jobId: "test",
          name: "go test",
          startedAt: STARTED,
          steps: [
            {
              name: "go test ./...",
              startedAt: STARTED,
              output: "first package",
            },
          ],
        },
      ],
    };
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path) ? { enabled: true, runs: [live] } : live,
      ),
    );
    render(<CIPane runId="abc" showError={() => {}} />);

    expect(await screen.findByText("first package")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Hide output" }),
    ).toBeInTheDocument();
  });

  it("polls while a run is going and stops once it has finished", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const live = {
      id: "abc",
      repo: "acme/widgets",
      ref: "main",
      startedAt: STARTED,
      jobs: [],
    };
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path) ? { enabled: true, runs: [live] } : live,
      ),
    );
    render(<CIPane showError={() => {}} />);
    await screen.findByRole("table");

    // The listing polls while it is what is on screen.
    const listCalls = () =>
      api.mock.calls.filter(([path]) => isRunListing(path)).length;
    const before = listCalls();
    await vi.advanceTimersByTimeAsync(11000);
    expect(listCalls()).toBeGreaterThan(before);

    // An open, unfinished run polls while the listing stays fresh below it.
    await userEvent.click(screen.getByRole("button", { name: "acme/widgets" }));
    const listAtOpen = listCalls();
    const runCalls = () =>
      api.mock.calls.filter(([path]) => path === "/api/ci/runs/abc").length;
    const runsAtOpen = runCalls();
    await vi.advanceTimersByTimeAsync(11000);
    expect(runCalls()).toBeGreaterThan(runsAtOpen);
    expect(listCalls()).toBeGreaterThan(listAtOpen);

    // A finished run is not polled again: nothing more will happen to
    // it, and each poll would re-fetch every step's output with it.
    api.mockImplementation((path) =>
      Promise.resolve(
        isRunListing(path)
          ? { enabled: true, runs: [live] }
          : { ...live, conclusion: "success", finishedAt: STARTED },
      ),
    );
    await vi.advanceTimersByTimeAsync(6000);
    const settled = runCalls();
    await vi.advanceTimersByTimeAsync(20000);
    expect(runCalls()).toBe(settled);
  });

  // A CI run belongs to no team (single-tenant reset, stage 4c), so the
  // selected team narrows nothing.
  it("lists every run whatever team is selected", async () => {
    localStorage.setItem("grain.selectedTeam", "infra");
    const infraRun = {
      ...failedRun(),
      id: "run-infra",
      ref: "infra-branch",
    };
    const defaultRun = {
      ...failedRun(),
      id: "run-default",
      ref: "default-branch",
    };
    api.mockImplementation((path) =>
      isRunListing(path)
        ? Promise.resolve({ enabled: true, runs: [infraRun, defaultRun] })
        : Promise.resolve({ enabled: true }),
    );
    render(<CIPane config={{}} showError={() => {}} />);
    await screen.findByRole("table");

    expect(screen.getByText("infra-branch")).toBeInTheDocument();
    expect(screen.getByText("default-branch")).toBeInTheDocument();
  });

  it("starts a run with no team, whatever team is selected", async () => {
    localStorage.setItem("grain.selectedTeam", "infra");
    const started = {
      id: "new-team-run",
      repo: "acme/widgets",
      ref: "infra/task-1",
      startedAt: STARTED,
      jobs: [],
    };
    api.mockImplementation((path, opts) => {
      if (path === "/api/ci/runs" && opts?.method === "POST")
        return Promise.resolve(started);
      if (path.startsWith("/api/ci/workflows?"))
        return Promise.resolve({
          workflows: [{ path: ".github/workflows/ci.yml", name: "CI" }],
        });
      if (isRunListing(path))
        return Promise.resolve({ enabled: true, runs: [] });
      return Promise.resolve(started);
    });
    render(
      <CIPane
        config={{
          targetRepos: ["acme/widgets"],
          deployment: { id: "default" },
        }}
        showError={() => {}}
      />,
    );

    await userEvent.click(
      await screen.findByRole("button", { name: "Start a run" }),
    );
    await userEvent.selectOptions(
      screen.getByLabelText(/^Repo$/),
      "acme/widgets",
    );
    await userEvent.type(screen.getByLabelText("Branch"), "infra/task-1");
    expect(screen.queryByLabelText("Team")).not.toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: "Load workflows" }),
    );
    await userEvent.click(await screen.findByLabelText("Workflow"));
    await userEvent.click(screen.getByRole("option", { name: /CI —/ }));
    await userEvent.click(screen.getByRole("button", { name: "Start run" }));

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/ci/runs", {
        method: "POST",
        body: JSON.stringify({
          repo: "acme/widgets",
          branch: "infra/task-1",
          workflow: ".github/workflows/ci.yml",
        }),
      }),
    );
  });
});

describe("CI helpers", () => {
  it("puts failures first and leaves the rest in plan order", () => {
    const jobs = [
      { jobId: "a", conclusion: "success" },
      { jobId: "b", conclusion: "" },
      { jobId: "c", conclusion: "failure" },
      { jobId: "d", conclusion: "cancelled" },
      { jobId: "e", conclusion: "success" },
    ];
    expect(sortJobs(jobs).map((j) => j.jobId)).toEqual([
      "c",
      "d",
      "a",
      "e",
      "b",
    ]);
    // The argument is not reordered under the caller: the API serves
    // Run.Jobs in plan order on purpose.
    expect(jobs[0].jobId).toBe("a");
  });

  // A job grain could not run at all (ci.ConclusionErrored) is not a
  // failure and must not read as one: it sits below the failures, and it
  // gets a word and a colour of its own rather than falling through to
  // the unknown-conclusion default.
  it("keeps a job grain could not run apart from one that failed", () => {
    const jobs = [
      { jobId: "a", conclusion: "success" },
      { jobId: "b", conclusion: "errored" },
      { jobId: "c", conclusion: "failure" },
    ];
    expect(sortJobs(jobs).map((j) => j.jobId)).toEqual(["c", "b", "a"]);
    expect(conclusionChip("errored")).toEqual({
      label: "Errored",
      color: "warning",
    });
    expect(
      jobSummary({
        jobs: [{ conclusion: "errored" }, { conclusion: "success" }],
      }),
    ).toBe("2 jobs: 1 errored, 1 success");
  });

  it("renders a span the way somebody reading a run says one", () => {
    expect(formatDuration(4000)).toBe("4s");
    expect(formatDuration(83000)).toBe("1m 23s");
    expect(formatDuration(7500000)).toBe("2h 5m");
    expect(formatDuration(null)).toBe("—");
  });

  it("measures a run that has not finished against now", () => {
    const now = Date.parse("2026-09-07T12:00:30Z");
    expect(spanOf({ startedAt: STARTED }, now)).toBe(30000);
    expect(
      spanOf({ startedAt: STARTED, finishedAt: "2026-09-07T12:00:10Z" }, now),
    ).toBe(10000);
    expect(spanOf({}, now)).toBe(null);
  });

  // A timestamp nobody has arrives as no field at all, which is the
  // first thing instantOf answers for; the year-1 instant grain used to
  // send instead -- `omitempty` does not omit a struct -- is refused
  // too, because taken for a real one it drew a job that errored
  // waiting for a gcp-key grant as 17756979h 39m.
  it("treats an absent timestamp, and Go's zero time, as no time at all", () => {
    const now = Date.parse("2026-09-07T12:00:30Z");
    expect(spanOf({ finishedAt: STARTED }, now)).toBe(null);
    expect(progressChip({}).label).toBe("Pending");
    expect(jobSummary({ jobs: [{}] })).toBe("1 job: 1 pending");

    expect(spanOf({ startedAt: ZERO_TIME, finishedAt: STARTED }, now)).toBe(
      null,
    );
    expect(spanOf({ startedAt: STARTED, finishedAt: ZERO_TIME }, now)).toBe(
      30000,
    );
    expect(progressChip({ startedAt: ZERO_TIME }).label).toBe("Pending");
    expect(progressChip({ jobs: [{ startedAt: ZERO_TIME }] }, true).label).toBe(
      "Pending",
    );
    expect(jobSummary({ jobs: [{ startedAt: ZERO_TIME }] })).toBe(
      "1 job: 1 pending",
    );
  });

  it("draws a job that errored before it ran with no duration on it", () => {
    render(
      <RunDetail
        run={{
          id: "r1",
          repo: "o/r",
          ref: "main",
          conclusion: "failure",
          startedAt: STARTED,
          finishedAt: "2026-09-07T12:04:00Z",
          jobs: [
            {
              jobId: "held",
              name: "Probe Baseline",
              workflow: "Continuous integration",
              conclusion: "errored",
              // No startedAt at all: this job never got a machine, and
              // that is what grain now says about one of those.
              finishedAt: "2026-09-07T12:04:00Z",
              error: "waiting for this job's capability grants: no keys",
            },
          ],
        }}
        zone="UTC"
        onBack={() => {}}
        onCancel={() => {}}
      />,
    );

    expect(screen.getByText("Probe Baseline")).toBeInTheDocument();
    // The job's caption is the workflow name and nothing else: a span
    // would be appended to it as " · 17756979h 39m".
    expect(screen.getByText("Continuous integration")).toBeInTheDocument();
    expect(screen.queryByText(/\d{4,}h/)).not.toBeInTheDocument();
  });

  // grain/task-326: a job's duration starts when it gets a machine, so
  // "22m" on a row is silent about whether the job was starved or slow.
  // The wait is drawn beside the work, and a job that only ever queued
  // -- one cancelled before it got a machine -- has the wait and nothing
  // else, where before it had nothing at all.
  it("draws what a job spent queueing beside what it spent running", () => {
    render(
      <RunDetail
        run={{
          id: "r1",
          repo: "o/r",
          ref: "main",
          conclusion: "cancelled",
          startedAt: STARTED,
          finishedAt: "2026-09-07T12:40:00Z",
          jobs: [
            {
              jobId: "image",
              name: "image",
              workflow: "Continuous integration",
              conclusion: "cancelled",
              // Never started: 36 minutes of queueing and then a
              // timeout, which is the run nobody could explain.
              finishedAt: "2026-09-07T12:36:00Z",
              queued: 36 * 60 * 1e9,
              queuedReason:
                "for room in this deployment's sandbox budget -- memory",
            },
            {
              jobId: "go",
              name: "go",
              workflow: "Continuous integration",
              conclusion: "success",
              startedAt: "2026-09-07T12:18:00Z",
              finishedAt: "2026-09-07T12:22:00Z",
              queued: 18 * 60 * 1e9,
              queuedReason: "behind higher-priority work",
            },
          ],
        }}
        zone="UTC"
        onBack={() => {}}
        onCancel={() => {}}
      />,
    );

    // The job that only queued says so, rather than showing nothing.
    expect(screen.getByText(/36m 0s queued/)).toBeInTheDocument();
    // The one that did both says both, in that order.
    expect(screen.getByText(/18m 0s queued · 4m 0s/)).toBeInTheDocument();
    // And which scarce thing held each of them, which is the difference
    // between "this deployment is full" and "this job is slow".
    expect(screen.getByText(/sandbox budget/)).toBeInTheDocument();
    expect(screen.getByText(/higher-priority work/)).toBeInTheDocument();
    // The suite's total, which is job-time and so longer than the run.
    expect(screen.getByText("54m 0s of job-time queued")).toBeInTheDocument();
  });

  it("summarizes a run's jobs worst-first, and says so when they are all green", () => {
    expect(jobSummary({ jobs: [] })).toBe("no jobs");
    expect(
      jobSummary({
        jobs: [{ conclusion: "success" }, { conclusion: "success" }],
      }),
    ).toBe("2 jobs, all green");
    expect(jobSummary({ jobs: [{ conclusion: "success" }] })).toBe(
      "1 job, all green",
    );
    expect(jobSummary({ jobs: [{ conclusion: "failure" }] })).toBe(
      "1 job: 1 failure",
    );
    expect(
      jobSummary({
        jobs: [
          { conclusion: "success", cached: true },
          { conclusion: "success", cached: true },
        ],
      }),
    ).toBe("2 jobs, all green · 2 cache hits");
    expect(
      jobSummary({
        jobs: [
          { conclusion: "success", cached: true },
          { conclusion: "failure" },
          { conclusion: "", startedAt: STARTED },
          { conclusion: "" },
        ],
      }),
    ).toBe("4 jobs: 1 failure, 1 success, 1 running, 1 pending · 1 cache hit");
  });

  // grain/task-304: the job cache, which until now was a directory on
  // the host and some lines in the daemon's journal.

  it("says what the job cache is holding, and empties it when asked", async () => {
    const held = {
      enabled: true,
      available: true,
      cache: {
        dir: "/var/lib/grain/data/state/ci-cache",
        entries: 12,
        bytes: 3 << 20,
        hits: 7,
        misses: 3,
        maxBytes: 256 << 20,
        maxEntries: 4096,
        maxAgeSeconds: 30 * 24 * 60 * 60,
      },
    };
    api.mockImplementation((path, opts) => {
      if (path === "/api/ci/cache" && opts?.method === "DELETE")
        return Promise.resolve({
          enabled: true,
          available: true,
          removed: 12,
          cache: { ...held.cache, entries: 0, bytes: 0 },
        });
      if (path === "/api/ci/cache") return Promise.resolve(held);
      return Promise.resolve({ enabled: true, runs: [failedRun()] });
    });
    render(<CIPane showError={() => {}} />);

    expect(
      await screen.findByText(/12 entries · 3.0 MiB of 256.0 MiB/),
    ).toBeInTheDocument();
    // The hit rate is dated to the daemon, because that is as far back
    // as the counters go.
    expect(
      screen.getByText(/70% of 10 lookups hit since this daemon started/),
    ).toBeInTheDocument();

    vi.spyOn(window, "confirm").mockReturnValue(true);
    await userEvent.click(screen.getByRole("button", { name: "Clear cache" }));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/ci/cache", { method: "DELETE" }),
    );
    // What came back from the DELETE is what is drawn: no second fetch
    // to find out what an emptied cache holds.
    expect(await screen.findByText(/0 entries · 0 B of/)).toBeInTheDocument();
  });

  it("does not clear the cache when the question is declined", async () => {
    // Counted here rather than read off the mock's history: the history
    // outlives the test that made it, and what this is about is whether
    // *this* click sent anything.
    let deletes = 0;
    api.mockImplementation((path, opts) => {
      if (opts?.method === "DELETE") deletes++;
      return Promise.resolve(
        path === "/api/ci/cache"
          ? { enabled: true, available: true, cache: { entries: 1, bytes: 10 } }
          : { enabled: true, runs: [] },
      );
    });
    render(<CIPane showError={() => {}} />);
    vi.spyOn(window, "confirm").mockReturnValue(false);
    await userEvent.click(
      await screen.findByRole("button", { name: "Clear cache" }),
    );
    expect(deletes).toBe(0);
    expect(await screen.findByText(/1 entry/)).toBeInTheDocument();
  });

  it("tells a deployment with no cache from a cache with nothing in it", async () => {
    api.mockImplementation((path) =>
      Promise.resolve(
        path === "/api/ci/cache"
          ? { enabled: true, available: false }
          : { enabled: true, runs: [failedRun()] },
      ),
    );
    render(<CIPane showError={() => {}} />);

    expect(
      await screen.findByText(/No job cache: every job runs, every time/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Clear cache" }),
    ).not.toBeInTheDocument();
  });

  it("says so when the cache cannot be read at all, without losing the runs", async () => {
    api.mockImplementation((path) =>
      path === "/api/ci/cache"
        ? Promise.reject(
            new Error("reading the cache directory: permission denied"),
          )
        : Promise.resolve({ enabled: true, runs: [failedRun()] }),
    );
    render(<CIPane showError={() => {}} />);

    expect(await screen.findByText(/permission denied/)).toBeInTheDocument();
    // A footnote that failed does not take the table with it.
    expect(screen.getByText("acme/widgets")).toBeInTheDocument();
  });

  it("renders a cache size the way somebody sizing a disk says one", () => {
    expect(formatBytes(3 << 20)).toBe("3.0 MiB");
    expect(formatBytes(2048)).toBe("2 KiB");
    // A ceiling in gigabytes reads as one, as System → Sandboxes reads
    // the same figure, rather than as "20480.0 MB".
    expect(formatBytes(20 * 2 ** 30)).toBe("20.0 GiB");
    expect(formatBytes(12)).toBe("12 B");
    expect(formatBytes(null)).toBe("—");
    expect(cacheSummary({ entries: 1, bytes: 0, maxBytes: 1 << 20 })).toBe(
      "1 entry · 0 B of 1.0 MiB · no lookups since this daemon started",
    );
  });

  it("draws a job being retried as running, with the first attempt's failure", () => {
    render(
      <RunDetail
        run={{
          id: "r1",
          repo: "o/r",
          ref: "main",
          conclusion: "",
          startedAt: STARTED,
          jobs: [
            {
              jobId: "go",
              name: "go",
              workflow: "Continuous integration",
              conclusion: "",
              startedAt: STARTED,
              sandbox: "sandbox-2",
              retried: true,
              retryCount: 1,
              retryReason: 'step "test": FAIL TestFlaky',
              steps: [{ name: "test", conclusion: "", startedAt: STARTED }],
            },
          ],
        }}
        zone="UTC"
        onBack={() => {}}
        onCancel={() => {}}
      />,
    );

    expect(screen.getAllByText("Running")).not.toHaveLength(0);
    expect(screen.queryByText("Failure")).not.toBeInTheDocument();
    expect(screen.getByText("retry 1/1")).toBeInTheDocument();
    expect(
      screen.getByText('first attempt failed: step "test": FAIL TestFlaky'),
    ).toBeInTheDocument();
    expect(
      activeSandboxes([
        {
          conclusion: "",
          jobs: [{ conclusion: "", sandbox: "sandbox-2", retried: true }],
        },
      ]),
    ).toHaveLength(1);
  });

  it("renders live output frame immediately for in-progress step", () => {
    render(
      <RunDetail
        run={{
          id: "r1",
          repo: "o/r",
          ref: "main",
          conclusion: "",
          startedAt: STARTED,
          jobs: [
            {
              jobId: "j1",
              name: "test",
              workflow: "Continuous integration",
              conclusion: "",
              startedAt: STARTED,
              steps: [
                {
                  name: "checkout",
                  conclusion: "success",
                  output: "checked out",
                },
                {
                  name: "build",
                  conclusion: "",
                  startedAt: STARTED,
                  output: "",
                },
              ],
            },
          ],
        }}
        zone="UTC"
        onBack={() => {}}
        onCancel={() => {}}
      />,
    );

    expect(screen.getByText("build")).toBeInTheDocument();
    expect(screen.getByText("(waiting for output...)")).toBeInTheDocument();
    expect(screen.getByText("Hide output")).toBeInTheDocument();
  });

  it("renders live output frame with text for in-progress step with output", () => {
    render(
      <RunDetail
        run={{
          id: "r1",
          repo: "o/r",
          ref: "main",
          conclusion: "",
          startedAt: STARTED,
          jobs: [
            {
              jobId: "j1",
              name: "test",
              workflow: "Continuous integration",
              conclusion: "",
              startedAt: STARTED,
              steps: [
                {
                  name: "build",
                  conclusion: "",
                  startedAt: STARTED,
                  output: "compiling pkg/...",
                },
              ],
            },
          ],
        }}
        zone="UTC"
        onBack={() => {}}
        onCancel={() => {}}
      />,
    );

    expect(screen.getByText("compiling pkg/...")).toBeInTheDocument();
  });

  it("shows active running indicator for a running job on a machine with no steps yet", () => {
    render(
      <RunDetail
        run={{
          id: "r1",
          repo: "o/r",
          ref: "main",
          conclusion: "",
          startedAt: STARTED,
          jobs: [
            {
              jobId: "j1",
              name: "test",
              workflow: "Continuous integration",
              conclusion: "",
              startedAt: STARTED,
              sandbox: "ci-sandbox-123",
              steps: [],
            },
          ],
        }}
        zone="UTC"
        onBack={() => {}}
        onCancel={() => {}}
      />,
    );

    expect(screen.getByText("Running...")).toBeInTheDocument();
    expect(screen.queryByText("No steps ran.")).not.toBeInTheDocument();
  });
});

// grain/task-480: a concluded run had Cancel's opposite nowhere, so a run
// that errored on a capability the repository had since been granted
// could not be asked for again from here.
describe("Retry on a concluded run", () => {
  afterEach(() => vi.resetAllMocks());

  it("retries a task's run as that task's, and a by-hand run as it was", () => {
    const task = { ...failedRun(), requestedBy: "480" };
    expect(retryRequest(task)).toEqual({ taskId: "480" });
    expect(retryRequest(task, true)).toEqual({
      taskId: "480",
      recheck: true,
      recheckFailed: true,
    });
    const byHand = failedRun();
    expect(retryRequest(byHand)).toEqual({
      repo: "acme/widgets",
      ref: "grain/task-1",
      commit: "0123456789abcdef0123",
      event: "push",
    });
    expect(retryRequest(byHand, true)).toEqual({
      repo: "acme/widgets",
      ref: "grain/task-1",
      commit: "0123456789abcdef0123",
      event: "push",
      jobs: ["test"],
      recheck: true,
    });
    expect(retryRequest({ ...byHand, jobs: [] }, true)).toBeNull();
  });

  it("shows Retry on errored, failed and cancelled runs and not on others", () => {
    for (const conclusion of ["errored", "failure", "cancelled"]) {
      const { unmount } = render(
        <RunDetail
          run={{ ...failedRun(), conclusion }}
          zone="UTC"
          onCancel={() => {}}
          onRetry={() => {}}
        />,
      );
      expect(screen.getByRole("button", { name: "Retry" })).toBeVisible();
      expect(
        screen.queryByRole("button", { name: "Retry failed jobs" }) !== null,
      ).toBe(conclusion !== "cancelled");
      unmount();
    }
    for (const conclusion of ["success", ""]) {
      const { unmount } = render(
        <RunDetail
          run={{ ...failedRun(), conclusion }}
          zone="UTC"
          onCancel={() => {}}
          onRetry={() => {}}
        />,
      );
      expect(
        screen.queryByRole("button", { name: "Retry" }),
      ).not.toBeInTheDocument();
      unmount();
    }
  });

  it("posts the task's id and opens the run it started", async () => {
    const user = userEvent.setup();
    const errored = {
      ...failedRun(),
      requestedBy: "480",
      conclusion: "errored",
    };
    const replacement = {
      ...errored,
      id: "a1b2c3",
      conclusion: "",
      finishedAt: undefined,
      jobs: [],
    };
    api.mockImplementation((path, opts) => {
      if (path === "/api/ci/runs" && opts?.method === "POST")
        return Promise.resolve(replacement);
      if (path === `/api/ci/runs/${replacement.id}`)
        return Promise.resolve(replacement);
      return Promise.resolve(errored);
    });
    function Harness() {
      const [open, setOpen] = useState(errored.id);
      return (
        <CIRunOverlay
          runId={open}
          onClose={() => {}}
          onOpenRun={setOpen}
          showError={() => {}}
        />
      );
    }
    render(<Harness />);
    await user.click(await screen.findByRole("button", { name: "Retry" }));
    expect(api).toHaveBeenCalledWith("/api/ci/runs", {
      method: "POST",
      body: JSON.stringify({ taskId: "480" }),
    });
    // The pane follows to the new run, which is still going.
    expect(
      await screen.findByRole("button", { name: "Cancel run" }),
    ).toBeVisible();
  });
});
