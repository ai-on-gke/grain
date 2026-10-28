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
  DAY,
  DEFAULT_VIEW,
  GANTT_STORAGE_KEY,
  describeSpan,
  duration,
  groupIntoLanes,
  inWindow,
  laneOf,
  loadView,
  normalizeView,
  nowAt,
  placeOf,
  saveView,
  spanOf,
  startOfDay,
  stepFor,
  ticksFor,
  windowOf,
} from "./gantt.js";

const HOUR = 60 * 60 * 1000;
// A fixed "now" to measure everything against: noon UTC, so a test can
// say what a bar covers without racing the clock.
const NOW = Date.UTC(2026, 8, 6, 12, 0, 0);
const iso = (ms) => new Date(ms).toISOString();

const task = (over = {}) => ({
  id: "1",
  title: "Fix the thing",
  state: "completed",
  createdAt: iso(NOW - 4 * HOUR),
  ...over,
});

describe("spanOf", () => {
  // The ordinary bar: filed, dispatched an hour later, done an hour
  // after that. All three instants come back, and the bar is not still
  // going.
  it("runs from the filing to the ending, with the dispatch marked", () => {
    const span = spanOf(
      task({
        startedAt: iso(NOW - 3 * HOUR),
        endedAt: iso(NOW - 2 * HOUR),
      }),
      NOW,
    );
    expect(span.from).toBe(NOW - 4 * HOUR);
    expect(span.started).toBe(NOW - 3 * HOUR);
    expect(span.to).toBe(NOW - 2 * HOUR);
    expect(span.ongoing).toBe(false);
  });

  // An open task's bar ends at the current moment and says so, because
  // "finished exactly now" and "has not finished" are opposite facts
  // about a bar that reaches the right-hand edge.
  it("runs an unfinished task to now, and marks it ongoing", () => {
    const span = spanOf(
      task({ startedAt: iso(NOW - HOUR), state: "running" }),
      NOW,
    );
    expect(span.to).toBe(NOW);
    expect(span.ongoing).toBe(true);
  });

  // A task nobody has dispatched is all wait: it has a bar, and no mark
  // on it.
  it("has no dispatch mark for a task that has never run", () => {
    const span = spanOf(task({ state: "queued" }), NOW);
    expect(span.started).toBeNull();
    expect(span.ongoing).toBe(true);
  });

  // Nothing to draw is null, rather than a bar pinned to the epoch.
  it("is null for a task with no dates at all", () => {
    expect(spanOf({ id: "1", title: "x", state: "queued" }, NOW)).toBeNull();
  });

  // A clock disagreeing with itself upstream must not produce a negative
  // width: the ending is clamped to the filing rather than dropped.
  it("clamps an ending that precedes the filing", () => {
    const span = spanOf(
      task({ createdAt: iso(NOW), endedAt: iso(NOW - HOUR) }),
      NOW,
    );
    expect(span.to).toBe(span.from);
  });
});

describe("windowOf", () => {
  it("ends a fixed range at now and runs back by its own width", () => {
    expect(windowOf([], "24h", NOW)).toEqual({ from: NOW - DAY, to: NOW });
    expect(windowOf([], "7d", NOW)).toEqual({ from: NOW - 7 * DAY, to: NOW });
  });

  // "Everything" fits the tasks rather than the clock, so a deployment
  // being looked at for the first time gets a chart with bars in it.
  it("fits everything to the spans it is given", () => {
    const spans = [
      spanOf(
        task({ createdAt: iso(NOW - 40 * DAY), endedAt: iso(NOW - 39 * DAY) }),
        NOW,
      ),
      spanOf(
        task({ createdAt: iso(NOW - 5 * DAY), endedAt: iso(NOW - 4 * DAY) }),
        NOW,
      ),
    ];
    expect(windowOf(spans, "all", NOW)).toEqual({
      from: NOW - 40 * DAY,
      to: NOW - 4 * DAY,
    });
  });

  it("still has a width with nothing to show", () => {
    const empty = windowOf([], "all", NOW);
    expect(empty.to - empty.from).toBeGreaterThan(0);
  });
});

describe("inWindow", () => {
  // Overlap, not containment: the task that has been running since
  // Monday belongs on a chart of the last hour.
  it("keeps a bar that only overlaps the window", () => {
    const window = { from: NOW - HOUR, to: NOW };
    const long = spanOf(
      task({ createdAt: iso(NOW - 30 * DAY), state: "running" }),
      NOW,
    );
    expect(inWindow(long, window)).toBe(true);
    const old = spanOf(
      task({ createdAt: iso(NOW - 30 * DAY), endedAt: iso(NOW - 29 * DAY) }),
      NOW,
    );
    expect(inWindow(old, window)).toBe(false);
  });
});

