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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import Sidebar, { compactCount } from "./Sidebar.jsx";
import {
  SIDEBAR_COLLAPSED_STORAGE_KEY,
  useSidebarCollapsed,
} from "../sidebarCollapse.js";
import {
  SIDEBAR_HIDDEN_STORAGE_KEY,
  parseHiddenSections,
} from "../sidebarSections.js";

const noop = () => {};
const baseProps = {
  // Every team-owned list in the rail is the selected team's; rows that
  // name no team are "default"'s.
  selectedTeam: "default",
  stateFilter: "all",
  onSetFilter: noop,
  onOpenSystem: noop,
  onOpenMetrics: noop,
  onOpenNewTask: noop,
  onSetView: noop,
};
const tasks = [
  { id: 1, state: "queued", blocked: false },
  { id: 2, state: "queued", blocked: true, blockedBy: [3] },
  { id: 3, state: "running", blocked: false },
];

describe("Sidebar", () => {
  it("marks the Boards index as the current rail destination", () => {
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        view="boards"
        openBoardId={null}
        boards={[]}
      />,
    );

    expect(screen.getByRole("button", { name: /^Boards/ })).toHaveClass(
      "Mui-selected",
    );
  });

  // The selected paint is invisible to a screen reader; the open page
  // has to be said, on that entry and on no other.
  it("tells assistive technology which rail entry is the open page", () => {
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        view="boards"
        openBoardId={null}
        boards={[]}
      />,
    );

    expect(screen.getByRole("button", { name: /^Boards/ })).toHaveAttribute(
      "aria-current",
      "page",
    );
    const current = screen
      .getAllByRole("button")
      .filter((b) => b.getAttribute("aria-current"));
    expect(current).toHaveLength(1);
    expect(screen.getByRole("button", { name: /^Tasks/ })).not.toHaveAttribute(
      "aria-current",
    );
  });

  // The standing sessions -- the director among them -- sit directly
  // under the inbox when pinned.
  it("lists a standing session under the inbox when pinned and opens it when clicked", async () => {
    const onOpenSession = vi.fn();
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        pinnedItems={[
          { kind: "view", id: "inbox" },
          { kind: "session", id: "director" },
        ]}
        sessions={[{ id: "director", title: "Director", taskId: "7" }]}
        onOpenSession={onOpenSession}
      />,
    );

    const labels = screen.getAllByRole("button").map((e) => e.textContent);
    expect(labels.findIndex((l) => /^Director/.test(l))).toBe(
      labels.findIndex((l) => /^Inbox/.test(l)) + 1,
    );

    const dirButton = screen.getByRole("button", { name: /Director/ });
    expect(
      dirButton.querySelector('.nav-icon svg[data-glyph="director"]'),
    ).toBeInTheDocument();
    await userEvent.click(dirButton);
    expect(onOpenSession).toHaveBeenCalledWith("director");
  });

  it("draws no session entry on a deployment that configures none", () => {
    render(
      <Sidebar {...baseProps} config={null} tasks={tasks} sessions={[]} />,
    );
    expect(
      screen.queryByRole("button", { name: /Director/ }),
    ).not.toBeInTheDocument();
  });

  it("lists sessions as a panel destination and opens it when clicked", async () => {
    const onSetView = vi.fn();
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        sessions={[
          { id: "director", title: "Director", taskId: "7" },
          { id: "custom-reviewer", title: "Reviewer", taskId: "8" },
        ]}
        onSetView={onSetView}
      />,
    );

    const sessionsBtn = screen.getByRole("button", { name: /^Sessions 2/ });
    expect(sessionsBtn).toBeInTheDocument();
    expect(
      sessionsBtn.querySelector('.nav-icon svg[data-glyph="sessions"]'),
    ).toBeInTheDocument();
    await userEvent.click(sessionsBtn);
    expect(onSetView).toHaveBeenCalledWith("sessions");
  });

  // Boards are the deployment's (single-tenant reset, stage 4d): every
  // team selection lists all of them.
  it("lists every board whichever team is selected", () => {
    const testBoards = [
      { id: "board-1", name: "Default Board", team: "default" },
      { id: "board-2", name: "Infra Board", team: "infra" },
      { id: "board-3", name: "Sec Board", team: "security" },
    ];

    // When selectedTeam is "infra"
    const { rerender } = render(
      <Sidebar
        {...baseProps}
        config={{ deployment: { id: "default" } }}
        tasks={tasks}
        boards={testBoards}
        selectedTeam="infra"
      />,
    );

    expect(
      screen.getByRole("button", { name: /^Boards 3/ }),
    ).toBeInTheDocument();

    // Another team: the same boards.
    rerender(
      <Sidebar
        {...baseProps}
        config={{ deployment: { id: "default" } }}
        tasks={tasks}
        boards={testBoards}
        selectedTeam="security"
      />,
    );

    expect(
      screen.getByRole("button", { name: /^Boards 3/ }),
    ).toBeInTheDocument();
  });

  it("does not render sandboxes and caches in the sidebar navigation", () => {
    render(
      <Sidebar {...baseProps} config={{ sandboxCount: 3 }} tasks={tasks} />,
    );

    expect(
      screen.queryByRole("button", { name: /^Sandboxes/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^Caches/ }),
    ).not.toBeInTheDocument();
  });

  it("lists the destinations flat, with no team-scoped box around them", () => {
    render(
      <Sidebar
        {...baseProps}
        config={{ sandboxCount: 3 }}
        tasks={tasks}
        pinnedItems={[{ kind: "view", id: "inbox" }]}
      />,
    );

    // The bordered "team-scoped section" is gone with the teams
    // (docs/design/settings-panel.md §4.2); its entries are the list.
    expect(screen.queryByTestId("team-section")).toBeNull();
    expect(screen.queryByTestId("team-selector")).toBeNull();
    for (const name of [
      /^Tasks 3/,
      "Boards 0",
      "Sessions 0",
      "Processes 0",
      "CI 0",
      /^Repos/,
    ]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
  });

  // A standing session's task is parked in "Asked" between
  // exchanges and is left off the task list (hidesStandingSessions), so
  // the Tasks entry's total leaves it out too: it is counted beside
  // Sessions, and nowhere else.
  it("counts a standing session under Sessions and not under Tasks", () => {
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={[
          ...tasks,
          {
            id: 7,
            state: "awaiting reply",
            blocked: false,
            consoleSession: "director",
          },
        ]}
        sessions={[{ id: "director", title: "Director", taskId: "7" }]}
        pinnedItems={[
          { kind: "view", id: "tasks" },
          { kind: "view", id: "sessions" },
        ]}
      />,
    );

    // Both Tasks entries -- the pinned one and the one above the state
    // list -- say three, not four.
    const tasksButtons = screen.getAllByRole("button", { name: /^Tasks/ });
    expect(tasksButtons.length).toBeGreaterThan(0);
    for (const b of tasksButtons) {
      expect(b).toHaveAccessibleName(/^Tasks 3$/);
    }
    for (const b of screen.getAllByRole("button", { name: /^Sessions/ })) {
      expect(b).toHaveAccessibleName(/^Sessions 1$/);
    }
    expect(
      screen.queryByRole("button", { name: /^Asked/ }),
    ).not.toBeInTheDocument();
  });

  it("opens System → Caches when its tab is pinned", async () => {
    const onSetView = vi.fn();
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        pinnedItems={[{ kind: "view", id: "system", tab: "caches" }]}
        onSetView={onSetView}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: /^Caches/ }));
    expect(onSetView).toHaveBeenCalledWith("system", { systemTab: "caches" });
  });

  // The cache row prints how many items the cache holds -- packages,
  // images, git repos together -- so the rail says whether the proxy is
  // doing anything at a glance (grain/task-321).
  it("shows how many items the caches hold when pinned, abbreviated, and breakdown on hover", () => {
    const { rerender } = render(
      <Sidebar
        {...baseProps}
        config={{ packageCacheEntries: 2514, actionsCacheEntries: 1840 }}
        tasks={tasks}
        pinnedItems={[{ kind: "view", id: "system", tab: "caches" }]}
      />,
    );

    expect(
      screen.getByRole("button", { name: "Caches 4.4k" }),
    ).toBeInTheDocument();
    expect(
      screen.getByTitle(
        /4,354 cached items \(2,514 packages, 1,840 Actions cache entries\)/,
      ),
    ).toBeInTheDocument();

    // When only package cache is configured
    rerender(
      <Sidebar
        {...baseProps}
        config={{ packageCacheEntries: 2514 }}
        tasks={tasks}
        pinnedItems={[{ kind: "view", id: "system", tab: "caches" }]}
      />,
    );
    expect(
      screen.getByRole("button", { name: "Caches 2.5k" }),
    ).toBeInTheDocument();
    expect(screen.getByTitle(/2,514 packages cached/)).toBeInTheDocument();

    // When only actions cache is configured
    rerender(
      <Sidebar
        {...baseProps}
        config={{ actionsCacheEntries: 1840 }}
        tasks={tasks}
        pinnedItems={[{ kind: "view", id: "system", tab: "caches" }]}
      />,
    );
    expect(
      screen.getByRole("button", { name: "Caches 1.8k" }),
    ).toBeInTheDocument();
    expect(screen.getByTitle(/1,840 cache entries/)).toBeInTheDocument();
  });

  it("shows empty caches as 0 and one that is not running as nothing when pinned", () => {
    const { rerender } = render(
      <Sidebar
        {...baseProps}
        config={{ packageCacheEntries: 0, actionsCacheEntries: 0 }}
        tasks={tasks}
        pinnedItems={[{ kind: "view", id: "system", tab: "caches" }]}
      />,
    );
    expect(
      screen.getByRole("button", { name: "Caches 0" }),
    ).toBeInTheDocument();

    // No cache on this deployment: both fields absent
    rerender(
      <Sidebar
        {...baseProps}
        config={{}}
        tasks={tasks}
        pinnedItems={[{ kind: "view", id: "system", tab: "caches" }]}
      />,
    );
    expect(screen.getByRole("button", { name: "Caches" })).toBeInTheDocument();
  });

  // docs/design/settings-panel.md §5.6: a pinned System tab or Settings
  // section is selected only while that tab or section is open.
  it("selects a pinned tab or section only while it is the open one", () => {
    const pins = [
      { kind: "view", id: "system", tab: "sandboxes" },
      { kind: "view", id: "settings", section: "memory" },
    ];
    const { rerender } = render(
      <Sidebar
        {...baseProps}
        config={{ sandboxCount: 3 }}
        tasks={tasks}
        pinnedItems={pins}
        view="system"
        systemTab="sandboxes"
      />,
    );
    const sandboxes = () => screen.getByRole("button", { name: "Sandboxes 3" });
    const memory = () => screen.getByRole("button", { name: "Memory" });
    expect(sandboxes()).toHaveClass("Mui-selected");
    expect(memory()).not.toHaveClass("Mui-selected");

    rerender(
      <Sidebar
        {...baseProps}
        config={{ sandboxCount: 3 }}
        tasks={tasks}
        pinnedItems={pins}
        view="system"
        systemTab="logs"
      />,
    );
    expect(sandboxes()).not.toHaveClass("Mui-selected");

    rerender(
      <Sidebar
        {...baseProps}
        config={{ sandboxCount: 3 }}
        tasks={tasks}
        pinnedItems={pins}
        view="settings"
        settingsSection="memory"
      />,
    );
    expect(memory()).toHaveClass("Mui-selected");
  });

  it("selects a pinned System → Health entry on the default /system tab and keeps bare System selected across unpinned tabs", () => {
    const { rerender } = render(
      <Sidebar
        {...baseProps}
        config={{ sandboxCount: 3 }}
        tasks={tasks}
        pinnedItems={[
          { kind: "view", id: "system", tab: "health" },
          { kind: "view", id: "system", tab: "sandboxes" },
        ]}
        view="system"
        systemTab=""
      />,
    );
    expect(screen.getByRole("button", { name: "Health" })).toHaveClass(
      "Mui-selected",
    );
    expect(screen.getByRole("button", { name: "Sandboxes 3" })).not.toHaveClass(
      "Mui-selected",
    );

    // When bare System is pinned alongside Sandboxes, bare System stays selected
    // on Health and Logs, and yields selection on Sandboxes.
    const bareAndSandboxes = [
      { kind: "view", id: "system" },
      { kind: "view", id: "system", tab: "sandboxes" },
    ];
    rerender(
      <Sidebar
        {...baseProps}
        config={{ sandboxCount: 3 }}
        tasks={tasks}
        pinnedItems={bareAndSandboxes}
        view="system"
        systemTab="logs"
      />,
    );
    expect(screen.getByRole("button", { name: "System" })).toHaveClass(
      "Mui-selected",
    );
    expect(screen.getByRole("button", { name: "Sandboxes 3" })).not.toHaveClass(
      "Mui-selected",
    );

    rerender(
      <Sidebar
        {...baseProps}
        config={{ sandboxCount: 3 }}
        tasks={tasks}
        pinnedItems={bareAndSandboxes}
        view="system"
        systemTab="sandboxes"
      />,
    );
    expect(screen.getByRole("button", { name: "System" })).not.toHaveClass(
      "Mui-selected",
    );
    expect(screen.getByRole("button", { name: "Sandboxes 3" })).toHaveClass(
      "Mui-selected",
    );
  });

  it("shows the number of live sandboxes when pinned", () => {
    render(
      <Sidebar
        {...baseProps}
        config={{ sandboxCount: 3 }}
        tasks={tasks}
        pinnedItems={[{ kind: "view", id: "system", tab: "sandboxes" }]}
      />,
    );

    expect(
      screen.getByRole("button", { name: "Sandboxes 3" }),
    ).toBeInTheDocument();
  });

  it("shows the number of running CI runs", () => {
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        ciRuns={[
          { id: "running-1" },
          {
            id: "finished",
            finishedAt: "2026-09-08T12:00:00Z",
            conclusion: "success",
          },
          { id: "running-2" },
        ]}
      />,
    );

    expect(screen.getByRole("button", { name: /CI 2/ })).toBeInTheDocument();
  });

  it("counts tasks per state and lists only states that are present", () => {
    render(<Sidebar {...baseProps} config={null} tasks={tasks} />);

    expect(screen.getByRole("button", { name: /Tasks 3/ })).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Waiting 2/ }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Running 1/ }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Merging/ }),
    ).not.toBeInTheDocument();
  });

  // The whole point of making the wait a state rather than a chip: it is
  // countable, so "what is sitting here waiting on me?" has an entry in
  // the rail and a filter behind it.
  it("counts tasks waiting on a Submit click under Asked", () => {
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={[...tasks, { id: 4, state: "awaiting_submit", blocked: false }]}
      />,
    );
    expect(screen.getByRole("button", { name: /Asked/ })).toHaveTextContent(
      /Asked\s*1/,
    );
  });

  it("shows a blocked nav entry only when a task is blocked", () => {
    render(<Sidebar {...baseProps} config={null} tasks={tasks} />);
    expect(
      screen.getByRole("button", { name: /Blocked 1/ }),
    ).toBeInTheDocument();
  });

  it("omits the blocked nav entry when nothing is blocked", () => {
    const unblocked = tasks.map((t) => ({ ...t, blocked: false }));
    render(<Sidebar {...baseProps} config={null} tasks={unblocked} />);
    expect(
      screen.queryByRole("button", { name: /Blocked/ }),
    ).not.toBeInTheDocument();
  });

  it("calls onSetFilter with the clicked state", async () => {
    const onSetFilter = vi.fn();
    const onSetView = vi.fn();
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        onSetFilter={onSetFilter}
        onSetView={onSetView}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: /Waiting 2/ }));
    expect(onSetFilter).toHaveBeenCalledWith("queued");
    expect(onSetView).toHaveBeenCalledWith("tasks");
  });

  it("routes the new-task button, and the phone footer's System row, to their callbacks", async () => {
    const onOpenSystem = vi.fn();
    const onOpenNewTask = vi.fn();
    const user = userEvent.setup();

    const { rerender } = render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        onOpenSystem={onOpenSystem}
        onOpenNewTask={onOpenNewTask}
      />,
    );

    await user.click(screen.getByRole("button", { name: "+ New task" }));
    expect(onOpenNewTask).toHaveBeenCalledTimes(1);

    // Settings and System are the top bar's on a desktop, and Metrics is
    // a System tab: the rail has no footer list.
    expect(
      screen.queryByRole("button", { name: "Settings" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "System" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Metrics" }),
    ).not.toBeInTheDocument();

    rerender(
      <Sidebar
        {...baseProps}
        pinFooter
        config={null}
        tasks={tasks}
        onOpenSystem={onOpenSystem}
        onOpenNewTask={onOpenNewTask}
      />,
    );
    await user.click(screen.getByRole("button", { name: "System" }));
    expect(onOpenSystem).toHaveBeenCalledTimes(1);
  });

  it("opens the create options dropdown and triggers actions", async () => {
    const onOpenNewTask = vi.fn();
    const onNewSession = vi.fn();
    const onNewBoard = vi.fn();
    const user = userEvent.setup();

    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        onOpenNewTask={onOpenNewTask}
        onNewSession={onNewSession}
        onNewBoard={onNewBoard}
      />,
    );

    // Open create options menu
    await user.click(screen.getByRole("button", { name: "Create options" }));

    expect(
      screen.getByRole("menuitem", { name: /New task/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("menuitem", { name: /New session/i }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("menuitem", { name: /New team/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("menuitem", { name: /New board/i }),
    ).toBeInTheDocument();

    // Click "New session"
    await user.click(screen.getByRole("menuitem", { name: /New session/i }));
    expect(onNewSession).toHaveBeenCalledTimes(1);

    // Open menu again and click "New task"
    await user.click(screen.getByRole("button", { name: "Create options" }));
    await user.click(screen.getByRole("menuitem", { name: /New task/i }));
    expect(onOpenNewTask).toHaveBeenCalledTimes(1);

    // Open menu again and click "New board"
    await user.click(screen.getByRole("button", { name: "Create options" }));
    await user.click(screen.getByRole("menuitem", { name: /New board/i }));
    expect(onNewBoard).toHaveBeenCalledTimes(1);
  });

  // grain/task-58: when a footer entry's pane is open, the rail marks
  // that entry the way it marks whatever list view is up -- so the
  // sidebar says which pane the whole screen is showing.
  it("marks the phone footer's System row while System is showing", () => {
    render(
      <Sidebar
        {...baseProps}
        pinFooter
        config={null}
        tasks={tasks}
        view="system"
      />,
    );
    expect(screen.getByRole("button", { name: "System" })).toHaveClass(
      "Mui-selected",
    );
  });

  it("counts the tasks waiting on the reader beside the inbox entry", () => {
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={[
          { id: 1, state: "awaiting_reply", blocked: false },
          { id: 2, state: "proposed", blocked: false },
          { id: 3, state: "running", blocked: false },
          { id: 4, state: "closed", blocked: false },
        ]}
      />,
    );

    // 1 reply + 1 proposed = 2 waiting
    expect(screen.getByRole("button", { name: /Inbox 2/ })).toBeInTheDocument();
  });

  it("counts a submit and a reply together under one Asked entry, waiting on the reader", () => {
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={[
          { id: 1, state: "awaiting_reply", blocked: false },
          { id: 2, state: "awaiting_submit", blocked: false },
          { id: 3, state: "running", blocked: false },
        ]}
      />,
    );

    expect(screen.getByRole("button", { name: /Inbox 2/ })).toBeInTheDocument();
    // And both state entries carry the waiting-on-you title that explains
    // why they are colored with the inbox accent.
    // One entry, "Asked", for both states, carrying the waiting-on-you
    // title that explains why it is colored with the inbox accent.
    const asked = screen.getAllByRole("button", { name: /^Asked/ });
    expect(asked).toHaveLength(1);
    expect(asked[0]).toHaveTextContent(/Asked\s*2/);
    expect(asked[0]).toHaveAttribute(
      "title",
      expect.stringMatching(/waiting on you/),
    );
  });

  it("opens both asked states through the one Asked entry", async () => {
    const onSetFilter = vi.fn();
    render(
      <Sidebar
        {...baseProps}
        config={null}
        onSetFilter={onSetFilter}
        tasks={[
          { id: 1, state: "awaiting_reply", blocked: false },
          { id: 2, state: "awaiting_submit", blocked: false },
        ]}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /^Asked/ }));
    expect(onSetFilter).toHaveBeenCalledWith("asked");
  });

  it("shows no count on the inbox when nothing is waiting", () => {
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={[{ id: 1, state: "running", blocked: false }]}
      />,
    );

    expect(
      screen.getByRole("button", { name: /^Inbox\b/ }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^Inbox \d/ }),
    ).not.toBeInTheDocument();
  });

  it("counts waiting tasks without including base branch gates", () => {
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={[{ id: 4, state: "awaiting_reply", blocked: false }]}
        baseBranchGates={[
          { repo: "acme/widgets", branch: "main", judged: false },
        ]}
      />,
    );
    const inbox = screen.getByRole("button", { name: /Inbox 1/ });
    expect(
      within(inbox).getByText("1").closest(".MuiChip-root"),
    ).toHaveAttribute("title", "1 task waiting on you");
  });

  // The Inbox lists the config repository's sync failure as a row of its
  // own and counts it in its heading, so the rail entry that opens it
  // counts it too (2026-10-29 intake walkthrough).
  it("counts the config repository's sync failure, as the Inbox heading does", () => {
    render(
      <Sidebar
        {...baseProps}
        config={{
          stateRepoSyncFailure: {
            since: "2026-10-29T09:00:00Z",
            error: "git fetch origin: authentication required",
          },
        }}
        tasks={[{ id: 4, state: "awaiting_reply", blocked: false }]}
      />,
    );
    const inbox = screen.getByRole("button", { name: /Inbox 2/ });
    expect(
      within(inbox).getByText("2").closest(".MuiChip-root"),
    ).toHaveAttribute(
      "title",
      "1 task waiting on you, settings are not being exported",
    );
  });

  // A local-script call waiting for approval is listed and counted on
  // the Inbox; /api/config says how many there are, so the rail counts
  // them too -- including when nothing else waits, where it used to show
  // no count while a run was blocked on a person (task 1808).
  it("counts local-script calls waiting for approval, as the Inbox heading does", () => {
    const { rerender } = render(
      <Sidebar
        {...baseProps}
        config={{ pendingLocalScripts: 1 }}
        tasks={[]}
      />,
    );
    let inbox = screen.getByRole("button", { name: /Inbox 1/ });
    expect(
      within(inbox).getByText("1").closest(".MuiChip-root"),
    ).toHaveAttribute("title", "1 local script waiting for approval");

    rerender(
      <Sidebar
        {...baseProps}
        config={{ pendingLocalScripts: 2 }}
        tasks={[{ id: 4, state: "awaiting_reply", blocked: false }]}
      />,
    );
    inbox = screen.getByRole("button", { name: /Inbox 3/ });
    expect(
      within(inbox).getByText("3").closest(".MuiChip-root"),
    ).toHaveAttribute(
      "title",
      "1 task waiting on you, 2 local scripts waiting for approval",
    );
  });

  it("does not show inbox badge when only base branch gates exist and no tasks wait", () => {
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        baseBranchGates={[
          { repo: "acme/widgets", branch: "main", judged: false },
          { repo: "acme/other", branch: "trunk", judged: true },
        ]}
      />,
    );
    expect(
      screen.queryByRole("button", { name: /^Inbox \d/ }),
    ).not.toBeInTheDocument();
  });

  it("switches to the inbox view when clicked, and marks it while it is up", async () => {
    const onSetView = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={[]}
        view="tasks"
        onSetView={onSetView}
      />,
    );

    const button = screen.getByRole("button", { name: /^Inbox\b/ });
    expect(button).not.toHaveClass("Mui-selected");

    await user.click(button);
    expect(onSetView).toHaveBeenCalledWith("inbox");

    // Once App switches view to inbox, the rail entry is selected.
    rerender(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={[]}
        view="inbox"
        onSetView={onSetView}
      />,
    );
    expect(screen.getByRole("button", { name: /^Inbox\b/ })).toHaveClass(
      "Mui-selected",
    );
  });

  it("no longer offers a fixed board or timeline destination", () => {
    render(<Sidebar {...baseProps} config={null} tasks={tasks} />);

    expect(screen.queryByRole("button", { name: "Board" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Timeline" })).toBeNull();
    expect(screen.getByRole("button", { name: /^Boards/ })).toBeInTheDocument();
  });

  // docs/design/settings-panel.md §4.2: one Processes entry where
  // Schedules, Templates and Suites were, counting every row the page
  // lists.
  it("switches to Processes and counts every process when clicked", async () => {
    const onSetView = vi.fn();
    const user = userEvent.setup();
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={[]}
        schedules={[{ id: "sched-1" }, { id: "sched-2" }]}
        templates={[{ id: "template-1" }]}
        suites={[{ id: "suite-1" }]}
        processTriggers={[
          { repo: "acme/widgets", path: ".grain/triggers/a.yml" },
        ]}
        onSetView={onSetView}
      />,
    );

    for (const old of [/^Schedules/, /^Templates/, /^Suites/]) {
      expect(screen.queryByRole("button", { name: old })).toBeNull();
    }
    const button = screen.getByRole("button", { name: "Processes 5" });
    await user.click(button);

    expect(onSetView).toHaveBeenCalledWith("processes");
  });

  it("sends a click on CI to its own view, and shows zero running", async () => {
    const onSetView = vi.fn();
    const user = userEvent.setup();
    render(
      <Sidebar {...baseProps} config={null} tasks={[]} onSetView={onSetView} />,
    );

    const button = screen.getByRole("button", { name: /^CI 0/ });
    await user.click(button);

    expect(onSetView).toHaveBeenCalledWith("ci");
  });

  // grain/task-3100: the team's memory is a page of the rail's, beside
  // CI, rather than a section at the foot of its settings.
  it("has no Memory entry: Memory is a Settings section", () => {
    render(<Sidebar {...baseProps} config={null} tasks={[]} />);
    expect(
      screen.queryByRole("button", { name: /^Memory$/ }),
    ).not.toBeInTheDocument();
  });

  it("marks each list entry with its own glyph", () => {
    render(<Sidebar {...baseProps} config={null} tasks={[]} />);

    for (const [label, kind] of [
      ["Tasks", "tasks"],
      ["Boards", "boards"],
      ["Sessions", "sessions"],
      ["Processes", "processes"],
      ["CI", "ci"],
      ["Repos", "repos"],
    ]) {
      const entry = screen.getByRole("button", {
        name: new RegExp(`^${label}`),
      });
      expect(
        entry.querySelector(`svg[data-glyph="${kind}"]`),
      ).toBeInTheDocument();
    }
  });

  it("renders customized pinned navigation items and opens them on click", async () => {
    const onOpenSession = vi.fn();
    const onOpenBoard = vi.fn();
    const onSetView = vi.fn();
    const user = userEvent.setup();

    const customPinned = [
      { kind: "view", id: "ci" },
      { kind: "session", id: "reviewer" },
      { kind: "board", id: "board-1" },
    ];

    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        pinnedItems={customPinned}
        sessions={[
          { id: "director", title: "Director" },
          { id: "reviewer", title: "Reviewer" },
        ]}
        boards={[{ id: "board-1", name: "Alpha Roadmap" }]}
        onOpenSession={onOpenSession}
        onOpenBoard={onOpenBoard}
        onSetView={onSetView}
      />,
    );

    // Custom pinned items are rendered
    const ciBtn = screen.getAllByRole("button", { name: /^CI/ })[0];
    await user.click(ciBtn);
    expect(onSetView).toHaveBeenCalledWith("ci");

    const reviewerBtn = screen.getByRole("button", { name: "Reviewer" });
    await user.click(reviewerBtn);
    expect(onOpenSession).toHaveBeenCalledWith("reviewer");

    const boardBtn = screen.getByRole("button", {
      name: "Alpha Roadmap",
    });
    await user.click(boardBtn);
    expect(onOpenBoard).toHaveBeenCalledWith(
      expect.objectContaining({ id: "board-1", name: "Alpha Roadmap" }),
    );
  });

  it("renders customized pinned navigation items with custom icons and colors", async () => {
    const customPinned = [
      { kind: "view", id: "processes", icon: "schedules", color: "#ff5500" },
    ];

    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        pinnedItems={customPinned}
      />,
    );

    const pinnedRow = screen.getAllByRole("button", {
      name: /^Processes/,
    })[0];
    const iconSpan = pinnedRow.querySelector(".nav-icon");
    expect(iconSpan).toHaveStyle({ color: "rgb(255, 85, 0)" });
    expect(
      pinnedRow.querySelector('svg[data-glyph="schedules"]'),
    ).toBeInTheDocument();
  });

  it("opens Customize sidebar when the Customize button is clicked", async () => {
    const onOpenCustomize = vi.fn();
    const user = userEvent.setup();

    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        onOpenCustomize={onOpenCustomize}
      />,
    );

    const customizeBtn = screen.getByRole("button", {
      name: "Customize sidebar",
    });
    await user.click(customizeBtn);
    expect(onOpenCustomize).toHaveBeenCalledTimes(1);
  });

  it("marks each destination with its own glyph", () => {
    const { container } = render(
      <Sidebar {...baseProps} config={null} tasks={[]} />,
    );

    for (const [label, kind] of [
      ["Inbox", "inbox"],
      ["Repos", "repos"],
    ]) {
      const entry = screen.getByRole("button", {
        name: new RegExp(`^${label}\\b`),
      });
      const glyph = entry.querySelector(`svg[data-glyph="${kind}"]`);
      expect(glyph).toBeInTheDocument();
      expect(glyph.closest(".nav-icon")).not.toBeNull();
    }

    const tasks = screen.getByRole("button", { name: /^Tasks\b/ });
    expect(
      tasks.querySelector('.nav-icon svg[data-glyph="tasks"]'),
    ).toBeInTheDocument();

    expect(screen.queryByTestId("user-settings-button")).toBeNull();

    expect(container.querySelectorAll("svg[data-glyph]")).toHaveLength(7);
  });

  it("shows Merged in sidebar under tasks and separates its count from Closed", async () => {
    const onSetFilter = vi.fn();
    const user = userEvent.setup();
    const tasks = [
      { id: "1", state: "closed", prMergedAt: "2026-09-26T00:00:00Z" },
      { id: "2", state: "closed", endedAt: null }, // cancelled
      { id: "3", state: "running" },
    ];
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        onSetFilter={onSetFilter}
      />,
    );

    const mergedEntry = screen.getByRole("button", { name: /Merged 1/ });
    expect(mergedEntry).toBeInTheDocument();
    const closedEntry = screen.getByRole("button", { name: /Closed 1/ });
    expect(closedEntry).toBeInTheDocument();

    await user.click(mergedEntry);
    expect(onSetFilter).toHaveBeenCalledWith("merged");
  });

  it("shows the environment name beside the wordmark when one is configured", () => {
    render(
      <Sidebar
        {...baseProps}
        config={{ environmentName: "staging" }}
        tasks={tasks}
      />,
    );
    expect(screen.getByText("staging")).toBeInTheDocument();
  });

  it("shows no environment badge when the deployment is unnamed", () => {
    render(
      <Sidebar {...baseProps} config={{ environmentName: "" }} tasks={tasks} />,
    );
    expect(screen.queryByText("staging")).not.toBeInTheDocument();
  });

  it("prints the release, short commit and commit time in the footer", () => {
    render(
      <Sidebar
        {...baseProps}
        config={{
          version: {
            release: "v1.2.0",
            commit: "0fbfb4619f0a1c2d3e4f5a6b7c8d9e0f11223344",
            committedAt: "2026-09-03T14:02:11Z",
          },
        }}
        tasks={[]}
      />,
    );

    expect(
      screen.getByText("v1.2.0 · 0fbfb46 · 2026-09-03 14:02Z"),
    ).toBeInTheDocument();
    expect(
      screen.getByTitle(
        /Running commit 0fbfb4619f0a1c2d3e4f5a6b7c8d9e0f11223344, release v1.2.0/,
      ),
    ).toBeInTheDocument();
  });

  it("prints release like v1.3.0-rc when present", () => {
    render(
      <Sidebar
        {...baseProps}
        config={{
          version: {
            release: "v1.3.0-rc",
            commit: "0fbfb4619f0a1c2d3e4f5a6b7c8d9e0f11223344",
          },
        }}
        tasks={[]}
      />,
    );

    expect(screen.getByText("v1.3.0-rc · 0fbfb46")).toBeInTheDocument();
  });

  it("prints the build's short commit and commit time in the footer", () => {
    render(
      <Sidebar
        {...baseProps}
        config={{
          version: {
            commit: "0fbfb4619f0a1c2d3e4f5a6b7c8d9e0f11223344",
            committedAt: "2026-09-03T14:02:11Z",
          },
        }}
        tasks={[]}
      />,
    );

    expect(screen.getByText("0fbfb46 · 2026-09-03 14:02Z")).toBeInTheDocument();
    expect(
      screen.getByTitle(
        /Running commit 0fbfb4619f0a1c2d3e4f5a6b7c8d9e0f11223344/,
      ),
    ).toBeInTheDocument();
  });

  it("marks a build made from a modified tree", () => {
    render(
      <Sidebar
        {...baseProps}
        config={{
          version: {
            commit: "0fbfb46199",
            committedAt: "2026-09-03T14:02:11Z",
            modified: true,
          },
        }}
        tasks={[]}
      />,
    );

    expect(
      screen.getByText("0fbfb46-dirty · 2026-09-03 14:02Z"),
    ).toBeInTheDocument();
    expect(screen.getByTitle(/uncommitted changes/)).toBeInTheDocument();
  });

  it("prints the commit alone when the stamp carries no usable time", () => {
    render(
      <Sidebar
        {...baseProps}
        config={{
          version: {
            commit: "0fbfb4619f0a1c2d3e4f5a6b7c8d9e0f11223344",
            committedAt: "",
          },
        }}
        tasks={[]}
      />,
    );

    expect(screen.getByText("0fbfb46")).toBeInTheDocument();
  });

  it("shows no build stamp when the binary carries none", () => {
    render(<Sidebar {...baseProps} config={{ version: null }} tasks={tasks} />);
    expect(screen.queryByText(/[0-9a-f]{7}/)).not.toBeInTheDocument();
  });

  it("does not show Logs or Sandbox health as their own nav entries", () => {
    render(
      <Sidebar {...baseProps} config={{ logs: { count: 42 } }} tasks={tasks} />,
    );

    // Both became tabs of the System pane in grain/task-157, rather than
    // each claiming an entry of its own in the rail.
    expect(screen.queryByRole("button", { name: /Logs/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Sandbox health/ })).toBeNull();
  });

  // §4.3: on a phone the drawer's foot is the System row, then the build
  // stamp -- no user tile between or after them.
  it("ends the phone footer with System and the build stamp, and no user tile", () => {
    render(
      <Sidebar
        {...baseProps}
        pinFooter
        config={{
          actor: "debian",
          version: { commit: "abc1234", built: "2026-09-28T00:00:00Z" },
        }}
        tasks={[]}
      />,
    );
    const system = screen.getByRole("button", { name: "System" });
    const stamp = screen.getByTestId("build-stamp");
    expect(
      system.compareDocumentPosition(stamp) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(stamp.parentElement.lastElementChild).toBe(stamp);
    expect(screen.queryByTestId("user-tile")).toBeNull();
    expect(screen.queryByText("debian")).toBeNull();
  });

  it("marks the phone footer's System row when a merge reset settings and nobody was told", () => {
    render(
      <Sidebar
        {...baseProps}
        pinFooter
        config={{ stateRepoResetNotice: true }}
        tasks={[]}
      />,
    );

    const system = screen.getByRole("button", { name: /System/ });
    expect(system).toContainElement(
      screen.getByRole("img", { name: /reset settings/ }),
    );
  });

  it("switches to the boards view and shows its count when clicked", async () => {
    const onSetView = vi.fn();
    const user = userEvent.setup();
    render(
      <Sidebar
        {...baseProps}
        config={{}}
        tasks={[]}
        boards={[
          { id: "board-1", name: "Waiting on me", folder: "", kind: "list" },
          {
            id: "board-2",
            name: "Flaky tests",
            folder: "Reports",
            kind: "kanban",
          },
        ]}
        onSetView={onSetView}
      />,
    );

    const boardsBtn = screen.getByRole("button", { name: /^Boards 2/ });
    expect(boardsBtn).toBeInTheDocument();
    await user.click(boardsBtn);
    expect(onSetView).toHaveBeenCalledWith("boards");
  });

  // grain has no users (docs/design/settings-panel.md §6): the rail
  // names nobody, neither a browser-chosen name nor the server's actor.
  it("has no user tile, and names no user or actor", () => {
    localStorage.setItem("grain.username", "alice");
    render(
      <Sidebar
        {...baseProps}
        config={{ authenticated: true, actor: "user:bob@company.com" }}
        tasks={[]}
      />,
    );
    expect(screen.queryByTestId("user-tile")).not.toBeInTheDocument();
    expect(screen.queryByTestId("user-settings-button")).toBeNull();
    expect(screen.queryByLabelText("Current user")).toBeNull();
    expect(screen.queryByText("Set username")).toBeNull();
    expect(screen.queryByText("alice")).toBeNull();
    expect(screen.queryByText(/bob@company\.com/)).toBeNull();
  });

  it("links the build stamp to Settings → Upgrade", async () => {
    const onOpenUpgrade = vi.fn();
    const user = userEvent.setup();
    render(
      <Sidebar
        {...baseProps}
        config={{
          authenticated: true,
          actor: "alice@company.com",
          version: { commit: "abc1234", built: "2026-09-28T00:00:00Z" },
        }}
        tasks={[]}
        onOpenUpgrade={onOpenUpgrade}
      />,
    );

    const stamp = screen.getByTestId("build-stamp");
    expect(stamp).toHaveAttribute("href", "/settings/upgrade");
    // The footer is the build stamp alone (§4.3): the last thing in the
    // rail, under everything else it holds.
    expect(stamp.parentElement.lastElementChild).toBe(stamp);
    await user.click(stamp);
    expect(onOpenUpgrade).toHaveBeenCalledTimes(1);
  });

  it("includes the build edition in the build stamp and tooltip when present", () => {
    render(
      <Sidebar
        {...baseProps}
        config={{
          authenticated: true,
          actor: "alice@company.com",
          version: {
            release: "v2.0.0",
            edition: "google",
            commit: "0fbfb4619f0a",
            committedAt: "2026-09-03T14:02:11Z",
          },
        }}
        tasks={[]}
      />,
    );

    const stamp = screen.getByTestId("build-stamp");
    expect(stamp).toHaveTextContent(
      "v2.0.0 · google · 0fbfb46 · 2026-09-03 14:02Z",
    );
    expect(stamp.getAttribute("title")).toContain("google edition");
  });

  describe("Team header and Tasks list", () => {
    it("renders Tasks without carets and lists active states directly", async () => {
      const user = userEvent.setup();
      const onSetFilter = vi.fn();
      const onSetView = vi.fn();
      const sampleTasks = [
        { id: "1", title: "Task 1", state: "queued" },
        { id: "2", title: "Task 2", state: "running" },
        { id: "3", title: "Task 3", state: "queued", blocked: true },
      ];
      render(
        <Sidebar
          {...baseProps}
          config={{}}
          tasks={sampleTasks}
          onSetFilter={onSetFilter}
          onSetView={onSetView}
        />,
      );

      const tasksBtn = screen.getByRole("button", { name: /^Tasks/ });
      expect(tasksBtn).toBeInTheDocument();
      expect(
        screen.queryByTestId("toggle-tasks-button"),
      ).not.toBeInTheDocument();
      expect(
        tasksBtn.querySelector('svg[data-testid="ExpandMoreIcon"]'),
      ).toBeNull();
      expect(
        tasksBtn.querySelector('svg[data-testid="ChevronRightIcon"]'),
      ).toBeNull();

      // Breakdown states are visible directly
      expect(
        screen.getByRole("button", { name: /^Waiting/ }),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: /^Running/ }),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("button", { name: /^Blocked/ }),
      ).toBeInTheDocument();

      await user.click(tasksBtn);
      expect(onSetFilter).toHaveBeenCalledWith("all");
      expect(onSetView).toHaveBeenCalledWith("tasks");
    });

    it("does not change stateFilter when onSetView cancels navigation", async () => {
      const user = userEvent.setup();
      const onSetFilter = vi.fn();
      const onSetView = vi.fn(() => false);
      render(
        <Sidebar
          {...baseProps}
          tasks={[{ id: "1", state: "queued" }]}
          onSetFilter={onSetFilter}
          onSetView={onSetView}
        />,
      );

      await user.click(screen.getByRole("button", { name: /^Waiting/ }));
      expect(onSetView).toHaveBeenCalledWith("tasks");
      expect(onSetFilter).not.toHaveBeenCalled();
    });
  });
});

