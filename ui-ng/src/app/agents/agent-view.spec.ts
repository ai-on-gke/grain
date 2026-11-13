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
import type { Attempt, Task, TaskDetail } from "../api/models";
import {
  type Agent,
  QUIET_MS,
  cpuLevel,
  cpuNote,
  cpuOf,
  groupAgents,
  minutes,
  quietReason,
  running,
  sandboxFor,
  sortAgents,
  spark,
  toAgent,
  hostWork,
} from "./agent-view";

const NOW = Date.parse("2026-11-11T12:00:00Z");
const iso = (msAgo: number) => new Date(NOW - msAgo).toISOString();

function task(id: string, extra: Partial<Task> = {}): Task {
  return { id, title: `T${id}`, state: "running", ...extra } as Task;
}

function detail(attempts: Partial<Attempt>[]): TaskDetail {
  return { attempts } as unknown as TaskDetail;
}

describe("agent-view", () => {
  it("shows running tasks, interactive ones too, but not console sessions", () => {
    const list = running([
      task("1"),
      task("2", { state: "queued" } as Partial<Task>),
      task("3", { consoleSession: true } as Partial<Task>),
      task("4", { interactive: true } as Partial<Task>),
    ]);
    expect(list.map((t) => t.id)).toEqual(["1", "4"]);
  });

  it("marks a run quiet after ten minutes without an update", () => {
    const fresh = toAgent(
      task("1", { activityAt: iso(QUIET_MS - 1000) }),
      null,
      [],
      NOW,
    );
    const stale = toAgent(
      task("2", { activityAt: iso(QUIET_MS + 1000) }),
      null,
      [],
      NOW,
    );
    expect(fresh.status).toBe("working");
    expect(stale.status).toBe("quiet");
  });

  it("says setting up when grain's own setup reported last", () => {
    const a = toAgent(
      task("1", {
        activityAt: iso(1000),
        activityBySetup: true,
      } as Partial<Task>),
      null,
      [],
      NOW,
    );
    expect(a.status).toBe("setup");
  });

  it("falls back to the attempt start when there is no activity yet", () => {
    const a = toAgent(
      task("1"),
      detail([{ number: 1, startedAt: iso(QUIET_MS * 2) }]),
      [],
      NOW,
    );
    expect(a.status).toBe("quiet");
  });

  it("falls back to the task's last update when nothing else is known", () => {
    const a = toAgent(
      task("1", { updatedAt: iso(QUIET_MS * 2) } as Partial<Task>),
      null,
      [],
      NOW,
    );
    expect(a.status).toBe("quiet");
  });

  it("uses the latest attempt", () => {
    const a = toAgent(
      task("1", { activityAt: iso(0) }),
      detail([
        { number: 1, model: "old" },
        { number: 3, model: "new" },
        { number: 2, model: "mid" },
      ]),
      [],
      NOW,
    );
    expect(a.attempt?.model).toBe("new");
  });

  it("sorts quiet first, longest quiet first, then by start", () => {
    const mk = (id: string, act: number, start: number) =>
      toAgent(
        task(id, { activityAt: iso(act) }),
        detail([{ number: 1, startedAt: iso(start) }]),
        [],
        NOW,
      );
    const list = sortAgents([
      mk("a", 0, 1000),
      mk("b", QUIET_MS + 5, 0),
      mk("c", 0, 5000),
      mk("d", QUIET_MS * 3, 0),
    ]);
    expect(list.map((a) => a.task.id)).toEqual(["d", "b", "c", "a"]);
  });

  it("groups by repository, model and host work", () => {
    const a = toAgent(
      task("1", { repo: "o/b" }),
      detail([{ number: 1, model: "m2" }]),
      [],
      NOW,
    );
    const b = toAgent(task("2", { repo: "o/a" }), null, [], NOW);
    const c = toAgent(
      task("3"),
      detail([{ number: 1, hostd: { hostd: "laptop", deleted: true } }]),
      [],
      NOW,
    );
    expect(groupAgents([a, b], "none")).toEqual([{ key: "", agents: [a, b] }]);
    expect(groupAgents([a, b], "repo").map((g) => g.key)).toEqual([
      "o/a",
      "o/b",
    ]);
    expect(groupAgents([a, b], "model").map((g) => g.key)).toEqual([
      "Deployment default",
      "m2",
    ]);
    expect(groupAgents([a, c], "host").map((g) => g.key)).toEqual([
      "Host work on laptop (deleted)",
      "No host work",
    ]);
    expect(hostWork(a)).toBe("");
  });

  it("reads CPU as load over vCPUs and flags CPU-bound", () => {
    expect(cpuOf(undefined)).toBeNull();
    expect(cpuOf({ name: "x", cpus: 4 })).toBeNull();
    expect(cpuOf({ name: "x", cpus: 4, loadAverage: "nope" })).toBeNull();
    const c = cpuOf({ name: "x", cpus: 4, loadAverage: "2.00 1.50 1.00" })!;
    expect(c.share).toBe(0.5);
    expect(c.bound).toBe(false);
    expect(cpuOf({ name: "x", cpus: 2, loadAverage: "1.9, 1, 1" })!.bound).toBe(
      true,
    );
    expect(cpuLevel({ ...c, share: 0.3 })).toBe("low");
    expect(cpuLevel({ ...c, share: 0.7 })).toBe("mid");
    expect(cpuLevel({ ...c, share: 0.95 })).toBe("high");
  });

  it("matches a sandbox to its task by id, not by prefix", () => {
    const rs = [
      { name: "grain-task-12" },
      { name: "grain-task-1" },
      { name: "task7-x" },
    ];
    expect(sandboxFor(task("1"), rs)?.name).toBe("grain-task-1");
    expect(sandboxFor(task("12"), rs)?.name).toBe("grain-task-12");
    expect(sandboxFor(task("7"), rs)?.name).toBe("task7-x");
    expect(sandboxFor(task("2"), rs)).toBeUndefined();
  });

  it("skips CI and dead sandboxes for the same task", () => {
    const rs = [
      { name: "grain-ci-task-5", kind: "ci" },
      { name: "grain-task-5-old", terminationReason: "gone" },
      { name: "grain-task-5-x", ready: false },
      { name: "grain-task-5", kind: "task", terminationReason: "running" },
    ];
    expect(sandboxFor(task("5"), rs)?.name).toBe("grain-task-5");
  });

  it("explains a quiet run from its CPU", () => {
    const base = { quietMs: 12 * 60000, cpu: null } as Agent;
    expect(quietReason(base)).toEqual({
      text: "No update for 12m",
      stuck: false,
    });
    const busy = quietReason({
      ...base,
      cpu: { share: 0.8, load: 3.2, cpus: 4, bound: false, source: "probe" },
    });
    expect(busy.text).toContain("probably building or testing");
    expect(busy.stuck).toBe(false);
    const idle = quietReason({
      ...base,
      cpu: { share: 0.1, load: 0.4, cpus: 4, bound: false, source: "probe" },
    });
    expect(idle.text).toContain("likely stuck");
    expect(idle.stuck).toBe(true);
  });

  it("buckets activity into a 30-minute sparkline, oldest first", () => {
    const s = spark(
      [NOW - 1000, NOW - 2000, NOW - 29 * 60000, NOW - 31 * 60000, NOW + 5],
      NOW,
    );
    expect(s).toHaveLength(12);
    expect(s[11]).toBe(2);
    expect(s[0]).toBe(1);
    expect(s.reduce((a, b) => a + b, 0)).toBe(3);
  });

  it("formats minutes and hours", () => {
    expect(minutes(-5)).toBe("0m");
    expect(minutes(59 * 60000)).toBe("59m");
    expect(minutes(125 * 60000)).toBe("2h 5m");
  });
});

