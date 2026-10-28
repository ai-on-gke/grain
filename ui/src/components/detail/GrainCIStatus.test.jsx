import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import DetailOverlay from "../DetailOverlay.jsx";
import api from "../../api.js";
import { resetDetailMocks, baseTask, config } from "./testFixtures.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../../attachments.js", () => ({ default: vi.fn() }));

describe("DetailOverlay grain CI status", () => {
  afterEach(resetDetailMocks);

  // grain/task-246: grain's own CI reports on a task's branch, and the
  // pane shows the verdict itself -- copied onto the task, and so still
  // true long after the run it came from has been pruned. A task nothing
  // has run CI against says nothing at all, which is almost every task.
  // Rendered here without onOpenCIRun, which is a caller with nowhere to
  // send a reader: the verdict stands on its own.
  it("shows what grain CI said about this task's branch", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          ci: {
            runId: "3f2a1c",
            conclusion: "failure",
            detail: "failure: checks/test",
          },
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByText(/grain CI: failure/)).toBeInTheDocument();
    expect(screen.getByText(/checks\/test/)).toBeInTheDocument();
    // No pull request on this fixture, so there is nothing for the
    // verdict to hold -- and grain CI does not even start until one is
    // open (orchestrator.reconcileCI). The pane says so rather than
    // promising a consequence this task does not have.
    expect(screen.getByText(/Nothing is held by it yet/)).toBeInTheDocument();
  });

  // grain/task-296: the red that is not this branch's own. This alert is
  // where somebody decides to spend a repair run, and four of them were
  // once spent on a cause none of those branches owned while the change
  // that fixed it sat finished and unmerged.
  it("says when this red is a fault other branches share, and names the fix", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          branch: "grain/task-1",
          pullRequest: "acme/widgets#42",
          ci: {
            runId: "3f2a1c",
            conclusion: "failure",
            detail: "failure: image",
            faults: [
              {
                repo: "acme/widgets",
                jobId: "image",
                jobName: "image",
                step: "build the image",
                digest: "d1",
                refs: ["grain/task-1", "grain/task-7", "grain/task-9"],
                runs: [
                  { runId: "3f2a1c", ref: "grain/task-1" },
                  { runId: "other-1", ref: "grain/task-7" },
                  { runId: "other-2", ref: "grain/task-9" },
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
          },
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(
      screen.getByText(/image is failing the same way on 2 other branches/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Repairing this branch will not fix it/),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "acme/widgets#26" }),
    ).toHaveAttribute("href", "https://github.com/acme/widgets/pull/26");
    expect(screen.getByTestId("ci-fault-source-grain")).toHaveTextContent(
      "[grain CI]",
    );
  });

  it("shows a GitHub CI shared fault on a task even without a grain CI run", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          branch: "grain/task-10",
          pullRequest: "acme/widgets#55",
          ciFaults: [
            {
              source: "github",
              repo: "acme/widgets",
              jobId: "CI / test",
              jobName: "CI / test",
              step: "Run go test ./...",
              digest: "gh-d1",
              refs: ["grain/task-10", "grain/task-11", "grain/task-12"],
              runs: [
                { runId: "9001", ref: "grain/task-10" },
                { runId: "9002", ref: "grain/task-11" },
                { runId: "9003", ref: "grain/task-12" },
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
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByTestId("ci-fault-source-github")).toHaveTextContent(
      "[GitHub CI]",
    );
    expect(
      screen.getByText(
        /CI \/ test is failing the same way on 2 other branches/,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "acme/widgets#99" }),
    ).toHaveAttribute("href", "https://github.com/acme/widgets/pull/99");
  });

  // And nothing at all on the ordinary red build, which is every red
  // build grain has drawn no such conclusion about.
  it("says nothing about other branches when grain found no shared fault", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          ci: { runId: "3f2a1c", conclusion: "failure", faults: [] },
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByText(/grain CI: failure/)).toBeInTheDocument();
    expect(screen.queryByText(/failing the same way/)).not.toBeInTheDocument();
  });

  // grain/task-302 made the verdict hold the merge, and grain/task-356
  // made the pane say so: a person reading a pull request that is not
  // merging had the verdict here and the pull request's state beside it,
  // with nothing saying the first was why the second.
  it("says a red grain CI run is what is holding the pull request", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          pullRequest: "acme/widgets#42",
          ci: {
            runId: "3f2a1c",
            commit: "abcdef0123456789",
            conclusion: "failure",
            detail: "failure: checks/test",
          },
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByText(/grain CI: failure/)).toBeInTheDocument();
    expect(
      screen.getByText(/keeps this pull request out of the merge queue/),
    ).toBeInTheDocument();
    // The commit it is a verdict about, which is what decides whether it
    // holds anything at all (orchestrator.grainCIVerdictOf).
    expect(screen.getByText(/abcdef012345/)).toBeInTheDocument();
  });

  // A run still going holds the pull request pending, which is the half
  // of this a reader is most likely to be staring at: every GitHub check
  // green, and the merge still waiting.
  it("says a grain CI run in flight is holding the pull request pending", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          pullRequest: "acme/widgets#42",
          ci: { runId: "3f2a1c", commit: "abcdef0123456789" },
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByText(/grain CI is running/)).toBeInTheDocument();
    expect(
      screen.getByText(/held pending until this reports/),
    ).toBeInTheDocument();
  });

  // Only a failure holds anything -- a cancelled run is this
  // deployment's own supersede or a daemon shutting down, and reading it
  // as a block would send somebody looking for a broken branch that is
  // not broken.
  it("says a cancelled grain CI run holds nothing", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          pullRequest: "acme/widgets#42",
          ci: { runId: "3f2a1c", conclusion: "cancelled" },
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByText(/That holds nothing/)).toBeInTheDocument();
    expect(
      screen.queryByText(/keeps this pull request out of the merge queue/),
    ).not.toBeInTheDocument();
  });

  // grain/task-361: and the verdict that is not about the branch at all.
  // A run this deployment could not do holds the merge the way an
  // unfinished check does, so the pane has to say both halves -- that
  // nothing here is the branch's fault, and that it is waiting anyway.
  // It is the one conclusion other than failure with a consequence, so
  // "That holds nothing" would be wrong about it in both directions.
  it("says a run grain could not do is about the deployment, not the branch", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          pullRequest: "acme/widgets#42",
          ci: {
            runId: "3f2a1c",
            commit: "abcdef0123456789",
            conclusion: "errored",
            detail: "grain CI could not run 1 job(s) (tests/go)",
          },
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByText(/grain CI: errored/)).toBeInTheDocument();
    expect(
      screen.getByText(/nothing here is a verdict on this branch/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/as it does for a check that has not finished/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/That holds nothing/)).not.toBeInTheDocument();
    expect(
      screen.queryByText(/keeps this pull request out of the merge queue/),
    ).not.toBeInTheDocument();
  });

  // The same verdict on a task with no pull request open: there is
  // nothing to hold yet, and the pane says that without dropping the
  // half that matters -- that it is the deployment, not the branch.
  it("says an errored grain CI run holds nothing before a pull request is open", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          ci: { runId: "3f2a1c", conclusion: "errored" },
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(
      screen.getByText(/nothing here is a verdict on this branch/),
    ).toBeInTheDocument();
    expect(screen.getByText(/Nothing is held by it yet/)).toBeInTheDocument();
  });

  // grain/task-297: and from the verdict to the run behind it, which is
  // where the step output that says *why* a branch is red actually
  // lives.
  it("offers the run behind a grain CI verdict to a caller that can open one", async () => {
    const onOpenCIRun = vi.fn();
    render(
      <DetailOverlay
        task={{ ...baseTask, ci: { runId: "3f2a1c", conclusion: "failure" } }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        onOpenCIRun={onOpenCIRun}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Show the run" }));
    expect(onOpenCIRun).toHaveBeenCalledWith("3f2a1c");
  });

  // A run grain never recorded an id for -- one this deployment has
  // forgotten and re-synced the verdict of -- has nothing to point at.
  it("offers no run when the verdict carries no id", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, ci: { conclusion: "success" } }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        onOpenCIRun={vi.fn()}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(
      screen.queryByRole("button", { name: "Show the run" }),
    ).not.toBeInTheDocument();
  });

  it("says nothing about grain CI on a task it has not run against", () => {
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.queryByText(/grain CI/)).not.toBeInTheDocument();
  });
  // grain/task-480: an errored verdict grain had stopped retrying could
  // only be force-merged, since nothing here asked for another run. The
  // button posts the task's id, so the run it starts is recorded as this
  // task's verdict, and the task is read back through act.
  it("re-runs grain CI for the task on a verdict that did not pass", async () => {
    const user = userEvent.setup();
    const act = vi.fn(async (mutate) => {
      await mutate();
      return true;
    });
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          id: "480",
          state: "completed",
          repo: "acme/widgets",
          pullRequest: "acme/widgets#42",
          ci: {
            runId: "r9",
            commit: "cf3c8a34bcba0000",
            conclusion: "errored",
            detail: "grain CI could not run 1 job(s) (tests/smoke)",
          },
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Re-run CI" }));
    expect(api).toHaveBeenCalledWith("/api/ci/runs", {
      method: "POST",
      body: JSON.stringify({ taskId: "480" }),
    });
    expect(act).toHaveBeenCalledWith(expect.any(Function), "480");

    await user.click(
      screen.getByRole("button", { name: "Re-run failed jobs" }),
    );
    expect(api).toHaveBeenCalledWith("/api/ci/runs", {
      method: "POST",
      body: JSON.stringify({
        taskId: "480",
        recheck: true,
        recheckFailed: true,
      }),
    });
  });

  it("offers no re-run on a green or running verdict", () => {
    for (const ci of [
      { runId: "r1", conclusion: "success" },
      { runId: "r2", conclusion: "" },
    ]) {
      const { unmount } = render(
        <DetailOverlay
          task={{
            ...baseTask,
            state: "completed",
            repo: "acme/widgets",
            ci,
          }}
          tasks={[]}
          config={config}
          onClose={() => {}}
          onOpenTask={() => {}}
          act={vi.fn()}
        />,
      );
      expect(
        screen.queryByRole("button", { name: "Re-run CI" }),
      ).not.toBeInTheDocument();
      unmount();
    }
  });
});
