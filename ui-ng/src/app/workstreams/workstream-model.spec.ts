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
import type { Task } from "../api/models";
import {
  blockingChain,
  buildTreeRows,
  layoutTree,
  nodeRelationships,
  thumbLayout,
} from "./workstream-layout";
import {
  deriveWorkstreams,
  filterWorkstreams,
  progressSummaryText,
} from "./workstream-model";

const NOW = Date.parse("2026-11-11T20:00:00Z");

function task(id: string, over: Partial<Task> = {}): Task {
  return {
    id,
    title: `Task ${id}`,
    state: "queued",
    repo: "sidthe/grain-scratch-repo",
    author: "alice",
    authorKind: "human",
    owner: "alice",
    createdAt: "2026-11-10T10:00:00Z",
    updatedAt: "2026-11-11T12:00:00Z",
    ...over,
  };
}

describe("workstream-model", () => {
  it("excludes standalone 1-task leaves unless explicitly started or selected", () => {
    const solo = task("10", { state: "running" });
    const started = task("20", { state: "running", title: "Started stream" });
    const selected = task("30", { state: "queued" });

    const d1 = deriveWorkstreams([solo, started, selected], {
      now: NOW,
      startedIds: new Set(["20"]),
      selectedKey: "task:30",
    });

    expect(d1.byKey.has("task:10")).toBe(false);
    expect(d1.taskToStream.has("10")).toBe(false);

    const s20 = d1.byKey.get("task:20");
    expect(s20).toBeDefined();
    expect(s20?.planning).toBe(true);
    expect(s20?.state).toBe("planning");
    expect(s20?.counts.total).toBe(0);

    expect(d1.byKey.has("task:30")).toBe(true);
  });

  it("splits direct children of a console/Director session into separate user workstreams", () => {
    const director = task("5", {
      title: "Director",
      consoleSession: "2923",
      interactive: true,
      author: "director",
      authorKind: "agent",
    });
    const branchA = task("50", {
      title: "Fix test A",
      state: "completed",
      generatedFrom: "5",
      origin: "task:5",
      creator: "alice",
    });
    const childA1 = task("51", {
      title: "Step A1",
      state: "running",
      generatedFrom: "50",
      origin: "task:5",
    });
    const branchB = task("60", {
      title: "Feature B",
      state: "completed",
      generatedFrom: "5",
      origin: "task:5",
      creator: "alice",
    });
    const childB1 = task("61", {
      title: "Step B1",
      state: "queued",
      generatedFrom: "60",
      origin: "task:5",
    });

    const d = deriveWorkstreams(
      [director, branchA, childA1, branchB, childB1],
      {
        now: NOW,
        viewer: "alice",
      },
    );

    expect(d.byKey.has("task:5")).toBe(false);
    const sA = d.byKey.get("director:2923:50");
    const sB = d.byKey.get("director:2923:60");
    expect(sA?.by.sub).toBe("director");
    expect(sA?.by.heuristic).toContain("Director");
    expect(sA?.by.isYou).toBe(true);
    expect(sA?.nodes.map((n) => n.id)).toEqual(["50", "51"]);
    expect(sB?.nodes.map((n) => n.id)).toEqual(["60", "61"]);
  });

  it("excludes task: roots from progress counts but counts all GitHub owner/repo#N tasks", () => {
    const root = task("100", { state: "completed" });
    const c1 = task("101", {
      state: "completed",
      prMergedAt: "2026-11-11T01:00:00Z",
      origin: "task:100",
      generatedFrom: "100",
    });
    const c2 = task("102", {
      state: "proposed",
      origin: "task:100",
      generatedFrom: "100",
    });

    const gh1 = task("201", {
      state: "completed",
      prMergedAt: "2026-11-11T02:00:00Z",
      origin: "sidthe/repo#42",
      imported: true,
    });
    const gh2 = task("202", {
      state: "proposed",
      origin: "sidthe/repo#42",
      imported: true,
    });

    const d = deriveWorkstreams([root, c1, c2, gh1, gh2], { now: NOW });
    const tStream = d.byKey.get("task:100")!;
    expect(tStream.counts).toMatchObject({
      done: 1,
      person: 1,
      proposed: 1,
      total: 2,
      percent: 50,
    });
    expect(tStream.state).toBe("person");
    expect(progressSummaryText(tStream)).toContain("1 proposed");

    const ghStream = d.byKey.get("sidthe/repo#42")!;
    expect(ghStream.counts).toMatchObject({
      done: 1,
      person: 1,
      proposed: 1,
      total: 2,
    });
    expect(ghStream.by.kind).toBe("system");
    expect(ghStream.by.sub).toBe("import");
  });

  it("folds stacked review/fix children unless waiting on a person", () => {
    const root = task("300", { state: "completed" });
    const impl = task("301", {
      state: "running",
      origin: "task:300",
      generatedFrom: "300",
    });
    const revDone = task("302", {
      state: "completed",
      stacked: true,
      review: true,
      origin: "task:300",
      generatedFrom: "301",
    });
    const revAsked = task("303", {
      state: "awaiting_reply",
      stacked: true,
      review: true,
      origin: "task:300",
      generatedFrom: "301",
    });

    const d = deriveWorkstreams([root, impl, revDone, revAsked], { now: NOW });
    const s = d.byKey.get("task:300")!;
    expect(s.nodes.map((n) => n.id)).toEqual(["300", "301", "303"]);
    expect(s.byId.get("301")?.foldedReviews.map((f) => f.id)).toEqual(["302"]);
    expect(d.taskToStream.get("302")?.key).toBe("task:300");
  });

  it("handles cycles in generatedFrom and dependsOn without hanging", () => {
    const a = task("401", {
      state: "queued",
      generatedFrom: "402",
      dependsOn: ["402"],
    });
    const b = task("402", {
      state: "queued",
      generatedFrom: "401",
      dependsOn: ["401"],
    });

    const d = deriveWorkstreams([a, b], { now: NOW });
    expect(d.streams.length).toBe(1);
    const s = d.streams[0];
    const layout = layoutTree(s, { selectedId: "401" });
    expect(layout.nodes.length).toBe(2);
    const chain = blockingChain(s, "401");
    expect(chain.map((r) => r.taskId)).toEqual(["402"]);
  });

  it("collapses recurring scheduled runs by author and normalized title stem", () => {
    const older = task("500", {
      title: "Dead code sweep · Nov 10",
      state: "completed",
      scheduled: true,
      author: "schedule",
      authorKind: "automation",
      updatedAt: "2026-11-10T03:00:00Z",
    });
    const newer = task("501", {
      title: "Dead code sweep · Nov 11",
      state: "completed",
      scheduled: true,
      author: "schedule",
      authorKind: "automation",
      updatedAt: "2026-11-11T03:00:00Z",
    });

    const d = deriveWorkstreams([older, newer], { now: NOW });
    expect(d.streams.length).toBe(1);
    const rep = d.streams[0];
    expect(rep.key).toBe("task:501");
    expect(rep.seriesCount).toBe(2);
    expect(rep.by.heuristic).toContain("2 runs");
    expect(d.taskToStream.get("500")?.key).toBe("task:501");
  });

  it("promotes system streams needing a person into People scope and respects 14-day Done window", () => {
    const userRoot = task("600", { state: "completed" });
    const userChild = task("601", {
      state: "running",
      origin: "task:600",
      generatedFrom: "600",
    });

    const sysPersonRoot = task("610", {
      state: "completed",
      origin: "sidthe/repo#1",
      imported: true,
    });
    const sysPersonChild = task("611", {
      state: "proposed",
      origin: "sidthe/repo#1",
      imported: true,
    });

    const sysActiveRoot = task("620", {
      state: "completed",
      suiteRun: true,
      author: "suite",
      authorKind: "automation",
    });
    const sysActiveChild = task("621", {
      state: "running",
      origin: "task:620",
      generatedFrom: "620",
    });

    const staleRoot = task("630", {
      state: "completed",
      createdAt: "2026-10-01T00:00:00Z",
      updatedAt: "2026-10-10T00:00:00Z",
      closedAt: "2026-10-10T00:00:00Z",
    });
    const staleChild = task("631", {
      state: "completed",
      origin: "task:630",
      generatedFrom: "630",
      createdAt: "2026-10-01T00:00:00Z",
      updatedAt: "2026-10-10T00:00:00Z",
      closedAt: "2026-10-10T00:00:00Z",
    });

    const d = deriveWorkstreams(
      [
        userRoot,
        userChild,
        sysPersonRoot,
        sysPersonChild,
        sysActiveRoot,
        sysActiveChild,
        staleRoot,
        staleChild,
      ],
      { now: NOW, viewer: "alice" },
    );

    const inPeople = filterWorkstreams(d.streams, {
      scope: "people",
      mine: false,
      sub: new Set(),
      state: "any",
      repo: "",
      sort: "attn",
      query: "",
    });
    expect(inPeople.visible.map((s) => s.key)).toEqual([
      "sidthe/repo#1",
      "task:600",
    ]);
    expect(inPeople.promotedCount).toBe(1);
    expect(inPeople.hiddenSystem.map((s) => s.key)).toEqual(["task:620"]);

    const inDone = filterWorkstreams(d.streams, {
      scope: "all",
      mine: false,
      sub: new Set(),
      state: "done",
      repo: "",
      sort: "attn",
      query: "",
    });
    expect(inDone.visible.map((s) => s.key)).toEqual(["task:630"]);
  });
});

