// The fixtures and the API fake the App.*.test.jsx files share.
//
// These used to sit at the top of App.test.jsx, when that file held
// every test that renders the whole shell. It was split into one file
// per area (see App.test.jsx's own header for why), and this is what
// all of them need: the config a deployment reports, the two tasks the
// list starts with, the metrics report the Metrics pane reads, and the
// routing fake behind api.js.
//
// Not a *.test.jsx name on purpose -- vitest's include glob would pick
// it up as a test file of its own and fail it for containing no tests.
// The mock this leans on is each test file's own: `vi.mock("./api.js")`
// there replaces the module for that file's whole import graph, this
// module included, so setupApi below is programming the same mock the
// test can then assert against.
import { cleanup } from "@testing-library/react";
import { vi } from "vitest";
import api from "./api.js";

export const config = {
  defaultTarget: "acme/widgets",
  actor: "grain",
  capabilities: [],
  rebootEnabled: false,
};

export const initialTasks = [
  {
    id: "1",
    title: "Fix bug",
    state: "queued",
    repo: "acme/widgets",
    blocked: false,
    capabilities: [],
    reads: [],
  },
  {
    id: "2",
    title: "Add feature",
    state: "proposed",
    repo: "acme/other",
    blocked: false,
    capabilities: [],
    reads: [],
  },
];

// metricsReport is the shape GET /api/metrics returns (pkg/ui's own
// MetricsReport), trimmed to what the Metrics pane reads. Its oldest
// queued task is "Fix bug", the one task in
// initialTasks that is actually queued, so the link out of the backlog
// has somewhere real to land.
export const metricsReport = {
  since: "2026-08-27T00:00:00Z",
  until: "2026-09-03T00:00:00Z",
  windowSeconds: 604800,
  throughput: {
    tasksFiled: 2,
    tasksCompleted: 1,
    tasksClosed: 0,
    runsStarted: 2,
    runsFinished: 1,
    filedPerDay: 0.3,
    completedPerDay: 0.1,
    runsFinishedPerDay: 0.1,
    buckets: [],
  },
  latency: [
    {
      stage: "lead_time",
      label: "filed -> completed",
      description: "the whole of what whoever filed the task waited",
      n: 1,
      minSeconds: 60,
      p50Seconds: 60,
      p90Seconds: 60,
      p99Seconds: 60,
      maxSeconds: 60,
      meanSeconds: 60,
    },
  ],
  runs: {
    outcomes: { succeeded: 1 },
    attemptsPerCompletion: 1,
    meanConcurrent: 0.1,
    maxConcurrent: 3,
    utilization: 0.03,
    live: 0,
  },
  backlog: {
    byState: { queued: 1, proposed: 1 },
    queued: 1,
    oldestQueuedSeconds: 3600,
    oldestQueuedTaskId: "1",
  },
};

// setupApi wires a routing fake covering every endpoint App and the
// overlays it can open touch, backed by a mutable task list so actions
// that mutate (create, approve, ...) are reflected the next time the
// list is refetched -- the same way the real store behaves.
// fillIn is model.SubstituteFields, small enough to restate for the
// fake: a template's title with every `{{name}}` this instantiation has
// a value for replaced, and each field's declared default standing in
// for the ones it does not (model.Template.FieldValues). The real
// substitution is the server's alone -- nothing in the app does it --
// and this exists so a test can see the title a server would have
// handed back.
function fillIn(template, values) {
  let title = template.title || "";
  for (const field of template.fields || []) {
    const value =
      ((values || {})[field.name] || "").trim() || field.default || "";
    title = title.replaceAll(
      new RegExp(`\\{\\{\\s*${field.name}\\s*\\}\\}`, "g"),
      value,
    );
  }
  return title;
}

