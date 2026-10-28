import { beforeEach, describe, expect, it } from "vitest";
import {
  DEFAULT_WIDTH,
  METRICS_RANGES,
  PANEL_KINDS,
  normalizePanel,
  normalizePanels,
  panelGanttView,
  panelMetricsWindow,
  panelNarrowing,
  panelPath,
  panelRangeNote,
  panelRanges,
  panelRow,
  panelUsesNarrowing,
  panelTitle,
  panelView,
} from "./panels.js";
import { GANTT_STORAGE_KEY, RANGES } from "./gantt.js";
import { narrowingFromQuery } from "./paths.js";
import { reportScope } from "./report.js";
import { NO_NARROWING } from "./taskFilters.js";

// The panels a dashboard is assembled from (grain/task-88). Everything
// here is a pure reduction over a row that may have been hand-edited
// into the state repository, which is why it is pinned away from any
// render: a board written for a newer build has to draw.

describe("normalizePanels", () => {
  it("keeps a panel that says everything, in the words it said it in", () => {
    expect(
      normalizePanels([
        {
          kind: "gantt",
          title: "This week",
          query: "f=repo:any:acme/widgets",
          range: "30d",
          width: "full",
          orderKey: 2097152,
        },
      ])[0],
    ).toEqual({
      key: "panel-0",
      kind: "gantt",
      title: "This week",
      query: "f=repo:any:acme/widgets",
      range: "30d",
      width: "full",
      orderKey: 2097152,
      stored: {
        kind: "gantt",
        title: "This week",
        query: "f=repo:any:acme/widgets",
        range: "30d",
        width: "full",
      },
    });
  });

  // The row as the store holds it, kept beside the row as this build
  // draws it: a reorder sends the board's whole `panels` array back
  // (panelOrder.js), and a fallback made for a render must not be what
  // gets written.
  it("keeps the words a row it cannot read actually carried", () => {
    const panel = normalizePanel({ kind: "burndown", width: "quarter" });
    expect(panel.kind).toBe("list");
    expect(panel.width).toBe(DEFAULT_WIDTH);
    expect(panel.stored).toEqual({
      kind: "burndown",
      title: "",
      query: "",
      range: "",
      width: "quarter",
    });
  });

  it("puts a panel back on the wire at the key it is given", () => {
    const panel = normalizePanel({
      kind: "list",
      title: "Live work",
      query: "q=sandbox",
      orderKey: 7,
    });
    expect(panelRow(panel, 12)).toEqual({
      kind: "list",
      title: "Live work",
      query: "q=sandbox",
      range: "",
      width: "",
      orderKey: 12,
    });
  });

  // The order key is kept although nothing draws with it: it is the
  // only thing that names a panel row, and a prompt asking for one to
  // be changed has to say which (boards.js's panelLines).
  it("keeps the order key, and reads a missing one as zero", () => {
    expect(normalizePanel({ kind: "list", orderKey: 1048576 }).orderKey).toBe(
      1048576,
    );
    expect(normalizePanel({ kind: "list" }).orderKey).toBe(0);
    expect(normalizePanel({ kind: "list", orderKey: "third" }).orderKey).toBe(
      0,
    );
  });

  // A panel of a kind this build does not have falls back to the list
  // rather than leaving a hole: the tasks it is about are still worth
  // showing, and a dashboard written for a newer build should open.
  it("falls back to a list for a kind this build does not have", () => {
    expect(normalizePanel({ kind: "burndown" }).kind).toBe("list");
  });

  it("falls back to half for a width that is not a width", () => {
    expect(normalizePanel({ kind: "list", width: "wide" }).width).toBe(
      DEFAULT_WIDTH,
    );
    expect(normalizePanel({ kind: "list" }).width).toBe(DEFAULT_WIDTH);
  });

  it("reads anything that is not a list of panels as no panels", () => {
    expect(normalizePanels(null)).toEqual([]);
    expect(normalizePanels("nonsense")).toEqual([]);
    expect(normalizePanels([null, "nonsense"]).map((p) => p.kind)).toEqual([
      "list",
      "list",
    ]);
  });

  it("gives every panel a key of its own for the render", () => {
    const panels = normalizePanels([{ kind: "list" }, { kind: "list" }]);
    expect(panels[0].key).not.toBe(panels[1].key);
  });
});

