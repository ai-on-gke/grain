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

import { beforeEach, describe, expect, it } from "vitest";
import {
  ATTRIBUTES,
  CUSTOM_PREFIX,
  attributeById,
  attributesFor,
  originOf,
} from "./taskAttributes.js";
// A URL is read by the same parser the address bar's is (paths.js), so
// the old-spelling link a test below opens is the one somebody kept
// rather than a narrowing hand-written to look like it.
import { parsePath } from "./paths.js";
import {
  NO_NARROWING,
  NONE,
  defaultFilters,
  defaultNarrowing,
  defaultSortFor,
  describeCondition,
  describeSort,
  describeUnknownConditions,
  directionLabels,
  isDefaultSort,
  isSessionTask,
  isNarrowed,
  matchesFilters,
  narrowTasks,
  newCondition,
  conditionFor,
  operatorsFor,
  optionsFor,
  resolveConditions,
  sortTasks,
  unknownConditions,
} from "./taskFilters.js";

// The comparisons and the vocabulary they are made of, tested apart from
// the editor that drives them (NarrowingBar.test.jsx) -- these are the
// answers a link, a chip and a list of rows all depend on being the
// same.
const config = {
  capabilities: [
    { id: "gh", name: "GitHub" },
    { id: "gcp", name: "Google Cloud" },
  ],
};

const task = (extra) => ({
  id: "1",
  title: "Fix the flake",
  state: "queued",
  capabilities: [],
  blocked: false,
  ...extra,
});

const attrs = attributesFor(config);
const attr = (id) => attributeById(attrs, id);

// conditions is what a view holds: the stored clauses paired with the
// attributes they name.
const conditions = (...clauses) => resolveConditions(clauses, attrs);

const matches = (t, ...clauses) =>
  matchesFilters(t, conditions(...clauses), "", config);

// "Asked" is one value standing for both asked states (state.js's
// ASKED_STATES): the menu offers it once, it selects both, and a
// condition kept from before then naming one of them still selects just
// that one.
describe("the state attribute's Asked", () => {
  const reply = task({ id: "2", state: "awaiting_reply" });
  const submit = task({ id: "3", state: "awaiting_submit" });
  const queued = task({ id: "4", state: "queued" });

  it("offers the two asked states as one Asked option", () => {
    const options = optionsFor(attr("state"), [reply, submit, queued], config);
    expect(options.map((o) => [o.value, o.label])).toEqual([
      ["queued", "Waiting"],
      ["asked", "Asked"],
    ]);
  });

  it("selects both underlying states, and nothing else", () => {
    const asked = { attr: "state", op: "any", values: ["asked"] };
    expect(matches(reply, asked)).toBe(true);
    expect(matches(submit, asked)).toBe(true);
    expect(matches(queued, asked)).toBe(false);
    const notAsked = { attr: "state", op: "none", values: ["asked"] };
    expect(matches(reply, notAsked)).toBe(false);
    expect(matches(queued, notAsked)).toBe(true);
  });

  it("keeps a saved condition on one old state key selecting just that state", () => {
    const old = { attr: "state", op: "any", values: ["awaiting_reply"] };
    expect(matches(reply, old)).toBe(true);
    expect(matches(submit, old)).toBe(false);
    // And its menu still shows what it picked, named apart from Asked.
    const options = optionsFor(attr("state"), [reply, submit], config, [
      "awaiting_reply",
    ]);
    expect(options.map((o) => o.label)).toEqual(["Asked", "Asked (reply)"]);
  });
});

describe("operatorsFor", () => {
  it("offers each kind of attribute the comparisons it can make", () => {
    expect(operatorsFor(attr("origin"))).toEqual(["any", "none"]);
    expect(operatorsFor(attr("capability"))).toEqual([
      "any",
      "none",
      "all",
      "empty",
      "filled",
    ]);
    expect(operatorsFor(attr("title"))).toEqual(["has", "hasnot"]);
    expect(operatorsFor(attr("created"))).toEqual([
      "within",
      "after",
      "before",
      "empty",
      "filled",
    ]);
    expect(operatorsFor(attr("started"))).toEqual([
      "within",
      "after",
      "before",
      "empty",
      "filled",
    ]);
    expect(operatorsFor(attr("completed"))).toEqual([
      "within",
      "after",
      "before",
      "empty",
      "filled",
    ]);
  });

  // "is empty" and "is set" are worth offering only where having no
  // value is a real answer: every task has an auto-merge setting, so
  // there they would be a comparison that never matches and one that
  // always does.
  it("offers 'is empty' only where an attribute can be absent", () => {
    expect(operatorsFor(attr("repo"))).toContain("empty");
    expect(operatorsFor(attr("autoMerge"))).not.toContain("empty");
  });

  it("starts a new condition on the first comparison, with no value", () => {
    expect(newCondition(attr("repo"))).toEqual({
      attr: "repo",
      op: "any",
      values: [],
    });
  });

  // Changing a row's attribute keeps the comparison where the new
  // attribute makes it too, and never keeps the values.
  it("keeps a condition answerable when its attribute changes", () => {
    const before = { attr: "repo", op: "none", values: ["acme/widgets"] };
    expect(conditionFor(attr("author"), before)).toEqual({
      attr: "author",
      op: "none",
      values: [],
    });
    expect(conditionFor(attr("title"), before)).toEqual({
      attr: "title",
      op: "has",
      values: [],
    });
  });
});

