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

// App's shell: what a fresh load puts on screen, the banners a
// deployment's own state raises, the rail beside the page, and the poll
// that keeps the list current.
//
// This file and its five siblings were one file, App.test.jsx, until
// grain/task-147. Splitting it was not about length for its own sake:
// what a test in here costs depends on how many tests have run before
// it in the same file. Every byRole query has to decide whether each
// candidate is visible, which is window.getComputedStyle, and jsdom
// answers that by matching every CSS rule in the document -- while
// MUI's emotion runtime inserts one <style> element per rule the first
// time some component needs it and never takes one out. So each pane a
// test opens leaves its rules behind for every query after it. In the
// old single file 1,363 rules had piled up by the last test, one
// unscoped screen.getByRole("button", { name: "Metrics" }) took 2.2s
// against 0.17s for the same call in the first test, and about two
// thirds of the file's 87s was spent inside getComputedStyle.
//
// A test file gets its own jsdom document, so a file only pays for the
// panes it opens itself. Keep a new test with the file whose area it
// belongs to rather than starting a seventh, and keep an eye on what a
// file opens: it is the union of the panes, not the count of the
// tests, that sets the price.
//
// The other five, all of them rendering the same <App />:
//
//   App.test.jsx            the shell, the banners, the rail
//   App.tasks.test.jsx      the backlog and the views over it
//   App.repos.test.jsx      a repo's own page and its releases
//   App.schedules.test.jsx  schedules, templates and suites
//   App.overlays.test.jsx   Settings, System and Metrics
//   App.narrowing.test.jsx  a narrowed list, and the URL that carries it

import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import App, {
  GATE_INTERVAL_MS,
  HIDDEN_POLL_EVERY,
  POLL_INTERVAL_MS,
} from "./App.jsx";
import api from "./api.js";
import {
  config,
  initialTasks,
  resetAppTestState,
  setupApi,
} from "./appTestHarness.js";
import { runTimers } from "./testClock.js";
import { UNCHANGED } from "./poll.js";
import { BOARD_ORDER_SPACING } from "./boardOrder.js";
import { PHONE_QUERY } from "./phone.js";

// A pass-through, like the taskAttributes.js mock below: only the
// default fetch wrapper is faked, so isUnreachable/unreachableReason --
// which App.jsx's poll calls on whatever this mock rejects with --
// stay the real, pure functions rather than becoming undefined.
vi.mock("./api.js", async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, default: vi.fn() };
});

// findAlertRow opens the top bar's alerts panel (AlertsButton.jsx), once
// the bell has something to show, and finds the row whose alert id is
// id -- a string, or a pattern for an id that carries a team or a
// connection.
async function findAlertRow(id) {
  const bell = await screen.findByTestId("alerts-button");
  await waitFor(() => expect(bell.dataset.severity).not.toBe("none"));
  if (!screen.queryByTestId("alerts-panel")) fireEvent.click(bell);
  await screen.findByTestId("alerts-panel");
  const matches = (rowId) =>
    typeof id === "string" ? rowId === id : id.test(rowId);
  return waitFor(() => {
    const row = screen
      .getAllByTestId("alert-row")
      .find((r) => matches(r.dataset.alertId));
    expect(row).toBeTruthy();
    return row;
  });
}

// How the poll test at the foot of this file counts what a tick costs.
//
// attributesFor is the first thing every task view asks for as it draws
// -- which attributes this deployment has -- and the first thing a
// narrowing asks for before it can answer (taskFilters.js). Nothing else
// calls it, and comparing one poll's response against the last one
// certainly does not. So a count of nought over a tick is "no view
// re-rendered and no list was narrowed again", which is exactly the
// claim.
//
// A pass-through: every other export is the real one and this one calls
// the real one, so nothing else in this file sees a different module.
const { narrowings } = vi.hoisted(() => ({ narrowings: { count: 0 } }));
vi.mock("./taskAttributes.js", async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    attributesFor: (config) => {
      narrowings.count++;
      return actual.attributesFor(config);
    },
  };
});

