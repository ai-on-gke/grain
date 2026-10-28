import { describe, expect, it } from "vitest";
import {
  REPORT_SECTIONS,
  boardSections,
  hasAbsoluteSpan,
  normalizeSections,
  reportPath,
  reportScope,
  reportWindow,
  scopeSummary,
  sectionTitle,
} from "./report.js";
import { narrowingFromQuery } from "./paths.js";
import { ATTRIBUTES } from "./taskAttributes.js";

// The reading a report board gives a board's query (report.js's own
// note): three conditions become a scope the daemon can compute under,
// and everything else is dropped -- visibly, which is the half that
// needs testing as much as the scope does.
const scopeOf = (query) => reportScope(narrowingFromQuery(query), ATTRIBUTES);

describe("reportScope", () => {
  it("reads a repo and an author off the query", () => {
    const scope = scopeOf("f=repo:any:acme/widgets&f=author:any:ada");
    expect(scope.repos).toEqual(["acme/widgets"]);
    expect(scope.authors).toEqual(["ada"]);
    expect(scope.ignored).toEqual([]);
  });

  it("takes every value of an is-any-of condition", () => {
    expect(scopeOf("f=repo:any:acme/widgets|acme/docs").repos).toEqual([
      "acme/widgets",
      "acme/docs",
    ]);
  });

  // A date range is the report's window rather than a filter on filing
  // dates -- the one reading here that is not literal, argued in
  // report.js and printed on the page.
  it("reads a relative date range as a window", () => {
    expect(scopeOf("f=created:within:30").within).toBe("30d");
  });

  it("reads an absolute date range as a span", () => {
    const scope = scopeOf(
      "f=created:after:2026-01-01&f=created:before:2026-02-01",
    );
    expect(scope.since).toBe("2026-01-01");
    expect(scope.until).toBe("2026-02-01");
    expect(hasAbsoluteSpan(scope)).toBe(true);
  });

  // The half a report cannot use is named rather than silently dropped:
  // a board saved off a narrowed list will usually carry one of these,
  // and the reader is about to ask why the numbers are bigger than they
  // expected.
  it("names the conditions it could not use", () => {
    const scope = scopeOf(
      "q=flaky&f=state:any:running&f=repo:any:acme/widgets",
    );
    expect(scope.repos).toEqual(["acme/widgets"]);
    expect(scope.ignored).toEqual([
      'the search for "flaky"',
      "State (running)",
    ]);
  });

  // A comparison the endpoint cannot answer is not half-applied: "every
  // repo except this one" is a question about rows, and a report has no
  // rows to leave out.
  it("ignores a repo condition that is not is-any-of", () => {
    const scope = scopeOf("f=repo:none:acme/widgets");
    expect(scope.repos).toEqual([]);
    expect(scope.ignored).toEqual(["Repo (acme/widgets)"]);
  });

  // A sort has no rows to order, so it is neither used nor complained
  // about: it is not a narrowing at all.
  it("says nothing about a sort", () => {
    expect(scopeOf("sort=created:desc").ignored).toEqual([]);
  });

  it("is empty for a board that narrows nothing", () => {
    const scope = scopeOf("");
    expect(scope.repos).toEqual([]);
    expect(scope.authors).toEqual([]);
    expect(scope.ignored).toEqual([]);
    expect(scopeSummary(scope)).toBe("");
  });

  it("summarises the scope the way the page heads itself", () => {
    expect(
      scopeSummary(scopeOf("f=repo:any:acme/widgets&f=author:any:ada")),
    ).toBe("in acme/widgets, by ada");
  });

  it("does not complain about an incomplete filter condition still waiting for a value", () => {
    const scope = reportScope(
      {
        search: "",
        filters: [{ attr: "repo", op: "any", values: [] }],
      },
      ATTRIBUTES,
    );
    expect(scope.repos).toEqual([]);
    expect(scope.ignored).toEqual([]);
  });

  it("rejects out-of-range or non-integer created:within day counts as ignored conditions", () => {
    for (const bad of ["0", "-5", "1.5", "400"]) {
      const scope = scopeOf(`f=created:within:${bad}`);
      expect(scope.within).toBe("");
      expect(scope.ignored).toEqual([`Created (${bad})`]);
    }
  });
});