describe("matchesFilters", () => {
  it("matches any of the values named, and none of them", () => {
    const widgets = task({ repo: "acme/widgets" });
    const gadgets = task({ repo: "acme/gadgets" });
    const any = { attr: "repo", op: "any", values: ["acme/widgets"] };
    expect(matches(widgets, any)).toBe(true);
    expect(matches(gadgets, any)).toBe(false);
    const none = { attr: "repo", op: "none", values: ["acme/widgets"] };
    expect(matches(widgets, none)).toBe(false);
    expect(matches(gadgets, none)).toBe(true);
  });

  it("reads 'has none of these' as a value of its own", () => {
    const loose = task({});
    const targeted = task({ repo: "acme/widgets" });
    const clause = { attr: "repo", op: "any", values: [NONE] };
    expect(matches(loose, clause)).toBe(true);
    expect(matches(targeted, clause)).toBe(false);
  });

  it("tells 'any of' from 'all of' on a multi-valued attribute", () => {
    const both = task({ capabilities: ["gh", "gcp"] });
    const one = task({ capabilities: ["gcp"] });
    const values = ["gh", "gcp"];
    expect(matches(both, { attr: "capability", op: "any", values })).toBe(true);
    expect(matches(one, { attr: "capability", op: "any", values })).toBe(true);
    expect(matches(both, { attr: "capability", op: "all", values })).toBe(true);
    expect(matches(one, { attr: "capability", op: "all", values })).toBe(false);
  });

  // A write target is a repo and a branch (grain/task-54), and the
  // attribute is its repo: "may land work in acme/widgets-state" is the
  // question, and the branch is not part of it. Nor is it folded into
  // "repo", which stays the one repo this task's own runs push to.
  it("matches the other repos a task's work may land in, by their repo names", () => {
    const t = task({
      repo: "acme/widgets",
      writes: [{ repo: "acme/widgets-state", base: "deploy" }],
    });
    const wrote = (op, values) => ({ attr: "writes", op, values });
    expect(matches(t, wrote("any", ["acme/widgets-state"]))).toBe(true);
    expect(matches(t, wrote("none", ["acme/widgets-state"]))).toBe(false);
    expect(matches(t, wrote("empty", []))).toBe(false);
    expect(
      matches(t, { attr: "repo", op: "any", values: ["acme/widgets-state"] }),
    ).toBe(false);
    const own = task({ repo: "acme/widgets" });
    expect(matches(own, wrote("empty", []))).toBe(true);
    expect(matches(own, wrote("any", [NONE]))).toBe(true);
  });

  it("matches text by what is written on screen, not by the raw value", () => {
    // The capability's name, which is not its id -- so "Google" finds a
    // task nothing about whose data says "google".
    const t = task({ capabilities: ["gcp"] });
    expect(matches(t, { attr: "capability", op: "has", values: ["google"] }));
    expect(matches(t, { attr: "title", op: "has", values: ["FLAKE"] })).toBe(
      true,
    );
    expect(matches(t, { attr: "title", op: "hasnot", values: ["flake"] })).toBe(
      false,
    );
  });

  it("tells an attribute that is set from one that is not", () => {
    expect(matches(task({}), { attr: "repo", op: "empty", values: [] })).toBe(
      true,
    );
    expect(
      matches(task({ repo: "acme/widgets" }), {
        attr: "repo",
        op: "filled",
        values: [],
      }),
    ).toBe(true);
  });

  it("compares dates as the days somebody typed", () => {
    const t = task({ createdAt: "2026-02-10T12:00:00Z" });
    const after = (v) => ({ attr: "created", op: "after", values: [v] });
    const before = (v) => ({ attr: "created", op: "before", values: [v] });
    expect(matches(t, after("2026-02-01"))).toBe(true);
    expect(matches(t, after("2026-03-01"))).toBe(false);
    expect(matches(t, before("2026-03-01"))).toBe(true);
    expect(matches(t, before("2026-02-01"))).toBe(false);
    // The whole day named is inside "after" and outside "before", so the
    // two together are a range with no gap and no overlap.
    expect(matches(t, after("2026-02-10"))).toBe(true);
    expect(matches(t, before("2026-02-10"))).toBe(false);
  });

  // The day named starts when the rows say it does: every timestamp on
  // screen is printed in the zone the deployment keeps (time.js), so a
  // date typed into a filter is read in that zone too. 08:00 UTC is
  // still the 9th in Los Angeles.
  it("reads a date in the deployment's own time zone", () => {
    const t = task({ createdAt: "2026-02-10T04:00:00Z" });
    const clause = { attr: "created", op: "after", values: ["2026-02-10"] };
    const inZone = (timeZone) =>
      matchesFilters(
        t,
        conditions(clause),
        "",
        timeZone ? { ...config, timeZone } : config,
      );
    expect(inZone("UTC")).toBe(true);
    expect(inZone("America/Los_Angeles")).toBe(false);
    expect(inZone("Australia/Sydney")).toBe(true);
  });

  it("compares a date against how long ago it was", () => {
    const day = 24 * 60 * 60 * 1000;
    const recent = task({
      createdAt: new Date(Date.now() - day).toISOString(),
    });
    const old = task({
      createdAt: new Date(Date.now() - 40 * day).toISOString(),
    });
    const clause = { attr: "created", op: "within", values: ["7"] };
    expect(matches(recent, clause)).toBe(true);
    expect(matches(old, clause)).toBe(false);
  });

  // A row the editor has added but nobody has answered yet is a question
  // still being written, not a filter that matches nothing.
  it("narrows nothing while a condition is still waiting for its value", () => {
    expect(
      matches(task({ repo: "acme/widgets" }), {
        attr: "repo",
        op: "any",
        values: [],
      }),
    ).toBe(true);
  });

  it("takes every condition together, and the search box with them", () => {
    const t = task({ repo: "acme/widgets", author: "ada" });
    const clauses = conditions(
      { attr: "repo", op: "any", values: ["acme/widgets"] },
      { attr: "author", op: "any", values: ["ada"] },
    );
    expect(matchesFilters(t, clauses, "", config)).toBe(true);
    expect(matchesFilters(t, clauses, "flake", config)).toBe(true);
    expect(matchesFilters(t, clauses, "nothing", config)).toBe(false);
    expect(
      matchesFilters(
        task({ repo: "acme/widgets", author: "grace" }),
        clauses,
        "",
        config,
      ),
    ).toBe(false);
  });

  // A link from a newer build, or one naming a custom attribute this
  // deployment has dropped: the clause is not a question this build can
  // ask, so it is dropped rather than left to empty the list.
  it("drops a condition on an attribute it has never heard of", () => {
    const clauses = conditions({
      attr: "custom.team",
      op: "any",
      values: ["infra"],
    });
    expect(clauses).toEqual([]);
    expect(matchesFilters(task({}), clauses, "", config)).toBe(true);
  });

  it("keeps the stored position of the conditions it does keep", () => {
    expect(
      conditions(
        { attr: "custom.gone", op: "any", values: ["x"] },
        { attr: "repo", op: "any", values: ["acme/widgets"] },
      ).map((c) => c.index),
    ).toEqual([1]);
  });
});

describe("optionsFor", () => {
  const tasks = [
    task({ repo: "acme/widgets", capabilities: ["gcp"] }),
    task({ repo: "acme/gadgets", capabilities: ["gh", "gcp"] }),
    task({}),
  ];

  // The glyph is the figure the menu row carries for a value that names
  // one of the kinds grain keeps a list of (grain/task-38, task-47):
  // every repo is a repo; "No repo" names none.
  it("offers every value the tasks carry, and 'none' when some carry none", () => {
    expect(optionsFor(attr("repo"), tasks, config)).toEqual([
      { value: "acme/gadgets", label: "acme/gadgets", glyph: "repos" },
      { value: "acme/widgets", label: "acme/widgets", glyph: "repos" },
      { value: NONE, label: "No repo", glyph: null },
    ]);
  });

  // Every value of this menu is a repo too, so its rows wear the same
  // figure the repo menu's do -- and a task whose work lands nowhere
  // else is a real answer worth offering.
  it("offers a write target by its repo name alone", () => {
    const writing = [
      task({
        repo: "acme/widgets",
        writes: [{ repo: "acme/widgets-state", base: "deploy" }],
      }),
      task({ repo: "acme/gadgets" }),
    ];
    expect(optionsFor(attr("writes"), writing, config)).toEqual([
      {
        value: "acme/widgets-state",
        label: "acme/widgets-state",
        glyph: "repos",
      },
      { value: NONE, label: "No other writeable repos", glyph: null },
    ]);
  });

  it("labels a value the way the deployment does, and orders by that", () => {
    expect(optionsFor(attr("capability"), tasks, config)).toEqual([
      { value: "gh", label: "GitHub", glyph: null },
      { value: "gcp", label: "Google Cloud", glyph: null },
      { value: NONE, label: "No capabilities", glyph: null },
    ]);
  });

  it("keeps a fixed vocabulary in its own order, unknown values last", () => {
    expect(
      optionsFor(
        attr("state"),
        [
          task({ state: "running" }),
          task({ state: "queued" }),
          task({ state: "sideways" }),
        ],
        config,
      ).map((o) => o.value),
    ).toEqual(["queued", "running", "sideways"]);
  });

  // A value chosen before the sidebar moved, or one arriving in a link,
  // stays in its own menu: a condition that goes on filtering by a value
  // has to be able to show it and take it back off.
  it("keeps a picked value on the menu even when no task carries it", () => {
    expect(
      optionsFor(attr("repo"), tasks, config, ["acme/gone"]).map(
        (o) => o.value,
      ),
    ).toContain("acme/gone");
  });
});