describe("placeOf", () => {
  const window = { from: NOW - 4 * HOUR, to: NOW };

  it("places a bar as percentages of the window", () => {
    const span = spanOf(
      task({
        createdAt: iso(NOW - 4 * HOUR),
        startedAt: iso(NOW - 3 * HOUR),
        endedAt: iso(NOW - 2 * HOUR),
      }),
      NOW,
    );
    const place = placeOf(span, window);
    expect(place.left).toBeCloseTo(0);
    expect(place.width).toBeCloseTo(50);
    // The wait is a quarter of the window: one hour of the four.
    expect(place.wait).toBeCloseTo(25);
    expect(place.clippedStart).toBe(false);
    expect(place.clippedEnd).toBe(false);
  });

  // A bar wider than the window is cut at both edges and says so, so
  // "longer than this range" cannot read as "exactly this long".
  it("clips to the window and reports both cuts", () => {
    const span = spanOf(
      task({ createdAt: iso(NOW - 30 * DAY), state: "running" }),
      NOW + HOUR,
    );
    const place = placeOf(span, window);
    expect(place.left).toBeCloseTo(0);
    expect(place.width).toBeCloseTo(100);
    expect(place.clippedStart).toBe(true);
    expect(place.clippedEnd).toBe(true);
  });

  // A task that lived a few seconds is still worth finding on screen.
  it("gives a momentary task a visible width", () => {
    const at = NOW - HOUR;
    const span = spanOf(
      task({ createdAt: iso(at), endedAt: iso(at + 1000) }),
      NOW,
    );
    expect(placeOf(span, window).width).toBeGreaterThan(0);
  });
});

describe("nowAt", () => {
  it("is where the current moment falls, and null outside the window", () => {
    expect(nowAt({ from: NOW - DAY, to: NOW }, NOW)).toBeCloseTo(100);
    expect(nowAt({ from: NOW - DAY, to: NOW - HOUR }, NOW)).toBeNull();
  });
});

describe("the axis", () => {
  // The ladder: a day of chart gets hours, a month gets days, and
  // neither gets more labels than fit.
  it("picks a spacing that leaves a readable number of ticks", () => {
    expect(stepFor({ from: NOW - DAY, to: NOW })).toBe(3 * HOUR);
    expect(stepFor({ from: NOW - 7 * DAY, to: NOW })).toBe(DAY);
    expect(stepFor({ from: NOW - 30 * DAY, to: NOW })).toBe(7 * DAY);
  });

  // Ticks are anchored to midnight in the deployment's zone rather than
  // to whenever the page happened to load, so they land on times
  // somebody can name.
  it("anchors ticks to midnight in the given zone", () => {
    const window = { from: NOW - DAY, to: NOW };
    const ticks = ticksFor(window, "UTC", 6 * HOUR);
    expect(ticks.length).toBeGreaterThan(0);
    for (const tick of ticks) {
      expect((tick.at - startOfDay(tick.at, "UTC")) % (6 * HOUR)).toBe(0);
      expect(tick.left).toBeGreaterThanOrEqual(0);
      expect(tick.left).toBeLessThanOrEqual(100);
    }
    // A tick that falls on a day boundary is labelled with the date,
    // since an axis of eight times of day says nothing about which day.
    const midnight = ticks.find((t) => t.day);
    expect(midnight.label).toMatch(/\w+ \d+/);
    expect(ticks.find((t) => !t.day).label).toMatch(/\d\d:\d\d/);
  });

  // The zone is the deployment's, not the browser's: the same instant is
  // a day boundary in one zone and the middle of the afternoon in
  // another, and every other timestamp in the UI already reads this way.
  it("takes its day boundaries from the zone it is given", () => {
    const at = Date.UTC(2026, 8, 6, 4, 0, 0);
    expect(startOfDay(at, "UTC")).toBe(Date.UTC(2026, 8, 6));
    // Tokyo is nine hours ahead, so 04:00 UTC is already the afternoon
    // of the same day there -- and its midnight was 15:00 UTC the day
    // before.
    expect(startOfDay(at, "Asia/Tokyo")).toBe(Date.UTC(2026, 8, 5, 15, 0, 0));
  });
});