describe("panelTitle", () => {
  it("is the panel's own heading when it has one", () => {
    expect(panelTitle({ kind: "list", title: "Waiting on me" })).toBe(
      "Waiting on me",
    );
  });

  // A panel that is exactly what it looks like needs no title invented
  // for it, so it is named after what it is.
  it("is the name of the kind when it has none", () => {
    expect(panelTitle({ kind: "states", title: "" })).toBe("State counts");
  });
});

describe("panelNarrowing", () => {
  // What the dashboard as a whole is asking: the board's own question
  // when nobody has touched the toolbar, and whatever the toolbar says
  // once somebody has (grain/task-111).
  const dashboard = narrowingFromQuery("f=repo:any:acme/widgets");

  it("is the panel's own query, read the way a link is read", () => {
    const narrowing = panelNarrowing(
      { query: "q=flake&f=state:any:failed" },
      dashboard,
    );
    expect(narrowing.search).toBe("flake");
    expect(narrowing.filters).toEqual([
      { attr: "state", op: "any", values: ["failed"] },
    ]);
  });

  // A panel that asks nothing of its own asks the dashboard's own
  // question -- which is what makes "a dashboard about one repo" one
  // query rather than one per panel.
  it("is the dashboard's own question when the panel has none", () => {
    const narrowing = panelNarrowing({ query: "" }, dashboard);
    expect(narrowing.filters).toEqual([
      { attr: "repo", op: "any", values: ["acme/widgets"] },
    ]);
  });

  // The narrowing it falls back on is the live one, so narrowing the
  // dashboard from its toolbar narrows every panel that inherits...
  it("follows the dashboard's narrowing as it moves", () => {
    const narrowed = narrowingFromQuery("f=repo:any:acme/gadgets&q=flake");
    expect(panelNarrowing({ query: "" }, narrowed)).toBe(narrowed);
  });

  // ...and leaves alone every panel that has already said what it is
  // about. The two are not intersected: two conditions on one attribute
  // narrow each other, so a panel about acme/widgets on a dashboard
  // narrowed to acme/gadgets would silently become a panel about
  // nothing.
  it("leaves a panel with its own question out of the dashboard's", () => {
    const narrowing = panelNarrowing(
      { query: "f=repo:any:acme/widgets" },
      narrowingFromQuery("f=repo:any:acme/gadgets"),
    );
    expect(narrowing.filters).toEqual([
      { attr: "repo", op: "any", values: ["acme/widgets"] },
    ]);
  });

  // A hand-edited row can carry a query this build can make nothing of.
  // That is a query that named nothing, so it inherits the way an empty
  // one does: a panel over every task would carry nothing in its "Open"
  // link either, and a link carrying nothing opens the list on the
  // deployment's default (grain/task-309).
  it("is the dashboard's own question for a query it cannot read", () => {
    expect(panelNarrowing({ query: "&&" }, dashboard)).toBe(dashboard);
  });

  it("is every task when neither says anything", () => {
    expect(panelNarrowing({}, null)).toBe(NO_NARROWING);
    expect(panelNarrowing({}, NO_NARROWING)).toBe(NO_NARROWING);
  });
});

