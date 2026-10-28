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

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import DetailOverlay from "../DetailOverlay.jsx";
import api from "../../api.js";
import { TimeZoneProvider } from "../../TimeZoneContext.jsx";
import { resetDetailMocks, baseTask, config } from "./testFixtures.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../../attachments.js", () => ({ default: vi.fn() }));

describe("DetailOverlay actions", () => {
  afterEach(resetDetailMocks);

  it("shows an Approve button for a proposed task, wired to the approve endpoint", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, state: "proposed" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Approve" }));

    expect(act).toHaveBeenCalledWith(expect.any(Function), "12");
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/approve", {
      method: "POST",
    });
  });

  it("shows a Withdraw approval button for a queued task, wired to the withdraw endpoint", async () => {
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

    await user.click(screen.getByRole("button", { name: "Withdraw approval" }));

    expect(act).toHaveBeenCalledWith(expect.any(Function), "12");
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/withdraw-approval", {
      method: "POST",
    });
  });

  // The states Client.WithdrawApproval refuses never get the button:
  // a proposed task has no approval to withdraw, and a running one is
  // stopped with Cancel instead.
  it("offers no Withdraw approval button on a task that is not queued", () => {
    const { rerender } = render(
      <DetailOverlay
        task={{ ...baseTask, state: "proposed" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Withdraw approval" }),
    ).not.toBeInTheDocument();

    rerender(
      <DetailOverlay
        task={{ ...baseTask, state: "running" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Withdraw approval" }),
    ).not.toBeInTheDocument();
  });

  // Defer and Undefer, on exactly the states ui.Client.Defer accepts:
  // both of the ones a task can be in before it has ever run, and the
  // deferred state itself. Deferring keeps the approval, which is why a
  // queued task gets the button too rather than being told to withdraw
  // first.
  it("shows a Defer button on a task that has not run, wired to the defer endpoint", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(
      <DetailOverlay
        task={{ ...baseTask, state: "proposed" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Defer" }));
    expect(act).toHaveBeenCalledWith(expect.any(Function), "12");
    act.mock.calls[0][0]();
    // An empty wake date is the deferral this button has always made:
    // aside until somebody presses Undefer (ui.DeferOptions).
    expect(api).toHaveBeenCalledWith("/api/tasks/12/defer", {
      method: "POST",
      body: JSON.stringify({ deferredUntil: "" }),
    });

    rerender(
      <DetailOverlay
        task={baseTask}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );
    expect(screen.getByRole("button", { name: "Defer" })).toBeInTheDocument();
  });

  // The wake date beside the button (model.Task.DeferredUntil): a day
  // chosen here travels as the instant that day starts, where this
  // browser is, because "not before the 14th" is what somebody typing a
  // date means.
  it("sends the day a wake date names as its own local midnight", async () => {
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

    const field = screen.getByLabelText("Until (optional)");
    await user.type(field, "2026-09-14");
    await user.click(screen.getByRole("button", { name: "Defer" }));
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/defer", {
      method: "POST",
      body: JSON.stringify({
        deferredUntil: new Date("2026-09-14T00:00").toISOString(),
      }),
    });
  });

  // With a deployment zone set, the day is read in that zone, which is
  // the one the "Picks itself back up on" note prints it in. Read in
  // the browser's zone instead, a pick of the 31st from Tokyo under a
  // UTC deployment came back as "12/30/2026, 3:00:00 PM".
  it("reads a wake date in the deployment's zone, the one its note prints it in", async () => {
    vi.useFakeTimers({
      now: new Date("2026-10-04T03:00:00Z"),
      toFake: ["Date"],
    });
    try {
      const act = vi.fn();
      const user = userEvent.setup();
      render(
        <TimeZoneProvider zone="Asia/Tokyo">
          <DetailOverlay
            task={baseTask}
            tasks={[]}
            config={config}
            onClose={() => {}}
            onOpenTask={() => {}}
            act={act}
          />
        </TimeZoneProvider>,
      );

      const field = screen.getByLabelText("Until (optional)");
      // Noon on the 4th in Tokyo: the earliest day offered is the 5th
      // there, whatever day it is where the test runs.
      expect(field).toHaveAttribute("min", "2026-10-05");
      await user.type(field, "2026-12-31");
      await user.click(screen.getByRole("button", { name: "Defer" }));
      act.mock.calls[0][0]();
      expect(api).toHaveBeenCalledWith("/api/tasks/12/defer", {
        method: "POST",
        body: JSON.stringify({ deferredUntil: "2026-12-30T15:00:00.000Z" }),
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("prints the wake time a zoned deferral was sent as on the day that was picked", () => {
    render(
      <TimeZoneProvider zone="Asia/Tokyo">
        <DetailOverlay
          task={{
            ...baseTask,
            state: "deferred",
            deferredUntil: "2026-12-30T15:00:00.000Z",
          }}
          tasks={[]}
          config={config}
          onClose={() => {}}
          onOpenTask={() => {}}
          act={vi.fn()}
        />
      </TimeZoneProvider>,
    );
    expect(screen.getByText(/Picks itself back up on/)).toHaveTextContent(
      /12\/31\/2026, 12:00:00\s?AM/,
    );
  });

  it("says when a deferred task picks itself back up, and nothing when it never will", () => {
    const { rerender } = render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "deferred",
          deferredUntil: "2026-09-14T00:00:00Z",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText(/Picks itself back up on/)).toBeInTheDocument();
    expect(
      screen.getByText(/back into the proposals for someone to approve/),
    ).toBeInTheDocument();

    // An approved one wakes onto the queue instead, and says so: the
    // state reads "deferred" either way.
    rerender(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "deferred",
          approved: true,
          deferredUntil: "2026-09-14T00:00:00Z",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText(/, onto the queue\./)).toBeInTheDocument();

    rerender(
      <DetailOverlay
        task={{ ...baseTask, state: "deferred" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByText(/Picks itself back up on/),
    ).not.toBeInTheDocument();
  });

  // Approving or withdrawing approval on a deferred task leaves it
  // deferred and keeps its wake time, rather than requiring Undefer
  // first (which would clear deferredUntil).
  it("allows approving and withdrawing approval on a deferred task while preserving its wake time", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "deferred",
          approved: false,
          deferredUntil: "2026-09-14T00:00:00Z",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    expect(
      screen.getByText(/back into the proposals for someone to approve/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Withdraw approval" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Approve" }));
    expect(act).toHaveBeenCalledWith(expect.any(Function), "12");
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/approve", {
      method: "POST",
    });

    rerender(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "deferred",
          approved: true,
          deferredUntil: "2026-09-14T00:00:00Z",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    expect(screen.getByText(/, onto the queue\./)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Approve" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Withdraw approval" }));
    expect(act).toHaveBeenNthCalledWith(2, expect.any(Function), "12");
    act.mock.calls[1][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/withdraw-approval", {
      method: "POST",
    });
  });

  it("offers Undefer, and no Defer, on a deferred task", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, state: "deferred" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    expect(
      screen.queryByRole("button", { name: "Defer" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Undefer" }));
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/undefer", {
      method: "POST",
    });
  });

  it("offers no Defer button on a task whose run has already happened", () => {
    for (const state of ["running", "completed", "closed"]) {
      const { unmount } = render(
        <DetailOverlay
          task={{ ...baseTask, state }}
          tasks={[]}
          config={config}
          onClose={() => {}}
          onOpenTask={() => {}}
          act={vi.fn()}
        />,
      );
      expect(
        screen.queryByRole("button", { name: "Defer" }),
      ).not.toBeInTheDocument();
      unmount();
    }
  });

  it("shows a Submit button once a pull request exists and auto-merge is off", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, pullRequest: "acme/widgets#1" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Submit" }));

    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/submit", {
      method: "POST",
    });
  });

  // grain/task-182. App.jsx opens this pane from /tasks/:id on a mount
  // effect that waits on /api/tasks/:id and nothing else, while it holds
  // `config` as useState(null) until /api/config answers separately.
  // Nothing orders those two, so on a loaded machine the task can win and
  // this renders with no config at all -- which used to throw out of the
  // model row and take the whole tree with it. What that looked like from
  // outside was the pane simply not being there: two `ui e2e` runs went
  // red on two different specs, each asserting on an element of this pane
  // that "was not found", with a TypeError in the browser console nobody
  // was reading.
  it("renders before /api/config has answered", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, pullRequest: "acme/widgets#1" }}
        tasks={[]}
        config={null}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByRole("button", { name: "Submit" })).toBeInTheDocument();
    expect(screen.getByText("Fix the login bug")).toBeInTheDocument();
  });

  it("hides the Submit button once auto-merge is already on", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, pullRequest: "acme/widgets#1", autoMerge: true }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Submit" }),
    ).not.toBeInTheDocument();
  });

  // bwsalmon/agents#483: on a deployment whose GitHub credential can't
  // read pull request checks, Submit still flips autoMerge but nothing
  // ever merges -- without this warning that looked identical to Submit
  // doing nothing at all.
  it("warns that auto-merge is degraded once a task is submitted on a deployment that can't read checks", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, pullRequest: "acme/widgets#1", autoMerge: true }}
        tasks={[]}
        config={{ ...config, autoMergeDegraded: true }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.getByText(/can't read pull request checks/),
    ).toBeInTheDocument();
  });

  it("shows no auto-merge warning on a deployment that can read checks", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, pullRequest: "acme/widgets#1", autoMerge: true }}
        tasks={[]}
        config={{ ...config, autoMergeDegraded: false }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByText(/can't read pull request checks/),
    ).not.toBeInTheDocument();
  });

  it("shows no auto-merge warning before a task has been submitted, even when degraded", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, pullRequest: "acme/widgets#1", autoMerge: false }}
        tasks={[]}
        config={{ ...config, autoMergeDegraded: true }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByText(/can't read pull request checks/),
    ).not.toBeInTheDocument();
  });

  it("shows a Retry button for a failed task, wired to the retry endpoint", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, state: "failed" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Retry" }));

    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/retry", { method: "POST" });
  });

  it("shows Retry merge once the queue has given up, wired to the retry-merge endpoint", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
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
        act={act}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Retry merge" }));

    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/retry-merge", {
      method: "POST",
    });
  });

  // The button is the answer to having been given up on, so a pull
  // request still on the queue has nothing for it to do.
  it("shows no Retry merge for a task the queue is still driving", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "completed",
          pullRequest: "acme/widgets#42",
          autoMerge: true,
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Retry merge" }),
    ).not.toBeInTheDocument();
  });

  // The override of the queue rather than an appeal to it: one prompt,
  // the reason it asks for sent as the body, and nothing at all sent
  // when the prompt is dismissed (ui.Client.ForceMerge).
  it("forces a merge with the reason typed at the prompt", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    vi.spyOn(window, "prompt").mockReturnValue("  the release is broken  ");
    const { rerender } = render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "completed",
          pullRequest: "acme/widgets#42",
          autoMerge: true,
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Force merge" }));

    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/force-merge", {
      method: "POST",
      body: JSON.stringify({ reason: "the release is broken" }),
    });

    // The commit the page shows goes with it: the override is for that
    // commit only (bwsalmon/grain#2399).
    act.mockClear();
    api.mockClear();
    rerender(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "completed",
          pullRequest: "acme/widgets#42",
          autoMerge: true,
          ci: {
            runId: "3f2a1c",
            commit: "abcdef0123456789",
            conclusion: "failure",
          },
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Force merge" }));
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/force-merge", {
      method: "POST",
      body: JSON.stringify({
        reason: "the release is broken",
        head: "abcdef0123456789",
      }),
    });

    // Dismissing the prompt is "no": an override nobody asked for is the
    // one thing this button must never send.
    act.mockClear();
    api.mockClear();
    window.prompt.mockReturnValue(null);
    await user.click(screen.getByRole("button", { name: "Force merge" }));
    expect(act).not.toHaveBeenCalled();
    expect(api).not.toHaveBeenCalledWith(
      "/api/tasks/12/force-merge",
      expect.anything(),
    );
    window.prompt.mockRestore();
  });

  // Offered on a task with something to merge, and on no other: a
  // completed task with no pull request has nothing, and a closed one is
  // refused by ForceMerge (no cycle reads it). A running task with
  // neither a pull request nor a CI commit has not pushed anything yet.
  it("shows no Force merge without anything to merge, or on a closed task", () => {
    const { rerender } = render(
      <DetailOverlay
        task={{ ...baseTask, state: "completed" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Force merge" }),
    ).not.toBeInTheDocument();

    rerender(
      <DetailOverlay
        task={{ ...baseTask, state: "running" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Force merge" }),
    ).not.toBeInTheDocument();

    rerender(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "closed",
          pullRequest: "acme/widgets#42",
          autoMerge: true,
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Force merge" }),
    ).not.toBeInTheDocument();
  });

  // A run blocked in wait_for_checks on CI that never finishes is the
  // case the override is most wanted for: offered while the run is
  // still in flight, with a pull request open or only a CI commit to
  // show its branch was pushed, and the prompt saying the run is
  // stopped along the way.
  it("offers Force merge on a running task with a pull request or a CI commit", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    vi.spyOn(window, "prompt").mockReturnValue("CI is hung");
    const ci = { runId: "3f2a1c", commit: "abcdef0123456789" };
    const { rerender } = render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "running",
          pullRequest: "acme/widgets#42",
          autoMerge: true,
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Force merge" }));
    expect(window.prompt.mock.calls[0][0]).toContain(
      "stops the agent run in flight",
    );
    expect(window.prompt.mock.calls[0][0]).not.toContain(
      "opens the pull request",
    );

    act.mockClear();
    api.mockClear();
    window.prompt.mockClear();
    rerender(
      <DetailOverlay
        task={{ ...baseTask, state: "running", autoMerge: true, ci }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Force merge" }));
    expect(window.prompt.mock.calls[0][0]).toContain("opens the pull request");
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/force-merge", {
      method: "POST",
      body: JSON.stringify({ reason: "CI is hung", head: "abcdef0123456789" }),
    });
    window.prompt.mockRestore();
  });

  // The two halves of what "Run now" is for: a task waiting its turn,
  // and a task whose work was over and needs an agent back on it.
  it("shows Run now on a queued task, wired to the run-now endpoint", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, state: "queued" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Run now" }));

    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/run-now", {
      method: "POST",
    });
  });

  it("shows Run now on a task whose pull request is on the merge queue", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "completed",
          pullRequest: "acme/widgets#42",
          autoMerge: true,
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByRole("button", { name: "Run now" })).toBeInTheDocument();
  });

  // A closed task's run would be cancelled on the spot, and a running
  // one has an agent on it already -- ui.Client.RunNow refuses both.
  it.each(["closed", "running"])("shows no Run now for a %s task", (state) => {
    render(
      <DetailOverlay
        task={{ ...baseTask, state }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole("button", { name: "Run now" }),
    ).not.toBeInTheDocument();
  });

  it("says a run has been asked for while the request is outstanding", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "queued",
          runNowAt: "2026-08-01T00:00:00Z",
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(screen.getByText(/Asked for a run/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Run now" })).toBeDisabled();
  });

  it("shows Reopen for a closed task, wired to the reopen endpoint", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, state: "closed" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Reopen" }));

    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/reopen", {
      method: "POST",
    });
  });

  // A pull request nobody is going to merge, said where the person who
  // closed the task is looking. grain leaves the same note on the task
  // and on the pull request itself (model.OrphanedPullRequestNote); this
  // is the copy that needs no reading of the conversation to find.
  it("warns that a closed task has left its pull request open and unwatched", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, state: "closed", pullRequest: "acme/widgets#42" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    expect(
      screen.getByText(
        /acme\/widgets#42 is still open, and grain has stopped watching it\./,
      ),
    ).toBeInTheDocument();
  });

  // The ordinary ending closes the task too: a pull request that merged
  // sets ClosedAt alongside PrMergedAt (orchestrator.
  // recordPullRequestEvents), and there is nothing orphaned about it.
  it("says nothing about a closed task whose pull request merged", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "closed",
          pullRequest: "acme/widgets#42",
          pullRequestEvents: [{ kind: "merged", at: "2026-08-28T12:00:00Z" }],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    expect(
      screen.queryByText(/grain has stopped watching it/),
    ).not.toBeInTheDocument();
  });

  it("shows Cancel for a running task, only closing after confirmation", async () => {
    const act = vi.fn();
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(false));
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, state: "running" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(act).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });

  it("closes a running task once cancellation is confirmed", async () => {
    const act = vi.fn();
    vi.stubGlobal("confirm", vi.fn().mockReturnValue(true));
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, state: "running" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Cancel" }));

    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/close", {
      method: "POST",
      body: JSON.stringify({ close_pull_request: false }),
    });
    vi.unstubAllGlobals();
  });

  it("shows Close for a queued task, wired to the close endpoint", async () => {
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

    await user.click(screen.getByRole("button", { name: "Close" }));

    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/close", {
      method: "POST",
      body: JSON.stringify({ close_pull_request: false }),
    });
  });

  // The one choice in grain that destroys work on GitHub, so it is worth
  // pinning all three halves of it: the box is only offered where there
  // is an open pull request to close, it is off until somebody ticks it,
  // and what it sends is read at the moment Close is clicked.
  it("offers to close an open pull request alongside the task, unticked", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, pullRequest: "acme/widgets#42" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    const box = screen.getByRole("checkbox", {
      name: /Close acme\/widgets#42 too/,
    });
    expect(box).not.toBeChecked();

    await user.click(screen.getByRole("button", { name: "Close" }));
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/close", {
      method: "POST",
      body: JSON.stringify({ close_pull_request: false }),
    });

    await user.click(box);
    await user.click(screen.getByRole("button", { name: "Close" }));
    act.mock.calls[1][0]();
    expect(api).toHaveBeenLastCalledWith("/api/tasks/12/close", {
      method: "POST",
      body: JSON.stringify({ close_pull_request: true }),
    });
  });

  it("does not offer to close a pull request there is no open one to close", () => {
    const { rerender } = render(
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
      screen.queryByRole("checkbox", { name: /too/ }),
    ).not.toBeInTheDocument();

    // One that already merged is the same: there is nothing left to
    // close, and an option with nothing behind it invites a tick that
    // means nothing.
    rerender(
      <DetailOverlay
        task={{
          ...baseTask,
          pullRequest: "acme/widgets#42",
          pullRequestEvents: [{ kind: "merged", at: "2026-08-28T12:00:00Z" }],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole("checkbox", { name: /too/ }),
    ).not.toBeInTheDocument();
  });

  // Cancelling a running task takes the same close path, so it can orphan
  // a pull request the run had already opened -- and its confirmation has
  // to say when confirming it will also shut one.
  it("names the pull request in a cancel confirmation when it will be closed too", async () => {
    const act = vi.fn();
    const confirm = vi.fn().mockReturnValue(true);
    vi.stubGlobal("confirm", confirm);
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, state: "running", pullRequest: "acme/widgets#42" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(
      screen.getByRole("checkbox", { name: /Close acme\/widgets#42 too/ }),
    );
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(confirm.mock.calls[0][0]).toMatch(
      /close acme\/widgets#42 on GitHub/,
    );
    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/close", {
      method: "POST",
      body: JSON.stringify({ close_pull_request: true }),
    });
    vi.unstubAllGlobals();
  });

  // ui.Client.Submit is `task.AutoMerge = true` and nothing else, so the
  // box and the button are one control: ticking it is what Submit does,
  // and Submit has nothing left to do once it is ticked.
  it("submits the task by ticking auto-merge", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, pullRequest: "acme/widgets#1", autoMerge: false }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
        showError={() => {}}
      />,
    );

    await user.click(
      screen.getByRole("checkbox", { name: /Auto-merge once checks pass/ }),
    );

    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12", {
      method: "PATCH",
      body: JSON.stringify({ autoMerge: true }),
    });
  });

  // grain/task-3451: a task a person files to fix a red base branch is
  // marked as such, which takes it out of the wait on that branch's
  // repair.
  it("marks a task as fixing its red base branch", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "acme/widgets", base: "main" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
        showError={() => {}}
      />,
    );

    await user.click(
      screen.getByRole("checkbox", { name: /This fixes a red main/ }),
    );

    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12", {
      method: "PATCH",
      body: JSON.stringify({ fixesBaseBranch: true }),
    });
  });

  it("does not let grain's own base repair be unmarked", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          repo: "acme/widgets",
          base: "main",
          fixesBaseBranch: true,
          grainBaseRepair: true,
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={() => {}}
        showError={() => {}}
      />,
    );

    const box = screen.getByRole("checkbox", { name: /This fixes a red main/ });
    expect(box).toBeChecked();
    expect(box).toBeDisabled();
  });

  // The half Submit never had: taking a task back off the merge queue.
  it("takes a task off the merge queue by unticking auto-merge", async () => {
    const act = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, pullRequest: "acme/widgets#1", autoMerge: true }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
        showError={() => {}}
      />,
    );

    await user.click(
      screen.getByRole("checkbox", { name: /Auto-merge once checks pass/ }),
    );

    act.mock.calls[0][0]();
    expect(api).toHaveBeenCalledWith("/api/tasks/12", {
      method: "PATCH",
      body: JSON.stringify({ autoMerge: false }),
    });
  });

  // Auto-merge is read after the run, not at checkout, so unlike the
  // repo and base above it is the one edit here that does count for the
  // run already in flight.
  it("says an auto-merge edit still counts for the run in flight", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, state: "running", repo: "acme/widgets" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    expect(
      screen.getByText(/still counts for the run in flight/),
    ).toBeInTheDocument();
  });

  // 2026-10-23 intake walkthrough (task 271): the pane stays mounted
  // through Defer and Undefer, and the Until date it kept made the next
  // plain Defer wake on the old date.
  it("forgets the wake date once a deferral is made", async () => {
    const act = vi.fn((fn) => fn());
    const user = userEvent.setup();
    const props = {
      tasks: [],
      config,
      onClose: () => {},
      onOpenTask: () => {},
      act,
    };
    const { rerender } = render(<DetailOverlay task={baseTask} {...props} />);
    await user.type(screen.getByLabelText("Until (optional)"), "2030-01-15");
    await user.click(screen.getByRole("button", { name: "Defer" }));

    rerender(
      <DetailOverlay
        task={{ ...baseTask, state: "deferred", approved: true }}
        {...props}
      />,
    );
    rerender(<DetailOverlay task={baseTask} {...props} />);
    expect(screen.getByLabelText("Until (optional)")).toHaveValue("");
    api.mockClear();
    await user.click(screen.getByRole("button", { name: "Defer" }));
    expect(api).toHaveBeenCalledWith("/api/tasks/12/defer", {
      method: "POST",
      body: JSON.stringify({ deferredUntil: "" }),
    });
  });
});
