import { describe, expect, it } from "vitest";

import {
  addPanelTask,
  boardColumns,
  boardFolders,
  boardKindLabel,
  boardNarrowing,
  editBoardTask,
  keepPanelQueryTask,
  keptQuery,
  kindForView,
  newBoardTask,
  normalizeBoards,
} from "./boards.js";
import { NO_NARROWING, defaultNarrowing } from "./taskFilters.js";
import { narrowingFromQuery } from "./paths.js";
import { REPORT_SECTIONS } from "./report.js";
import { RANGES } from "./gantt.js";

const board = (over = {}) => ({
  id: "board-1",
  name: "Waiting on me",
  folder: "",
  kind: "list",
  description: "",
  query: "",
  team: "default",
  columns: [],
  panels: [],
  sections: [],
  orderKey: 0,
  ...over,
});

describe("normalizeBoards", () => {
  // A board can be written by an agent editing the state repository's
  // dump, so this build can be handed anything at all. None of it is
  // worth a blank screen.
  it("keeps what it can read and drops what is not a board", () => {
    expect(
      normalizeBoards([
        { id: "board-1", name: "Mine", kind: "kanban" },
        { name: "no id" },
        null,
        "nonsense",
      ]),
    ).toEqual([
      {
        id: "board-1",
        name: "Mine",
        folder: "",
        kind: "kanban",
        description: "",
        query: "",
        columns: [],
        panels: [],
        sections: [],
        orderKey: 0,
      },
    ]);
  });

  // Boards are the deployment's (single-tenant reset, stage 4d): an old
  // server's team is not carried.
  it("carries no team on a board", () => {
    const [board] = normalizeBoards([{ id: "board-1", team: "infra" }]);
    expect(board).not.toHaveProperty("team");
  });

  it("falls back to a list for a kind this build does not have", () => {
    expect(normalizeBoards([{ id: "board-9", kind: "burndown" }])[0].kind).toBe(
      "list",
    );
  });

  it("keeps a timeline board, which is a kind this build does have", () => {
    expect(normalizeBoards([{ id: "board-9", kind: "gantt" }])[0].kind).toBe(
      "gantt",
    );
  });

  it("shows a nameless board under its own id rather than blank", () => {
    expect(normalizeBoards([{ id: "board-9" }])[0].name).toBe("board-9");
  });

  it("reads anything that is not a list of boards as no boards", () => {
    expect(normalizeBoards(undefined)).toEqual([]);
    expect(normalizeBoards({})).toEqual([]);
  });
});

describe("boardFolders", () => {
  it("puts the unfiled boards first and groups the rest by folder", () => {
    const { loose, folders } = boardFolders([
      board({ id: "board-1", name: "Loose" }),
      board({ id: "board-2", name: "A", folder: "Reports" }),
      board({ id: "board-3", name: "B", folder: "Reports" }),
      board({ id: "board-4", name: "C", folder: "Mine" }),
    ]);
    expect(loose.map((b) => b.name)).toEqual(["Loose"]);
    expect(folders.map((f) => f.folder)).toEqual(["Reports", "Mine"]);
    expect(folders[0].boards.map((b) => b.name)).toEqual(["A", "B"]);
  });

  it("has nothing to group when there are no boards", () => {
    expect(boardFolders([])).toEqual({ loose: [], folders: [] });
  });
});