// compactCount is only ever asked for a rail count, so what matters is
// that it stays short and never prints something a reader would take
// for a bug ("1000k", "-1", "NaN").
describe("compactCount", () => {
  it("writes a count the way somebody says one", () => {
    expect(compactCount(0)).toBe("0");
    expect(compactCount(940)).toBe("940");
    expect(compactCount(999)).toBe("999");
    expect(compactCount(1000)).toBe("1k");
    expect(compactCount(1049)).toBe("1k");
    expect(compactCount(2514)).toBe("2.5k");
    expect(compactCount(9949)).toBe("9.9k");
    // Past ten thousand the decimal is noise, and the round number is
    // what somebody reads off a rail.
    expect(compactCount(24300)).toBe("24k");
    expect(compactCount(999499)).toBe("999k");
  });

  it("has nothing to say about a number it was not given", () => {
    expect(compactCount(null)).toBe("");
    expect(compactCount(undefined)).toBe("");
    expect(compactCount("")).toBe("");
  });
});

describe("Sidebar staging entries", () => {
  const sandboxes = [
    {
      name: "docs-preview",
      enabled: true,
      state: "running",
      uiSubpath: "/staging/docs-preview",
    },
    {
      name: "grain-staging",
      enabled: true,
      state: "restarting",
      restarts: 5,
      hostPort: 4280,
    },
    { name: "hidden", enabled: true, state: "running" },
  ];

  it("omits the Staging group when no persistent sandboxes are exposed", () => {
    render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        view="inbox"
        pinnedItems={[]}
        persistentSandboxes={[]}
      />,
    );
    expect(screen.queryByTestId("staging-group")).not.toBeInTheDocument();
    expect(screen.queryByText("Staging")).not.toBeInTheDocument();
  });

  it("lists each exposed sandbox under Staging with its state", () => {
    render(
      <Sidebar
        {...baseProps}
        config={{ listenPort: Number(window.location.port) || 80 }}
        tasks={tasks}
        view="inbox"
        pinnedItems={[]}
        persistentSandboxes={sandboxes}
      />,
    );
    const group = screen.getByTestId("staging-group");
    expect(within(group).getByText("Staging")).toBeInTheDocument();
    const docs = within(group).getByTestId("staging-item-docs-preview");
    expect(docs).toHaveAttribute("data-state", "running");
    const docsLink = within(docs).getByRole("link", { name: /docs-preview/ });
    expect(docsLink).toHaveAttribute("href", "/staging/docs-preview/");
    expect(docsLink).toHaveAttribute("target", "_blank");
    const staging = within(group).getByTestId("staging-item-grain-staging");
    expect(staging).toHaveAttribute("data-state", "crash-looping");
    expect(
      within(staging).getByRole("link", { name: /grain-staging/ }),
    ).toHaveAttribute(
      "href",
      `${window.location.protocol}//${window.location.hostname}:4280/`,
    );
    expect(within(group).queryByText("hidden")).not.toBeInTheDocument();
  });

  it("says when a host port will not load through the page's proxy", () => {
    render(
      <Sidebar
        {...baseProps}
        config={{ listenPort: 8080, proxied: true }}
        tasks={tasks}
        view="inbox"
        pinnedItems={[]}
        persistentSandboxes={sandboxes}
      />,
    );
    const staging = screen.getByTestId("staging-item-grain-staging");
    expect(
      within(staging).getByText("not reachable through this proxy"),
    ).toBeInTheDocument();
    expect(within(staging).getByRole("link").getAttribute("title")).toMatch(
      /ui_subpath/,
    );
    // A sub-path is the UI's own origin: no warning.
    expect(
      within(screen.getByTestId("staging-item-docs-preview")).queryByText(
        "not reachable through this proxy",
      ),
    ).not.toBeInTheDocument();
  });

  it("pins and unpins an entry", async () => {
    localStorage.removeItem("grain.pinned");
    const user = userEvent.setup();
    const { rerender } = render(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        view="inbox"
        persistentSandboxes={sandboxes}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Pin grain-staging" }));
    expect(JSON.parse(localStorage.getItem("grain.pinned"))).toContainEqual({
      kind: "staging",
      id: "grain-staging",
    });
    rerender(
      <Sidebar
        {...baseProps}
        config={null}
        tasks={tasks}
        view="inbox"
        persistentSandboxes={sandboxes}
      />,
    );
    // Listed under Pinned as well as under Staging now.
    expect(screen.getAllByTestId("staging-item-grain-staging")).toHaveLength(2);
    await user.click(
      screen.getAllByRole("button", { name: "Unpin grain-staging" })[0],
    );
    expect(JSON.parse(localStorage.getItem("grain.pinned"))).not.toContainEqual(
      { kind: "staging", id: "grain-staging" },
    );
    localStorage.removeItem("grain.pinned");
  });
});

