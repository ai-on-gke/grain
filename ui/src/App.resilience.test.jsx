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

// What the shell does when something it was handed is not what it
// expected (grain/task-1435): a page that throws while drawing is
// contained by its own boundary rather than taking the rail and every
// other pane with it, and a /api/config that answers with something
// other than grain says so instead of leaving the splash up forever.
//
// Part of the App suite -- see App.test.jsx for why it is several files.

import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import App, { CONFIG_POLL_TIMEOUT_MS, POLL_INTERVAL_MS } from "./App.jsx";
import api from "./api.js";
import { initialTasks, resetAppTestState, setupApi } from "./appTestHarness.js";
import { runTimers } from "./testClock.js";
import { formatTime } from "./time.js";

// A pass-through: only the default fetch wrapper is faked, so
// isUnreachable/unreachableReason -- which App.jsx's poll calls on
// whatever this mock rejects with -- stay the real, pure functions
// rather than becoming undefined (App.test.jsx's own mock, same reason).
vi.mock("./api.js", async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, default: vi.fn() };
});

// answering wraps setupApi's fake with one path answered differently.
function answering(path, respond) {
  const base = api.getMockImplementation();
  api.mockImplementation((p, opts) =>
    p === path ? respond(opts) : base(p, opts),
  );
}

// React's development build rethrows a caught render error through a
// window "error" event of its own, which jsdom prints in full; it is the
// throw under test, so it is kept out of the output here.
const quiet = (event) => event.preventDefault();

