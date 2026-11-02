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

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import App, { POLL_INTERVAL_MS } from "./App.jsx";
import api from "./api.js";
import { initialTasks, resetAppTestState, setupApi } from "./appTestHarness.js";
import { runTimers } from "./testClock.js";

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
});
