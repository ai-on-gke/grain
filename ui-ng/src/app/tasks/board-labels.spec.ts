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

import type { Board } from "../api/models";
import {
  boardLabels,
  canonicalNextBoardQuery,
  classicBoardHint,
  nextBoardParams,
} from "./board-labels";

const b = (id: string, name: string, folder?: string): Board => ({
  id,
  name,
  kind: "list",
  ...(folder ? { folder } : {}),
});

describe("boardLabels", () => {
  it("leaves a unique name as written", () => {
    const got = boardLabels([b("1", "Board"), b("2", "Ops queue", "Ops")]);
    expect(got.get("1")).toBe("Board");
    expect(got.get("2")).toBe("Ops queue");
  });

  it("names the folder of each board sharing a name", () => {
    const got = boardLabels([
      b("1", "Triage"),
      b("2", "Triage", "Ops"),
      b("3", "Triage", "Zeta"),
    ]);
    expect([...got.values()]).toEqual([
      "Triage, outside every folder",
      "Triage, in Ops",
      "Triage, in Zeta",
    ]);
  });

  it("falls back to the id for two of one name in one folder", () => {
    const got = boardLabels([b("7", "Triage", "Ops"), b("9", "Triage", "Ops")]);
    expect(got.get("7")).toBe("Triage (7), in Ops");
    expect(got.get("9")).toBe("Triage (9), in Ops");
  });
});

describe("nextBoardParams", () => {
  const board = (kind: string, query?: string): Board => ({
    id: "1",
    name: "B",
    kind,
    ...(query === undefined ? {} : { query }),
  });

  it("applies a list board saved from /next/tasks here", () => {
    expect(
      nextBoardParams(
        board(
          "list",
          "tab=asked&group=repo&q=retry&repo=acme%2Fw&view=timeline",
        ),
      ),
    ).toEqual({
      tab: "asked",
      group: "repo",
      q: "retry",
      repo: "acme/w",
      view: "timeline",
    });
    expect(nextBoardParams(board("list"))).toEqual({
      tab: null,
      group: null,
      q: null,
      repo: null,
      view: null,
    });
  });

  it("sends every other kind to its classic page", () => {
    for (const kind of ["kanban", "gantt", "report", "panels", "tree"]) {
      expect(nextBoardParams(board(kind))).toBeNull();
    }
  });

  it("sends a list narrowed in the classic grammar to its classic page", () => {
    expect(nextBoardParams(board("list", "f=state:any:queued"))).toBeNull();
    expect(nextBoardParams(board("list", "q=retry&sort=created"))).toBeNull();
  });

  it("names a classic board by the kind the Boards index shows", () => {
    expect(classicBoardHint(board("gantt"))).toBe("timeline");
    expect(classicBoardHint(board("panels"))).toBe("dashboard");
    expect(classicBoardHint(board("kanban"))).toBe("kanban");
    expect(classicBoardHint(board("list", "f=state:any:queued"))).toBe(
      "filtered list",
    );
  });

  it("canonicalizes /next/tasks queries and excludes classic boards", () => {
    expect(canonicalNextBoardQuery(board("kanban", "q=merge"))).toBeNull();
    expect(canonicalNextBoardQuery(board("report", "repo=acme/w"))).toBeNull();
    expect(
      canonicalNextBoardQuery(board("list", "f=state:any:queued")),
    ).toBeNull();
    expect(
      canonicalNextBoardQuery(
        board("list", "q=merge&repo=acme/w&tab=asked&view=timeline"),
      ),
    ).toBe("tab=asked&q=merge&repo=acme%2Fw&view=timeline");
    expect(
      canonicalNextBoardQuery(board("list", "tab=backlog&group=none")),
    ).toBe("");
  });
});
