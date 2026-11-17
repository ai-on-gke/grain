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
import {
  capServerRuns,
  capServerState,
  capServerStateHelp,
  holderLabel,
  removeServerConfirm,
  sortCapServers,
  summarize,
} from "./capability-servers";

describe("capability servers", () => {
  it("lists in-process built-ins first, then built-ins, then custom, keeping order within each", () => {
    const ids = sortCapServers([
      { id: "custom-a" },
      { id: "figma", builtin: { name: "figma" } },
      { id: "api", inProcess: { name: "api" } },
      { id: "custom-b" },
      { id: "gcp", config: { builtin: "gcp" } },
      { id: "github", inProcess: { name: "github" } },
    ]).map((s) => s.id);
    expect(ids).toEqual([
      "api",
      "github",
      "figma",
      "gcp",
      "custom-a",
      "custom-b",
    ]);
  });

  it("says host rather than hostd in a placed server's chip, help and runs label", () => {
    const srv = {
      id: "s",
      config: { command: "x", run: "hostd", hostd: "mac-mini" },
      status: { state: "needs-hostd:mac-mini" },
    };
    expect(capServerState(srv).label).toBe("needs host mac-mini");
    expect(capServerStateHelp(srv)).not.toMatch(/hostd/);
    expect(capServerRuns(srv).label).toBe("on host mac-mini");
  });

  it("names what a waiting server waits for", () => {
    expect(
      capServerState({
        id: "g",
        status: { state: "needs-config", waiting: "a renewed credential" },
      }),
    ).toEqual({ label: "waiting for a renewed credential", tone: "blast" });
  });

  it("cuts a long summary to its first sentence and keeps the rest", () => {
    const s = summarize("Mints keys. Then it does a great deal more besides.");
    expect(s).toEqual({
      short: "Mints keys.",
      full: "Mints keys. Then it does a great deal more besides.",
      truncated: true,
    });
    expect(summarize("Short.").truncated).toBe(false);
  });

  it("labels holders as people read them", () => {
    expect(
      holderLabel({
        kind: "task",
        id: "7",
        name: "Fix it",
        repo: "acme/app",
        grant: "g",
      }),
    ).toBe("Task #7 “Fix it” on acme/app");
    expect(holderLabel({ kind: "default", id: "deployment", grant: "g" })).toBe(
      "Deployment defaults",
    );
  });
});

describe("removeServerConfirm", () => {
  const run = () => Promise.resolve();
  it("says grain could not count when usage is unknown", () => {
    const req = removeServerConfirm("srv", null, run);
    expect(req.message).toContain("could not count");
    expect(req.items).toEqual([]);
  });
  it("lists each holder with its grant", () => {
    const req = removeServerConfirm(
      "srv",
      [{ kind: "template", id: "n", name: "Nightly", grant: "srv#t" }],
      run,
    );
    expect(req.message).toContain("1 grant still names");
    expect(req.items).toEqual(["Template “Nightly” — srv#t"]);
  });
});
