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

import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import UpgradePanel, {
  AutoUpgradeSettings,
  resolvedImage,
} from "./UpgradePanel.jsx";
import api from "../api.js";
import { fetchUpgradeTargets } from "../upgradeTargets.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));
// The branch field's drop-down (grain/task-1914), answered apart from
// api() so the status reads and posts each test queues stay in order
// whether or not a click opened the list.
vi.mock("../upgradeTargets.js", () => ({
  fetchUpgradeTargets: vi.fn(async () => ({
    targets: [],
    allowedBranches: ["main"],
  })),
}));

describe("UpgradePanel", () => {
  afterEach(() => {
    api.mockReset();
  });

  // 2026-10-13 deployment-operations walkthrough: a failed first read of
  // GET /api/upgrade left the tab blank once its snackbar had gone.
  it("says the upgrade status could not be read when the first read fails", async () => {
    api.mockRejectedValue(new Error("upgrader unreachable"));
    render(<UpgradePanel showError={() => {}} />);

    expect(
      await screen.findByText(
        /upgrade status could not be read \(upgrader unreachable\)/,
      ),
    ).toBeInTheDocument();
  });

  // grain/task-3689: the box starts on the channel the deployment
  // follows, so a deployment installed on latest is offered the next
  // release rather than main's head -- even when its last upgrade
  // attempt named another branch.
  it("defaults the branch to the channel this deployment follows", async () => {
    api.mockImplementation((url) => {
      if (String(url).includes("/api/upgrade/release-notes")) {
        return Promise.resolve({ notes: [] });
      }
      return Promise.resolve({
        enabled: true,
        mode: "image",
        channel: "latest",
        running: { image: "ghcr.io/bwsalmon/grain/grain:latest@sha256:0123" },
        phase: "ok",
        branch: "main",
      });
    });
    render(<UpgradePanel showError={() => {}} />);

    const box = await screen.findByLabelText(/Branch/);
    await waitFor(() => expect(box).toHaveValue("latest"));
  });

  it("shows a note instead of a form when the deployment has no upgrader configured", async () => {
    api.mockResolvedValueOnce({ enabled: false });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByText(/not available/i)).toBeInTheDocument();
    expect(screen.queryByLabelText(/Branch/)).not.toBeInTheDocument();
  });

  // grain/task-39: the copy above the box has to describe the pipeline
  // this deployment actually runs. It described the source one to
  // everybody until the API started saying which is which, which was a
  // promise of a checkout and a build to every host that has neither.

  it("describes the image pipeline on a deployment that pulls images", async () => {
    api.mockResolvedValue({ enabled: true, mode: "image", phase: "" });
    render(<UpgradePanel showError={() => {}} />);

    expect(
      await screen.findByText(/Pulls the image CI published/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/rebuilds bin\/grain/i)).not.toBeInTheDocument();
  });

  it("describes the build pipeline on a deployment that builds on the host", async () => {
    api.mockResolvedValue({ enabled: true, mode: "source", phase: "" });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByText(/rebuilds bin\/grain/i)).toBeInTheDocument();
    expect(
      screen.queryByText(/Pulls the image CI published/i),
    ).not.toBeInTheDocument();
  });

  // grain/task-1681: an operator typed "latest" expecting main's head and
  // got a release, or the other way round. The box says which is which.
  it("says what latest, main and candidate each mean", async () => {
    api.mockResolvedValue({ enabled: true, mode: "image", phase: "" });
    render(<UpgradePanel showError={() => {}} />);

    expect(
      await screen.findByText(
        /latest is the last promoted release, main is the head of main, and candidate \(or rc\) is the current release candidate/,
      ),
    ).toBeInTheDocument();
  });

  it("warns when the running release candidate was discarded and offers moving to latest or a replacement candidate", async () => {
    api.mockImplementation((path) => {
      if (String(path).startsWith("/api/upgrade/release-notes")) {
        return Promise.resolve({
          branch: "latest",
          from: "v2.3.0-rc.1",
          version: "v2.1.1",
          notes: [],
          message:
            "v2.1.1 is not newer than v2.3.0-rc.1, which this deployment runs",
        });
      }
      return Promise.resolve({
        enabled: true,
        mode: "image",
        channel: "candidate",
        running: { image: "ghcr.io/bwsalmon/grain/grain:v2.3.0-rc.1" },
        discardedRc: {
          running: "v2.3.0-rc.1",
          latest: "v2.1.1",
        },
        phase: "",
      });
    });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    expect(
      await screen.findByText(
        /Release candidate v2\.3\.0-rc\.1 was discarded\./,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/it will never downgrade on its own/),
    ).toBeInTheDocument();
    expect(screen.getByText("v2.3.0-rc.2")).toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: "Move to latest (v2.1.1)" }),
    );
    expect(screen.getByLabelText(/Branch/)).toHaveValue("latest");
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/upgrade/release-notes?branch=latest",
      ),
    );
  });

  it("offers upgrading to the replacement candidate when a discarded RC has been superseded", async () => {
    api.mockImplementation((path) => {
      if (String(path).startsWith("/api/upgrade/release-notes")) {
        return Promise.resolve({
          branch: "candidate",
          from: "v2.3.0-rc.1",
          version: "v2.3.0-rc.2",
          notes: [],
        });
      }
      return Promise.resolve({
        enabled: true,
        mode: "image",
        channel: "candidate",
        running: { image: "ghcr.io/bwsalmon/grain/grain:v2.3.0-rc.1" },
        discardedRc: {
          running: "v2.3.0-rc.1",
          latest: "v2.1.1",
          replacementRc: "v2.3.0-rc.2",
        },
        phase: "",
      });
    });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    expect(
      await screen.findByText(/Release candidate v2\.3\.0-rc\.1 was discarded/),
    ).toBeInTheDocument();
    const upgradeBtn = screen.getByRole("button", {
      name: "Upgrade to candidate (v2.3.0-rc.2)",
    });
    expect(upgradeBtn).toBeInTheDocument();

    await user.click(upgradeBtn);
    expect(screen.getByLabelText(/Branch/)).toHaveValue("candidate");
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/upgrade/release-notes?branch=candidate",
      ),
    );
  });

  // grain/task-1914: the standard places to upgrade to are a pick from a
  // list, and the box still takes any other branch typed into it.

  it("offers the standard branches and released versions in a drop-down", async () => {
    fetchUpgradeTargets.mockResolvedValueOnce({
      targets: [
        { name: "latest", kind: "channel", allowed: true },
        { name: "candidate", kind: "channel", allowed: true },
        { name: "main", kind: "channel", allowed: true },
        { name: "v1.6.0", kind: "version", allowed: true },
        { name: "v1.5.0", kind: "version", allowed: false },
      ],
      allowedBranches: ["main", "candidate", "latest", "v1.6.0"],
    });
    api
      .mockResolvedValueOnce({ enabled: true, mode: "image", phase: "" })
      .mockResolvedValueOnce({
        enabled: true,
        phase: "running",
        branch: "v1.6.0",
      });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    await user.click(await screen.findByLabelText(/Branch/));
    const list = await screen.findByRole("listbox");
    expect(within(list).getByText("Standard branches")).toBeInTheDocument();
    expect(within(list).getByText("Released versions")).toBeInTheDocument();
    expect(
      within(list).getByText(/the last promoted release/),
    ).toBeInTheDocument();
    // Listed, so it is visibly there, but not one this deployment takes.
    expect(within(list).getByText("v1.5.0").closest("li")).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    expect(
      screen.getByText(/only upgrades to main, candidate, latest, v1.6.0/),
    ).toBeInTheDocument();

    await user.click(within(list).getByText("v1.6.0"));
    expect(screen.getByLabelText(/Branch/)).toHaveValue("v1.6.0");
    await user.click(screen.getByRole("button", { name: "Upgrade" }));
    expect(api).toHaveBeenLastCalledWith("/api/upgrade", {
      method: "POST",
      body: JSON.stringify({ branch: "v1.6.0", afterTasksFinish: false }),
    });
  });

  it("still upgrades to a branch that is not in the list", async () => {
    fetchUpgradeTargets.mockResolvedValueOnce({
      targets: [{ name: "main", kind: "channel", allowed: true }],
      allowedBranches: ["**"],
    });
    api
      .mockResolvedValueOnce({ enabled: true, mode: "image", phase: "" })
      .mockResolvedValueOnce({
        enabled: true,
        phase: "running",
        branch: "grain/task-1914",
      });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    await user.type(await screen.findByLabelText(/Branch/), "grain/task-1914");
    await user.click(screen.getByRole("button", { name: "Upgrade" }));
    expect(api).toHaveBeenLastCalledWith("/api/upgrade", {
      method: "POST",
      body: JSON.stringify({
        branch: "grain/task-1914",
        afterTasksFinish: false,
      }),
    });
  });

  // Choosing a target previews what it brings (grain/task-3092); the
  // branch the box opens on -- where this deployment already is -- is
  // not previewed, since asking pulls an image.
  it("previews the release notes of a chosen target, and not of the one it opened on", async () => {
    api.mockImplementation((path) => {
      if (path.startsWith("/api/upgrade/release-notes")) {
        return Promise.resolve({
          branch: "v1.8.0",
          from: "v1.7.0",
          version: "v1.8.0",
          notes: [
            {
              version: "v1.8.0",
              markdown: "# v1.8.0\n\n- Rendezvous relay.\n",
            },
          ],
        });
      }
      return Promise.resolve({
        enabled: true,
        mode: "image",
        phase: "ok",
        branch: "main",
      });
    });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    const box = await screen.findByLabelText(/Branch/);
    await waitFor(() => expect(box).toHaveValue("main"));
    await new Promise((resolve) => setTimeout(resolve, 1000));
    expect(api).not.toHaveBeenCalledWith(
      expect.stringContaining("/api/upgrade/release-notes"),
    );

    await user.clear(box);
    await user.type(box, "v1.8.0");
    expect(
      await screen.findByText("Rendezvous relay.", {}, { timeout: 3000 }),
    ).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith(
      "/api/upgrade/release-notes?branch=v1.8.0",
    );
  });

  // docs/test-runs/2026-10-05-image-signing-and-upgrade-targets.md,
  // finding E: a failed list read was swallowed into an empty list whose
  // helper still said "Pick one from the list".
  it("says when the list of targets could not be loaded, and still takes a typed branch", async () => {
    const err = new Error("bad gateway");
    err.status = 502;
    fetchUpgradeTargets.mockRejectedValueOnce(err);
    api.mockResolvedValueOnce({ enabled: true, mode: "image", phase: "" });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    const input = await screen.findByLabelText(/Branch/);
    await user.click(input);
    expect(
      await screen.findByText(
        /The list of branches could not be loaded \(bad gateway\); type a branch instead\./,
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/Pick one from the list/),
    ).not.toBeInTheDocument();
    await user.type(input, "feature/x");
    expect(input).toHaveValue("feature/x");
  });

  it("says nothing about a list an older daemon does not serve", async () => {
    const err = new Error("no such API endpoint");
    err.status = 404;
    fetchUpgradeTargets.mockRejectedValueOnce(err);
    api.mockResolvedValueOnce({ enabled: true, mode: "image", phase: "" });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    await user.click(await screen.findByLabelText(/Branch/));
    expect(
      await screen.findByText(/Pick one from the list or type any branch\./),
    ).toBeInTheDocument();
    expect(screen.queryByText(/could not be loaded/)).not.toBeInTheDocument();
  });

  it("shows every target when opened on a branch that is already chosen", async () => {
    fetchUpgradeTargets.mockResolvedValueOnce({
      targets: [
        { name: "latest", kind: "channel", allowed: true },
        { name: "main", kind: "channel", allowed: true },
      ],
      allowedBranches: ["main", "latest"],
    });
    api.mockResolvedValueOnce({ enabled: true, phase: "ok", branch: "main" });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    const input = await screen.findByLabelText(/Branch/);
    expect(input).toHaveValue("main");
    await user.click(input);
    const list = await screen.findByRole("listbox");
    expect(within(list).getByText("latest")).toBeInTheDocument();
  });

  it("claims neither pipeline when the API names no mode", async () => {
    // An older daemon serving this UI: better one sentence true of both
    // than a confident description of the wrong one.
    api.mockResolvedValue({ enabled: true, phase: "" });
    render(<UpgradePanel showError={() => {}} />);

    expect(
      await screen.findByText(
        /Upgrades this deployment onto the branch below/i,
      ),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(/Branch/)).toBeInTheDocument();
  });

  // grain/task-50: the pane said where the button would take a
  // deployment and nothing about where it already was, which on a host
  // whose last cut-over came from scripts/setup.sh is not recorded
  // anywhere else the pane can see.

  it("names the image this deployment is running", async () => {
    api.mockResolvedValue({
      enabled: true,
      mode: "image",
      phase: "",
      running: { image: "ghcr.io/bwsalmon/grain/grain:main" },
    });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByText(/Now running/)).toHaveTextContent(
      "Now running ghcr.io/bwsalmon/grain/grain:main",
    );
  });

  it("names the commit an installed binary was built from", async () => {
    api.mockResolvedValue({
      enabled: true,
      mode: "source",
      phase: "",
      running: { commit: "0fbfb4619f0a1c2d3e4f5a6b", modified: true },
    });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByText(/Now running/)).toHaveTextContent(
      "Now running 0fbfb46-dirty",
    );
    expect(screen.getByTitle("0fbfb4619f0a1c2d3e4f5a6b")).toBeInTheDocument();
  });

  it("says nothing when the daemon cannot say what is running", async () => {
    // An older daemon, or a deployment whose ref file cannot be read:
    // one line fewer, and a form that still works.
    api.mockResolvedValue({ enabled: true, mode: "image", phase: "" });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByLabelText(/Branch/)).toBeInTheDocument();
    expect(screen.queryByText(/Now running/)).not.toBeInTheDocument();
  });

  it("submits the entered branch and shows the resulting status", async () => {
    api
      .mockResolvedValueOnce({ enabled: true, phase: "", branch: "" })
      .mockResolvedValueOnce({
        enabled: true,
        phase: "running",
        branch: "grain/issue-396",
      });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    const input = await screen.findByLabelText(/Branch/);
    await user.type(input, "grain/issue-396");
    await user.click(screen.getByRole("button", { name: "Upgrade" }));

    expect(api).toHaveBeenLastCalledWith("/api/upgrade", {
      method: "POST",
      body: JSON.stringify({
        branch: "grain/issue-396",
        afterTasksFinish: false,
      }),
    });
    expect(await screen.findByText("running")).toBeInTheDocument();
    expect(screen.getByText("grain/issue-396")).toBeInTheDocument();
  });

  // The button is the one control in this app that restarts the
  // deployment, so the states around the click are worth pinning down as
  // firmly as the click itself: what it does with an empty box, what it
  // looks like while an upgrade it started is still going, and what
  // happens when the server refuses.

  it("defaults a new upgrade to the branch of the current version", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      phase: "ok",
      branch: "grain/release",
    });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByLabelText(/Branch/)).toHaveValue("grain/release");
  });

  it("does not overwrite a typed branch when polling while phase is ok", async () => {
    api.mockImplementation((url) => {
      if (String(url).includes("/api/upgrade/release-notes")) {
        return Promise.resolve({ notes: [] });
      }
      return Promise.resolve({
        enabled: true,
        phase: "ok",
        branch: "grain/release",
        scheduled: { branch: "grain/next" },
      });
    });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    const box = await screen.findByLabelText(/Branch/);
    expect(box).toHaveValue("grain/release");
    await user.clear(box);
    await user.type(box, "v2.0.1");
    expect(box).toHaveValue("v2.0.1");

    await act(async () => {
      await new Promise((r) => setTimeout(r, 1600));
    });
    expect(box).toHaveValue("v2.0.1");
  });

  it("does not reuse the branch of a failed upgrade", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      phase: "failed",
      branch: "grain/broken",
    });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByLabelText(/Branch/)).toHaveValue("");
  });

  it("defaults branch to running.branch if available and phase is not ok", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      phase: "",
      running: { branch: "main", commit: "1234567" },
    });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByLabelText(/Branch/)).toHaveValue("main");
  });

  it("does not start an upgrade from an empty branch box", async () => {
    api.mockResolvedValueOnce({ enabled: true, phase: "", branch: "" });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    await screen.findByLabelText(/Branch/);
    await user.click(screen.getByRole("button", { name: "Upgrade" }));

    // Only the initial GET: nothing was posted.
    expect(api).toHaveBeenCalledTimes(1);
    expect(api).toHaveBeenCalledWith("/api/upgrade");
  });

  it("does not start an upgrade from a branch box holding only spaces", async () => {
    api.mockResolvedValueOnce({ enabled: true, phase: "", branch: "" });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    await user.type(await screen.findByLabelText(/Branch/), "   ");
    await user.click(screen.getByRole("button", { name: "Upgrade" }));

    expect(api).toHaveBeenCalledTimes(1);
  });

  it("trims the branch it posts", async () => {
    api
      .mockResolvedValueOnce({ enabled: true, phase: "", branch: "" })
      .mockResolvedValueOnce({
        enabled: true,
        phase: "running",
        branch: "main",
      });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    // A branch pasted out of a terminal or a PR page arrives with
    // whitespace around it more often than not, and the API rejects
    // anything validateBranch does not like (pkg/upgrade).
    await user.type(await screen.findByLabelText(/Branch/), "  main  ");
    await user.click(screen.getByRole("button", { name: "Upgrade" }));

    expect(api).toHaveBeenLastCalledWith("/api/upgrade", {
      method: "POST",
      body: JSON.stringify({ branch: "main", afterTasksFinish: false }),
    });
  });

  // grain/task-213: the restart an upgrade ends in discards every run in
  // flight, and the pane used to say nothing about that -- an operator
  // pressing the button at the wrong moment threw away however many
  // agent-hours happened to be under way.

  it("says how many runs an upgrade would discard", async () => {
    api.mockResolvedValue({
      enabled: true,
      mode: "source",
      phase: "",
      inFlightRuns: 3,
    });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByText(/3 runs are in flight/)).toHaveTextContent(
      /the work is lost and the tasks are queued again/,
    );
  });

  // task 1389: only the runs on a substrate the restart stops are lost,
  // and the pane says which frameworks they are on and how many survive.
  it("says which in-flight runs an upgrade would lose, by framework", async () => {
    api.mockResolvedValue({
      enabled: true,
      mode: "image",
      phase: "",
      inFlightRuns: 4,
      atRisk: {
        lost: 3,
        lostByFramework: [
          { framework: "claude", count: 2 },
          { framework: "setup", count: 1 },
        ],
        survive: 1,
      },
    });
    render(<UpgradePanel showError={() => {}} />);

    const warning = await screen.findByText(/4 runs are in flight/);
    expect(warning).toHaveTextContent(/3 would be lost \(claude 2, setup 1\)/);
    expect(warning).toHaveTextContent(
      /1 is in a run host or grain hostd and would be adopted/,
    );
  });

  it("shows what the last restart adopted and lost", async () => {
    api.mockResolvedValue({
      enabled: true,
      mode: "image",
      phase: "ok",
      branch: "main",
      lastRestart: {
        at: "2026-10-26T07:15:00Z",
        adopted: [
          {
            run: "1115-1",
            task: "1115",
            framework: "antigravity",
            reason: "its spool went 12s unread",
          },
        ],
        lost: [
          {
            run: "1120-1",
            task: "1120",
            framework: "claude",
            reason: "its CLI stopped",
            session: "director",
          },
          {
            run: "1121-1",
            task: "1121",
            setup: true,
            reason: "still setting up",
          },
        ],
        lostByFramework: [
          { framework: "claude", count: 1 },
          { framework: "setup", count: 1 },
        ],
      },
    });
    const { container } = render(<UpgradePanel showError={() => {}} />);

    await screen.findByText(/Last restart/);
    const summary = container.querySelector(".upgrade-last-restart-summary");
    expect(summary).toHaveTextContent("1 adopted, 2 lost (claude 1, setup 1).");
    expect(screen.getByRole("link", { name: "1120-1" })).toHaveAttribute(
      "href",
      "/tasks/1120",
    );
    expect(screen.getByText(/1121-1/).closest("li")).toHaveTextContent(
      /lost: run 1121-1 in setup/,
    );
    expect(screen.getByText(/1120-1/).closest("li")).toHaveTextContent(
      /lost: run 1120-1 \(the Director session\) on claude/,
    );
  });

  it("says which agent CLIs an upgrade interrupts, with nothing in flight", async () => {
    api.mockResolvedValue({
      enabled: true,
      mode: "image",
      phase: "",
      inFlightRuns: 0,
      upgradeInterrupts: [
        {
          framework: "codex",
          note: "A daemon upgrade interrupts codex runs: codex runs in the daemon's own container.",
        },
      ],
    });
    const { container } = render(<UpgradePanel showError={() => {}} />);
    await screen.findByLabelText(/Branch/);
    expect(container.querySelector(".upgrade-interrupts")).toHaveTextContent(
      "A daemon upgrade interrupts codex runs",
    );
  });

  it("warns about nothing on an idle deployment", async () => {
    api.mockResolvedValue({
      enabled: true,
      mode: "source",
      phase: "",
      inFlightRuns: 0,
    });
    render(<UpgradePanel showError={() => {}} />);

    await screen.findByLabelText(/Branch/);
    expect(screen.queryByText(/in flight/)).not.toBeInTheDocument();
  });

  it("confirms before discarding runs, and posts nothing when told not to", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      phase: "",
      branch: "",
      inFlightRuns: 2,
    });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    await user.type(await screen.findByLabelText(/Branch/), "main");
    await user.click(screen.getByRole("button", { name: "Upgrade" }));

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toBeInTheDocument();
    expect(
      within(dialog).getByText(/2 runs are in flight/),
    ).toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );

    // Only the initial GET: the upgrade was called off.
    expect(api).toHaveBeenCalledTimes(1);
  });

  it("confirms before discarding runs, and posts when confirmed", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        phase: "",
        branch: "",
        inFlightRuns: 2,
      })
      .mockResolvedValueOnce({
        enabled: true,
        phase: "running",
        branch: "main",
      });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    await user.type(await screen.findByLabelText(/Branch/), "main");
    await user.click(screen.getByRole("button", { name: "Upgrade" }));

    const dialog = await screen.findByRole("dialog");
    expect(dialog).toBeInTheDocument();
    expect(
      within(dialog).getByText(/2 runs are in flight/),
    ).toBeInTheDocument();

    await user.click(within(dialog).getByRole("button", { name: "Upgrade" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );

    expect(api).toHaveBeenLastCalledWith("/api/upgrade", {
      method: "POST",
      body: JSON.stringify({ branch: "main", afterTasksFinish: false }),
    });
  });

  it("does not ask twice when there is nothing in flight to lose", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        phase: "",
        branch: "",
        inFlightRuns: 0,
      })
      .mockResolvedValueOnce({
        enabled: true,
        phase: "running",
        branch: "main",
      });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    await user.type(await screen.findByLabelText(/Branch/), "main");
    await user.click(screen.getByRole("button", { name: "Upgrade" }));

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(api).toHaveBeenLastCalledWith("/api/upgrade", {
      method: "POST",
      body: JSON.stringify({ branch: "main", afterTasksFinish: false }),
    });
  });

  it("locks the form while an upgrade is running", async () => {
    api.mockResolvedValue({
      enabled: true,
      phase: "running",
      branch: "main",
      detail: "",
    });
    render(<UpgradePanel showError={() => {}} />);

    // One upgrade at a time (pkg/upgrade's Upgrader): a second click
    // during one would only ever come back as an error, so the button
    // says what is happening instead of inviting it.
    const button = await screen.findByRole("button", { name: "Upgrading…" });
    expect(button).toBeDisabled();
    expect(screen.getByLabelText(/Branch/)).toBeDisabled();
  });

  // The brand mark, big, while the upgrade is in flight: the step list
  // below it can sit unchanged for minutes on one build line, and the
  // only thing on the pane that then says "still working" rather than
  // "wedged" is a figure that keeps moving.

  it("shows a large animated mark while an upgrade is running", async () => {
    api.mockResolvedValue({
      enabled: true,
      phase: "running",
      branch: "main",
    });
    render(<UpgradePanel showError={() => {}} />);

    // What this pins is that the pane asks for a mark at all, that it
    // names the branch being upgraded onto, and that it asks for one
    // three times the size of the sidebar's. The size is asserted off
    // the style box GrainMark sets, which is the one thing common to
    // both of the elements it can render -- the live canvas, and the
    // still it falls back to where there is no canvas to paint on,
    // which in jsdom is everywhere.
    const mark = await screen.findByTitle("grain — upgrading to main");
    expect(mark).toHaveStyle({ width: "96px", height: "96px" });
  });

  it("takes the mark away once the upgrade is over", async () => {
    // Not work in progress any more -- and a mark still cycling over a
    // finished upgrade (or, worse, a failed one) would say it was.
    api.mockResolvedValue({
      enabled: true,
      phase: "ok",
      branch: "main",
      detail: "pulled ghcr.io/bwsalmon/grain/grain:main; restarting",
    });
    render(<UpgradePanel showError={() => {}} />);

    await screen.findByText("ok");
    expect(screen.queryByTitle(/upgrading/)).not.toBeInTheDocument();
  });

  it("keeps polling until the upgrade lands, then stops", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        phase: "running",
        branch: "main",
      })
      .mockResolvedValueOnce({
        enabled: true,
        phase: "running",
        branch: "main",
      })
      .mockResolvedValue({
        enabled: true,
        phase: "ok",
        branch: "main",
        detail: "pulled ghcr.io/bwsalmon/grain/grain:main; restarting",
      });
    // act() around each step, so the re-render an answer causes has
    // happened by the time the assertions below read the DOM. The fake
    // clock has to be installed before the render, since the panel's poll
    // interval is scheduled the moment it learns an upgrade is running
    // and a real interval would not be advanced by the fake clock at all.
    const poll = async () => {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3000);
      });
    };

    vi.useFakeTimers();
    try {
      render(<UpgradePanel showError={() => {}} />);
      await act(async () => {});
      expect(screen.getByText("running")).toBeInTheDocument();

      // Two poll intervals: the first still says running, the second
      // brings back the finished upgrade.
      await poll();
      await poll();
      expect(screen.getByText("ok")).toBeInTheDocument();
      expect(
        screen.getByText(
          "pulled ghcr.io/bwsalmon/grain/grain:main; restarting",
        ),
      ).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Upgrade" })).toBeEnabled();

      // And the poll stops with it: an upgrade that has landed is not
      // worth a request every three seconds for as long as the pane is
      // open.
      const settled = api.mock.calls.length;
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30000);
      });
      expect(api).toHaveBeenCalledTimes(settled);
    } finally {
      vi.useRealTimers();
    }
  });

  it("reloads once the upgraded process starts answering", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        phase: "running",
        branch: "main",
      })
      .mockResolvedValueOnce({
        enabled: true,
        phase: "ok",
        branch: "main",
      })
      // The successful status is written before the old process restarts,
      // so its first config answer still carries the old build stamp.
      .mockResolvedValueOnce({ version: { commit: "old" } })
      .mockRejectedValueOnce(new Error("connection refused"))
      .mockResolvedValue({ version: { commit: "new" } });
    const onRestart = vi.fn();

    vi.useFakeTimers();
    try {
      render(
        <UpgradePanel
          showError={() => {}}
          version={{ commit: "old" }}
          onRestart={onRestart}
        />,
      );
      await act(async () => {});
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3000);
      });
      expect(onRestart).not.toHaveBeenCalled();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(2000);
      });
      expect(onRestart).toHaveBeenCalledTimes(1);
      expect(api).toHaveBeenLastCalledWith(
        expect.stringMatching(/^\/api\/config\?after-upgrade=/),
      );
    } finally {
      vi.useRealTimers();
    }
  });

  it("hands a refused upgrade to showError", async () => {
    api
      .mockResolvedValueOnce({ enabled: true, phase: "", branch: "" })
      .mockRejectedValueOnce(new Error("an upgrade is already in progress"));
    const showError = vi.fn();
    const user = userEvent.setup();
    render(<UpgradePanel showError={showError} />);

    await user.type(await screen.findByLabelText(/Branch/), "main");
    await user.click(screen.getByRole("button", { name: "Upgrade" }));

    await waitFor(() =>
      expect(showError).toHaveBeenCalledWith(
        new Error("an upgrade is already in progress"),
      ),
    );
    // The form stays usable: the upgrade never started, so the button
    // must not be left saying it did.
    expect(screen.getByRole("button", { name: "Upgrade" })).toBeEnabled();
  });

  it("safely falls back to console.error when showError is omitted or not a function", async () => {
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => {});
    api
      .mockResolvedValueOnce({ enabled: true, phase: "", branch: "" })
      .mockRejectedValueOnce(new Error("network error"));
    const user = userEvent.setup();
    render(<UpgradePanel />);

    await user.type(await screen.findByLabelText(/Branch/), "main");
    await user.click(screen.getByRole("button", { name: "Upgrade" }));

    await waitFor(() =>
      expect(consoleError).toHaveBeenCalledWith(new Error("network error")),
    );
    consoleError.mockRestore();
  });

  it("handles initial fetch error safely when showError is omitted", async () => {
    const consoleError = vi
      .spyOn(console, "error")
      .mockImplementation(() => {});
    api.mockRejectedValueOnce(new Error("initial fetch failed"));
    render(<UpgradePanel />);

    await waitFor(() =>
      expect(consoleError).toHaveBeenCalledWith(
        new Error("initial fetch failed"),
      ),
    );
    consoleError.mockRestore();
  });

  // The steps an upgrade reports (pkg/upgrade's progress, grain/task-37).
  // An upgrade is minutes of work, and before this the pane said
  // "running" for all of them; what it has to show now is which step it
  // is on, how far through the plan that is, and what that step's own
  // command last printed.

  it("shows the plan, the step it is on, and what that step last printed", async () => {
    api.mockResolvedValue({
      enabled: true,
      phase: "running",
      branch: "main",
      detail: "step 2 of 4: build",
      steps: [
        {
          name: "checkout",
          state: "done",
          detail: "main at 7b137f0 (Merge pull request #802)",
          startedAt: "2026-09-06T10:00:00Z",
          finishedAt: "2026-09-06T10:00:04Z",
        },
        {
          name: "build",
          state: "running",
          detail: "#12 [builder 4/9] RUN go build ./...",
          startedAt: "2026-09-06T10:00:04Z",
        },
        { name: "install", state: "pending" },
        { name: "health check", state: "pending" },
      ],
    });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByText(/Step 2 of 4: build/)).toBeInTheDocument();
    expect(
      screen.getByText("#12 [builder 4/9] RUN go build ./..."),
    ).toBeInTheDocument();
    // The whole plan is on screen from the first poll, the steps still
    // to come included -- pkg/upgrade decides it before it starts.
    expect(screen.getByText("health check")).toBeInTheDocument();
    // A finished step keeps its own duration and what it achieved.
    expect(screen.getByText("4s")).toBeInTheDocument();
    expect(
      screen.getByText("main at 7b137f0 (Merge pull request #802)"),
    ).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute(
      "aria-valuenow",
      "25",
    );
  });

  it("counts the running step up while nothing else changes", async () => {
    // A build that sits on one output line for four minutes is the case
    // this exists for: without a clock, a slow upgrade and a hung one
    // look identical.
    const startedAt = new Date(Date.now() - 5000).toISOString();
    api.mockResolvedValue({
      enabled: true,
      phase: "running",
      branch: "main",
      steps: [{ name: "build", state: "running", startedAt }],
    });

    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      render(<UpgradePanel showError={() => {}} />);
      await act(async () => {});
      expect(screen.getByText(/Step 1 of 1: build \(5s\)/)).toBeInTheDocument();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(4000);
      });
      expect(screen.getByText(/Step 1 of 1: build \(9s\)/)).toBeInTheDocument();
    } finally {
      vi.useRealTimers();
    }
  });

  it("marks the step an upgrade failed on, and says why steps were skipped", async () => {
    api.mockResolvedValue({
      enabled: true,
      phase: "failed",
      branch: "main",
      detail:
        "pull: docker pull ghcr.io/x:main: exit status 1: manifest unknown",
      steps: [
        {
          name: "pull image",
          state: "failed",
          detail: "manifest unknown",
          startedAt: "2026-09-06T10:00:00Z",
          finishedAt: "2026-09-06T10:00:02Z",
        },
        {
          name: "health check",
          state: "skipped",
          detail: "no health check configured",
        },
        { name: "record image", state: "pending" },
      ],
    });
    render(<UpgradePanel showError={() => {}} />);

    const failed = await screen.findByText("pull image");
    expect(failed.closest("li")).toHaveClass("upgrade-step-failed");
    expect(screen.getByText("manifest unknown")).toBeInTheDocument();
    expect(screen.getByText("no health check configured")).toBeInTheDocument();
  });

  it("still shows a status an older daemon wrote, with no steps in it", async () => {
    api.mockResolvedValue({
      enabled: true,
      phase: "ok",
      branch: "main",
      detail: "built and installed; restarting",
    });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByText("ok")).toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  });

  it("reports a failed upgrade's detail", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      branch: "main",
      phase: "failed",
      detail: "checkout: git fetch: exit status 128",
    });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByText("failed")).toBeInTheDocument();
    expect(
      screen.getByText("checkout: git fetch: exit status 128"),
    ).toBeInTheDocument();
  });
});