describe("boardNarrowing", () => {
  // A board's question is stored in the encoding the address bar
  // carries, and read by the parser that opens a link somebody pasted --
  // one parser, so the two cannot disagree.
  it("reads a stored query the way a link is read", () => {
    expect(
      boardNarrowing(
        board({ query: "q=flake&sort=created:desc&f=state:any:running" }),
      ),
    ).toEqual({
      search: "flake",
      sort: [{ attr: "created", dir: "desc" }],
      filters: [{ attr: "state", op: "any", values: ["running"] }],
    });
  });

  // A board whose query names nothing has asked nothing, which is what
  // null means everywhere else a query is read (paths.js, App's own
  // askedNarrowing) -- so the deployment's default answers for it, the
  // same way it answers for "/" (grain/task-309). It is emphatically not
  // "every task": a dashboard that meant that opened over the archive
  // while every list it linked to hid it.
  it("has asked nothing for a list or a dashboard that narrows nothing", () => {
    expect(boardNarrowing(board())).toBeNull();
    expect(boardNarrowing(board({ query: "" }))).toBeNull();
    expect(boardNarrowing(board({ kind: "panels" }))).toBeNull();
    expect(boardNarrowing(null)).toBeNull();
  });

  // A query this build can make nothing of is a query that named
  // nothing: "&&" is not a narrowing somebody asked for.
  it("has asked nothing for a query it cannot read", () => {
    expect(boardNarrowing(board({ query: "&&" }))).toBeNull();
  });

  // The kinds that answer the question themselves: a Kanban board says
  // which states it is about in its columns, a timeline is a chart of
  // when work happened and most work that happened has finished, and a
  // report is not a view of tasks at all. Each of the three would be
  // narrowed away from what it is for.
  it("is every task for the kinds that answer for themselves", () => {
    expect(boardNarrowing(board({ kind: "kanban" }))).toBe(NO_NARROWING);
    expect(boardNarrowing(board({ kind: "gantt" }))).toBe(NO_NARROWING);
    expect(boardNarrowing(board({ kind: "report" }))).toBe(NO_NARROWING);
  });
});

describe("boardColumns", () => {
  it("uses the board's own columns when it carries any", () => {
    const columns = boardColumns(
      board({
        kind: "kanban",
        columns: [
          { name: "Waiting", states: ["proposed", "queued"] },
          { name: "Working", states: ["running"] },
        ],
      }),
    );
    expect(columns.map((c) => c.title)).toEqual(["Waiting", "Working"]);
    expect(columns[0].states).toEqual(["proposed", "queued"]);
    // Ids are made here, for the render, and are unique.
    expect(new Set(columns.map((c) => c.id)).size).toBe(2);
  });

  // A board asked for as "a kanban board of my open work" should not
  // have to invent a column layout to be a board: the browser's own is
  // the layout its reader already chose.
  it("leaves a board with no columns of its own to the browser's", () => {
    expect(boardColumns(board({ kind: "kanban" }))).toBeNull();
  });

  // A row that is not a column at all -- a hand-edited dump can hold
  // one -- is not an "Untitled" column collecting nothing.
  it("leaves a board whose columns are not columns to the browser's", () => {
    expect(boardColumns(board({ kind: "kanban", columns: ["nonsense"] }))).toBe(
      null,
    );
  });

  // A state this build has never heard of is dropped and the column
  // stays, which is board.js's own treatment of a stored layout written
  // by a newer build.
  it("drops a state this build does not have and keeps the column", () => {
    const columns = boardColumns(
      board({
        kind: "kanban",
        columns: [{ name: "Working", states: ["running", "nonsense"] }],
      }),
    );
    expect(columns).toHaveLength(1);
    expect(columns[0].states).toEqual(["running"]);
  });
});

