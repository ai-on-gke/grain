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

import { describe, expect, it } from "vitest";
import { keepIfSame, samePayload } from "./poll.js";

// What a poll does with an answer that has not changed (poll.js). The
// comparison decides whether a whole tab re-renders, so what is worth
// pinning is both halves of it: that it says yes to two parses of the
// same response, and that it says no to every difference a task list can
// actually carry.

describe("samePayload", () => {
  it("says yes to two parses of one response", () => {
    const one = { id: "1", title: "Fix bug", capabilities: [], links: null };
    expect(samePayload(one, { ...one, capabilities: [] })).toBe(true);
    expect(samePayload([one], [{ ...one, capabilities: [] }])).toBe(true);
  });

  it("says yes to the very same value", () => {
    const list = [{ id: "1" }];
    expect(samePayload(list, list)).toBe(true);
    expect(samePayload("a", "a")).toBe(true);
    expect(samePayload(null, null)).toBe(true);
  });

  // The differences a poll is actually there to notice.
  it("says no to a field that moved", () => {
    expect(samePayload({ state: "queued" }, { state: "running" })).toBe(false);
    expect(samePayload({ blocked: false }, { blocked: true })).toBe(false);
    expect(samePayload([{ id: "1" }], [{ id: "1" }, { id: "2" }])).toBe(false);
    expect(
      samePayload([{ id: "1" }, { id: "2" }], [{ id: "2" }, { id: "1" }]),
    ).toBe(false);
    expect(samePayload({ a: 1 }, { a: 1, b: 2 })).toBe(false);
    expect(samePayload({ a: 1, b: 2 }, { a: 1 })).toBe(false);
  });

  // A field a newer daemon sends as null and an older one leaves out are
  // not the same answer, and neither are the two spellings of nothing:
  // something downstream reads one of them and not the other.
  it("tells absent from empty", () => {
    expect(samePayload({ capabilities: null }, { capabilities: [] })).toBe(
      false,
    );
    expect(samePayload({ capabilities: null }, {})).toBe(false);
    expect(samePayload({ n: 0 }, { n: "0" })).toBe(false);
    expect(samePayload({ n: 0 }, { n: false })).toBe(false);
  });

  it("compares all the way down", () => {
    const run = (note) => ({ runs: [{ id: 1, activity: { note } }] });
    expect(samePayload(run("waiting for CI"), run("waiting for CI"))).toBe(
      true,
    );
    expect(samePayload(run("waiting for CI"), run("pushing"))).toBe(false);
  });

  it("does not mistake an array for the object with the same keys", () => {
    expect(samePayload(["a"], { 0: "a" })).toBe(false);
    expect(samePayload({ 0: "a" }, ["a"])).toBe(false);
  });
});

describe("keepIfSame", () => {
  // The whole point: the identity already in state survives a poll that
  // said nothing new, so React bails out of the re-render and every memo
  // keyed on it holds (taskFilters.js's narrowTasks).
  it("keeps the value already held when the new one says the same", () => {
    const held = [{ id: "1", title: "Fix bug" }];
    expect(keepIfSame(held, [{ id: "1", title: "Fix bug" }])).toBe(held);
  });

  it("takes the new one when anything has changed", () => {
    const held = [{ id: "1", state: "queued" }];
    const next = [{ id: "1", state: "running" }];
    expect(keepIfSame(held, next)).toBe(next);
  });
});