// grain/task-156: the same button, told to wait. An upgrade ends in a
// restart that discards every run under way, so an operator of a busy
// deployment could either pay that or sit watching the run count --
// this is the third option, and what the pane has to say about it while
// it waits.
describe("UpgradePanel scheduling", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("schedules instead of upgrading when told to wait", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        mode: "image",
        phase: "",
        inFlightRuns: 3,
      })
      .mockResolvedValueOnce({
        enabled: true,
        mode: "image",
        phase: "",
        scheduled: { branch: "main", busyRuns: 3, busyCiRuns: 0 },
      });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    await user.type(await screen.findByLabelText(/Branch/), "main");
    await user.click(
      screen.getByLabelText(/wait until this deployment is idle/i),
    );
    await user.click(screen.getByRole("button", { name: "Schedule upgrade" }));

    // Nothing is being thrown away, so there is nothing to confirm.
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(api).toHaveBeenLastCalledWith("/api/upgrade", {
      method: "POST",
      body: JSON.stringify({ branch: "main", afterTasksFinish: true }),
    });
    expect(await screen.findByText(/waiting to upgrade to/i)).toHaveTextContent(
      /3 runs still to finish/,
    );
  });

  it("says the upgrade is about to start once nothing is left", async () => {
    api.mockResolvedValue({
      enabled: true,
      mode: "image",
      phase: "",
      scheduled: { branch: "main", busyRuns: 0, busyCiRuns: 0 },
    });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByText(/waiting to upgrade to/i)).toHaveTextContent(
      /nothing is running/,
    );
  });

  it("counts grain's own CI among what it is waiting for", async () => {
    api.mockResolvedValue({
      enabled: true,
      mode: "image",
      phase: "",
      scheduled: { branch: "main", busyRuns: 1, busyCiRuns: 2 },
    });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByText(/waiting to upgrade to/i)).toHaveTextContent(
      /1 run and 2 CI runs still to finish/,
    );
  });

  // grain/task-353: an upgrade a task scheduled for itself (the
  // self-upgrade capability) is named after that task. "Asked for by
  // operator" on a restart no operator asked for is the answer this
  // pane exists not to give.
  it("names the task that asked for the upgrade", async () => {
    api.mockResolvedValue({
      enabled: true,
      mode: "image",
      phase: "",
      scheduled: {
        branch: "main",
        byKind: "agent",
        byId: "353",
        busyRuns: 1,
        busyCiRuns: 0,
      },
    });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByText(/waiting to upgrade to/i)).toHaveTextContent(
      /asked for by task 353/,
    );
  });

  it("names the operator who asked for the upgrade", async () => {
    api.mockResolvedValue({
      enabled: true,
      mode: "image",
      phase: "",
      scheduled: {
        branch: "main",
        byKind: "human",
        byId: "alice",
        busyRuns: 1,
        busyCiRuns: 0,
      },
    });
    render(<UpgradePanel showError={() => {}} />);

    expect(await screen.findByText(/waiting to upgrade to/i)).toHaveTextContent(
      /asked for by alice/,
    );
  });

  // An older row, or a deployment with no actor set: one clause fewer,
  // rather than a sentence about nobody.
  it("says nothing about who asked when the deployment did not record it", async () => {
    api.mockResolvedValue({
      enabled: true,
      mode: "image",
      phase: "",
      scheduled: { branch: "main", busyRuns: 1, busyCiRuns: 0 },
    });
    render(<UpgradePanel showError={() => {}} />);

    expect(
      await screen.findByText(/waiting to upgrade to/i),
    ).not.toHaveTextContent(/asked for by/);
  });

  it("cancels a scheduled upgrade", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        mode: "image",
        phase: "",
        scheduled: { branch: "main", busyRuns: 1, busyCiRuns: 0 },
      })
      .mockResolvedValueOnce({
        enabled: true,
        mode: "image",
        phase: "",
        cancelled: true,
      });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    await user.click(await screen.findByRole("button", { name: "Cancel" }));

    expect(api).toHaveBeenLastCalledWith("/api/upgrade/scheduled", {
      method: "DELETE",
    });
    await waitFor(() =>
      expect(
        screen.queryByText(/waiting to upgrade to/i),
      ).not.toBeInTheDocument(),
    );
  });

  it("does not upgrade and informs the user when the selected version is already running", async () => {
    api.mockResolvedValueOnce({
      enabled: true,
      mode: "image",
      phase: "",
      branch: "",
      running: {
        image: "ghcr.io/bwsalmon/grain/grain:v1.7.0",
        release: "v1.7.0",
      },
      inFlightRuns: 2,
    });
    const user = userEvent.setup();
    render(
      <UpgradePanel
        showError={() => {}}
        version={{ release: "v1.7.0", commit: "1234567" }}
      />,
    );

    const input = await screen.findByLabelText(/Branch/);
    await user.type(input, "v1.7.0");
    await user.click(screen.getByRole("button", { name: "Upgrade" }));

    expect(
      await screen.findByText(
        /The version on v1\.7\.0 has not changed, so this deployment was not upgraded\./,
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    expect(api).not.toHaveBeenCalledWith("/api/upgrade", expect.anything());
  });

  it("does not upgrade when the release notes preview shows the selected channel has the same version", async () => {
    api.mockImplementation((path) => {
      if (path.startsWith("/api/upgrade/release-notes")) {
        return Promise.resolve({
          branch: "latest",
          from: "v1.7.0",
          version: "v1.7.0",
          notes: [],
          message:
            "v1.7.0 is not newer than v1.7.0, which this deployment runs",
        });
      }
      return Promise.resolve({
        enabled: true,
        mode: "image",
        phase: "",
        branch: "",
        running: {
          image: "ghcr.io/bwsalmon/grain/grain:v1.7.0",
          release: "v1.7.0",
        },
      });
    });
    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    const input = await screen.findByLabelText(/Branch/);
    await user.type(input, "latest");
    expect(
      await screen.findByText(
        /v1\.7\.0 is not newer than v1\.7\.0/i,
        {},
        { timeout: 3000 },
      ),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Upgrade" }));
    expect(
      await screen.findByText(
        /The version on latest has not changed, so this deployment was not upgraded\./,
      ),
    ).toBeInTheDocument();
    expect(api).not.toHaveBeenCalledWith("/api/upgrade", expect.anything());
  });

  it("informs the user and does not poll for a restart when an upgrade reports the version on the channel has not changed", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const onRestart = vi.fn();
    api
      .mockResolvedValueOnce({
        enabled: true,
        mode: "image",
        channel: "latest",
        phase: "",
        branch: "",
        running: {
          image: "ghcr.io/bwsalmon/grain/grain:latest@sha256:abc",
        },
      })
      .mockResolvedValueOnce({
        enabled: true,
        mode: "image",
        channel: "latest",
        phase: "running",
        branch: "latest",
        detail: "step 1 of 6: pull image",
      })
      .mockResolvedValueOnce({
        enabled: true,
        mode: "image",
        channel: "latest",
        phase: "ok",
        unchanged: true,
        branch: "latest",
        detail:
          "the version on latest has not changed (already running ghcr.io/bwsalmon/grain/grain:latest@sha256:abc)",
      });

    try {
      const user = userEvent.setup({
        advanceTimers: vi.advanceTimersByTime,
      });
      render(
        <UpgradePanel
          showError={() => {}}
          version={{ commit: "1111111" }}
          onRestart={onRestart}
        />,
      );

      expect(await screen.findByLabelText(/Branch/)).toHaveValue("latest");
      await user.click(screen.getByRole("button", { name: "Upgrade" }));

      await act(async () => {
        await vi.advanceTimersByTimeAsync(3000);
      });

      expect(
        screen.getByText(
          /The version on latest has not changed, so this deployment was not upgraded\./,
        ),
      ).toBeInTheDocument();
      expect(
        screen.getByText(
          /already running ghcr\.io\/bwsalmon\/grain\/grain:latest@sha256:abc/,
        ),
      ).toBeInTheDocument();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(5000);
      });
      expect(api).not.toHaveBeenCalledWith(
        expect.stringContaining("/api/config?after-upgrade="),
      );
      expect(onRestart).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });
});