describe("the tasks the board buttons file", () => {
  it("carries the narrowing on screen into a new board's prompt", () => {
    const task = newBoardTask({
      narrowing: {
        search: "flake",
        sort: [],
        filters: [{ attr: "repo", op: "any", values: ["acme/widgets"] }],
      },
    });
    expect(task.title).toBe("Add a board");
    expect(task.description).toContain("q=flake");
    expect(task.description).toContain("f=repo:any:acme/widgets");
    // ...and says where a board actually lives, since that is the whole
    // of what the agent has to do.
    expect(task.description).toContain("tables/board.json");
  });

  // An agent reading the prompt has to know what "gantt" means without
  // going and finding the renderer, and has to know that the range and
  // the grouping are not fields of the row it is being asked to write.
  it("says what a timeline board is when one is asked for", () => {
    const task = newBoardTask({ narrowing: NO_NARROWING, kind: "gantt" });
    expect(task.description).toContain("kind: gantt");
    expect(task.description).toContain("against a clock");
    expect(task.description).toContain("browser setting");
  });

  // A tree board with no query draws every task, done ones included
  // (boardNarrowing), so the prompt's note on empty queries has to say
  // so rather than leave the agent to guess it reads like a list.
  it("says what a tree board shows when its query is empty", () => {
    const d = newBoardTask({
      narrowing: NO_NARROWING,
      kind: "tree",
    }).description;
    expect(d).toContain("kind: tree");
    expect(d.replace(/\s+/g, " ")).toContain(
      "a Kanban board, a timeline, a tree or a report shows every task",
    );
    expect(boardNarrowing({ kind: "tree" })).toBe(NO_NARROWING);
  });

  // "Dashboard" is the name of one kind (panels), so a first line
  // calling every board one contradicted the "kind: list" a request from
  // a narrowed list carries a few lines below it (grain/task-1531).
  it("does not call a board of another kind a dashboard", () => {
    for (const kind of ["list", "kanban", "gantt", "report", "tree"]) {
      const task = newBoardTask({ narrowing: NO_NARROWING, kind });
      expect(task.description.split("\n")[0]).toBe(
        "Add a saved board to grain.",
      );
    }
  });

  // The kinds menu named the panels and report notes for every prompt,
  // and only a prompt about that kind carries the note.
  it("only points below at a note the prompt carries", () => {
    for (const kind of [
      "list",
      "kanban",
      "gantt",
      "panels",
      "report",
      "tree",
    ]) {
      const d = newBoardTask({ narrowing: NO_NARROWING, kind }).description;
      expect(d.includes("see the panels section below")).toBe(
        kind === "panels",
      );
      expect(d.includes("see the report section below")).toBe(
        kind === "report",
      );
    }
  });

  // A board's own columns have no editor on the page (TaskBoard's
  // fixedColumns), so the edit prompt is the only place they are said.
  it("lists the columns a Kanban board has when it is edited", () => {
    const task = editBoardTask(
      board({
        kind: "kanban",
        columns: [
          { name: "Waiting", states: ["queued", "proposed"] },
          { name: "Going", states: ["running"] },
        ],
      }),
      { narrowing: NO_NARROWING },
    );
    expect(task.description).toContain("Its columns, left to right:");
    expect(task.description).toContain('1. "Waiting": `queued`, `proposed`');
    expect(task.description).toContain('2. "Going": `running`');
  });

  it("says a Kanban board with no columns of its own follows the browser", () => {
    const task = editBoardTask(board({ kind: "kanban" }), {
      narrowing: NO_NARROWING,
    });
    expect(task.description).toContain("none of its own");
  });

  // A report's Window picker rides in the address bar, not the board's
  // query, and both prompts used to drop it (2026-10-13 walkthrough).
  it("says which window a report is being read over, and how to keep it", () => {
    const edit = editBoardTask(board({ kind: "report" }), {
      narrowing: NO_NARROWING,
      window: "90d",
    }).description;
    expect(edit).toContain("over the last 90 days");
    expect(edit).toContain("`f=created:within:90`");
    const fresh = newBoardTask({
      narrowing: NO_NARROWING,
      kind: "report",
      window: "24h",
    }).description;
    expect(fresh).toContain("over the last 24 hours");
    expect(fresh).toContain("`f=created:within:1`");
  });

  it("says nothing of a window for other kinds, or none picked", () => {
    for (const d of [
      editBoardTask(board({ kind: "report" }), { narrowing: NO_NARROWING })
        .description,
      editBoardTask(board({ kind: "gantt" }), {
        narrowing: NO_NARROWING,
        window: "30d",
      }).description,
      newBoardTask({ narrowing: NO_NARROWING, kind: "list", window: "30d" })
        .description,
    ])
      expect(d).not.toContain("Window picker");
  });

  it("asks for a board without a query when nothing is narrowed", () => {
    const task = newBoardTask({ narrowing: NO_NARROWING });
    expect(task.description).toContain("query: <see below>");
  });

  it("opens an edit on what the board says today", () => {
    const task = editBoardTask(
      board({ name: "Waiting on me", query: "f=state:any:awaiting_reply" }),
      { narrowing: NO_NARROWING },
    );
    expect(task.title).toBe('Change the board "Waiting on me"');
    expect(task.description).toContain("board-1");
    expect(task.description).toContain("f=state:any:awaiting_reply");
  });

  // The gesture the whole feature is for: narrow the board on screen
  // until it is right, then click Edit board.
  it("points out a narrowing the reader has made since", () => {
    const task = editBoardTask(board({ query: "q=old" }), {
      narrowing: { search: "new", sort: [], filters: [] },
    });
    expect(task.description).toContain("q=new");
    expect(task.description).toContain("probably the query it should be saved");
  });

  it("says nothing about a narrowing that is the board's own", () => {
    const task = editBoardTask(board({ query: "q=same" }), {
      narrowing: { search: "same", sort: [], filters: [] },
    });
    expect(task.description).not.toContain("probably the query");
  });
});

