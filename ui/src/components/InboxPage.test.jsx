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
import InboxPage from "./InboxPage.jsx";
import { inboxTasks } from "../state.js";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const task = (over) => ({
  id: "1",
  title: "A task",
  state: "queued",
  capabilities: [],
  blocked: false,
  ...over,
});

// One task in every case the inbox is about, plus three it is not: a
// running task, a queued one and a closed one all wait on grain rather
// than on the reader.
const tasks = [
  task({ id: "1", title: "Add a dark mode toggle", state: "proposed" }),
  task({ id: "2", title: "Fix flaky retry test", state: "queued" }),
  task({ id: "3", title: "Bump the Go toolchain", state: "running" }),
  task({ id: "4", title: "Investigate the stall", state: "awaiting_reply" }),
  task({ id: "5", title: "Rotate the signing key", state: "failed" }),
  task({
    id: "6",
    title: "Cache the readiness probe",
    state: "awaiting_submit",
    pullRequest: "acme/widgets#12",
  }),
  task({
    id: "7",
    title: "Add pagination to the tasks API",
    state: "completed",
    pullRequest: "acme/widgets#13",
    mergeQueueBlockedAt: "2026-09-01T10:00:00Z",
  }),
  task({ id: "8", title: "Spike: websocket transport", state: "closed" }),
  // A queue entry grain's stall detector has flagged: the queue still
  // thinks it is waiting for CI, nothing has escalated, and the note is
  // the only account of it there is (state.js's mergeQueueStalled).
  task({
    id: "10",
    title: "Split the sandbox pool",
    state: "completed",
    pullRequest: "acme/widgets#14",
    mergeQueueState: "waiting_ci",
    mergeQueueNote: "Waiting on check go on acme/widgets#14",
    mergeQueueStalledAt: "2026-09-01T10:00:00Z",
  }),
];

const noop = () => {};
const baseProps = {
  config: null,
  onOpenTask: noop,
  onAct: noop,
  onActBatch: noop,
  showError: noop,
};

function renderInbox(over = {}) {
  return render(<InboxPage {...baseProps} tasks={tasks} {...over} />);
}

// A row's own buttons live beside it, so every assertion about an action
// scopes to the row naming the task rather than to the page.
function row(title) {
  return screen.getByText(title).closest(".inbox-item");
}

