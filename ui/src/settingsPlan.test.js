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

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  formatSettingValue,
  planChanges,
  settingErrorMessage,
  settingLabel,
  settingLocation,
  settingMin,
} from "./settingsPlan.js";

describe("settingLabel", () => {
  it("names a setting the way the field that sets it is labelled", () => {
    expect(settingLabel("pollInterval")).toBe("Poll interval");
    expect(settingLabel("githubInsecureHttp")).toBe(
      "Speak plain HTTP to GitHub host",
    );
  });

  // A failed save reads "Couldn't save <label>" (useSettingsSection.js),
  // and the preview lists each change under the same label, so every box
  // a Settings section saves through needs one here -- the 2026-10-23
  // walkthrough found "Couldn't save maxAutoSubmitDepth" and
  // "Couldn't save sandboxDiskFloorShare" for boxes missing from the
  // table. This reads the sections' own data-setting-id attributes so a
  // box added later without a label fails here rather than in a browser.
  it("has a label for every setting a Settings section saves", () => {
    const dir = path.join(
      path.dirname(fileURLToPath(import.meta.url)),
      "components",
      "settings",
    );
    const ids = new Set();
    for (const file of fs.readdirSync(dir)) {
      if (!file.endsWith(".jsx") || file.includes(".test.")) continue;
      const src = fs.readFileSync(path.join(dir, file), "utf8");
      for (const m of src.matchAll(/data-setting-id="([A-Za-z0-9]+)"/g))
        ids.add(m[1]);
    }
    expect(ids.size).toBeGreaterThan(40);
    const unlabelled = [...ids].filter((id) => settingLabel(id) === id);
    expect(unlabelled).toEqual([]);
    const unlocated = [...ids].filter((id) => !settingLocation(id));
    expect(unlocated).toEqual([]);
  });

  it("maps each setting key to its Settings section and card anchor", () => {
    expect(settingLocation("pollInterval")).toEqual({
      section: "engine",
      anchor: "polling",
      href: "/settings/engine#polling",
    });
    expect(settingLocation("ciJobsPerRun")).toEqual({
      section: "ci",
      anchor: "ci-queue",
      href: "/settings/ci#ci-queue",
    });
    expect(settingLocation("sandboxBackend")).toEqual({
      section: "sandboxes",
      anchor: "backend-and-size",
      href: "/settings/sandboxes#backend-and-size",
    });
    expect(settingLocation("somethingNewerThanThisBuild")).toBeNull();
  });

  // A UI running against a newer daemon: the setting still gets a line,
  // under the only name this build knows for it.
  it("falls back to the JSON name for a setting it has never heard of", () => {
    expect(settingLabel("somethingNewerThanThisBuild")).toBe(
      "somethingNewerThanThisBuild",
    );
  });
});