describe("boardKindLabel", () => {
  it("names the kinds there are, and calls anything else a list", () => {
    expect(boardKindLabel("list")).toBe("List");
    expect(boardKindLabel("kanban")).toBe("Kanban");
    // The chart's board kind is "gantt" and its label is the word the
    // nav rail already uses for the same view.
    expect(boardKindLabel("gantt")).toBe("Timeline");
    expect(boardKindLabel("burndown")).toBe("List");
  });
});

describe("kindForView", () => {
  // "New board" read from a timeline board means another timeline. This
  // is the half of "save what I am looking at" that the narrowing does
  // not carry.
  it("offers an open board's own kind, and a list when none is open", () => {
    expect(kindForView("boards", board({ kind: "gantt" }))).toBe("gantt");
    expect(kindForView("boards", null)).toBe("list");
  });

  // Every other view offers the list a board is by default: with the
  // fixed board and timeline gone from the rail (grain/task-162), a
  // board page is the only place left that is a layout as well as a
  // question. Asking for either of the other kinds is a word in the
  // prompt, which is why the prompt names all four.
  it("offers a list everywhere there is no layout to carry", () => {
    expect(kindForView("tasks")).toBe("list");
    expect(kindForView("repos")).toBe("list");
    expect(newBoardTask().description).toContain("kanban (columns of states");
  });
});

// A dashboard of panels (grain/task-88) is a kind of board like the
// other three: the rows underneath it are the only thing new, and they
// are normalized on the way in for the same reason a board is.
describe("a panels board", () => {
  it("is a kind this build has, with a word for it", () => {
    expect(boardKindLabel("panels")).toBe("Panels");
    expect(normalizeBoards([{ id: "board-1", kind: "panels" }])[0].kind).toBe(
      "panels",
    );
  });

  it("normalizes the panels it carries", () => {
    const [b] = normalizeBoards([
      {
        id: "board-1",
        kind: "panels",
        panels: [{ kind: "burndown" }, { kind: "gantt", width: "full" }],
      },
    ]);
    expect(b.panels.map((p) => [p.kind, p.width])).toEqual([
      ["list", "half"],
      ["gantt", "full"],
    ]);
  });

  it("has no panels when the row carries none", () => {
    expect(
      normalizeBoards([{ id: "board-1", kind: "panels" }])[0].panels,
    ).toEqual([]);
  });

  // An agent asked for a dashboard has to be told what a panel is and
  // which file the rows go in -- neither of which it can guess from the
  // board row alone.
  it("tells a new board's prompt what a panel is", () => {
    const task = newBoardTask({ narrowing: NO_NARROWING, kind: "panels" });
    expect(task.description).toContain("tables/board_panel.json");
    expect(task.description).toContain("`states`");
    expect(task.description).toContain("`third`");
  });

  it("says nothing about panels in a prompt for a board that has none", () => {
    const task = newBoardTask({ narrowing: NO_NARROWING, kind: "list" });
    expect(task.description).not.toContain("tables/board_panel.json");
  });

  // Editing a dashboard is editing its panels, so the prompt has to say
  // what the panels are today.
  it("lists the panels a dashboard has when it is edited", () => {
    const task = editBoardTask(
      board({
        kind: "panels",
        panels: [
          {
            key: "panel-0",
            kind: "gantt",
            title: "This week",
            width: "full",
            range: "7d",
            query: "",
          },
        ],
      }),
      { narrowing: NO_NARROWING },
    );
    expect(task.description).toContain('gantt "This week"');
    expect(task.description).toContain("tables/board_panel.json");
  });

  // A range the panel row names and this build cannot use is listed as
  // what it is rather than copied out verbatim. An agent reading "1d"
  // beside "7d" has been told this dashboard holds a one-day panel,
  // which it does not, and would write the same word again when asked
  // for another one like it.
  it("says when a panel's range is not one this build has", () => {
    const task = editBoardTask(
      board({
        kind: "panels",
        panels: [
          {
            key: "panel-0",
            kind: "gantt",
            title: "Yesterday",
            width: "half",
            range: "1d",
            query: "",
          },
          {
            key: "panel-1",
            kind: "list",
            title: "Mine",
            width: "half",
            range: "7d",
            query: "",
          },
        ],
      }),
      { narrowing: NO_NARROWING },
    );
    expect(task.description).toContain("not one this build has");
    expect(task.description).toContain("covers no span");
  });

  // The vocabulary a prompt offers is read off the build rather than
  // typed into the sentence, so a range this build gains or drops
  // cannot leave the prompt naming the wrong words -- PANEL_KINDS and
  // PANEL_WIDTHS are already written that way for the same reason.
  it("offers the ranges this build actually has", () => {
    const task = editBoardTask(board({ kind: "panels" }), {
      narrowing: NO_NARROWING,
    });
    for (const range of RANGES.map((r) => r.id)) {
      expect(task.description).toContain(`\`${range}\``);
    }
  });
});