export function setupApi(
  tasks = initialTasks,
  schedules = [],
  templates = [],
  suites = [],
  boards = [],
  // The runs of those suites (grain/task-322), for a test that opens
  // one's own pane. Last, and defaulting to none, so every existing
  // call site is unchanged.
  suiteRuns = [],
  // grain's own CI runs (grain/task-354), for the rail's CI section.
  // Defaulting to none, so a deployment in these tests is one running
  // CI with nothing to show for it yet -- which is what every test that
  // does not care about CI wants the section to be.
  ciRuns = [],
) {
  let tasksState = [...tasks];
  let schedulesState = [...schedules];
  let templatesState = [...templates];
  const suitesState = [...suites];
  const suiteRunsState = [...suiteRuns];
  // The boards, kept in the order the real ListBoards returns them: by
  // folder, then by order key, then by id. A drag writes an order key
  // and expects to read the new order back, so a fake that handed them
  // back in whatever order they were patched in would let a broken move
  // pass (pkg/model/board_store.go).
  const sortBoards = (list) =>
    [...list].sort(
      (a, b) =>
        a.folder.localeCompare(b.folder) ||
        a.orderKey - b.orderKey ||
        a.id.localeCompare(b.id),
    );
  // A board's panels, as PutBoard and ListBoards between them leave
  // them: a panel whose order key is 0 is renumbered to its position on
  // the way in -- the store reads 0 as "this row was not numbered" --
  // and they come back in key order. Modelled here rather than stored as
  // they arrived, because a reorder writes the whole array and then
  // reads the order back (ui/src/panelOrder.js), so a fake that skipped
  // this would pass a move that wrote a key the store rewrites under it.
  const storePanels = (panels) =>
    panels
      .map((p, i) => ({ ...p, orderKey: p.orderKey === 0 ? i : p.orderKey }))
      .sort((a, b) => a.orderKey - b.orderKey);
  let boardsState = sortBoards(
    boards.map((b) =>
      Array.isArray(b.panels) ? { ...b, panels: storePanels(b.panels) } : b,
    ),
  );
  // rejectPatch is how a test says the server refuses one row's edit:
  // it answers with the Error api.js throws for a non-2xx (the server's
  // own "error" text), or null for the ids that go through. Set through
  // the handle returned below, since a partial failure is a property of
  // one test rather than of this fake.
  let rejectPatch = () => null;
  api.mockImplementation((path, opts) => {
    const method = opts?.method || "GET";
    if (path === "/api/config") return Promise.resolve(config);
    if (path === "/api/tasks" && method === "GET")
      return Promise.resolve(tasksState);
    if (path === "/api/tasks" && method === "POST") {
      const body = JSON.parse(opts.body);
      // A templateId, given, drives the content the same way
      // ui.Client.fromTemplate does server-side (grain/task-58): the
      // template fills every blank the request left, with its input
      // fields substituted from `fields`. The fake substitutes the same
      // `{{name}}` holes so a test filing from a template sees the title
      // a real server would hand back.
      const fromTemplate = body.templateId
        ? templatesState.find((t) => t.id === body.templateId)
        : null;
      const newTask = {
        id: "3",
        title: fromTemplate
          ? body.title || fillIn(fromTemplate, body.fields)
          : body.title,
        state: "proposed",
        repo: body.repo || "",
        creator: body.creator || "",
        blocked: false,
        capabilities: [],
        reads: [],
      };
      tasksState = [...tasksState, newTask];
      return Promise.resolve(newTask);
    }
    if (path === "/api/tasks/reorder" && method === "POST") {
      const { ids, afterId } = JSON.parse(opts.body);
      const rest = tasksState.filter((t) => !ids.includes(t.id));
      const moved = ids.map((id) => tasksState.find((t) => t.id === id));
      const idx = afterId ? rest.findIndex((t) => t.id === afterId) + 1 : 0;
      tasksState = [...rest.slice(0, idx), ...moved, ...rest.slice(idx)];
      return Promise.resolve(tasksState);
    }
    const detailMatch = path.match(/^\/api\/tasks\/(\w+)$/);
    // An edit, which the bulk edit form sends one of per selected task
    // (BulkEditOverlay.jsx). Applied to the list behind the fake rather
    // than answered and forgotten, so a test can ask afterwards which
    // rows actually moved -- which is the whole question when only some
    // of them did.
    if (detailMatch && method === "PATCH") {
      const id = detailMatch[1];
      const refusal = rejectPatch(id);
      if (refusal) return Promise.reject(refusal);
      const body = JSON.parse(opts.body);
      tasksState = tasksState.map((t) => (t.id === id ? { ...t, ...body } : t));
      return Promise.resolve(tasksState.find((t) => t.id === id));
    }
    if (detailMatch) {
      const t = tasksState.find((t) => t.id === detailMatch[1]);
      return Promise.resolve({
        description: "",
        comments: [],
        capabilities: [],
        ...t,
      });
    }
    if (/^\/api\/tasks\/\w+\/(approve|submit|retry|close|reopen)$/.test(path))
      return Promise.resolve({});
    if (path === "/api/secrets") return Promise.resolve({ enabled: false });
    if (path === "/api/settings") return Promise.resolve({ configured: false });
    // A repo's own page (RepoPage, grain/task-111) reads these on
    // landing.
    if (/^\/api\/repos\/[^/]+\/[^/]+\/branches$/.test(path) && method === "GET")
      return Promise.resolve([]);
    // And one pull request of that repo (PullRequestPage.jsx,
    // grain/task-375) reads this one: the change head brings to base,
    // taken from where the two forked rather than from base's own tip,
    // which is what keeps it readable once the branch has merged.
    if (/\/compare\?/.test(path) && method === "GET") {
      const asked = new URLSearchParams(path.split("?")[1]);
      return Promise.resolve({
        base: asked.get("base") || "main",
        head: asked.get("head"),
        mergeBase: "fork4200000000",
        merged: true,
        diff: [
          "diff --git a/a.txt b/a.txt",
          "index 1111111..2222222 100644",
          "--- a/a.txt",
          "+++ b/a.txt",
          "@@ -1,1 +1,1 @@",
          "-the old line",
          "+the changed line",
          "",
        ].join("\n"),
      });
    }
    if (
      /^\/api\/repos\/[^/]+\/[^/]+\/release-schedules$/.test(path) &&
      method === "GET"
    )
      return Promise.resolve([]);
    if (
      /^\/api\/repos\/[^/]+\/[^/]+\/capabilities$/.test(path) &&
      method === "GET"
    ) {
      return Promise.resolve({
        repo: "",
        defaultCapabilities: [],
        deploymentDefaultCapabilities: [],
        effectiveDefaultCapabilities: [],
      });
    }
    if (/^\/api\/repos\/[^/]+\/[^/]+\/releases$/.test(path))
      return Promise.resolve([]);
    if (/^\/api\/repos\/[^/]+\/[^/]+\/releases\/[^/]+\/candidates$/.test(path))
      return Promise.resolve([]);
    if (/^\/api\/repos\/[^/]+\/[^/]+\/qualification-plan$/.test(path)) {
      return Promise.resolve({
        configured: false,
        repo: "",
        requireApproval: false,
        autoPromote: false,
        items: [],
      });
    }
    if (
      /^\/api\/repos\/[^/]+\/[^/]+\/candidates\/[^/]+\/qualification$/.test(
        path,
      )
    )
      return Promise.resolve(null);
    if (path === "/api/schedules" && method === "GET")
      return Promise.resolve(schedulesState);
    if (path === "/api/schedules" && method === "POST") {
      const body = JSON.parse(opts.body);
      // A templateId, given, drives title/description/etc the same way
      // ui.Client.CreateSchedule does server-side (schedules.go's own
      // scheduleContentFromTemplate) -- the fake needs to reproduce that
      // so a test creating a template-backed schedule sees the same
      // content a real server would hand back. Repo and base are never
      // among them (a template carries no target of its own), so they
      // always come from body, template or not.
      const fromTemplate = body.templateId
        ? templatesState.find((t) => t.id === body.templateId)
        : null;
      const newSchedule = {
        id: "sched-2",
        enabled: true,
        nextRunAt: "2026-08-30T00:00:00Z",
        ...(fromTemplate && {
          title: fromTemplate.title,
          description: fromTemplate.description,
          autoMerge: fromTemplate.autoMerge,
          capabilities: fromTemplate.capabilities,
          templateId: fromTemplate.id,
          templateName: fromTemplate.name,
        }),
        ...body,
      };
      schedulesState = [...schedulesState, newSchedule];
      return Promise.resolve(newSchedule);
    }
    const scheduleMatch = path.match(/^\/api\/schedules\/([\w-]+)$/);
    if (scheduleMatch && method === "PATCH") {
      const body = JSON.parse(opts.body);
      schedulesState = schedulesState.map((s) =>
        s.id === scheduleMatch[1] ? { ...s, ...body } : s,
      );
      return Promise.resolve(
        schedulesState.find((s) => s.id === scheduleMatch[1]),
      );
    }
    if (scheduleMatch && method === "DELETE") {
      schedulesState = schedulesState.filter((s) => s.id !== scheduleMatch[1]);
      return Promise.resolve(null);
    }
    const templateCompositionMatch = path.match(
      /^\/api\/templates\/([\w-]+)\/composition(\?.*)?$/,
    );
    // The preview beside the form that fills a template in
    // (UseTemplateOverlay.jsx): one task, with the holes filled from the
    // `field.<name>` parameters, which is what the real endpoint
    // composes (ui.Client.TemplateComposition).
    if (templateCompositionMatch && method === "GET") {
      const tmpl = templatesState.find(
        (t) => t.id === templateCompositionMatch[1],
      );
      if (!tmpl) return Promise.reject(new Error("no such template"));
      const query = new URLSearchParams(templateCompositionMatch[2] || "");
      const values = {};
      for (const [key, value] of query.entries()) {
        if (key.startsWith("field."))
          values[key.slice("field.".length)] = value;
      }
      const title = fillIn(tmpl, values);
      return Promise.resolve({
        kind: "task",
        repo: query.get("repo") || "",
        base: query.get("base") || "",
        tasks: [
          {
            templateId: tmpl.id,
            templateName: tmpl.name,
            title,
            fields: [
              { name: "title", values: [title], source: "template" },
              {
                name: "autoMerge",
                values: [tmpl.autoMerge ? "on" : "off"],
                source: "template",
              },
            ],
          },
        ],
      });
    }
    if (path === "/api/templates" && method === "GET")
      return Promise.resolve(templatesState);
    if (path === "/api/templates" && method === "POST") {
      const body = JSON.parse(opts.body);
      const newTemplate = { id: "template-2", capabilities: [], ...body };
      templatesState = [...templatesState, newTemplate];
      return Promise.resolve(newTemplate);
    }
    const templateMatch = path.match(/^\/api\/templates\/([\w-]+)$/);
    if (templateMatch && method === "PATCH") {
      const body = JSON.parse(opts.body);
      templatesState = templatesState.map((t) =>
        t.id === templateMatch[1] ? { ...t, ...body } : t,
      );
      return Promise.resolve(
        templatesState.find((t) => t.id === templateMatch[1]),
      );
    }
    if (templateMatch && method === "DELETE") {
      templatesState = templatesState.filter((t) => t.id !== templateMatch[1]);
      return Promise.resolve(null);
    }
    // Suites (bwsalmon/agents#642). The suites are fetched on mount and
    // their runs on the way into the suites page, and
    // ui.Client.ListSuites/ListSuiteRuns both build their
    // result with make([]T, 0, ...), so a deployment with no suites gets
    // [] rather than null -- mirror that here. Falling through to this
    // fake's own null default instead would set App's suites state to
    // null, and Sidebar's `suites = []` parameter default only covers
    // undefined, so `suites.length` would take the whole app down.
    if (path === "/api/suites" && method === "GET")
      return Promise.resolve(suitesState);
    if (path === "/api/suite-runs" && method === "GET")
      return Promise.resolve(suiteRunsState);
    // Saved boards (grain/task-48), fetched on mount like the four
    // lists above. [] rather than this fake's own null default for the
    // same reason suites are.
    if (path === "/api/boards" && method === "GET")
      return Promise.resolve(boardsState);
    const boardMatch = path.match(/^\/api\/boards\/([\w-]+)$/);
    if (boardMatch && method === "PATCH") {
      const body = JSON.parse(opts.body);
      const patch = Array.isArray(body.panels)
        ? { ...body, panels: storePanels(body.panels) }
        : body;
      boardsState = sortBoards(
        boardsState.map((b) =>
          b.id === boardMatch[1] ? { ...b, ...patch } : b,
        ),
      );
      return Promise.resolve(boardsState.find((b) => b.id === boardMatch[1]));
    }
    // grain's own CI: the runs, one run entire, and the job cache line
    // above them -- read by the rail's CI section (CIPage.jsx) and by a
    // repo page's own runs list (RepoRuns.jsx). Enabled, because a
    // deployment answering `enabled: false` is one whose CI section says
    // only "not available" -- the uninteresting half of that pane.
    if (path.startsWith("/api/ci/runs")) {
      const id = path.match(/^\/api\/ci\/runs\/([\w-]+)$/);
      if (id) {
        const run = ciRuns.find((r) => r.id === id[1]);
        return run
          ? Promise.resolve(run)
          : Promise.reject(new Error(`no CI run "${id[1]}"`));
      }
      return Promise.resolve({ enabled: true, runs: ciRuns });
    }
    if (path === "/api/ci/cache")
      return Promise.resolve({ enabled: true, available: false });
    if (path === "/api/upgrade") return Promise.resolve({ enabled: false });
    if (path === "/api/logs") return Promise.resolve({ enabled: false });
    if (path === "/api/sandboxes") return Promise.resolve({ enabled: false });
    // The base branches nothing is judging (grain/task-470), polled by
    // the shell for the inbox and the rail's count. None, which is a
    // deployment whose gates are all working -- the ordinary day, and
    // what every test that is not about them wants. An explicit empty
    // list rather than this fake's own null default, for the reason the
    // suites are: App reads `.gates` off the answer.
    if (path === "/api/base-branch-gates")
      return Promise.resolve({ gates: [] });
    // A cache holding one thing, so the Caches page in the rail
    // (CachesPage.jsx) has a figure to draw rather than the "this
    // deployment runs none" state, which is the uninteresting half.
    if (path === "/api/package-cache")
      return Promise.resolve({
        enabled: true,
        cache: [{ kind: "apt", entries: 412, bytes: 3 * (1 << 30) }],
      });
    // The same for the Actions cache section on the Caches page:
    // a cache holding one repository's entries, so the interesting half
    // is what a navigation test lands on.
    if (path === "/api/actions-cache")
      return Promise.resolve({
        enabled: true,
        usedByJobs: true,
        entries: 12,
        bytes: 3 * (1 << 30),
        maxBytes: 20 * (1 << 30),
        repos: [{ repo: "acme/widgets", bytes: 3 * (1 << 30) }],
      });
    if (path.startsWith("/api/metrics")) return Promise.resolve(metricsReport);
    return Promise.resolve(null);
  });
  return {
    set rejectPatch(fn) {
      rejectPatch = fn;
    },
    get tasksState() {
      return tasksState;
    },
    get schedulesState() {
      return schedulesState;
    },
    get templatesState() {
      return templatesState;
    },
    get boardsState() {
      return boardsState;
    },
  };
}