// The rail folded down to its icons (sidebarCollapse.js). Rendered
// through the same hook App.jsx uses, so the toggle, the "[" key and
// localStorage are the real ones.
function FoldableSidebar(props) {
  const [collapsed, , toggle] = useSidebarCollapsed();
  return (
    <Sidebar
      {...baseProps}
      config={null}
      tasks={tasks}
      collapsed={collapsed}
      onToggleCollapsed={toggle}
      {...props}
    />
  );
}

describe("Sidebar folded to an icon rail", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => localStorage.clear());

  it("has no toggle where the rail cannot fold (the phone drawer)", () => {
    render(<Sidebar {...baseProps} config={null} tasks={tasks} collapsed />);
    expect(
      screen.queryByRole("button", {
        name: /collapse sidebar|expand sidebar/i,
      }),
    ).toBeNull();
    // Without the toggle, `collapsed` is ignored: the drawer is never
    // a rail of icons.
    expect(document.querySelector("aside")).toHaveAttribute(
      "data-collapsed",
      "false",
    );
  });

  it("folds and unfolds from the chevron, saying which it is", async () => {
    const user = userEvent.setup();
    render(<FoldableSidebar view="boards" />);
    const aside = document.querySelector("aside");
    const collapse = screen.getByRole("button", { name: "Collapse sidebar" });
    expect(collapse).toHaveAttribute("aria-expanded", "true");
    expect(aside).toHaveAttribute("data-collapsed", "false");

    await user.click(collapse);
    expect(aside).toHaveAttribute("data-collapsed", "true");
    const expand = screen.getByRole("button", { name: "Expand sidebar" });
    expect(expand).toHaveAttribute("aria-expanded", "false");
    expect(expand).toHaveAttribute("aria-keyshortcuts", "[");

    await user.click(expand);
    expect(aside).toHaveAttribute("data-collapsed", "false");
  });

  it("remembers the choice in this browser's localStorage", async () => {
    const user = userEvent.setup();
    const { unmount } = render(<FoldableSidebar view="boards" />);
    await user.click(screen.getByRole("button", { name: "Collapse sidebar" }));
    expect(localStorage.getItem(SIDEBAR_COLLAPSED_STORAGE_KEY)).toBe("true");
    unmount();

    render(<FoldableSidebar view="boards" />);
    expect(document.querySelector("aside")).toHaveAttribute(
      "data-collapsed",
      "true",
    );
    await user.click(screen.getByRole("button", { name: "Expand sidebar" }));
    expect(localStorage.getItem(SIDEBAR_COLLAPSED_STORAGE_KEY)).toBeNull();
  });

  it("folds on the [ key, but not while somebody is typing one", async () => {
    const user = userEvent.setup();
    render(
      <>
        <input aria-label="somewhere to type" />
        <FoldableSidebar view="boards" />
      </>,
    );
    const aside = document.querySelector("aside");
    await user.keyboard("[[");
    expect(aside).toHaveAttribute("data-collapsed", "true");

    await user.click(screen.getByLabelText("somewhere to type"));
    await user.keyboard("[[");
    expect(aside).toHaveAttribute("data-collapsed", "true");
    expect(screen.getByLabelText("somewhere to type")).toHaveValue("[");

    await user.click(document.body);
    await user.keyboard("[[");
    expect(aside).toHaveAttribute("data-collapsed", "false");
  });

  it("keeps every entry's accessible name and the open page when folded", () => {
    localStorage.setItem(SIDEBAR_COLLAPSED_STORAGE_KEY, "true");
    render(
      <FoldableSidebar
        view="boards"
        boards={[{ id: "b1", name: "Release" }]}
        pinnedItems={[{ kind: "view", id: "inbox" }]}
      />,
    );
    for (const name of [
      /^Inbox/,
      /^Tasks/,
      /^Waiting/,
      /^Running/,
      /^Blocked/,
      /^Boards/,
      /^Sessions/,
      /^Processes/,
      /^CI/,
      /^Repos/,
    ]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
    const boards = screen.getByRole("button", { name: /^Boards/ });
    expect(boards).toHaveAttribute("aria-current", "page");
    expect(boards).toHaveClass("Mui-selected");
    expect(
      screen
        .getAllByRole("button")
        .filter((b) => b.getAttribute("aria-current")),
    ).toHaveLength(1);
    // The things with no icon of their own get one, or go.
    expect(
      screen.getByRole("button", { name: "Customize sidebar" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create" })).toBeInTheDocument();
  });

  it("shows each label as a tooltip on hover and on keyboard focus", async () => {
    localStorage.setItem(SIDEBAR_COLLAPSED_STORAGE_KEY, "true");
    const user = userEvent.setup();
    render(<FoldableSidebar view="tasks" />);

    await user.hover(screen.getByRole("button", { name: /^Processes/ }));
    expect(await screen.findByRole("tooltip")).toHaveTextContent("Processes");
    await user.unhover(screen.getByRole("button", { name: /^Processes/ }));
    await waitFor(() => expect(screen.queryByRole("tooltip")).toBeNull());

    const repos = screen.getByRole("button", { name: /^Repos/ });
    while (document.activeElement !== repos) await user.tab();
    expect(await screen.findByRole("tooltip")).toHaveTextContent("Repos");
    expect(repos).toHaveAccessibleDescription("Repos");
  });

  it("puts the counts on the icons as badges, and leaves zeroes off", () => {
    localStorage.setItem(SIDEBAR_COLLAPSED_STORAGE_KEY, "true");
    render(<FoldableSidebar view="boards" boards={[]} />);
    const tasksCount = within(
      screen.getByRole("button", { name: /^Tasks/ }),
    ).getByText("3");
    expect(tasksCount).toHaveClass("rail-count");
    expect(tasksCount).not.toHaveAttribute("data-zero");
    const boardsCount = within(
      screen.getByRole("button", { name: /^Boards/ }),
    ).getByText("0");
    expect(boardsCount).toHaveAttribute("data-zero", "true");
  });
});

// Customize sidebar's sections (sidebarSections.js): which of the
// panel's destinations this browser shows, kept in localStorage.
describe("Sidebar sections this browser hides", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => localStorage.clear());

  const hideViaCustomize = async (user, label) => {
    await user.click(screen.getByRole("button", { name: "Customize sidebar" }));
    await user.click(screen.getByRole("switch", { name: `Show ${label}` }));
    await user.click(screen.getByRole("button", { name: "Save" }));
  };

  it("shows every section by default", () => {
    render(<Sidebar {...baseProps} config={null} tasks={tasks} />);
    for (const name of [
      /^Tasks/,
      /^Boards/,
      /^Sessions/,
      /^Processes/,
      /^CI/,
      /^Repos/,
    ]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
    expect(screen.getByText("Pinned")).toBeInTheDocument();
  });

  it("hides a section toggled off in Customize sidebar, and keeps it hidden after a reload", async () => {
    const user = userEvent.setup();
    const { unmount } = render(
      <Sidebar {...baseProps} config={null} tasks={tasks} boards={[]} />,
    );
    await hideViaCustomize(user, "Boards");

    expect(screen.queryByRole("button", { name: /^Boards/ })).toBeNull();
    expect(
      screen.getByRole("button", { name: /^Sessions/ }),
    ).toBeInTheDocument();
    expect(
      JSON.parse(localStorage.getItem(SIDEBAR_HIDDEN_STORAGE_KEY)),
    ).toEqual(["boards"]);

    unmount();
    render(<Sidebar {...baseProps} config={null} tasks={tasks} boards={[]} />);
    expect(screen.queryByRole("button", { name: /^Boards/ })).toBeNull();
  });

  it("hides the section, and its count, from the folded rail too", () => {
    localStorage.setItem(SIDEBAR_COLLAPSED_STORAGE_KEY, "true");
    localStorage.setItem(
      SIDEBAR_HIDDEN_STORAGE_KEY,
      JSON.stringify(["processes"]),
    );
    render(<FoldableSidebar view="tasks" />);
    expect(document.querySelector("aside")).toHaveAttribute(
      "data-collapsed",
      "true",
    );
    expect(screen.queryByRole("button", { name: /^Processes/ })).toBeNull();
    // The control to bring it back is still on the rail.
    expect(
      screen.getByRole("button", { name: "Customize sidebar" }),
    ).toBeInTheDocument();
  });

  it("puts everything back on Reset to default", async () => {
    localStorage.setItem(
      SIDEBAR_HIDDEN_STORAGE_KEY,
      JSON.stringify(["boards", "sessions", "pinned"]),
    );
    const user = userEvent.setup();
    render(<Sidebar {...baseProps} config={null} tasks={tasks} boards={[]} />);
    expect(screen.queryByRole("button", { name: /^Sessions/ })).toBeNull();

    await user.click(screen.getByRole("button", { name: "Customize sidebar" }));
    await user.click(screen.getByRole("button", { name: "Reset to default" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(screen.getByRole("button", { name: /^Boards/ })).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /^Sessions/ }),
    ).toBeInTheDocument();
    expect(screen.getByText("Pinned")).toBeInTheDocument();
    expect(localStorage.getItem(SIDEBAR_HIDDEN_STORAGE_KEY)).toBeNull();
  });

  it("keeps a pin of a hidden section under Pinned", () => {
    localStorage.setItem(
      "grain.pinned",
      JSON.stringify([{ kind: "view", id: "boards" }]),
    );
    localStorage.setItem(
      SIDEBAR_HIDDEN_STORAGE_KEY,
      JSON.stringify(["boards"]),
    );
    render(<Sidebar {...baseProps} config={null} tasks={tasks} boards={[]} />);
    expect(screen.getAllByRole("button", { name: /^Boards/ })).toHaveLength(1);
  });

  it("keeps the Customize control when Pinned itself is hidden", () => {
    localStorage.setItem(
      SIDEBAR_HIDDEN_STORAGE_KEY,
      JSON.stringify(["pinned"]),
    );
    render(<Sidebar {...baseProps} config={null} tasks={tasks} />);
    expect(screen.queryByText("Pinned")).toBeNull();
    expect(screen.queryByRole("button", { name: /^Inbox/ })).toBeNull();
    expect(
      screen.getByRole("button", { name: "Customize sidebar" }),
    ).toBeInTheDocument();
  });

  it("ignores ids it does not know, so a later section is never hidden", () => {
    expect(parseHiddenSections(JSON.stringify(["someday", "ci"]))).toEqual([
      "ci",
    ]);
    expect(parseHiddenSections("not json")).toEqual([]);
    expect(parseHiddenSections(JSON.stringify({ ci: true }))).toEqual([]);
  });
});