describe("sortTasks", () => {
  const named = (id, extra) => task({ id: String(id), title: id, ...extra });

  it("leaves the backlog's own order alone, and hands the same array back", () => {
    const tasks = [named("b"), named("a")];
    expect(sortTasks(tasks, [], attrs, config)).toBe(tasks);
  });

  it("orders on one attribute, either way", () => {
    const tasks = [named("b"), named("c"), named("a")];
    const titles = (sort) =>
      sortTasks(tasks, sort, attrs, config).map((t) => t.title);
    expect(titles([{ attr: "title", dir: "asc" }])).toEqual(["a", "b", "c"]);
    expect(titles([{ attr: "title", dir: "desc" }])).toEqual(["c", "b", "a"]);
  });

  it("breaks the first key's ties with the next", () => {
    const tasks = [
      named("c", { repo: "acme/widgets" }),
      named("a", { repo: "acme/widgets" }),
      named("b", { repo: "acme/gadgets" }),
    ];
    expect(
      sortTasks(
        tasks,
        [
          { attr: "repo", dir: "asc" },
          { attr: "title", dir: "asc" },
        ],
        attrs,
        config,
      ).map((t) => t.title),
    ).toEqual(["b", "a", "c"]);
  });

  // Tasks with no value for the attribute go last whichever way the
  // order points: reversing "Repo (A–Z)" should not bury the list under
  // every task that has no repo.
  it("puts the tasks with no value last in both directions", () => {
    const tasks = [named("a"), named("b", { repo: "acme/widgets" })];
    const titles = (dir) =>
      sortTasks(tasks, [{ attr: "repo", dir }], attrs, config).map(
        (t) => t.title,
      );
    expect(titles("asc")).toEqual(["b", "a"]);
    expect(titles("desc")).toEqual(["b", "a"]);
  });

  it("orders a state by the sidebar's own order rather than by its label", () => {
    const tasks = [
      named("a", { state: "closed" }),
      named("b", { state: "proposed" }),
      named("c", { state: "running" }),
    ];
    expect(
      sortTasks(tasks, [{ attr: "state", dir: "asc" }], attrs, config).map(
        (t) => t.state,
      ),
    ).toEqual(["proposed", "running", "closed"]);
  });

  it("orders tasks by started and completed times", () => {
    const t1 = named("first", {
      startedAt: "2026-02-10T10:00:00Z",
      endedAt: "2026-02-10T11:00:00Z",
    });
    const t2 = named("second", {
      startedAt: "2026-02-10T12:00:00Z",
      endedAt: "2026-02-10T12:30:00Z",
    });
    const t3 = named("third", { startedAt: null, endedAt: null });

    expect(
      sortTasks(
        [t2, t1, t3],
        [{ attr: "started", dir: "asc" }],
        attrs,
        config,
      ).map((t) => t.title),
    ).toEqual(["first", "second", "third"]);

    expect(
      sortTasks(
        [t1, t2, t3],
        [{ attr: "completed", dir: "desc" }],
        attrs,
        config,
      ).map((t) => t.title),
    ).toEqual(["second", "first", "third"]);
  });

  // The Merged panel's own order (defaultSortFor): when the pull
  // request merged, read off whichever of the task's fields says so,
  // and a task that never merged goes last either way.
  it("orders tasks by when they merged", () => {
    const named = (title, extra) => task({ id: title, title, ...extra });
    const tasks = [
      named("by PR", { prMergedAt: "2026-09-01T00:00:00Z" }),
      named("unmerged", { state: "closed", closedAt: "2026-12-01T00:00:00Z" }),
      named("by event", {
        pullRequestEvents: [{ kind: "merged", at: "2026-10-01T00:00:00Z" }],
      }),
      named("by close", {
        state: "closed",
        closedReason: "merged",
        closedAt: "2026-11-01T00:00:00Z",
      }),
    ];
    const titles = (dir) =>
      sortTasks(tasks, [{ attr: "merged", dir }], attrs, config).map(
        (t) => t.title,
      );
    expect(titles("desc")).toEqual([
      "by close",
      "by event",
      "by PR",
      "unmerged",
    ]);
    expect(titles("asc")).toEqual([
      "by PR",
      "by event",
      "by close",
      "unmerged",
    ]);
  });

  it("orders task ids as the numbers they are", () => {
    const tasks = [named(10), named(9), named(100)];
    expect(
      sortTasks(tasks, [{ attr: "id", dir: "asc" }], attrs, config).map(
        (t) => t.id,
      ),
    ).toEqual(["9", "10", "100"]);
  });

  it("ignores a key naming an attribute it has never heard of", () => {
    const tasks = [named("b"), named("a")];
    expect(
      sortTasks(
        tasks,
        [{ attr: "custom.gone", dir: "asc" }],
        attrs,
        config,
      ).map((t) => t.title),
    ).toEqual(["b", "a"]);
  });

  it("preserves server default ordering when sort array is empty", () => {
    const closedNewest = named("newest closed", {
      state: "closed",
      endedAt: "2026-09-25T12:00:00Z",
    });
    const closedOldest = named("oldest closed", {
      state: "closed",
      endedAt: "2026-09-20T12:00:00Z",
    });
    const tasks = [closedNewest, closedOldest];
    expect(sortTasks(tasks, [], attrs, config)).toEqual(tasks);
  });
});

