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

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { buildProcessRows } from "./processes.js";
import {
  DEFAULT_PROCESS_NARROWING,
  DEFAULT_PROCESS_SORT,
  PROCESS_ATTRIBUTES,
  kindNarrowing,
  kindsOf,
  narrowProcesses,
  processNarrowingFromSearch,
  processNarrowingOf,
  processNarrowingQuery,
  scheduleTypeOf,
  sourceOf,
  withKinds,
} from "./processFilters.js";
import { directionLabels, operatorsFor, optionsFor } from "./taskFilters.js";

// The same four schedules pkg/schedulefilter's filter_test.go asks its
// questions of, at the same moment, so the two readings can be checked
// against each other line for line.
const NOW = Date.parse("2026-10-24T12:00:00Z");
const at = (hours) => new Date(NOW + hours * 3600 * 1000).toISOString();

const schedules = [
  {
    id: "sched-a",
    title: "Nightly deps",
    source: "bwsalmon/grain",
    type: "template",
    declaredIn: {
      repo: "bwsalmon/grain",
      path: ".grain/schedules/nightly.yml",
    },
    repo: "bwsalmon/grain",
    templateId: "repo:template:deps",
    templateName: "deps",
    enabled: true,
    recurrence: { kind: "daily", timeOfDay: "02:00" },
    nextRunAt: at(2),
    lastRunAt: at(-22),
  },
  {
    id: "sched-b",
    title: "Candidate cut",
    source: "deployment",
    type: "task",
    repo: "bwsalmon/grain",
    base: "candidate",
    enabled: false,
    recurrence: { kind: "weekly", weekday: "tuesday", timeOfDay: "09:00" },
    nextRunAt: at(72),
  },
  {
    id: "sched-c",
    title: "Upgrade",
    system: true,
    templateId: "system:upgrade",
    enabled: true,
    recurrence: { kind: "everyNHours", everyNHours: 6 },
    nextRunAt: at(-1 / 60),
  },
  {
    id: "sched-d",
    title: "Workflow nightly",
    declaredIn: { repo: "acme/widgets", path: ".grain/schedules/ci.yml" },
    workflowReview: "on-failure",
    templateId: "system:workflow-review:ci",
    system: true,
    repo: "acme/widgets",
    enabled: true,
    recurrence: { kind: "daily", timeOfDay: "06:00" },
    nextRunAt: at(30),
    lastWorkflowRun: { conclusion: "failure" },
  },
];
const templates = [
  {
    id: "template-1",
    name: "deps",
    repo: "bwsalmon/grain",
    autoMerge: true,
    declaredIn: { repo: "bwsalmon/grain", path: ".grain/templates/deps.yml" },
  },
];
const suites = [
  {
    id: "suite-1",
    name: "release",
    items: [{ templateId: "template-1" }],
    requireApproval: true,
  },
];

const rows = buildProcessRows({ schedules, templates, suites });
const ids = (narrowing) =>
  narrowProcesses(rows, narrowing, { timeZone: "UTC" }).map((r) => r.id);
const only = (...filters) => ids({ sort: [], filters });

