import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import api from "../api.js";
import RepoReleaseWorkflowSettings from "./RepoReleaseWorkflowSettings.jsx";
import { leavingLosesEdits } from "../unsavedEdits.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const settings = {
  repo: "acme/widgets",
  enabled: true,
  available: true,
  secretsDir: "/home/debian/.grain-release-secrets",
  secrets: [{ name: "NPM_TOKEN", set: true }],
};

function renderSettings(over = {}) {
  const props = {
    repo: "acme/widgets",
    onOpenRun: vi.fn(),
    showError: vi.fn(),
    ...over,
  };
  render(<RepoReleaseWorkflowSettings {...props} />);
  return props;
}

describe("RepoReleaseWorkflowSettings", () => {
  afterEach(() => api.mockReset());

  it("turns automatic post-merge releases off", async () => {
    api
      .mockResolvedValueOnce(settings)
      .mockResolvedValueOnce({ ...settings, enabled: false });
    renderSettings();

    await userEvent.click(
      await screen.findByRole("button", {
        name: "Turn off automatic releases",
      }),
    );

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/release", {
        method: "PUT",
        body: JSON.stringify({ enabled: false }),
      }),
    );
    expect(
      screen.getByText(/Merges will not start release workflows/),
    ).toBeInTheDocument();
  });

  it("adds a write-only custom release secret", async () => {
    api
      .mockResolvedValueOnce({ ...settings, secrets: [] })
      .mockResolvedValueOnce(settings);
    renderSettings();

    await userEvent.click(
      await screen.findByRole("button", { name: "Add custom secret" }),
    );
    await userEvent.type(
      screen.getByRole("textbox", { name: /Secret name/ }),
      "NPM_TOKEN",
    );
    await userEvent.type(
      screen.getByLabelText(/Secret value/),
      "private-value",
    );
    await userEvent.click(screen.getByRole("button", { name: "Save secret" }));

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/repos/acme/widgets/release-secrets/NPM_TOKEN",
        {
          method: "PUT",
          body: JSON.stringify({ value: "private-value" }),
        },
      ),
    );
    expect(screen.getByText("NPM_TOKEN")).toBeInTheDocument();
    expect(screen.queryByText("private-value")).toBeNull();
  });

  // Turning publishing off used to turn the merge queue's base-branch
  // check off with it, silently (grain/task-330), and naming no jobs
  // still left it off until grain/task-458. Now the branch is judged by
  // everything a push would run either way, and this pane is where an
  // operator says which of those jobs they are willing to pay for.
  it("says the base branch is judged by the whole push plan and takes a narrower set", async () => {
    api
      .mockResolvedValueOnce({
        ...settings,
        enabled: false,
        baseBranchJobs: [],
      })
      .mockResolvedValueOnce({
        ...settings,
        enabled: false,
        baseBranchJobs: ["go", "lint"],
      });
    renderSettings();

    expect(
      await screen.findByText(/everything a push would run/),
    ).toBeInTheDocument();
    await userEvent.type(
      screen.getByRole("textbox", { name: /Jobs that judge base branches/ }),
      "go, lint",
    );
    await userEvent.click(screen.getByRole("button", { name: "Save jobs" }));

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/release", {
        method: "PUT",
        body: JSON.stringify({ baseBranchJobs: ["go", "lint"] }),
      }),
    );
    expect(
      await screen.findByText(/go, lint runs on push/),
    ).toBeInTheDocument();
  });

  // The setting is not the consequence (grain/task-453). The warning
  // above says what the switch means and reads the same on the day it is
  // flipped and on the day after next; this says what has become of the
  // branch since, and above all how long it has been true, which is the
  // sentence that would have ended a 33-hour outage of this control on
  // its first hour.
  it("says what has become of the base-branch check, and since when", async () => {
    api.mockResolvedValueOnce({
      ...settings,
      enabled: false,
      baseBranchJobs: [],
      baseBranchGate: {
        repo: "acme/widgets",
        branch: "main",
        judged: true,
        reason: "running-long",
        detail:
          "The base-branch run for acme/widgets's main has had no verdict " +
          "since it started 2026-09-19 07:32 UTC, 33h ago.",
      },
    });
    renderSettings();

    const alert = await screen.findByText(/has had no verdict/);
    expect(alert.textContent).toMatch(/33h ago/);
    expect(alert.closest(".MuiAlert-standardWarning")).not.toBeNull();
  });

  // An unjudged gate is said once, in the repository page's header
  // (RepoPage, from GET /api/base-branch-gates); this pane repeating it
  // put the same sentence on one page twice.
  it("leaves an unjudged gate to the page header", async () => {
    api.mockResolvedValueOnce({
      ...settings,
      baseBranchGate: {
        repo: "acme/widgets",
        branch: "main",
        judged: false,
        reason: "grain-ci-off",
        detail:
          "Nothing is testing acme/widgets's base branch main, and the merge " +
          "queue is merging onto it anyway.",
      },
    });
    renderSettings();

    expect(
      await screen.findByRole("button", { name: "Add custom secret" }),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Nothing is testing/)).toBeNull();
  });

  // The same distinction on the other switch (grain/task-511). "Merges
  // start release workflows" says what is meant to happen; this says
  // that it has not happened for nine merges, which since grain/task-461
  // nothing else on this deployment would ever mention.
  it("says when this repository's releases have stopped publishing", async () => {
    api.mockResolvedValueOnce({
      ...settings,
      baseBranchPublishing: {
        repo: "acme/widgets",
        branch: "main",
        failures: 9,
        failingForMS: 33 * 60 * 60 * 1000,
        runInFlight: false,
        runID: "rel-7",
        detail:
          "Nothing has published from acme/widgets main since 2026-09-19 " +
          "07:32 UTC, 33h ago: 9 releases have been started since and every " +
          "one of them published nothing. Nothing is held up by that.",
      },
    });
    renderSettings();

    const alert = await screen.findByText(
      /Nothing has published from acme\/widgets main/,
    );
    expect(alert.textContent).toMatch(/33h ago/);
    expect(alert.closest(".MuiAlert-standardWarning")).not.toBeNull();
  });

  // And a repository whose gate is working says nothing extra: the
  // finding is absent rather than green, for the reason every other
  // reading on this pane is absent when there is nothing to report.
  it("says nothing extra while the base-branch check is working", async () => {
    api.mockResolvedValueOnce({
      ...settings,
      enabled: false,
      baseBranchJobs: ["go", "lint"],
    });
    renderSettings();

    expect(
      await screen.findByText(/still checks this repository/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Nothing is testing/)).not.toBeInTheDocument();
  });

  // The other thing the switch governs, and the one that took a day of
  // hour-long CI runs to find (grain/task-397): this repository's release
  // workflow is also what publishes the sandbox guest, and a guest is a
  // CI input rather than a release artifact.
  it("warns that the sandbox guest stops being published", async () => {
    api.mockResolvedValueOnce({
      ...settings,
      enabled: false,
      baseBranchJobs: ["go"],
      publishesSandboxGuest: true,
      sandboxGuestCacheJob: ".grain/workflows/image.yml:guest-cache",
    });
    renderSettings();

    expect(
      await screen.findByText(/publishes the sandbox guest/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(".grain/workflows/image.yml:guest-cache"),
    ).toBeInTheDocument();
  });

  // And the repair for it (grain/task-409): the cache job is named among
  // the jobs grain dispatches after every merge, so it follows the base
  // branch with releases off and the pane says so rather than warning
  // about an outage that is not happening.
  it("says the guest cache still follows the branch when it is dispatched", async () => {
    api.mockResolvedValueOnce({
      ...settings,
      enabled: false,
      baseBranchJobs: ["go"],
      publishesSandboxGuest: true,
      sandboxGuestCacheJob: ".grain/workflows/image.yml:guest-cache",
      postMergeDispatchJobs: [".grain/workflows/image.yml:guest-cache"],
    });
    renderSettings();

    expect(
      await screen.findByText(/goes on following it with releases off/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/builds one inline/)).not.toBeInTheDocument();
  });

  // And the repair for the repair (grain/task-457): for the repository
  // publishing the guest this deployment's own sandboxes are built from,
  // grain dispatches that job itself, so the pane has nothing to ask for.
  // An operator who switched releases off should not also have to know
  // this setting exists to keep their CI fast.
  it("says grain keeps the guest cache warm without being asked", async () => {
    api.mockResolvedValueOnce({
      ...settings,
      enabled: false,
      baseBranchJobs: ["go"],
      publishesSandboxGuest: true,
      sandboxGuestCacheJob: ".grain/workflows/image.yml:guest-cache",
      sandboxGuestCacheAutomatic: true,
      postMergeDispatchJobs: [],
    });
    renderSettings();

    expect(
      await screen.findByText(/goes on following it with releases off/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Grain does that on its own here/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/builds one inline/)).not.toBeInTheDocument();
  });

  // What is left to warn about once grain fills the cache by itself: it
  // cannot here, because the job it would dispatch holds no credential to
  // push with. That is a grant to make, not a field to fill in.
  it("warns when it cannot fill the cache for want of the publish grant", async () => {
    api.mockResolvedValueOnce({
      ...settings,
      enabled: false,
      baseBranchJobs: ["go"],
      publishesSandboxGuest: true,
      sandboxGuestCacheJob: ".grain/workflows/image.yml:guest-cache",
      sandboxGuestCacheAutomatic: false,
      postMergeDispatchJobs: [],
    });
    renderSettings();

    expect(await screen.findByText(/builds one inline/)).toBeInTheDocument();
    expect(screen.getByText("package-publish")).toBeInTheDocument();
  });

  // The jobs themselves are named on this pane and are not part of the
  // switch above: they are dispatched whether releases are on or off,
  // which is what makes the cache independent of the release.
  it("takes the jobs dispatched after every merge", async () => {
    api
      .mockResolvedValueOnce({ ...settings, postMergeDispatchJobs: [] })
      .mockResolvedValueOnce({
        ...settings,
        postMergeDispatchJobs: [".grain/workflows/image.yml:guest-cache"],
      });
    renderSettings();

    await userEvent.type(
      await screen.findByRole("textbox", {
        name: /Jobs dispatched after every merge/,
      }),
      ".grain/workflows/image.yml:guest-cache",
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Save dispatched jobs" }),
    );

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/release", {
        method: "PUT",
        body: JSON.stringify({
          postMergeDispatchJobs: [".grain/workflows/image.yml:guest-cache"],
        }),
      }),
    );
  });

  // And says nothing of the sort for a repository that publishes no
  // guest, which is every repository but one.
  it("says nothing about a guest for a repository that publishes none", async () => {
    api.mockResolvedValueOnce({ ...settings, enabled: false });
    renderSettings();

    expect(
      await screen.findByText(/Merges will not start release workflows/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/sandbox guest/)).not.toBeInTheDocument();
  });

  it("runs a trusted release by hand and opens its run", async () => {
    api
      .mockResolvedValueOnce({ ...settings, enabled: false })
      .mockResolvedValueOnce({ id: "release-1" });
    const { onOpenRun } = renderSettings();

    await userEvent.click(
      await screen.findByRole("button", { name: "Run release by hand" }),
    );
    await userEvent.type(
      screen.getByRole("textbox", { name: /Release branch/ }),
      "main",
    );
    await userEvent.click(screen.getByRole("button", { name: "Run release" }));

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/release-runs", {
        method: "POST",
        body: JSON.stringify({ branch: "main" }),
      }),
    );
    expect(onOpenRun).toHaveBeenCalledWith("release-1", "acme/widgets");
  });

  // grain/task-2414: Run release, which had focus, is gone once the run
  // starts, so the run's pane handed focus back to nothing on close.
  it("puts focus back on the manual release button before opening the run", async () => {
    api
      .mockResolvedValueOnce({ ...settings, enabled: false })
      .mockResolvedValueOnce({ id: "release-1" });
    let focusedAtOpen = null;
    const { onOpenRun } = renderSettings();
    onOpenRun.mockImplementation(() => {
      focusedAtOpen = document.activeElement;
    });

    await userEvent.click(
      await screen.findByRole("button", { name: "Run release by hand" }),
    );
    await userEvent.type(
      screen.getByRole("textbox", { name: /Release branch/ }),
      "main{Enter}",
    );

    await waitFor(() => expect(onOpenRun).toHaveBeenCalled());
    expect(focusedAtOpen).toBe(
      screen.getByRole("button", { name: "Run release by hand" }),
    );
  });

  it("sends one manual release however many times Run release is clicked while it is on its way", async () => {
    let answer;
    api
      .mockResolvedValueOnce({ ...settings, enabled: false })
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            answer = resolve;
          }),
      );
    const { onOpenRun } = renderSettings();

    await userEvent.click(
      await screen.findByRole("button", { name: "Run release by hand" }),
    );
    await userEvent.type(
      screen.getByRole("textbox", { name: /Release branch/ }),
      "main",
    );
    const run = screen.getByRole("button", { name: "Run release" });
    await userEvent.click(run);
    expect(run).toBeDisabled();
    // A disabled button cannot be clicked, but the form can still be
    // submitted -- Enter in the branch box -- and that must not send a
    // second release either.
    fireEvent.submit(run.closest("form"));

    const releaseRuns = () =>
      api.mock.calls.filter(
        (c) => c[0] === "/api/repos/acme/widgets/release-runs",
      ).length;
    expect(releaseRuns()).toBe(1);

    answer({ id: "release-1" });
    await waitFor(() =>
      expect(onOpenRun).toHaveBeenCalledWith("release-1", "acme/widgets"),
    );
    expect(releaseRuns()).toBe(1);
  });

  // The second of the two by-hand controls (grain/task-480), and the one
  // that is not a release: run a base branch's own check now. One button
  // per branch grain is watching, because those rows are the whole of
  // what can be asked for.
  it("asks for a base branch's own check and says when it starts", async () => {
    api
      .mockResolvedValueOnce({ ...settings, baseBranches: ["main"] })
      .mockResolvedValueOnce({
        repo: "acme/widgets",
        branch: "main",
        runInFlight: false,
        detail:
          "grain will start a base-branch run for acme/widgets main at abc123 on its next cycle.",
      });
    renderSettings();

    await userEvent.click(
      await screen.findByRole("button", {
        name: "Run the base-branch check now",
      }),
    );

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/repos/acme/widgets/base-branch-runs",
        { method: "POST", body: JSON.stringify({ branch: "main" }) },
      ),
    );
    expect(await screen.findByText(/on its next cycle/)).toBeInTheDocument();
  });

  // A reader who holds only the viewer or proposer role on every team
  // owning the repository (RepoPage's writeRefusal) still reads the pane,
  // but every write on it is drawn disabled rather than offered to fail.
  it("offers a read-only reader none of its writes", async () => {
    api.mockResolvedValueOnce({
      ...settings,
      enabled: false,
      baseBranches: ["main"],
    });
    renderSettings({ readOnly: true });

    for (const name of [
      "Turn on automatic releases",
      "Run release by hand",
      "Run the base-branch check now",
      "Save jobs",
      "Save dispatched jobs",
    ]) {
      expect(await screen.findByRole("button", { name })).toBeDisabled();
    }
    // The job boxes too, not only their Save buttons: a box a reader
    // could type into was an edit the leave prompt then asked about and
    // nothing could save (docs/test-runs/2026-10-15-repo-tabs-standing-
    // work.md, finding 2).
    for (const name of [
      "Jobs that judge base branches",
      "Jobs dispatched after every merge",
    ]) {
      expect(screen.getByRole("textbox", { name })).toBeDisabled();
    }
    expect(
      screen.getByText(/Merges will not start release workflows/),
    ).toBeInTheDocument();
    expect(api).toHaveBeenCalledTimes(1);
  });

  // A repository with no base-branch row has nothing to re-run: no
  // button, rather than one that answers 404.
  it("offers no base-branch check for a repository grain is watching no branch of", async () => {
    api.mockResolvedValueOnce(settings);
    renderSettings();

    await screen.findByRole("button", { name: "Run release by hand" });
    expect(
      screen.queryByRole("button", { name: /base-branch check/ }),
    ).not.toBeInTheDocument();
  });

  it("counts an unsaved dispatched-jobs value as an edit leaving would lose", async () => {
    api
      .mockResolvedValueOnce({
        ...settings,
        postMergeDispatchJobs: ["a.yml:b"],
      })
      .mockResolvedValueOnce({
        ...settings,
        postMergeDispatchJobs: ["a.yml:b", "ci.yml:c"],
      });
    const { unmount } = render(
      <RepoReleaseWorkflowSettings
        repo="acme/widgets"
        onOpenRun={vi.fn()}
        showError={vi.fn()}
      />,
    );
    const box = await screen.findByLabelText(
      "Jobs dispatched after every merge",
    );
    expect(leavingLosesEdits()).toBe(false);

    fireEvent.change(box, { target: { value: "a.yml:b, ci.yml:c" } });
    expect(leavingLosesEdits()).toBe(true);
    // The same jobs spelled with other spacing are what is saved.
    fireEvent.change(box, { target: { value: " a.yml:b " } });
    expect(leavingLosesEdits()).toBe(false);

    fireEvent.change(box, { target: { value: "a.yml:b, ci.yml:c" } });
    await userEvent.click(
      screen.getByRole("button", { name: "Save dispatched jobs" }),
    );
    await waitFor(() => expect(api).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(leavingLosesEdits()).toBe(false));

    fireEvent.change(
      screen.getByLabelText("Jobs dispatched after every merge"),
      { target: { value: "unsaved" } },
    );
    expect(leavingLosesEdits()).toBe(true);
    unmount();
    expect(leavingLosesEdits()).toBe(false);
  });
});