describe("panelPath", () => {
  // The tasks a panel is about, asked the same question. The timeline
  // and the state counts used to land on /gantt and /board; those views
  // went with grain/task-162 and there is no fixed chart or fixed set of
  // columns left to hand a narrowing to, so all three land on the list
  // -- which is still the same tasks, and still one copyable URL.
  it("links to the full view of the tasks it is about, asking the same question", () => {
    expect(
      panelPath({ kind: "gantt", query: "f=state:any:running" }, null),
    ).toBe("/?f=state:any:running");
    expect(panelPath({ kind: "list", query: "q=flake" }, null)).toBe(
      "/?q=flake",
    );
    expect(panelPath({ kind: "states" }, narrowingFromQuery("q=ci"))).toBe(
      "/?q=ci",
    );
  });

  // The throughput panel goes to the report it is a small copy of, and
  // carries its scope there: the Metrics pane takes one now
  // (grain/task-379), so the pane the link opens is the one the panel
  // is drawing rather than the whole deployment's.
  it("links the throughput panel to its own report, carrying the scope", () => {
    expect(
      panelPath({ kind: "metrics", query: "f=repo:any:acme/widgets" }),
    ).toBe("/system/metrics?f=repo:any:acme/widgets");
    expect(
      panelPath({ kind: "metrics" }, narrowingFromQuery("f=author:any:ada")),
    ).toBe("/system/metrics?f=author:any:ada");
  });

  // ...and its span, which is the half the old link to the task list
  // could not carry at all. A report reads a `created` range as the
  // window to measure over (report.js), so that is what the panel's own
  // range travels as.
  it("carries the panel's own range as the report's window", () => {
    expect(panelPath({ kind: "metrics", range: "30d" })).toBe(
      "/system/metrics?f=created:within:30",
    );
    expect(panelPath({ kind: "metrics", range: "24h" })).toBe(
      "/system/metrics?f=created:within:1",
    );
    // A panel that names no range says nothing about the span, and the
    // pane opens on the default every report here shares.
    expect(
      panelPath({ kind: "metrics", query: "f=repo:any:acme/widgets" }),
    ).toBe("/system/metrics?f=repo:any:acme/widgets");
  });

  // The panel's range wins over a relative span the narrowing names,
  // exactly as panelMetricsWindow prefers it on the panel itself.
  it("replaces the span the narrowing names with the panel's own", () => {
    expect(
      panelPath(
        { kind: "metrics", range: "90d" },
        narrowingFromQuery("f=created:within:7&f=repo:any:acme/widgets"),
      ),
    ).toBe("/system/metrics?f=repo:any:acme/widgets&f=created:within:90");
  });

  // An absolute span is the one thing the panel's range does not
  // replace: dates are the more specific thing to have said, and
  // reportPath sends them in place of a window at both ends of the link.
  it("leaves a span named as actual dates alone", () => {
    expect(
      panelPath(
        { kind: "metrics", range: "7d" },
        narrowingFromQuery("f=created:after:2026-08-01"),
      ),
    ).toBe("/system/metrics?f=created:after:2026-08-01");
  });

  // A range this build cannot ask a report for ("all") is not a window,
  // so it travels as nothing rather than as a span nobody named.
  it("carries no span for a range the report cannot be asked for", () => {
    expect(panelPath({ kind: "metrics", range: "all" })).toBe(
      "/system/metrics",
    );
  });

  it("names a view for every kind there is", () => {
    for (const kind of PANEL_KINDS) {
      expect(panelView({ kind: kind.id })).toBe(kind.view);
    }
  });
});

// Whether a given narrowing would mean anything written onto a given
// panel, which is what decides whether "keep this narrowing" is offered
// on one (PanelBoard.jsx).
describe("panelUsesNarrowing", () => {
  const q = (query) => narrowingFromQuery(query);

  it("is true of every kind that narrows the tasks it draws", () => {
    for (const kind of ["list", "gantt", "states"]) {
      expect(panelUsesNarrowing({ kind }, q("q=flake"))).toBe(true);
    }
  });

  // The throughput panel reads a query as a scope rather than as a
  // filter (grain/task-317), so it is the one kind where the answer
  // depends on what the narrowing says: three things reach the report
  // and the rest are dropped, and keeping something that would be
  // dropped is keeping nothing.
  it("is true of the throughput panel for what its report can be scoped by", () => {
    for (const query of [
      "f=repo:any:acme/widgets",
      "f=author:any:ada",
      "f=created:within:30",
      "f=created:after:2026-01-01",
    ]) {
      expect(panelUsesNarrowing({ kind: "metrics" }, q(query))).toBe(true);
    }
  });

  it("is false of the throughput panel for a narrowing its report drops", () => {
    for (const query of [
      "q=flake",
      "f=state:any:running",
      "sort=created:desc",
    ]) {
      expect(panelUsesNarrowing({ kind: "metrics" }, q(query))).toBe(false);
    }
    expect(panelUsesNarrowing({ kind: "metrics" }, NO_NARROWING)).toBe(false);
  });
});

describe("panelGanttView", () => {
  beforeEach(() => localStorage.clear());

  // The dashboard says how far back its timeline looks; the reader's own
  // browser still says how the bars are grouped, which is the split the
  // board already makes between what a board is about and how somebody
  // is reading it.
  it("takes the panel's range over the browser's, and the browser's lanes", () => {
    localStorage.setItem(
      GANTT_STORAGE_KEY,
      JSON.stringify({ range: "24h", lanes: "repo" }),
    );
    expect(
      panelGanttView(
        { range: "30d" },
        JSON.parse(localStorage.getItem(GANTT_STORAGE_KEY)),
      ),
    ).toEqual({ range: "30d", lanes: "repo" });
  });

  it("falls back to the browser's range when the panel names none", () => {
    expect(
      panelGanttView({ range: "" }, { range: "24h", lanes: "none" }),
    ).toEqual({ range: "24h", lanes: "none" });
  });

  it("falls back for a range this build has dropped", () => {
    expect(
      panelGanttView(
        { range: "since-tuesday" },
        { range: "24h", lanes: "none" },
      ).range,
    ).toBe("24h");
  });
});

