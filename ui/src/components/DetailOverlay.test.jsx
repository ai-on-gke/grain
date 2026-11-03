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
import DetailOverlay from "./DetailOverlay.jsx";
import api from "../api.js";
import { resetDetailMocks, baseTask, config } from "./detail/testFixtures.js";

vi.mock("../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../attachments.js", () => ({ default: vi.fn() }));

describe("DetailOverlay", () => {
  afterEach(resetDetailMocks);

  it("renders the task id, title, description and state badge", () => {
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    const heading = screen.getByRole("heading", { name: /Fix the login bug/ });
    expect(heading).toHaveTextContent("12 Fix the login bug");
    // grain/task-85: the number is a bubble carrying the task's own
    // state colour, not a bare figure run into the front of the title.
    expect(heading.querySelector(".task-id-bubble")).toHaveClass(
      "task-id-queued",
    );
    expect(screen.getByText("Login fails on retry")).toBeInTheDocument();
    expect(screen.getByText("Waiting")).toBeInTheDocument();
  });

  // grain/task-94: a task fills the whole pane beside the sidebar, not a
  // box floating in the middle of it -- an agent's own answer runs long,
  // and reading one through a 900px porthole was the complaint.
  it("opens as a full pane rather than a centered dialog", () => {
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    expect(document.querySelector(".MuiDialog-paper")).toHaveClass(
      "MuiDialog-paperFullScreen",
    );
    expect(
      document.querySelector(".overlay-pane .detail-layout"),
    ).toBeInTheDocument();
  });

  // A task pane is a modal dialog, and a screen reader landing in one
  // has to hear which task it is in -- MUI's Dialog took no name from
  // the heading drawn inside it (2026-10-02 intake walkthrough). The
  // name holds while editing, when the title heading is gone.
  it("names its dialog after the task, also while editing", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    expect(
      screen.getByRole("dialog", { name: "Task 12: Fix the login bug" }),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Edit" }));
    expect(
      screen.getByRole("dialog", { name: "Task 12: Fix the login bug" }),
    ).toBeInTheDocument();
  });

  // The page somebody opens *because* the list said "running" must not
  // answer with less than the list did: the same status line, under the
  // badge that only says it is running (grain/task-240).
  it("shows what a running task's agent says it is doing", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "running",
          activity: "running the test suite",
          activityAt: new Date().toISOString(),
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("running the test suite")).toBeInTheDocument();
  });

  // ...and it must not answer with *more* than the list did either. The
  // threshold past which a phrase is dated lives in state.js's
  // activityAge and is the same in both, so a line this page calls stale
  // is one the row calls stale too (grain/task-204).
  it("dates a status only once it has stood as long as the row wants", () => {
    const ageOf = (msAgo) => {
      const { unmount } = render(
        <DetailOverlay
          task={{
            ...baseTask,
            state: "running",
            activity: "waiting for a reply",
            activityAt: new Date(Date.now() - msAgo).toISOString(),
          }}
          tasks={[]}
          config={config}
          onClose={() => {}}
          onOpenTask={() => {}}
          act={vi.fn()}
        />,
      );
      const age = document.querySelector(".task-activity-age")?.textContent;
      unmount();
      return age;
    };
    expect(ageOf(2 * 60 * 1000)).toBeUndefined();
    expect(ageOf(40 * 60 * 1000)).toContain("40m ago");
  });

  // The same attribution the list makes (grain/task-295): a phrase
  // written while the run's sandbox was still being built is grain's,
  // not the agent's, and the page says so rather than passing it off.
  it("marks a setup status as grain's own", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "running",
          activity: "building a sandbox",
          activityBySetup: true,
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("building a sandbox")).toBeInTheDocument();
    expect(document.querySelector(".task-activity-by")).toHaveTextContent(
      "grain",
    );
  });

  it("shows the reason a queued task is waiting in details and on the state badge hover", () => {
    const waitReason =
      "waiting: all connections serving claude-sonnet-4-6 are busy (1 busy: 4/4 in use)";
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "queued",
          activity: waitReason,
          activityBySetup: true,
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText(waitReason)).toBeInTheDocument();
    expect(document.querySelector(".detail-state .badge")).toHaveAttribute(
      "title",
      `Waiting: ${waitReason.replace(/^waiting: /, "")}`,
    );
  });

  // A task whose run is over but whose change has not landed is the one
  // this page used to answer least about: the row said "Merging" and so
  // did this, for hours, with the reason nowhere but the daemon's log.
  it("spells out what the merge queue is waiting for", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "completed",
          mergeQueueState: "merge_failed",
          mergeQueueNote:
            "GitHub has refused this merge 12 times: At least 1 approving review is required.",
          mergeQueueStateAt: new Date(
            Date.now() - 3 * 3600 * 1000,
          ).toISOString(),
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("Merge failed")).toBeInTheDocument();
    expect(document.querySelector(".detail-activity-note")).toHaveTextContent(
      "At least 1 approving review is required.",
    );
    expect(document.querySelector(".task-activity-age")).toHaveTextContent(
      "3h ago",
    );
  });

  // And the one thing the sentence alone could not say: that grain has
  // stopped believing the wait (Task.MergeQueueStalledAt, orchestrator's
  // liveness.go). The page somebody opens from a row carries the row's
  // own chip, so the two do not disagree about what is happening.
  it("puts the stalled chip on a merge-queue wait grain has flagged", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "completed",
          mergeQueueState: "waiting_ci",
          mergeQueueNote:
            "grain CI has not reported on 4b2c1 -- and this has stood here for more than 2h15m.",
          mergeQueueStateAt: new Date(
            Date.now() - 4 * 3600 * 1000,
          ).toISOString(),
          mergeQueueStalledAt: new Date(
            Date.now() - 90 * 60 * 1000,
          ).toISOString(),
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    // The phrase stays: it is still nominally waiting for CI, and the
    // note beside it is the account of why that has stopped meaning
    // anything.
    expect(screen.getByText("Waiting for CI")).toBeInTheDocument();
    expect(screen.getByText("Stalled")).toBeInTheDocument();
  });

  it("shows no chip for a merge-queue wait that is going normally", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "completed",
          mergeQueueState: "waiting_ci",
          mergeQueueStateAt: new Date(Date.now() - 60 * 1000).toISOString(),
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.queryByText("Stalled")).not.toBeInTheDocument();
  });

  it("shows no status line for a task that is not running", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "completed",
          activity: "running the test suite",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(document.querySelector(".detail-activity")).not.toBeInTheDocument();
  });

  it("shows a placeholder when there is no description", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, description: "" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("(no description)")).toBeInTheDocument();
  });

  it("links the pull request when one is attached", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, pullRequest: "acme/widgets#42" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    const link = screen.getByRole("link", { name: "acme/widgets#42" });
    expect(link).toHaveAttribute(
      "href",
      "https://github.com/acme/widgets/pull/42",
    );
  });

  // The refs GitHub has for the repo, which is what says whether the
  // task's branch has been pushed yet.
  const answerBranchRefs = (names) =>
    api.mockImplementation((path) =>
      Promise.resolve(path.endsWith("/github-branches") ? names : null),
    );

  it("links the internal PR until a GitHub pull request is attached", async () => {
    answerBranchRefs(["main", "grain/task-12"]);
    const { rerender } = render(
      <DetailOverlay
        task={{
          ...baseTask,
          repo: "acme/widgets",
          branch: "grain/task-12",
          base: "release/v1",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      await screen.findByRole("link", { name: "Internal PR" }),
    ).toHaveAttribute(
      "href",
      "/repos/acme/widgets/compare/release/v1...grain/task-12",
    );

    rerender(
      <DetailOverlay
        task={{
          ...baseTask,
          repo: "acme/widgets",
          branch: "grain/task-12",
          pullRequest: "acme/widgets#42",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole("link", { name: "Internal PR" }),
    ).not.toBeInTheDocument();
  });

  // grain/task-89: a task carries its branch name from the moment it is
  // filed, so the name is no evidence that any run of it has pushed --
  // and grain's comparison of a branch nobody has created has nothing to
  // show.
  it("offers no internal PR until the branch is on the remote", async () => {
    answerBranchRefs(["main"]);
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "acme/widgets", branch: "grain/task-12" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/repos/acme/widgets/github-branches",
      ),
    );
    expect(
      screen.queryByRole("link", { name: "Internal PR" }),
    ).not.toBeInTheDocument();
  });

  // The comparison is read through the same credential the ref listing
  // is, so a repo grain cannot read the branches of is one it cannot
  // draw a diff for either: no link, rather than a link to an error.
  it("offers no internal PR when the repo's branches cannot be read", async () => {
    api.mockImplementation((path) =>
      path.endsWith("/github-branches")
        ? Promise.reject(new Error("no credential"))
        : Promise.resolve(null),
    );
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "acme/widgets", branch: "grain/task-12" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/repos/acme/widgets/github-branches",
      ),
    );
    expect(
      screen.queryByRole("link", { name: "Internal PR" }),
    ).not.toBeInTheDocument();
  });

  // A per-task agent framework override gets the same treatment: named
  // only when this task overrides the deployment's own, since most do
  // not and a row saying so on every task is noise.
  it("shows the agent framework only when the task overrides it", () => {
    const { rerender } = render(
      <DetailOverlay
        task={{ ...baseTask, agentFramework: "claude" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("Agent framework")).toBeInTheDocument();
    expect(screen.getByText("Claude")).toBeInTheDocument();

    rerender(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.queryByText("Agent framework")).not.toBeInTheDocument();
  });

  // grain/task-110: which model a task runs on, and which connection it
  // is pinned to, as two rows -- they are two choices.
  it("names the task's model and its pinned connection separately", () => {
    const pooled = {
      ...config,
      agentBackends: [
        {
          id: "codex-day",
          name: "Day account",
          framework: "codex",
          models: ["gpt-5.1"],
        },
        {
          id: "claude-deep",
          name: "Deep account",
          framework: "claude",
          models: ["claude-opus-5"],
          disabled: true,
        },
      ],
    };
    const { rerender } = render(
      <DetailOverlay
        task={{
          ...baseTask,
          model: "claude-opus-5",
          agentBackendId: "claude-deep",
        }}
        tasks={[]}
        config={pooled}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("claude-opus-5")).toBeInTheDocument();
    expect(screen.getByText("Connection")).toBeInTheDocument();
    expect(
      screen.getByText("Deep account · Claude · disabled, pin only"),
    ).toBeInTheDocument();

    // No model, no team default, no pin: the rotation, and no
    // connection row.
    rerender(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={pooled}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.getByText("Rotation · provider default (unset)"),
    ).toBeInTheDocument();
    expect(screen.queryByText("Connection")).not.toBeInTheDocument();
  });

  // grain/task-1427: a task that named no model of its own runs on its
  // team's default, and says so rather than "Rotation".
  it("names the deployment's default model for a task that named none", () => {
    const teamed = {
      ...config,
      agentBackends: [
        {
          id: "codex-day",
          name: "Day account",
          framework: "codex",
          discovered: { models: ["gpt-5.1-codex", "gpt-5.1-mini"] },
        },
      ],
      deployment: { id: "default", defaultModel: "gemini-3.1-pro" },
    };
    const { rerender } = render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={teamed}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.getByText("deployment default (gemini-3.1-pro)"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/^Rotation/)).not.toBeInTheDocument();

    // A pin is its own row, and a pinned task that named no model runs
    // the connection's provider default rather than the team's default --
    // labelled as unset, since nobody chose it.
    rerender(
      <DetailOverlay
        task={{ ...baseTask, agentBackendId: "codex-day" }}
        tasks={[]}
        config={teamed}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("Day account · Codex")).toBeInTheDocument();
    expect(
      screen.getByText("gpt-5.1-codex (provider default (unset))"),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("deployment default (gemini-3.1-pro)"),
    ).not.toBeInTheDocument();

    // The task's own model wins over the team's default.
    rerender(
      <DetailOverlay
        task={{ ...baseTask, model: "claude-opus-5" }}
        tasks={[]}
        config={teamed}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("claude-opus-5")).toBeInTheDocument();
    expect(
      screen.queryByText("deployment default (gemini-3.1-pro)"),
    ).not.toBeInTheDocument();
  });

  // grain/task-1868: a model a template or a schedule chose names that
  // declaration, the way the team default names the team.
  it("names the declaration that chose a task's model", () => {
    const { rerender } = render(
      <DetailOverlay
        task={{ ...baseTask, model: "model-b", modelSource: "template" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("model-b (from the template)")).toBeInTheDocument();

    rerender(
      <DetailOverlay
        task={{ ...baseTask, model: "model-b", modelSource: "schedule" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("model-b (from the schedule)")).toBeInTheDocument();
  });

  // grain/task-114: a per-task prompt extension override, shown the same
  // way and for the same reason -- and shown in full, since what it says
  // is the whole of what makes this task's runs different.
  it("shows the prompt extension only when the task overrides it", () => {
    const { rerender } = render(
      <DetailOverlay
        task={{ ...baseTask, promptExtension: "Ignore the house rules." }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("Prompt extension")).toBeInTheDocument();
    expect(screen.getByText("Ignore the house rules.")).toBeInTheDocument();

    rerender(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.queryByText("Prompt extension")).not.toBeInTheDocument();
  });

  // grain/task-284: the review attached to a task, named by the
  // template it comes from -- and saying which task is carrying it out
  // once one has been filed, since "this will be reviewed" and "task 91
  // is reviewing it" are different facts to whoever is watching it.
  it("names the attached review, and the task filed for it once there is one", () => {
    const { rerender } = render(
      <DetailOverlay
        task={{
          ...baseTask,
          reviewTemplateId: "7",
          reviewTemplateName: "Security pass",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("Review")).toBeInTheDocument();
    expect(screen.getByText("Security pass")).toBeInTheDocument();

    rerender(
      <DetailOverlay
        task={{
          ...baseTask,
          reviewTemplateId: "7",
          reviewTemplateName: "Security pass",
          reviewTask: "91",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("Security pass (task 91)")).toBeInTheDocument();

    rerender(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.queryByText("Review")).not.toBeInTheDocument();
  });

  // grain/task-320: that row is the one value in this column that names
  // a template rather than describing the task, so it carries the
  // templates figure (ItemGlyph.jsx, docs/brand.md) -- "this came from a
  // template", said by the same lattice the Templates page uses.
  it("marks the attached review's template with the templates glyph", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          reviewTemplateId: "7",
          reviewTemplateName: "Security pass",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    const value = screen.getByText("Security pass").closest(".declared-value");
    expect(
      value.querySelector('svg[data-glyph="templates"]'),
    ).toBeInTheDocument();
    // Decorative -- the name is right there, and a screen reader saying
    // "templates" as well would only say it twice.
    expect(value.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
  });

  // bwsalmon/agents#534: a per-task sandbox shape override.
  it("shows a sandbox shape override when set, and hides it when not", () => {
    const { rerender } = render(
      <DetailOverlay
        task={{
          ...baseTask,
          sandboxCpus: 4,
          sandboxMemoryMb: 8192,
          sandboxDiskGb: 40,
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("Sandbox vCPUs")).toBeInTheDocument();
    expect(screen.getByText("4")).toBeInTheDocument();
    expect(screen.getByText("Sandbox memory (MiB)")).toBeInTheDocument();
    expect(screen.getByText("8192")).toBeInTheDocument();
    expect(screen.getByText("Sandbox disk (GiB)")).toBeInTheDocument();
    expect(screen.getByText("40")).toBeInTheDocument();

    rerender(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.queryByText("Sandbox vCPUs")).not.toBeInTheDocument();
    expect(screen.queryByText("Sandbox memory (MiB)")).not.toBeInTheDocument();
    expect(screen.queryByText("Sandbox disk (GiB)")).not.toBeInTheDocument();
  });

  // grain/task-26: the same override on a deployment whose sandboxes
  // have no shape of their own is not merely unused -- it fails every
  // attempt at sandbox setup -- and the task can arrive here with one
  // however the form now behaves.
  it("warns that a shape override fails the run where sandboxes have no shape", () => {
    const { rerender } = render(
      <DetailOverlay
        task={{ ...baseTask, sandboxCpus: 4 }}
        tasks={[]}
        config={{ ...config, sandboxShapeIgnored: true }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText(/fails at sandbox setup/)).toBeVisible();

    // Nothing to warn about on a task that declares no shape, on the
    // same deployment: it runs like any other.
    rerender(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={{ ...config, sandboxShapeIgnored: true }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.queryByText(/fails at sandbox setup/)).toBeNull();

    // Nor on a deployment whose sandboxes take the shape they are given.
    rerender(
      <DetailOverlay
        task={{ ...baseTask, sandboxCpus: 4 }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.queryByText(/fails at sandbox setup/)).toBeNull();
  });

  // grain/task-41: the warning above used to end "file the task again
  // without one", which cost a task its comments and links to unset
  // three numbers the API had always been willing to clear.
  it("clears a refused sandbox shape override in one PATCH", async () => {
    const act = vi.fn((mutate) => mutate());
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          sandboxCpus: 4,
          sandboxMemoryMb: 8192,
          sandboxDiskGb: 40,
        }}
        tasks={[]}
        config={{ ...config, sandboxShapeIgnored: true }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(
      screen.getByRole("button", { name: "Clear sandbox shape" }),
    );

    // All three at once: ui.Client.UpdateTask refuses only the shape a
    // request states, so three zeroes are accepted on exactly this
    // deployment -- and a task left with two of the three still set
    // would still fail at sandbox setup.
    expect(api).toHaveBeenCalledWith("/api/tasks/12", {
      method: "PATCH",
      body: JSON.stringify({
        sandboxCpus: 0,
        sandboxMemoryMb: 0,
        sandboxDiskGb: 0,
      }),
    });
    expect(act.mock.calls[0][1]).toBe("12");
  });

  // The button belongs to the warning, not to the rows: a shape this
  // deployment can honour is a working override, and clearing it is not
  // a fix for anything.
  it("offers no clear button where the sandbox shape is honoured", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, sandboxCpus: 4 }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Clear sandbox shape" }),
    ).toBeNull();
  });

  // bwsalmon/agents#539: an interactive task's Timeline reads as a chat.
  // It carries no "Mode" row: sessions live in the Sessions pane, and the
  // chat itself says what the task is.
  it("renders an interactive task's Timeline as a Chat, with no Mode row", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, state: "running", interactive: true }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.queryByText("Mode")).toBeNull();
    expect(
      document.querySelector(".grain-mark-interactive"),
    ).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Chat" })).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Message...")).toBeInTheDocument();
  });

  it("labels an ordinary task's Timeline as Timeline, not Chat, and shows no Mode row", () => {
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.queryByText("Mode")).not.toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: "Timeline" }),
    ).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Reply...")).toBeInTheDocument();
  });

  it("shows the merge-blocked chip once the merge queue has given up", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "completed",
          pullRequest: "acme/widgets#42",
          autoMerge: true,
          mergeQueueBlockedAt: "2026-08-01T00:00:00Z",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("Merge blocked")).toBeInTheDocument();
  });

  // Waiting on a Submit click is a state now, so the badge itself says
  // it and no chip is added beside it (state.js's completionPhase).
  it("says awaiting submit on the state badge, with no chip repeating it", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "awaiting_submit",
          pullRequest: "acme/widgets#42",
          autoMerge: false,
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getAllByText("Asked")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Submit" })).toBeInTheDocument();
  });

  it("opens the originating task when generatedFrom is clicked", async () => {
    const onOpenTask = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, generatedFrom: "9" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={onOpenTask}
        act={vi.fn()}
      />,
    );

    const toggle = screen.getByRole("button", { name: /Provenance/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("link", { name: "9" })).not.toBeInTheDocument();

    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    await user.click(screen.getByText("9"));

    expect(onOpenTask).toHaveBeenCalledWith("9");
  });

  // docs/test-runs/2026-10-05-task-intake-inbox-lifecycle.md, bug 2: the
  // header's links to other tasks had an href of "#", so "open in new
  // tab" or "copy link" on one reached the pane already open.
  it("gives a link to another task that task's own address", async () => {
    const onOpenTask = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, generatedFrom: "9", origin: "task:12" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={onOpenTask}
        act={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: /Provenance/ }));

    expect(screen.getByRole("link", { name: "9" })).toHaveAttribute(
      "href",
      "/tasks/9",
    );
    const origin = screen.getByRole("link", { name: "12" });
    expect(origin).toHaveAttribute("href", "/tasks/12");
    // A modified click is the browser's to handle, in a new tab.
    await user.keyboard("{Control>}");
    await user.click(origin);
    await user.keyboard("{/Control}");
    expect(onOpenTask).not.toHaveBeenCalled();
    await user.click(origin);
    expect(onOpenTask).toHaveBeenCalledWith("12");
  });

  // docs/test-runs/2026-10-01-security-hold-escalated-proposals.md,
  // findings 3 and 4: a held change looked like any other awaiting-submit
  // task, and the only way to its review was a number in a comment.
  it("says a change is on a security hold and links its review", async () => {
    const onOpenTask = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "awaiting_submit",
          pullRequest: "acme/widgets#7",
          securityHold: "trust-boundary/v3",
          securityReviewTask: "91",
          comments: [
            {
              author: "review",
              authorKind: "automation",
              body: "acme/widgets#7 touches grain's trust boundary.\n\nFiled task 91 to review it. Not `task 5`.",
            },
          ],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={onOpenTask}
        act={vi.fn()}
      />,
    );

    // The chip beside the badge, and the notice beside Submit.
    expect(screen.getAllByText("Security hold").length).toBeGreaterThan(0);
    const notice = screen.getByTestId("security-hold");
    expect(notice).toHaveTextContent("Security hold (trust-boundary/v3)");
    expect(screen.getByRole("button", { name: "Submit" })).toBeInTheDocument();

    await user.click(
      within(notice).getByRole("link", { name: "Security review: task 91" }),
    );
    expect(onOpenTask).toHaveBeenLastCalledWith("91");

    // grain's own comment names the review in plain words; that is a
    // link now too, and code is left as it was written.
    onOpenTask.mockClear();
    await user.click(screen.getByRole("link", { name: "task 91" }));
    expect(onOpenTask).toHaveBeenCalledWith("91");
    expect(
      screen.queryByRole("link", { name: "task 5" }),
    ).not.toBeInTheDocument();
  });

  // orchestrator's reviewquestions.go: a change whose review left
  // questions for its owner is presented as that decision, with Submit as
  // "ship as is" beside it.
  it("presents a change held on review questions as a decision", async () => {
    const onOpenTask = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "awaiting_submit",
          pullRequest: "acme/widgets#7",
          reviewQuestions: "92",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={onOpenTask}
        act={vi.fn()}
      />,
    );
    const notice = screen.getByTestId("review-questions");
    expect(notice).toHaveTextContent("Review needs your decision");
    expect(
      screen.getByRole("button", { name: "Ship as is" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Submit" }),
    ).not.toBeInTheDocument();
    await user.click(
      within(notice).getByRole("link", { name: "Review: task 92" }),
    );
    expect(onOpenTask).toHaveBeenLastCalledWith("92");
  });

  it("points at the security review once the hold has ended", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "awaiting_submit",
          pullRequest: "acme/widgets#7",
          securityReviewTask: "91",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.queryByText("Security hold")).not.toBeInTheDocument();
    expect(screen.getByTestId("security-hold")).toHaveTextContent(
      "read its security review, then Submit",
    );
  });

  it("leaves a person's own task references alone", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          comments: [
            { author: "ann", authorKind: "human", body: "Same as task 91." },
          ],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole("link", { name: "task 91" }),
    ).not.toBeInTheDocument();
  });

  // A chain of re-filed revisions is only legible if it can be walked
  // from either end (model.LinkSupersedes, grain/task-473): the closed
  // task points at what replaced it, and the replacement at what it
  // grew out of.
  it("links both halves of a supersede", async () => {
    const onOpenTask = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, supersedes: "468", supersededBy: "471" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={onOpenTask}
        act={vi.fn()}
      />,
    );

    expect(
      screen.queryByText("supersedes", { exact: false }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Provenance/ }));

    expect(
      screen.getByText("supersedes", { exact: false }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("superseded by", { exact: false }),
    ).toBeInTheDocument();

    await user.click(screen.getByText("471"));
    expect(onOpenTask).toHaveBeenCalledWith("471");
  });

  it("collapses provenance by default, expands to show all provenance links, resets on task switch, and renders nothing when absent", async () => {
    const onOpenTask = vi.fn();
    const onOpenSuiteRun = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={onOpenTask}
        onOpenSuiteRun={onOpenSuiteRun}
        act={vi.fn()}
      />,
    );

    // A task with no provenance metadata renders no provenance toggle at all.
    expect(
      screen.queryByRole("button", { name: /Provenance/ }),
    ).not.toBeInTheDocument();

    const richTask = {
      ...baseTask,
      id: "12",
      pullRequest: "acme/widgets#42",
      generatedFrom: "9",
      filedBySuiteRun: {
        runId: 3,
        pass: 2,
        suiteName: "walk-suite",
        templateName: "Smoke",
      },
      origin: "acme/widgets#10",
      securityReviewTask: "91",
      reverts: "5",
      revertedBy: "6",
      portOf: "7",
      portedBy: ["8"],
      noPort: true,
      googleChatSpace: "spaces/AAAA",
      changelists: ["cl/123456"],
      presubmit: { state: "failed", failing: ["//foo:bar_test"], snapshot: 3 },
    };

    rerender(
      <DetailOverlay
        task={richTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={onOpenTask}
        onOpenSuiteRun={onOpenSuiteRun}
        act={vi.fn()}
      />,
    );

    // Primary freshness items (freshness age and PR link) remain directly visible.
    expect(screen.getByText(/as of just now/)).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "acme/widgets#42" }),
    ).toBeInTheDocument();
    // So is each of the task's CLs, as its go link.
    expect(screen.getByRole("link", { name: "cl/123456" })).toHaveAttribute(
      "href",
      "http://cl/123456",
    );
    // Beside the primary CL, its presubmit badge naming the failing check.
    expect(screen.getByTestId("presubmit-badge")).toHaveAttribute(
      "data-state",
      "failed",
    );
    expect(
      screen.getByText("presubmit failed: //foo:bar_test"),
    ).toBeInTheDocument();

    // Provenance links start collapsed behind the toggle.
    const toggle = screen.getByRole("button", { name: /Provenance/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText(/generated from/)).not.toBeInTheDocument();
    expect(screen.queryByText(/walk-suite run 3/)).not.toBeInTheDocument();
    expect(screen.queryByText(/reverts/)).not.toBeInTheDocument();
    expect(screen.queryByText(/not ported/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Google Chat space/)).not.toBeInTheDocument();

    // Expanding shows all provenance details.
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    const links = within(document.querySelector(".provenance-links"));
    expect(links.getByText(/generated from/)).toBeInTheDocument();
    expect(
      links.getByRole("link", { name: "walk-suite run 3" }),
    ).toBeInTheDocument();
    expect(
      links.getByRole("link", { name: "acme/widgets#10" }),
    ).toHaveAttribute("href", "https://github.com/acme/widgets/issues/10");
    expect(links.getByText(/security review/)).toBeInTheDocument();
    expect(links.getByText(/reverts/)).toBeInTheDocument();
    expect(links.getByText(/reverted by/)).toBeInTheDocument();
    expect(links.getByText(/port of/)).toBeInTheDocument();
    expect(links.getByText(/ported by/)).toBeInTheDocument();
    expect(links.getByText(/not ported/)).toBeInTheDocument();
    expect(links.getByRole("link", { name: "spaces/AAAA" })).toHaveAttribute(
      "href",
      "https://mail.google.com/chat/u/0/#chat/space/AAAA",
    );

    await user.click(screen.getByRole("link", { name: "walk-suite run 3" }));
    expect(onOpenSuiteRun).toHaveBeenCalledWith("3");

    // Switching to another task resets the expanded state back to collapsed.
    rerender(
      <DetailOverlay
        task={{ ...baseTask, id: "13", generatedFrom: "12" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={onOpenTask}
        onOpenSuiteRun={onOpenSuiteRun}
        act={vi.fn()}
      />,
    );
    expect(screen.getByRole("button", { name: /Provenance/ })).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    expect(screen.queryByText(/generated from/)).not.toBeInTheDocument();
  });

  // A failed task's open dependency is not what it is waiting on, so its
  // badge stays "Failed" (state.js's isBlockedWaiting) and there is no
  // separate Blocked chip beside it any more.
  it("shows the failed badge and failure summary for a blocked failed task", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "failed",
          blocked: true,
          failedAttempts: 2,
          lastFailureReason: "build error",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    expect(screen.queryByText("Blocked")).toBeNull();
    expect(document.querySelector(".detail-state .badge")).toHaveClass(
      "badge-failed",
    );
    expect(
      screen.getByText(/2 consecutive failed attempts/),
    ).toBeInTheDocument();
    expect(screen.getByText(/build error/)).toBeInTheDocument();
  });

  // A description is markdown for the same reason a comment body is: a
  // proposed task's own body is written by the run that proposed it
  // (propose_task), in the same syntax.
  it("renders the description as markdown", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          description: "**Why:** the retry path never resets",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    expect(document.querySelector(".description strong")).toHaveTextContent(
      "Why:",
    );
  });

  // grain/task-91, grain/task-175: the title and description above are
  // only part of what a dispatch hands the agent, so the whole prompt is
  // a click away from the task itself -- the only place it is offered,
  // now that task rows no longer carry a button of their own.
  it("opens the full prompt the agent was given", async () => {
    const user = userEvent.setup();
    api.mockResolvedValueOnce({
      prompt: "Fix the login bug\n\nWork in acme/widgets.",
      attempt: 2,
    });
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Prompt" }));

    expect(
      await screen.findByText(/Work in acme\/widgets\./),
    ).toBeInTheDocument();
    expect(api).toHaveBeenLastCalledWith("/api/tasks/12/prompt");
  });

  it("shows the task's own attachments as links to the download endpoint", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          attachments: [
            {
              id: 3,
              filename: "notes.txt",
              contentType: "text/plain",
              size: 4,
            },
          ],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    const link = screen.getByRole("link", { name: "notes.txt" });
    expect(link).toHaveAttribute("href", "/api/tasks/12/attachments/3");
  });

  // grain/task-1937: a queued task whose connection is paused said only
  // "queued"; the pane now names the hold, and the run-now button says
  // a run asked for now waits for it too.
  it("says a queued task is held by its connection's usage-limit pause", () => {
    const entry = {
      connection: "conn-ag",
      connectionName: "AG Connection",
      team: baseTask.team || "default",
      framework: "antigravity",
      until: "2026-10-04T22:58:16Z",
      secondsRemaining: 21540,
      reason: "antigravity: quota exhausted",
    };
    const paused = {
      ...config,
      agentFramework: "antigravity",
      agentBackends: [
        { id: "conn-ag", framework: "antigravity", team: entry.team },
        { id: "conn-claude", framework: "claude", team: entry.team },
      ],
      agentPause: {
        paused: true,
        until: entry.until,
        secondsRemaining: entry.secondsRemaining,
        reason: entry.reason,
        pauses: [entry],
      },
    };
    const { rerender } = render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={paused}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    // Pinned to the paused connection: nothing else to run on.
    rerender(
      <DetailOverlay
        task={{ ...baseTask, agentBackendId: "conn-ag" }}
        tasks={[]}
        config={paused}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    const line = document.querySelector(".detail-framework-paused");
    expect(line).toHaveTextContent(
      /waiting: connection AG Connection is paused until .*\(about 5h 59m\) — antigravity: quota exhausted/,
    );
    expect(
      screen.getByText(/A run asked for now waits for that/),
    ).toBeInTheDocument();

    // A task the rotation can put on the team's other connection is not
    // held by it.
    rerender(
      <DetailOverlay
        task={{ ...baseTask, agentFramework: "claude" }}
        tasks={[]}
        config={paused}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      document.querySelector(".detail-framework-paused"),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByText(/A run asked for now waits for that/),
    ).not.toBeInTheDocument();
  });

  describe("pending question callout", () => {
    const waitingTask = {
      ...baseTask,
      state: "awaiting_reply",
      description: "Long original description of the task.",
      comments: [
        {
          id: 1,
          author: "bsalmon",
          body: "Earlier note",
          createdAt: "2026-06-01T10:00:00Z",
        },
        {
          id: 2,
          author: "grain",
          body: "Should we target **release-1.4** or `main`?",
          createdAt: "2026-06-01T10:05:00Z",
          attachments: [
            {
              id: 7,
              filename: "trace.log",
              contentType: "text/plain",
              size: 128,
            },
          ],
        },
      ],
    };

    it("surfaces the pending question and reply box up front ahead of the description and timeline", async () => {
      const user = userEvent.setup();
      const act = vi.fn((fn) => fn().then(() => true));
      render(
        <DetailOverlay
          task={waitingTask}
          tasks={[]}
          config={config}
          onClose={() => {}}
          onOpenTask={() => {}}
          act={act}
        />,
      );

      const callout = screen.getByTestId("pending-question");
      expect(
        within(callout).getByText("Waiting for your reply"),
      ).toBeInTheDocument();
      expect(
        callout.querySelector(".pending-question-body strong"),
      ).toHaveTextContent("release-1.4");
      expect(
        within(callout).getByRole("link", { name: "trace.log" }),
      ).toHaveAttribute("href", "/api/tasks/12/attachments/7");

      const description = document.querySelector(".description");
      const timeline = document.querySelector(".timeline");
      expect(
        callout.compareDocumentPosition(description) &
          Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();
      expect(
        callout.compareDocumentPosition(timeline) &
          Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();

      const replyBox = within(callout).getByLabelText("Reply to 12");
      await user.type(replyBox, "Target release-1.4, please.");
      await user.click(within(callout).getByRole("button", { name: "Send" }));

      expect(act).toHaveBeenCalledTimes(1);
      expect(api).toHaveBeenCalledWith("/api/tasks/12/comments", {
        method: "POST",
        body: JSON.stringify({
          body: "Target release-1.4, please.",
          attachments: [],
        }),
      });
      await waitFor(() => expect(replyBox).toHaveValue(""));
    });

    it("preserves drafted reply text across background poll re-renders and failed sends", async () => {
      const user = userEvent.setup();
      const act = vi.fn().mockResolvedValue(false);
      const { rerender } = render(
        <DetailOverlay
          task={waitingTask}
          tasks={[]}
          config={config}
          onClose={() => {}}
          onOpenTask={() => {}}
          act={act}
        />,
      );

      const callout = screen.getByTestId("pending-question");
      const replyBox = within(callout).getByLabelText("Reply to 12");
      await user.type(replyBox, "Draft answer in progress");

      // Simulate a 3-second poll re-render with a fresh task object reference.
      rerender(
        <DetailOverlay
          task={{ ...waitingTask, comments: [...waitingTask.comments] }}
          tasks={[]}
          config={config}
          onClose={() => {}}
          onOpenTask={() => {}}
          act={act}
        />,
      );
      expect(
        within(screen.getByTestId("pending-question")).getByLabelText(
          "Reply to 12",
        ),
      ).toHaveValue("Draft answer in progress");

      // A failed send restores the draft rather than losing it.
      await user.click(within(callout).getByRole("button", { name: "Send" }));
      expect(act).toHaveBeenCalledTimes(1);
      expect(
        within(screen.getByTestId("pending-question")).getByLabelText(
          "Reply to 12",
        ),
      ).toHaveValue("Draft answer in progress");
    });

    it("omits the pending question callout when waiting on a secret, interactive, or not awaiting_reply", () => {
      const { rerender } = render(
        <DetailOverlay
          task={{
            ...waitingTask,
            pendingSecret: {
              name: "api-token",
              secret: "api-token",
              key: "value",
              reason: "Needed for deploy",
              set: false,
            },
          }}
          tasks={[]}
          config={config}
          onClose={() => {}}
          onOpenTask={() => {}}
          act={vi.fn()}
        />,
      );
      expect(screen.queryByTestId("pending-question")).not.toBeInTheDocument();

      rerender(
        <DetailOverlay
          task={{ ...waitingTask, interactive: true }}
          tasks={[]}
          config={config}
          onClose={() => {}}
          onOpenTask={() => {}}
          act={vi.fn()}
        />,
      );
      expect(screen.queryByTestId("pending-question")).not.toBeInTheDocument();

      rerender(
        <DetailOverlay
          task={{ ...waitingTask, state: "running" }}
          tasks={[]}
          config={config}
          onClose={() => {}}
          onOpenTask={() => {}}
          act={vi.fn()}
        />,
      );
      expect(screen.queryByTestId("pending-question")).not.toBeInTheDocument();
    });

    it("opens linked task references in the pending question on plain click and leaves modified clicks to the browser", async () => {
      const onOpenTask = vi.fn();
      const user = userEvent.setup();
      render(
        <DetailOverlay
          task={{
            ...waitingTask,
            comments: [
              {
                id: 2,
                author: "grain",
                authorKind: "automation",
                body: "Blocked on task 91 — should we wait for it?",
                createdAt: "2026-06-01T10:05:00Z",
              },
            ],
          }}
          tasks={[]}
          config={config}
          onClose={() => {}}
          onOpenTask={onOpenTask}
          act={vi.fn()}
        />,
      );

      const callout = screen.getByTestId("pending-question");
      const link = within(callout).getByRole("link", { name: "task 91" });
      expect(link).toHaveAttribute("href", "#task-91");

      await user.keyboard("{Control>}");
      await user.click(link);
      await user.keyboard("{/Control}");
      expect(onOpenTask).not.toHaveBeenCalled();

      await user.click(link);
      expect(onOpenTask).toHaveBeenCalledWith("91");
    });
  });

  it("lets an operator edit model and network access directly in the sidebar on an active console session task", async () => {
    const user = userEvent.setup();
    const act = vi.fn((fn) => fn().then(() => true));
    const sessionTask = {
      ...baseTask,
      interactive: true,
      consoleSession: "director",
      model: "",
      networkAccess: "open",
    };
    const sessionConfig = {
      ...config,
      deployment: { id: "default", defaultModel: "gpt-5.6-sol" },
      agentBackends: [
        {
          id: "claude-deep",
          framework: "claude",
          models: ["claude-opus-5"],
        },
      ],
    };

    render(
      <DetailOverlay
        task={sessionTask}
        tasks={[]}
        config={sessionConfig}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    const netSelect = screen.getByLabelText("Network access");
    expect(netSelect).toHaveValue("open");
    await user.selectOptions(netSelect, "none");
    expect(api).toHaveBeenCalledWith("/api/tasks/12", {
      method: "PATCH",
      body: JSON.stringify({ networkAccess: "none" }),
    });

    const picker = screen.getByRole("combobox", { name: "Model" });
    await user.click(picker);
    await user.click(screen.getByRole("option", { name: "claude-opus-5" }));
    expect(api).toHaveBeenCalledWith("/api/tasks/12", {
      method: "PATCH",
      body: JSON.stringify({ model: "claude-opus-5" }),
    });
  });
});