describe("App", () => {
  afterEach(resetAppTestState);

  it("loads config and the task list on mount", async () => {
    setupApi();
    render(<App />);

    expect(await screen.findByText("Fix bug")).toBeInTheDocument();
    expect(screen.getByText("Add feature")).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith("/api/config");
    // Conditionally: the poll of the task list carries the ETag it was
    // last answered with, so a tick that has nothing to say costs a 304
    // rather than the whole list (api.js, poll.js).
    expect(api).toHaveBeenCalledWith("/api/tasks", { poll: true });
  });

  // Nothing on this screen reads GET /api/state-repo any more (the
  // Settings pane fetches its own). Asked on load, it once cost every
  // reader who was not an administrator a 403 and a console warning
  // (task 2232).
  describe("the state repo", () => {
    const withConfig = (overrides) => {
      setupApi();
      const fake = api.getMockImplementation();
      api.mockImplementation((path, opts) => {
        if (path === "/api/config")
          return Promise.resolve({ ...config, ...overrides });
        if (path === "/api/state-repo")
          return Promise.resolve({ available: false });
        return fake(path, opts);
      });
    };

    it("is not asked for on load", async () => {
      withConfig({ actor: "pat" });
      const warn = vi.spyOn(console, "warn");
      render(<App />);

      expect(await screen.findByText("Fix bug")).toBeInTheDocument();
      expect(api).toHaveBeenCalledWith("/api/config");
      expect(api).not.toHaveBeenCalledWith("/api/state-repo");
      expect(warn).not.toHaveBeenCalledWith(
        "grain: state repo unavailable",
        expect.anything(),
      );
      warn.mockRestore();
    });
  });

  // What an unfetched list shows (grain/task-235). Every list App holds
  // starts out [], so "this deployment has none of these" and "these
  // have not arrived yet" used to render identically -- and a page
  // loaded at /boards/<id> drew the boards list's own "No boards yet."
  // for the whole of the fetch, to a reader who had just asked for a
  // board by name. Neither of these tests is about boards particularly:
  // they are the two halves of the fix, held where the shell's own
  // tests are.
  describe("the first load", () => {
    const board = {
      id: "board-1",
      name: "Everything",
      kind: "list",
      query: "",
      folder: "",
      orderKey: 1,
    };

    // deferring answers every endpoint but one and hands back the lever
    // that answers that one, so a test can look at the screen at a
    // moment the real app only reaches on a slow machine.
    const deferring = (held) => {
      const realImpl = api.getMockImplementation();
      let land;
      api.mockImplementation((path, opts) =>
        path === held
          ? new Promise((resolve) => {
              land = () => resolve(realImpl(path, opts));
            })
          : realImpl(path, opts),
      );
      return () => land();
    };

    // A macrotask, which is past every already-resolved promise in the
    // mount effect's chain: whatever has not rendered after this is
    // waiting on the held endpoint and nothing else.
    const flush = () =>
      act(async () => {
        await new Promise((resolve) => setTimeout(resolve, 0));
      });

    it("draws nothing at all while the boards a URL names are in flight", async () => {
      window.history.replaceState(null, "", "/boards/board-1");
      setupApi(initialTasks, [], [], [], [board]);
      const landBoards = deferring("/api/boards");
      render(<App />);

      // Config, the tasks and the other three lists have all answered
      // by here. The boards have not -- so the screen says nothing,
      // rather than the boards page's empty state or the rail's "0"s.
      await flush();
      expect(screen.queryByText("No boards yet.")).toBeNull();
      expect(screen.queryByRole("heading", { name: "Boards" })).toBeNull();
      expect(document.querySelector(".loading-screen")).not.toBeNull();

      landBoards();
      expect(
        await screen.findByRole("heading", { name: "Everything" }),
      ).toBeInTheDocument();
    });

    it("asks for the lists without waiting on the config round trip", async () => {
      setupApi();
      const landConfig = deferring("/api/config");
      render(<App />);

      // All five list requests are out while config is still open,
      // which is the difference between one round trip of waiting and
      // two of it.
      await waitFor(() => expect(api).toHaveBeenCalledWith("/api/boards"));
      expect(api).toHaveBeenCalledWith("/api/tasks", { poll: true });
      for (const path of ["/api/schedules", "/api/templates", "/api/suites"]) {
        expect(api).toHaveBeenCalledWith(path);
      }
      expect(document.querySelector(".loading-screen")).not.toBeNull();

      landConfig();
      expect(await screen.findByText("Fix bug")).toBeInTheDocument();
    });
  });

  it("does not send anybody to a deployment-wide model setting under all teams", async () => {
    // There is none: connections and models are each team's own
    // (grain/task-1601), so with no one team selected there is nothing to
    // point at, whatever the deployment as a whole has.
    setupApi();
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({
            ...config,
            modelsConfigured: false,
          })
        : realImpl(path, opts),
    );

    render(<App />);

    expect(await screen.findByText("Fix bug")).toBeInTheDocument();
    expect(screen.queryByText(/no model/i)).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Add connection" }),
    ).not.toBeInTheDocument();
  });

  it("shows no reconciler-down alert by default", async () => {
    setupApi();
    render(<App />);

    await screen.findByText("Fix bug");
    expect(
      screen.queryByText(/reconcile loop has stopped/i),
    ).not.toBeInTheDocument();
    // No alerts at all: the bell is quiet.
    expect(screen.getByTestId("alerts-button")).toHaveAttribute(
      "data-severity",
      "none",
    );
  });

  it("raises a critical alert when the deployment's reconciler has died (bwsalmon/agents#576)", async () => {
    setupApi();
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({ ...config, reconcilerDown: true })
        : realImpl(path, opts),
    );

    render(<App />);

    const row = await findAlertRow("reconciler-down");
    expect(row).toHaveAttribute("data-severity", "critical");
    expect(row).toHaveTextContent(/reconcile loop has stopped/i);
  });

  it("opens Settings → Config repo from the state-sync-failing alert and from the Inbox state-sync row", async () => {
    setupApi();
    const realImpl = api.getMockImplementation();
    const stateRepoSyncFailure = {
      since: "2026-10-06T08:57:32Z",
      error: "push rejected",
      repo: "grain/config",
    };
    api.mockImplementation((path, opts) => {
      if (path === "/api/config")
        return Promise.resolve({ ...config, stateRepoSyncFailure });
      if (path === "/api/state-repo")
        return Promise.resolve({ available: true, repo: "grain/config" });
      return realImpl(path, opts);
    });

    render(<App />);

    const row = await findAlertRow("state-sync-failing");
    fireEvent.click(within(row).getByRole("button", { name: "Config repo" }));
    await waitFor(() =>
      expect(window.location.pathname).toBe("/settings/config-repo"),
    );

    window.history.pushState({}, "", "/inbox");
    window.dispatchEvent(new PopStateEvent("popstate"));
    const inboxGroup = await screen.findByText(
      "Settings are not being exported",
    );
    const inboxSection = inboxGroup.closest(".inbox-group");
    fireEvent.click(
      within(inboxSection).getByRole("button", { name: "Config repo" }),
    );
    await waitFor(() =>
      expect(window.location.pathname).toBe("/settings/config-repo"),
    );
  });

  // grain/task-132: an operator opening grain during a provider's
  // usage-limit window used to see a queue of ready tasks and nothing
  // running, with no explanation on screen at all.
  it("raises an alert while the agent's usage limit has dispatch paused", async () => {
    setupApi();
    const realImpl = api.getMockImplementation();
    const agentPause = {
      paused: true,
      until: "2026-09-03T17:00:00Z",
      reason: "claude: usage limit reached; resets at 2026-09-03T17:00:00Z",
      secondsRemaining: 7200,
    };
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({ ...config, agentPause })
        : realImpl(path, opts),
    );

    render(<App />);

    const row = await findAlertRow(/^agent-pause\//);
    expect(row).toHaveTextContent(/agent usage limit reached/i);
    expect(row).toHaveTextContent(/about 2h 0m/);
  });

  // grain/task-38: the deliberate halt beside that involuntary one --
  // an operator's own, or grain's around a check of an upgrade it has
  // just installed.
  it("raises an alert while the pipeline itself is paused", async () => {
    setupApi();
    const realImpl = api.getMockImplementation();
    const pipelinePause = {
      paused: true,
      since: "2026-09-03T17:00:00Z",
      reason: "checking the upgrade to main (task 412)",
      byKind: "automation",
      byId: "upgrade",
      untilTask: "412",
    };
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({ ...config, pipelinePause })
        : realImpl(path, opts),
    );

    render(<App />);

    const row = await findAlertRow("pipeline-pause");
    expect(row).toHaveTextContent(/no new tasks are being picked up/i);
    expect(row).toHaveTextContent(/lifts itself when task 412 finishes/i);
  });

  it("raises no pause alert on a deployment that is dispatching", async () => {
    setupApi();
    render(<App />);

    await screen.findByText("Fix bug");
    expect(
      screen.queryByText(/agent usage limit reached/i),
    ).not.toBeInTheDocument();
  });

  // Both are rows of one panel now, worst first: a dead reconcile loop
  // is the larger fact, so it leads, and the pause is still there below
  // it for whoever reads on (grain/task-4008).
  it("lists the reconciler-down alert above a pause when both are true", async () => {
    setupApi();
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({
            ...config,
            reconcilerDown: true,
            agentPause: {
              paused: true,
              until: "2026-09-03T17:00:00Z",
              secondsRemaining: 7200,
            },
          })
        : realImpl(path, opts),
    );

    render(<App />);

    await findAlertRow("reconciler-down");
    const rows = screen.getAllByTestId("alert-row");
    expect(rows.map((r) => r.dataset.alertId)).toEqual([
      "reconciler-down",
      "agent-pause//0",
    ]);
  });

  // The standing warnings are no longer a strip across the top of the
  // page (grain/task-4008): nothing is drawn above the rail and the page
  // unless the daemon cannot be reached (DisconnectedBanner).
  it("draws no banner above the page for a standing warning", async () => {
    setupApi();
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({ ...config, reconcilerDown: true })
        : realImpl(path, opts),
    );

    const { container } = render(<App />);

    await screen.findByText("Fix bug");
    await waitFor(() =>
      expect(screen.getByTestId("alerts-button")).toHaveAttribute(
        "data-severity",
        "critical",
      ),
    );
    const shell = container.querySelector(".app-shell");
    expect(shell.firstElementChild).toHaveClass("app-body");
    expect(screen.queryByTestId("top-bar")).toBeNull();
    expect(
      screen.queryByText(/reconcile loop has stopped/i),
    ).not.toBeInTheDocument();
  });

  // grain/task-4007: the page crumb is one of grain's own labels, lower
  // case like the rest of its navigation, and written that way rather
  // than transformed so what a screen reader says is what is shown.
  it("names the page in the header rail in lower case", async () => {
    setupApi();
    render(<App />);
    const crumb = await screen.findByTestId("top-bar-page");
    expect(crumb.textContent).not.toBe("");
    expect(crumb.textContent).toBe(crumb.textContent.toLowerCase());
  });

  // The alert's own "Resume now" -- an operator who has just topped a
  // plan up has no reason to wait out a window that no longer applies.
  it("lifts the pause and re-reads the config when Resume now is clicked", async () => {
    setupApi();
    const realImpl = api.getMockImplementation();
    let paused = true;
    api.mockImplementation((path, opts) => {
      if (path === "/api/config") {
        return Promise.resolve(
          paused
            ? {
                ...config,
                agentPause: {
                  paused: true,
                  until: "2026-09-03T17:00:00Z",
                  secondsRemaining: 7200,
                  canLift: true,
                },
              }
            : config,
        );
      }
      if (path === "/api/pause" && opts?.method === "DELETE") {
        paused = false;
        return Promise.resolve({
          enabled: true,
          lifted: true,
          pause: { paused: false },
        });
      }
      return realImpl(path, opts);
    });

    render(<App />);
    const row = await findAlertRow(/^agent-pause\//);
    fireEvent.click(within(row).getByRole("button", { name: /resume now/i }));

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/pause", { method: "DELETE" }),
    );
    await waitFor(() =>
      expect(
        screen.queryByText(/agent usage limit reached/i),
      ).not.toBeInTheDocument(),
    );
  });

  // grain/task-69: the deployment's name in the tab strip, which is the
  // one piece of chrome the app cannot draw into. Name first, because a
  // narrow tab truncates its title from the end.
  it("titles the tab with the environment name when one is configured", async () => {
    setupApi();
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({ ...config, environmentName: "staging" })
        : realImpl(path, opts),
    );

    render(<App />);

    await screen.findByText("Fix bug");
    await waitFor(() => expect(document.title).toBe("staging — grain"));
  });

  // grain/task-368: every absolute time under App is printed on the
  // deployment's own clock, which /api/config carries -- not on the
  // browser's, which is a different clock from the one the daemon fires
  // schedules against.
  it("prints times on the deployment's own clock, not the browser's", async () => {
    const schedule = {
      id: "sched-1",
      title: "Nightly dependency bump",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      timeZone: "America/Los_Angeles",
      recurrence: { kind: "daily", timeOfDay: "09:00" },
      enabled: true,
      nextRunAt: "2026-08-29T16:00:00Z",
    };
    setupApi(initialTasks, [schedule]);
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({ ...config, timeZone: "America/Los_Angeles" })
        : realImpl(path, opts),
    );
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Processes/ }));
    await screen.findByText("Nightly dependency bump");

    const expected = new Date(schedule.nextRunAt).toLocaleString(undefined, {
      timeZone: "America/Los_Angeles",
    });
    expect(
      document.querySelector('.process-row[data-kind="schedule"] .process-next')
        .textContent,
    ).toContain(expected);
    // And the schedule form labels its time field with that same zone,
    // since what is typed there is read on it.
    await user.click(screen.getByText("Nightly dependency bump"));
    expect(
      await screen.findByLabelText(/Time \(America\/Los_Angeles/),
    ).toBeInTheDocument();
  });

  // grain/task-322: a deployment that never set a clock reports "", and
  // a browser left on "Browser default" in Settings → Preferences prints
  // every time in its own zone -- not in UTC, which is what the provider
  // used to be handed whenever the deployment's column was empty. Which
  // zone each time was asked for is read off toLocaleString itself, since
  // the test runner's own zone may well be UTC already.
  async function nextRunZone(configZone) {
    const schedule = {
      id: "sched-1",
      title: "Nightly dependency bump",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      recurrence: { kind: "daily", timeOfDay: "09:00" },
      enabled: true,
      nextRunAt: "2026-08-29T16:00:00Z",
    };
    setupApi(initialTasks, [schedule]);
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({ ...config, timeZone: configZone })
        : realImpl(path, opts),
    );
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");
    const spy = vi
      .spyOn(Date.prototype, "toLocaleString")
      .mockImplementation(function (_locale, opts) {
        return `[${opts?.timeZone || "browser"}]`;
      });
    try {
      await user.click(screen.getByRole("button", { name: /^Processes/ }));
      await screen.findByText("Nightly dependency bump");
      return document.querySelector(
        '.process-row[data-kind="schedule"] .process-next',
      ).textContent;
    } finally {
      spy.mockRestore();
    }
  }

  it("prints times in the browser's own zone when neither the deployment nor Preferences name one", async () => {
    expect(await nextRunZone("")).toContain("[browser]");
  });

  it("prints times in the zone picked in Preferences over the deployment's", async () => {
    localStorage.setItem("grain.timeZone", "Asia/Tokyo");
    try {
      expect(await nextRunZone("America/Los_Angeles")).toContain(
        "[Asia/Tokyo]",
      );
    } finally {
      localStorage.removeItem("grain.timeZone");
    }
  });

  it("leaves the tab titled just grain on an unnamed deployment", async () => {
    setupApi();
    render(<App />);

    await screen.findByText("Fix bug");
    await waitFor(() => expect(document.title).toBe("grain"));
  });

  // The Caches page combining proxy and actions caches (grain/task-321, grain/task-357).
  // ...which is System → Caches now, and /caches goes there.
  it("renders the Caches page at System → Caches", async () => {
    window.history.pushState({}, "", "/caches");
    setupApi();
    render(<App />);

    expect(
      await screen.findByText("apt packages: 412 packages, 3.0 GiB"),
    ).toBeInTheDocument();
    expect(
      await screen.findByText("acme/widgets: 3.0 GiB"),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe("/system/caches"),
    );
  });

  it("shows a loading screen with the large mark until config loads (bwsalmon/agents#555)", async () => {
    setupApi();
    let resolveConfig;
    const configPromise = new Promise((resolve) => {
      resolveConfig = resolve;
    });
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config" ? configPromise : realImpl(path, opts),
    );

    render(<App />);

    expect(screen.getByTitle("grain")).toHaveAttribute("width", "320");
    expect(
      screen.queryByRole("button", { name: "+ New task" }),
    ).not.toBeInTheDocument();

    resolveConfig(config);

    await screen.findByText("Fix bug");
    // 32: the sidebar's mark, the smallest size the v2 glyphs still read
    // as shapes at (Sidebar.jsx).
    expect(screen.getByTitle("grain")).toHaveAttribute("width", "32");
  });

  it("shows access denied screen when user is not on allowed list", async () => {
    setupApi();
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({
            ...config,
            authenticated: true,
            allowed: false,
            accessDenied: true,
            actor: "unauthorized@example.com",
          })
        : realImpl(path, opts),
    );

    render(<App />);

    expect(
      await screen.findByRole("heading", { name: /access denied/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /you do not have access to this server and need to request it/i,
      ),
    ).toBeInTheDocument();
    expect(screen.getAllByText("unauthorized@example.com")).toHaveLength(2);
  });

  it("shows access denied screen when /api/config returns 403 forbidden", async () => {
    setupApi();
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.reject(new Error("403 Forbidden: access denied"))
        : realImpl(path, opts),
    );

    render(<App />);

    expect(
      await screen.findByRole("heading", { name: /access denied/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        /you do not have access to this server and need to request it/i,
      ),
    ).toBeInTheDocument();
  });

  // The poll's guard reads accessDenied, so the effect that owns the
  // interval has to be re-run when it flips -- a closure taken at mount
  // still holds false and keeps re-reading every list for a screen that
  // shows none of them.
  it("stops polling once access is denied", async () => {
    vi.useFakeTimers();
    setupApi();
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.reject(new Error("403 Forbidden: access denied"))
        : realImpl(path, opts),
    );

    render(<App />);
    await runTimers();
    expect(
      screen.getByRole("heading", { name: /access denied/i }),
    ).toBeInTheDocument();

    const tasksCalls = () =>
      api.mock.calls.filter((c) => c[0] === "/api/tasks").length;
    const callsBefore = tasksCalls();
    await runTimers(POLL_INTERVAL_MS * 3);
    expect(tasksCalls()).toBe(callsBefore);
  });

  // The phone shell (src/phone.js, PhoneNav.jsx). jsdom has no
  // matchMedia at all, so every test above this one gets `false` from
  // useMediaQuery and renders the rail beside the page, which is the
  // layout a tablet and a desktop both keep -- these two stub the answer
  // to say which shell each viewport gets.
  describe("on a phone", () => {
    function stubViewport(isPhone) {
      window.matchMedia = (query) => ({
        matches: query === PHONE_QUERY && isPhone,
        media: query,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
      });
    }

    afterEach(() => {
      delete window.matchMedia;
      delete document.documentElement.dataset.phone;
    });

    it("puts the nav rail in a drawer behind a top bar, and closes it again on a tap", async () => {
      stubViewport(true);
      setupApi();
      render(<App />);
      await screen.findByText("Fix bug");

      // The task list is on screen as usual; the rail is not, and the
      // stylesheet has been told which shell this is.
      expect(document.documentElement.dataset.phone).toBe("true");
      expect(screen.queryByText("Repos")).toBeNull();
      // The gear is on the phone's own bar (settings-panel.md §4.1).
      expect(screen.getByTestId("phone-settings")).toBeInTheDocument();

      fireEvent.click(screen.getByRole("button", { name: "Open navigation" }));
      expect(await screen.findByText("Repos")).toBeInTheDocument();
      // ...and System is at the foot of the drawer.
      expect(
        screen.getByRole("button", { name: "System" }),
      ).toBeInTheDocument();

      // The same rail, doing the same thing it does beside the page --
      // and getting out of the way once it has done it.
      fireEvent.click(screen.getByText("Inbox"));
      expect(
        await screen.findByRole("heading", { name: "Inbox" }),
      ).toBeInTheDocument();
      await waitFor(() => expect(screen.queryByText("Repos")).toBeNull());
    });

    it("keeps the rail beside the page on anything wider", async () => {
      stubViewport(false);
      setupApi();
      render(<App />);
      await screen.findByText("Fix bug");

      expect(document.documentElement.dataset.phone).toBe("false");
      expect(screen.getByText("Repos")).toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: "Open navigation" }),
      ).toBeNull();
    });
  });

  // grain/task-81: a board's folder and its place in that folder are
  // dragged rather than asked for, and the drag is a PATCH against the
  // same row an agent would edit in tables/board.json.
  describe("moving a board", () => {
    const board = (id, name, folder, orderKey) => ({
      id,
      name,
      folder,
      kind: "list",
      description: "",
      query: "",
      columns: [],
      orderKey,
    });
    const seeded = [
      board("board-1", "Waiting on me", "", 0),
      board("board-2", "Weekly report", "", 1000),
      board("board-3", "Flaky tests", "Reports", 0),
    ];
    // By text rather than by role: a role query walks the whole
    // accessibility tree, and the whole App is a big tree to walk six
    // times a test -- App.test.jsx's other queries are text queries for
    // the same reason.
    const railRow = (name) => screen.getByText(name).closest("li");
    // BoardList marks a drag a tick after dragstart (Chromium cancels a
    // drag whose row moves inside the handler), so a drop has to wait
    // for that tick the way a real one always does.
    const dragStart = async (row) => {
      fireEvent.dragStart(row);
      await act(() => new Promise((resolve) => setTimeout(resolve, 0)));
    };
    const patchOf = (id) =>
      api.mock.calls.find(
        ([path, opts]) =>
          path === `/api/boards/${id}` && opts?.method === "PATCH",
      );

    // Both halves of "did the drop write anything" in one test: a whole
    // App render costs a second or two, and the question they ask -- is
    // this drop worth a request -- is one question.
    it("writes a new order key when a board is dragged up the rail", async () => {
      const user = userEvent.setup();
      const state = setupApi(initialTasks, [], [], [], seeded);
      render(<App />);
      await user.click(await screen.findByRole("button", { name: /^Boards/ }));
      await screen.findByText("Weekly report");

      // Dropped above the board that already follows it: that is where
      // it is, so nothing is written.
      await dragStart(railRow("Waiting on me"));
      fireEvent.dragOver(railRow("Weekly report"));
      fireEvent.drop(railRow("Weekly report"));
      expect(patchOf("board-1")).toBeUndefined();

      await dragStart(railRow("Weekly report"));
      fireEvent.dragOver(railRow("Waiting on me"));
      fireEvent.drop(railRow("Waiting on me"));

      await waitFor(() => expect(patchOf("board-2")).toBeDefined());
      // Nothing else is touched, and the key is a whole spacing below
      // the board it landed above -- there is no neighbour on the other
      // side to halve a gap with.
      expect(JSON.parse(patchOf("board-2")[1].body)).toEqual({
        folder: "",
        orderKey: -BOARD_ORDER_SPACING,
      });
      expect(patchOf("board-1")).toBeUndefined();
      await waitFor(() =>
        expect(state.boardsState.map((b) => b.id)).toEqual([
          "board-2",
          "board-1",
          "board-3",
        ]),
      );
      // What is slow here is the App render this and every test above it
      // does, and the last tests in a long file are the ones a loaded CI
      // runner starves. Nothing in the move itself waits on anything but
      // a mocked request, so the file-wide budget in ui/vite.config.js is
      // the right bound on it.
    });

    it("files a board under the folder it is dropped on", async () => {
      const user = userEvent.setup();
      const state = setupApi(initialTasks, [], [], [], seeded);
      render(<App />);
      await user.click(await screen.findByRole("button", { name: /^Boards/ }));
      await screen.findByText("Waiting on me");
      const heading = screen.getByText("Reports").closest("li");

      await dragStart(railRow("Waiting on me"));
      fireEvent.dragOver(heading);
      fireEvent.drop(heading);

      await waitFor(() => expect(patchOf("board-1")).toBeDefined());
      expect(JSON.parse(patchOf("board-1")[1].body)).toEqual({
        folder: "Reports",
        orderKey: -BOARD_ORDER_SPACING,
      });
      // ...and the rail reads it back rather than believing its own
      // drag: the board is under the folder heading now.
      await waitFor(() =>
        expect(state.boardsState.find((b) => b.id === "board-1").folder).toBe(
          "Reports",
        ),
      );
    });
  });

  // The poll is a timer, so this test runs the clock rather than waiting
  // on it: POLL_INTERVAL_MS is imported from App.jsx rather than written
  // out here, so raising the interval cannot leave a test behind that
  // waits the old one and then says the poll is broken.
  it("polls the task list on an interval", async () => {
    vi.useFakeTimers();
    setupApi();
    render(<App />);
    // getBy after a fake-clock step rather than findBy, since waitFor
    // cannot poll under a fake clock -- see runTimers.
    await runTimers();
    expect(screen.getByText("Fix bug")).toBeInTheDocument();

    const tasksCalls = () =>
      api.mock.calls.filter((c) => c[0] === "/api/tasks").length;
    const callsBefore = tasksCalls();

    // A hair short of the interval asks for nothing new...
    await runTimers(POLL_INTERVAL_MS - 100);
    expect(tasksCalls()).toBe(callsBefore);

    // ...and crossing it re-reads the list.
    await runTimers(200);
    expect(tasksCalls()).toBeGreaterThan(callsBefore);
  });

  // A background tab skips the poll, which left the inbox's desktop
  // notifications -- written for exactly that tab -- with nothing new to
  // announce until the reader came back. With them turned on, a hidden
  // tab still re-reads the task list every HIDDEN_POLL_EVERY ticks, and
  // only the task list; without them it reads nothing at all.
  it("keeps reading the task list in a hidden tab only while notifications are on", async () => {
    vi.useFakeTimers();
    setupApi();
    const visibility = vi
      .spyOn(document, "visibilityState", "get")
      .mockReturnValue("visible");
    window.Notification = { permission: "granted" };
    try {
      render(<App />);
      await runTimers();
      expect(screen.getByText("Fix bug")).toBeInTheDocument();
      visibility.mockReturnValue("hidden");
      const calls = (path) =>
        api.mock.calls.filter((c) => c[0] === path).length;
      const before = api.mock.calls.length;
      const tasksBefore = calls("/api/tasks");

      await runTimers(POLL_INTERVAL_MS * (HIDDEN_POLL_EVERY - 1) + 100);
      expect(api.mock.calls.length).toBe(before);

      await runTimers(POLL_INTERVAL_MS);
      expect(calls("/api/tasks")).toBe(tasksBefore + 1);
      expect(api.mock.calls.slice(before).map((c) => c[0])).toEqual([
        "/api/tasks",
      ]);

      // Turned off, a hidden tab reads nothing at all, the base-branch
      // gates' own minute's clock included
      // (docs/test-runs/2026-10-07-inbox-read-only-hidden-tab.md)...
      localStorage.setItem("grain.inboxNotifications", "off");
      const quiet = api.mock.calls.length;
      await runTimers(GATE_INTERVAL_MS * 3);
      expect(api.mock.calls.slice(quiet).map((c) => c[0])).toEqual([]);

      // ...and shown again, it reads the gates at once rather than on
      // that clock's next tick.
      const gatesBefore = calls("/api/base-branch-gates");
      visibility.mockReturnValue("visible");
      document.dispatchEvent(new Event("visibilitychange"));
      await runTimers(0);
      expect(calls("/api/base-branch-gates")).toBe(gatesBefore + 1);
    } finally {
      visibility.mockRestore();
      delete window.Notification;
      localStorage.removeItem("grain.inboxNotifications");
    }
  });

  // The other half of that poll: what it costs when the answer is the
  // same answer, which on any real deployment is most ticks.
  //
  // `await api(...)` parses fresh JSON every time, so every three
  // seconds every view was handed a brand new array of identical rows --
  // a re-render of everything holding it and a miss in every memo keyed
  // on its identity, which on a store of 929 tasks was six dashboard
  // panels re-sorting and re-filtering all of them for nothing
  // (docs/hand-runs/2026-09-07-panel-dashboard.md, section 5). A poll
  // keeps what it already holds when the new answer says the same thing
  // (poll.js), and this is what pins it.
  //
  // How it counts: the rows the app is holding report every read of
  // their title. The list draws titles, its toolbar's menus read them,
  // and every narrowing over them reads them -- so a tick that
  // re-rendered anything would run the count up well past the one look
  // per task the comparison itself takes.
  it("re-renders nothing when a poll brings back the same tasks", async () => {
    vi.useFakeTimers();
    setupApi();
    // A fresh copy of the same rows per request, which is what
    // `await api(...)` really hands back: JSON.parse builds a new object
    // graph every time, however little has changed. The fake above
    // answers with the one array it holds, which would hide the whole
    // question.
    const rest = api.getMockImplementation();
    api.mockImplementation((path, opts) => {
      if (path === "/api/tasks" && (opts?.method || "GET") === "GET")
        return Promise.resolve(initialTasks.map((t) => ({ ...t })));
      return rest(path, opts);
    });

    render(<App />);
    await runTimers();
    expect(screen.getByText("Fix bug")).toBeInTheDocument();

    narrowings.count = 0;
    await runTimers(POLL_INTERVAL_MS + 100);
    // Nothing asked what this deployment's attributes are, which every
    // task view does as it draws and every narrowing does before it can
    // answer: the tick compared the response against the one already
    // held, found it the same, and stopped there.
    expect(narrowings.count).toBe(0);

    // And it is still a poll: a list that has actually changed still
    // arrives on screen.
    initialTasks.push({
      id: "9",
      title: "Chase the flake",
      state: "queued",
      repo: "acme/widgets",
      blocked: false,
      capabilities: [],
      reads: [],
    });
    try {
      await runTimers(POLL_INTERVAL_MS + 100);
      expect(screen.getByText("Chase the flake")).toBeInTheDocument();
    } finally {
      initialTasks.pop();
    }
  });

  // And the tick before that comparison: a poll that already holds the
  // current list is answered by the server with a 304 and no body at all
  // (pkg/ui/task_list_cache.go), which api.js hands back as UNCHANGED --
  // 1.7 MB neither sent nor parsed on a store with real history. What
  // this pins is that the shell treats it as "keep what you have"
  // rather than as a list of no tasks, which is what a sentinel read as
  // an ordinary answer would look like.
  it("keeps the list it has when the server says nothing has changed", async () => {
    vi.useFakeTimers();
    setupApi();
    const rest = api.getMockImplementation();
    let unchanged = false;
    api.mockImplementation((path, opts) => {
      if (path === "/api/tasks" && (opts?.method || "GET") === "GET") {
        // The two answers a polled list can give, in the shapes api.js
        // really produces them: the sentinel for a 304, and a freshly
        // parsed copy of the rows for a 200.
        if (unchanged) return Promise.resolve(UNCHANGED);
        return Promise.resolve(initialTasks.map((t) => ({ ...t })));
      }
      return rest(path, opts);
    });

    render(<App />);
    await runTimers();
    expect(screen.getByText("Fix bug")).toBeInTheDocument();

    unchanged = true;
    narrowings.count = 0;
    await runTimers(POLL_INTERVAL_MS + 100);

    expect(narrowings.count).toBe(0);
    expect(screen.getByText("Fix bug")).toBeInTheDocument();
    expect(screen.getByText("Add feature")).toBeInTheDocument();

    // And the poll that does bring something back still lands.
    unchanged = false;
    initialTasks.push({
      id: "9",
      title: "Chase the flake",
      state: "queued",
      repo: "acme/widgets",
      blocked: false,
      capabilities: [],
      reads: [],
    });
    try {
      await runTimers(POLL_INTERVAL_MS + 100);
      expect(screen.getByText("Chase the flake")).toBeInTheDocument();
    } finally {
      initialTasks.pop();
    }
  });

  it("polls the open console session conditionally, keeps state on UNCHANGED, and skips overlapping ticks", async () => {
    vi.useFakeTimers();
    window.history.pushState({}, "", "/session/director");
    setupApi();
    const rest = api.getMockImplementation();
    const sessionTask = {
      id: "session-1",
      title: "Director",
      state: "waiting_input",
      description: "",
      capabilities: [],
      comments: [
        {
          id: "c1",
          author: "director",
          body: "Standing by for instructions.",
          createdAt: "2026-09-03T12:00:00Z",
        },
      ],
    };
    let respondSessionTask = () => Promise.resolve({ ...sessionTask });
    api.mockImplementation((path, opts) => {
      if (
        path === "/api/console-sessions" &&
        (opts?.method || "GET") === "GET"
      ) {
        return Promise.resolve({
          sessions: [
            { id: "director", title: "Director", taskId: "session-1" },
          ],
        });
      }
      if (
        path === "/api/tasks/session-1" &&
        (opts?.method || "GET") === "GET"
      ) {
        return respondSessionTask(opts);
      }
      return rest(path, opts);
    });

    render(<App />);
    await runTimers();
    expect(
      screen.getByText("Standing by for instructions."),
    ).toBeInTheDocument();

    const sessionCalls = () =>
      api.mock.calls.filter(([p]) => p === "/api/tasks/session-1");
    expect(sessionCalls().length).toBeGreaterThanOrEqual(1);
    expect(sessionCalls().at(-1)[1]).toEqual({ poll: true });

    // A 304 Not Modified (UNCHANGED) leaves the existing session task in state.
    respondSessionTask = () => Promise.resolve(UNCHANGED);
    const beforeUnchanged = sessionCalls().length;
    await runTimers(POLL_INTERVAL_MS + 100);
    expect(sessionCalls().length).toBe(beforeUnchanged + 1);
    expect(sessionCalls().at(-1)[1]).toEqual({ poll: true });
    expect(
      screen.getByText("Standing by for instructions."),
    ).toBeInTheDocument();

    // While a session poll request is still in flight, subsequent interval
    // ticks do not fire overlapping requests.
    let resolveInFlight;
    respondSessionTask = () =>
      new Promise((resolve) => {
        resolveInFlight = resolve;
      });
    const beforeSlow = sessionCalls().length;
    await runTimers(POLL_INTERVAL_MS);
    expect(sessionCalls().length).toBe(beforeSlow + 1);

    // Two more poll intervals pass while the slow fetch is still pending:
    // no overlapping /api/tasks/session-1 call is made.
    await runTimers(POLL_INTERVAL_MS * 2);
    expect(sessionCalls().length).toBe(beforeSlow + 1);

    // Once the in-flight request settles, the next tick polls again.
    resolveInFlight({
      ...sessionTask,
      comments: [
        ...sessionTask.comments,
        {
          id: "c2",
          author: "director",
          body: "Slow response landed.",
          createdAt: "2026-09-03T12:01:00Z",
        },
      ],
    });
    await runTimers(0);
    expect(screen.getByText("Slow response landed.")).toBeInTheDocument();

    await runTimers(POLL_INTERVAL_MS);
    expect(sessionCalls().length).toBe(beforeSlow + 2);
  });

  it(
    "refreshes the open session's task immediately when Stop cancels an in-flight turn or Start a fresh chat restarts the session",
    { timeout: 60000 },
    async () => {
      vi.useFakeTimers();
      window.history.pushState({}, "", "/session/director");
      setupApi();
      const rest = api.getMockImplementation();
      let activeTaskId = "session-1";
      let session1State = "running";
      api.mockImplementation((path, opts) => {
        if (
          path === "/api/console-sessions" &&
          (opts?.method || "GET") === "GET"
        ) {
          return Promise.resolve({
            sessions: [
              { id: "director", title: "Director", taskId: activeTaskId },
            ],
          });
        }
        if (
          path === "/api/console-sessions/director/cancel" &&
          opts?.method === "POST"
        ) {
          session1State = "awaiting_reply";
          return Promise.resolve({});
        }
        if (
          path === "/api/console-sessions/director/restart" &&
          opts?.method === "POST"
        ) {
          session1State = "closed";
          activeTaskId = "session-2";
          return Promise.resolve({
            sessions: [
              { id: "director", title: "Director", taskId: activeTaskId },
            ],
          });
        }
        if (
          path === "/api/tasks/session-1" &&
          (opts?.method || "GET") === "GET"
        ) {
          return Promise.resolve({
            id: "session-1",
            title: "Director",
            state: session1State,
            interactive: true,
            consoleSession: "director",
            description: "",
            capabilities: [],
            comments: [
              {
                id: "c1",
                author: "director",
                body: "Old conversation turn.",
                createdAt: "2026-09-03T12:00:00Z",
              },
            ],
          });
        }
        if (
          path === "/api/tasks/session-2" &&
          (opts?.method || "GET") === "GET"
        ) {
          return Promise.resolve({
            id: "session-2",
            title: "Director",
            state: "awaiting_reply",
            interactive: true,
            consoleSession: "director",
            description: "",
            capabilities: [],
            comments: [
              {
                id: "c2",
                author: "grain",
                authorKind: "automation",
                body: "Standing handover carried onto session-2.",
                createdAt: "2026-09-03T12:05:00Z",
              },
            ],
          });
        }
        return rest(path, opts);
      });

      render(<App />);
      await runTimers();
      expect(screen.getByText("Old conversation turn.")).toBeInTheDocument();
      fireEvent.click(screen.getByText("Stop"));
      await runTimers(0);

      expect(screen.queryByText("Stop")).toBeNull();
      expect(screen.getByText("waiting for your reply")).toBeInTheDocument();

      fireEvent.click(screen.getByText("Start a fresh chat"));
      fireEvent.click(screen.getByText("Start fresh with summary"));
      await runTimers(0);

      expect(
        screen.getByText("Standing handover carried onto session-2."),
      ).toBeInTheDocument();
      expect(screen.queryByText("Old conversation turn.")).toBeNull();
      expect(screen.getByText("Start a fresh chat")).toBeInTheDocument();
    },
  );

  it(
    "starts a standing session from its first message and refreshes the task list so its status shows on the sessions list",
    { timeout: 60000 },
    async () => {
      window.history.pushState({}, "", "/sessions");
      setupApi();
      const rest = api.getMockImplementation();
      let sessionsState = [];
      let sessionTasks = [];
      let createBody = null;
      api.mockImplementation((path, opts) => {
        const method = opts?.method || "GET";
        if (path === "/api/console-sessions" && method === "GET") {
          return Promise.resolve({ sessions: sessionsState });
        }
        if (path === "/api/console-sessions" && method === "POST") {
          createBody = JSON.parse(opts.body);
          // The daemon names the session from the message
          // (model.ConsoleSessionTitleFromMessage) and posts the message
          // into the task it files.
          const created = {
            id: "plan-the-release",
            title: "Plan the release",
            taskId: "session-99",
          };
          sessionsState = [created];
          sessionTasks = [
            {
              id: "session-99",
              title: "Plan the release",
              state: "awaiting_reply",
              interactive: true,
              consoleSession: "plan-the-release",
              description: "",
              capabilities: [],
              comments: [
                {
                  id: 1,
                  author: "alice",
                  authorKind: "human",
                  body: createBody.message,
                  createdAt: "2026-10-01T00:00:00Z",
                },
              ],
            },
          ];
          return Promise.resolve({
            sessions: sessionsState,
            created: "plan-the-release",
          });
        }
        if (path === "/api/tasks" && method === "GET") {
          return Promise.resolve([...sessionTasks]);
        }
        if (path === "/api/tasks/session-99" && method === "GET") {
          return Promise.resolve(sessionTasks[0]);
        }
        return rest(path, opts);
      });

      render(<App />);
      const [newBtn] = await screen.findAllByText("+ New session");
      fireEvent.click(newBtn);

      // No dialog: the chat itself opens, empty, at /session/new, with
      // a name field that may be left blank.
      expect(window.location.pathname).toBe("/session/new");
      expect(await screen.findByLabelText(/Session name/)).toHaveValue("");
      expect(await screen.findByText("New chat")).toBeInTheDocument();
      // Nothing was created by opening it.
      expect(createBody).toBeNull();

      const box = await screen.findByLabelText("Message");
      box.focus();
      await userEvent.paste("plan the release please");
      fireEvent.click(screen.getByText("Send"));

      await waitFor(() => expect(createBody).not.toBeNull());
      expect(createBody.message).toBe("plan the release please");
      expect(createBody.title).toBeUndefined();

      // The new session's own chat is open, under the name the daemon gave it.
      await screen.findByText("waiting for your reply");
      await waitFor(() =>
        expect(window.location.pathname).toBe("/session/plan-the-release"),
      );
      expect(
        await screen.findByText("plan the release please"),
      ).toBeInTheDocument();

      fireEvent.click(screen.getByText("Sessions"));
      const row = await screen.findByLabelText("Open session Plan the release");
      expect(row).toHaveAccessibleDescription("Asked");
    },
  );

  it(
    "creates a named session from the Session name field alone and pins it when asked",
    { timeout: 60000 },
    async () => {
      window.history.pushState({}, "", "/session/new");
      setupApi();
      const rest = api.getMockImplementation();
      let sessionsState = [];
      let createBody = null;
      api.mockImplementation((path, opts) => {
        const method = opts?.method || "GET";
        if (path === "/api/console-sessions" && method === "GET") {
          return Promise.resolve({ sessions: sessionsState });
        }
        if (path === "/api/console-sessions" && method === "POST") {
          createBody = JSON.parse(opts.body);
          sessionsState = [
            { id: "release-planning", title: "Release planning" },
          ];
          return Promise.resolve({
            sessions: sessionsState,
            created: "release-planning",
          });
        }
        return rest(path, opts);
      });

      render(<App />);
      const name = await screen.findByLabelText(/Session name/);
      fireEvent.change(name, { target: { value: "Release planning" } });
      fireEvent.click(screen.getByLabelText("Pin to sidebar"));
      fireEvent.click(screen.getByText("Create session"));

      await waitFor(() => expect(createBody).not.toBeNull());
      expect(createBody.title).toBe("Release planning");
      expect(createBody.message).toBeUndefined();
      await waitFor(() =>
        expect(window.location.pathname).toBe("/session/release-planning"),
      );
      expect(JSON.parse(localStorage.getItem("grain.pinned"))).toContainEqual({
        kind: "session",
        id: "release-planning",
      });
    },
  );

  it(
    "does not show the previous standing session's conversation while the newly opened session's task is still loading",
    { timeout: 60000 },
    async () => {
      localStorage.setItem(
        "grain.pinned",
        JSON.stringify([
          { kind: "session", id: "director" },
          { kind: "session", id: "reviewer" },
        ]),
      );
      window.history.pushState({}, "", "/session/director");
      setupApi();
      const rest = api.getMockImplementation();
      let resolveReviewerTask;
      const reviewerTaskPromise = new Promise((resolve) => {
        resolveReviewerTask = resolve;
      });
      api.mockImplementation((path, opts) => {
        const method = opts?.method || "GET";
        if (path === "/api/console-sessions" && method === "GET") {
          return Promise.resolve({
            sessions: [
              { id: "director", title: "Director", taskId: "session-1" },
              { id: "reviewer", title: "Reviewer", taskId: "session-2" },
            ],
          });
        }
        if (path === "/api/tasks/session-1" && method === "GET") {
          return Promise.resolve({
            id: "session-1",
            title: "Director",
            state: "awaiting_reply",
            interactive: true,
            consoleSession: "director",
            description: "Director initial prompt",
            capabilities: [],
            comments: [
              {
                id: 1,
                author: "grain",
                body: "Director secret message",
                createdAt: "2026-10-01T00:00:00Z",
              },
            ],
          });
        }
        if (path === "/api/tasks/session-2" && method === "GET") {
          return reviewerTaskPromise;
        }
        return rest(path, opts);
      });

      render(<App />);
      expect(
        await screen.findByText("Director secret message"),
      ).toBeInTheDocument();

      await userEvent.click(screen.getByRole("button", { name: /^Reviewer/ }));
      expect(
        screen.queryByText("Director secret message"),
      ).not.toBeInTheDocument();

      resolveReviewerTask({
        id: "session-2",
        title: "Reviewer",
        state: "awaiting_reply",
        interactive: true,
        consoleSession: "reviewer",
        description: "Reviewer initial prompt",
        capabilities: [],
        comments: [
          {
            id: 2,
            author: "grain",
            body: "Reviewer ready message",
            createdAt: "2026-10-01T00:01:00Z",
          },
        ],
      });
      expect(
        await screen.findByText("Reviewer ready message"),
      ).toBeInTheDocument();
    },
  );

  // grain has no users (docs/design/settings-panel.md §6): a first visit
  // is asked for no name, and the app is what it opens on.
  it("asks a first visit for no username", async () => {
    setupApi();
    render(<App />);

    expect(await screen.findByText("Fix bug")).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: "Welcome to Grain" }),
    ).toBeNull();
    expect(screen.queryByLabelText("Username")).toBeNull();
    expect(screen.queryByTestId("user-tile")).toBeNull();
    expect(localStorage.getItem("grain.username")).toBeNull();
  });

  // Nor is the actor the server reports shown in the rail: with one
  // credential and one owner a name on every screen says nothing. It is on
  // Settings → Access, for an IAP sign-in, alone.
  it("names nobody in the rail when the server reports an actor", async () => {
    setupApi();
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({
            ...config,
            authenticated: true,
            actor: "auth-user@example.com",
            actorKind: "human",
          })
        : realImpl(path, opts),
    );

    render(<App />);

    expect(await screen.findByText("Fix bug")).toBeInTheDocument();
    expect(screen.queryByText("auth-user@example.com")).toBeNull();
  });

  // A browser the server names nobody for (actor "") and refuses every
  // list is signed out, and is sent to the access-code sign-in rather
  // than shown an empty app or asked for a name
  // (docs/test-runs/2026-10-04-security-boundaries.md).
  it("sends a browser the server does not believe to the access code", async () => {
    localStorage.setItem("grain.username", "alice");
    setupApi();
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({
            ...config,
            authenticated: false,
            actor: "",
            actorKind: "",
            singleTenantRefusal: "this daemon keeps no accounts",
          })
        : realImpl(path, opts),
    );

    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Signed out" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Enter access code" }),
    ).toHaveAttribute("href", "/");
    expect(screen.getByTestId("single-tenant-refusal")).toHaveTextContent(
      "this daemon keeps no accounts",
    );
    expect(screen.queryByLabelText("Username")).toBeNull();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  // The 10-15 install walkthrough: on a fresh daemon every load the page
  // starts beside /api/config is refused with a 401, and one of them came
  // up as an error toast over the sign-in screen that already asks for
  // exactly that.
  it("raises no error toast for the 401s behind the signed-out screen", async () => {
    setupApi();
    api.mockImplementation((path) => {
      if (path === "/api/config") {
        return Promise.resolve({
          ...config,
          authenticated: false,
          actor: "",
          actorKind: "",
        });
      }
      const err = new Error("sign in to use this server's API");
      err.status = 401;
      err.explained = true;
      return Promise.reject(err);
    });

    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Signed out" }),
    ).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 300));
    expect(
      screen.queryByText("sign in to use this server's API"),
    ).not.toBeInTheDocument();
  });

  it("does not sign out a caller the server trusts without a session", async () => {
    setupApi();
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({
            ...config,
            authenticated: false,
            actor: "debian",
            actorKind: "human",
          })
        : realImpl(path, opts),
    );

    render(<App />);

    expect(await screen.findByText("Fix bug")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Signed out" })).toBeNull();
  });

  describe("automatic reload on backend version change", () => {
    it("reloads the page when polled config version commit changes", async () => {
      vi.useFakeTimers();
      setupApi();
      let currentCommit = "commit-aaa";
      const rest = api.getMockImplementation();
      api.mockImplementation((path, opts) => {
        if (path === "/api/config") {
          return Promise.resolve({
            ...config,
            version: { commit: currentCommit },
          });
        }
        return rest(path, opts);
      });

      const onReload = vi.fn();
      render(<App onReload={onReload} />);
      await runTimers();
      expect(screen.getByText("Fix bug")).toBeInTheDocument();
      expect(onReload).not.toHaveBeenCalled();

      // Poll tick with the same commit does not reload
      await runTimers(POLL_INTERVAL_MS + 100);
      expect(onReload).not.toHaveBeenCalled();

      // Backend upgrades and returns a new commit
      currentCommit = "commit-bbb";
      await runTimers(POLL_INTERVAL_MS + 100);

      expect(onReload).toHaveBeenCalledTimes(1);
    });

    it("does not reload when config has no version or commit", async () => {
      vi.useFakeTimers();
      setupApi();
      const onReload = vi.fn();
      render(<App onReload={onReload} />);
      await runTimers();
      expect(screen.getByText("Fix bug")).toBeInTheDocument();

      await runTimers(POLL_INTERVAL_MS + 100);
      expect(onReload).not.toHaveBeenCalled();
    });

    it("triggers window.location.reload by default when commit changes", async () => {
      vi.useFakeTimers();
      setupApi();
      let currentCommit = "v1.0.0";
      const rest = api.getMockImplementation();
      api.mockImplementation((path, opts) => {
        if (path === "/api/config") {
          return Promise.resolve({
            ...config,
            version: { commit: currentCommit },
          });
        }
        return rest(path, opts);
      });

      const reloadMock = vi.fn();
      const originalLocation = window.location;
      delete window.location;
      window.location = {
        ...originalLocation,
        reload: reloadMock,
        pathname: "/",
        search: "",
      };

      try {
        render(<App />);
        await runTimers();
        expect(screen.getByText("Fix bug")).toBeInTheDocument();
        expect(reloadMock).not.toHaveBeenCalled();

        currentCommit = "v2.0.0";
        await runTimers(POLL_INTERVAL_MS + 100);

        expect(reloadMock).toHaveBeenCalledTimes(1);
      } finally {
        window.location = originalLocation;
      }
    });
  });

  // The disconnected state itself (grain/task-26): what the shell shows
  // when the daemon stops answering at all, told apart from an ordinary
  // error by api.js marking exactly that case (isUnreachable).
  describe("when the daemon cannot be reached", () => {
    it("shows the disconnected banner instead of a quiet console warning, keeping the stale list on screen", async () => {
      vi.useFakeTimers();
      setupApi();
      render(<App />);
      await runTimers();
      expect(screen.getByText("Fix bug")).toBeInTheDocument();
      expect(
        screen.queryByText(/retrying automatically/i),
      ).not.toBeInTheDocument();

      const rest = api.getMockImplementation();
      api.mockImplementation((path, opts) => {
        if (path === "/api/tasks" && (opts?.method || "GET") === "GET") {
          return Promise.reject(new TypeError("Failed to fetch"));
        }
        return rest(path, opts);
      });

      await runTimers(POLL_INTERVAL_MS + 100);
      expect(
        screen.queryByText(/this deployment cannot be reached/i),
      ).not.toBeInTheDocument();

      await runTimers(POLL_INTERVAL_MS);

      expect(
        screen.getByText(/this deployment cannot be reached/i),
      ).toBeInTheDocument();
      expect(screen.getByText(/retrying automatically/i)).toBeInTheDocument();
      // The stale list is still exactly what it was -- nothing was
      // blanked out just because the next poll could not confirm it.
      expect(screen.getByText("Fix bug")).toBeInTheDocument();
    });

    it("recovers on its own once the daemon answers again, with no reload", async () => {
      vi.useFakeTimers();
      setupApi();
      render(<App />);
      await runTimers();
      expect(screen.getByText("Fix bug")).toBeInTheDocument();

      const rest = api.getMockImplementation();
      let down = true;
      api.mockImplementation((path, opts) => {
        if (
          down &&
          path === "/api/tasks" &&
          (opts?.method || "GET") === "GET"
        ) {
          return Promise.reject(new TypeError("Failed to fetch"));
        }
        return rest(path, opts);
      });

      await runTimers(POLL_INTERVAL_MS * 2 + 100);
      expect(
        screen.getByText(/this deployment cannot be reached/i),
      ).toBeInTheDocument();

      down = false;
      await runTimers(POLL_INTERVAL_MS * 2 + 100);

      expect(
        screen.queryByText(/this deployment cannot be reached/i),
      ).not.toBeInTheDocument();
      expect(screen.getByText("Fix bug")).toBeInTheDocument();
    });

    it("does not show the disconnected banner for an ordinary error the daemon itself answered with", async () => {
      vi.useFakeTimers();
      setupApi();
      render(<App />);
      await runTimers();
      expect(screen.getByText("Fix bug")).toBeInTheDocument();

      const rest = api.getMockImplementation();
      api.mockImplementation((path, opts) => {
        if (path === "/api/tasks" && (opts?.method || "GET") === "GET") {
          return Promise.reject(new Error("internal error"));
        }
        return rest(path, opts);
      });

      await runTimers(POLL_INTERVAL_MS + 100);

      expect(screen.queryByText(/cannot be reached/i)).not.toBeInTheDocument();
    });
  });
});