describe("panelMetricsWindow", () => {
  it("is the panel's own window when the report can be asked for one", () => {
    expect(panelMetricsWindow({ range: "30d" })).toBe("30d");
  });

  // "Everything" is a range the timeline sizes to its own bars; the
  // report is computed over a window that ends now, so there is nothing
  // to ask it for.
  it("falls back for a range the report cannot be asked for", () => {
    expect(panelMetricsWindow({ range: "all" })).toBe("7d");
    expect(panelMetricsWindow({ range: "" })).toBe("7d");
  });

  // The span follows the same rule as the question: a panel that has
  // said what it measures over goes on measuring over it while the
  // toolbar moves, and a panel that says nothing follows the dashboard
  // -- where a date range means the span to measure rather than a
  // filter on filing dates (report.js).
  it("takes the narrowing's span only when the panel names none", () => {
    const scope = reportScope(narrowingFromQuery("f=created:within:30"), []);
    expect(panelMetricsWindow({ range: "" }, scope)).toBe("30d");
    expect(panelMetricsWindow({ range: "24h" }, scope)).toBe("24h");
  });
});

// The falling back above is right and it used not to be visible, which
// is a different thing: a panel written `1d` drew a week and looked
// exactly like a panel that had asked for one. A hand run measuring the
// real dashboard was misled by precisely that
// (docs/hand-runs/2026-09-07-panel-dashboard.md, section 5).
describe("panelRangeNote", () => {
  it("says nothing about a panel whose range this build has", () => {
    for (const range of RANGES.map((r) => r.id)) {
      expect(panelRangeNote({ kind: "gantt", range }, range)).toBe("");
    }
    for (const range of METRICS_RANGES) {
      expect(panelRangeNote({ kind: "metrics", range }, range)).toBe("");
    }
  });

  it("says nothing about a panel that named no range at all", () => {
    expect(panelRangeNote({ kind: "gantt", range: "" }, "7d")).toBe("");
    expect(panelRangeNote({ kind: "list" }, undefined)).toBe("");
  });

  // The word it could not use, and what it drew instead -- which on a
  // timeline is the browser's own last range, so "instead" is not a
  // constant anybody could have guessed.
  it("names the word it could not use and the span it drew", () => {
    const note = panelRangeNote({ kind: "gantt", range: "1d" }, "7d");
    expect(note).toContain("1d");
    expect(note).toContain("last 7 days");
  });

  // Not a typo: a range this build has that a report cannot be measured
  // over. Told it was not a range, its author would go looking for a
  // misspelling that is not there.
  it("says why a report cannot be measured over everything", () => {
    const note = panelRangeNote({ kind: "metrics", range: "all" }, "7d");
    expect(note).toContain("all");
    expect(note).toContain("report");
    expect(note).not.toContain("not a range this build has");
  });

  // The other way a range goes missing: written on a kind that draws no
  // span, where there is no fallback at all because there was nothing
  // to fall back from.
  it("says a range on a panel that covers no span is not read", () => {
    const note = panelRangeNote({ kind: "list", range: "7d" }, undefined);
    expect(note).toContain("7d");
    expect(note).toContain("not read");
    expect(
      panelRangeNote({ kind: "states", range: "24h" }, undefined),
    ).toContain("not read");
  });
});

describe("panelRanges", () => {
  it("gives the timeline every range and the report every window but one", () => {
    expect(panelRanges("gantt")).toEqual(RANGES.map((r) => r.id));
    expect(panelRanges("metrics")).toEqual(METRICS_RANGES);
    expect(panelRanges("metrics")).not.toContain("all");
  });

  // Null rather than an empty list: "this kind has no range" and "this
  // kind's range vocabulary happens to be empty" are different facts,
  // and the callers say different sentences about them.
  it("gives the kinds that cover no span no vocabulary at all", () => {
    expect(panelRanges("list")).toBeNull();
    expect(panelRanges("states")).toBeNull();
  });
});