// grain/task-38: what grain does after an upgrade lands, on the same tab
// as the button that starts one. Rendered against the settings the pane
// already holds, and saved through the same PUT /api/settings every
// other tab uses -- so only what changed is sent.
describe("UpgradeCheckSettings", () => {
  afterEach(() => {
    api.mockReset();
  });

  const settings = {
    upgradeCheckMode: "auto",
    upgradeCheckModes: ["off", "approve", "auto"],
    upgradeCheckPausesPipeline: false,
  };

  it("saves each complete choice as soon as it changes", async () => {
    api.mockResolvedValue({ enabled: false });
    const onSaveSettings = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(
      <UpgradePanel
        showError={() => {}}
        settings={settings}
        onSaveSettings={onSaveSettings}
      />,
    );

    await user.click(
      await screen.findByLabelText(/hold the queue until the check is done/i),
    );

    await waitFor(() =>
      expect(onSaveSettings).toHaveBeenCalledWith(
        { upgradeCheckPausesPipeline: true },
        { close: false },
      ),
    );
  });

  // Holding the queue for a check that is never filed would be a
  // setting with nothing behind it.
  it("cannot ask for a hold when no check is filed at all", async () => {
    api.mockResolvedValue({ enabled: false });
    render(
      <UpgradePanel
        showError={() => {}}
        settings={{ ...settings, upgradeCheckMode: "off" }}
        onSaveSettings={vi.fn()}
      />,
    );

    expect(
      await screen.findByLabelText(/hold the queue until the check is done/i),
    ).toBeDisabled();
  });

  // A deployment with no upgrader still stores the choice: it may be
  // configured for upgrading later, and should not have to discover
  // afterwards that this setting existed.
  it("is shown even where upgrading itself is not available", async () => {
    api.mockResolvedValue({ enabled: false });
    render(
      <UpgradePanel
        showError={() => {}}
        settings={settings}
        onSaveSettings={vi.fn()}
      />,
    );

    expect(await screen.findByText(/not available/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/check the upgrade/i)).toBeInTheDocument();
  });
});