// resetAppTestState is the afterEach every one of those files runs: the
// four pieces of state that outlive a test's own render, put back the
// way a fresh browser would have them.
//
// It unmounts the test's App first. This hook runs before setupTests.js's
// own cleanup (vitest runs afterEach hooks last-registered first), and an
// App still mounted while the fake is reset and the address put back
// writes the address straight back as its pending effects flush on the
// way out -- see setupTests.js. cleanup is idempotent, so the one there
// then finds nothing left to unmount.
export function resetAppTestState() {
  cleanup();
  api.mockReset();
  vi.useRealTimers();
  // A repo page's List/Board switch is stored per browser (board.js),
  // so one test's choice would otherwise be the next test's view.
  localStorage.clear();
  // The address bar outlives a test's own render -- and now carries a
  // list's narrowing (grain/task-317) -- so put it back to "/" rather
  // than let one test's URL seed the next test's App.
  window.history.replaceState(null, "", "/");
}

// A note on what a byRole query costs here, since the obvious fix is
// not the one that works. Scoping a rail click with
// within(screen.getByRole("complementary")) was tried and measured at
// about 3% of App.overlays.test.jsx, which is not worth giving up what
// an unscoped getByRole tells you (it throws when a second button of
// that name has appeared somewhere else on screen). The reason it buys
// so little: the expensive half of a byRole query is not deciding
// which candidates are visible, it is computing each candidate's
// accessible name -- dom-accessibility-api walks every candidate's
// subtree calling getComputedStyle as it goes -- and the candidates
// are the rail's own entries, so scoping to the rail keeps nearly all
// of them. What does bring the cost down is having fewer CSS rules in
// the document for jsdom to match, which is what splitting the file by
// area does (see App.test.jsx). Query by text where a test does not
// mean anything by the role, the way the board-drag tests in
// App.test.jsx already do.