// The question every task view asks, and the memo under it: a dashboard
// is several views of one list at once, so what is worth pinning here is
// that asking the same thing of the same list twice costs one answer --
// and that an answer is never kept where keeping it could make it wrong.
describe("narrowTasks", () => {
  // A task whose title cannot be read without saying so. The sort reads
  // it and the search reads it; nothing else in a narrowing does. So
  // `reads` is how many times these tasks have actually been put
  // through one, which is the only way to tell an answer that was
  // computed from one that was remembered.
  let reads = 0;
  const counted = (title, extra) => {
    const t = task({ id: title, ...extra });
    Object.defineProperty(t, "title", {
      enumerable: true,
      get() {
        reads++;
        return title;
      },
    });
    return t;
  };
  const threeTasks = () => [
    counted("c", { state: "queued" }),
    counted("a", { state: "running" }),
    counted("b", { state: "queued" }),
  ];
  const byTitle = (extra) => ({
    search: "",
    sort: [{ attr: "title", dir: "asc" }],
    filters: [],
    ...extra,
  });

  beforeEach(() => {
    reads = 0;
  });

  it("resolves, sorts and matches -- the three steps every view took itself", () => {
    const tasks = threeTasks();
    expect(
      narrowTasks(
        tasks,
        byTitle({
          filters: [{ attr: "state", op: "any", values: ["queued"] }],
        }),
        config,
      ).map((t) => t.id),
    ).toEqual(["b", "c"]);
  });

  it("answers the same question of the same list once", () => {
    const tasks = threeTasks();
    const first = narrowTasks(tasks, byTitle(), config);
    const spent = reads;
    expect(spent).toBeGreaterThan(0);

    // The same list and the same question, asked by another panel: the
    // answer it gets is the one that was already worked out, down to
    // the array, and no task was looked at to produce it.
    expect(narrowTasks(tasks, byTitle(), config)).toBe(first);
    expect(reads).toBe(spent);
  });

  // The cost bound this is really about: N panels over one list must not
  // be N sorts of it. Two questions, same order -- which is the six-panel
  // dashboard, where the panels differ in what they ask but not in how
  // they are ordered -- and the second one pays for its filter and not
  // for the sort.
  it("shares one sorted list between questions asked in the same order", () => {
    const tasks = threeTasks();
    narrowTasks(
      tasks,
      byTitle({ filters: [{ attr: "state", op: "any", values: ["queued"] }] }),
      config,
    );
    const sorting = reads;
    expect(sorting).toBeGreaterThan(0);

    expect(
      narrowTasks(
        tasks,
        byTitle({
          filters: [{ attr: "state", op: "any", values: ["running"] }],
        }),
        config,
      ).map((t) => t.id),
    ).toEqual(["a"]);
    expect(reads).toBe(sorting);
  });

  // Identity is the whole soundness argument: an answer belongs to the
  // array it was computed from, so a list that has actually changed is a
  // different entry and gets a different answer. (Which is why App keeps
  // the array it has when a poll says the same thing -- poll.js -- and
  // why it is safe for it to do so.)
  it("answers a different list afresh", () => {
    const first = narrowTasks(threeTasks(), byTitle(), config);
    reads = 0;
    const second = narrowTasks(threeTasks(), byTitle(), config);
    expect(reads).toBeGreaterThan(0);
    expect(second).not.toBe(first);
    expect(second.map((t) => t.id)).toEqual(["a", "b", "c"]);
  });

  // A different question is a different answer, however alike the two
  // read: the key is the whole narrowing, not the sort it happens to
  // share.
  it("tells two questions apart", () => {
    const tasks = threeTasks();
    expect(
      narrowTasks(tasks, byTitle({ search: "a" }), config).map((t) => t.id),
    ).toEqual(["a"]);
    expect(narrowTasks(tasks, byTitle(), config).map((t) => t.id)).toEqual([
      "a",
      "b",
      "c",
    ]);
  });

  // config is the second half of what a narrowing means -- the custom
  // attributes that exist, the zone a date is read in -- so an answer
  // does not survive it changing.
  it("answers afresh when the config does", () => {
    const tasks = threeTasks();
    const first = narrowTasks(tasks, byTitle(), config);
    expect(narrowTasks(tasks, byTitle(), { ...config })).not.toBe(first);
  });

  // A list keeps its identity for as long as the deployment is quiet
  // (poll.js), so what is remembered about it has to be bounded: a
  // search box asks a new question per keystroke, and the oldest of them
  // goes rather than the cache growing for the life of the tab.
  it("keeps a bounded number of questions per list", () => {
    const tasks = threeTasks();
    const first = byTitle({ search: "q0" });
    narrowTasks(tasks, first, config);
    for (let i = 1; i <= 20; i++) {
      narrowTasks(tasks, byTitle({ search: `q${i}` }), config);
    }
    // The one asked twenty questions ago has been dropped and is
    // answered afresh; the most recent one is still there.
    reads = 0;
    narrowTasks(tasks, first, config);
    expect(reads).toBeGreaterThan(0);
    reads = 0;
    narrowTasks(tasks, byTitle({ search: "q20" }), config);
    expect(reads).toBe(0);
  });

  // The one question that cannot be remembered: "within the last day" is
  // answered against the clock, so an answer kept for it would go on
  // being true after it had stopped being.
  it("never keeps an answer the clock is part of", () => {
    const tasks = threeTasks();
    const within = byTitle({
      filters: [{ attr: "created", op: "within", values: ["1"] }],
    });
    const first = narrowTasks(tasks, within, config);
    reads = 0;
    const second = narrowTasks(tasks, within, config);
    expect(second).not.toBe(first);
    // The sort is still shared -- it is the clock the answer depends on,
    // not the order.
    expect(reads).toBe(0);
  });
});

describe("describing a narrowing", () => {
  it("writes a condition as the sentence it is", () => {
    expect(
      describeCondition(
        attr("repo"),
        { attr: "repo", op: "any", values: ["acme/widgets", NONE] },
        config,
      ),
    ).toBe("Repo is any of acme/widgets, No repo");
    expect(
      describeCondition(
        attr("capability"),
        { attr: "capability", op: "all", values: ["gcp"] },
        config,
      ),
    ).toBe("Capability has all of Google Cloud");
    expect(
      describeCondition(
        attr("created"),
        { attr: "created", op: "within", values: ["7"] },
        config,
      ),
    ).toBe("Created is within the last 7 days");
    expect(
      describeCondition(attr("repo"), {
        attr: "repo",
        op: "empty",
        values: [],
      }),
    ).toBe("Repo is empty");
  });

  it("says a condition is still waiting rather than reading as a filter", () => {
    expect(
      describeCondition(attr("repo"), { attr: "repo", op: "any", values: [] }),
    ).toBe("Repo is any of…");
  });

  it("writes a sort as the order it puts the list in", () => {
    expect(
      describeSort(
        [
          { attr: "state", dir: "asc" },
          { attr: "created", dir: "desc" },
        ],
        attrs,
      ),
    ).toBe("State First to last, then Created Newest first");
  });
});

// The condition a task view opens narrowed by, which is the whole of
// what "closed and deferred tasks are hidden by default" is now: an
// ordinary condition in the narrowing rather than a rule of the list's
// own, so it shows as a chip, edits like any other and travels in a
// link.
describe("defaultFilters", () => {
  it("leaves set-aside states out", () => {
    expect(defaultFilters(config)).toEqual([
      { attr: "state", op: "none", values: ["deferred", "closed"] },
    ]);
    // The same answer before the deployment's own config has arrived,
    // which is what a view renders with on its first paint.
    expect(defaultNarrowing(null)).toEqual({
      search: "",
      sort: [],
      filters: [{ attr: "state", op: "none", values: ["deferred", "closed"] }],
    });
  });

  it("leaves closed out of it where the deployment shows closed tasks", () => {
    expect(defaultFilters({ ...config, showClosedByDefault: true })).toEqual([
      { attr: "state", op: "none", values: ["deferred"] },
    ]);
  });

  // Interactive tasks were once a Kind task views opened leaving out.
  // They are never in a task view now (isSessionTask), so a deployment
  // whose stored default still carries that clause has it dropped
  // rather than shown as a chip on an attribute that no longer exists.
  it("drops the legacy interactive-kind clause from a stored default", () => {
    const legacy = "f=state:none:deferred|closed&f=kind:none:interactive";
    expect(defaultFilters({ ...config, defaultFilters: legacy })).toEqual([
      { attr: "state", op: "none", values: ["deferred", "closed"] },
    ]);
    expect(
      defaultFilters({
        ...config,
        defaultFilters: legacy,
        showClosedByDefault: true,
      }),
    ).toEqual([{ attr: "state", op: "none", values: ["deferred"] }]);
  });

  it("reads deployment defaults from the config repository query", () => {
    expect(
      defaultFilters({
        ...config,
        defaultFilters:
          "f=state:any:queued|in_progress&f=repo:none:acme/widgets",
      }),
    ).toEqual([
      { attr: "state", op: "any", values: ["queued", "in_progress"] },
      { attr: "repo", op: "none", values: ["acme/widgets"] },
    ]);
    expect(defaultFilters({ ...config, defaultFilters: "" })).toEqual([]);
  });

  // It reads as the sentence a reader has to be able to act on, since
  // the chip is now the only thing saying what the list is leaving out.
  it("reads as a sentence on the chip", () => {
    const [cond] = defaultFilters(config);
    expect(describeCondition(attr("state"), cond, config)).toBe(
      "State is none of Deferred, Closed",
    );
  });
});

