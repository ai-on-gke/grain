import { describe, expect, it } from "vitest";
import {
  buildProcessRows,
  countByKind,
  declaringRepos,
  describeRecurrence,
  isWorkflowSchedule,
  kindsParam,
  parseKinds,
  triggersOf,
} from "./processes.js";
import {
  DEFAULT_PROCESS_NARROWING,
  narrowProcesses,
} from "./processFilters.js";

const schedules = [
  {
    id: "sched-1",
    title: "Nightly",
    repo: "acme/widgets",
    templateId: "template-1",
    enabled: true,
    lastRunAt: "2026-10-18T00:00:00Z",
    nextRunAt: "2026-10-21T00:00:00Z",
  },
  {
    id: "sched-2",
    title: "Hourly",
    repo: "acme/widgets",
    templateId: "template-1",
    enabled: true,
    lastRunAt: "2026-10-19T00:00:00Z",
    nextRunAt: "2026-10-20T12:00:00Z",
  },
  {
    id: "sched-3",
    title: "Paused one",
    repo: "acme/widgets",
    templateId: "template-1",
    enabled: false,
    nextRunAt: "2026-10-20T01:00:00Z",
  },
  {
    id: "repo:schedule:acme:widgets:nightly",
    title: "nightly workflow",
    repo: "acme/widgets",
    workflowReview: "on-failure",
    enabled: true,
    nextRunAt: "2026-10-22T00:00:00Z",
    declaredIn: { repo: "acme/widgets", path: ".grain/workflows/nightly.yml" },
  },
  {
    id: "sched-5",
    title: "Suite nightly",
    repo: "acme/widgets",
    suiteId: "suite-1",
    enabled: true,
    nextRunAt: "2026-10-23T00:00:00Z",
  },
];
const templates = [
  { id: "template-1", name: "Bump", repo: "acme/widgets" },
  { id: "template-2", name: "Unbound" },
  {
    id: "template-3",
    name: "Declared",
    repo: "acme/gadgets",
    declaredIn: { repo: "acme/gadgets", path: ".grain/templates/d.yml" },
  },
];
const suites = [
  {
    id: "suite-1",
    name: "Qualify",
    items: [{ templateId: "template-1" }, { templateId: "template-3" }],
  },
  { id: "suite-2", name: "Lonely", items: [{ templateId: "template-1" }] },
];
const suiteRuns = [
  { id: 1, suiteId: "suite-1", createdAt: "2026-10-01T00:00:00Z" },
  { id: 2, suiteId: "suite-1", createdAt: "2026-10-15T00:00:00Z" },
];
const triggers = [{ repo: "acme/gadgets", path: ".grain/triggers/docs.yml" }];

const rows = buildProcessRows({
  schedules,
  templates,
  suites,
  suiteRuns,
  triggers,
});
const row = (key) => rows.find((r) => r.key === key);