describe("addPanelTask", () => {
  // The gesture: narrow the dashboard until it says what you meant,
  // then ask for that as a panel.
  it("carries the narrowing on screen into the panel it asks for", () => {
    const task = addPanelTask(board({ kind: "panels", name: "Operations" }), {
      narrowing: {
        search: "flake",
        sort: [],
        filters: [{ attr: "state", op: "any", values: ["failed"] }],
      },
    });
    expect(task.title).toBe('Add a panel to the dashboard "Operations"');
    expect(task.description).toContain("q=flake");
    expect(task.description).toContain("f=state:any:failed");
    expect(task.description).toContain("tables/board_panel.json");
  });

  // ...and says what it is being added to, so the row lands in the right
  // place in the order.
  it("says what the dashboard holds already", () => {
    const task = addPanelTask(
      board({
        kind: "panels",
        panels: [
          {
            key: "panel-0",
            kind: "states",
            title: "",
            width: "half",
            range: "",
            query: "",
          },
        ],
      }),
      { narrowing: NO_NARROWING },
    );
    expect(task.description).toContain("1. states");
  });

  // A dashboard opens on its own question, so a toolbar still showing
  // exactly that is not a narrowing anybody composed: the panel it asks
  // for inherits, which is an empty query rather than the board's own
  // written out a second time (grain/task-111).
  it("asks for a panel that follows the dashboard when nothing was narrowed", () => {
    const task = addPanelTask(
      board({ kind: "panels", query: "f=repo:any:acme/widgets" }),
      { narrowing: narrowingFromQuery("f=repo:any:acme/widgets") },
    );
    expect(task.description).not.toContain("`f=repo:any:acme/widgets`");
    expect(task.description).toContain("empty means the dashboard's own");
  });

  it("says a dashboard with no panels is empty", () => {
    const task = addPanelTask(board({ kind: "panels" }), {
      narrowing: NO_NARROWING,
    });
    expect(task.description).toContain("none yet");
  });

  // What the panels it lists are said to be, since an agent writing a
  // row among them has to know where the gaps in the order are and
  // which of them already carry a question.
  it("says each panel's order key and whether it has a query of its own", () => {
    const task = addPanelTask(
      board({
        kind: "panels",
        panels: [
          { kind: "list", title: "Mine", query: "q=flake", orderKey: 1048576 },
          { kind: "states", orderKey: 2097152 },
          { kind: "metrics", orderKey: 3145728 },
        ],
      }),
      { narrowing: NO_NARROWING },
    );
    expect(task.description).toContain("`order_key` 1048576");
    expect(task.description).toContain("narrowed to `q=flake`");
    expect(task.description).toContain(
      "following the dashboard's own question",
    );
    // ...the throughput panel among them, which since grain/task-317
    // follows the dashboard like the rest. Which parts of a query its
    // report can use is said once in the note below rather than on its
    // row.
    expect(task.description).toMatch(
      /3\. metrics.*following the dashboard's own question/,
    );
    expect(task.description).toContain("read as a *scope*");
  });
});

