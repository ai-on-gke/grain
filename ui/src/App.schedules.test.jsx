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

// Schedules, templates and suites: the panes the rail opens for them,
// the URL each open item has of its own (grain/task-139), and creating
// a schedule from a template.
//
// Part of the App suite -- see App.test.jsx for why it is six files.

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { optionValues } from "./searchSelectTesting.js";
import { afterEach, describe, expect, it, vi } from "vitest";
import App from "./App.jsx";
import api from "./api.js";
import { DISCARD_PROMPT } from "./components/Overlay.jsx";
import { initialTasks, resetAppTestState, setupApi } from "./appTestHarness.js";

// A pass-through: only the default fetch wrapper is faked, so
// isUnreachable/unreachableReason -- which App.jsx's poll and showError
// call on whatever this mock rejects with -- stay the real, pure
// functions rather than becoming undefined (App.test.jsx's own mock,
// same reason).
vi.mock("./api.js", async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, default: vi.fn() };
});

describe("App", () => {
  afterEach(resetAppTestState);

  it("switches to Processes, listing the schedules", async () => {
    const schedule = {
      id: "sched-1",
      title: "Nightly dependency bump",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      recurrence: { kind: "everyNHours", everyNHours: 24 },
      enabled: true,
      nextRunAt: "2026-08-29T00:00:00Z",
    };
    setupApi(initialTasks, [schedule]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Processes/ }));

    expect(
      await screen.findByRole("heading", { name: "Processes" }),
    ).toBeInTheDocument();
    expect(
      await screen.findByText("Nightly dependency bump"),
    ).toBeInTheDocument();
    expect(screen.queryByText("Fix bug")).not.toBeInTheDocument();
  });

  it("edits a schedule from the schedules pane", async () => {
    const schedule = {
      id: "sched-1",
      title: "Nightly dependency bump",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      recurrence: { kind: "everyNHours", everyNHours: 24 },
      enabled: true,
      nextRunAt: "2026-08-29T00:00:00Z",
    };
    setupApi(initialTasks, [schedule]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Processes/ }));
    await user.click(await screen.findByText("Nightly dependency bump"));
    expect(
      await screen.findByRole("heading", { name: "Edit schedule" }),
    ).toBeInTheDocument();

    const titleField = screen.getByLabelText(/Title/);
    await user.clear(titleField);
    await user.type(titleField, "Weekly dependency bump");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(
      await screen.findByText("Weekly dependency bump"),
    ).toBeInTheDocument();
  });

  // A raw task schedule's Keep running is a box on its pane, read back
  // from what was saved and sent on Save (2026-11-03 walkthrough: the
  // API took keepRunning but the form had no way to say it).
  it("reads and saves a schedule's Keep running", async () => {
    const schedule = {
      id: "sched-1",
      title: "Nightly dependency bump",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      keepRunning: true,
      recurrence: { kind: "everyNHours", everyNHours: 24 },
      enabled: true,
      nextRunAt: "2026-08-29T00:00:00Z",
    };
    setupApi(initialTasks, [schedule]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Processes/ }));
    await user.click(await screen.findByText("Nightly dependency bump"));
    const box = await screen.findByRole("checkbox", { name: "Keep running" });
    expect(box).toBeChecked();
    await user.click(box);
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(
        api.mock.calls.find(
          ([path, opts]) =>
            path === "/api/schedules/sched-1" && opts?.method === "PATCH",
        ),
      ).toBeTruthy(),
    );
    const [, opts] = api.mock.calls.find(
      ([path, o]) => path === "/api/schedules/sched-1" && o?.method === "PATCH",
    );
    expect(JSON.parse(opts.body)).toMatchObject({ keepRunning: false });
  });

  it("previews a saved schedule with the model it asks for", async () => {
    // The draft endpoint has no saved row to fall back on, so the model
    // the save leaves out when unchanged has to be in the preview's body
    // (docs/test-runs/2026-11-04-recurring-composed-ui.md, bug 1).
    const schedule = {
      id: "sched-1",
      title: "Nightly dependency bump",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      model: "gemini-2.5-flash",
      recurrence: { kind: "everyNHours", everyNHours: 24 },
      enabled: true,
      nextRunAt: "2026-08-29T00:00:00Z",
    };
    setupApi(initialTasks, [schedule]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Processes/ }));
    await user.click(await screen.findByText("Nightly dependency bump"));
    await screen.findByRole("heading", { name: "Edit schedule" });

    const previews = () =>
      api.mock.calls
        .filter(
          ([path, o]) =>
            path === "/api/schedules/composition" && o?.method === "POST",
        )
        .map(([, o]) => JSON.parse(o.body));
    await waitFor(() => expect(previews().length).toBeGreaterThan(0));
    expect(previews().at(-1)).toMatchObject({ model: "gemini-2.5-flash" });

    // The save itself still leaves an unchanged model out.
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(
        api.mock.calls.find(
          ([path, o]) =>
            path === "/api/schedules/sched-1" && o?.method === "PATCH",
        ),
      ).toBeTruthy(),
    );
    const [, opts] = api.mock.calls.find(
      ([path, o]) => path === "/api/schedules/sched-1" && o?.method === "PATCH",
    );
    expect(JSON.parse(opts.body)).not.toHaveProperty("model");
  });

  // Admission (model.Schedule.Admission) is a column on schedules as on
  // tasks; a raw schedule's pane had no way to say it (2026-11-07
  // walkthrough).
  it("reads and saves a raw schedule's admission", async () => {
    const schedule = {
      id: "sched-1",
      title: "Nightly dependency bump",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      admission: "bypass",
      recurrence: { kind: "everyNHours", everyNHours: 24 },
      enabled: true,
      nextRunAt: "2026-08-29T00:00:00Z",
    };
    setupApi(initialTasks, [schedule]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Processes/ }));
    await user.click(await screen.findByText("Nightly dependency bump"));
    const select = await screen.findByRole("combobox", { name: "Admission" });
    expect(select).toHaveTextContent("Bypass queue");
    await user.click(select);
    await user.click(await screen.findByRole("option", { name: "Durable" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(
        api.mock.calls.find(
          ([path, opts]) =>
            path === "/api/schedules/sched-1" && opts?.method === "PATCH",
        ),
      ).toBeTruthy(),
    );
    const [, opts] = api.mock.calls.find(
      ([path, o]) => path === "/api/schedules/sched-1" && o?.method === "PATCH",
    );
    expect(JSON.parse(opts.body)).toMatchObject({ admission: "durable" });
  });

  // A declared schedule may raise its template's admission (template
  // bypass, schedule durable): the preview has to be composed with the
  // schedule's own, which the draft endpoint cannot read off a saved
  // row (2026-11-07 walkthrough).
  it("previews a template-backed schedule with its own admission", async () => {
    const schedule = {
      id: "sched-1",
      title: "Bump dependencies",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      templateId: "template-1",
      templateName: "Nightly dependency bump",
      admission: "durable",
      recurrence: { kind: "everyNHours", everyNHours: 24 },
      enabled: true,
      nextRunAt: "2026-08-29T00:00:00Z",
    };
    setupApi(initialTasks, [schedule]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Processes/ }));
    await user.click(await screen.findByText("Bump dependencies"));
    await screen.findByRole("heading", { name: "Edit schedule" });
    const previews = () =>
      api.mock.calls
        .filter(
          ([path, o]) =>
            path === "/api/schedules/composition" && o?.method === "POST",
        )
        .map(([, o]) => JSON.parse(o.body));
    await waitFor(() => expect(previews().length).toBeGreaterThan(0));
    expect(previews().at(-1)).toMatchObject({
      templateId: "template-1",
      admission: "durable",
    });
    // Its admission is the template's to edit, not this pane's.
    expect(screen.queryByRole("combobox", { name: "Admission" })).toBeNull();
  });

  // Hosting (model.Schedule.Hosting) sits beside admission, the setting
  // that replaced the console capability.
  it("reads and saves a raw schedule's hosting", async () => {
    const schedule = {
      id: "sched-1",
      title: "Nightly dependency bump",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      recurrence: { kind: "everyNHours", everyNHours: 24 },
      enabled: true,
      nextRunAt: "2026-08-29T00:00:00Z",
    };
    setupApi(initialTasks, [schedule]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Processes/ }));
    await user.click(await screen.findByText("Nightly dependency bump"));
    const select = await screen.findByRole("combobox", { name: "Hosting" });
    expect(select).toHaveTextContent("Unattended");
    await user.click(select);
    await user.click(
      await screen.findByRole("option", { name: "Interactive console" }),
    );
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(
        api.mock.calls.find(
          ([path, opts]) =>
            path === "/api/schedules/sched-1" && opts?.method === "PATCH",
        ),
      ).toBeTruthy(),
    );
    const [, opts] = api.mock.calls.find(
      ([path, o]) => path === "/api/schedules/sched-1" && o?.method === "PATCH",
    );
    expect(JSON.parse(opts.body)).toMatchObject({ hosting: "console" });
  });

  it("switches a schedule to every N minutes", async () => {
    const schedule = {
      id: "sched-1",
      title: "Release manager",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      recurrence: { kind: "everyNHours", everyNHours: 1 },
      enabled: true,
      nextRunAt: "2026-08-29T00:00:00Z",
    };
    const api = setupApi(initialTasks, [schedule]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Processes/ }));
    await user.click(await screen.findByText("Release manager"));
    await screen.findByRole("heading", { name: "Edit schedule" });

    await user.click(screen.getByRole("combobox", { name: "Repeat" }));
    await user.click(
      await screen.findByRole("option", { name: "Every N minutes" }),
    );
    const minutes = screen.getByRole("spinbutton", { name: /Every N minutes/ });
    // The floor is the browser's to enforce too, not only the API's.
    expect(minutes).toHaveAttribute("min", "10");
    expect(screen.queryByLabelText(/^Time \(/)).not.toBeInTheDocument();
    await user.clear(minutes);
    await user.type(minutes, "30");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(api.schedulesState[0].recurrence).toEqual({
        kind: "everyNMinutes",
        everyNMinutes: 30,
      }),
    );
  });

  // grain/task-139: a schedule, a template and a suite are addressable
  // in their own right, the way a task already was -- which one is open
  // lives here in App rather than inside the list component, so
  // paths.js can name it. The URL going stale (an item deleted, or a
  // link to one that never existed) is App's to correct, since App is
  // what holds the list the id has to be in.
  it("gives an open schedule its own URL, and clears it again on close", async () => {
    const schedule = {
      id: "sched-1",
      title: "Nightly dependency bump",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      recurrence: { kind: "everyNHours", everyNHours: 24 },
      enabled: true,
      nextRunAt: "2026-08-29T00:00:00Z",
    };
    setupApi(initialTasks, [schedule]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Processes/ }));
    await user.click(await screen.findByText("Nightly dependency bump"));

    expect(
      await screen.findByRole("heading", { name: "Edit schedule" }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe("/processes/schedules/sched-1"),
    );

    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(
      screen.queryByRole("heading", { name: "Edit schedule" }),
    ).not.toBeInTheDocument();
    await waitFor(() => expect(window.location.pathname).toBe("/processes"));
  });

  // grain/task-1630: Escape and "← Back" asked before dropping an edited
  // schedule, but the browser's own Back -- a phone's back gesture --
  // closed it without a word. It moves the address bar first and tells
  // App through popstate, which is simulated here the same way.
  it("asks before browser Back drops an edited schedule, and stays if told to", async () => {
    const schedule = {
      id: "sched-1",
      title: "Nightly dependency bump",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      recurrence: { kind: "everyNHours", everyNHours: 24 },
      enabled: true,
      nextRunAt: "2026-08-29T00:00:00Z",
    };
    setupApi(initialTasks, [schedule]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");
    await user.click(screen.getByRole("button", { name: /^Processes/ }));
    await user.click(await screen.findByText("Nightly dependency bump"));
    await screen.findByRole("heading", { name: "Edit schedule" });
    await user.type(screen.getByLabelText(/Title/), " (edited)");

    const back = () => {
      window.history.pushState(null, "", "/processes");
      window.dispatchEvent(new PopStateEvent("popstate"));
    };
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    back();
    expect(confirm).toHaveBeenCalledWith(
      "You have unsaved changes here. Leave and discard them?",
    );
    await waitFor(() =>
      expect(window.location.pathname).toBe("/processes/schedules/sched-1"),
    );
    expect(screen.getByLabelText(/Title/)).toHaveValue(
      "Nightly dependency bump (edited)",
    );
    // Back blurred the field to commit it; staying hands focus back, so
    // an Escape pressed next reaches the pane instead of <body>.
    expect(screen.getByLabelText(/Title/)).toHaveFocus();

    confirm.mockReturnValue(true);
    back();
    await waitFor(() =>
      expect(
        screen.queryByRole("heading", { name: "Edit schedule" }),
      ).not.toBeInTheDocument(),
    );
    await waitFor(() => expect(window.location.pathname).toBe("/processes"));
    confirm.mockRestore();
  });

  it("lets browser Back leave an unedited schedule without asking", async () => {
    const schedule = {
      id: "sched-1",
      title: "Nightly dependency bump",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      recurrence: { kind: "everyNHours", everyNHours: 24 },
      enabled: true,
      nextRunAt: "2026-08-29T00:00:00Z",
    };
    setupApi(initialTasks, [schedule]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");
    await user.click(screen.getByRole("button", { name: /^Processes/ }));
    await user.click(await screen.findByText("Nightly dependency bump"));
    await screen.findByRole("heading", { name: "Edit schedule" });

    const confirm = vi.spyOn(window, "confirm");
    window.history.pushState(null, "", "/processes");
    window.dispatchEvent(new PopStateEvent("popstate"));
    await waitFor(() =>
      expect(
        screen.queryByRole("heading", { name: "Edit schedule" }),
      ).not.toBeInTheDocument(),
    );
    expect(confirm).not.toHaveBeenCalled();
    confirm.mockRestore();
  });

  it("opens the schedule an old /schedules/:id link names, at its Processes path", async () => {
    const schedule = {
      id: "sched-1",
      title: "Nightly dependency bump",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      recurrence: { kind: "everyNHours", everyNHours: 24 },
      enabled: true,
      nextRunAt: "2026-08-29T00:00:00Z",
    };
    window.history.replaceState(null, "", "/schedules/sched-1");
    setupApi(initialTasks, [schedule]);
    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Edit schedule" }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(/Title/)).toHaveValue(
      "Nightly dependency bump",
    );
    await waitFor(() =>
      expect(window.location.pathname).toBe("/processes/schedules/sched-1"),
    );
  });

  it("opens the template an old /templates/:id link names, at its Processes path", async () => {
    const template = {
      id: "template-1",
      name: "Dependency bump",
      title: "Bump dependencies",
      description: "",
      autoMerge: false,
      capabilities: [],
    };
    window.history.replaceState(null, "", "/templates/template-1");
    setupApi(initialTasks, [], [template]);
    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Edit template" }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe("/processes/templates/template-1"),
    );
  });

  it("opens the suite an old /suites/:id link names, at its Processes path", async () => {
    const suite = {
      id: "suite-1",
      name: "Nightly sweep",
      items: [],
      mode: "until_clean",
      maxPasses: 5,
      requireApproval: false,
      autoMerge: true,
    };
    window.history.replaceState(null, "", "/suites/suite-1");
    setupApi(initialTasks, [], [], [suite]);
    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Edit suite" }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe("/processes/suites/suite-1"),
    );
  });

  // grain/task-322: a run of a suite is addressable too, two segments
  // under the suites list -- the pane that says what that run has
  // actually filed. Cold-loadable like the three above it, which is why
  // the runs are fetched on the way into the suites view rather than
  // only on the poll tick after it.
  it("opens the suite run an old /suites/runs/:id link names, at its Processes path", async () => {
    const suite = {
      id: "suite-1",
      name: "Nightly sweep",
      items: [],
      mode: "until_clean",
      maxPasses: 5,
      requireApproval: false,
      autoMerge: true,
    };
    const run = {
      id: 7,
      suiteId: "suite-1",
      suiteName: "Nightly sweep",
      repo: "acme/widgets",
      base: "main",
      mode: "until_clean",
      maxPasses: 5,
      status: "active",
      pass: 1,
      tasks: [
        {
          taskId: "1",
          templateId: "template-1",
          templateName: "Smoke",
          pass: 1,
          state: "queued",
        },
      ],
    };
    window.history.replaceState(null, "", "/suites/runs/7");
    setupApi(initialTasks, [], [], [suite], [], [run]);
    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Nightly sweep" }),
    ).toBeInTheDocument();
    // The run's own task, and not the rest of the backlog.
    expect(await screen.findByText("Fix bug")).toBeInTheDocument();
    expect(screen.queryByText("Add feature")).not.toBeInTheDocument();
    await waitFor(() =>
      expect(window.location.pathname).toBe("/processes/suites/runs/7"),
    );
  });

  it("drops a suite run id the deployment does not have", async () => {
    window.history.replaceState(null, "", "/suites/runs/404");
    setupApi(initialTasks, [], [], []);
    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Suite runs" }),
    ).toBeTruthy();
    await waitFor(() => expect(window.location.pathname).toBe("/processes"));
  });

  it("falls back to the plain list when the URL names a schedule that isn't there", async () => {
    window.history.replaceState(null, "", "/schedules/gone");
    setupApi(initialTasks, []);
    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Processes" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: "Edit schedule" }),
    ).not.toBeInTheDocument();
    // The stale id is dropped once the list it named has landed, so the
    // address bar stops pointing at a pane that isn't open.
    await waitFor(() => expect(window.location.pathname).toBe("/processes"));
  });

  // grain/task-1432: that correction used to land after the URL effect's
  // first run, and so was pushed as a step of its own -- Back from the
  // list went to the stale link, which corrected itself all over again.
  // It replaces now, and says what was not there, the way /tasks/:id
  // does for a task.
  it.each([
    ["/schedules/nope", "/processes", 'no schedule "nope"'],
    ["/processes/templates/nope", "/processes", 'no template "nope"'],
    ["/suites/nope", "/processes", 'no suite "nope"'],
    ["/processes/suites/runs/404", "/processes", 'no suite run "404"'],
    ["/boards/nope", "/boards", 'no board "nope"'],
  ])(
    "replaces a link to %s that isn't there, and says so",
    async (stale, list, message) => {
      window.history.replaceState(null, "", stale);
      setupApi(initialTasks, [], [], []);
      const pushes = vi.spyOn(window.history, "pushState");
      render(<App />);

      await waitFor(() => expect(window.location.pathname).toBe(list));
      expect(await screen.findByText(message)).toBeInTheDocument();
      expect(pushes).not.toHaveBeenCalled();
      pushes.mockRestore();
    },
  );

  // ...and only for a link: an item deleted from its own pane is closed
  // by the same fetch, and nobody needs telling it is gone.
  it("says nothing when a schedule opened from its URL is deleted", async () => {
    const schedule = {
      id: "sched-1",
      title: "Nightly dependency bump",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      recurrence: { kind: "everyNHours", everyNHours: 24 },
      enabled: true,
      nextRunAt: "2026-08-29T00:00:00Z",
    };
    window.history.replaceState(null, "", "/schedules/sched-1");
    setupApi(initialTasks, [schedule]);
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    const user = userEvent.setup();
    render(<App />);

    await screen.findByRole("heading", { name: "Edit schedule" });
    await user.click(screen.getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(window.location.pathname).toBe("/processes"));
    expect(screen.queryByText(/no schedule/)).not.toBeInTheDocument();
    confirm.mockRestore();
  });

  it("switches to Processes, listing the templates", async () => {
    const template = {
      id: "template-1",
      name: "Dependency bump",
      title: "Bump dependencies",
      description: "",
      autoMerge: false,
      capabilities: [],
    };
    setupApi(initialTasks, [], [template]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Processes/ }));

    expect(
      await screen.findByRole("heading", { name: "Processes" }),
    ).toBeInTheDocument();
    expect(await screen.findByText("Dependency bump")).toBeInTheDocument();
    expect(screen.queryByText("Fix bug")).not.toBeInTheDocument();
  });

  it("creates a schedule from a template, hiding the content fields", async () => {
    const template = {
      id: "template-1",
      name: "Dependency bump",
      title: "Bump dependencies",
      description: "",
      autoMerge: false,
      capabilities: [],
    };
    setupApi(initialTasks, [], [template]);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    await user.click(screen.getByRole("button", { name: /^Processes/ }));
    // New asks for the kind first (design/design/settings-panel.design.md §5.3).
    await user.click(await screen.findByRole("button", { name: "+ New" }));
    await user.click(
      within(await screen.findByTestId("new-process-kind")).getByRole(
        "menuitem",
        { name: /Schedule/ },
      ),
    );
    await screen.findByRole("heading", { name: "New schedule" });

    // A SearchSelect (grain/task-2910): a word from the template's name
    // finds it.
    const picker = screen.getByRole("combobox", { name: "Template" });
    await user.click(picker);
    await user.clear(picker);
    await user.type(picker, "bump");
    expect(optionValues(picker)).toEqual(["template-1"]);
    await user.keyboard("{Enter}");

    expect(screen.queryByLabelText(/^Title/)).not.toBeInTheDocument();
    expect(
      screen.getByText(/come from the selected template/),
    ).toBeInTheDocument();
    await user.type(screen.getByLabelText(/^Repos/), "acme/widgets");

    await user.click(screen.getByRole("button", { name: "Add schedule" }));

    // The new schedule's row, beside the template's own on the same page.
    await waitFor(() =>
      expect(
        document.querySelector('.process-row[data-kind="schedule"]'),
      ).toHaveTextContent("Bump dependencies"),
    );
  });

  it("replaces history when redirecting a /processes/triggers path to the repo's Advanced tab", async () => {
    window.history.replaceState(
      null,
      "",
      "/processes/triggers/acme/widgets/.grain/triggers/docs.yml",
    );
    setupApi(initialTasks, []);
    const pushes = vi.spyOn(window.history, "pushState");
    render(<App />);

    await waitFor(() =>
      expect(window.location.pathname).toBe(
        "/repos/acme/widgets/settings/advanced",
      ),
    );
    expect(pushes).not.toHaveBeenCalled();
    pushes.mockRestore();
  });

  it("closes an edited New schedule dialog on browser Back when discard is confirmed", async () => {
    const schedule = {
      id: "sched-1",
      title: "Nightly dependency bump",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      recurrence: { kind: "everyNHours", everyNHours: 24 },
      enabled: true,
      nextRunAt: "2026-08-29T00:00:00Z",
    };
    window.history.replaceState(null, "", "/processes?kind=schedule");
    setupApi(initialTasks, [schedule]);
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    const user = userEvent.setup();
    render(<App />);

    await screen.findByRole("heading", { name: "Processes" });
    await user.click(screen.getByRole("button", { name: /^All / }));
    await waitFor(() => expect(window.location.search).toBe(""));

    await user.click(screen.getByRole("button", { name: "+ New" }));
    await user.click(
      within(await screen.findByTestId("new-process-kind")).getByRole(
        "menuitem",
        { name: /Schedule/ },
      ),
    );
    await screen.findByRole("heading", { name: "New schedule" });
    await user.type(screen.getByLabelText("Title"), "Half-written");

    window.history.replaceState(null, "", "/processes?kind=schedule");
    window.dispatchEvent(new PopStateEvent("popstate"));

    try {
      expect(confirm).toHaveBeenCalledWith(DISCARD_PROMPT);
      // The dialog leaves through MUI's exit transition, which on a
      // loaded CI runner outlasts waitFor's one-second default.
      await waitFor(
        () =>
          expect(
            screen.queryByRole("heading", { name: "New schedule" }),
          ).not.toBeInTheDocument(),
        { timeout: 5000 },
      );
    } finally {
      // Restored even when the wait fails, so the next test's own spy
      // does not wrap this one and count its calls as well.
      confirm.mockRestore();
    }
  });

  it("guards unsaved schedule edits before following the suppressed task link", async () => {
    const schedule = {
      id: "sched-1",
      title: "Nightly dependency bump",
      description: "",
      repo: "acme/widgets",
      base: "",
      autoMerge: false,
      recurrence: { kind: "everyNHours", everyNHours: 24 },
      enabled: true,
      nextRunAt: "2026-08-29T00:00:00Z",
      suppressed: { reason: "task", taskId: "1" },
    };
    window.history.replaceState(null, "", "/processes/schedules/sched-1");
    setupApi(initialTasks, [schedule]);
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const user = userEvent.setup();
    render(<App />);

    await screen.findByRole("heading", { name: "Edit schedule" });
    const title = screen.getByLabelText("Title");
    await user.type(title, " edited");

    try {
      await user.click(screen.getByRole("link", { name: "task 1" }));
      expect(confirm).toHaveBeenCalledWith(DISCARD_PROMPT);
      expect(
        screen.getByRole("heading", { name: "Edit schedule" }),
      ).toBeInTheDocument();

      confirm.mockReturnValue(true);
      await user.click(screen.getByRole("link", { name: "task 1" }));
      expect(confirm).toHaveBeenCalledTimes(2);
      await waitFor(
        () =>
          expect(
            screen.queryByRole("heading", { name: "Edit schedule" }),
          ).not.toBeInTheDocument(),
        { timeout: 5000 },
      );
    } finally {
      confirm.mockRestore();
    }
  });

  it("refreshes schedules, templates and suites immediately when navigating into Processes", async () => {
    setupApi(initialTasks, [], [], []);
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText("Fix bug");

    // Simulate declarations synced on another view before entering Processes
    // (before the 3-second background poll fires).
    setupApi(
      initialTasks,
      [
        {
          id: "repo:schedule:acme:widgets:nightly",
          title: "Declared nightly audit",
          repo: "acme/widgets",
          recurrence: { kind: "daily", timeOfDay: "02:00" },
          enabled: true,
          nextRunAt: "2026-08-29T02:00:00Z",
          declaredIn: {
            repo: "acme/widgets",
            path: ".grain/schedules/nightly.yml",
          },
        },
      ],
      [
        {
          id: "repo:template:acme:widgets:audit",
          name: "Declared audit template",
          title: "Audit repo",
          repo: "acme/widgets",
          declaredIn: {
            repo: "acme/widgets",
            path: ".grain/templates/audit.yml",
          },
        },
      ],
      [
        {
          id: "repo:suite:acme:widgets:smoke",
          name: "Declared smoke suite",
          repo: "acme/widgets",
          mode: "until_clean",
          maxPasses: 2,
          items: [{ templateId: "repo:template:acme:widgets:audit" }],
          declaredIn: { repo: "acme/widgets", path: ".grain/suites/smoke.yml" },
        },
      ],
    );

    await user.click(screen.getByRole("button", { name: /^Processes/ }));

    expect(
      await screen.findByText("Declared nightly audit"),
    ).toBeInTheDocument();
    expect(
      await screen.findByText("Declared audit template"),
    ).toBeInTheDocument();
    expect(await screen.findByText("Declared smoke suite")).toBeInTheDocument();
  });
});
