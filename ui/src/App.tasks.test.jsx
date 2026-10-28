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

// The backlog and the views over it: a task's own pane, filing one,
// the batch bar, the drag that reorders the list, and the inbox and the
// saved boards showing the same tasks a different way.
//
// Part of the App suite -- see App.test.jsx for why it is six files.

import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import App, { LOADING_PANE_DELAY_MS, POLL_INTERVAL_MS } from "./App.jsx";
import api from "./api.js";
import {
  config,
  initialTasks,
  resetAppTestState,
  setupApi,
} from "./appTestHarness.js";
import { runTimers } from "./testClock.js";

// A pass-through: only the default fetch wrapper is faked, so
// isUnreachable/unreachableReason -- which App.jsx's poll calls on
// whatever this mock rejects with -- stay the real, pure functions
// rather than becoming undefined (App.test.jsx's own mock, same reason).
vi.mock("./api.js", async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, default: vi.fn() };
});

// The columns and the chart are kinds a saved board can be, and since
// grain/task-162 that is the only way to reach either: the two fixed
// entries that used to sit on the rail above the boards are gone. So
// the tests that used to click "Board" or load /gantt open one of these
// instead. Neither carries columns or a query of its own, which is a
// board over every task drawn with whatever columns this browser is set
// to (boards.js) -- exactly what the fixed pair drew.
const kanbanBoard = {
  id: "board-1",
  name: "Everything",
  kind: "kanban",
  query: "",
  folder: "",
  orderKey: 1,
};

const ganttBoard = {
  id: "board-2",
  name: "When",
  kind: "gantt",
  query: "",
  folder: "",
  orderKey: 2,
};