// # What a link written before the default became a condition asks for
//
// Making that default an ordinary condition (grain/task-186) put it in
// the query string, and left every link written before it carrying a
// narrowing without one. Such a link still parses -- paths.js reads the
// spelling the old toolbar wrote, which paths.test.js's "reads the
// narrowing an older build wrote" pins -- but App.jsx resolves
// `askedNarrowing || defaultNarrowing(config)`, so a narrowing that
// parsed at all is the whole of what the list is narrowed by and
// nothing puts the condition back.
//
// The call, made here and in the README beside the rest of the filter
// vocabulary: an old link means what it says rather than what it used
// to mean, so it opens showing closed and deferred tasks. The
// alternative -- folding the default in whenever a narrowing names no
// state -- would make "this list, closed ones included" unsayable in a
// link, since that narrowing is spelt by *not* carrying the condition,
// and would leave one query string meaning two things depending on
// which build wrote it. What the reader gets instead is on screen: no
// State chip above the list, and Clear one press away.
//
// This is here rather than in paths.test.js because what is being
// pinned is the list, not the parse: the URL is the input, the tasks
// that survive it are the answer.
describe("a link written before the default was a condition", () => {
  const filed = (id, title, state, repo) => task({ id, title, state, repo });
  const backlog = [
    filed("1", "ci: cache the module dir", "queued", "acme/widgets"),
    filed("2", "ci: the nightly soak", "closed", "acme/widgets"),
    filed("3", "ci: a second runner", "deferred", "acme/widgets"),
    filed("4", "ci: the sandbox image", "queued", "acme/gadgets"),
    filed("5", "Fix the flake", "queued", "acme/widgets"),
  ];

  // The list one URL produces, resolved the way the views resolve it:
  // the narrowing the link asks for or the deployment's default
  // (App.jsx), then the search and the conditions over the tasks
  // (TaskList.jsx). The rail is left where it opens, on "All" -- a link
  // carries no rail state, so the narrowing is the whole question.
  const shownBy = (narrowing) => {
    const { search, filters } = narrowing || defaultNarrowing(config);
    const q = search.trim().toLowerCase();
    return backlog
      .filter((t) => matchesFilters(t, conditions(...filters), q, config))
      .map((t) => t.id);
  };
  const listFor = (search) => shownBy(parsePath("/", search).narrowing);

  // The behaviour change, stated: each of these used to hide the closed
  // and the deferred task, and now does not.
  it("shows the closed and deferred tasks it never asked to hide", () => {
    expect(listFor("?q=ci&repo=acme/widgets")).toEqual(["1", "2", "3"]);
    expect(listFor("?q=ci")).toEqual(["1", "2", "3", "4"]);
    expect(listFor("?repo=acme/widgets")).toEqual(["1", "2", "3", "5"]);
    // A sort is a question about order, not about which tasks there
    // are, but it is enough to make a query a narrowing -- so a link
    // that only names one is an old link too.
    expect(listFor("?sort=newest")).toEqual(["1", "2", "3", "4", "5"]);
  });

  // The default is not gone, only un-asked-for: it is what a link
  // narrowing nothing still opens on, and what this build writes into
  // any link it is left in.
  it("leaves the default in force for a link that narrows nothing", () => {
    expect(listFor("")).toEqual(["1", "4", "5"]);
    expect(
      listFor("?q=ci&f=repo:any:acme/widgets&f=state:none:deferred|closed"),
    ).toEqual(["1"]);
  });

  // Nobody is stuck on the wider list an old link opens: it counts as
  // narrowed, which is what puts Clear in the toolbar, and Clear is the
  // deployment's own default again.
  it("is one Clear away from the list it used to open", () => {
    const asked = parsePath("/", "?q=ci&repo=acme/widgets").narrowing;
    expect(isNarrowed(asked, defaultFilters(config))).toBe(true);
    expect(
      shownBy({ search: "", sort: [], filters: defaultFilters(config) }),
    ).toEqual(["1", "4", "5"]);
  });
});

// isNarrowed is "asks more than the view opened asking", which is what
// the Clear button appears on.
describe("isNarrowed", () => {
  it("is false for a view sitting on its own defaults", () => {
    expect(isNarrowed(defaultNarrowing(config), defaultFilters(config))).toBe(
      false,
    );
    // ...and for a board, whose defaults are nothing at all: a board
    // says what it is about in full, so an empty one really is every
    // task.
    expect(isNarrowed({ search: "", sort: [], filters: [] })).toBe(false);
  });

  it("is true once the default condition is edited or deleted", () => {
    expect(
      isNarrowed({ search: "", sort: [], filters: [] }, defaultFilters(config)),
    ).toBe(true);
    expect(
      isNarrowed(
        {
          search: "",
          sort: [],
          filters: [{ attr: "state", op: "none", values: ["closed"] }],
        },
        defaultFilters(config),
      ),
    ).toBe(true);
  });

  it("ignores the sort, which is not something a list is rescued from", () => {
    expect(
      isNarrowed(
        { search: "", sort: [{ attr: "created", dir: "desc" }], filters: [] },
        [],
      ),
    ).toBe(false);
  });
});