describe("workstream-layout", () => {
  it("folds >8 leaves when >2 are done, caps >40 children, and unfolds on expandAll or selection", () => {
    const root = task("700", { state: "completed" });
    const kids: Task[] = [];
    for (let i = 1; i <= 45; i++) {
      kids.push(
        task(String(700 + i), {
          state: i <= 5 ? "completed" : "queued",
          origin: "task:700",
          generatedFrom: "700",
        }),
      );
    }

    const d = deriveWorkstreams([root, ...kids], { now: NOW });
    const s = d.byKey.get("task:700")!;

    const packed = layoutTree(s, { selectedId: "710" });
    const packNode = packed.nodes.find((n) => n.id === "pack:700");
    const moreNode = packed.nodes.find((n) => n.id === "more:700");
    expect(packNode?.packedCount).toBe(5);
    expect(moreNode?.packedCount).toBe(2);

    // Selecting one of the done leaves keeps it out of the pack pill.
    const selDone = layoutTree(s, { selectedId: "701" });
    expect(selDone.nodes.some((n) => n.id === "701")).toBe(true);
    expect(selDone.nodes.find((n) => n.id === "pack:700")?.packedCount).toBe(4);

    // expandAll renders every single node.
    const expanded = layoutTree(s, { expandAll: true });
    expect(expanded.nodes.length).toBe(46);

    // thumbLayout caps miniature nodes at 24.
    const thumb = thumbLayout(s);
    expect(thumb.nodes.length).toBe(24);
    expect(thumb.overflowCount).toBe(22);
  });

  it("tracks cross-stream portals, hideDone ancestor preservation, and blocking chains", () => {
    const r1 = task("800", { state: "completed" });
    const r1Child = task("801", {
      state: "running",
      origin: "task:800",
      generatedFrom: "800",
    });

    const r2 = task("900", { state: "completed" });
    const r2DoneParent = task("901", {
      state: "completed",
      origin: "task:900",
      generatedFrom: "900",
    });
    const r2DoneLeaf = task("902", {
      state: "completed",
      origin: "task:900",
      generatedFrom: "901",
    });
    const r2Blocked = task("903", {
      state: "queued",
      origin: "task:900",
      generatedFrom: "901",
      dependsOn: ["801"],
    });

    const d = deriveWorkstreams(
      [r1, r1Child, r2, r2DoneParent, r2DoneLeaf, r2Blocked],
      { now: NOW },
    );
    const allTasksById = new Map(
      d.streams.flatMap((st) => [...st.byId.entries()]),
    );
    const s2 = d.byKey.get("task:900")!;

    const treeRows = buildTreeRows(s2, { hideDone: true });
    expect(treeRows.hiddenDoneCount).toBe(1); // Only 902 is hidden; 901 stays as ancestor of 903.
    expect(treeRows.rows.map((r) => r.id)).toEqual(["900", "901", "903"]);

    const layout = layoutTree(s2, {
      selectedId: "903",
      hideDone: true,
      allTasksById,
      taskToStream: d.taskToStream,
    });
    expect(layout.portals.length).toBe(1);
    expect(layout.portals[0].targetTaskId).toBe("801");
    expect(layout.portals[0].streamKey).toBe("task:800");

    const rels = nodeRelationships(s2, "903", allTasksById, d.taskToStream);
    expect(rels.filedBy?.taskId).toBe("901");
    expect(rels.waitsOn.length).toBe(1);
    expect(rels.blockingChain[0]).toMatchObject({
      taskId: "801",
      crossStream: true,
      streamKey: "task:800",
    });
  });

  it("treats unmerged completed PRs and stalled merge queues as merging/held rather than done", () => {
    const root = task("1100", { state: "completed", title: "Release work" });
    const mergingPr = task("1101", {
      state: "completed",
      pullRequest: { url: "https://github.com/sidthe/grain/pull/101" },
      origin: "task:1100",
      generatedFrom: "1100",
    });
    const stalledPr = task("1102", {
      state: "completed",
      pullRequest: { url: "https://github.com/sidthe/grain/pull/102" },
      mergeQueueStalledAt: "2026-11-11T15:00:00Z",
      origin: "task:1100",
      generatedFrom: "1100",
    });
    const mergedPr = task("1103", {
      state: "completed",
      pullRequest: { url: "https://github.com/sidthe/grain/pull/103" },
      merged: true,
      prMergedAt: "2026-11-11T16:00:00Z",
      origin: "task:1100",
      generatedFrom: "1100",
    });

    const d = deriveWorkstreams([root, mergingPr, stalledPr, mergedPr], {
      now: NOW,
    });
    const s = d.byKey.get("task:1100")!;
    expect(s.byId.get("1101")?.category).toBe("run");
    expect(s.byId.get("1101")?.glyph).toBe("merging");
    expect(s.byId.get("1102")?.category).toBe("person");
    expect(s.byId.get("1102")?.glyph).toBe("held");
    expect(s.byId.get("1103")?.category).toBe("done");
    expect(s.byId.get("1103")?.glyph).toBe("merged");
    expect(s.counts).toMatchObject({
      done: 1,
      run: 1,
      person: 1,
      total: 3,
    });
    expect(s.state).toBe("person");
  });

  it("never folds failed, running, blocked, or queued review children into +N rev", () => {
    const root = task("1200", { state: "completed", title: "Review stream" });
    const parent = task("1201", {
      state: "running",
      origin: "task:1200",
      generatedFrom: "1200",
    });
    const revDone = task("1202", {
      state: "closed",
      review: true,
      origin: "task:1200",
      generatedFrom: "1201",
    });
    const revFailed = task("1203", {
      state: "failed",
      review: true,
      origin: "task:1200",
      generatedFrom: "1201",
    });
    const revRunning = task("1204", {
      state: "running",
      stacked: true,
      origin: "task:1200",
      generatedFrom: "1201",
    });

    const d = deriveWorkstreams(
      [root, parent, revDone, revFailed, revRunning],
      { now: NOW },
    );
    const s = d.byKey.get("task:1200")!;
    expect(s.byId.get("1201")?.foldedReviews.map((t) => t.id)).toEqual([
      "1202",
    ]);
    expect(s.byId.has("1203")).toBe(true);
    expect(s.byId.has("1204")).toBe(true);
  });

  it("promotes missing-parent single child without excluding it and avoids planning on 1-task failed streams", () => {
    const orphanChild = task("1301", {
      state: "running",
      origin: "task:1300",
    });
    const orphanGrandchild = task("1302", {
      state: "queued",
      origin: "task:1300",
      generatedFrom: "1301",
    });
    const failedSolo = task("1400", { state: "failed" });
    const doneSolo = task("1401", { state: "closed" });

    const d = deriveWorkstreams(
      [orphanChild, orphanGrandchild, failedSolo, doneSolo],
      {
        now: NOW,
        startedIds: new Set(["1400", "1401"]),
      },
    );
    const orphanStream = d.byKey.get("task:1300")!;
    expect(orphanStream.counts).toMatchObject({ run: 1, queued: 1, total: 2 });
    expect(orphanStream.planning).toBe(false);
    expect(orphanStream.state).toBe("active");

    const failedStream = d.byKey.get("task:1400")!;
    expect(failedStream.planning).toBe(false);
    expect(failedStream.state).toBe("blocked");

    const doneStream = d.byKey.get("task:1401")!;
    expect(doneStream.planning).toBe(false);
    expect(doneStream.state).toBe("done");
  });

  it("registers older collapsed schedule series keys in byKey and stacks depth-0 portals with positive coords", () => {
    const older = task("1500", {
      title: "Nightly check run 1",
      scheduled: true,
      author: "schedule",
      authorKind: "automation",
      state: "closed",
      updatedAt: "2026-11-10T01:00:00Z",
    });
    const newer = task("1501", {
      title: "Nightly check run 2",
      scheduled: true,
      author: "schedule",
      authorKind: "automation",
      state: "proposed",
      updatedAt: "2026-11-11T01:00:00Z",
      dependsOn: ["800", "900"],
    });
    const extA = task("800", { state: "running" });
    const extA2 = task("801", {
      state: "running",
      origin: "task:800",
      generatedFrom: "800",
    });
    const extB = task("900", { state: "running" });
    const extB2 = task("901", {
      state: "running",
      origin: "task:900",
      generatedFrom: "900",
      dependsOn: ["1501"],
    });

    const d = deriveWorkstreams([older, newer, extA, extA2, extB, extB2], {
      now: NOW,
    });
    expect(d.byKey.get("task:1500")?.rootId).toBe("1501");

    const allTasksById = new Map(
      d.streams.flatMap((st) => [...st.byId.entries()]),
    );
    const sched = d.byKey.get("task:1501")!;
    const layout = layoutTree(sched, {
      allTasksById,
      taskToStream: d.taskToStream,
    });
    expect(layout.portals.length).toBe(2);
    for (const p of layout.portals) {
      expect(p.x).toBeGreaterThanOrEqual(4);
      expect(p.w).toBeGreaterThanOrEqual(68);
    }
    expect(layout.portals[1].y).toBeGreaterThan(layout.portals[0].y);

    const rels = nodeRelationships(sched, "1501", allTasksById, d.taskToStream);
    expect(
      rels.waitedOnBy.some((r) => r.taskId === "901" && r.crossStream),
    ).toBe(true);
  });

  it("tracks BFS depth accurately in blockingChain beyond 8 direct blockers", () => {
    const root = task("1600", { state: "completed" });
    const blockers = Array.from({ length: 9 }, (_, i) =>
      task(String(1610 + i), {
        state: "running",
        origin: "task:1600",
        generatedFrom: "1600",
        ...(i === 0 ? { dependsOn: ["1699"] } : {}),
      }),
    );
    const hop2 = task("1699", {
      state: "failed",
      origin: "task:1600",
      generatedFrom: "1600",
    });
    const leaf = task("1650", {
      state: "queued",
      origin: "task:1600",
      generatedFrom: "1600",
      dependsOn: blockers.map((b) => b.id),
    });

    const d = deriveWorkstreams([root, ...blockers, hop2, leaf], { now: NOW });
    const s = d.byKey.get("task:1600")!;
    const chain = blockingChain(s, "1650");
    expect(chain.some((r) => r.taskId === "1699")).toBe(true);
  });
});