describe("App", () => {
  afterEach(resetAppTestState);

  // A standing session's task is parked on its own conversation between
  // exchanges, so it reads "awaiting reply" for as long as nobody is
  // typing into it. It belongs to the Sessions pane, not to the task
  // list or the rail's per-state counts (grain/task-2318).
  it("keeps a standing session's task out of the list and the rail's counts", async () => {
    setupApi([
      ...initialTasks,
      {
        id: "9",
        title: "Director",
        state: "awaiting_reply",
        consoleSession: "director",
        interactive: true,
        team: "default",
        capabilities: [],
      },
    ]);
    render(<App />);
    await screen.findByText("Fix bug");
    expect(screen.queryByText("Director")).not.toBeInTheDocument();
    expect(screen.queryByText("Asked")).not.toBeInTheDocument();
  });

  it("redirects /tasks/{id} for a console session task to the session view", async () => {
    window.history.replaceState(null, "", "/tasks/9");
    setupApi([
      ...initialTasks,
      {
        id: "9",
        title: "Director",
        state: "awaiting_reply",
        consoleSession: "director",
        interactive: true,
        team: "default",
        capabilities: [],
      },
    ]);
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) => {
      if (path === "/api/console-sessions") {
        return Promise.resolve({
          sessions: [
            {
              id: "director",
              title: "Director",
              taskId: "9",
              capabilities: [],
            },
          ],
          capabilities: [],
          networkAccessOptions: ["open", "none"],
        });
      }
      return realImpl(path, opts);
    });
    render(<App />);
    await waitFor(() =>
      expect(window.location.pathname).toBe("/session/director"),
    );
    expect(
      await screen.findByRole("heading", { name: "Director" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: /9 Director/ }),
    ).not.toBeInTheDocument();
  });

  it("opens a task's detail overlay on click and closes it", async () => {
    setupApi();
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByText("Fix bug"));

    expect(
      await screen.findByRole("heading", { name: /Fix bug/ }),
    ).toHaveTextContent("1 Fix bug");

    // A task's pane leaves by the back button in its top-left corner,
    // the same as a repo's page does (grain/task-177).
    await user.click(screen.getByRole("button", { name: "← Back" }));

    expect(
      screen.queryByRole("heading", { name: /Fix bug/ }),
    ).not.toBeInTheDocument();
  });

  // A poll tick reads the open task's detail last, after the lists, so
  // a click onto another task can land while that read is still out.
  // Its answer belongs to the task that was open when it was asked for,
  // and writing it into the pane would show the first task under the
  // second one's selection -- until the second changed, since its own
  // conditional poll answers UNCHANGED for a body it never delivered.
  it("drops a polled detail that arrives after another task was opened", async () => {
    vi.useFakeTimers();
    setupApi();
    const realImpl = api.getMockImplementation();
    let release;
    api.mockImplementation((path, opts) => {
      if (path === "/api/tasks/1" && opts?.poll) {
        return new Promise((resolve) => {
          release = () => resolve(realImpl(path, { method: "GET" }));
        });
      }
      return realImpl(path, opts);
    });
    render(<App />);
    await runTimers();

    fireEvent.click(screen.getByText("Fix bug"));
    await runTimers();
    expect(
      screen.getByRole("heading", { name: /Fix bug/ }),
    ).toBeInTheDocument();

    // The tick that asks for task 1's detail, held open.
    await runTimers(POLL_INTERVAL_MS);
    expect(release).toBeTypeOf("function");

    fireEvent.click(screen.getByText("Add feature"));
    await runTimers();
    expect(
      screen.getByRole("heading", { name: /Add feature/ }),
    ).toBeInTheDocument();

    release();
    await runTimers();
    expect(
      screen.getByRole("heading", { name: /Add feature/ }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: /Fix bug/ }),
    ).not.toBeInTheDocument();
  });

  // Two opens in quick succession start two fetches, and the one the
  // user asked for last is the one the pane should land on, whichever
  // answers last.
  it("keeps the later open when an earlier task's detail arrives late", async () => {
    vi.useFakeTimers();
    setupApi();
    const realImpl = api.getMockImplementation();
    let release;
    api.mockImplementation((path, opts) => {
      if (path === "/api/tasks/1" && !opts?.poll) {
        return new Promise((resolve) => {
          release = () => resolve(realImpl(path, opts));
        });
      }
      return realImpl(path, opts);
    });
    render(<App />);
    await runTimers();

    fireEvent.click(screen.getByText("Fix bug"));
    await runTimers();
    expect(release).toBeTypeOf("function");

    fireEvent.click(screen.getByText("Add feature"));
    await runTimers();
    expect(
      screen.getByRole("heading", { name: /Add feature/ }),
    ).toBeInTheDocument();

    release();
    await runTimers();
    expect(
      screen.getByRole("heading", { name: /Add feature/ }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: /Fix bug/ }),
    ).not.toBeInTheDocument();
  });

  // grain/task-1756: on a slow connection nothing used to appear until
  // the detail arrived, so a click on a task looked like it had done
  // nothing. A slow detail opens the pane loading, and fills it in after.
  // Not at once, though (grain/task-2179): a loading pane put up for every
  // click and swapped for the task a moment later was the pane flickering.
  it("opens a loading pane while a slow task's detail is on its way", async () => {
    vi.useFakeTimers();
    setupApi();
    const realImpl = api.getMockImplementation();
    let release;
    api.mockImplementation((path, opts) => {
      if (path === "/api/tasks/1" && !opts?.poll) {
        return new Promise((resolve) => {
          release = () => resolve(realImpl(path, opts));
        });
      }
      return realImpl(path, opts);
    });
    render(<App />);
    await runTimers();

    fireEvent.click(screen.getByText("Fix bug"));
    await runTimers();
    expect(
      screen.queryByRole("dialog", { name: "Loading task 1" }),
    ).not.toBeInTheDocument();

    await runTimers(LOADING_PANE_DELAY_MS);
    expect(
      screen.getByRole("dialog", { name: "Loading task 1" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: /Fix bug/ }),
    ).not.toBeInTheDocument();

    release();
    await runTimers();
    expect(
      screen.getByRole("heading", { name: /Fix bug/ }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("dialog", { name: "Loading task 1" }),
    ).not.toBeInTheDocument();
  });

  // Opening another task from an open one keeps the open one on screen
  // while the next answers, rather than blanking the pane to a loading
  // mark for a moment and back (grain/task-2179).
  it("keeps the open task up while the next one's detail is on its way", async () => {
    vi.useFakeTimers();
    setupApi();
    const realImpl = api.getMockImplementation();
    let release;
    api.mockImplementation((path, opts) => {
      if (path === "/api/tasks/2" && !opts?.poll) {
        return new Promise((resolve) => {
          release = () => resolve(realImpl(path, opts));
        });
      }
      return realImpl(path, opts);
    });
    render(<App />);
    await runTimers();

    fireEvent.click(screen.getByText("Fix bug"));
    await runTimers();
    expect(
      screen.getByRole("heading", { name: /Fix bug/ }),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByText("Add feature"));
    await runTimers();
    expect(
      screen.getByRole("heading", { name: /Fix bug/ }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("dialog", { name: "Loading task 2" }),
    ).not.toBeInTheDocument();

    release();
    await runTimers();
    expect(
      screen.getByRole("heading", { name: /Add feature/ }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: /Fix bug/ }),
    ).not.toBeInTheDocument();
  });

  // A task that fails to load takes its loading pane away with it,
  // leaving the list and the error rather than a pane that never fills.
  it("closes the loading pane when a task fails to load", async () => {
    vi.useFakeTimers();
    setupApi();
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) => {
      if (path === "/api/tasks/1" && !opts?.poll) {
        return Promise.reject(new Error("task 1 is gone"));
      }
      return realImpl(path, opts);
    });
    render(<App />);
    await runTimers();

    fireEvent.click(screen.getByText("Fix bug"));
    await runTimers();
    expect(
      screen.queryByRole("dialog", { name: "Loading task 1" }),
    ).not.toBeInTheDocument();
    expect(screen.getByText(/task 1 is gone/)).toBeInTheDocument();
  });

  // Closing the pane abandons an open still in flight: its answer must
  // not bring back a pane the user just left.
  it("drops an open that resolves after the pane was closed", async () => {
    vi.useFakeTimers();
    setupApi();
    const realImpl = api.getMockImplementation();
    let release;
    api.mockImplementation((path, opts) => {
      if (path === "/api/tasks/1" && !opts?.poll) {
        return new Promise((resolve) => {
          release = () => resolve(realImpl(path, opts));
        });
      }
      return realImpl(path, opts);
    });
    render(<App />);
    await runTimers();

    fireEvent.click(screen.getByText("Add feature"));
    await runTimers();
    expect(
      screen.getByRole("heading", { name: /Add feature/ }),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByText("Fix bug"));
    await runTimers();
    expect(release).toBeTypeOf("function");

    fireEvent.click(screen.getByRole("button", { name: "← Back" }));
    await runTimers();
    expect(
      screen.queryByRole("heading", { name: /Add feature/ }),
    ).not.toBeInTheDocument();

    release();
    await runTimers();
    expect(
      screen.queryByRole("heading", { name: /Fix bug/ }),
    ).not.toBeInTheDocument();
  });

  // grain/task-1356: the task a URL names arrives a fetch later than the
  // page loads, and the URL effect used to write the page behind it over
  // the address bar in between -- replacing /tasks/1 with "/" and then
  // pushing /tasks/1 back, so Back after a reload skipped the page the
  // reader had come from.
  it("leaves history alone when a task URL is reloaded", async () => {
    setupApi();
    window.history.replaceState(null, "", "/repos");
    window.history.pushState(null, "", "/tasks/1");
    const length = window.history.length;
    const replace = vi.spyOn(window.history, "replaceState");
    const push = vi.spyOn(window.history, "pushState");

    render(<App />);
    await screen.findByRole("heading", { name: /Fix bug/ });

    await waitFor(() => expect(window.location.pathname).toBe("/tasks/1"));
    expect(window.history.length).toBe(length);
    expect(push).not.toHaveBeenCalled();
    expect(replace.mock.calls.map((c) => c[2])).not.toContain("/");
    push.mockRestore();
    replace.mockRestore();
  });

  // grain/task-3173: a task URL whose load fails with anything but a
  // 404 -- a 500 while the daemon restarts, the network gone -- used to
  // drop the reader on the list and write "/" over the address bar, so
  // reloading once grain was back no longer found the task. The pane
  // stays on the error instead, and Retry (or the poll) opens the task.
  it("keeps a task URL whose load fails with a server error", async () => {
    setupApi();
    const base = api.getMockImplementation();
    let failing = true;
    api.mockImplementation((p, opts) => {
      if (failing && p === "/api/tasks/1") {
        const err = new Error("500 Internal Server Error");
        err.status = 500;
        return Promise.reject(err);
      }
      return base(p, opts);
    });
    window.history.replaceState(null, "", "/tasks/1");
    const user = userEvent.setup();

    render(<App />);
    expect(
      await screen.findByRole("heading", {
        name: "Task 1 could not be loaded",
      }),
    ).toBeInTheDocument();
    expect(screen.getByText("500 Internal Server Error")).toBeInTheDocument();
    await screen.findByText("Fix bug");
    expect(window.location.pathname).toBe("/tasks/1");

    failing = false;
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await screen.findByRole("heading", { name: /Fix bug/ });
    expect(window.location.pathname).toBe("/tasks/1");
  });

  it("opens a task that failed to load once the poll reaches it", async () => {
    vi.useFakeTimers();
    vi.spyOn(console, "warn").mockImplementation(() => {});
    setupApi();
    const base = api.getMockImplementation();
    let failing = true;
    api.mockImplementation((p, opts) =>
      failing && p === "/api/tasks/1"
        ? Promise.reject(new TypeError("Failed to fetch"))
        : base(p, opts),
    );
    window.history.replaceState(null, "", "/tasks/1");

    render(<App />);
    await runTimers();
    expect(
      screen.getByRole("heading", { name: "Task 1 could not be loaded" }),
    ).toBeInTheDocument();
    expect(window.location.pathname).toBe("/tasks/1");

    failing = false;
    await runTimers(POLL_INTERVAL_MS * 3);
    expect(
      screen.getByRole("heading", { name: /Fix bug/ }),
    ).toBeInTheDocument();
    expect(window.location.pathname).toBe("/tasks/1");
    vi.useRealTimers();
  });

  it("falls back to the list for a task URL that answers 404", async () => {
    setupApi();
    const base = api.getMockImplementation();
    api.mockImplementation((p, opts) => {
      if (p === "/api/tasks/1") {
        const err = new Error("no such task");
        err.status = 404;
        return Promise.reject(err);
      }
      return base(p, opts);
    });
    window.history.replaceState(null, "", "/tasks/1");

    render(<App />);
    await screen.findByText("no such task");
    await waitFor(() => expect(window.location.pathname).toBe("/"));
    expect(
      screen.queryByRole("heading", { name: /could not be loaded/ }),
    ).toBeNull();
  });

  // ...and the same gap on Forward pushed a copy of the page behind the
  // task, which also threw away whatever was further forward.
  it("does not push history when Forward lands on a task", async () => {
    setupApi();
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByText("Fix bug"));
    await screen.findByRole("heading", { name: /Fix bug/ });
    await waitFor(() => expect(window.location.pathname).toBe("/tasks/1"));
    const length = window.history.length;

    window.history.back();
    await waitFor(() =>
      expect(
        screen.queryByRole("heading", { name: /Fix bug/ }),
      ).not.toBeInTheDocument(),
    );
    const push = vi.spyOn(window.history, "pushState");
    window.history.forward();
    await screen.findByRole("heading", { name: /Fix bug/ });

    await waitFor(() => expect(window.location.pathname).toBe("/tasks/1"));
    expect(push).not.toHaveBeenCalled();
    expect(window.history.length).toBe(length);
    push.mockRestore();
  });

  it("discards a field edit when a dependency opens another task", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    const tasks = [
      {
        ...initialTasks[0],
        customFields: { customer: "Northwind" },
        dependsOn: ["2"],
      },
      {
        ...initialTasks[1],
        customFields: { customer: "Contoso" },
      },
    ];
    setupApi(tasks);
    const inner = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({
            ...config,
            customFields: [{ name: "customer", label: "Customer" }],
          })
        : inner(path, opts),
    );
    const user = userEvent.setup();
    window.history.replaceState(null, "", "/tasks/1");

    render(<App />);
    await screen.findByRole("heading", { name: /Fix bug/ });
    await user.click(screen.getByRole("button", { name: "Edit fields" }));
    await user.clear(screen.getByLabelText("Customer"));
    await user.type(screen.getByLabelText("Customer"), "Draft for task one");

    // The dependency chip's label is the id and title in parts of their
    // own (detail/Dependencies.jsx), so it is matched whole.
    await user.click(
      screen.getByText(
        (_, el) =>
          el.classList.contains("MuiChip-label") &&
          el.textContent === "2 Add feature",
      ),
    );

    expect(confirm).toHaveBeenCalledWith(
      "You have unsaved changes here. Leave and discard them?",
    );
    await screen.findByRole("heading", { name: /Add feature/ });
    expect(
      screen.queryByRole("button", { name: "Save fields" }),
    ).not.toBeInTheDocument();
    expect(screen.getByText("Contoso")).toBeInTheDocument();
    expect(screen.queryByText("Draft for task one")).not.toBeInTheDocument();
    confirm.mockRestore();
  });

  // A declaration that changed under a stored value: "critical" was
  // dropped from the options after this task chose it. The pane sends
  // every declared field, so the select showing an empty box over it
  // meant Save restated a value nobody could see and the request was
  // refused -- and the task could not be edited at all until somebody
  // read the state repository. It is shown as what it is instead, and
  // the server grandfathers the restatement
  // (model.NormalizeCustomFieldValuesFrom).
  it("shows a retired enum option in the field editor and saves it back", async () => {
    const tasks = [
      { ...initialTasks[0], customFields: { priority: "critical" } },
      initialTasks[1],
    ];
    setupApi(tasks);
    const inner = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config"
        ? Promise.resolve({
            ...config,
            customFields: [
              {
                name: "priority",
                label: "Priority",
                type: "enum",
                options: ["low", "high"],
              },
            ],
          })
        : inner(path, opts),
    );
    const user = userEvent.setup();
    window.history.replaceState(null, "", "/tasks/1");

    render(<App />);
    await screen.findByRole("heading", { name: /Fix bug/ });
    await user.click(screen.getByRole("button", { name: "Edit fields" }));

    expect(screen.getByLabelText("Priority")).toHaveTextContent("critical");
    expect(screen.getByText(/no longer offers it/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Save fields" }));

    expect(api).toHaveBeenCalledWith("/api/tasks/1", {
      method: "PATCH",
      body: JSON.stringify({ customFields: { priority: "critical" } }),
    });
  });

  it("creates a new task and shows it in the refreshed list", async () => {
    setupApi();
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: "+ New task" }));
    await user.type(screen.getByLabelText(/Title/), "Ship it");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(await screen.findByText("Ship it")).toBeInTheDocument();
  });

  // A cold /new link is parsed before the config request that declares
  // its fields has landed. The form must wait for that first load before
  // seeding its state, or the link's value is initialized against no
  // declarations and stays blank after the field appears.
  it("prefills a custom field when /new is loaded before config arrives", async () => {
    setupApi();
    let resolveConfig;
    const configPromise = new Promise((resolve) => {
      resolveConfig = resolve;
    });
    const inner = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/config" ? configPromise : inner(path, opts),
    );
    window.history.replaceState(null, "", "/new?field.priority=high");

    render(<App />);

    expect(
      screen.queryByRole("heading", { name: "New task" }),
    ).not.toBeInTheDocument();

    resolveConfig({
      ...config,
      customFields: [
        {
          name: "priority",
          label: "Priority",
          type: "enum",
          options: ["low", "high"],
        },
      ],
    });

    expect(await screen.findByLabelText("Priority")).toHaveTextContent("high");
  });

  // grain/task-202: filing a task also writes which end of the backlog
  // new work joins, and the new-task form seeds its picker from that
  // (config.newestFirst). The config is otherwise fetched once, at
  // mount, so creating a task has to re-read it -- or the choice just
  // made would not show up until a reload.
  it("re-reads the config after a task is filed, so the backlog-end picker keeps the last choice", async () => {
    setupApi();
    const inner = api.getMockImplementation();
    let newestFirst = false;
    api.mockImplementation((path, opts) => {
      if (path === "/api/config")
        return Promise.resolve({ ...config, newestFirst });
      if (path === "/api/tasks" && opts?.method === "POST") {
        const body = JSON.parse(opts.body);
        if (typeof body.atFront === "boolean") newestFirst = body.atFront;
      }
      return inner(path, opts);
    });
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: "+ New task" }));
    await user.type(screen.getByLabelText(/Title/), "Ship it");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByLabelText("Add to backlog"));
    await user.click(screen.getByRole("option", { name: /^Front/ }));
    await user.click(screen.getByRole("button", { name: "Create task" }));
    await screen.findByText("Ship it");

    await user.click(screen.getByRole("button", { name: "+ New task" }));
    expect(screen.getByLabelText("Add to backlog")).toHaveTextContent(/^Front/);
  });

  // The banner takes itself down five seconds later (App.jsx's own
  // setTimeout), and that is the assertion -- so the clock is this
  // test's to run rather than to wait out. On the real one this was the
  // slowest test in the file at 5.1s, all of it spent asleep;
  // runTimers' fake clock gets to the same two assertions, in the same
  // order, in milliseconds. resetAppTestState puts the real timers back
  // afterwards.
  it("shows an error banner when the initial load fails, and clears it after 5s", async () => {
    vi.useFakeTimers();
    setupApi();
    api.mockRejectedValueOnce(new Error("config unavailable"));
    render(<App />);

    // No findBy here: under the fake clock waitFor's own polling timer
    // never fires either (see runTimers). Letting the rejected fetch
    // land is what this first, zero-length step is for.
    // The banner, by its role: the page under it says the same thing
    // for as long as there is still no config (ConfigUnavailable.jsx).
    await runTimers();
    expect(screen.getByRole("alert")).toHaveTextContent("config unavailable");

    // Still up a tenth of a second before its time, gone a tenth after:
    // free to check on a clock this test owns, and the difference
    // between "clears itself after 5s" and "clears itself".
    await runTimers(4900);
    expect(screen.getByRole("alert")).toHaveTextContent("config unavailable");

    await runTimers(200);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("selects a task, runs a batch approve, and clears the selection on success", async () => {
    setupApi();
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    const checkboxes = screen.getAllByRole("checkbox");
    await user.click(checkboxes[1]);

    expect(screen.getByText("1 selected")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Approve" }));

    expect(await screen.findByText("Fix bug")).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith("/api/tasks/1/approve", {
      method: "POST",
    });
    expect(screen.queryByText(/selected/)).not.toBeInTheDocument();
  });

  // grain/task-127: bulk edit is the only write in the list-working
  // surface that touches many rows at once, and there is no endpoint for
  // it -- it is one PATCH per selected task (BulkEditOverlay's apply,
  // through actBatch). So "some of them landed and one was refused" is
  // an ordinary outcome, and the failure worth pinning against is a form
  // that reports one outcome for the whole batch: an edit that half
  // happened, reported as done, over a list that has already moved.
  //
  // What it does instead, and what this asserts: the rows that could be
  // edited are, the refusal is reported with a count and the server's
  // own words, and the selection stands so the refused row can be seen
  // and retried rather than re-picked.
  it("reports a bulk edit that only half landed, and keeps the selection", async () => {
    const state = setupApi();
    const refusal =
      "acme/gadgets is this task's own repo, which it already writes to";
    state.rejectPatch = (id) => (id === "2" ? new Error(refusal) : null);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("checkbox", { name: "Select 1" }));
    await user.click(screen.getByRole("checkbox", { name: "Select 2" }));
    expect(screen.getByText("2 selected")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Bulk edit…" }));
    await user.click(
      await screen.findByRole("checkbox", { name: /^Base branch/ }),
    );
    await user.type(
      screen.getByRole("textbox", { name: "Base branch" }),
      "release/2",
    );
    await user.click(screen.getByRole("button", { name: "Apply to 2 tasks" }));

    // One PATCH per task, both fired: the refusal of the second does not
    // cancel the first, and the first landing does not excuse reporting
    // the second.
    const patch = {
      method: "PATCH",
      body: JSON.stringify({ base: "release/2" }),
    };
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/tasks/1", patch),
    );
    expect(api).toHaveBeenCalledWith("/api/tasks/2", patch);

    expect(
      await screen.findByText(`1 of 2 task(s) failed: ${refusal}`),
    ).toBeInTheDocument();

    // Half the edit happened, and the store behind the list says which
    // half -- the thing the reader would otherwise have to work out row
    // by row.
    expect(state.tasksState.find((t) => t.id === "1").base).toBe("release/2");
    expect(state.tasksState.find((t) => t.id === "2").base).toBeUndefined();

    // The selection stands, and the form is closed -- so Bulk edit is
    // one click away again, over the same rows, rather than a re-pick.
    expect(screen.getByText("2 selected")).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: "Bulk edit" }),
    ).not.toBeInTheDocument();
  });

  // The same edit with nothing refused, for the contrast the case above
  // is about: every row landed, so there is nothing to report and the
  // selection is done with.
  it("clears the selection when every row of a bulk edit lands", async () => {
    const state = setupApi();
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("checkbox", { name: "Select 1" }));
    await user.click(screen.getByRole("checkbox", { name: "Select 2" }));
    await user.click(screen.getByRole("button", { name: "Bulk edit…" }));
    await user.click(
      await screen.findByRole("checkbox", { name: /^Base branch/ }),
    );
    await user.type(
      screen.getByRole("textbox", { name: "Base branch" }),
      "release/2",
    );
    await user.click(screen.getByRole("button", { name: "Apply to 2 tasks" }));

    await waitFor(() =>
      expect(screen.queryByText(/selected/)).not.toBeInTheDocument(),
    );
    expect(state.tasksState.map((t) => t.base)).toEqual([
      "release/2",
      "release/2",
    ]);
    expect(screen.queryByText(/task\(s\) failed/)).not.toBeInTheDocument();
  });

  it("drags a task onto another and reorders the list via the API (bwsalmon/agents#476)", async () => {
    setupApi();
    render(<App />);
    await screen.findByText("Fix bug");

    const titles = () =>
      [...document.querySelectorAll(".task-title")].map((el) => el.textContent);
    expect(titles()).toEqual(["Fix bug", "Add feature"]);

    fireEvent.dragStart(screen.getByText("Add feature").closest("li"));
    fireEvent.dragOver(screen.getByText("Fix bug").closest("li"));
    fireEvent.drop(screen.getByText("Fix bug").closest("li"));

    expect(api).toHaveBeenCalledWith("/api/tasks/reorder", {
      method: "POST",
      body: JSON.stringify({ ids: ["2"], afterId: undefined, beforeId: "1" }),
    });
    await waitFor(() => expect(titles()).toEqual(["Add feature", "Fix bug"]));
  });

  it("shows the info icon beside Boards explaining how to create a board", async () => {
    setupApi(initialTasks);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Boards/ }));
    expect(
      await screen.findByText(
        /A board is a saved way of looking at this deployment/,
      ),
    ).toBeInTheDocument();
  });

  // grain/task-287: the board is the same tasks as the list, in columns
  // -- and since grain/task-162 it is reached the way every other saved
  // board is, by name from the rail's own Boards section rather than
  // from one fixed entry above it. The columns, the URL and the
  // selection the batch bar acts on are all unchanged.
  it("opens a Kanban board from the rail, laying the same tasks out in columns", async () => {
    setupApi(initialTasks, [], [], [], [kanbanBoard]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Boards/ }));
    await user.click(await screen.findByText("Everything"));

    expect(
      await screen.findByRole("heading", { name: "Board" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Fix bug").closest(".board-column"),
    ).toHaveTextContent("Waiting");
    expect(
      screen.getByText("Add feature").closest(".board-column"),
    ).toHaveTextContent("Proposed");
    await waitFor(() =>
      expect(window.location.pathname).toBe("/boards/board-1"),
    );
  });

  // grain/task-20: the inbox is the same tasks again, narrowed to what
  // is waiting on the reader and answered where they sit -- so an action
  // fired from a row must not open that task's pane over the list they
  // are working down.
  it("answers a proposal from the inbox without opening its pane", async () => {
    setupApi();
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    // The count of what is waiting rides in the entry's own label.
    await user.click(screen.getByRole("button", { name: "Inbox 1" }));

    expect(
      await screen.findByRole("heading", { name: "Inbox" }),
    ).toBeInTheDocument();
    await waitFor(() => expect(window.location.pathname).toBe("/inbox"));
    // The queued task is grain's business, not the reader's.
    expect(screen.queryByText("Fix bug")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Approve" }));

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/tasks/2/approve", {
        method: "POST",
      }),
    );
    expect(document.querySelector(".detail-header")).toBeNull();
  });

  // grain/task-470: the inbox holds one thing that is not a task, and
  // the shell is what fetches it -- the rail counts it from every view,
  it("does not count a base branch nothing is judging in the rail or inbox", async () => {
    setupApi();
    // Routed by path rather than queued with mockImplementationOnce: the
    // shell has several fetches in flight at mount, and a one-shot
    // answer lands on whichever of them asks first.
    const realImpl = api.getMockImplementation();
    api.mockImplementation((path, opts) =>
      path === "/api/base-branch-gates"
        ? Promise.resolve({
            gates: [
              {
                repo: "acme/widgets",
                branch: "main",
                judged: false,
                reason: "grain-ci-off",
                detail:
                  "Nothing is testing acme/widgets's base branch main, and" +
                  " the merge queue is merging onto it anyway.",
              },
            ],
          })
        : realImpl(path, opts),
    );
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    // Only the proposal waiting (Inbox 1), not including the unjudged branch.
    const inbox = await screen.findByRole("button", { name: "Inbox 1" });
    await user.click(inbox);

    expect(
      screen.queryByRole("heading", {
        name: "Look at a base branch nothing is judging",
      }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByText(/merging onto it anyway/),
    ).not.toBeInTheDocument();
  });

  it("opens the inbox on a fresh load of /inbox", async () => {
    window.history.replaceState(null, "", "/inbox");
    setupApi();
    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Inbox" }),
    ).toBeInTheDocument();
    // findBy: the heading draws before the list it heads has arrived.
    expect(await screen.findByText("Add feature")).toBeInTheDocument();
    await waitFor(() => expect(window.location.pathname).toBe("/inbox"));
  });

  it("opens a Kanban board on a fresh load of its own URL", async () => {
    window.history.replaceState(null, "", "/boards/board-1");
    setupApi(initialTasks, [], [], [], [kanbanBoard]);
    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Board" }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe("/boards/board-1"),
    );
  });

  // The chart is a board like the columns are (GanttChart.jsx,
  // grain/task-55): loadable cold, and still there after a reload.
  it("opens a timeline board on a fresh load of its own URL", async () => {
    window.history.replaceState(null, "", "/boards/board-2");
    setupApi(initialTasks, [], [], [], [ganttBoard]);
    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Timeline" }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe("/boards/board-2"),
    );
  });

  // The two paths those layouts used to live at are gone with the
  // entries that led to them (grain/task-162). A link somebody kept
  // opens the task list rather than an error or a blank pane.
  it("lands a kept /board or /gantt link on the task list", async () => {
    window.history.replaceState(null, "", "/gantt");
    setupApi();
    render(<App />);

    expect(await screen.findByText("Fix bug")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Timeline" })).toBeNull();
    await waitFor(() => expect(window.location.pathname).toBe("/"));
  });

  // A dashboard (grain/task-88) is a board like any other -- a row, an
  // entry in the rail, a link -- and what it renders is several views at
  // once, each narrowed to its own question (PanelBoard.jsx).
  it("opens a dashboard of panels on a fresh load of its own URL", async () => {
    window.history.replaceState(null, "", "/boards/board-1");
    setupApi(
      initialTasks,
      [],
      [],
      [],
      [
        {
          id: "board-1",
          name: "Operations",
          kind: "panels",
          query: "",
          folder: "",
          orderKey: 1,
          panels: [
            {
              kind: "list",
              title: "Widgets",
              query: "f=repo:any:acme/widgets",
            },
            { kind: "states", title: "Where everything is" },
          ],
        },
      ],
    );
    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Operations" }),
    ).toBeInTheDocument();
    // Each panel asks its own question of the same tasks: the list panel
    // is narrowed to one repo, the counts beside it are not.
    const panel = screen.getByText("Widgets").closest(".panel");
    expect(within(panel).getByText("Fix bug")).toBeInTheDocument();
    expect(within(panel).queryByText("Add feature")).toBeNull();
    expect(screen.getByText("Where everything is")).toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe("/boards/board-1"),
    );
  });

  // Clicking through from a panel is the ordinary view, asking exactly
  // what the panel was asking -- and it is a real URL, so it can be sent
  // as well as clicked.
  it("leaves a dashboard for the full view behind a panel", async () => {
    window.history.replaceState(null, "", "/boards/board-1");
    setupApi(
      initialTasks,
      [],
      [],
      [],
      [
        {
          id: "board-1",
          name: "Operations",
          kind: "panels",
          folder: "",
          orderKey: 1,
          panels: [
            {
              kind: "list",
              title: "Widgets",
              query: "f=repo:any:acme/widgets",
            },
          ],
        },
      ],
    );
    const user = userEvent.setup();
    render(<App />);
    await screen.findByRole("heading", { name: "Operations" });

    await user.click(screen.getByRole("link", { name: "Open" }));
    await waitFor(() =>
      expect(window.location.pathname + window.location.search).toBe(
        "/?f=repo:any:acme/widgets",
      ),
    );
    expect(screen.queryByRole("heading", { name: "Operations" })).toBeNull();
    expect(screen.getByText("Fix bug")).toBeInTheDocument();
    expect(screen.queryByText("Add feature")).toBeNull();
  });

  // grain/task-309, and the reason these two tests are written as a
  // comparison rather than as two lists of titles: a panel and the list
  // its "Open" link opens have to be about the same tasks. They were
  // not. A dashboard whose board carries no query used to open on
  // NO_NARROWING -- every task, the archive included -- while the list
  // it linked to applied the deployment's default, so on a deployment
  // with real history the panel counted 929 and the list showed 11, and
  // the eight rows under the count were eight tasks closed weeks ago.
  // On demo data, where one task in twelve is closed, none of it shows;
  // hence a closed task in the store here.
  //
  // Both halves of the answer are pinned, because they are one answer
  // (README, "What a link written before that change now means"): a
  // query that names nothing -- a board's or a link's -- is a view
  // nobody has narrowed, and the deployment's default answers for it;
  // a query that names something is the whole of what it asks.
  describe("a dashboard and the list its panels link to", () => {
    const longDone = {
      id: "3",
      title: "Long done",
      state: "closed",
      repo: "acme/widgets",
      blocked: false,
      capabilities: [],
      reads: [],
    };

    const dashboard = (panels) => [
      {
        id: "board-1",
        name: "Operations",
        kind: "panels",
        query: "",
        folder: "",
        orderKey: 1,
        panels,
      },
    ];

    // The titles on screen under `root`, in the order they are drawn --
    // a panel's rows and a list's rows are the same rows (TaskRow), so
    // the two are comparable by exactly this.
    const titlesIn = (root) =>
      [...root.querySelectorAll(".task-row .task-title")].map(
        (el) => el.textContent,
      );

    it("hides the same tasks, on a dashboard that asks nothing", async () => {
      window.history.replaceState(null, "", "/boards/board-1");
      setupApi(
        [...initialTasks, longDone],
        [],
        [],
        [],
        dashboard([{ kind: "list", title: "Everything" }]),
      );
      const user = userEvent.setup();
      render(<App />);
      await screen.findByRole("heading", { name: "Operations" });

      const panel = screen.getByText("Everything").closest(".panel");
      const onPanel = titlesIn(panel);
      expect(onPanel).toEqual(["Fix bug", "Add feature"]);
      // And the count on the header is the count of what the link
      // opens, which is the whole complaint: a header saying 929 over a
      // link to 11 is a number about nothing anybody can then read.
      expect(panel.querySelector(".count").textContent).toBe(
        String(onPanel.length),
      );

      await user.click(within(panel).getByRole("link", { name: "Open" }));

      await waitFor(() =>
        expect(window.location.pathname + window.location.search).toBe(
          "/?f=state:none:deferred|closed",
        ),
      );
      expect(titlesIn(document.body)).toEqual(onPanel);
    });

    it("shows the same tasks, for a panel that asks its own question", async () => {
      window.history.replaceState(null, "", "/boards/board-1");
      setupApi(
        [...initialTasks, longDone],
        [],
        [],
        [],
        dashboard([
          { kind: "list", title: "Widgets", query: "f=repo:any:acme/widgets" },
        ]),
      );
      const user = userEvent.setup();
      render(<App />);
      await screen.findByRole("heading", { name: "Operations" });

      // A query naming a repo and no state is a question about every
      // task in that repo, closed ones included -- said once, by the
      // panel and by the list it opens.
      const panel = screen.getByText("Widgets").closest(".panel");
      const onPanel = titlesIn(panel);
      expect(onPanel).toEqual(["Fix bug", "Long done"]);

      await user.click(within(panel).getByRole("link", { name: "Open" }));

      await waitFor(() =>
        expect(window.location.pathname + window.location.search).toBe(
          "/?f=repo:any:acme/widgets",
        ),
      );
      expect(titlesIn(document.body)).toEqual(onPanel);
    });
  });

  // Narrowing a dashboard on the spot (grain/task-111): the toolbar
  // over the panels narrows what a panel with no question of its own
  // inherits, and -- because it is the narrowing every task view shares
  // -- the address bar says so, so the narrowed dashboard is a link.
  it("narrows a dashboard from its toolbar, and puts it in the URL", async () => {
    window.history.replaceState(null, "", "/boards/board-1");
    setupApi(
      initialTasks,
      [],
      [],
      [],
      [
        {
          id: "board-1",
          name: "Operations",
          kind: "panels",
          folder: "",
          orderKey: 1,
          panels: [{ kind: "list", title: "Everything" }],
        },
      ],
    );
    const user = userEvent.setup();
    render(<App />);
    await screen.findByRole("heading", { name: "Operations" });
    const panel = screen.getByText("Everything").closest(".panel");
    expect(within(panel).getByText("Add feature")).toBeInTheDocument();

    await user.type(
      screen.getByPlaceholderText("Search this dashboard…"),
      "bug",
    );

    expect(within(panel).getByText("Fix bug")).toBeInTheDocument();
    expect(within(panel).queryByText("Add feature")).toBeNull();
    await waitFor(() =>
      expect(window.location.pathname + window.location.search).toBe(
        // The whole of what the dashboard is narrowed by, exactly as a
        // narrowed list's URL carries it: this board asks nothing of its
        // own, so it opened on the deployment's own condition
        // (grain/task-309) and the search is a narrowing of that.
        "/boards/board-1?q=bug&f=state:none:deferred|closed",
      ),
    );
  });

  // grain/task-368: a panel's place on its dashboard is the one edit to
  // a dashboard the UI writes itself, and the whole road is worth one
  // test -- the gesture, the PATCH it sends, and the order that comes
  // back afterwards. Driven through the handle's tap gesture rather than
  // a drag, for the same reason every other reorder test here is.
  it("moves a panel on a dashboard, and reads the new order back", async () => {
    window.history.replaceState(null, "", "/boards/board-1");
    const state = setupApi(
      initialTasks,
      [],
      [],
      [],
      [
        {
          id: "board-1",
          name: "Operations",
          kind: "panels",
          folder: "",
          orderKey: 1,
          // Unnumbered, which is what a dashboard created through
          // POST /api/boards actually looks like: the store numbers them
          // 0, 1, 2 on the way in.
          panels: [
            { kind: "list", title: "Live work", orderKey: 0 },
            { kind: "states", title: "Where everything is", orderKey: 1 },
            { kind: "gantt", title: "This week", orderKey: 2 },
          ],
        },
      ],
    );
    const user = userEvent.setup();
    render(<App />);
    await screen.findByRole("heading", { name: "Operations" });
    const headings = () =>
      [...document.querySelectorAll(".panel-title")].map(
        (el) => el.textContent,
      );
    expect(headings()).toEqual([
      "Live work",
      "Where everything is",
      "This week",
    ]);

    await user.click(
      screen.getByRole("button", { name: "Move panel 3, “This week”" }),
    );
    await user.click(
      screen.getByRole("button", {
        name: "Move here, above panel 1, “Live work”",
      }),
    );

    // One PATCH, carrying every panel of the board: a panel has no
    // write of its own and the array is the unit /api/boards takes
    // (panelOrder.js). Zero remains the first panel's real key while the
    // moved panel takes the open place below it.
    const patch = api.mock.calls.find(
      ([path, opts]) =>
        path === "/api/boards/board-1" && opts?.method === "PATCH",
    );
    expect(patch).toBeDefined();
    const sent = JSON.parse(patch[1].body);
    expect(sent.panels.map((p) => p.title)).toEqual([
      "This week",
      "Live work",
      "Where everything is",
    ]);
    expect(sent.panels.map((p) => p.orderKey)).toEqual([-(1 << 20), 0, 1]);

    // ...and the dashboard reads the order back rather than believing
    // its own drop.
    await waitFor(() =>
      expect(headings()).toEqual([
        "This week",
        "Live work",
        "Where everything is",
      ]),
    );
    expect(state.boardsState[0].panels.map((p) => p.title)).toEqual([
      "This week",
      "Live work",
      "Where everything is",
    ]);
  });

  it("selects a task from a board card and runs a batch action on it", async () => {
    window.history.replaceState(null, "", "/boards/board-1");
    setupApi(initialTasks, [], [], [], [kanbanBoard]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByRole("heading", { name: "Board" });

    await user.click(screen.getByRole("checkbox", { name: "Select 2" }));
    await user.click(screen.getByRole("button", { name: "Approve" }));

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/tasks/2/approve", {
        method: "POST",
      }),
    );
  });
});