describe("agent-view CPU from run ids and host processes", () => {
  const row = (sandbox: string, extra: Record<string, unknown> = {}) => ({
    sandbox,
    name: `/data/runs/${sandbox}`,
    kind: "agent",
    backend: "host",
    ready: true,
    terminationReason: "running",
    ...extra,
  });

  it("joins a sandbox row to its run by run id", () => {
    const rs = [row("1-1"), row("1-3"), row("12-1"), row("1-2")];
    // The current attempt's own row.
    expect(sandboxFor(task("1"), rs, { number: 2 } as Attempt)?.sandbox).toBe(
      "1-2",
    );
    // Attempt unknown: the newest live attempt, never task 12's.
    expect(sandboxFor(task("1"), rs)?.sandbox).toBe("1-3");
    expect(sandboxFor(task("12"), rs)?.sandbox).toBe("12-1");
    expect(sandboxFor(task("2"), rs)).toBeUndefined();
    // CI and dead rows are skipped here too.
    expect(
      sandboxFor(task("4"), [
        row("4-1", { kind: "ci", name: "c4-1" }),
        row("4-2", { terminationReason: "gone" }),
      ]),
    ).toBeUndefined();
  });

  it("reads a host run's busy cores against the host's vCPUs", () => {
    const c = cpuOf(row("1-1", { cpuCores: 3, cpuWindowMS: 15000 }), 24)!;
    expect(c).toEqual(
      expect.objectContaining({
        source: "process",
        load: 3,
        cpus: 24,
        share: 0.125,
        bound: false,
        windowMs: 15000,
      }),
    );
    // An idle run's 0 is a reading, not an absence.
    expect(cpuOf(row("1-1", { cpuCores: 0 }), 24)!.share).toBe(0);
    // Without a denominator there is no share to draw.
    expect(cpuOf(row("1-1", { cpuCores: 1 }))).toBeNull();
    // A probed sandbox keeps its own load average reading.
    expect(
      cpuOf({ name: "vm", cpus: 4, loadAverage: "2 1 1" }, 24)!.source,
    ).toBe("probe");
  });

  it("toAgent carries the reading or the reason there is none", () => {
    const d = detail([{ number: 1, startedAt: iso(60000) }]);
    const a = toAgent(task("1"), d, [row("1-1", { cpuCores: 1.2 })], NOW, 8);
    expect(a.cpu?.load).toBe(1.2);
    expect(a.cpuNote).toBe("");
    const b = toAgent(task("1"), d, [row("1-1")], NOW, 8);
    expect(b.cpu).toBeNull();
    expect(b.cpuNote).toContain("none of this run's processes were found");
  });

  it("names why there is no reading by backend", () => {
    expect(cpuNote(undefined)).toContain("reported no sandbox for this run");
    expect(cpuNote(row("1-1"))).toContain("no /proc");
    expect(
      cpuNote({ name: "vm-1", backend: "granule", sandbox: "1-1" }),
    ).toContain("The granule sandbox did not report its load");
  });

  it("treats half a core of a host run as busy when quiet", () => {
    const base = { quietMs: 12 * 60000, cpu: null } as Agent;
    const busy = quietReason({
      ...base,
      cpu: {
        share: 0.03,
        load: 0.7,
        cpus: 24,
        bound: false,
        source: "process",
      },
    });
    expect(busy.stuck).toBe(false);
    expect(busy.text).toContain("probably building");
    const idle = quietReason({
      ...base,
      cpu: {
        share: 0.004,
        load: 0.1,
        cpus: 24,
        bound: false,
        source: "process",
      },
    });
    expect(idle.stuck).toBe(true);
  });
});