describe("reportPath", () => {
  it("sends the scope and a window ending now", () => {
    const path = reportPath({
      scope: scopeOf("f=repo:any:acme/widgets&f=author:any:ada"),
      window: "7d",
      buckets: 24,
    });
    const params = new URLSearchParams(path.slice(path.indexOf("?")));
    expect(params.getAll("repo")).toEqual(["acme/widgets"]);
    expect(params.getAll("author")).toEqual(["ada"]);
    expect(params.get("window")).toBe("7d");
    expect(params.get("buckets")).toBe("24");
  });

  // An absolute span is the more specific thing to have said, so it goes
  // instead of a window rather than beside it -- the endpoint would
  // ignore the window anyway, and sending both would be a URL that lies.
  it("sends an absolute span instead of a window", () => {
    const path = reportPath({
      scope: scopeOf("f=created:after:2026-01-01&f=created:before:2026-02-01"),
      window: "7d",
    });
    const params = new URLSearchParams(path.slice(path.indexOf("?")));
    expect(params.get("since")).toBe("2026-01-01");
    expect(params.get("until")).toBe("2026-02-01");
    expect(params.get("window")).toBe(null);
  });

  it("repeats the parameter once per repo", () => {
    const path = reportPath({
      scope: scopeOf("f=repo:any:acme/widgets|acme/docs"),
      window: "30d",
    });
    expect(
      new URLSearchParams(path.slice(path.indexOf("?"))).getAll("repo"),
    ).toEqual(["acme/widgets", "acme/docs"]);
  });
});

describe("reportWindow", () => {
  it("is the span the query names, or the default", () => {
    expect(reportWindow(scopeOf("f=created:within:30"), "7d")).toBe("30d");
    expect(reportWindow(scopeOf(""), "7d")).toBe("7d");
  });
});

describe("boardSections", () => {
  // A board that names no sections prints the whole report -- the
  // fallback a Kanban board with no columns gets, and what an agent
  // asked for "a report board for acme/widgets" produces.
  it("falls back to the whole report", () => {
    expect(boardSections({ sections: [] }).map((s) => s.kind)).toEqual(
      REPORT_SECTIONS.map((s) => s.id),
    );
    expect(boardSections(null).map((s) => s.kind)).toEqual(
      REPORT_SECTIONS.map((s) => s.id),
    );
  });

  it("keeps the board's own sections in the order they arrived", () => {
    const sections = boardSections({
      sections: [
        { kind: "latency" },
        { kind: "throughput", title: "Delivery" },
      ],
    });
    expect(sections.map((s) => s.kind)).toEqual(["latency", "throughput"]);
    expect(sectionTitle(sections[1])).toBe("Delivery");
    expect(sectionTitle(sections[0])).toBe("Latency");
  });

  // A section naming something this build does not have is dropped
  // rather than falling back to another one: no section stands in for
  // another, and drawing the throughput because a row said "burndown"
  // would be a lie about what is on screen.
  it("drops a section this build does not have", () => {
    expect(
      normalizeSections([{ kind: "burndown" }, { kind: "latency" }]).map(
        (s) => s.kind,
      ),
    ).toEqual(["latency"]);
    expect(normalizeSections("nonsense")).toEqual([]);
  });

  // ...and a board whose every section was one of those falls all the
  // way back, since "the board says nothing" is exactly the case the
  // fallback is for and an empty page is not an answer.
  it("falls back for a board whose sections are all unknown", () => {
    expect(boardSections({ sections: [{ kind: "burndown" }] }).length).toBe(
      REPORT_SECTIONS.length,
    );
  });
});
