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

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import RepoReleases, {
  POLL_INTERVAL_MS,
  releaseActionReasons,
  withoutDanglingDependencies,
} from "./RepoReleases.jsx";
import api from "../api.js";
import { runTimers } from "../testClock.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const unconfiguredQualificationPlan = {
  configured: false,
  repo: "acme/widgets",
  requireApproval: false,
  autoPromote: false,
  items: [],
};

const smokeTemplate = {
  id: "template-1",
  name: "Smoke test",
  title: "Smoke test",
  autoMerge: false,
  capabilities: [],
};
const unrelatedTemplate = {
  id: "template-2",
  name: "Unrelated",
  title: "Unrelated",
  autoMerge: false,
  capabilities: [],
};

const activeRelease = {
  repo: "acme/widgets",
  name: "myfeat",
  latestBranch: "myfeat.latest",
  prodBranch: "myfeat",
  status: "active",
};

// Distinct branches for the active vs. promoted candidate below -- a real
// cut always produces a fresh branch, and giving the two fixtures the
// same one only makes "current candidate" vs. "history" harder to tell
// apart in a rendered tree that shows the current one in both places.
const activeCandidate = {
  id: 2,
  release: "myfeat",
  branch: "myfeat.rc.2",
  status: "active",
};
const promotedCandidate = {
  id: 1,
  release: "myfeat",
  branch: "myfeat.rc.1",
  status: "promoted",
};

// setupApi wires a routing fake covering every endpoint RepoReleases
// touches, backed by mutable state, the same way appTestHarness.js's
// setupApi does for the App tests -- unlike a finite chain of api.mockResolvedValueOnce
// calls, this keeps answering correctly no matter how many times the
// component's own poll (bwsalmon/agents#530) re-fetches in the
// background while a test is still running.
function setupApi({
  releases: releasesInit = [],
  candidates = [],
  qualificationPlan = unconfiguredQualificationPlan,
  qualificationRuns = {},
  nextCut = null,
  creation = { allowed: true },
} = {}) {
  let releasesState = [...releasesInit];
  let candidatesState = [...candidates];
  let qualificationPlanState = { ...qualificationPlan };
  let runsState = { ...qualificationRuns };

  api.mockImplementation((path, opts) => {
    const method = opts?.method || "GET";

    if (path === "/api/repos/acme/widgets/releases" && method === "POST") {
      const created = {
        repo: "acme/widgets",
        latestBranch: `${JSON.parse(opts.body).name}.latest`,
        prodBranch: JSON.parse(opts.body).name,
        status: "provisioning",
        name: JSON.parse(opts.body).name,
      };
      releasesState = [...releasesState, created];
      return Promise.resolve(created);
    }
    if (/^\/api\/repos\/[^/]+\/[^/]+\/release-creation$/.test(path)) {
      return Promise.resolve(creation);
    }
    if (path === "/api/repos/acme/widgets/releases" && method === "GET") {
      return Promise.resolve(releasesState);
    }
    const deleteMatch = path.match(
      /^\/api\/repos\/acme\/widgets\/releases\/([^/]+)$/,
    );
    if (deleteMatch && method === "DELETE") {
      releasesState = releasesState.filter((r) => r.name !== deleteMatch[1]);
      return Promise.resolve(null);
    }
    if (
      /^\/api\/repos\/acme\/widgets\/releases\/[^/]+\/candidates\/promote$/.test(
        path,
      ) &&
      method === "POST"
    ) {
      candidatesState = candidatesState.map((c, i) =>
        i === 0 ? { ...c, status: "promoted" } : c,
      );
      return Promise.resolve(candidatesState[0]);
    }
    if (
      /^\/api\/repos\/acme\/widgets\/releases\/[^/]+\/candidates$/.test(path)
    ) {
      if (method === "POST") {
        if (nextCut) candidatesState = [nextCut, ...candidatesState];
        return Promise.resolve(candidatesState[0]);
      }
      return Promise.resolve(candidatesState);
    }
    if (
      /^\/api\/repos\/acme\/widgets\/releases\/[^/]+\/merge$/.test(path) &&
      method === "POST"
    ) {
      releasesState = releasesState.map((r) => ({
        ...r,
        status: "merged",
        pullRequestUrl: "https://example/pull/1",
      }));
      return Promise.resolve(releasesState[0]);
    }
    const approveMatch = path.match(
      /^\/api\/repos\/[^/]+\/[^/]+\/candidates\/([^/]+)\/qualification\/approve$/,
    );
    if (approveMatch && method === "POST") {
      const run = runsState[approveMatch[1]];
      if (run) {
        runsState = {
          ...runsState,
          [approveMatch[1]]: {
            ...run,
            status: "running",
            tasks: run.tasks.map((t) => ({ ...t, approved: true })),
          },
        };
      }
      return Promise.resolve(runsState[approveMatch[1]] || null);
    }
    const runMatch = path.match(
      /^\/api\/repos\/[^/]+\/[^/]+\/candidates\/([^/]+)\/qualification$/,
    );
    if (runMatch && method === "GET") {
      return Promise.resolve(runsState[runMatch[1]] || null);
    }
    if (/^\/api\/repos\/[^/]+\/[^/]+\/qualification-plan$/.test(path)) {
      if (method === "PUT") {
        qualificationPlanState = {
          ...qualificationPlanState,
          ...JSON.parse(opts.body),
        };
      }
      return Promise.resolve(qualificationPlanState);
    }
    return Promise.resolve(null);
  });

  return {
    get candidatesState() {
      return candidatesState;
    },
  };
}

