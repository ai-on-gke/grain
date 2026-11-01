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

import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import DetailOverlay from "../DetailOverlay.jsx";
import { TimelineOrderProvider } from "../../TimelineOrderContext.jsx";
import api from "../../api.js";
import fileToAttachment from "../../attachments.js";
import { STATE_LABELS } from "../../state.js";
import { resetDetailMocks, baseTask, config } from "./testFixtures.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../../attachments.js", () => ({ default: vi.fn() }));

describe("DetailOverlay timeline", () => {
  afterEach(resetDetailMocks);

  it("says that commenting on a task awaiting reply reopens it", () => {
    const { rerender } = render(
      <DetailOverlay
        task={{ ...baseTask, state: "awaiting_reply" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.getByText("Sending a comment reopens this task for another run."),
    ).toBeInTheDocument();
    rerender(
      <DetailOverlay
        task={{ ...baseTask, state: "awaiting_submit" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByText(
        "Sending a comment reopens this task for another run.",
      ),
    ).not.toBeInTheDocument();
  });

  it("shows no timeline entries when the task has no history", () => {
    const { container } = render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("Timeline")).toBeInTheDocument();
    expect(container.querySelectorAll(".timeline-item")).toHaveLength(0);
  });

  // bwsalmon/agents#503: the first attempt is the task's one "running"
  // node -- it only reads as "Attempt #1" once a second one exists to
  // tell it apart from.
  it("lists every attempt on the timeline in order, numbering only the second and later ones", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T12:00:00Z",
              finishedAt: "2026-08-28T12:10:00Z",
              outcome: "failed",
              detail: "build error",
            },
            { number: 2, startedAt: "2026-08-28T13:00:00Z" },
          ],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    expect(screen.getByText("Failed")).toBeInTheDocument();
    expect(screen.queryByText("Attempt #1 · Failed")).not.toBeInTheDocument();
    expect(screen.getByText("build error")).toBeInTheDocument();
    expect(screen.getByText("Attempt #2 · Running")).toBeInTheDocument();
  });

  // A task left to the deployment default or the rotation can run each
  // attempt on a different model, so each attempt says which it got --
  // finished or still running -- and one with none recorded says nothing.
  it("shows the model each attempt ran on in its meta line", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T12:00:00Z",
              finishedAt: "2026-08-28T12:10:00Z",
              outcome: "failed",
              model: "gemini-3.1-pro",
              agentFramework: "antigravity",
            },
            {
              number: 2,
              startedAt: "2026-08-28T13:00:00Z",
              finishedAt: "2026-08-28T13:10:00Z",
              outcome: "failed",
            },
            {
              number: 3,
              startedAt: "2026-08-28T14:00:00Z",
              model: "gemini-3.1-flash",
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

    const first = screen.getByText("gemini-3.1-pro");
    expect(first).toHaveClass("timeline-attempt-model");
    expect(first.closest(".timeline-attempt-ran-on")).toHaveAttribute(
      "title",
      "Ran on gemini-3.1-pro (antigravity)",
    );
    expect(first.closest(".timeline-meta").textContent).toMatch(
      /^started .* · finished .* · gemini-3\.1-pro$/,
    );
    const running = screen.getByText("gemini-3.1-flash");
    expect(running.closest(".timeline-meta").textContent).toMatch(
      /^started .* · gemini-3\.1-flash$/,
    );
    expect(document.querySelectorAll(".timeline-attempt-model")).toHaveLength(
      2,
    );
  });

  // bwsalmon/agents#503: a "running" transition and the attempt it started
  // both landing on the timeline read as two nodes for the same moment.
  it("shows a single node for a running transition and its first attempt, not two", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "running",
          transitions: [
            { state: "queued", at: "2026-08-28T12:00:00Z" },
            { state: "running", at: "2026-08-28T12:01:00Z" },
          ],
          attempts: [{ number: 1, startedAt: "2026-08-28T12:01:00Z" }],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    const list = screen
      .getByText("Timeline")
      .closest(".timeline")
      .querySelector(".timeline-list");
    expect(within(list).getAllByText("Running")).toHaveLength(1);
    expect(list.querySelectorAll(".timeline-item")).toHaveLength(2);
  });

  it("lists every transition on the timeline in order, with its label and time", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          transitions: [
            { state: "proposed", at: "2026-08-28T12:00:00Z" },
            { state: "queued", at: "2026-08-28T12:01:00Z" },
            { state: "running", at: "2026-08-28T12:02:00Z" },
          ],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    const list = screen
      .getByText("Timeline")
      .closest(".timeline")
      .querySelector(".timeline-list");
    const labels = ["Proposed", "Waiting", "Running"];
    for (const label of labels) {
      expect(within(list).getByText(label)).toBeInTheDocument();
    }
  });

  it("lists pull request events on the timeline with their own label and, when a pull request is linked, a link to it", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          pullRequest: "acme/widgets#42",
          pullRequestEvents: [
            { kind: "opened", at: "2026-08-28T12:00:00Z" },
            { kind: "merged", at: "2026-08-28T13:00:00Z" },
          ],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    const list = screen
      .getByText("Timeline")
      .closest(".timeline")
      .querySelector(".timeline-list");
    expect(within(list).getByText("PR opened")).toBeInTheDocument();
    expect(within(list).getByText("PR merged")).toBeInTheDocument();
    expect(
      within(list).getAllByRole("link", { name: "acme/widgets#42" }),
    ).toHaveLength(2);
  });

  it("labels a pull request closed without merging distinctly from a merged one", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          pullRequestEvents: [{ kind: "closed", at: "2026-08-28T12:00:00Z" }],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText("PR closed without merging")).toBeInTheDocument();
  });

  // bwsalmon/agents#503: a synced "PR closed"/"merged" event can land with
  // a later timestamp than the local "closed" transition that preceded
  // it -- "closed" should still read as the end of the timeline, which
  // under the newest-first default (grain/task-203) is the top of the
  // list.
  it("keeps the closed transition at the end of the chronology even when a pull request event syncs in with a later timestamp", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          transitions: [
            { state: "proposed", at: "2026-08-28T09:00:00Z" },
            { state: "closed", at: "2026-08-28T10:00:00Z" },
          ],
          pullRequestEvents: [{ kind: "merged", at: "2026-08-28T11:00:00Z" }],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    const list = screen
      .getByText("Timeline")
      .closest(".timeline")
      .querySelector(".timeline-list");
    const items = [...list.querySelectorAll(".timeline-item")];
    expect(items[0].textContent).toContain("Closed");
  });

  // A close somebody asked for and the reopen that undid it
  // (model.TaskEvent): both on the timeline, each saying who.
  it("shows who closed and reopened a task, why, and what the close cancelled", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          transitions: [
            { state: "queued", at: "2026-08-28T09:00:00Z" },
            {
              state: "closed",
              at: "2026-08-28T10:00:00Z",
              by: "human bob@example.com",
              reason: "cycling in-flight tasks",
              detail:
                "cancelled run 1920-1 (attempt 1) and CI run b8d940e14a9aef1d",
            },
            { state: "reopened", at: "2026-08-28T10:00:05Z", by: "agent 1937" },
          ],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    expect(screen.getByText("Reopened")).toBeInTheDocument();
    expect(screen.getByText("by agent 1937")).toBeInTheDocument();
    expect(
      screen.getByText(
        "by human bob@example.com · cycling in-flight tasks · cancelled run 1920-1 (attempt 1) and CI run b8d940e14a9aef1d",
      ),
    ).toBeInTheDocument();
  });

  // bwsalmon/agents#503: a pull request re-linked to a reopened task keeps
  // its own original open time, which can predate this lifecycle's first
  // transition -- it should not sort ahead of everything else. Under the
  // newest-first default (grain/task-203) the earliest event is the last
  // row, which is where "Proposed" is looked for here.
  it("does not let a pull request opened before this lifecycle's first transition sort ahead of it", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          transitions: [{ state: "proposed", at: "2026-08-28T12:00:00Z" }],
          pullRequestEvents: [{ kind: "opened", at: "2026-08-01T00:00:00Z" }],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    const list = screen
      .getByText("Timeline")
      .closest(".timeline")
      .querySelector(".timeline-list");
    const items = [...list.querySelectorAll(".timeline-item")];
    expect(items[items.length - 1].textContent).toContain("Proposed");
  });

  it("interleaves transitions, attempts and comments by their own timestamps", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          transitions: [
            { state: "proposed", at: "2026-08-28T09:00:00Z" },
            { state: "closed", at: "2026-08-28T15:00:00Z" },
          ],
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T12:00:00Z",
              finishedAt: "2026-08-28T12:10:00Z",
              outcome: "succeeded",
            },
          ],
          comments: [
            {
              author: "alice",
              authorKind: "human",
              body: "looks good",
              createdAt: "2026-08-28T13:00:00Z",
            },
          ],
          pullRequestEvents: [{ kind: "merged", at: "2026-08-28T14:00:00Z" }],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    const titles = screen
      .getAllByText(/^Proposed$|^Succeeded$|^looks good$|^PR merged$|^Closed$/)
      .map((el) => el.textContent);
    // Newest first by default (grain/task-203), so this is the
    // chronological interleaving read from its end back to its start.
    expect(titles).toEqual([
      "Closed",
      "PR merged",
      "looks good",
      "Succeeded",
      "Proposed",
    ]);
  });

  it("renders copy button for comments and copies comment body on click", async () => {
    const writeTextMock = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: {
        writeText: writeTextMock,
      },
      writable: true,
      configurable: true,
    });

    render(
      <DetailOverlay
        task={{
          ...baseTask,
          comments: [
            {
              author: "alice",
              authorKind: "human",
              body: "looks good to me",
              createdAt: "2026-08-28T13:00:00Z",
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

    const copyBtn = screen.getByRole("button", { name: "Copy comment" });
    expect(copyBtn).toBeInTheDocument();

    fireEvent.click(copyBtn);
    expect(writeTextMock).toHaveBeenCalledWith("looks good to me");
  });

  // grain/task-203: the timeline reads newest first, and which way round
  // it reads is the reader's own per-browser choice
  // (TimelineOrderContext). The reply box goes with the newest end
  // rather than staying at the bottom: under a newest-first feed,
  // replying to what a run just said should not mean scrolling past
  // everything that came before it.
  describe("which way round the timeline reads", () => {
    afterEach(() => {
      localStorage.clear();
    });

    const historicTask = {
      ...baseTask,
      transitions: [
        { state: "proposed", at: "2026-08-28T09:00:00Z" },
        { state: "queued", at: "2026-08-28T10:00:00Z" },
        { state: "running", at: "2026-08-28T11:00:00Z" },
      ],
    };

    const rowLabels = () =>
      [
        ...screen
          .getByText("Timeline")
          .closest(".timeline")
          .querySelectorAll(".timeline-item .timeline-title"),
      ].map((el) => el.textContent);

    it("shows the most recent entry first, with the reply box above the list", () => {
      render(
        <DetailOverlay
          task={historicTask}
          tasks={[]}
          config={config}
          onClose={() => {}}
          onOpenTask={() => {}}
          act={vi.fn()}
        />,
      );

      expect(rowLabels()).toEqual(["Running", "Waiting", "Proposed"]);
      const timeline = screen.getByText("Timeline").closest(".timeline");
      expect(timeline).toHaveClass("timeline-newest-first");
      expect(
        timeline
          .querySelector(".comment-form")
          .compareDocumentPosition(timeline.querySelector(".timeline-list")) &
          Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();
    });

    it("shows the oldest entry first, with the reply box below the list, when the reader has asked for that", () => {
      localStorage.setItem("grain.timelineOrder", "oldest");
      render(
        <TimelineOrderProvider>
          <DetailOverlay
            task={historicTask}
            tasks={[]}
            config={config}
            onClose={() => {}}
            onOpenTask={() => {}}
            act={vi.fn()}
          />
        </TimelineOrderProvider>,
      );

      expect(rowLabels()).toEqual(["Proposed", "Waiting", "Running"]);
      const timeline = screen.getByText("Timeline").closest(".timeline");
      expect(timeline).not.toHaveClass("timeline-newest-first");
      expect(
        timeline
          .querySelector(".timeline-list")
          .compareDocumentPosition(timeline.querySelector(".comment-form")) &
          Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();
    });
  });

  it("only animates the running badge for the current transition, not a past one", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "running",
          transitions: [
            { state: "queued", at: "2026-08-28T12:00:00Z" },
            { state: "running", at: "2026-08-28T12:01:00Z" },
            { state: "awaiting_reply", at: "2026-08-28T12:02:00Z" },
            { state: "running", at: "2026-08-28T12:03:00Z" },
          ],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    // Newest first (grain/task-203): the current "running" is the first
    // of the two rows, the finished one from earlier the second.
    const runningItems = screen
      .getAllByText("Running")
      .map((el) => el.closest(".timeline-item"));
    expect(runningItems[0].querySelector(".badge")).not.toHaveClass(
      "badge-static",
    );
    expect(runningItems[1].querySelector(".badge")).toHaveClass("badge-static");
    expect(runningItems[1].querySelector(".timeline-marker")).not.toHaveClass(
      "timeline-marker-mark",
    );
  });

  it("does not animate non-running timeline items even when the task is running", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "running",
          transitions: [
            { state: "proposed", at: "2026-08-28T12:00:00Z" },
            { state: "queued", at: "2026-08-28T12:01:00Z" },
          ],
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T12:02:00Z",
              outcome: "",
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

    const timeline = document.querySelector(".timeline");
    const queuedItem = within(timeline)
      .getByText("Waiting")
      .closest(".timeline-item");
    expect(queuedItem.querySelector(".badge")).toHaveClass("badge-queued");
    expect(queuedItem.querySelector(".badge")).not.toHaveClass("badge-mark");
    expect(queuedItem.querySelector(".grain-mark-sheet")).toBeNull();

    const runningAttempt = within(timeline)
      .getByText("Running")
      .closest(".timeline-item");
    expect(runningAttempt.querySelector(".badge")).toHaveClass("badge-mark");
    expect(runningAttempt.querySelector(".grain-mark-sheet")).not.toBeNull();

    // The 20px mark's marker starts the connecting line below the mark
    // rather than at the 11px dot's offset, inside it (style.css keys on
    // this class); every other entry keeps the dot's.
    expect(runningAttempt.querySelector(".timeline-marker")).toHaveClass(
      "timeline-marker-mark",
    );
    expect(queuedItem.querySelector(".timeline-marker")).not.toHaveClass(
      "timeline-marker-mark",
    );
  });

  // A run that ended without an agent ever finishing still ended, and
  // badly. Without an entry of its own, "setup-failed" rendered through
  // the raw-string fallback as "Setup-failed" under a "queued" badge --
  // which reads as a run that has not started, for one that started and
  // could not be given a sandbox.
  it("labels an attempt whose sandbox could not be built", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T12:00:00Z",
              finishedAt: "2026-08-28T12:02:00Z",
              outcome: "setup-failed",
              detail:
                "this run's sandbox could not be prepared: guest never became reachable",
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

    const item = screen.getByText("Setup failed").closest(".timeline-item");
    expect(item.querySelector(".badge")).toHaveClass("badge-failed");
  });

  // The detail of a setup failure says only that the sandbox could not be
  // prepared; how far grain actually got before it broke lives in the
  // phrase it stopped on (ui.Attempt.SetupNote), which nothing showed
  // anybody until now -- Store.TaskActivity reads live runs only.
  it("says how far setup got on an attempt that never reached its agent", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T12:00:00Z",
              finishedAt: "2026-08-28T12:02:00Z",
              outcome: "setup-failed",
              detail:
                "this run's sandbox could not be prepared: guest never became reachable",
              setupNote: "cloning acme/widgets",
              setupNoteAt: "2026-08-28T12:01:00Z",
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

    const item = screen.getByText("Setup failed").closest(".timeline-item");
    const note = item.querySelector(".timeline-setup-note");
    expect(note).toHaveTextContent("gave up while: cloning acme/widgets");
    // Whose sentence it is, said out loud rather than left to the
    // wording -- the same mark the live phrase carries on a task row.
    expect(note.querySelector(".task-activity-by")).toHaveTextContent("grain");
  });

  // Nobody gave up on a run somebody cancelled: the verb follows the
  // outcome so the breadcrumb does not read as a verdict on a run that
  // was simply stopped.
  it("says a cancelled attempt stopped rather than gave up", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T12:00:00Z",
              finishedAt: "2026-08-28T12:02:00Z",
              outcome: "cancelled",
              setupNote: "building a sandbox",
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

    expect(
      screen.getByText(/stopped while: building a sandbox/),
    ).toBeInTheDocument();
  });

  // A run still in setup is narrated as something happening now, at the
  // top of this same page. Repeating it on the attempt in the past tense
  // would say it had already given up.
  it("leaves the setup phrase off an attempt that is still running", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "running",
          activity: "building a sandbox",
          activityBySetup: true,
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T12:00:00Z",
              setupNote: "building a sandbox",
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

    expect(screen.queryByText(/gave up while/)).not.toBeInTheDocument();
    expect(
      document.querySelector(".timeline-setup-note"),
    ).not.toBeInTheDocument();
  });

  it("labels an attempt whose process died mid-run", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T12:00:00Z",
              finishedAt: "2026-08-28T12:02:00Z",
              outcome: "orphaned",
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

    const item = screen.getByText("Orphaned").closest(".timeline-item");
    expect(item.querySelector(".badge")).toHaveClass("badge-failed");
  });

  // The one a task retried over and over is most likely to be sitting
  // on: the agent worked, the branch is pushed, and the call that turns
  // it into a pull request is what failed. Through the raw-string
  // fallback it read "Finish-failed" under a "queued" badge, which is
  // the opposite of what a reader needs from an attempt that is the
  // reason the task keeps coming back.
  it("labels an attempt whose result could not be turned into a pull request", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T12:00:00Z",
              finishedAt: "2026-08-28T12:02:00Z",
              outcome: "finish-failed",
              detail:
                "this run's result could not be turned into a pull request or a comment: 422 Validation Failed",
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

    const item = screen.getByText("Finish failed").closest(".timeline-item");
    expect(item.querySelector(".badge")).toHaveClass("badge-failed");
  });

  // The fallback still has to work: an outcome added on the backend
  // before this map catches up shows up rather than disappearing.
  it("falls back to the raw outcome for one it does not recognise", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T12:00:00Z",
              finishedAt: "2026-08-28T12:02:00Z",
              outcome: "invented",
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

    expect(screen.getByText("Invented")).toBeInTheDocument();
  });

  it("renders comments, distinguishing ones relayed on behalf of someone else", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          comments: [
            { author: "alice", authorKind: "human", body: "please retry" },
            {
              author: "grain",
              authorKind: "agent",
              onBehalfOf: "the dispatched run",
              body: "what branch should I target?",
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

    expect(screen.getByText("alice · human")).toBeInTheDocument();
    expect(screen.getByText("please retry")).toBeInTheDocument();
    expect(
      screen.getByText("grain on behalf of the dispatched run · agent"),
    ).toBeInTheDocument();
  });

  // grain/task-287: a run asked to write a handover posts a summary of a
  // conversation the person reading it can already see, so the thread has
  // to say why it is there -- and which comment grain keeps quoting to
  // later runs once the rest of the thread is too long to carry.
  it("marks the comment that is this task's handover", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          comments: [
            { author: "alice", authorKind: "human", body: "please retry" },
            {
              author: "grain",
              authorKind: "agent",
              onBehalfOf: "the dispatched run",
              handover: true,
              body: "the merge lane may only ever hold one branch",
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

    expect(screen.getByText("alice · human")).toBeInTheDocument();
    expect(
      screen.getByText(
        "grain on behalf of the dispatched run · agent · handover",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/standing summary for later runs/),
    ).toBeInTheDocument();
  });

  // grain/task-93: an agent's answer arrives as markdown, and reading it
  // as one flat run of text is what this renders it out of.
  it("renders a comment body as markdown", () => {
    // document, not render()'s own container: Overlay is a MUI Dialog,
    // so its content is portalled out of the container into <body>.
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          comments: [
            {
              author: "grain",
              authorKind: "agent",
              body: "## What I did\n\n- touched `run.go`\n- pushed it",
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

    expect(
      screen.getByRole("heading", { name: "What I did" }),
    ).toBeInTheDocument();
    expect(document.querySelectorAll(".timeline-comment-body li")).toHaveLength(
      2,
    );
    expect(screen.getByText("run.go").tagName).toBe("CODE");
  });

  it("posts a comment and clears the textarea", async () => {
    const act = vi.fn().mockResolvedValue(true);
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    const textarea = screen.getByPlaceholderText("Reply...");
    await user.type(textarea, "sounds good");
    await user.click(screen.getByRole("button", { name: "Comment" }));

    expect(act).toHaveBeenCalledWith(expect.any(Function), "12");
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/comments", {
      method: "POST",
      body: JSON.stringify({
        body: "sounds good",
        attachments: [],
      }),
    });
    expect(textarea).toHaveValue("");
  });

  // grain/task-1699: the box is cleared as the comment is sent, and a
  // comment the server refused used to stay cleared -- lost with the
  // error. act() resolves false for one that did not land.
  it("puts a comment that did not land back in the textarea", async () => {
    const act = vi.fn().mockResolvedValue(false);
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    const textarea = screen.getByPlaceholderText("Reply...");
    await user.type(textarea, "qa comment under 500");
    await user.click(screen.getByRole("button", { name: "Comment" }));

    expect(act).toHaveBeenCalledTimes(1);
    expect(textarea).toHaveValue("qa comment under 500");
  });

  it("ignores duplicate comment submissions while one is already in flight", async () => {
    let finishAct;
    const act = vi.fn().mockImplementation(
      () =>
        new Promise((resolve) => {
          finishAct = resolve;
        }),
    );
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    const textarea = screen.getByPlaceholderText("Reply...");
    await user.type(textarea, "sounds good");
    const button = screen.getByRole("button", { name: "Comment" });
    await user.click(button);
    await user.click(button);

    expect(act).toHaveBeenCalledTimes(1);
    finishAct();
  });

  it("does not post an empty or whitespace-only comment with no attachment either", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.type(screen.getByPlaceholderText("Reply..."), "   ");
    await user.click(screen.getByRole("button", { name: "Comment" }));

    expect(act).not.toHaveBeenCalled();
  });

  // bwsalmon/agents#522: a reply that carries only a file, no text, is
  // still worth sending -- the empty-comment guard above must not treat
  // it the same as one with nothing at all.
  it("posts a comment carrying only an attachment, with no body", async () => {
    const upload = {
      filename: "screenshot.png",
      contentType: "image/png",
      content: "ZmFrZQ==",
    };
    fileToAttachment.mockResolvedValueOnce(upload);
    const act = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    const file = new File(["fake"], "screenshot.png", { type: "image/png" });
    await user.upload(document.querySelector('input[type="file"]'), file);
    await user.click(screen.getByRole("button", { name: "Comment" }));

    expect(act).toHaveBeenCalledWith(expect.any(Function), "12");
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/comments", {
      method: "POST",
      body: JSON.stringify({
        body: "",
        attachments: [upload],
      }),
    });
  });

  it("attaches an image pasted into the comment reply box", async () => {
    const upload = {
      filename: "pasted-comment.png",
      contentType: "image/png",
      content: "cGZpbGU=",
    };
    fileToAttachment.mockResolvedValueOnce(upload);
    const act = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    const file = new File(["fake"], "pasted-comment.png", {
      type: "image/png",
    });
    const replyBox = screen.getByPlaceholderText("Reply...");
    fireEvent.paste(replyBox, {
      clipboardData: {
        items: [
          {
            kind: "file",
            type: "image/png",
            getAsFile: () => file,
          },
        ],
      },
    });

    expect(await screen.findByText("pasted-comment.png")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Comment" }));

    expect(act).toHaveBeenCalledWith(expect.any(Function), "12");
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/comments", {
      method: "POST",
      body: JSON.stringify({
        body: "",
        attachments: [upload],
      }),
    });
  });

  it("attaches multiple non-image files pasted into the comment composer", async () => {
    const upload1 = {
      filename: "trace.log",
      contentType: "text/plain",
      content: "dHJhY2U=",
    };
    const upload2 = {
      filename: "archive.zip",
      contentType: "application/zip",
      content: "emlw",
    };
    fileToAttachment
      .mockResolvedValueOnce(upload1)
      .mockResolvedValueOnce(upload2);
    const act = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    const file1 = new File(["trace"], "trace.log", { type: "text/plain" });
    const file2 = new File(["zip"], "archive.zip", {
      type: "application/zip",
    });
    const replyBox = screen.getByPlaceholderText("Reply...");
    fireEvent.paste(replyBox, {
      clipboardData: {
        files: [file1, file2],
      },
    });

    expect(await screen.findByText("trace.log")).toBeInTheDocument();
    expect(await screen.findByText("archive.zip")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Comment" }));

    expect(act).toHaveBeenCalledWith(expect.any(Function), "12");
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/comments", {
      method: "POST",
      body: JSON.stringify({
        body: "",
        attachments: [upload1, upload2],
      }),
    });
  });

  it("attaches files dropped into the comment composer via drag-and-drop", async () => {
    const upload = {
      filename: "dropped.txt",
      contentType: "text/plain",
      content: "dGV4dA==",
    };
    fileToAttachment.mockResolvedValueOnce(upload);
    const act = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    const file = new File(["text"], "dropped.txt", { type: "text/plain" });
    const composer = document.querySelector(".comment-form");

    const dragOverEvt = new Event("dragover", {
      bubbles: true,
      cancelable: true,
    });
    composer.dispatchEvent(dragOverEvt);
    expect(dragOverEvt.defaultPrevented).toBe(true);

    fireEvent.drop(composer, {
      dataTransfer: {
        files: [file],
      },
    });

    expect(await screen.findByText("dropped.txt")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Comment" }));

    expect(act).toHaveBeenCalledWith(expect.any(Function), "12");
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/comments", {
      method: "POST",
      body: JSON.stringify({
        body: "",
        attachments: [upload],
      }),
    });
  });

  it("shows a comment's own attachments as links to the download endpoint", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          comments: [
            {
              author: "alice",
              authorKind: "human",
              body: "",
              attachments: [
                {
                  id: 5,
                  filename: "repro.zip",
                  contentType: "application/zip",
                  size: 9,
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
      />,
    );

    const link = screen.getByRole("link", { name: "repro.zip" });
    expect(link).toHaveAttribute("href", "/api/tasks/12/attachments/5");
  });

  // bwsalmon/agents#446: typing on a task attempt opens a window over its
  // own agent transcript.
  it("opens an attempt's transcript when its timeline row is clicked", async () => {
    api.mockResolvedValue({ transcript: "found it" });
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T12:00:00Z",
              finishedAt: "2026-08-28T12:10:00Z",
              outcome: "succeeded",
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

    await user.click(screen.getByText("Succeeded"));

    expect(
      await screen.findByText("Attempt #1 transcript"),
    ).toBeInTheDocument();
    expect(await screen.findByText("found it")).toBeInTheDocument();
    expect(api).toHaveBeenLastCalledWith("/api/tasks/12/attempts/1/transcript");
  });

  it("opens an attempt's transcript when its timeline row is focused and Enter is pressed", async () => {
    api.mockResolvedValue({ transcript: "found it" });
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          attempts: [{ number: 1, startedAt: "2026-08-28T12:00:00Z" }],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    // The entry is a listitem holding a button, not a button standing
    // in for a listitem (grain/task-1582).
    const item = screen.getByText("Running").closest(".timeline-item-attempt");
    expect(item.tagName).toBe("LI");
    expect(item).not.toHaveAttribute("role");
    within(item).getByRole("button", { name: "Running" }).focus();
    await user.keyboard("{Enter}");

    expect(
      await screen.findByText("Attempt #1 transcript"),
    ).toBeInTheDocument();
  });

  it("does not make a plain transition or comment row interactive", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          transitions: [{ state: "queued", at: "2026-08-28T12:00:00Z" }],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    const row = screen
      .getByText("Timeline")
      .closest(".timeline")
      .querySelector(".timeline-item");
    expect(row).not.toHaveClass("timeline-item-attempt");
    expect(row).not.toHaveAttribute("tabindex");
  });

  it("opens a referenced task in place on a plain click, and leaves a modified click to the browser", async () => {
    const onOpenTask = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          comments: [
            {
              author: "grain",
              authorKind: "automation",
              body: "Depends on task 42.",
            },
          ],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={onOpenTask}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    const link = screen.getByRole("link", { name: "task 42" });
    await user.click(link);
    expect(onOpenTask).toHaveBeenCalledWith("42");

    onOpenTask.mockClear();
    const root = link.closest("body > div");
    let leftToBrowser;
    const follow = (e) => {
      leftToBrowser = !e.defaultPrevented;
      e.preventDefault();
    };
    root.addEventListener("click", follow);
    fireEvent.click(link, { ctrlKey: true });
    root.removeEventListener("click", follow);
    expect(leftToBrowser).toBe(true);
    expect(onOpenTask).not.toHaveBeenCalled();
  });

  // grain/task-4004: the timeline put events out of order in five ways.
  // Each fixture below reproduces one of them, read both ways round.
  describe("chronological order", () => {
    afterEach(() => {
      localStorage.clear();
    });

    // Each row by its title, or a comment by its body.
    const rows = () =>
      [
        ...screen
          .getByText("Timeline")
          .closest(".timeline")
          .querySelectorAll(".timeline-item"),
      ].map(
        (li) =>
          (
            li.querySelector(".timeline-title") ||
            li.querySelector(".timeline-comment-body")
          ).textContent,
      );

    const renderIn = (order, task) => {
      localStorage.setItem("grain.timelineOrder", order);
      render(
        <TimelineOrderProvider>
          <DetailOverlay
            task={{ ...baseTask, ...task }}
            tasks={[]}
            config={config}
            onClose={() => {}}
            onOpenTask={() => {}}
            act={vi.fn()}
          />
        </TimelineOrderProvider>,
      );
      return rows();
    };

    // Renders the task oldest first and newest first, expecting the one
    // order and its mirror.
    const expectChronology = (task, oldestFirst) => {
      expect(renderIn("oldest", task)).toEqual(oldestFirst);
      cleanup();
      expect(renderIn("newest", task)).toEqual([...oldestFirst].reverse());
    };

    const comment = (body, createdAt) => ({
      author: "alice",
      authorKind: "human",
      body,
      createdAt,
    });

    it("keeps a task's history after its close in place when it is reopened and run again", () => {
      expectChronology(
        {
          transitions: [
            { state: "proposed", at: "2026-08-28T09:00:00Z" },
            { state: "queued", at: "2026-08-28T09:01:00Z" },
            { state: "closed", at: "2026-08-28T10:00:00Z" },
            { state: "queued", at: "2026-08-28T11:00:00Z" },
          ],
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T09:02:00Z",
              finishedAt: "2026-08-28T09:30:00Z",
              outcome: "succeeded",
            },
            {
              number: 2,
              startedAt: "2026-08-28T11:05:00Z",
              finishedAt: "2026-08-28T11:30:00Z",
              outcome: "failed",
            },
          ],
          comments: [comment("why again?", "2026-08-28T11:10:00Z")],
          // Synced five minutes after the close: still ahead of it.
          pullRequestEvents: [{ kind: "merged", at: "2026-08-28T10:05:00Z" }],
        },
        [
          "Proposed",
          "Waiting",
          "Succeeded",
          "PR merged",
          "Closed",
          "Waiting",
          "why again?",
          "Attempt #2 · Failed",
        ],
      );
    });

    it("leaves a pull request event after the close when the task moved on in between", () => {
      expectChronology(
        {
          transitions: [
            { state: "proposed", at: "2026-08-28T09:00:00Z" },
            { state: "closed", at: "2026-08-28T10:00:00Z" },
            { state: "queued", at: "2026-08-28T10:10:00Z" },
          ],
          pullRequestEvents: [{ kind: "merged", at: "2026-08-28T10:20:00Z" }],
        },
        ["Proposed", "Closed", "Waiting", "PR merged"],
      );
    });

    it("puts an attempt's outcome after the comments posted while it ran", () => {
      expectChronology(
        {
          transitions: [{ state: "queued", at: "2026-08-28T11:00:00Z" }],
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T12:00:00Z",
              finishedAt: "2026-08-28T12:30:00Z",
              outcome: "succeeded",
            },
            { number: 2, startedAt: "2026-08-28T13:00:00Z" },
          ],
          comments: [
            comment("a question mid-run", "2026-08-28T12:10:00Z"),
            comment("and during the second", "2026-08-28T13:05:00Z"),
          ],
        },
        [
          "Waiting",
          "a question mid-run",
          "Succeeded",
          "and during the second",
          "Attempt #2 · Running",
        ],
      );
    });

    it("keeps an attempt's start time in its meta line and opens its transcript from where it now sits", async () => {
      api.mockResolvedValue({ transcript: "found it" });
      renderIn("oldest", {
        attempts: [
          {
            number: 1,
            startedAt: "2026-08-28T12:00:00Z",
            finishedAt: "2026-08-28T12:30:00Z",
            outcome: "succeeded",
          },
        ],
      });
      const row = screen
        .getByText("Timeline")
        .closest(".timeline")
        .querySelector(".timeline-item-attempt");
      expect(row.querySelector(".timeline-meta").textContent).toMatch(
        /^started .* · finished /,
      );
      fireEvent.click(row);
      expect(await screen.findByText("found it")).toBeInTheDocument();
      expect(api).toHaveBeenLastCalledWith(
        "/api/tasks/12/attempts/1/transcript",
      );
    });

    it("places an undated event beside its neighbours of the same kind, never at the epoch", () => {
      expectChronology(
        {
          transitions: [
            { state: "proposed", at: "2026-08-28T09:00:00Z" },
            { state: "queued", at: "2026-08-28T10:00:00Z" },
          ],
          pullRequestEvents: [
            { kind: "opened", at: "2026-08-28T09:30:00Z" },
            { kind: "merged" },
          ],
          comments: [
            comment("first", "2026-08-28T09:10:00Z"),
            // Go's zero time.Time, which is what an unset field sends.
            comment("unset", "0001-01-01T00:00:00Z"),
            comment("third", "2026-08-28T10:30:00Z"),
          ],
        },
        [
          "Proposed",
          "first",
          "unset",
          "PR opened",
          "PR merged",
          "Waiting",
          "third",
        ],
      );
    });

    it("puts an event with nothing to place it by at the end, saying its time is unknown", () => {
      expectChronology(
        {
          transitions: [{ state: "proposed", at: "2026-08-28T09:00:00Z" }],
          pullRequestEvents: [{ kind: "opened" }],
        },
        ["Proposed", "PR opened"],
      );
      expect(screen.getByText("time unknown")).toBeInTheDocument();
    });

    it("orders same-instant events causally rather than by kind", () => {
      const at = "2026-08-28T12:00:00Z";
      expectChronology(
        {
          // Each array in the reverse of its causal order, so nothing
          // passes by falling out of the way they were stitched together.
          transitions: [
            { state: "proposed", at },
            { state: "queued", at },
            { state: "completed", at },
            { state: "closed", at },
          ],
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T11:59:00Z",
              finishedAt: at,
              outcome: "succeeded",
            },
          ],
          comments: [comment("said in the same second", at)],
          pullRequestEvents: [{ kind: "merged", at }],
        },
        [
          "Proposed",
          "Waiting",
          "said in the same second",
          "PR merged",
          "Succeeded",
          STATE_LABELS.completed,
          "Closed",
        ],
      );
    });

    it("does not let an Invalid Date scramble the rest of the order", () => {
      expectChronology(
        {
          transitions: [
            { state: "proposed", at: "2026-08-28T09:00:00Z" },
            { state: "queued", at: "2026-08-28T10:00:00Z" },
            { state: "closed", at: "2026-08-28T12:00:00Z" },
          ],
          comments: [
            comment("first", "2026-08-28T09:30:00Z"),
            comment("garbled", "not a date"),
            comment("third", "2026-08-28T11:00:00Z"),
          ],
        },
        ["Proposed", "first", "garbled", "Waiting", "third", "Closed"],
      );
    });

    it("marks an inferred transition's time as approximate", () => {
      renderIn("oldest", {
        transitions: [
          { state: "queued", at: "2026-08-28T09:00:00Z" },
          { state: "queued", at: "2026-08-28T10:00:00Z", inferred: true },
        ],
      });
      const whens = [
        ...screen
          .getByText("Timeline")
          .closest(".timeline")
          .querySelectorAll(".timeline-when"),
      ];
      expect(whens[0].textContent).not.toMatch(/^~/);
      expect(whens[0]).not.toHaveAttribute("title");
      expect(whens[1].textContent).toMatch(/^~ /);
      expect(whens[1].getAttribute("title")).toMatch(/^Inferred/);
    });

    it("puts an inferred requeue after the attempt whose finish it stands at", () => {
      expectChronology(
        {
          transitions: [
            { state: "queued", at: "2026-08-28T12:00:00Z", inferred: true },
          ],
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T11:00:00Z",
              finishedAt: "2026-08-28T12:00:00Z",
              outcome: "failed",
            },
          ],
        },
        ["Failed", "Waiting"],
      );
    });
  });
});