describe("processFilters", () => {
  // A next run is still to come, so "oldest first" would name the run
  // furthest off as the old one; a last run has happened, and keeps
  // the date wording.
  it("words the next run's sort as soonest and latest, and the last run's as oldest and newest", () => {
    const attr = (id) => PROCESS_ATTRIBUTES.find((a) => a.id === id);
    expect(directionLabels(attr("next"))).toEqual({
      asc: "Soonest first",
      desc: "Latest first",
    });
    expect(directionLabels(attr("last"))).toEqual({
      asc: "Oldest first",
      desc: "Newest first",
    });
  });

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
  });
  afterEach(() => vi.useRealTimers());

  it("reads where each row is declared", () => {
    const of = Object.fromEntries(rows.map((r) => [r.id, sourceOf(r)]));
    expect(of).toEqual({
      "sched-a": "bwsalmon/grain",
      "sched-b": "deployment",
      // Worked out from the row when the daemon sends no source.
      "sched-c": "system",
      "sched-d": "acme/widgets",
      "template-1": "bwsalmon/grain",
      "suite-1": "deployment",
    });
  });

  it("reads what each schedule fires, from the wire or from its fields", () => {
    expect(schedules.map(scheduleTypeOf)).toEqual([
      "template",
      "task",
      "system",
      "workflow",
    ]);
    expect(scheduleTypeOf({ suiteId: "suite-1" })).toBe("suite");
    expect(scheduleTypeOf({ templateId: "t" })).toBe("template");
  });

  it("offers the comparisons the brief names, and next run's forward window", () => {
    const attr = (id) => PROCESS_ATTRIBUTES.find((a) => a.id === id);
    expect(operatorsFor(attr("source"))).toEqual(["any", "none"]);
    expect(operatorsFor(attr("name"))).toEqual(["has", "hasnot"]);
    expect(operatorsFor(attr("next"))).toEqual([
      "coming",
      "within",
      "after",
      "before",
      "empty",
      "filled",
    ]);
    expect(operatorsFor(attr("last"))).not.toContain("coming");
    // A menu offers only values the rows carry, in a fixed order where
    // there is one.
    expect(optionsFor(attr("type"), rows, null).map((o) => o.value)).toEqual([
      "template",
      "task",
      "workflow",
      "system",
      "__none__",
    ]);
    expect(optionsFor(attr("source"), rows, null).map((o) => o.label)).toEqual([
      "acme/widgets",
      "bwsalmon/grain",
      "grain (system)",
      "This deployment (UI, API or config repo)",
    ]);
  });

  // The same questions, with the same answers, as filter_test.go's
  // TestFilterReadsTheProcessesPagesConditions -- over schedules only,
  // as the API is.
  it("answers each condition the way GET /api/schedules does", () => {
    const schedulesOnly = {
      attr: "kind",
      op: "any",
      values: ["schedule", "workflow"],
    };
    const ask = (...filters) => only(schedulesOnly, ...filters);
    expect(
      ask({ attr: "source", op: "any", values: ["bwsalmon/grain"] }),
    ).toEqual(["sched-a"]);
    expect(
      ask({
        attr: "source",
        op: "any",
        values: ["bwsalmon/grain", "acme/widgets"],
      }),
    ).toEqual(["sched-a", "sched-d"]);
    expect(
      ask({ attr: "source", op: "none", values: ["system", "deployment"] }),
    ).toEqual(["sched-a", "sched-d"]);
    expect(ask({ attr: "type", op: "any", values: ["template"] })).toEqual([
      "sched-a",
    ]);
    expect(
      ask({ attr: "type", op: "none", values: ["template", "task"] }),
    ).toEqual(["sched-c", "sched-d"]);
    expect(ask({ attr: "name", op: "has", values: ["candidate"] })).toEqual([
      "sched-b",
    ]);
    expect(ask({ attr: "name", op: "hasnot", values: ["nightly"] })).toEqual([
      "sched-b",
      "sched-c",
    ]);
    expect(ask({ attr: "enabled", op: "any", values: ["no"] })).toEqual([
      "sched-b",
    ]);
    expect(ask({ attr: "base", op: "any", values: ["__none__"] })).toEqual([
      "sched-a",
      "sched-c",
      "sched-d",
    ]);
    expect(ask({ attr: "template", op: "any", values: ["deps"] })).toEqual([
      "sched-a",
    ]);
    expect(
      ask({ attr: "lastOutcome", op: "any", values: ["failure"] }),
    ).toEqual(["sched-d"]);
    expect(ask({ attr: "last", op: "empty", values: [] })).toEqual([
      "sched-b",
      "sched-c",
      "sched-d",
    ]);
    // What fires in the next six hours: the overdue one included.
    expect(ask({ attr: "next", op: "coming", values: ["6"] })).toEqual([
      "sched-a",
      "sched-c",
    ]);
    expect(ask({ attr: "last", op: "within", values: ["2"] })).toEqual([
      "sched-a",
    ]);
    expect(ask({ attr: "next", op: "after", values: ["2026-10-25"] })).toEqual([
      "sched-b",
      "sched-d",
    ]);
    expect(ask({ attr: "next", op: "before", values: ["2026-10-25"] })).toEqual(
      ["sched-a", "sched-c"],
    );
    // A half-written condition narrows nothing.
    expect(ask({ attr: "enabled", op: "any", values: [] })).toHaveLength(4);
  });

  it("asks the schedule-only attributes of every kind, and only schedules answer", () => {
    expect(only({ attr: "enabled", op: "any", values: ["yes"] })).toEqual([
      "sched-a",
      "sched-c",
      "sched-d",
    ]);
    expect(
      only({ attr: "source", op: "any", values: ["bwsalmon/grain"] }),
    ).toEqual(["sched-a", "template-1"]);
    expect(
      only({ attr: "requireApproval", op: "any", values: ["yes"] }),
    ).toEqual(["suite-1"]);
    expect(only({ attr: "autoMerge", op: "any", values: ["yes"] })).toEqual([
      "template-1",
    ]);
  });

  it("searches the name and the id", () => {
    expect(ids({ search: "sched-b", sort: [] })).toEqual(["sched-b"]);
    expect(ids({ search: "nightly.yml", sort: [] })).toEqual(["sched-a"]);
  });

  it("sorts by next run by default, and by several keys", () => {
    expect(ids(DEFAULT_PROCESS_NARROWING)).toEqual([
      "sched-c",
      "sched-a",
      "sched-d",
      "sched-b",
      // Nothing fires either of these, so neither has a next run, and
      // a row with none sorts last, by name.
      "template-1",
      "suite-1",
    ]);
    expect(
      ids({
        sort: [
          { attr: "kind", dir: "asc" },
          { attr: "name", dir: "desc" },
        ],
      }),
    ).toEqual([
      "sched-c",
      "sched-a",
      "sched-b",
      "sched-d",
      "template-1",
      "suite-1",
    ]);
  });

  it("keeps the kind chips as the one kind condition", () => {
    expect(kindsOf([])).toEqual([]);
    const f = withKinds(
      [{ attr: "enabled", op: "any", values: ["no"] }],
      ["suite", "schedule"],
    );
    expect(f).toEqual([
      { attr: "kind", op: "any", values: ["schedule", "suite"] },
      { attr: "enabled", op: "any", values: ["no"] },
    ]);
    expect(kindsOf(f)).toEqual(["schedule", "suite"]);
    expect(withKinds(f, ["template"])[0].values).toEqual(["template"]);
    expect(withKinds(f, [])).toEqual([
      { attr: "enabled", op: "any", values: ["no"] },
    ]);
    // "Kind is none of" is not something the chips can say.
    expect(
      kindsOf([{ attr: "kind", op: "none", values: ["trigger"] }]),
    ).toEqual([]);
  });

  it("reads and writes the narrowing as a link, old links included", () => {
    const query =
      "?q=x&sort=next:desc&f=source:any:bwsalmon/grain&f=name:has:a%253Ab";
    const parsed = processNarrowingFromSearch(new URLSearchParams(query));
    expect(parsed).toEqual({
      search: "x",
      sort: [{ attr: "next", dir: "desc" }],
      filters: [
        { attr: "source", op: "any", values: ["bwsalmon/grain"] },
        { attr: "name", op: "has", values: ["a:b"] },
      ],
    });
    expect(processNarrowingQuery(parsed)).toBe(query);
    expect(processNarrowingFromSearch(new URLSearchParams(""))).toBe(null);
    // A key the page has no attribute for is dropped, as an unknown kind
    // is; with nothing left the order is the default one.
    expect(
      processNarrowingFromSearch(new URLSearchParams("?sort=bogus:up")).sort,
    ).toEqual(DEFAULT_PROCESS_SORT);
    expect(
      processNarrowingFromSearch(new URLSearchParams("?sort=bogus,name:desc"))
        .sort,
    ).toEqual([{ attr: "name", dir: "desc" }]);
    expect(processNarrowingQuery(DEFAULT_PROCESS_NARROWING)).toBe("");
    expect(processNarrowingQuery(kindNarrowing(["schedule"]))).toBe(
      "?kind=schedule",
    );
    expect(
      processNarrowingOf({ kinds: ["suite"], q: "r", sort: "last" }),
    ).toEqual({
      search: "r",
      sort: [{ attr: "last", dir: "desc" }],
      filters: [{ attr: "kind", op: "any", values: ["suite"] }],
    });
  });
});
