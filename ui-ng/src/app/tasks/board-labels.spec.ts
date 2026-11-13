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
import { boardLabels } from "./board-labels";

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
