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
  filterTargets,
  hasBreakingChange,
  nextRCVersion,
  pickerHelp,
  readReleaseNotes,
  readTargets,
  resolvedImage,
} from "./upgrade-view";

describe("upgrade-view", () => {
  it("reads GET /api/upgrade/targets, current and legacy shapes", () => {
    const t = readTargets({
      targets: [
        { name: "latest", kind: "channel", allowed: true },
        { name: "v3.6.0", kind: "version", allowed: false },
      ],
      allowedBranches: ["latest"],
      default: "latest",
      retractedRelease: { running: "v3.6.0", latest: "v3.5.1" },
      discardedRc: { running: "" },
    });
    expect(t.targets.map((x) => [x.name, x.allowed])).toEqual([
      ["latest", true],
      ["v3.6.0", false],
    ]);
    expect(t.retractedRelease).toEqual({
      running: "v3.6.0",
      latest: "v3.5.1",
      newer: undefined,
    });
    // A discardedRc that names no running candidate is no warning.
    expect(t.discardedRc).toBeNull();

    const legacy = readTargets([{ branch: "stable", version: "v3.7.0" }]);
    expect(legacy.targets).toEqual([
      { name: "stable", kind: "channel", allowed: true, version: "v3.7.0" },
    ]);
  });

  it("filters targets like the classic picker: all on a listed name, else substring", () => {
    const targets = readTargets({
      targets: [
        { name: "latest", kind: "channel", allowed: true },
        { name: "main", kind: "channel", allowed: true },
        { name: "v3.6.0", kind: "version", allowed: true },
        { name: "v3.7.0", kind: "version", allowed: true },
      ],
    }).targets;
    expect(filterTargets(targets, "").map((g) => g.label)).toEqual([
      "Standard branches",
      "Released versions",
    ]);
    expect(
      filterTargets(targets, "main").flatMap((g) => g.options.length),
    ).toEqual([2, 2]);
    expect(
      filterTargets(targets, "3.7").map((g) => g.options.map((o) => o.name)),
    ).toEqual([["v3.7.0"]]);
    expect(filterTargets(targets, "nothing-like-it")).toEqual([]);
  });

  it("says which branches the deployment allows", () => {
    const t = readTargets({ targets: [], allowedBranches: ["latest", "main"] });
    expect(pickerHelp(t, "")).toContain(
      "only upgrades to latest, main (-upgrade-allowed-branches)",
    );
    expect(pickerHelp(null, "502 Bad Gateway")).toContain(
      "could not be loaded (502 Bad Gateway)",
    );
  });

  it("resolves the image an upgrade would pull", () => {
    expect(resolvedImage("", "docker.io/grainai/grain", "feature/x")).toBe(
      "docker.io/grainai/grain:feature..x",
    );
    expect(
      resolvedImage("ghcr.io/acme/grain", "docker.io/grainai/grain", "main"),
    ).toBe("ghcr.io/acme/grain:main");
    expect(
      resolvedImage("ghcr.io/acme/grain:v1", "docker.io/grainai/grain", "main"),
    ).toBe("ghcr.io/acme/grain:v1");
    expect(
      resolvedImage("localhost:5000/grain", "docker.io/grainai/grain", "main"),
    ).toBe("localhost:5000/grain:main");
    expect(resolvedImage("r/grain@sha256:abc", "x", "main")).toBe(
      "r/grain@sha256:abc",
    );
  });

  it("flags breaking changes in release notes", () => {
    expect(hasBreakingChange("## Breaking changes\n- gone")).toBe(true);
    expect(hasBreakingChange("- **Breaking:** the flag is gone")).toBe(true);
    expect(hasBreakingChange("BREAKING CHANGE: renamed")).toBe(true);
    expect(hasBreakingChange("- Host sandboxes are removed (breaking).")).toBe(
      true,
    );
    expect(hasBreakingChange("- Host sandboxes are removed (breaking)")).toBe(
      true,
    );
    expect(hasBreakingChange("- Fixed a non-breaking bug")).toBe(false);
  });

  it("reads the release-notes preview", () => {
    const p = readReleaseNotes({
      from: "v3.5.1",
      version: "v3.7.0",
      notes: [
        { version: "v3.7.0", markdown: "# v3.7.0\n- **Breaking:** x" },
        { version: "v3.6.0", markdown: "" },
      ],
    });
    expect(p.notes.map((n) => [n.version, n.breaking])).toEqual([
      ["v3.7.0", true],
      ["v3.6.0", false],
    ]);
    expect(p.notes[1].markdown).toBe("# v3.6.0");
    expect(readReleaseNotes("## v3.7.0\n- Faster").notes).toHaveLength(1);
  });

  it("names the next release candidate", () => {
    expect(nextRCVersion("v3.7.0-rc.2")).toBe("v3.7.0-rc.3");
    expect(nextRCVersion("v3.7.0")).toBe("the next candidate");
  });
});