describe("processes", () => {
  it("lists one row per item of each kind, with keys unique across kinds", () => {
    expect(rows).toHaveLength(5 + 3 + 2 + 1);
    expect(new Set(rows.map((r) => r.key)).size).toBe(rows.length);
    expect(countByKind(rows)).toEqual({
      schedule: 4,
      workflow: 1,
      template: 3,
      suite: 2,
      trigger: 1,
    });
  });

  it("classifies a schedule with a workflowReview as a workflow schedule", () => {
    expect(isWorkflowSchedule(schedules[3])).toBe(true);
    expect(isWorkflowSchedule(schedules[0])).toBe(false);
    expect(row("schedule:repo:schedule:acme:widgets:nightly").kind).toBe(
      "workflow",
    );
  });

  it("derives a template's last and next runs from the schedules that fire it", () => {
    const t = row("template:template-1");
    // The newest last run of the three, and the soonest next run of
    // the two that are enabled: the paused one's earlier time is not a
    // firing that is coming.
    expect(t.lastRunAt).toBe("2026-10-19T00:00:00Z");
    expect(t.nextRunAt).toBe("2026-10-20T12:00:00Z");
    expect(t.nextLabel).toBe(null);
    expect(t.enabled).toBe(null);
    expect(row("template:template-2")).toMatchObject({
      lastRunAt: null,
      nextRunAt: null,
      nextLabel: "on demand",
      repo: "any",
    });
  });

  it("derives a suite's runs from its own runs and the schedules that fire it", () => {
    expect(row("suite:suite-1")).toMatchObject({
      lastRunAt: "2026-10-15T00:00:00Z",
      nextRunAt: "2026-10-23T00:00:00Z",
      repo: "several",
    });
    expect(row("suite:suite-2")).toMatchObject({
      lastRunAt: null,
      nextLabel: "on demand",
      repo: "acme/widgets",
    });
  });

  it("lists a trigger by its path, read-only, on change", () => {
    expect(row("trigger:acme/gadgets:.grain/triggers/docs.yml")).toMatchObject({
      kind: "trigger",
      name: "docs.yml",
      repo: "acme/gadgets",
      lastRunAt: null,
      nextLabel: "on change",
      enabled: null,
    });
  });

  it("round-trips the kind filter's query, in the chips' order", () => {
    expect(parseKinds("suite,schedule,bogus")).toEqual(["schedule", "suite"]);
    expect(kindsParam(["suite", "schedule"])).toBe("schedule,suite");
    expect(kindsParam([])).toBe("");
    expect(
      kindsParam(["schedule", "workflow", "template", "suite", "trigger"]),
    ).toBe("");
    expect(parseKinds(kindsParam(["trigger", "workflow"]))).toEqual([
      "workflow",
      "trigger",
    ]);
  });

  it("sorts by next run by default, rows with none last", () => {
    const sorted = narrowProcesses(rows, DEFAULT_PROCESS_NARROWING);
    expect(sorted[0].key).toBe("schedule:sched-3");
    expect(sorted.at(-1).nextRunAt).toBe(null);
    const byLast = narrowProcesses(rows, {
      sort: [{ attr: "last", dir: "desc" }],
    });
    expect(byLast[0].key).toBe("template:template-1");
  });

  it("searches the name, the repo, the cadence and where a row was declared", () => {
    const matches = (r, q) =>
      narrowProcesses([r], { search: q, sort: [] }).length === 1;
    expect(matches(row("template:template-3"), "templates/d.yml")).toBe(true);
    expect(matches(row("schedule:sched-1"), "WIDGETS")).toBe(true);
    expect(matches(row("schedule:sched-1"), "gadgets")).toBe(false);
    expect(
      matches(
        {
          name: "x",
          id: "x",
          item: { recurrence: { kind: "daily", timeOfDay: "09:15" } },
        },
        "09:15",
      ),
    ).toBe(true);
  });

  it("reads triggers from every repository something was declared in", () => {
    expect(declaringRepos({ schedules, templates, suites })).toEqual([
      "acme/gadgets",
      "acme/widgets",
    ]);
    expect(declaringRepos({ schedules: { not: "a list" } })).toEqual([]);
    // A target repository is read too: it may declare only a trigger.
    expect(
      declaringRepos({ schedules, repos: ["acme/zeta", "acme/widgets"] }),
    ).toEqual(["acme/widgets", "acme/zeta"]);
  });

  it("reads a repository's triggers out of its declarations status", () => {
    expect(
      triggersOf("acme/widgets", {
        repo: "acme/widgets",
        declarationsStatus: { triggers: [".grain/triggers/payments.yml"] },
      }),
    ).toEqual([{ repo: "acme/widgets", path: ".grain/triggers/payments.yml" }]);
    expect(triggersOf("acme/widgets", { declarations: false })).toEqual([]);
  });

  it("describes a cadence the way the row shows it", () => {
    expect(describeRecurrence({ kind: "everyNHours", everyNHours: 6 })).toBe(
      "every 6h",
    );
    expect(
      describeRecurrence({ kind: "everyNMinutes", everyNMinutes: 30 }),
    ).toBe("every 30m");
    expect(
      describeRecurrence({
        kind: "weekly",
        weekday: "friday",
        timeOfDay: "08:30",
        timeZone: "America/New_York",
      }),
    ).toBe("Fridays at 08:30 America/New_York");
  });
});