function current() {
  return within(document.body.querySelector(".candidate-current"));
}

describe("RepoReleases", () => {
  afterEach(() => {
    api.mockReset();
    vi.unstubAllGlobals();
    // The poll test below stops the clock; leave it running for
    // everything else in this file (see ../testClock.js).
    vi.useRealTimers();
  });

  it("loads the given repo's releases and shows the current candidate", async () => {
    setupApi({ releases: [activeRelease], candidates: [activeCandidate] });
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    await screen.findByRole("button", { name: "Promote current RC" });

    expect(
      screen.getByRole("heading", { name: "acme/widgets releases" }),
    ).toBeInTheDocument();
    expect(current().getByText("myfeat.rc.2")).toBeInTheDocument();
    expect(current().getByText(/active/)).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/releases");
    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/releases/myfeat/candidates",
    );
    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/qualification-plan",
    );
    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/candidates/2/qualification",
    );
  });

  it("loads a host-qualified repo's releases through its host-qualified owner", async () => {
    api.mockImplementation((path) =>
      Promise.resolve(
        path.endsWith("/qualification-plan")
          ? unconfiguredQualificationPlan
          : path.endsWith("/release-creation")
            ? { allowed: true }
            : [],
      ),
    );
    render(
      <RepoReleases
        repo="git.forge.example/experimental/bsalmon-test"
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    const root = "/api/repos/git.forge.example%2Fexperimental/bsalmon-test";

    await waitFor(() => expect(api).toHaveBeenCalledWith(`${root}/releases`));
    expect(api).toHaveBeenCalledWith(`${root}/qualification-plan`);
    expect(api).toHaveBeenCalledWith(`${root}/release-creation`);
  });

  // The release the address names (/repos/acme/widgets/releases/myfeat)
  // is the one shown, not whichever is newest -- which is what a reload,
  // Back, or a link somebody sent all depend on.
  const newerRelease = { ...activeRelease, name: "newer", prodBranch: "newer" };

  it("shows the release the address names rather than the newest", async () => {
    setupApi({
      releases: [newerRelease, activeRelease],
      candidates: [activeCandidate],
    });
    const onSelectRelease = vi.fn();
    render(
      <RepoReleases
        repo="acme/widgets"
        release="myfeat"
        onSelectRelease={onSelectRelease}
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    await screen.findByRole("button", { name: "Promote current RC" });

    expect(screen.getByRole("combobox", { name: "Release" })).toHaveTextContent(
      "myfeat",
    );
    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/releases/myfeat/candidates",
    );
    expect(api).not.toHaveBeenCalledWith(
      "/api/repos/acme/widgets/releases/newer/candidates",
    );
    expect(onSelectRelease).not.toHaveBeenCalled();
  });

  it("puts a picked release in the address", async () => {
    const user = userEvent.setup();
    setupApi({
      releases: [newerRelease, activeRelease],
      candidates: [activeCandidate],
    });
    const onSelectRelease = vi.fn();
    render(
      <RepoReleases
        repo="acme/widgets"
        onSelectRelease={onSelectRelease}
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    await screen.findByRole("button", { name: "Promote current RC" });
    // Opened without one, it shows the newest and leaves the address be.
    expect(screen.getByRole("combobox", { name: "Release" })).toHaveTextContent(
      "newer",
    );
    expect(onSelectRelease).not.toHaveBeenCalled();

    await user.click(screen.getByRole("combobox", { name: "Release" }));
    await user.click(await screen.findByRole("option", { name: /^myfeat/ }));

    expect(onSelectRelease).toHaveBeenCalledWith("myfeat");
  });

  it("shows the newest, and says so, when the address names a release the repo does not have", async () => {
    setupApi({ releases: [newerRelease, activeRelease] });
    const onSelectRelease = vi.fn();
    render(
      <RepoReleases
        repo="acme/widgets"
        release="gone"
        onSelectRelease={onSelectRelease}
        onBack={() => {}}
        showError={() => {}}
      />,
    );

    await vi.waitFor(() =>
      expect(onSelectRelease).toHaveBeenCalledWith(null, "gone"),
    );
    expect(screen.getByRole("combobox", { name: "Release" })).toHaveTextContent(
      "newer",
    );
  });

  it("says why Cut new RC is off while the current candidate is active", async () => {
    setupApi({ releases: [activeRelease], candidates: [activeCandidate] });
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    const reasons = await screen.findByTestId("release-action-reasons");
    expect(reasons).toHaveTextContent(
      "Cut new RC: myfeat.rc.2 is active: a new RC is cut once it is promoted.",
    );
    expect(reasons).not.toHaveTextContent("Promote current RC:");
  });

  it("says why Promote current RC is off once the candidate is promoted", async () => {
    setupApi({ releases: [activeRelease], candidates: [promotedCandidate] });
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    const reasons = await screen.findByTestId("release-action-reasons");
    expect(reasons).toHaveTextContent(
      "Promote current RC: myfeat.rc.1 is already promoted: a candidate is promoted once.",
    );
    expect(reasons).not.toHaveTextContent("Cut new RC:");
    expect(screen.getByRole("button", { name: "Cut new RC" })).toBeEnabled();
  });

  it("follows the address when Back or Forward changes the release it names", async () => {
    setupApi({
      releases: [newerRelease, activeRelease],
      candidates: [activeCandidate],
    });
    const props = {
      repo: "acme/widgets",
      onSelectRelease: () => {},
      onBack: () => {},
      showError: () => {},
    };
    const { rerender } = render(<RepoReleases {...props} release="myfeat" />);
    await screen.findByRole("button", { name: "Promote current RC" });

    rerender(<RepoReleases {...props} release="newer" />);
    await vi.waitFor(() =>
      expect(
        screen.getByRole("combobox", { name: "Release" }),
      ).toHaveTextContent("newer"),
    );

    rerender(<RepoReleases {...props} release="myfeat" />);
    await vi.waitFor(() =>
      expect(
        screen.getByRole("combobox", { name: "Release" }),
      ).toHaveTextContent("myfeat"),
    );
    // And back to the bare address, which is the newest again.
    rerender(<RepoReleases {...props} release={null} />);
    await vi.waitFor(() =>
      expect(
        screen.getByRole("combobox", { name: "Release" }),
      ).toHaveTextContent("newer"),
    );
  });

  it("enables Promote but not Cut when the current candidate is still active", async () => {
    setupApi({ releases: [activeRelease], candidates: [activeCandidate] });
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    await screen.findByRole("button", { name: "Promote current RC" });

    expect(screen.getByRole("button", { name: "Cut new RC" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Promote current RC" }),
    ).toBeEnabled();
  });

  it("shows a note and no release picker when the repo has no releases yet", async () => {
    setupApi({ releases: [], candidates: [] });
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );

    expect(await screen.findByText(/has no releases yet/)).toBeInTheDocument();
    expect(screen.queryByLabelText("Release")).not.toBeInTheDocument();
  });

  it("creates a new release from the form", async () => {
    setupApi({ releases: [] });
    const user = userEvent.setup();
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    await screen.findByLabelText(/New release name/);

    await user.type(screen.getByLabelText(/New release name/), "myfeat");
    await user.click(screen.getByRole("button", { name: "Create release" }));

    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/releases", {
      method: "POST",
      body: JSON.stringify({ name: "myfeat" }),
    });
    await screen.findByLabelText("Release");
  });

  it("cuts a new RC when eligible", async () => {
    setupApi({
      releases: [activeRelease],
      candidates: [promotedCandidate],
      nextCut: activeCandidate,
    });
    const user = userEvent.setup();
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    await screen.findByRole("button", { name: "Promote current RC" });

    await user.click(screen.getByRole("button", { name: "Cut new RC" }));

    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/releases/myfeat/candidates",
      { method: "POST" },
    );
    expect(await current().findByText("myfeat.rc.2")).toBeInTheDocument();
  });

  it("promotes the current RC when eligible, once confirmed", async () => {
    setupApi({ releases: [activeRelease], candidates: [activeCandidate] });
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const user = userEvent.setup();
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    await screen.findByRole("button", { name: "Promote current RC" });

    await user.click(
      screen.getByRole("button", { name: "Promote current RC" }),
    );

    expect(confirm).toHaveBeenCalledWith(
      "Promote myfeat.rc.2 to myfeat? This moves acme/widgets's myfeat branch to myfeat.rc.2.",
    );
    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/releases/myfeat/candidates/promote",
      { method: "POST" },
    );
    expect(await current().findByText(/promoted/)).toBeInTheDocument();
  });

  // Promotion moves the prod branch on GitHub, so declining the
  // confirmation must leave it where it is (grain/task-1423).
  it("does not promote when the confirmation is declined", async () => {
    setupApi({ releases: [activeRelease], candidates: [activeCandidate] });
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(false));
    const user = userEvent.setup();
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    await screen.findByRole("button", { name: "Promote current RC" });

    await user.click(
      screen.getByRole("button", { name: "Promote current RC" }),
    );

    expect(confirm).toHaveBeenCalledTimes(1);
    expect(api).not.toHaveBeenCalledWith(
      "/api/repos/acme/widgets/releases/myfeat/candidates/promote",
      { method: "POST" },
    );
    expect(current().getByText(/active/)).toBeInTheDocument();
  });

  it("requests a merge back to the default branch, once confirmed", async () => {
    setupApi({ releases: [activeRelease], candidates: [promotedCandidate] });
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const user = userEvent.setup();
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    await screen.findByRole("button", {
      name: "Merge myfeat into the default branch",
    });

    await user.click(
      screen.getByRole("button", {
        name: "Merge myfeat into the default branch",
      }),
    );

    expect(confirm).toHaveBeenCalledWith(
      "Merge myfeat into acme/widgets's default branch? This opens a pull request on GitHub from myfeat.",
    );
    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/releases/myfeat/merge",
      { method: "POST" },
    );
    expect(await screen.findByText(/Merge requested/)).toBeInTheDocument();
  });

  // Merging back opens a pull request on GitHub, so declining the
  // confirmation must request nothing (grain/task-1423).
  it("does not request a merge back when the confirmation is declined", async () => {
    setupApi({ releases: [activeRelease], candidates: [promotedCandidate] });
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(false));
    const user = userEvent.setup();
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    const merge = await screen.findByRole("button", {
      name: "Merge myfeat into the default branch",
    });

    await user.click(merge);

    expect(confirm).toHaveBeenCalledTimes(1);
    expect(api).not.toHaveBeenCalledWith(
      "/api/repos/acme/widgets/releases/myfeat/merge",
      { method: "POST" },
    );
    expect(merge).toBeEnabled();
    expect(screen.queryByText(/Merge requested/)).not.toBeInTheDocument();
  });

  // A repo off the target repo list cannot have a release created in it
  // (ui.Client.CreateRelease refuses), so the pane says why in place of
  // offering a form every submission of which would be turned away.
  it("says why, instead of offering a create form, when the repo is off the target repo list", async () => {
    const reason =
      "nobody/missing is not on this deployment's target repo list, so a release cannot be created in it";
    setupApi({ releases: [], creation: { allowed: false, reason } });
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );

    expect(await screen.findByText(reason)).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/release-creation",
    );
    expect(screen.queryByLabelText(/New release name/)).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Create release" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText(/has no releases yet/)).not.toBeInTheDocument();
  });

  // Somebody who is not one of the repo's admins may not create, cut,
  // promote or merge back a release (ui.Client.requireRepoPerson), so the
  // pane says why in place of the form and leaves those buttons disabled.
  it("disables cut, promote and merge back for a caller who is not a repo admin", async () => {
    const reason =
      'user "mia" does not have permission to modify repository acme/widgets';
    setupApi({
      releases: [activeRelease],
      candidates: [promotedCandidate],
      creation: { allowed: false, manageable: false, reason },
    });
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );

    expect(await screen.findByText(reason)).toBeInTheDocument();
    expect((await screen.findAllByText("myfeat.rc.1")).length).toBeGreaterThan(
      0,
    );
    expect(screen.queryByLabelText(/New release name/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cut new RC" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Promote current RC" }),
    ).toBeDisabled();
    expect(
      screen.getByRole("button", {
        name: "Merge myfeat into the default branch",
      }),
    ).toBeDisabled();
  });

  // A release whose prod branch carried nothing the default branch did
  // not already have merges without a pull request at all, and says why
  // -- otherwise this pane showed a bare "Merge requested." next to a
  // link that was never coming.
  it("says why a merged release has no pull request when there was nothing to merge back", async () => {
    const settled = {
      ...activeRelease,
      status: "merged",
      mergeNote:
        "myfeat carried no commits main did not already have, so GitHub had no pull request to open.",
    };
    setupApi({ releases: [settled], candidates: [promotedCandidate] });
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );

    expect(
      await screen.findByText(/Nothing to merge back/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/carried no commits main did not already have/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Merge requested/)).not.toBeInTheDocument();
  });

  // The back button names the repo, not the repo list: this pane opens
  // from that repo's own page now (grain/task-111), which is one step
  // up rather than two.
  it("calls onBack when the back button is clicked", async () => {
    setupApi({ releases: [activeRelease], candidates: [activeCandidate] });
    const onBack = vi.fn();
    const user = userEvent.setup();
    render(
      <RepoReleases repo="acme/widgets" onBack={onBack} showError={() => {}} />,
    );
    await screen.findByRole("button", { name: "Promote current RC" });

    await user.click(screen.getByRole("button", { name: "← acme/widgets" }));

    expect(onBack).toHaveBeenCalledTimes(1);
  });

  it("shows a pending-approval banner and approves the run's tasks in bulk", async () => {
    const run = {
      id: 5,
      candidateId: 2,
      createdAt: "2026-08-27T12:00:00Z",
      status: "pending_approval",
      tasks: [
        {
          taskId: "10",
          templateId: "template-1",
          templateName: "Smoke test",
          instanceIndex: 1,
          repeat: 1,
          approved: false,
          state: "proposed",
        },
      ],
    };
    setupApi({
      releases: [activeRelease],
      candidates: [activeCandidate],
      qualificationRuns: { 2: run },
    });
    const user = userEvent.setup();
    const onOpenTask = vi.fn();
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        onOpenTask={onOpenTask}
        showError={() => {}}
      />,
    );
    const taskLink = await screen.findByRole("link", { name: "Smoke test" });
    expect(taskLink).toHaveAttribute("href", "/tasks/10");
    await user.click(taskLink);
    expect(onOpenTask).toHaveBeenCalledWith("10");

    expect(
      screen.getByText(
        /Pick from any template declared under Processes, except ones bound to another repo/,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "No templates this repo can qualify yet -- create one under Processes first.",
      ),
    ).toBeInTheDocument();

    expect(
      screen.getByText(/need approval before any of them can run/),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Approve all" }));

    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/candidates/2/qualification/approve",
      { method: "POST" },
    );
    expect(await screen.findByText("Running")).toBeInTheDocument();
  });

  it("shows a ready-to-promote banner once qualification succeeds", async () => {
    const run = {
      id: 5,
      candidateId: 2,
      createdAt: "2026-08-27T12:00:00Z",
      status: "succeeded",
      tasks: [
        {
          taskId: "10",
          templateId: "template-1",
          templateName: "Smoke test",
          instanceIndex: 1,
          repeat: 1,
          approved: true,
          state: "completed",
        },
      ],
    };
    setupApi({
      releases: [activeRelease],
      candidates: [activeCandidate],
      qualificationRuns: { 2: run },
    });
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );

    expect(await screen.findByText(/ready to promote/)).toBeInTheDocument();
  });

  it("shows a failed banner, with the failing task's own badge, when qualification fails", async () => {
    const run = {
      id: 5,
      candidateId: 2,
      createdAt: "2026-08-27T12:00:00Z",
      status: "failed",
      tasks: [
        {
          taskId: "10",
          templateId: "template-1",
          templateName: "Smoke test",
          instanceIndex: 1,
          repeat: 2,
          approved: true,
          state: "failed",
        },
        {
          taskId: "11",
          templateId: "template-1",
          templateName: "Smoke test",
          instanceIndex: 2,
          repeat: 2,
          approved: true,
          state: "completed",
        },
      ],
    };
    setupApi({
      releases: [activeRelease],
      candidates: [activeCandidate],
      qualificationRuns: { 2: run },
    });
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );

    expect(await screen.findByText(/Qualification failed/)).toBeInTheDocument();
    expect(screen.getByText("Failed")).toBeInTheDocument();
    // The failed instance's own "1/2" -- the ui.QualificationRun's own
    // failures-first ordering already sorted it first among the two.
    expect(screen.getByText("(1/2)")).toBeInTheDocument();
  });

  it("offers every template, and saves a new qualification item", async () => {
    setupApi({ releases: [activeRelease], candidates: [activeCandidate] });
    const user = userEvent.setup();
    render(
      <RepoReleases
        repo="acme/widgets"
        templates={[smokeTemplate, unrelatedTemplate]}
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    await screen.findByRole("button", { name: "Add item" });

    await user.click(screen.getByRole("button", { name: "Add item" }));
    await user.click(screen.getByLabelText("Template"));
    expect(
      screen.getByRole("option", { name: "Smoke test" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("option", { name: "Unrelated" }),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("option", { name: "Smoke test" }));
    await user.click(
      screen.getByRole("button", { name: "Save qualification plan" }),
    );

    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/qualification-plan",
      {
        method: "PUT",
        body: JSON.stringify({
          requireApproval: false,
          autoPromote: false,
          items: [{ templateId: "template-1", repeat: 1, dependsOn: [] }],
        }),
      },
    );
  });

  // grain/task-2414: "Depends on" offers only the templates the plan's
  // other items name, so a dependency left on a removed item's template
  // could not be unticked, and the server refused every save after.
  it("drops a removed item's template from the others' dependencies", async () => {
    setupApi({
      releases: [activeRelease],
      candidates: [activeCandidate],
      qualificationPlan: {
        configured: true,
        repo: "acme/widgets",
        requireApproval: false,
        autoPromote: false,
        items: [
          { templateId: "template-1", repeat: 1, dependsOn: [] },
          { templateId: "template-2", repeat: 1, dependsOn: ["template-1"] },
        ],
      },
    });
    const user = userEvent.setup();
    render(
      <RepoReleases
        repo="acme/widgets"
        templates={[smokeTemplate, unrelatedTemplate]}
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    await screen.findByRole("button", { name: "Save qualification plan" });

    await user.click(screen.getAllByRole("button", { name: "Remove" })[0]);
    await user.click(
      screen.getByRole("button", { name: "Save qualification plan" }),
    );

    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/qualification-plan",
      {
        method: "PUT",
        body: JSON.stringify({
          requireApproval: false,
          autoPromote: false,
          items: [{ templateId: "template-2", repeat: 1, dependsOn: [] }],
        }),
      },
    );
  });

  it("keeps a dependency another item still names, and drops one nobody does", () => {
    expect(
      withoutDanglingDependencies([
        { templateId: "a", repeat: 1, dependsOn: ["b", "gone"] },
        { templateId: "b", repeat: 1, dependsOn: [] },
        { templateId: "", repeat: 1, dependsOn: ["a"] },
      ]),
    ).toEqual([
      { templateId: "a", repeat: 1, dependsOn: ["b"] },
      { templateId: "b", repeat: 1, dependsOn: [] },
      { templateId: "", repeat: 1, dependsOn: ["a"] },
    ]);
  });

  // grain/task-47: this page is one repo, one page deeper than its own,
  // and its qualification plan is a list of templates -- so both carry
  // the figures the rest of the app names those kinds with
  // (ItemGlyph.jsx, docs/brand.md).
  it("heads the page with the repos glyph and glyphs the template picker", async () => {
    setupApi({ releases: [activeRelease], candidates: [activeCandidate] });
    const user = userEvent.setup();
    render(
      <RepoReleases
        repo="acme/widgets"
        templates={[smokeTemplate]}
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    const heading = await screen.findByRole("heading", {
      name: "acme/widgets releases",
    });
    expect(
      heading.parentElement.querySelector('svg[data-glyph="repos"]'),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Add item" }));
    await user.click(screen.getByLabelText("Template"));
    expect(
      (await screen.findByRole("option", { name: "Smoke test" })).querySelector(
        'svg[data-glyph="templates"]',
      ),
    ).toBeInTheDocument();
    expect(
      screen
        .getByRole("option", { name: "Choose a template" })
        .querySelector("svg[data-glyph]"),
    ).toBeNull();
  });

  // The pane's poll is a timer, so this test runs the clock rather than
  // waiting out three real seconds of it. POLL_INTERVAL_MS comes from
  // the component rather than being written out again here, so raising
  // the interval cannot leave a test behind that waits the old one and
  // then reports the poll as broken.
  it("polls the candidate and qualification state on an interval", async () => {
    vi.useFakeTimers();
    setupApi({ releases: [activeRelease], candidates: [activeCandidate] });
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    // getBy after a step of the fake clock rather than findBy: waitFor
    // polls on a timer of its own, which a fake clock also stops.
    await runTimers();
    expect(
      screen.getByRole("button", { name: "Promote current RC" }),
    ).toBeInTheDocument();

    const candidateCalls = () =>
      api.mock.calls.filter(
        (c) => c[0] === "/api/repos/acme/widgets/releases/myfeat/candidates",
      ).length;
    const callsBefore = candidateCalls();

    // A hair short of the interval asks for nothing new...
    await runTimers(POLL_INTERVAL_MS - 100);
    expect(candidateCalls()).toBe(callsBefore);

    // ...and crossing it re-reads the candidates.
    await runTimers(200);
    expect(candidateCalls()).toBeGreaterThan(callsBefore);
  });

  // A pick in the release picker used to change the heading, the branch
  // names and the merge button at once while the candidate, its error and
  // the Cut/Promote state stayed the previous release's until the next
  // poll -- up to POLL_INTERVAL_MS of one release's controls answering
  // for another (docs/test-runs/2026-10-01-repository-release-memory-ui.md).
  it("never shows the previous release's candidate after another release is picked", async () => {
    const otherRelease = {
      ...activeRelease,
      name: "2.1",
      latestBranch: "2.1.latest",
      prodBranch: "2.1",
    };
    const otherCandidate = {
      id: 7,
      release: "2.1",
      branch: "2.1.rc.1",
      status: "promoted",
    };
    let releaseOther;
    const otherCandidates = new Promise((resolve) => {
      releaseOther = resolve;
    });
    api.mockImplementation((path) => {
      if (path === "/api/repos/acme/widgets/releases")
        return Promise.resolve([activeRelease, otherRelease]);
      if (path === "/api/repos/acme/widgets/qualification-plan")
        return Promise.resolve(unconfiguredQualificationPlan);
      if (path === "/api/repos/acme/widgets/releases/myfeat/candidates")
        return Promise.resolve([activeCandidate]);
      if (path === "/api/repos/acme/widgets/releases/2.1/candidates")
        return otherCandidates;
      return Promise.resolve(null);
    });
    const user = userEvent.setup();
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    expect(
      await screen.findByText("myfeat.rc.2", {
        selector: ".candidate-current strong",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Promote current RC" }),
    ).toBeEnabled();

    await user.click(screen.getByRole("combobox", { name: "Release" }));
    await user.click(screen.getByRole("option", { name: /^2\.1 -- / }));

    // 2.1's candidates are still on their way: nothing of myfeat's is
    // left on the page, and neither button acts on a candidate nobody
    // has seen.
    expect(
      screen.getByText("latest: 2.1.latest, prod: 2.1"),
    ).toBeInTheDocument();
    expect(screen.queryByText("myfeat.rc.2")).not.toBeInTheDocument();
    expect(screen.getByText("Loading candidates…")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cut new RC" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Promote current RC" }),
    ).toBeDisabled();
    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/releases/2.1/candidates",
    );

    releaseOther([otherCandidate]);
    expect(
      await screen.findByText("2.1.rc.1", {
        selector: ".candidate-current strong",
      }),
    ).toBeInTheDocument();
    expect(screen.queryByText("myfeat.rc.2")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cut new RC" })).toBeEnabled();
    expect(
      screen.getByRole("button", { name: "Promote current RC" }),
    ).toBeDisabled();
  });

  it("keeps the way back and the heading up while the first read is out, and says so if it fails", async () => {
    let fail;
    api.mockImplementation(
      () =>
        new Promise((_, reject) => {
          fail = reject;
        }),
    );
    const onBack = vi.fn();
    const user = userEvent.setup();
    render(
      <RepoReleases repo="acme/widgets" onBack={onBack} showError={() => {}} />,
    );

    expect(
      screen.getByRole("heading", { name: "acme/widgets releases" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Loading releases…")).toBeInTheDocument();

    fail(new Error("502 Bad Gateway"));
    expect(
      await screen.findByText(
        /Could not load acme\/widgets's releases: 502 Bad Gateway/,
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText("Loading releases…")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /acme\/widgets/ }));
    expect(onBack).toHaveBeenCalled();
  });

  it("displays an alert when there is an unjudged base branch gate for this repo", async () => {
    setupApi({ releases: [activeRelease], candidates: [activeCandidate] });
    render(
      <RepoReleases
        repo="acme/widgets"
        baseBranchGates={[
          {
            repo: "acme/widgets",
            branch: "main",
            judged: false,
            detail:
              "Nothing is testing acme/widgets's base branch main, and the merge queue is merging onto it anyway.",
          },
          {
            repo: "other/repo",
            branch: "main",
            judged: false,
            detail: "Other repo warning",
          },
        ]}
        onBack={() => {}}
        showError={() => {}}
      />,
    );

    const alert = await screen.findByText(
      /Nothing is testing acme\/widgets's base branch main/,
    );
    expect(alert).toBeInTheDocument();
    expect(screen.queryByText("Other repo warning")).not.toBeInTheDocument();
  });

  // docs/test-runs/2026-10-12-base-gate-warnings.md: the warning named a
  // repair nothing on this pane makes, and had no way to where it is made.
  it("links an unjudged gate's warning to the repository's settings", async () => {
    const user = userEvent.setup();
    setupApi({ releases: [activeRelease], candidates: [activeCandidate] });
    const onBack = vi.fn();
    render(
      <RepoReleases
        repo="acme/widgets"
        baseBranchGates={[
          {
            repo: "acme/widgets",
            judged: false,
            reason: "grain-ci-off",
            detail: "Nothing is testing acme/widgets's default branch.",
          },
        ]}
        onBack={onBack}
        showError={() => {}}
      />,
    );
    await screen.findByText(/Nothing is testing acme\/widgets's default/);
    await user.click(
      screen.getByRole("button", { name: "Open repository settings" }),
    );
    expect(onBack).toHaveBeenCalled();
  });

  // docs/test-runs/2026-10-30-repository-release-memory-ui.md: with the
  // page able to open a tab, a grain-CI-off gate goes straight to CI Mode
  // rather than to the Overview, where a second link had to be found.
  it("sends a grain-CI-off gate's warning straight to the CI tab", async () => {
    const user = userEvent.setup();
    setupApi({ releases: [activeRelease], candidates: [activeCandidate] });
    const onBack = vi.fn();
    const onOpenTab = vi.fn();
    const gate = {
      repo: "acme/widgets",
      judged: false,
      detail: "Nothing is testing acme/widgets's default branch.",
    };
    const { rerender } = render(
      <RepoReleases
        repo="acme/widgets"
        baseBranchGates={[{ ...gate, reason: "grain-ci-off" }]}
        onBack={onBack}
        onOpenTab={onOpenTab}
        showError={() => {}}
      />,
    );
    await screen.findByText(/Nothing is testing acme\/widgets's default/);
    expect(
      screen.queryByRole("button", { name: "Open repository settings" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Go to CI Mode" }));
    expect(onOpenTab).toHaveBeenCalledWith("ci");
    expect(onBack).not.toHaveBeenCalled();

    // A gate held for any other reason has no single switch to name, so
    // it still goes to the page.
    rerender(
      <RepoReleases
        repo="acme/widgets"
        baseBranchGates={[{ ...gate, reason: "never-run" }]}
        onBack={onBack}
        onOpenTab={onOpenTab}
        showError={() => {}}
      />,
    );
    await user.click(
      screen.getByRole("button", { name: "Open repository settings" }),
    );
    expect(onBack).toHaveBeenCalled();
  });

  it("ignores a stale in-flight poll that resolves after a release is removed", async () => {
    const secondRelease = {
      repo: "acme/widgets",
      name: "v1.0",
      latestBranch: "v1.0.latest",
      prodBranch: "v1.0",
      status: "active",
    };
    const initialReleases = [activeRelease, secondRelease];
    setupApi({
      releases: initialReleases,
      candidates: [activeCandidate],
    });
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const user = userEvent.setup();
    render(
      <RepoReleases
        repo="acme/widgets"
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    await screen.findByRole("button", { name: "Delete release" });

    // Start a background poll (via visibilitychange) whose GET /releases hangs
    // until after Delete release completes.
    let resolveSlowReleases;
    const baseImpl = api.getMockImplementation();
    let interceptNextGetReleases = true;
    api.mockImplementation((path, opts) => {
      const method = opts?.method || "GET";
      if (
        interceptNextGetReleases &&
        path === "/api/repos/acme/widgets/releases" &&
        method === "GET"
      ) {
        interceptNextGetReleases = false;
        return new Promise((resolve) => {
          resolveSlowReleases = () => resolve(initialReleases);
        });
      }
      return baseImpl(path, opts);
    });

    document.dispatchEvent(new Event("visibilitychange"));
    expect(resolveSlowReleases).toBeTypeOf("function");

    await user.click(screen.getByRole("button", { name: "Delete release" }));
    expect(
      await screen.findByRole("combobox", { name: "Release" }),
    ).toHaveTextContent("v1.0");

    // Now the stale poll from before the deletion resolves with the old list.
    resolveSlowReleases();
    await new Promise((r) => setTimeout(r, 20));

    expect(screen.getByRole("combobox", { name: "Release" })).toHaveTextContent(
      "v1.0",
    );
  });

  it("clears the URL release on Delete release without reporting it as missing", async () => {
    const secondRelease = {
      repo: "acme/widgets",
      name: "v1.0",
      latestBranch: "v1.0.latest",
      prodBranch: "v1.0",
      status: "active",
    };
    setupApi({
      releases: [activeRelease, secondRelease],
      candidates: [activeCandidate],
    });
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const onSelectRelease = vi.fn();
    const user = userEvent.setup();
    render(
      <RepoReleases
        repo="acme/widgets"
        release="myfeat"
        onSelectRelease={onSelectRelease}
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    await screen.findByRole("button", { name: "Delete release" });

    await user.click(screen.getByRole("button", { name: "Delete release" }));
    expect(
      await screen.findByRole("combobox", { name: "Release" }),
    ).toHaveTextContent("v1.0");
    expect(onSelectRelease).toHaveBeenCalledWith(null);
    expect(onSelectRelease).not.toHaveBeenCalledWith(null, "myfeat");
  });
});

describe("releaseActionReasons", () => {
  const release = activeRelease;
  it("says nothing for buttons that are on", () => {
    expect(
      releaseActionReasons({
        repo: "acme/widgets",
        release,
        candidate: null,
        manageRefused: false,
      }).cut,
    ).toBeNull();
  });
  it("gives one reason for both when the caller is not an admin", () => {
    const r = releaseActionReasons({
      repo: "acme/widgets",
      release,
      candidate: activeCandidate,
      manageRefused: true,
    });
    expect(r.cut).toBe(
      "Only acme/widgets's admins can cut or promote its release candidates.",
    );
    expect(r.promote).toBe(r.cut);
  });
  it("names a release that is still provisioning", () => {
    const r = releaseActionReasons({
      repo: "acme/widgets",
      release: { ...release, status: "provisioning" },
      candidate: null,
      manageRefused: false,
    });
    expect(r.cut).toBe(
      "myfeat is still provisioning: grain cuts and promotes its candidates once its myfeat.latest branch exists.",
    );
  });
  it("names a merged release", () => {
    const r = releaseActionReasons({
      repo: "acme/widgets",
      release: { ...release, status: "merge_requested" },
      candidate: promotedCandidate,
      manageRefused: false,
    });
    expect(r.promote).toBe(
      "myfeat is merge requested: only an active release cuts or promotes candidates.",
    );
  });
  it("explains a candidate in flight and a missing one", () => {
    expect(
      releaseActionReasons({
        repo: "acme/widgets",
        release,
        candidate: { ...activeCandidate, status: "cutting" },
        manageRefused: false,
      }),
    ).toEqual({
      cut: "myfeat.rc.2 is still being cut.",
      promote:
        "myfeat.rc.2 is cutting: only an active candidate can be promoted.",
    });
    expect(
      releaseActionReasons({
        repo: "acme/widgets",
        release,
        candidate: null,
        manageRefused: false,
      }).promote,
    ).toBe("No candidate to promote yet: cut one first.");
  });
});