describe("PostgresDatabaseUpgrade", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("shows postgres active state when running on postgres", async () => {
    api.mockImplementation((path) => {
      if (path === "/api/upgrade") {
        return Promise.resolve({
          enabled: false,
          database: {
            driver: "postgres",
            dsn: "postgres://user:******@localhost:5432/grain",
            isSqlite: false,
            postgresAvailable: true,
          },
        });
      }
      if (path === "/api/database") {
        return Promise.resolve({
          driver: "postgres",
          dsn: "postgres://user:******@localhost:5432/grain",
          isSqlite: false,
          postgresAvailable: true,
        });
      }
      return Promise.resolve({ enabled: false });
    });

    render(<UpgradePanel showError={() => {}} />);
    expect(
      await screen.findByText(/This deployment is running on/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/PostgreSQL/)).toBeInTheDocument();
  });

  it("allows testing postgres connection and upgrading from sqlite", async () => {
    api.mockImplementation((path) => {
      if (path === "/api/database") {
        return Promise.resolve({
          driver: "sqlite",
          dsn: "/var/lib/grain/data/store",
          isSqlite: true,
          postgresAvailable: true,
        });
      }
      if (path === "/api/database/test") {
        return Promise.resolve({ ok: true, message: "Connection successful" });
      }
      if (path === "/api/database/upgrade") {
        return Promise.resolve({
          ok: true,
          message: "Successfully migrated database to PostgreSQL",
          tablesMigrated: 12,
        });
      }
      return Promise.resolve({ enabled: false });
    });

    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    expect(
      await screen.findByText(/Currently running on/i),
    ).toBeInTheDocument();

    const dsnInput = screen.getByLabelText(/PostgreSQL DSN/i);
    await user.type(dsnInput, "postgres://user:pass@localhost:5432/graindb");

    const testBtn = screen.getByRole("button", { name: /Test Connection/i });
    await user.click(testBtn);

    expect(api).toHaveBeenCalledWith("/api/database/test", {
      method: "POST",
      body: JSON.stringify({
        dsn: "postgres://user:pass@localhost:5432/graindb",
      }),
    });
    expect(
      await screen.findByText(/Connection successful/i),
    ).toBeInTheDocument();

    const upgradeBtn = screen.getByRole("button", {
      name: /Migrate Database to PostgreSQL/i,
    });
    await user.click(upgradeBtn);

    expect(api).toHaveBeenCalledWith("/api/database/upgrade", {
      method: "POST",
      body: JSON.stringify({
        dsn: "postgres://user:pass@localhost:5432/graindb",
        migrateData: true,
      }),
    });
  });

  it("allows migrating to local embedded postgres when no DSN is provided", async () => {
    let dbStatus = {
      driver: "sqlite",
      dsn: "/var/lib/grain/data/store",
      isSqlite: true,
      postgresAvailable: true,
    };
    api.mockImplementation((path) => {
      if (path === "/api/database") {
        return Promise.resolve(dbStatus);
      }
      if (path === "/api/database/test") {
        return Promise.resolve({ ok: true, message: "Connection successful" });
      }
      if (path === "/api/database/upgrade") {
        dbStatus = {
          driver: "postgres",
          dsn: "",
          isSqlite: false,
          postgresAvailable: true,
        };
        return Promise.resolve({
          ok: true,
          message: "Successfully migrated database to PostgreSQL",
          tablesMigrated: 12,
          driver: "postgres",
          dsn: "",
        });
      }
      return Promise.resolve({ enabled: false });
    });

    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);

    expect(
      await screen.findByText(/Currently running on/i),
    ).toBeInTheDocument();

    const upgradeBtn = screen.getByRole("button", {
      name: /Migrate Database to PostgreSQL/i,
    });
    // Click without entering any DSN (uses local embedded postgres)
    await user.click(upgradeBtn);

    expect(api).toHaveBeenCalledWith("/api/database/upgrade", {
      method: "POST",
      body: JSON.stringify({
        dsn: "",
        migrateData: true,
      }),
    });

    expect(
      await screen.findByText(/This deployment is running on/i),
    ).toBeInTheDocument();
  });

  // The route answers a first request with 202 confirmationRequired and
  // writes a single-use token to the daemon log (pkg/ui/
  // database_confirm.go). The form has to take that token and send the
  // same request back with it; it once showed the 202 as a red error
  // with nowhere to type the token (2026-10-02 §6 walkthrough).
  it("asks for the daemon-log confirmation token and resends the request with it", async () => {
    const bodies = [];
    api.mockImplementation((path, opts) => {
      if (path === "/api/database") {
        return Promise.resolve({
          driver: "sqlite",
          isSqlite: true,
          postgresAvailable: false,
        });
      }
      if (path === "/api/database/upgrade") {
        const body = JSON.parse(opts.body);
        bodies.push(body);
        if (!body.confirmationToken) {
          return Promise.resolve({
            confirmationRequired: true,
            message:
              "Nothing has been changed yet. A confirmation token has been written to the daemon log.",
            expiresAt: "2026-10-03T01:16:10Z",
          });
        }
        return Promise.resolve({
          ok: true,
          message: "Successfully migrated SQLite database to PostgreSQL",
          tablesMigrated: 88,
        });
      }
      return Promise.resolve({ enabled: false });
    });

    const user = userEvent.setup();
    render(<UpgradePanel showError={() => {}} />);
    expect(
      await screen.findByText(/Currently running on/i),
    ).toBeInTheDocument();
    const dsn = "postgres://user:pass@db:5432/grain";
    await user.type(screen.getByLabelText(/PostgreSQL DSN/i), dsn);
    expect(
      screen.queryByLabelText(/Confirmation token/i),
    ).not.toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: /Migrate Database to PostgreSQL/i }),
    );
    expect(
      await screen.findByText(/Nothing has been changed yet/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Migration failed/i)).not.toBeInTheDocument();

    // Editing the DSN drops the token: it was issued for the old one.
    await user.type(screen.getByLabelText(/PostgreSQL DSN/i), "x");
    expect(
      screen.queryByLabelText(/Confirmation token/i),
    ).not.toBeInTheDocument();
    await user.type(screen.getByLabelText(/PostgreSQL DSN/i), "{Backspace}");
    await user.click(
      screen.getByRole("button", { name: /Migrate Database to PostgreSQL/i }),
    );

    await user.type(
      await screen.findByLabelText(/Confirmation token/i),
      "  abcdefghijklmnopqrstuvwx ",
    );
    await user.click(
      screen.getByRole("button", { name: /Confirm Migration/i }),
    );

    expect(
      await screen.findByText(/Successfully migrated SQLite database/i),
    ).toBeInTheDocument();
    expect(bodies).toEqual([
      { dsn, migrateData: true },
      { dsn, migrateData: true },
      { dsn, migrateData: true, confirmationToken: "abcdefghijklmnopqrstuvwx" },
    ]);
    expect(
      screen.queryByLabelText(/Confirmation token/i),
    ).not.toBeInTheDocument();
  });

  it("displays error alert when migration fails", async () => {
    api.mockImplementation((path) => {
      if (path === "/api/database") {
        return Promise.resolve({
          driver: "sqlite",
          dsn: "/var/lib/grain/data/store",
          isSqlite: true,
          postgresAvailable: true,
        });
      }
      if (path === "/api/database/upgrade") {
        return Promise.reject(
          new Error(
            "could not look up effective user ID 1000: user does not exist",
          ),
        );
      }
      return Promise.resolve({ enabled: false });
    });

    const user = userEvent.setup();
    render(<UpgradePanel />);

    expect(
      await screen.findByText(/Currently running on/i),
    ).toBeInTheDocument();

    const upgradeBtn = screen.getByRole("button", {
      name: /Migrate Database to PostgreSQL/i,
    });
    await user.click(upgradeBtn);

    expect(
      await screen.findByText(
        /could not look up effective user ID 1000: user does not exist/i,
      ),
    ).toBeInTheDocument();
  });
  // grain/task-543: a track defaulted to a
  // repository hosts could not pull from, and nothing on the pane said
  // which image the button would pull or let an operator pick another.
  describe("choosing the image", () => {
    const GHCR = "registry.example/acme/acme-grain";
    const AR = "mirror.example/acme/grain";
    const imageStatus = {
      enabled: true,
      mode: "image",
      channel: "main",
      running: { image: `${AR}:main@sha256:0123` },
      phase: "ok",
      branch: "main",
      imageRepo: AR,
      defaultImageRepo: GHCR,
      defaultImageRef: `${AR}:main`,
      imageRepos: [GHCR, AR],
    };

    it("resolves the reference an upgrade would pull", () => {
      expect(resolvedImage("", AR, "main")).toBe(`${AR}:main`);
      expect(resolvedImage(GHCR, AR, "grain/task-1")).toBe(
        `${GHCR}:grain..task-1`,
      );
      expect(resolvedImage(`${GHCR}:v2.1.0`, AR, "main")).toBe(
        `${GHCR}:v2.1.0`,
      );
      expect(resolvedImage(`${GHCR}@sha256:abc`, AR, "main")).toBe(
        `${GHCR}@sha256:abc`,
      );
      expect(resolvedImage("", AR, "")).toBe(AR);
    });

    it("shows what will be pulled and posts the repository chosen", async () => {
      api.mockImplementation((url, opts) => {
        if (String(url).includes("/api/upgrade/release-notes")) {
          return Promise.resolve({ notes: [] });
        }
        if (opts?.method === "POST") {
          return Promise.resolve({
            ...imageStatus,
            phase: "running",
            branch: "main",
          });
        }
        return Promise.resolve(imageStatus);
      });
      const user = userEvent.setup();
      render(<UpgradePanel showError={() => {}} />);

      expect(
        await screen.findByText(`${AR}:main`, { selector: "code" }),
      ).toBeInTheDocument();
      await user.click(screen.getByText(`${GHCR} (default)`));
      expect(screen.getByLabelText(/Image \(optional\)/)).toHaveValue(GHCR);
      expect(
        screen.getByText(`${GHCR}:main`, { selector: "code" }),
      ).toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: "Upgrade" }));
      expect(api).toHaveBeenLastCalledWith("/api/upgrade", {
        method: "POST",
        body: JSON.stringify({
          branch: "main",
          image: GHCR,
          afterTasksFinish: false,
        }),
      });
    });

    it("takes an explicit reference as typed", async () => {
      api.mockImplementation((url, opts) => {
        if (String(url).includes("/api/upgrade/release-notes")) {
          return Promise.resolve({ notes: [] });
        }
        if (opts?.method === "POST") {
          return Promise.resolve({ ...imageStatus, phase: "running" });
        }
        return Promise.resolve(imageStatus);
      });
      const user = userEvent.setup();
      render(<UpgradePanel showError={() => {}} />);

      const field = await screen.findByLabelText(/Image \(optional\)/);
      await user.type(field, `${GHCR}:candidate`);
      expect(
        screen.getByText(`${GHCR}:candidate`, { selector: "code" }),
      ).toBeInTheDocument();
      await user.click(screen.getByRole("button", { name: "Upgrade" }));
      expect(api).toHaveBeenLastCalledWith("/api/upgrade", {
        method: "POST",
        body: JSON.stringify({
          branch: "main",
          image: `${GHCR}:candidate`,
          afterTasksFinish: false,
        }),
      });
    });

    it("offers no image field on a deployment that does not pull images", async () => {
      api.mockResolvedValue({ enabled: true, mode: "source", phase: "" });
      render(<UpgradePanel showError={() => {}} />);

      await screen.findByLabelText(/Branch/);
      expect(
        screen.queryByLabelText(/Image \(optional\)/),
      ).not.toBeInTheDocument();
    });
  });
});