// "Keep" (PanelBoard.jsx): the reader has narrowed the dashboard from
// the toolbar, one panel is showing the answer, and this is the task
// that makes that narrowing the panel's own question. The step between
// "narrow it live" and "file a task about it" that neither of the two
// halves was doing.
describe("keepPanelQueryTask", () => {
  const dashboard = (over = {}) =>
    normalizeBoards([
      {
        id: "board-4",
        name: "Operations",
        kind: "panels",
        panels: [
          { kind: "list", title: "Live work", width: "full", orderKey: 1024 },
          { kind: "states", orderKey: 2048 },
        ],
        ...over,
      },
    ])[0];

  it("asks for the narrowing on screen as that panel's own query", () => {
    const b = dashboard();
    const task = keepPanelQueryTask(b, b.panels[0], {
      narrowing: narrowingFromQuery("q=sandbox"),
    });
    expect(task.title).toBe(
      'Narrow the panel "Live work" on the dashboard "Operations"',
    );
    expect(task.description).toContain("`q=sandbox`");
    expect(task.description).toContain("tables/board_panel.json");
  });

  // Which row it means, said the two ways a panel can be pointed at:
  // where it comes in the order, and the order key that is the only
  // thing on the row naming it.
  it("points at the one panel among the ones it lists", () => {
    const b = dashboard();
    const task = keepPanelQueryTask(b, b.panels[1], {
      narrowing: narrowingFromQuery("q=sandbox"),
    });
    expect(task.description).toContain("set the `query` of panel 2");
    expect(task.description).toMatch(
      /2\. states.*`order_key` 2048.*<-- this one/,
    );
    expect(task.description).not.toMatch(/1\. list.*<-- this one/);
  });

  // The consequence a reader cannot see from the gesture: a panel with
  // a query of its own stops following the toolbar (panels.js).
  it("says the panel will stop following the dashboard's toolbar", () => {
    const b = dashboard();
    const task = keepPanelQueryTask(b, b.panels[0], {
      narrowing: narrowingFromQuery("q=sandbox"),
    });
    expect(task.description).toContain("no longer narrowed by the dashboard");
  });

  // The dashboard's own question is not a narrowing anybody made, so
  // there is nothing to keep -- the same reading "Add panel" takes of
  // the same toolbar, and the prompt asks for the query in words rather
  // than writing the board's onto the panel.
  it("asks for a query in words when the dashboard is on its own question", () => {
    const b = dashboard({ query: "f=repo:any:acme/widgets" });
    const task = keepPanelQueryTask(b, b.panels[0], {
      narrowing: narrowingFromQuery("f=repo:any:acme/widgets"),
    });
    expect(task.description).not.toContain("`f=repo:any:acme/widgets`");
    expect(task.description).toContain("<the query it should ask");
  });
});