describe("attributesFor", () => {
  it("is the built-in vocabulary for a deployment that declares none", () => {
    expect(attributesFor(null)).toBe(ATTRIBUTES);
    expect(attributesFor({ customFields: [] })).toBe(ATTRIBUTES);
  });

  // A field the deployment declares (model.CustomField, served on
  // GET /api/config) is an attribute here: read off the task's own
  // `customFields` map, namespaced so it cannot shadow a built-in, and
  // filtered on like anything else.
  it("adds a deployment's own fields, namespaced", () => {
    const custom = attributesFor({
      ...config,
      customFields: [
        { name: "customer", type: "text" },
        { name: "repo", label: "Repo (theirs)", type: "text" },
      ],
    });
    const customer = attributeById(custom, `${CUSTOM_PREFIX}customer`);
    // No label was declared, so the name is what it is called.
    expect(customer.label).toBe("customer");
    expect(customer.values({ customFields: { customer: "acme" } })).toEqual([
      "acme",
    ]);
    expect(customer.values({})).toEqual([]);
    // The built-in "repo" is still the built-in "repo".
    expect(attributeById(custom, "repo").label).toBe("Repo");
    expect(attributeById(custom, `${CUSTOM_PREFIX}repo`).label).toBe(
      "Repo (theirs)",
    );
    expect(
      matchesFilters(
        { ...task({}), customFields: { customer: "acme" } },
        resolveConditions(
          [{ attr: `${CUSTOM_PREFIX}customer`, op: "has", values: ["acm"] }],
          custom,
        ),
        "",
        config,
      ),
    ).toBe(true);
  });

  // Every declared type reaches a comparison somebody would make of it:
  // an enum picks off its declared options in the declared order, a
  // boolean is an enum of two, and everything can be empty because no
  // field is required.
  it("gives each declared type the comparisons it takes", () => {
    const custom = attributesFor({
      ...config,
      customFields: [
        {
          name: "priority",
          label: "Priority",
          type: "enum",
          options: ["low", "medium", "high"],
        },
        { name: "escalated", label: "Escalated", type: "boolean" },
        { name: "due", label: "Due", type: "date" },
        { name: "points", label: "Points", type: "number" },
        { name: "notes", label: "Notes", type: "wildly-new" },
      ],
    });
    const of = (name) => attributeById(custom, `${CUSTOM_PREFIX}${name}`);

    // Every declared field ends with "legacy": it is the one kind of
    // attribute whose vocabulary a deployment can change under a value
    // a task is already carrying (grain/task-201).
    expect(operatorsFor(of("priority"))).toEqual([
      "any",
      "none",
      "empty",
      "filled",
      "legacy",
    ]);
    expect(operatorsFor(of("due"))).toEqual([
      "within",
      "after",
      "before",
      "empty",
      "filled",
      "legacy",
    ]);
    expect(operatorsFor(of("points"))).toEqual([
      "eq",
      "ne",
      "gt",
      "lt",
      "empty",
      "filled",
      "legacy",
    ]);
    // A type this build has never heard of is compared as the text it
    // arrived as rather than dropped out of the menus.
    expect(operatorsFor(of("notes"))).toEqual([
      "has",
      "hasnot",
      "empty",
      "filled",
      "legacy",
    ]);

    const tasks = [
      task({ id: "1", customFields: { priority: "high", escalated: "true" } }),
      task({ id: "2", customFields: { priority: "low" } }),
      task({ id: "3" }),
    ];
    // Declared order, not alphabetical, and "Not set" for the task that
    // says nothing.
    expect(
      optionsFor(of("priority"), tasks, config).map((o) => o.label),
    ).toEqual(["low", "high", "Not set"]);
    // A boolean reads as a question with a yes and a no, whatever the
    // store spells it.
    expect(
      optionsFor(of("escalated"), tasks, config).map((o) => o.label),
    ).toEqual(["Yes", "Not set"]);

    const kept = (clause) =>
      tasks
        .filter((t) =>
          matchesFilters(t, resolveConditions([clause], custom), "", config),
        )
        .map((t) => t.id);
    expect(
      kept({
        attr: `${CUSTOM_PREFIX}escalated`,
        op: "any",
        values: ["true"],
      }),
    ).toEqual(["1"]);
    expect(
      kept({ attr: `${CUSTOM_PREFIX}priority`, op: "empty", values: [] }),
    ).toEqual(["3"]);
  });

  // grain/task-201: the tasks a merged change to
  // tables/custom_field.json stranded. A value its declaration has moved
  // on from stays on the task until somebody edits it off
  // (model.NormalizeCustomFieldValuesFrom), and until this comparison
  // the only way to find one was to already know which value to ask
  // about -- the menus offer the values in view, so a retired option on
  // a long backlog was invisible.
  it("finds the tasks whose value the declaration has moved on from", () => {
    const custom = attributesFor({
      ...config,
      customFields: [
        {
          name: "priority",
          label: "Priority",
          type: "enum",
          // "critical" has been retired from the options.
          options: ["low", "high"],
        },
        // And `teams` has stopped holding a set.
        {
          name: "teams",
          label: "Teams",
          type: "enum",
          options: ["infra", "ui"],
        },
      ],
    });
    const of = (name) => attributeById(custom, `${CUSTOM_PREFIX}${name}`);
    const tasks = [
      task({ id: "1", customFields: { priority: "critical" } }),
      task({ id: "2", customFields: { priority: "high" } }),
      task({ id: "3" }),
      task({ id: "4", customFields: { teams: ["infra", "ui"] } }),
      task({ id: "5", customFields: { teams: "ui" } }),
    ];
    const kept = (clause) =>
      tasks
        .filter((t) =>
          matchesFilters(t, resolveConditions([clause], custom), "", config),
        )
        .map((t) => t.id);

    expect(
      kept({ attr: `${CUSTOM_PREFIX}priority`, op: "legacy", values: [] }),
    ).toEqual(["1"]);
    // The set is stranded whole, since every value in it is one the
    // declaration offers and there being two of them is what is not.
    expect(
      kept({ attr: `${CUSTOM_PREFIX}teams`, op: "legacy", values: [] }),
    ).toEqual(["4"]);
    // No built-in offers it, and a link that names one anyway narrows
    // nothing rather than emptying the list.
    expect(operatorsFor(of("priority"))).toContain("legacy");
    expect(operatorsFor(attributeById(custom, "state"))).not.toContain(
      "legacy",
    );
    expect(kept({ attr: "state", op: "legacy", values: [] })).toEqual([
      "1",
      "2",
      "3",
      "4",
      "5",
    ]);
  });

  // A field declared `multiple` (model.CustomField.Multiple) is asked
  // the questions a set takes -- "is any of", "is none of", "has all of"
  // -- whatever its own type is, and its values arrive as an array where
  // a task has several and as a plain string where it has one
  // (ui.CustomFieldValue).
  it("compares a multi-valued field as a set", () => {
    const custom = attributesFor({
      ...config,
      customFields: [
        {
          name: "areas",
          label: "Areas",
          type: "enum",
          options: ["ui", "daemon", "sandbox"],
          multiple: true,
        },
        { name: "components", label: "Components", multiple: true },
      ],
    });
    const of = (name) => attributeById(custom, `${CUSTOM_PREFIX}${name}`);

    // "legacy" last on both: a declared field is the one kind of
    // attribute whose vocabulary a deployment can change under a value
    // already stored (grain/task-201).
    expect(operatorsFor(of("areas"))).toEqual([
      "any",
      "none",
      "all",
      "empty",
      "filled",
      "legacy",
    ]);
    expect(operatorsFor(of("components"))).toEqual([
      "any",
      "none",
      "all",
      "empty",
      "filled",
      "legacy",
    ]);

    const tasks = [
      task({ id: "1", customFields: { areas: ["daemon", "ui"] } }),
      task({ id: "2", customFields: { areas: "sandbox" } }),
      task({ id: "3" }),
    ];
    // The menu still lists an enum's options in the order they were
    // declared, which multiple changes nothing about.
    expect(optionsFor(of("areas"), tasks, config).map((o) => o.label)).toEqual([
      "ui",
      "daemon",
      "sandbox",
      "Not set",
    ]);

    const kept = (clause) =>
      tasks
        .filter((t) =>
          matchesFilters(t, resolveConditions([clause], custom), "", config),
        )
        .map((t) => t.id);
    expect(
      kept({ attr: `${CUSTOM_PREFIX}areas`, op: "any", values: ["ui"] }),
    ).toEqual(["1"]);
    expect(
      kept({
        attr: `${CUSTOM_PREFIX}areas`,
        op: "all",
        values: ["ui", "daemon"],
      }),
    ).toEqual(["1"]);
    expect(
      kept({ attr: `${CUSTOM_PREFIX}areas`, op: "none", values: ["ui"] }),
    ).toEqual(["2", "3"]);
    expect(
      kept({ attr: `${CUSTOM_PREFIX}areas`, op: "empty", values: [] }),
    ).toEqual(["3"]);
  });

  // A date field holds a day (2026-03-04), not a timestamp, and the
  // editor's own value is a day too -- so the two are read in the same
  // zone. Read as UTC, "due on or after the 4th" would answer no to a
  // task due on the 4th anywhere west of UTC.
  it("compares a declared date as a day in the deployment's zone", () => {
    const custom = attributesFor({
      ...config,
      customFields: [{ name: "due", label: "Due", type: "date" }],
    });
    const west = { ...config, timeZone: "America/Los_Angeles" };
    const due = { ...task({}), customFields: { due: "2026-03-04" } };
    const matchesOn = (op, day) =>
      matchesFilters(
        due,
        resolveConditions(
          [{ attr: `${CUSTOM_PREFIX}due`, op, values: [day] }],
          custom,
        ),
        "",
        west,
      );
    expect(matchesOn("after", "2026-03-04")).toBe(true);
    expect(matchesOn("before", "2026-03-04")).toBe(false);
    expect(matchesOn("before", "2026-03-05")).toBe(true);
    // A value that is not a day at all -- a field retyped to `date`
    // after tasks had been filed against it -- matches neither
    // comparison, rather than falling on one side of every one.
    const whenever = { ...task({}), customFields: { due: "whenever" } };
    for (const op of ["after", "before"])
      expect(
        matchesFilters(
          whenever,
          resolveConditions(
            [{ attr: `${CUSTOM_PREFIX}due`, op, values: ["2026-03-04"] }],
            custom,
          ),
          "",
          west,
        ),
      ).toBe(false);
  });

  // What counts as having no value, which is what "is empty" and the
  // "Not set" row then find -- and, since no field is required, the
  // answer a custom attribute gives most often.
  it("reads anything but a string or a number as no value at all", () => {
    const custom = attributesFor({
      ...config,
      customFields: [{ name: "customer", label: "Customer", type: "text" }],
    });
    const of = attributeById(custom, `${CUSTOM_PREFIX}customer`);
    expect(of.values({ customFields: { customer: "acme" } })).toEqual(["acme"]);
    // The empty string a form submits for an input nobody touched is
    // "nobody said" rather than a value that happens to be blank.
    expect(of.values({ customFields: { customer: "" } })).toEqual([]);
    expect(of.values({ customFields: { customer: null } })).toEqual([]);
    expect(of.values({ customFields: {} })).toEqual([]);
    expect(of.values({})).toEqual([]);

    // And "Not set" is a value in its own right in the menu, not only a
    // comparison of its own: "is any of Not set" is how somebody asks
    // for the tasks nobody has filled the field in on.
    const asked = (t) =>
      matchesFilters(
        t,
        resolveConditions(
          [{ attr: `${CUSTOM_PREFIX}customer`, op: "any", values: [NONE] }],
          custom,
        ),
        "",
        config,
      );
    expect(asked(task({}))).toBe(true);
    expect(asked({ ...task({}), customFields: { customer: "acme" } })).toBe(
      false,
    );
  });

  // No built-in attribute is a number, so a declared one is the only way
  // to reach these comparisons at all -- they are worth stating rather
  // than inheriting from a built-in's coverage, because there is none.
  it("compares a declared number as a number", () => {
    const custom = attributesFor({
      ...config,
      customFields: [{ name: "points", label: "Points", type: "number" }],
    });
    const id = `${CUSTOM_PREFIX}points`;
    const kept = (op, bound, values) =>
      values.filter((v) =>
        matchesFilters(
          { ...task({}), customFields: { points: v } },
          resolveConditions([{ attr: id, op, values: [bound] }], custom),
          "",
          config,
        ),
      );
    const points = ["2", "9", "10"];
    // 9 is more than 10 as text and less than it as a number, which is
    // the whole of what "as a number" means here.
    expect(kept("gt", "9", points)).toEqual(["10"]);
    expect(kept("lt", "9", points)).toEqual(["2"]);
    expect(kept("eq", "9", points)).toEqual(["9"]);
    expect(kept("ne", "9", points)).toEqual(["2", "10"]);
    // A value that is not a number matches no bound rather than every
    // one, and "9.0" is 9.
    expect(kept("gt", "1", ["lots"])).toEqual([]);
    expect(kept("eq", "9", ["9.0"])).toEqual(["9.0"]);
  });

  // A boolean is written the way it is read: the chip says Yes, not the
  // "true" the store holds.
  it("writes a boolean condition in the words its menu offers", () => {
    const custom = attributesFor({
      ...config,
      customFields: [
        { name: "escalated", label: "Escalated", type: "boolean" },
      ],
    });
    const of = attributeById(custom, `${CUSTOM_PREFIX}escalated`);
    expect(
      describeCondition(
        of,
        { attr: of.id, op: "any", values: ["true"] },
        config,
      ),
    ).toBe("Escalated is any of Yes");
    expect(
      describeCondition(of, { attr: of.id, op: "empty", values: [] }, config),
    ).toBe("Escalated is empty");
  });

  // Sorting, which is the other half of what a declared field is for and
  // the half a filter cannot stand in for: "the backlog, most urgent
  // first" is an order, not a narrowing.
  describe("ordering a list by a declared field", () => {
    const custom = attributesFor({
      ...config,
      customFields: [
        {
          name: "priority",
          label: "Priority",
          type: "enum",
          options: ["low", "medium", "high"],
        },
        { name: "due", label: "Due", type: "date" },
        { name: "points", label: "Points", type: "number" },
        { name: "customer", label: "Customer", type: "text" },
      ],
    });
    const id = (name) => `${CUSTOM_PREFIX}${name}`;
    const of = (name) => attributeById(custom, id(name));
    const filed = (name, value) =>
      task({ id: value ?? "none", customFields: value && { [name]: value } });
    const order = (name, dir, values) =>
      sortTasks(
        values.map((v) => filed(name, v)),
        [{ attr: id(name), dir }],
        custom,
        config,
      ).map((t) => t.customFields?.[name]);

    // The whole of what an enum buys over the free text it would
    // otherwise be. Alphabetically these run high, low, medium, which is
    // an order about the words rather than about the work.
    it("ranks an enum by its declared options, not by their spelling", () => {
      const values = ["medium", "high", "low"];
      expect(order("priority", "asc", values)).toEqual([
        "low",
        "medium",
        "high",
      ]);
      expect(order("priority", "desc", values)).toEqual([
        "high",
        "medium",
        "low",
      ]);
    });

    // A value the field no longer declares -- an option dropped from
    // tables/custom_field.json with tasks still carrying it -- ranks
    // after every declared one rather than ahead of them, which is where
    // an unfound index would otherwise put it.
    it("ranks a value the field no longer declares after the ones it does", () => {
      expect(order("priority", "asc", ["urgent", "high", "low"])).toEqual([
        "low",
        "high",
        "urgent",
      ]);
    });

    // By the day, not by the text: read as text these are already in
    // order, so the ordering is asserted where the two readings differ.
    it("ranks a date by the day it names", () => {
      const days = ["2026-03-10", "2026-02-28", "2026-03-09"];
      expect(order("due", "asc", days)).toEqual([
        "2026-02-28",
        "2026-03-09",
        "2026-03-10",
      ]);
      expect(order("due", "desc", days)).toEqual([
        "2026-03-10",
        "2026-03-09",
        "2026-02-28",
      ]);
    });

    // Every date on screen is read in the deployment's own zone, and an
    // order has to agree with the filter above about which day a value
    // is on: the same three days come out in the same order east of UTC
    // and west of it.
    it("ranks the same days the same way in any zone", () => {
      const days = ["2026-03-10", "2026-02-28"];
      for (const timeZone of ["UTC", "America/Los_Angeles", "Pacific/Auckland"])
        expect(
          sortTasks(
            days.map((d) => filed("due", d)),
            [{ attr: id("due"), dir: "asc" }],
            custom,
            { ...config, timeZone },
          ).map((t) => t.customFields.due),
        ).toEqual(["2026-02-28", "2026-03-10"]);
    });

    // As the numbers they are: as text, 10 sorts between 1 and 2.
    it("ranks a number as a number", () => {
      expect(order("points", "asc", ["10", "9", "100"])).toEqual([
        "9",
        "10",
        "100",
      ]);
    });

    // The rule every built-in attribute follows: the tasks nobody filled
    // the field in on go last whichever way the order points, so
    // reversing an order does not bury the list under the tasks it says
    // nothing about. Worth stating for a custom field in particular,
    // where "nobody said" is the ordinary answer rather than the rare
    // one.
    it("puts the tasks with no value for it last in both directions", () => {
      for (const dir of ["asc", "desc"])
        expect(order("priority", dir, ["high", null, "low"]).at(-1)).toBe(
          undefined,
        );
    });

    // What the sort button reads once a field is picked: the words come
    // from the declared type, so a date offers newest and oldest, an
    // enum with an order offers first and last, and free text is A–Z.
    it("words the direction the way the field's type reads", () => {
      expect(directionLabels(of("due"))).toEqual({
        asc: "Oldest first",
        desc: "Newest first",
      });
      expect(directionLabels(of("points"))).toEqual({
        asc: "Lowest first",
        desc: "Highest first",
      });
      expect(directionLabels(of("priority")).asc).toBe("First to last");
      expect(directionLabels(of("customer")).asc).toBe("A–Z");
      expect(
        describeSort(
          [
            { attr: id("priority"), dir: "desc" },
            { attr: id("due"), dir: "asc" },
          ],
          custom,
        ),
      ).toBe("Priority Last to first, then Due Oldest first");
    });
  });
});