describe("App resilience", () => {
  beforeEach(() => {
    window.addEventListener("error", quiet);
    // React and the boundary both report a caught render throw on the
    // console, which is right in the app and noise in this output.
    vi.spyOn(console, "error").mockImplementation(() => {});
  });

  afterEach(() => {
    window.removeEventListener("error", quiet);
    vi.restoreAllMocks();
    vi.useRealTimers();
    resetAppTestState();
  });

  // grain/task-26: a poll that could not reach grain at all was a
  // console warning and nothing else, so a page that had stopped
  // updating looked exactly like one that had not. api.js's
  // isUnreachable (network failure, or a 502/503/504 from whatever
  // sits in front of grain) drives a standing DisconnectedBanner
  // instead, which stays up -- never one per tick -- until a tick gets
  // an answer again.
  it("says grain is out of reach, and clears when it answers", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "warn").mockImplementation(() => {});
    setupApi(initialTasks);
    const base = api.getMockImplementation();
    let offline = false;
    api.mockImplementation((p, opts) =>
      offline
        ? Promise.reject(new TypeError("Failed to fetch"))
        : base(p, opts),
    );
    render(<App />);
    await runTimers();
    expect(screen.getByText("Fix bug")).toBeInTheDocument();
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();

    offline = true;
    await runTimers(POLL_INTERVAL_MS);
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();
    await runTimers(POLL_INTERVAL_MS);
    expect(screen.getByText(/cannot be reached/i)).toBeInTheDocument();

    // Many more failed ticks, however the backoff spaces them out:
    // still the one banner, not one per tick.
    await runTimers(POLL_INTERVAL_MS * 5);
    expect(screen.getAllByText(/cannot be reached/i)).toHaveLength(1);
    // What was on screen stays there to read.
    expect(screen.getByText("Fix bug")).toBeInTheDocument();

    offline = false;
    // Generous rather than one more tick: the backoff above may have
    // grown past POLL_INTERVAL_MS by now, and recovery is only as fast
    // as the next tick it actually lets through.
    await runTimers(POLL_INTERVAL_MS * 10);
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();
  });

  // grain/task-3619: a daemon that accepts the connection and then never
  // answers (stopped, deadlocked, a proxy holding the socket) left the
  // poll waiting on its first unanswered request forever, so the banner
  // never came up. The poll's /api/config goes through the real api()
  // here, over a fetch that never answers, so its own timeout is what
  // ends the wait.
  it("says grain is out of reach when it stops answering, and clears when it answers", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const { default: realApi } = await vi.importActual("./api.js");
    setupApi(initialTasks);
    const base = api.getMockImplementation();
    let hung = false;
    const silent = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (_path, init) =>
          new Promise((_resolve, reject) => {
            silent.push({ init, at: Date.now() });
            init.signal?.addEventListener("abort", () =>
              reject(new DOMException("aborted", "AbortError")),
            );
          }),
      ),
    );
    api.mockImplementation((p, opts) =>
      hung && p === "/api/config" ? realApi(p, opts) : base(p, opts),
    );
    render(<App />);
    await runTimers();
    expect(screen.getByText("Fix bug")).toBeInTheDocument();

    hung = true;
    // The first unanswered tick, past its timeout: one slow answer is
    // not an outage.
    await runTimers(POLL_INTERVAL_MS + CONFIG_POLL_TIMEOUT_MS);
    expect(silent.length).toBeGreaterThan(0);
    expect(silent.every(({ init }) => init.signal)).toBe(true);
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();
    // The second is.
    await runTimers(POLL_INTERVAL_MS + CONFIG_POLL_TIMEOUT_MS);
    expect(
      screen.getByText(/accepted the connection but sent no answer/i),
    ).toBeInTheDocument();
    // "Since" is when the first unanswered request was asked, not when
    // the wait for it ran out.
    expect(screen.getByTestId("disconnected-banner")).toHaveTextContent(
      `since ${formatTime(new Date(silent[0].at))}`,
    );
    expect(screen.getByText("Fix bug")).toBeInTheDocument();

    hung = false;
    await runTimers(POLL_INTERVAL_MS * 12);
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();
  });

  // docs/test-runs/2026-11-09-shell-liveness.md, finding 1: only the
  // tick's config read had a timeout, so the tick's other reads stayed
  // open on a silent daemon, a new set every tick. Over HTTP/1.1 a
  // browser holds six connections to a host, and once those reads held
  // them all the config read queued behind them, so a proxy answering
  // new requests again never took the banner down. A failed tick now
  // gives up on its own reads, and the background polls' are bounded.
  it("gives up on the rest of a tick's reads when grain stops answering", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const { default: realApi } = await vi.importActual("./api.js");
    setupApi(initialTasks);
    const base = api.getMockImplementation();
    let hung = false;
    const silent = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (path, init) =>
          new Promise((_resolve, reject) => {
            silent.push({ path, init });
            init.signal?.addEventListener("abort", () =>
              reject(new DOMException("aborted", "AbortError")),
            );
          }),
      ),
    );
    const held = [
      "/api/tasks",
      "/api/config",
      "/api/ci/runs",
      "/api/console-sessions",
      "/api/persistent-sandboxes",
      "/api/base-branch-gates",
      "/api/notifications",
    ];
    // The poll's own list read, not closedTasks.js's one-off closed-task
    // query on the same path.
    const isHeld = (path, h) =>
      h === "/api/tasks" ? path === h : path.startsWith(h);
    api.mockImplementation((p, opts) =>
      hung && held.some((h) => isHeld(p, h)) ? realApi(p, opts) : base(p, opts),
    );
    render(<App />);
    await runTimers();
    expect(screen.getByText("Fix bug")).toBeInTheDocument();

    hung = true;
    // Long enough for the banner, the backoff, and the slower polls'
    // (staging every 15 s, notifications every 30 s, gates every 60 s)
    // own reads to have been asked more than once.
    await runTimers(90_000);
    expect(
      screen.getByText(/accepted the connection but sent no answer/i),
    ).toBeInTheDocument();
    const open = (prefix) =>
      silent.filter(
        ({ path, init }) => isHeld(path, prefix) && !init.signal?.aborted,
      );
    for (const prefix of held) {
      expect(
        silent.filter(({ path }) => isHeld(path, prefix)).length,
      ).toBeGreaterThan(0);
      // Every one asked was either given up on or is the one in flight.
      expect(open(prefix).length, prefix).toBeLessThanOrEqual(1);
    }
    // Given up on is not an error of its own: the tasks pane says what
    // the banner says, not "aborted".
    expect(screen.queryByText(/aborted/i)).toBeNull();

    hung = false;
    await runTimers(POLL_INTERVAL_MS * 12);
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();
  });

  // grain/task-2258: the backoff spaces out the main poll through an
  // outage, but it must not keep the banner up once grain answers
  // again. The restart watch keeps asking every POLL_INTERVAL_MS, and
  // its first answer resets the backoff and polls at once.
  it("clears the banner within a tick of grain answering after a long outage", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "warn").mockImplementation(() => {});
    setupApi(initialTasks);
    const base = api.getMockImplementation();
    let offline = false;
    api.mockImplementation((p, opts) =>
      offline
        ? Promise.reject(new TypeError("Failed to fetch"))
        : base(p, opts),
    );
    render(<App />);
    await runTimers();
    expect(screen.getByText("Fix bug")).toBeInTheDocument();

    // Long enough for the backoff to reach its cap of ten ticks.
    offline = true;
    await runTimers(POLL_INTERVAL_MS * 30);
    expect(screen.getByText(/cannot be reached/i)).toBeInTheDocument();

    offline = false;
    await runTimers(POLL_INTERVAL_MS + 500);
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();
    expect(api).toHaveBeenCalledWith("/api/restart-watch?quick=1");
  });

  it("ignores a single transient unreachable poll blip and only raises the banner on 2 consecutive failed ticks", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "warn").mockImplementation(() => {});
    setupApi(initialTasks);
    const base = api.getMockImplementation();
    let offline = false;
    api.mockImplementation((p, opts) =>
      offline
        ? Promise.reject(new TypeError("Failed to fetch"))
        : base(p, opts),
    );
    render(<App />);
    await runTimers();
    expect(screen.getByText("Fix bug")).toBeInTheDocument();

    // A single unreachable tick does not flash DisconnectedBanner.
    offline = true;
    await runTimers(POLL_INTERVAL_MS);
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();

    // A reachable tick right after resets the consecutive failure count,
    // so a later single blip still does not raise the banner.
    offline = false;
    await runTimers(POLL_INTERVAL_MS);
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();

    offline = true;
    await runTimers(POLL_INTERVAL_MS);
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();

    // A second consecutive unreachable tick puts DisconnectedBanner up.
    await runTimers(POLL_INTERVAL_MS);
    expect(screen.getByText(/cannot be reached/i)).toBeInTheDocument();

    // And a subsequent reachable tick clears it.
    offline = false;
    await runTimers(POLL_INTERVAL_MS * 2);
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();
  });

  // design/design/non-disruptive-upgrade.design.md, Stage 1: a daemon that said it was
  // restarting (GET /api/config's `restarting`) and then stopped
  // answering is an upgrade's restart, and the banner says so.
  it("says grain is restarting when the daemon said it was", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "warn").mockImplementation(() => {});
    setupApi(initialTasks);
    const base = api.getMockImplementation();
    let restarting = false;
    let offline = false;
    api.mockImplementation(async (p, opts) => {
      if (offline) throw new TypeError("Failed to fetch");
      const answer = await base(p, opts);
      return p === "/api/config" && restarting
        ? { ...answer, restarting: true }
        : answer;
    });
    render(<App />);
    await runTimers();

    restarting = true;
    await runTimers(POLL_INTERVAL_MS);
    expect(screen.queryByText(/grain is restarting/i)).toBeNull();

    offline = true;
    await runTimers(POLL_INTERVAL_MS);
    expect(screen.getByText(/grain is restarting/i)).toBeInTheDocument();
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();

    offline = false;
    restarting = false;
    await runTimers(POLL_INTERVAL_MS * 10);
    expect(screen.queryByText(/grain is restarting/i)).toBeNull();
  });

  // grain/task-3543: a SIGTERM'd daemon with little to drain is gone
  // before the next /api/config poll, which never says `restarting`.
  // The held GET /api/restart-watch is answered as the shutdown starts,
  // and that alone is enough for the banner to say "restarting".
  it("says grain is restarting when the held watch hears it go down", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "warn").mockImplementation(() => {});
    setupApi(initialTasks);
    const base = api.getMockImplementation();
    let offline = false;
    let announce = null;
    api.mockImplementation((p, opts) => {
      if (offline) return Promise.reject(new TypeError("Failed to fetch"));
      if (p === "/api/restart-watch") {
        return new Promise((resolve) => {
          announce = () => resolve({ restarting: true });
        });
      }
      return base(p, opts);
    });
    render(<App />);
    await runTimers();
    expect(announce).not.toBeNull();

    // The shutdown: the watch is answered and the API is gone at once,
    // with no /api/config poll in between.
    announce();
    offline = true;
    await runTimers(POLL_INTERVAL_MS);
    expect(screen.getByText(/grain is restarting/i)).toBeInTheDocument();
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();

    offline = false;
    await runTimers(POLL_INTERVAL_MS * 10);
    expect(screen.queryByText(/grain is restarting/i)).toBeNull();
  });

  it("keeps quiet about grain answering with an error of its own", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "warn").mockImplementation(() => {});
    setupApi(initialTasks);
    const base = api.getMockImplementation();
    let failing = false;
    api.mockImplementation((p, opts) => {
      if (failing && p === "/api/tasks") {
        const err = new Error("500 Internal Server Error");
        err.status = 500;
        return Promise.reject(err);
      }
      return base(p, opts);
    });
    render(<App />);
    await runTimers();
    failing = true;
    await runTimers(POLL_INTERVAL_MS * 4);
    expect(screen.queryByText(/cannot be reached/i)).toBeNull();
  });

  it("contains a page that throws while drawing, keeping the rail usable", async () => {
    setupApi(initialTasks);
    // Not an array: ProcessesList cannot draw this, and before a boundary
    // that blanked the whole app.
    answering("/api/schedules", () => Promise.resolve({ not: "an array" }));
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Processes/ }));

    expect(
      await screen.findByRole("heading", { name: "Something went wrong" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
    expect(
      console.error.mock.calls.some((args) =>
        String(args[0]).startsWith("grain: render failed in page"),
      ),
    ).toBe(true);

    // The rail is still there, and going somewhere else recovers.
    await user.click(screen.getByRole("button", { name: /^Tasks/ }));
    expect(await screen.findByText("Fix bug")).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: "Something went wrong" }),
    ).not.toBeInTheDocument();
  });

  it("explains a config answer that is not grain's instead of spinning", async () => {
    setupApi(initialTasks);
    answering("/api/config", () => {
      const err = new Error(
        "/api/config answered 200 with an HTML page rather than grain's JSON",
      );
      err.notGrain = true;
      return Promise.reject(err);
    });
    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "grain could not load" }),
    ).toBeInTheDocument();
    expect(screen.getByText(/something that isn't grain/)).toBeInTheDocument();
    expect(document.querySelector(".loading-screen")).toBeNull();
  });

  it("says tasks could not be loaded on dashboards and the rail when /api/tasks fails, then replaces the message when a retry succeeds", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "warn").mockImplementation(() => {});
    window.history.replaceState(null, "", "/boards/ops");
    setupApi(
      initialTasks,
      [],
      [],
      [],
      [
        {
          id: "ops",
          name: "Operations",
          kind: "panels",
          folder: "",
          orderKey: 1,
          panels: [
            { id: "p1", kind: "states", title: "States", orderKey: 1 },
            { id: "p2", kind: "list", title: "Queue", orderKey: 2 },
            { id: "p3", kind: "gantt", title: "Timeline", orderKey: 3 },
          ],
        },
      ],
    );
    const base = api.getMockImplementation();
    let failTasks = true;
    api.mockImplementation((p, opts) => {
      if (failTasks && p === "/api/tasks") {
        const err = new Error("synthetic tasks 500");
        err.status = 500;
        return Promise.reject(err);
      }
      return base(p, opts);
    });

    render(<App />);
    await runTimers();

    // Advance past the 5-second error banner timeout: the page and rail
    // must still explain that tasks failed to load rather than claiming 0.
    await runTimers(6000);
    expect(document.querySelector(".error-banner")).toBeNull();
    expect(screen.queryByText("Nothing here right now.")).toBeNull();
    expect(screen.queryByText("No tasks yet.")).toBeNull();
    expect(document.querySelectorAll(".panel-state")).toHaveLength(0);
    expect(
      screen.getAllByText(
        "The tasks could not be loaded (synthetic tasks 500).",
      ),
    ).toHaveLength(3);
    const railTasks = screen.getByRole("button", { name: /^Tasks/ });
    expect(railTasks).toHaveTextContent("could not be loaded");
    expect(railTasks).not.toHaveTextContent("0");

    // Once /api/tasks answers, Retry replaces the failure with the tasks.
    failTasks = false;
    fireEvent.click(screen.getAllByRole("button", { name: "Retry" })[0]);
    await runTimers();

    expect(screen.queryByText(/The tasks could not be loaded/)).toBeNull();
    expect(screen.getAllByText("Fix bug").length).toBeGreaterThan(0);
    expect(railTasks).not.toHaveTextContent("could not be loaded");
  });

  it("replaces the load failure with tasks on the next successful poll", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "warn").mockImplementation(() => {});
    setupApi(initialTasks);
    const base = api.getMockImplementation();
    let failTasks = true;
    api.mockImplementation((p, opts) => {
      if (failTasks && p === "/api/tasks") {
        const err = new Error("synthetic tasks 500");
        err.status = 500;
        return Promise.reject(err);
      }
      return base(p, opts);
    });

    render(<App />);
    await runTimers();
    expect(
      screen.getByText("The tasks could not be loaded (synthetic tasks 500)."),
    ).toBeInTheDocument();
    expect(screen.queryByText("No tasks in this state.")).toBeNull();

    failTasks = false;
    await runTimers(POLL_INTERVAL_MS * 4);
    expect(screen.queryByText(/The tasks could not be loaded/)).toBeNull();
    expect(screen.getByText("Fix bug")).toBeInTheDocument();
  });

  it("reports a failed /api/console-sessions load on /sessions instead of claiming no sessions exist, then recovers on Retry", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "warn").mockImplementation(() => {});
    window.history.replaceState(null, "", "/sessions");
    setupApi(initialTasks);
    const base = api.getMockImplementation();
    let failSessions = true;
    api.mockImplementation((p, opts) => {
      if (p === "/api/console-sessions") {
        if (failSessions) {
          const err = new Error("console session manager is still starting");
          err.status = 503;
          err.explained = true;
          return Promise.reject(err);
        }
        return Promise.resolve({
          sessions: [{ id: "director", title: "Director", taskId: "1" }],
          capabilities: [],
          networkAccessOptions: ["open", "none"],
        });
      }
      return base(p, opts);
    });

    render(<App />);
    await runTimers();

    // Advance past the 5-second transient error snackbar: /sessions must still
    // explain that sessions failed to load rather than claiming 0 sessions.
    await runTimers(6000);
    expect(document.querySelector(".error-banner")).toBeNull();
    expect(screen.queryByText("No interactive sessions yet.")).toBeNull();
    expect(
      screen.getByText(
        "The sessions could not be loaded (console session manager is still starting).",
      ),
    ).toBeInTheDocument();

    failSessions = false;
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await runTimers();

    expect(screen.queryByText(/The sessions could not be loaded/)).toBeNull();
    expect(
      screen.getByRole("button", { name: "Open session Director" }),
    ).toBeInTheDocument();
  });
});