// keptQuery is the reading both dashboard prompts take of the toolbar,
// and what decides whether a panel offers to keep anything at all.
describe("keptQuery", () => {
  it("is the query on screen when the reader narrowed it", () => {
    expect(
      keptQuery(board({ kind: "panels" }), narrowingFromQuery("q=ci")),
    ).toBe("q=ci");
  });

  it("is empty when the toolbar is still showing the board's own question", () => {
    const b = board({ kind: "panels", query: "f=repo:any:acme/widgets" });
    expect(keptQuery(b, narrowingFromQuery("f=repo:any:acme/widgets"))).toBe(
      "",
    );
    expect(keptQuery(board({ kind: "panels" }), NO_NARROWING)).toBe("");
  });

  // A board naming no query opens on the deployment's own default
  // rather than on nothing (grain/task-309's boardNarrowing), so the
  // condition already on its toolbar is nobody's narrowing -- and the
  // reader's own is still theirs, default condition and all, because
  // that is the query the panel is answering on screen.
  it("is empty on the default a board with no query of its own opens on", () => {
    const b = board({ kind: "panels" });
    const config = { repos: [], capabilities: [] };
    expect(keptQuery(b, defaultNarrowing(config), config)).toBe("");
    // Their own, default condition and all: a panel carrying a query is
    // read as written and gets no default, so the condition has to ride
    // along or keeping this would widen the panel.
    expect(
      keptQuery(b, { ...defaultNarrowing(config), search: "ci" }, config),
    ).toBe("q=ci&f=state:none:deferred|closed");
  });
});

// A report board (grain/task-83) is a kind like the other four, and the
// first that is not a view of the backlog. What is new about it from
// this file's point of view is the sections it carries and -- the part
// that matters most, since an agent is the one reading it -- what the
// prompt has to say about a query whose conditions a report can only
// half use.
describe("a report board", () => {
  it("is a kind this build has, with a word for it", () => {
    expect(boardKindLabel("report")).toBe("Report");
    expect(normalizeBoards([{ id: "board-1", kind: "report" }])[0].kind).toBe(
      "report",
    );
  });

  it("normalizes the sections it carries", () => {
    const [b] = normalizeBoards([
      {
        id: "board-1",
        kind: "report",
        sections: [{ kind: "burndown" }, { kind: "latency", title: "Waits" }],
      },
    ]);
    expect(b.sections.map((s) => [s.kind, s.title])).toEqual([
      ["latency", "Waits"],
    ]);
  });

  it("has no sections when the row carries none", () => {
    expect(
      normalizeBoards([{ id: "board-1", kind: "report" }])[0].sections,
    ).toEqual([]);
  });

  // The three things an agent cannot work out from the row in front of
  // it: that this kind is not a view of the task list, which conditions
  // of the query actually reach the report, and where the section rows
  // live.
  it("tells a new board's prompt what a report board is", () => {
    const task = newBoardTask({ narrowing: NO_NARROWING, kind: "report" });
    expect(task.description).toContain("tables/board_section.json");
    expect(task.description).toContain("f=repo:any:");
    expect(task.description).toContain("f=created:within:");
    expect(task.description).toContain("is ignored");
    // Every section this build has, so a section added here cannot leave
    // the prompt describing one fewer.
    for (const section of REPORT_SECTIONS) {
      expect(task.description).toContain(`\`${section.id}\``);
    }
  });

  it("says leaving the sections out is a complete answer", () => {
    const task = newBoardTask({ narrowing: NO_NARROWING, kind: "report" });
    expect(task.description).toContain("prints the whole report");
  });

  // The section of the prompt about reports is only in a prompt about a
  // report. (Where the rows live is in the paragraph every board prompt
  // carries, beside the Kanban columns, because that paragraph is about
  // the state repository rather than about any one kind.)
  it("says nothing about reports in a prompt for another kind", () => {
    const task = newBoardTask({ narrowing: NO_NARROWING, kind: "list" });
    expect(task.description).not.toContain("f=created:within:");
    expect(task.description).not.toContain("prints the whole report");
  });

  it("lists the sections a report has when it is edited", () => {
    const task = editBoardTask(
      board({
        kind: "report",
        sections: [
          { key: "section-0", kind: "throughput", title: "Last thirty days" },
        ],
      }),
      { narrowing: NO_NARROWING },
    );
    expect(task.description).toContain('throughput "Last thirty days"');
    expect(task.description).toContain("tables/board_section.json");
  });

  it("says so when a report board names no sections at all", () => {
    const task = editBoardTask(board({ kind: "report", sections: [] }), {
      narrowing: NO_NARROWING,
    });
    expect(task.description).toContain("prints the whole report");
  });
});