describe("originOf", () => {
  it("names the one thing that put a task in the list", () => {
    expect(originOf(task({}))).toBe("hand");
    expect(originOf(task({ generatedFrom: "7" }))).toBe("proposed");
    expect(originOf(task({ scheduled: true }))).toBe("schedule");
    expect(originOf(task({ suiteRun: true }))).toBe("suite");
    expect(originOf(task({ stacked: true, generatedFrom: "7" }))).toBe("fix");
    expect(originOf(task({ stacked: true, review: true }))).toBe("review");
  });
});

// A condition naming an attribute this deployment does not have is
// dropped rather than left to match nothing -- and, since grain/task-1421,
// named rather than dropped in silence: a board whose saved query
// misspelled an attribute used to show every task with no sign anything
// was wrong.
describe("unknown conditions", () => {
  const read = (query) => parsePath("/", `?${query}`).narrowing.filters;

  it("reads a clause whose attribute is unknown, so it can be named", () => {
    expect(read("f=nosuchattr:any:x")).toEqual([
      { attr: "nosuchattr", op: "any", values: ["x"] },
    ]);
  });

  it("drops it from the narrowing and names it, keeping its index", () => {
    const filters = read("f=repo:any:acme/widgets&f=nosuchattr:any:x");
    expect(resolveConditions(filters, attrs).map((c) => c.cond.attr)).toEqual([
      "repo",
    ]);
    expect(unknownConditions(filters, attrs)).toEqual([
      { cond: filters[1], index: 1 },
    ]);
  });

  it("names nothing when every attribute is known", () => {
    expect(unknownConditions(read("f=repo:any:acme/widgets"), attrs)).toEqual(
      [],
    );
    expect(describeUnknownConditions([])).toBe("");
  });

  it("says which attributes it could not read, once each", () => {
    const filters = read(
      "f=nosuchattr:any:x&f=nosuchattr:none:y&f=custom.gone:any:z",
    );
    expect(describeUnknownConditions(unknownConditions(filters, attrs))).toBe(
      "Unknown conditions: nosuchattr, custom.gone",
    );
    expect(
      describeUnknownConditions(unknownConditions(filters.slice(0, 1), attrs)),
    ).toBe("Unknown condition: nosuchattr");
  });

  // A comparison this build does not have is the same kind of stale
  // link, and was parsed away before anything could say so: the list
  // showed every task and the address lost the clause (grain/task-1531).
  it("keeps, drops and names a comparison it has never heard of", () => {
    const filters = read("f=repo:any:acme/widgets&f=state:bogus:running");
    expect(filters[1]).toEqual({
      attr: "state",
      op: "bogus",
      values: ["running"],
    });
    expect(resolveConditions(filters, attrs).map((c) => c.cond.attr)).toEqual([
      "repo",
    ]);
    expect(unknownConditions(filters, attrs)).toEqual([
      { cond: filters[1], index: 1, badOp: true },
    ]);
    expect(describeUnknownConditions(unknownConditions(filters, attrs))).toBe(
      "Unknown condition: state:bogus",
    );
    const tasks = [
      task({ id: "1", repo: "acme/widgets", state: "queued" }),
      task({ id: "2", repo: "acme/gadgets", state: "running" }),
    ];
    const narrowing = { search: "", sort: [], filters };
    expect(narrowTasks(tasks, narrowing, config).map((t) => t.id)).toEqual([
      "1",
    ]);
  });

  it("narrows by the conditions it can read and ignores the unknown one", () => {
    const tasks = [
      task({ id: "1", repo: "acme/widgets" }),
      task({ id: "2", repo: "acme/gadgets" }),
    ];
    const narrowing = {
      search: "",
      sort: [],
      filters: read("f=repo:any:acme/widgets&f=nosuchattr:any:x"),
    };
    expect(narrowTasks(tasks, narrowing, config).map((t) => t.id)).toEqual([
      "1",
    ]);
  });
});