describe("settingMin", () => {
  // The 2026-10-24 UI walkthrough (grain/task-660) found number boxes
  // sending out-of-range values to PUT /api/settings because nothing
  // checked their lower bound on blur. Every bounded number box in
  // components/settings must carry an inputProps `min` that matches
  // settingMin(id), and only the CI retention fields where -1 means
  // "keep everything" may omit `min`.
  it("matches the inputProps min on every bounded number box in Settings", () => {
    const dir = path.join(
      path.dirname(fileURLToPath(import.meta.url)),
      "components",
      "settings",
    );
    // CI retention fields accept -1 for "unlimited", so they have no min.
    const unbounded = new Set([
      "ciRunHistory",
      "ciStepOutputMb",
      "ciStepOutputDays",
    ]);
    const seen = new Map();
    for (const file of fs.readdirSync(dir)) {
      if (!file.endsWith(".jsx") || file.includes(".test.")) continue;
      const src = fs.readFileSync(path.join(dir, file), "utf8");
      for (const m of src.matchAll(/<TextField\b([\s\S]*?)\/>/g)) {
        const body = m[1];
        if (!/type="number"/.test(body)) continue;
        const id = body.match(/data-setting-id="([A-Za-z0-9]+)"/)?.[1];
        if (!id) continue;
        const minMatch = body.match(
          /inputProps=\{\{[^}]*\bmin:\s*(-?\d+(?:\.\d+)?)/,
        );
        seen.set(id, minMatch ? Number(minMatch[1]) : undefined);
      }
    }
    expect(seen.size).toBeGreaterThan(25);
    for (const [id, domMin] of seen) {
      if (unbounded.has(id)) {
        expect(domMin, `${id} should not have a min`).toBeUndefined();
        expect(
          settingMin(id),
          `${id} should not have settingMin`,
        ).toBeUndefined();
      } else {
        expect(domMin, `${id} is missing inputProps min`).toBeDefined();
        expect(settingMin(id), `${id} settingMin mismatch`).toBe(domMin);
      }
    }
  });
});

describe("settingErrorMessage", () => {
  it("replaces a leading API key with the box's on-screen label", () => {
    expect(settingErrorMessage("maxAutoSubmitDepth must be at least 1")).toBe(
      "Max unreviewed follow-up depth must be at least 1",
    );
    expect(settingErrorMessage("maxWorkers must be at least 1")).toBe(
      "Max worker agents must be at least 1",
    );
    expect(
      settingErrorMessage('pollInterval: time: invalid duration "banana"'),
    ).toBe('Poll interval: time: invalid duration "banana"');
  });

  it("leaves a message alone when it does not start with a known setting key", () => {
    expect(settingErrorMessage('time: invalid duration "banana"')).toBe(
      'time: invalid duration "banana"',
    );
    expect(settingErrorMessage("unknownKey must be at least 1")).toBe(
      "unknownKey must be at least 1",
    );
  });
});

describe("formatSettingValue", () => {
  it("writes each kind of value the way a reader wants it", () => {
    expect(formatSettingValue("60s")).toBe("60s");
    expect(formatSettingValue(4)).toBe("4");
    expect(formatSettingValue(true)).toBe("on");
    expect(formatSettingValue(false)).toBe("off");
    expect(formatSettingValue(["a/b", "c/d"])).toBe("a/b, c/d");
    expect(formatSettingValue([])).toBe("none");
  });

  // An absent key and an empty one read alike, so a settings response
  // that predates a field does not read as that field being cleared.
  it("reads a missing value and an empty one as the same not set", () => {
    expect(formatSettingValue("")).toBe("not set");
    expect(formatSettingValue(undefined)).toBe("not set");
    expect(formatSettingValue(null)).toBe("not set");
  });
});

describe("planChanges", () => {
  it("reports what the daemon would store, not what was typed", () => {
    const rows = planChanges(
      { pollInterval: "30s" },
      { pollInterval: "60s" },
      { pollInterval: "1m" },
    );

    expect(rows).toEqual([
      {
        field: "pollInterval",
        label: "Poll interval",
        was: "30s",
        becomes: "60s",
        changed: true,
        typed: "1m",
      },
    ]);
  });

  // The case a form comparing its own boxes against the last load gets
  // wrong: "1m" looks like a change and is not one.
  it("keeps a line for a typed value that normalizes to what is stored", () => {
    const rows = planChanges(
      { pollInterval: "60s" },
      { pollInterval: "60s" },
      { pollInterval: "1m" },
    );

    expect(rows).toEqual([
      {
        field: "pollInterval",
        label: "Poll interval",
        was: "60s",
        becomes: "60s",
        changed: false,
        typed: "1m",
      },
    ]);
  });

  it("says nothing about a value the daemon stores exactly as sent", () => {
    const [row] = planChanges(
      { maxWorkers: 2 },
      { maxWorkers: 4 },
      { maxWorkers: 4 },
    );

    expect(row).toMatchObject({ was: "2", becomes: "4", typed: null });
  });

  it("orders its rows the way the settings are listed, not the request", () => {
    const rows = planChanges(
      { maxWorkers: 2, environmentName: "" },
      { maxWorkers: 4, environmentName: "staging" },
      { maxWorkers: 4, environmentName: " staging " },
    );

    expect(rows.map((row) => row.field)).toEqual([
      "environmentName",
      "maxWorkers",
    ]);
    expect(rows[0]).toMatchObject({
      was: "not set",
      becomes: "staging",
      typed: " staging ",
    });
  });

  it("reports a setting the request never named but the plan moves", () => {
    const rows = planChanges(
      { agentFramework: "antigravity", claudeModel: "claude-sonnet-5" },
      { agentFramework: "claude", claudeModel: "claude-sonnet-5" },
      {},
    );

    expect(rows.map((row) => row.field)).toEqual(["agentFramework"]);
  });

  // A plan that leaves a key out has said nothing about that setting --
  // which is not the same as saying it would be cleared.
  it("ignores a setting the plan does not report at all", () => {
    expect(planChanges({ claudeModel: "claude-sonnet-5" }, {}, {})).toEqual([]);
  });

  // A stored value no daemon can run on is reported as the substitution
  // in force (ui.Settings.Unrunnable), and the pane's box holds that too
  // -- so the save that repairs the row sends a value equal to what the
  // pane was showing. The row it moves is the stored one, and that is
  // what the preview has to say it moves.
  it("previews an unrunnable stored value against the row, not the substitution", () => {
    const rows = planChanges(
      {
        pollInterval: "30s",
        unrunnable: [{ key: "pollInterval", stored: "0s", running: "30s" }],
      },
      { pollInterval: "30s" },
      { pollInterval: "30s" },
    );

    expect(rows).toEqual([
      {
        field: "pollInterval",
        label: "Poll interval",
        was: "0s",
        becomes: "30s",
        changed: true,
        typed: null,
      },
    ]);
  });

  // And a save that never names it says nothing about it: the plan
  // reports the same substitution the settings do, so there is no
  // difference to draw a line from.
  it("says nothing about an unrunnable value a save does not touch", () => {
    const rows = planChanges(
      {
        pollInterval: "30s",
        maxWorkers: 2,
        unrunnable: [{ key: "pollInterval", stored: "0s", running: "30s" }],
      },
      { pollInterval: "30s", maxWorkers: 4 },
      { maxWorkers: 4 },
    );

    expect(rows.map((row) => row.field)).toEqual(["maxWorkers"]);
  });

  it("gives a request nothing changed in nothing to show", () => {
    expect(planChanges({ maxWorkers: 2 }, { maxWorkers: 2 }, {})).toEqual([]);
  });

  it("still lists a setting this build has no label for", () => {
    const rows = planChanges({}, { somethingNew: "b" }, { somethingNew: "a" });

    expect(rows).toEqual([
      {
        field: "somethingNew",
        label: "somethingNew",
        was: "not set",
        becomes: "b",
        changed: true,
        typed: "a",
      },
    ]);
  });
});