describe("InboxPage", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    api.mockReset();
  });

  it("lists every task waiting on the reader, and nothing else", () => {
    const { container } = renderInbox();

    expect(container.querySelectorAll(".inbox-item")).toHaveLength(6);
    expect(screen.getByText("Add a dark mode toggle")).toBeInTheDocument();
    expect(screen.getByText("Investigate the stall")).toBeInTheDocument();
    expect(screen.getByText("Rotate the signing key")).toBeInTheDocument();
    expect(screen.getByText("Cache the readiness probe")).toBeInTheDocument();
    expect(
      screen.getByText("Add pagination to the tasks API"),
    ).toBeInTheDocument();
    expect(screen.getByText("Split the sandbox pool")).toBeInTheDocument();
    expect(screen.queryByText("Fix flaky retry test")).not.toBeInTheDocument();
    expect(screen.queryByText("Bump the Go toolchain")).not.toBeInTheDocument();
    expect(
      screen.queryByText("Spike: websocket transport"),
    ).not.toBeInTheDocument();
  });

  // The one way the page's own groups and state.js's waitsOnUser can
  // drift: a task the predicate admits that no group claims would be
  // counted in the nav rail and shown nowhere.
  it("gives every task waitsOnUser admits a group to sit in", () => {
    const { container } = renderInbox();
    expect(container.querySelectorAll(".inbox-item")).toHaveLength(
      inboxTasks(tasks).length,
    );
  });

  // A queued task with a stall note is the one row here whose badge is
  // telling the truth and whose state is still the problem: nothing on
  // the page would show it otherwise, since "queued" is not a state the
  // inbox admits (state.js's waitsOnUser, eighth). Its note is the
  // substance of the row, exactly as a stalled merge's is one group up.
  it("carries a task whose own state has stood too long, with its note", async () => {
    const onOpenTask = vi.fn();
    const onAct = vi.fn();
    const user = userEvent.setup();
    const stalled = task({
      id: "12",
      title: "Nothing is dispatching this",
      state: "queued",
      stall:
        "this has stood here for more than 3d, which is longer than `reconcileDispatch` should take",
    });
    renderInbox({ tasks: [...tasks, stalled], onOpenTask, onAct });

    const item = row("Nothing is dispatching this");
    expect(item.closest(".inbox-group").querySelector("h3")).toHaveTextContent(
      "Look at a stuck task",
    );
    expect(within(item).getByText("Stalled")).toBeInTheDocument();
    expect(item.querySelector(".inbox-note")).toHaveTextContent(
      "reconcileDispatch",
    );

    // The one answer is a way of going and looking. Nothing here fires a
    // request, because what to do about a stalled task depends on what
    // it turns out to be.
    await user.click(within(item).getByRole("button", { name: "Open task" }));
    expect(onOpenTask).toHaveBeenCalledWith("12");
    expect(onAct).not.toHaveBeenCalled();
  });

  // The task lane's escalation to a person is a button: the stuck
  // task's sub-case names the control that ends that kind of wait
  // (ui.Task.Reason), and the row offers it beside the note and the
  // sub-case itself.
  it("offers a stuck task its sub-case's control", async () => {
    const onAct = vi.fn();
    const user = userEvent.setup();
    const stalled = task({
      id: "12",
      title: "Nothing is dispatching this",
      state: "queued",
      stall: "this has stood here for more than 3d",
      reason: {
        reason: "queued/ready",
        class: "driven",
        driver: "reconcileDispatch",
        resource: "a worker slot (max_workers) and a sandbox",
        control: "Run now",
      },
    });
    renderInbox({ tasks: [...tasks, stalled], onAct });

    const item = row("Nothing is dispatching this");
    const chip = within(item).getByText("ready");
    expect(chip.closest(".task-reason").getAttribute("title")).toContain(
      "Driven by reconcileDispatch",
    );
    // Offered once: the inbox's own action, not the row's as well.
    expect(
      within(item).getAllByRole("button", { name: "Run now" }),
    ).toHaveLength(1);
    await user.click(within(item).getByRole("button", { name: "Run now" }));
    expect(onAct).toHaveBeenCalledTimes(1);
    await onAct.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/run-now", {
      method: "POST",
    });
  });

  // A sub-case with no control a row can press -- waiting on another
  // task, whose own row is where that is dealt with -- gets none.
  it("offers no control for a sub-case without one", () => {
    const stalled = task({
      id: "14",
      title: "Waiting on its dependency",
      state: "queued",
      stall: "this has stood here for more than 3d",
      reason: { reason: "queued/blocked_on_task", class: "driven" },
    });
    renderInbox({ tasks: [...tasks, stalled] });
    const item = row("Waiting on its dependency");
    expect(
      within(item)
        .getAllByRole("button")
        .map((b) => b.textContent),
    ).toEqual(["Open task"]);
  });

  // A completed task carrying both findings goes where the comment and
  // the button are: one task, one row, under the first group that claims
  // it. The task lane's note says only that 'completed' has stood too
  // long; the merge lane's row says which state of the merge cycle it is
  // standing in and offers the pull request.
  it("leaves a merge-blocked task in the group that has an answer for it", () => {
    const both = task({
      id: "13",
      title: "Blocked and standing still",
      state: "completed",
      pullRequest: "acme/widgets#16",
      mergeQueueBlockedAt: "2026-09-01T10:00:00Z",
      stall: "this has stood here for more than 12h",
    });
    const { container } = renderInbox({ tasks: [...tasks, both] });

    expect(
      [...container.querySelectorAll(".inbox-item")].filter((item) =>
        item.textContent.includes("Blocked and standing still"),
      ),
    ).toHaveLength(1);
    const item = row("Blocked and standing still");
    expect(
      within(item).getByRole("link", { name: "Open pull request" }),
    ).toBeInTheDocument();
  });

  // A run that opened a pull request it will not merge by itself is often
  // still running -- blocked in wait_for_checks -- and the change is owed
  // a Submit all the same. It is listed with the finished ones, Submit and
  // all, rather than hidden until the run exits; one the queue will land
  // on its own is not.
  it("offers Submit on a running task whose pull request is owed one", async () => {
    const onAct = vi.fn((fn) => fn());
    api.mockResolvedValue(null);
    const { container } = renderInbox({
      onAct,
      tasks: [
        task({
          id: "21",
          title: "Still waiting on its checks",
          state: "running",
          autoMerge: false,
          pullRequest: "acme/widgets#21",
        }),
        task({
          id: "22",
          title: "Lands by itself",
          state: "running",
          autoMerge: true,
          pullRequest: "acme/widgets#22",
        }),
        task({
          id: "23",
          title: "Already merged",
          state: "running",
          autoMerge: false,
          pullRequest: "acme/widgets#23",
          prMergedAt: "2026-10-25T00:00:00Z",
        }),
      ],
    });

    expect(container.querySelectorAll(".inbox-item")).toHaveLength(1);
    const item = row("Still waiting on its checks");
    expect(item.closest(".inbox-group").querySelector("h3")).toHaveTextContent(
      "Asked: submit for merge",
    );
    await userEvent.click(within(item).getByRole("button", { name: "Submit" }));
    expect(api).toHaveBeenCalledWith("/api/tasks/21/submit", {
      method: "POST",
    });
  });

  it("groups the waits with the answer each one needs first", () => {
    const { container } = renderInbox();
    const headings = [...container.querySelectorAll(".inbox-group-header h3")];
    expect(headings.map((h) => h.textContent)).toEqual([
      "Asked: reply",
      "Asked: submit for merge",
      "Unblock a merge",
      "Look at a stalled merge",
      "Retry or close",
      "Approve a proposal",
    ]);
  });

  // grain/task-372: the inbox has a figure of its own now -- the tray --
  // and this heading carries it at heading size, the way every other
  // page carries the figure of the rail entry that opened it.
  it("heads the pane with the inbox figure", () => {
    const { container } = renderInbox({ tasks });
    expect(
      container.querySelector('.header-icon svg[data-glyph="inbox"]'),
    ).toBeInTheDocument();
  });

  it("says so when nothing is waiting", () => {
    const { container } = renderInbox({
      tasks: [task({ state: "running" })],
    });
    expect(screen.getByText(/Nothing is waiting on you/)).toBeInTheDocument();
    expect(container.querySelectorAll(".inbox-item")).toHaveLength(0);
  });

  it("approves a proposal from its own row", async () => {
    const onAct = vi.fn((mutate) => mutate());
    const user = userEvent.setup();
    renderInbox({ onAct });

    await user.click(
      within(row("Add a dark mode toggle")).getByRole("button", {
        name: "Approve",
      }),
    );

    expect(api).toHaveBeenCalledWith("/api/tasks/1/approve", {
      method: "POST",
    });
  });

  it("submits a finished change from its own row", async () => {
    const onAct = vi.fn((mutate) => mutate());
    const user = userEvent.setup();
    renderInbox({ onAct });

    await user.click(
      within(row("Cache the readiness probe")).getByRole("button", {
        name: "Submit",
      }),
    );

    expect(api).toHaveBeenCalledWith("/api/tasks/6/submit", { method: "POST" });
  });

  // Acknowledging is closing: a task that answered in a comment has no
  // pull request to land and no run left to wait for, so being read is
  // the whole of its ending (model.StateAwaitingReply). No confirm on the
  // way, unlike the Close on a failure -- this one is not giving up on
  // anything.
  it("acknowledges an answered task from its own row", async () => {
    const onAct = vi.fn((mutate) => mutate());
    const user = userEvent.setup();
    renderInbox({ onAct });

    await user.click(
      within(row("Investigate the stall")).getByRole("button", {
        name: "Acknowledge",
      }),
    );

    expect(api).toHaveBeenCalledWith("/api/tasks/4/close", { method: "POST" });
  });

  it("retries a failed task from its own row", async () => {
    const onAct = vi.fn((mutate) => mutate());
    const user = userEvent.setup();
    renderInbox({ onAct });

    await user.click(
      within(row("Rotate the signing key")).getByRole("button", {
        name: "Retry",
      }),
    );

    expect(api).toHaveBeenCalledWith("/api/tasks/5/retry", { method: "POST" });
  });

  it("asks before closing, and does nothing when declined", async () => {
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
    const onAct = vi.fn();
    const user = userEvent.setup();
    renderInbox({ onAct });

    await user.click(
      within(row("Rotate the signing key")).getByRole("button", {
        name: "Close",
      }),
    );

    expect(confirmSpy).toHaveBeenCalled();
    expect(onAct).not.toHaveBeenCalled();
  });

  // "Not now" is the third answer to a proposal, and the one that keeps
  // the other two honest.
  it("defers a proposal from its own row", async () => {
    const onAct = vi.fn((mutate) => mutate());
    const user = userEvent.setup();
    renderInbox({ onAct });

    await user.click(
      within(row("Add a dark mode toggle")).getByRole("button", {
        name: "Defer",
      }),
    );

    expect(api).toHaveBeenCalledWith("/api/tasks/1/defer", { method: "POST" });
  });

  it("leaves a deferred task off the page altogether", () => {
    renderInbox({
      tasks: [task({ id: "9", title: "Later, maybe", state: "deferred" })],
    });
    expect(screen.queryByText("Later, maybe")).not.toBeInTheDocument();
    expect(screen.getByText(/Nothing is waiting on you/)).toBeInTheDocument();
  });

  it("closes a proposal once the decline is confirmed", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    const onAct = vi.fn((mutate) => mutate());
    const user = userEvent.setup();
    renderInbox({ onAct });

    await user.click(
      within(row("Add a dark mode toggle")).getByRole("button", {
        name: "Decline",
      }),
    );

    expect(api).toHaveBeenCalledWith("/api/tasks/1/close", { method: "POST" });
  });

  it("links a merge the queue gave up on straight to its pull request", () => {
    renderInbox();
    const link = within(row("Add pagination to the tasks API")).getByRole(
      "link",
      { name: "Open pull request" },
    );
    expect(link).toHaveAttribute(
      "href",
      "https://github.com/acme/widgets/pull/13",
    );
  });

  // grain/task-393: a merge-queue wait the stall detector has given up
  // believing in is the one thing in this inbox nobody is told about
  // anywhere else -- the queue files no comment, because as far as it is
  // concerned it is still driving. The row says the queue's own note,
  // since that is what names the thing to go and look at.
  it("carries a stalled merge under a heading of its own, with the queue's note", () => {
    const { container } = renderInbox();
    const stalled = row("Split the sandbox pool");

    expect(
      stalled.closest(".inbox-group").querySelector("h3"),
    ).toHaveTextContent("Look at a stalled merge");
    expect(stalled.querySelector(".inbox-note")).toHaveTextContent(
      "Waiting on check go on acme/widgets#14",
    );
    // Only the groups that have something to say carry one: this one,
    // and the two asked groups, whose note says which ask a row is.
    const noted = new Set(
      [...container.querySelectorAll(".inbox-note")].map(
        (n) => n.closest(".inbox-group").querySelector("h3").textContent,
      ),
    );
    for (const title of noted)
      expect([
        "Look at a stalled merge",
        "Asked: reply",
        "Asked: submit for merge",
      ]).toContain(title);
    expect(
      stalled.closest(".inbox-group").querySelectorAll(".inbox-note"),
    ).toHaveLength(1);
  });

  // A clean change GitHub will not merge until somebody approves it is
  // neither blocked nor stalled, and nothing in grain gives the approval:
  // without a group of its own it was a row in the task list and nowhere
  // else.
  it("carries a change waiting on a GitHub approval, with the queue's note and its pull request", () => {
    const { container } = renderInbox({
      tasks: [
        task({
          id: "14",
          title: "Move the billing exporter onto the new schema",
          state: "completed",
          pullRequest: "acme/widgets#17",
          mergeQueueState: "waiting_github_approval",
          mergeQueueNote:
            "GitHub requires an approving review on https://github.com/acme/widgets/pull/17",
        }),
        task({
          id: "16",
          title: "Closed while waiting for approval",
          state: "closed",
          pullRequest: "acme/widgets#19",
          mergeQueueState: "waiting_github_approval",
        }),
      ],
    });

    expect(container.querySelectorAll(".inbox-item")).toHaveLength(1);
    const item = row("Move the billing exporter onto the new schema");
    expect(item.closest(".inbox-group").querySelector("h3")).toHaveTextContent(
      "Approve on GitHub",
    );
    expect(item.querySelector(".inbox-note")).toHaveTextContent(
      "GitHub requires an approving review",
    );
    expect(
      within(item).getByRole("link", { name: "Open pull request" }),
    ).toHaveAttribute("href", "https://github.com/acme/widgets/pull/17");
    // The page the note sends somebody to is a link, as in the task pane
    // (docs/test-runs/2026-10-05-task-intake-inbox-lifecycle.md, bug 3).
    expect(
      within(item.querySelector(".inbox-note")).getByRole("link", {
        name: "https://github.com/acme/widgets/pull/17",
      }),
    ).toHaveAttribute("href", "https://github.com/acme/widgets/pull/17");
    expect(
      screen.queryByText("Waiting on the base to go green"),
    ).not.toBeInTheDocument();
  });

  // The approval is what it is waiting for, so a stall flag on the same
  // entry does not move it under "Look at a stalled merge".
  it("keeps an approval wait the stall detector flagged under Approve on GitHub", () => {
    const { container } = renderInbox({
      tasks: [
        task({
          id: "17",
          title: "Approval nobody has given",
          state: "completed",
          pullRequest: "acme/widgets#20",
          mergeQueueState: "waiting_github_approval",
          mergeQueueStalledAt: "2026-09-01T10:00:00Z",
        }),
      ],
    });
    expect(
      [...container.querySelectorAll(".inbox-group-header h3")].map(
        (h) => h.textContent,
      ),
    ).toEqual(["Approve on GitHub"]);
  });

  // Neither of its answers is a request: there is nothing to retry and
  // nothing to unblock, so both buttons are ways of going and looking.
  it("offers a stalled merge the pull request and the task, and nothing to press", async () => {
    const onOpenTask = vi.fn();
    const onAct = vi.fn();
    const user = userEvent.setup();
    renderInbox({ onOpenTask, onAct });
    const stalled = row("Split the sandbox pool");

    expect(
      within(stalled).getByRole("link", { name: "Open pull request" }),
    ).toHaveAttribute("href", "https://github.com/acme/widgets/pull/14");
    expect(
      within(stalled).queryByRole("button", { name: "Retry merge" }),
    ).not.toBeInTheDocument();

    await user.click(
      within(stalled).getByRole("button", { name: "Open task" }),
    );

    expect(onOpenTask).toHaveBeenCalledWith("10");
    expect(onAct).not.toHaveBeenCalled();
  });

  // Once the queue has given up and said why, that is the fact about the
  // row: it goes under "Unblock a merge" with its comment and its Retry,
  // and the stall it stood through on the way is not a second row.
  it("leaves a merge that is both blocked and stalled where the comment is", () => {
    const { container } = renderInbox({
      tasks: [
        task({
          id: "11",
          title: "Rework the cache key",
          state: "completed",
          pullRequest: "acme/widgets#15",
          mergeQueueState: "merge_failed",
          mergeQueueStalledAt: "2026-09-01T10:00:00Z",
          mergeQueueBlockedAt: "2026-09-01T11:00:00Z",
        }),
      ],
    });

    expect(container.querySelectorAll(".inbox-item")).toHaveLength(1);
    expect(
      [...container.querySelectorAll(".inbox-group-header h3")].map(
        (h) => h.textContent,
      ),
    ).toEqual(["Unblock a merge"]);
  });

  // ...and that Retry is the merge_failed sub-case's own control, which
  // the stalled group never gets to offer for a change this group has
  // already claimed. Found missing in the 2026-10-26 walkthrough.
  it("offers Retry merge on a merge the queue gave up on, once", async () => {
    const onAct = vi.fn();
    const user = userEvent.setup();
    renderInbox({
      onAct,
      tasks: [
        task({
          id: "11",
          title: "Rework the cache key",
          state: "completed",
          pullRequest: "acme/widgets#15",
          mergeQueueState: "merge_failed",
          mergeQueueBlockedAt: "2026-09-01T11:00:00Z",
          stall: "this has stood here for more than 3d",
          reason: { reason: "completed/merge_failed", control: "Retry merge" },
          mergeQueueReason: {
            reason: "merge_failed/given_up",
            control: "Retry merge",
          },
        }),
        task({
          id: "12",
          title: "Withdrawn by grain",
          state: "completed",
          pullRequest: "acme/widgets#16",
          mergeQueueState: "merge_failed",
          mergeQueueBlockedAt: "2026-09-01T11:00:00Z",
        }),
      ],
    });

    const item = row("Rework the cache key");
    expect(
      within(item).getAllByRole("button", { name: "Retry merge" }),
    ).toHaveLength(1);
    expect(
      within(row("Withdrawn by grain")).queryByRole("button", {
        name: "Retry merge",
      }),
    ).toBeNull();
    await user.click(within(item).getByRole("button", { name: "Retry merge" }));
    expect(onAct).toHaveBeenCalledTimes(1);
    await onAct.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/11/retry-merge", {
      method: "POST",
    });
  });

  // grain's own escalations are raised as notifications now
  // (docs/design/durable-tasks.md, DT-5), but the rows they map keep
  // appearing until they are removed: a stalled merge shows both its
  // pipeline.merge-stalled notification and its "Unblock a merge" row.
  it("keeps a stalled merge's row beside its merge-stalled notification", () => {
    const { container } = renderInbox({
      tasks: [
        task({
          id: "11",
          title: "Rework the cache key",
          state: "completed",
          pullRequest: "acme/widgets#15",
          mergeQueueState: "merge_failed",
          mergeQueueStalledAt: "2026-09-01T10:00:00Z",
          mergeQueueBlockedAt: "2026-09-01T11:00:00Z",
        }),
      ],
      notifications: [
        {
          id: 1,
          subject: "pipeline.merge-stalled:11",
          family: "pipeline.merge-stalled",
          severity: "warning",
          title: "The merge of task 11 has stalled",
          body: "acme/widgets#15 has been in `merging` past its patience.",
          taskId: "11",
          raisedAt: "2026-09-01T11:00:00Z",
          raiseCount: 1,
          actions: ["retry-merge", "open-task"],
        },
      ],
    });

    expect(screen.getAllByTestId("inbox-notification")).toHaveLength(1);
    expect(
      screen.getByText("The merge of task 11 has stalled"),
    ).toBeInTheDocument();
    expect(row("Rework the cache key")).not.toBeNull();
    expect(
      [...container.querySelectorAll(".inbox-group-header h3")].map(
        (h) => h.textContent,
      ),
    ).toEqual(expect.arrayContaining(["Notifications", "Unblock a merge"]));
  });

  it("opens a task's own pane when its row is clicked", async () => {
    const onOpenTask = vi.fn();
    const user = userEvent.setup();
    renderInbox({ onOpenTask });

    await user.click(screen.getByText("Rotate the signing key"));

    expect(onOpenTask).toHaveBeenCalledWith("5");
  });

  // A change held on its review's open questions is a question to its
  // owner (orchestrator's reviewquestions.go), not a bare Submit: it is
  // grouped apart, offers Reply, and keeps Submit only as "Ship as is".
  it("shows a change held on review questions as a decision, not a bare Submit", async () => {
    api.mockResolvedValue({
      id: "q1",
      comments: [
        {
          id: 1,
          authorKind: "automation",
          body: "**Review needs your decision.** Should the core build publish the google image?",
        },
      ],
    });
    renderInbox({
      tasks: [
        task({
          id: "q1",
          title: "Publish both images",
          state: "awaiting_submit",
          pullRequest: "acme/widgets#20",
          reviewQuestions: "21",
        }),
        task({
          id: "s1",
          title: "Plain change",
          state: "awaiting_submit",
          pullRequest: "acme/widgets#22",
        }),
      ],
    });

    const heading = screen.getByRole("heading", {
      name: "Review needs your decision",
    });
    const group = heading.closest("section");
    expect(within(group).getByText("Publish both images")).toBeInTheDocument();
    expect(within(group).queryByText("Plain change")).not.toBeInTheDocument();
    const held = row("Publish both images");
    expect(
      within(held).getByRole("button", { name: "Reply" }),
    ).toBeInTheDocument();
    expect(
      within(held).getByRole("button", { name: "Ship as is" }),
    ).toBeInTheDocument();
    expect(
      within(held).queryByRole("button", { name: "Submit" }),
    ).not.toBeInTheDocument();
    expect(
      await within(held).findByText(/Should the core build publish/),
    ).toBeInTheDocument();
    expect(
      within(row("Plain change")).getByRole("button", { name: "Submit" }),
    ).toBeInTheDocument();
  });

  // The whole-group buttons: the same answer given to every task under
  // one heading, which is what an inbox of forty proposals actually
  // needs.
  describe("answering a whole group at once", () => {
    // Two of everything the inbox can answer in bulk, so every group has
    // a "... all" to offer.
    const crowd = [
      task({ id: "p1", title: "Proposal one", state: "proposed" }),
      task({ id: "p2", title: "Proposal two", state: "proposed" }),
      task({ id: "s1", title: "Change one", state: "awaiting_submit" }),
      task({ id: "s2", title: "Change two", state: "awaiting_submit" }),
      task({ id: "a1", title: "Answer one", state: "awaiting_reply" }),
      task({ id: "a2", title: "Answer two", state: "awaiting_reply" }),
      task({ id: "f1", title: "Failure one", state: "failed" }),
      task({ id: "f2", title: "Failure two", state: "failed" }),
    ];

    // onActBatch as App wires it: fire the mutation at every id, so the
    // assertions below can be about the requests that went out.
    const batched = () => vi.fn((ids, mutate) => Promise.all(ids.map(mutate)));

    it("offers one button per answer a whole group can be given", () => {
      renderInbox({ tasks: crowd });

      expect(
        screen.getByRole("button", { name: "Approve all (2)" }),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "Defer all (2)" }),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "Submit all (2)" }),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "Acknowledge all (2)" }),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: "Retry all (2)" }),
      ).toBeInTheDocument();
    });

    // Closing a stack of tasks on one click is not an inbox action: the
    // row's own Close, with its own confirm, is the way to give up on
    // work. Nor is a link a group can be given all at once.
    it("offers no whole-group Close, Decline or link", () => {
      renderInbox({
        tasks: [
          ...crowd,
          task({
            id: "m1",
            title: "Blocked one",
            state: "completed",
            pullRequest: "acme/widgets#1",
            mergeQueueBlockedAt: "2026-09-01T10:00:00Z",
          }),
          task({
            id: "m2",
            title: "Blocked two",
            state: "completed",
            pullRequest: "acme/widgets#2",
            mergeQueueBlockedAt: "2026-09-01T10:00:00Z",
          }),
        ],
      });

      expect(screen.queryByText(/Close all/)).not.toBeInTheDocument();
      expect(screen.queryByText(/Decline all/)).not.toBeInTheDocument();
      expect(
        screen.queryByText(/Open pull request all/),
      ).not.toBeInTheDocument();
    });

    // A group of one already has its answer under the reader's cursor,
    // on the row itself.
    it("offers nothing on a group holding a single task", () => {
      const { container } = renderInbox();
      expect(
        container.querySelectorAll(".inbox-group-actions button"),
      ).toHaveLength(0);
    });

    it("approves every proposal once the confirm is accepted", async () => {
      const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
      const onActBatch = batched();
      const user = userEvent.setup();
      renderInbox({ tasks: crowd, onActBatch });

      await user.click(screen.getByRole("button", { name: "Approve all (2)" }));

      expect(confirmSpy).toHaveBeenCalledWith(
        expect.stringContaining("Approve all 2 proposals?"),
      );
      expect(onActBatch).toHaveBeenCalledWith(
        ["p1", "p2"],
        expect.any(Function),
      );
      expect(api).toHaveBeenCalledWith("/api/tasks/p1/approve", {
        method: "POST",
      });
      expect(api).toHaveBeenCalledWith("/api/tasks/p2/approve", {
        method: "POST",
      });
    });

    it("submits every finished change once the confirm is accepted", async () => {
      vi.spyOn(window, "confirm").mockReturnValue(true);
      const onActBatch = batched();
      const user = userEvent.setup();
      renderInbox({ tasks: crowd, onActBatch });

      await user.click(screen.getByRole("button", { name: "Submit all (2)" }));

      expect(api).toHaveBeenCalledWith("/api/tasks/s1/submit", {
        method: "POST",
      });
      expect(api).toHaveBeenCalledWith("/api/tasks/s2/submit", {
        method: "POST",
      });
    });

    // Every one of them asks first, however harmless the single-row
    // version of the same button is: the reader has by definition not
    // opened these tasks, so the confirm is the only place the count is
    // said out loud.
    it("does nothing at all when the confirm is declined", async () => {
      const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
      const onActBatch = batched();
      const user = userEvent.setup();
      renderInbox({ tasks: crowd, onActBatch });

      for (const name of [
        "Approve all (2)",
        "Defer all (2)",
        "Submit all (2)",
        "Acknowledge all (2)",
        "Retry all (2)",
      ]) {
        await user.click(screen.getByRole("button", { name }));
      }

      expect(confirmSpy).toHaveBeenCalledTimes(5);
      expect(onActBatch).not.toHaveBeenCalled();
      // The page's own poll of waiting local-script calls and pre-fetched
      // question details are reads, not answers to anything.
      expect(
        api.mock.calls.filter(
          ([path, opts]) =>
            opts?.method ||
            (path !== "/api/local-script-approvals" &&
              !path.startsWith("/api/tasks/")),
        ),
      ).toEqual([]);
    });

    it("acknowledges, retries and defers whole groups too", async () => {
      vi.spyOn(window, "confirm").mockReturnValue(true);
      const onActBatch = batched();
      const user = userEvent.setup();
      renderInbox({ tasks: crowd, onActBatch });

      await user.click(
        screen.getByRole("button", { name: "Acknowledge all (2)" }),
      );
      await user.click(screen.getByRole("button", { name: "Retry all (2)" }));
      await user.click(screen.getByRole("button", { name: "Defer all (2)" }));

      expect(api).toHaveBeenCalledWith("/api/tasks/a1/close", {
        method: "POST",
      });
      expect(api).toHaveBeenCalledWith("/api/tasks/f2/retry", {
        method: "POST",
      });
      expect(api).toHaveBeenCalledWith("/api/tasks/p1/defer", {
        method: "POST",
      });
    });
  });

  describe("replying to a parked question", () => {
    it("shows only the question's summary on the row, and the whole question on expanding it", async () => {
      api.mockImplementation(async (path) =>
        path === "/api/tasks/4"
          ? {
              id: "4",
              state: "awaiting_reply",
              questionSummary: "Pick the base branch for this change",
              comments: [
                { id: 1, body: "filed it" },
                {
                  id: 2,
                  summary: "Pick the base branch for this change",
                  body: "Which **base branch** should this target?",
                  attachments: [
                    {
                      id: 7,
                      filename: "branches.txt",
                      contentType: "text/plain",
                      size: 12,
                    },
                  ],
                },
              ],
            }
          : null,
      );
      const user = userEvent.setup();
      renderInbox();

      const item = row("Investigate the stall");
      const toggle = await within(item).findByRole("button", {
        name: /Pick the base branch for this change/,
      });
      expect(toggle).toHaveAttribute("aria-expanded", "false");
      expect(within(item).queryByText(/Which/)).not.toBeInTheDocument();
      expect(within(item).queryByText("branches.txt")).not.toBeInTheDocument();
      expect(
        within(item).queryByLabelText("Reply to 4"),
      ).not.toBeInTheDocument();

      await user.click(toggle);
      expect(toggle).toHaveAttribute("aria-expanded", "true");
      expect(item.querySelector(".inbox-question strong")).toHaveTextContent(
        "base branch",
      );
      expect(within(item).getByText(/branches\.txt/)).toBeInTheDocument();
      expect(
        document.getElementById(toggle.getAttribute("aria-controls")),
      ).toBeVisible();

      await user.click(toggle);
      expect(toggle).toHaveAttribute("aria-expanded", "false");
      expect(within(item).queryByText(/Which/)).not.toBeInTheDocument();
    });

    it("expands and collapses the question from the keyboard", async () => {
      api.mockImplementation(async (path) =>
        path === "/api/tasks/4"
          ? {
              id: "4",
              state: "awaiting_reply",
              comments: [
                {
                  id: 2,
                  summary: "Keep the v1 API or remove it?",
                  body: "Long reasoning about the v1 API.",
                },
              ],
            }
          : null,
      );
      const user = userEvent.setup();
      renderInbox();

      const item = row("Investigate the stall");
      const toggle = await within(item).findByRole("button", {
        name: /Keep the v1 API or remove it\?/,
      });
      toggle.focus();
      await user.keyboard("{Enter}");
      expect(toggle).toHaveAttribute("aria-expanded", "true");
      expect(
        within(item).getByText("Long reasoning about the v1 API."),
      ).toBeInTheDocument();
      await user.keyboard(" ");
      expect(toggle).toHaveAttribute("aria-expanded", "false");
    });

    it("falls back to the question's first line for a question asked without a summary", async () => {
      api.mockImplementation(async (path) =>
        path === "/api/tasks/4"
          ? {
              id: "4",
              state: "awaiting_reply",
              comments: [
                {
                  id: 2,
                  body: "## Which base branch?\n\nThe long version.",
                },
              ],
            }
          : null,
      );
      renderInbox();

      const item = row("Investigate the stall");
      expect(
        await within(item).findByRole("button", {
          name: /Which base branch\?/,
        }),
      ).toHaveAttribute("aria-expanded", "false");
      expect(
        within(item).queryByText("The long version."),
      ).not.toBeInTheDocument();
    });

    it("shows the question and posts the answer as a comment", async () => {
      api.mockImplementation(async (path) =>
        path === "/api/tasks/4"
          ? {
              id: "4",
              state: "awaiting_reply",
              comments: [
                { id: 1, body: "filed it" },
                { id: 2, body: "Which base branch should this target?" },
              ],
            }
          : null,
      );
      const onAct = vi.fn((mutate) => mutate());
      const user = userEvent.setup();
      renderInbox({ onAct });

      await user.click(
        within(row("Investigate the stall")).getByRole("button", {
          name: "Reply",
        }),
      );
      expect(
        await screen.findByText("Which base branch should this target?", {
          selector: ".inbox-question *",
        }),
      ).toBeInTheDocument();

      await user.type(
        screen.getByLabelText("Reply to 4"),
        "release-1.4, please",
      );
      await user.click(screen.getByRole("button", { name: "Send" }));

      expect(api).toHaveBeenCalledWith("/api/tasks/4/comments", {
        method: "POST",
        body: JSON.stringify({ body: "release-1.4, please" }),
      });
    });

    // grain/task-1699: a reply the server refused used to close the box
    // anyway, unmounting it and the draft in it along with the error.
    it("keeps the box and the draft open when the reply does not land", async () => {
      api.mockResolvedValue({
        id: "4",
        state: "awaiting_reply",
        comments: [{ id: 2, body: "Which base branch should this target?" }],
      });
      // What App's inboxAct resolves to once it has reported a failure.
      const onAct = vi.fn().mockResolvedValue(false);
      const user = userEvent.setup();
      renderInbox({ onAct });

      await user.click(
        within(row("Investigate the stall")).getByRole("button", {
          name: "Reply",
        }),
      );
      await screen.findByText("Which base branch should this target?", {
        selector: ".inbox-question *",
      });
      await user.type(
        screen.getByLabelText("Reply to 4"),
        "release-1.4, please",
      );
      await user.click(screen.getByRole("button", { name: "Send" }));

      expect(onAct).toHaveBeenCalledTimes(1);
      expect(screen.getByLabelText("Reply to 4")).toHaveValue(
        "release-1.4, please",
      );
    });

    it("ignores duplicate reply submissions while one is already in flight", async () => {
      api.mockResolvedValue({
        id: "4",
        state: "awaiting_reply",
        comments: [
          { id: 1, body: "filed it" },
          { id: 2, body: "Which base branch should this target?" },
        ],
      });
      // Never settles: the first reply is still in flight when the
      // second click lands.
      const onAct = vi.fn(() => new Promise(() => {}));
      const user = userEvent.setup();
      renderInbox({ onAct });

      await user.click(
        within(row("Investigate the stall")).getByRole("button", {
          name: "Reply",
        }),
      );
      expect(
        await screen.findByText("Which base branch should this target?", {
          selector: ".inbox-question *",
        }),
      ).toBeInTheDocument();

      await user.type(
        screen.getByLabelText("Reply to 4"),
        "release-1.4, please",
      );
      const button = screen.getByRole("button", { name: "Send" });
      await user.click(button);
      await user.click(button);

      expect(onAct).toHaveBeenCalledTimes(1);
    });

    // Send used to stay enabled over an empty box and do nothing when
    // pressed -- no request, no message, no sign it had been refused.
    it("offers no Send until the box holds more than whitespace", async () => {
      api.mockResolvedValue({ id: "4", comments: [] });
      const onAct = vi.fn();
      const user = userEvent.setup();
      renderInbox({ onAct });

      await user.click(
        within(row("Investigate the stall")).getByRole("button", {
          name: "Reply",
        }),
      );
      const send = await screen.findByRole("button", { name: "Send" });
      expect(send).toBeDisabled();

      const box = screen.getByLabelText("Reply to 4");
      await user.type(box, "   ");
      expect(send).toBeDisabled();

      await user.type(box, "go ahead");
      expect(send).toBeEnabled();

      await user.clear(box);
      expect(send).toBeDisabled();
      expect(onAct).not.toHaveBeenCalled();
    });

    it("closes the box again when Cancel is clicked", async () => {
      api.mockResolvedValue({ id: "4", comments: [] });
      const user = userEvent.setup();
      renderInbox();

      const line = within(row("Investigate the stall"));
      await user.click(line.getByRole("button", { name: "Reply" }));
      await user.click(await line.findByRole("button", { name: "Cancel" }));

      expect(
        screen.queryByRole("button", { name: "Send" }),
      ).not.toBeInTheDocument();
    });

    // A run parked on request_secret is in the same state as one parked
    // on a question, and a reply is a comment -- so the box that would
    // invite somebody to paste a credential into the conversation is not
    // offered at all.
    it("offers no reply box for a run that asked for a secret", async () => {
      api.mockResolvedValue({
        id: "4",
        state: "awaiting_reply",
        comments: [{ id: 1, body: "I need the deploy key" }],
        pendingSecret: {
          name: "deploy-key",
          secret: "deploy-key",
          key: "value",
          set: false,
        },
      });
      const onOpenTask = vi.fn();
      const user = userEvent.setup();
      renderInbox({ onOpenTask });

      await user.click(
        within(row("Investigate the stall")).getByRole("button", {
          name: "Reply",
        }),
      );

      expect(
        await screen.findByText(/asked for a credential \(deploy-key\)/),
      ).toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: "Send" }),
      ).not.toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: "Open the task" }));
      expect(onOpenTask).toHaveBeenCalledWith("4");
    });

    it("falls back to the task's own pane when the detail cannot be read", async () => {
      api.mockRejectedValue(new Error("nope"));
      const showError = vi.fn();
      const user = userEvent.setup();
      renderInbox({ showError });

      await user.click(
        within(row("Investigate the stall")).getByRole("button", {
          name: "Reply",
        }),
      );

      expect(
        await screen.findByRole("button", { name: "Open the task to reply" }),
      ).toBeInTheDocument();
      await waitFor(() => expect(showError).toHaveBeenCalled());

      // Closing the failed reply box does not fire another detail request.
      const callsBeforeCancel = api.mock.calls.length;
      await user.click(
        within(row("Investigate the stall")).getByRole("button", {
          name: "Cancel",
        }),
      );
      expect(api).toHaveBeenCalledTimes(callsBeforeCancel);
    });

    it("links task references in automation questions and leaves modified clicks to the browser", async () => {
      api.mockResolvedValue({
        id: "4",
        state: "awaiting_reply",
        comments: [
          {
            id: 1,
            author: "grain",
            authorKind: "automation",
            body: "Should this wait on task 91 first?",
          },
        ],
      });
      const onOpenTask = vi.fn();
      const user = userEvent.setup();
      renderInbox({ onOpenTask });

      const item = row("Investigate the stall");
      await user.click(
        await within(item).findByRole("button", { name: /Show question/ }),
      );
      const link = await within(item).findByRole("link", { name: "task 91" });
      expect(link).toHaveAttribute("href", "#task-91");

      await user.keyboard("{Control>}");
      await user.click(link);
      await user.keyboard("{/Control}");
      expect(onOpenTask).not.toHaveBeenCalled();

      await user.click(link);
      expect(onOpenTask).toHaveBeenCalledWith("91");
    });
  });
  // grain/task-470: the one row on this page that is not a task. A base
  // branch nothing is judging has no task to hang on -- it is a
  // repository, and the two surfaces it already reached (the System pane
  // and one repository's own settings) are places somebody only opens
  // once they already suspect something.
  describe("a base branch nothing is judging", () => {
    const unjudged = {
      repo: "acme/widgets",
      branch: "main",
      judged: false,
      reason: "grain-ci-off",
      runInFlight: false,
      mergesWaiting: true,
      detail:
        "Nothing is testing acme/widgets's base branch main, and the merge" +
        " queue is merging onto it anyway.",
    };
    const late = {
      repo: "acme/other",
      branch: "trunk",
      judged: true,
      reason: "no-run-started",
      runInFlight: false,
      mergesWaiting: true,
      lastRunAgeMS: 33 * 60 * 60 * 1000,
      detail:
        "A merge has landed on acme/other trunk and no base-branch run has" +
        " been started for it.",
    };

    it("does not render base branch gates in the inbox", () => {
      renderInbox({
        tasks: [task({ id: "4", state: "awaiting_reply" })],
        gates: [unjudged, late],
      });
      expect(
        screen.queryByText("Look at a base branch nothing is judging"),
      ).not.toBeInTheDocument();
      expect(screen.queryByText("acme/widgets")).not.toBeInTheDocument();
      expect(screen.queryByText("acme/other")).not.toBeInTheDocument();
    });

    it("does not count base branch gates towards the inbox header count", () => {
      const { container } = renderInbox({
        tasks: [task({ id: "4", state: "awaiting_reply" })],
        gates: [unjudged, late],
      });
      expect(
        container.querySelector(".content-header .count"),
      ).toHaveTextContent("1");
    });

    it("shows empty state when only gates exist and no tasks wait", () => {
      renderInbox({ tasks: [task({ state: "running" })], gates: [unjudged] });
      expect(screen.getByText(/Nothing is waiting on you/)).toHaveTextContent(
        "Every task is either running, waiting or done.",
      );
      expect(
        screen.queryByText(/base branch nothing is judging/),
      ).not.toBeInTheDocument();
    });
  });

  it("shows a waiting base-branch repair item with reason and Submit / Force merge buttons", async () => {
    const user = userEvent.setup();
    const onAct = vi.fn((fn) => fn());
    vi.spyOn(window, "prompt").mockReturnValue("unblock main");

    const repairTask = task({
      id: "323",
      title: "Fix grain CI on bwsalmon/grain main",
      state: "awaiting_submit",
      autoMerge: false,
      pullRequest: "bwsalmon/grain#4495",
      redBaseBranch: "main",
      securityHold: "trust-boundary/v4",
      securityReviewTask: "376",
    });

    renderInbox({ tasks: [repairTask], onAct });

    const item = row("Fix grain CI on bwsalmon/grain main");
    expect(item.closest(".inbox-group").querySelector("h3")).toHaveTextContent(
      "Base-branch repair waiting",
    );
    expect(item.querySelector(".inbox-note")).toHaveTextContent(
      "`main` is red — repair #323 is on security hold (trust-boundary/v4) for security review #376 — waiting for Submit",
    );

    await user.click(within(item).getByRole("button", { name: "Submit" }));
    expect(api).toHaveBeenCalledWith("/api/tasks/323/submit", {
      method: "POST",
    });

    await user.click(within(item).getByRole("button", { name: "Force merge" }));
    expect(api).toHaveBeenCalledWith("/api/tasks/323/force-merge", {
      method: "POST",
      body: JSON.stringify({ reason: "unblock main" }),
    });
  });
});
