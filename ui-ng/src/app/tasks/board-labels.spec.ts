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
import { boardKindName, boardLabels, opensHere } from "./board-labels";

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

describe("opensHere", () => {
  const q = (kind: string, query?: string): Board => ({
    id: "1",
    name: "B",
    kind,
    ...(query ? { query } : {}),
  });
  it("keeps a list in this page's own grammar here", () => {
    expect(opensHere(q("list"))).toBe(true);
    expect(opensHere(q("list", "tab=asked&q=retry&view=timeline"))).toBe(true);
  });
  it("sends every other kind to its classic page", () => {
    for (const k of ["kanban", "gantt", "report", "tree", "panels"]) {
      expect(opensHere(q(k))).toBe(false);
    }
  });
  it("sends a list narrowed in the classic grammar to its classic page", () => {
    expect(opensHere(q("list", "f=state:any:queued"))).toBe(false);
    expect(opensHere(q("list", "tab=asked&sort=priority"))).toBe(false);
  });
  it("names kinds as the classic UI does", () => {
    expect(boardKindName("gantt")).toBe("Timeline");
    expect(boardKindName("panels")).toBe("Panels");
    expect(boardKindName(undefined)).toBe("List");
  });
});