// The daily automatic upgrade: a channel, or Off, and a time of day on
// the deployment's own clock, each saved through PUT /api/settings.
describe("AutoUpgradeSettings", () => {
  const settings = {
    autoUpgradeChannel: "",
    autoUpgradeTime: "03:00",
    timeZone: "America/New_York",
  };

  it("is off, with the time disabled, until a channel is chosen", () => {
    render(<AutoUpgradeSettings settings={settings} onSave={vi.fn()} />);

    expect(
      screen.getByRole("combobox", { name: /channel/i }),
    ).toHaveTextContent(/off \(manual only\)/i);
    expect(screen.getByLabelText(/time of day/i)).toBeDisabled();
    expect(screen.getByText(/America\/New_York/)).toBeInTheDocument();
  });

  it("saves a channel with its time, and turning it off saves only that", async () => {
    const onSave = vi.fn();
    const user = userEvent.setup();
    render(<AutoUpgradeSettings settings={settings} onSave={onSave} />);

    await user.click(screen.getByRole("combobox", { name: /channel/i }));
    for (const name of ["latest", "candidate", "main"]) {
      expect(screen.getByRole("option", { name })).toBeInTheDocument();
    }
    await user.click(screen.getByRole("option", { name: "candidate" }));
    expect(onSave).toHaveBeenLastCalledWith({
      autoUpgradeChannel: "candidate",
      autoUpgradeTime: "03:00",
    });
    expect(screen.getByLabelText(/time of day/i)).toBeEnabled();

    await user.click(screen.getByRole("combobox", { name: /channel/i }));
    await user.click(screen.getByRole("option", { name: /off/i }));
    expect(onSave).toHaveBeenLastCalledWith({ autoUpgradeChannel: "" });
  });

  it("saves a new time of day once the field is left", async () => {
    const onSave = vi.fn();
    const user = userEvent.setup();
    render(
      <AutoUpgradeSettings
        settings={{ ...settings, autoUpgradeChannel: "latest" }}
        onSave={onSave}
      />,
    );

    const time = screen.getByLabelText(/time of day/i);
    await user.clear(time);
    await user.type(time, "04:30");
    expect(onSave).not.toHaveBeenCalled();
    await user.tab();
    expect(onSave).toHaveBeenCalledWith({ autoUpgradeTime: "04:30" });
  });

  it("offers a channel set some other way", async () => {
    const user = userEvent.setup();
    render(
      <AutoUpgradeSettings
        settings={{ ...settings, autoUpgradeChannel: "release-2026" }}
        onSave={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("combobox", { name: /channel/i }));
    expect(
      screen.getByRole("option", { name: "release-2026" }),
    ).toBeInTheDocument();
  });
});