describe("lanes", () => {
  const tasks = [
    task({ id: "1", repo: "acme/widgets", state: "running", author: "bob" }),
    task({ id: "2", repo: "acme/gadgets", state: "queued", author: "alice" }),
    task({ id: "3", state: "queued", author: "alice" }),
  ];

  it("is one unnamed lane when there is no grouping", () => {
    const lanes = groupIntoLanes(tasks, "none");
    expect(lanes).toHaveLength(1);
    expect(lanes[0].tasks).toHaveLength(3);
  });

  // A task with no value for what is being grouped by gets a lane of its
  // own rather than falling off the chart, and that lane sorts last.
  it("gives the tasks with no value a lane of their own, last", () => {
    const lanes = groupIntoLanes(tasks, "repo");
    expect(lanes.map((l) => l.label)).toEqual([
      "acme/gadgets",
      "acme/widgets",
      "No repo",
    ]);
    expect(lanes[2].tasks.map((t) => t.id)).toEqual(["3"]);
  });

  // States are laid out in the order the rest of the UI puts them in,
  // not alphabetically: a chart whose lanes read queued, running,
  // completed is a life cycle, and one that reads completed, queued,
  // running is a word list.
  it("orders state lanes by the life cycle", () => {
    const lanes = groupIntoLanes(tasks, "state");
    expect(lanes.map((l) => l.label)).toEqual(["Waiting", "Running"]);
  });

  it("names the lane a task belongs to", () => {
    expect(laneOf(tasks[0], "author")).toEqual({ key: "bob", label: "bob" });
    expect(laneOf(tasks[2], "repo")).toEqual({ key: "", label: "No repo" });
    expect(laneOf(tasks[0], "none")).toEqual({ key: "", label: "" });
  });
});

describe("the stored view", () => {
  beforeEach(() => localStorage.clear());

  it("is the default until something else is chosen", () => {
    expect(loadView()).toEqual(DEFAULT_VIEW);
  });

  it("remembers a choice, and stores nothing for the default", () => {
    saveView({ range: "30d", lanes: "repo" });
    expect(loadView()).toEqual({ range: "30d", lanes: "repo" });
    saveView(DEFAULT_VIEW);
    expect(localStorage.getItem(GANTT_STORAGE_KEY)).toBeNull();
    expect(loadView()).toEqual(DEFAULT_VIEW);
  });

  // A browser can be holding a value older than the build reading it,
  // which must fall back one field at a time rather than throwing at
  // render: the lane this build still has survives the range it dropped.
  it("falls back field by field on a stored view it cannot read", () => {
    expect(normalizeView({ range: "3 fortnights", lanes: "repo" })).toEqual({
      range: DEFAULT_VIEW.range,
      lanes: "repo",
    });
    expect(normalizeView("not a view")).toEqual(DEFAULT_VIEW);
    localStorage.setItem(GANTT_STORAGE_KEY, "{not json");
    expect(loadView()).toEqual(DEFAULT_VIEW);
  });
});

describe("describeSpan", () => {
  it("says how long it waited and how long it ran", () => {
    const span = spanOf(
      task({
        createdAt: iso(NOW - 4 * HOUR),
        startedAt: iso(NOW - 3 * HOUR),
        endedAt: iso(NOW - HOUR),
      }),
      NOW,
    );
    expect(describeSpan(span, NOW)).toBe("waited 1h, ran for 2h");
  });

  it("counts an unfinished run against the current moment", () => {
    const span = spanOf(
      task({
        createdAt: iso(NOW - 2 * HOUR),
        startedAt: iso(NOW - HOUR),
        state: "running",
      }),
      NOW,
    );
    expect(describeSpan(span, NOW)).toBe("waited 1h, running for 1h");
  });

  it("says plainly when a task has never been dispatched", () => {
    const span = spanOf(
      task({ createdAt: iso(NOW - DAY), state: "queued" }),
      NOW,
    );
    expect(describeSpan(span, NOW)).toBe("waiting for 1d, never dispatched");
  });
});

describe("duration", () => {
  it("gets coarser the longer it gets", () => {
    expect(duration(45 * 1000)).toBe("45s");
    expect(duration(90 * 1000)).toBe("1m");
    expect(duration(2 * HOUR + 30 * 60 * 1000)).toBe("2h 30m");
    expect(duration(3 * DAY)).toBe("3d");
    expect(duration(-5)).toBe("0s");
  });
});