// Filtering by person: the creator, the author, and "User" -- either of
// them (grain/task-2312).
describe("people filters", () => {
  it("matches User on the creator or a human author", () => {
    const filed = task({
      creator: "alice",
      author: "agent-run-7",
      authorKind: "agent",
    });
    const authored = task({ author: "bob", authorKind: "human" });
    const user = (...values) => ({ attr: "user", op: "any", values });
    expect(matches(filed, user("alice"))).toBe(true);
    expect(matches(filed, user("agent-run-7"))).toBe(false);
    expect(matches(authored, user("bob"))).toBe(true);
    expect(matches(authored, user("alice"))).toBe(false);
  });

  it("matches part of a name typed rather than picked", () => {
    expect(operatorsFor(attr("user"))).toEqual([
      "any",
      "none",
      "has",
      "hasnot",
    ]);
    const t = task({ creator: "Samantha" });
    expect(matches(t, { attr: "user", op: "has", values: ["sam"] })).toBe(true);
    expect(matches(t, { attr: "user", op: "hasnot", values: ["sam"] })).toBe(
      false,
    );
  });
});

describe("isSessionTask", () => {
  it("is a standing session's task or any other interactive task", () => {
    expect(isSessionTask({ consoleSession: "director" })).toBe(true);
    expect(isSessionTask({ interactive: true })).toBe(true);
    expect(isSessionTask({ id: "1" })).toBe(false);
  });

  it("leaves interactive tasks out of every narrowing", () => {
    const chat = {
      id: "3",
      title: "Chat",
      interactive: true,
      state: "running",
    };
    const plain = { id: "1", title: "Work", state: "running" };
    expect(narrowTasks([chat, plain], NO_NARROWING, null)).toEqual([plain]);
  });

  it("leaves standing sessions in when keepSessions is requested", () => {
    const sessionTask = {
      id: "12",
      title: "Director",
      consoleSession: "director",
      state: "awaiting_reply",
    };
    const plainTask = {
      id: "1",
      title: "Work",
      state: "running",
    };
    const list = [sessionTask, plainTask];
    expect(narrowTasks(list, NO_NARROWING, null)).toEqual([plainTask]);
    expect(
      narrowTasks(list, NO_NARROWING, null, { keepSessions: true }),
    ).toEqual([sessionTask, plainTask]);
  });
});

describe("defaultSortFor", () => {
  it("opens the Merged view newest merged first, and the rest in backlog order", () => {
    expect(defaultSortFor("merged")).toEqual([{ attr: "merged", dir: "desc" }]);
    for (const state of ["all", "blocked", "queued", "closed"])
      expect(defaultSortFor(state)).toEqual([]);
  });

  it("tells an order nobody chose from one somebody did", () => {
    expect(isDefaultSort([])).toBe(true);
    expect(isDefaultSort(undefined)).toBe(true);
    expect(isDefaultSort(defaultSortFor("merged"))).toBe(true);
    expect(isDefaultSort([{ attr: "merged", dir: "asc" }])).toBe(false);
    expect(isDefaultSort([{ attr: "created", dir: "desc" }])).toBe(false);
  });

  it("is no narrowing on its own", () => {
    expect(
      isNarrowed({ search: "", sort: defaultSortFor("merged"), filters: [] }),
    ).toBe(false);
  });

  it("filters on when a task merged", () => {
    const clause = { attr: "merged", op: "after", values: ["2026-10-01"] };
    const hit = (extra) =>
      matchesFilters(task(extra), conditions(clause), "", config);
    expect(hit({ prMergedAt: "2026-10-05T00:00:00Z" })).toBe(true);
    expect(hit({ prMergedAt: "2026-09-05T00:00:00Z" })).toBe(false);
    expect(hit({ closedAt: "2026-10-05T00:00:00Z" })).toBe(false);
  });
});
