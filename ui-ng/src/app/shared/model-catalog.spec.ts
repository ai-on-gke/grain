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

import {
  advertisedModels,
  availabilityNote,
  containsModel,
  modelAvailability,
  modelFramework,
  modelGroups,
  namedEffort,
  noEffortReason,
  parseDefaultModels,
} from "./model-catalog";

describe("model-catalog", () => {
  it("parses the default model list like model.ParseDefaultModels", () => {
    // Blanks dropped, case-insensitive duplicates dropped, first spelling kept.
    expect(
      parseDefaultModels(" gemini-3-pro , gemini-3-flash , GEMINI-3-PRO ,, "),
    ).toEqual(["gemini-3-pro", "gemini-3-flash"]);
    // A list may itself carry comma-joined entries (classic defaultModels()).
    expect(parseDefaultModels(["a", "b,c", " A ", ""])).toEqual([
      "a",
      "b",
      "c",
    ]);
    expect(parseDefaultModels(undefined)).toEqual([]);
    expect(parseDefaultModels(42)).toEqual([]);
    expect(parseDefaultModels("")).toEqual([]);
  });

  it("advertises a pinned list, else discovered minus exclusions (SupportedModels)", () => {
    expect(
      advertisedModels({
        id: "p",
        models: [" m1 ", "m2", "m1", ""],
        discovered: { models: ["x"] },
      }),
    ).toEqual(["m1", "m2"]);
    expect(
      advertisedModels({
        id: "d",
        excludedModels: ["OLD"],
        discovered: { models: ["new", "old", "new", " "] },
      }),
    ).toEqual(["new"]);
    // A provider never asked successfully advertises nothing: no guessed names.
    expect(advertisedModels({ id: "n", discovered: null })).toEqual([]);
  });

  it("groups models by connection with labels, state and discovery date", () => {
    const groups = modelGroups([
      {
        id: "agy",
        name: "Ambient agy",
        framework: "antigravity",
        discovered: {
          models: ["g1", "g2"],
          refreshedAt: "2026-11-13T09:00:00Z",
        },
      },
      { id: "claude", framework: "claude", models: ["c1"], disabled: true },
      { id: "", framework: "codex" },
    ]);
    expect(groups.map((g) => g.id)).toEqual(["agy", "claude"]);
    expect(groups[0]).toMatchObject({
      label: "Ambient agy (agy)",
      framework: "antigravity",
      disabled: false,
      pinned: false,
      models: ["g1", "g2"],
      refreshedAt: "2026-11-13T09:00:00Z",
    });
    expect(groups[1]).toMatchObject({
      label: "claude",
      disabled: true,
      pinned: true,
      models: ["c1"],
    });
    expect(modelGroups(null)).toEqual([]);
  });

  it("says whether an enabled connection, only disabled ones, or none advertise a model", () => {
    const groups = modelGroups([
      { id: "a", discovered: { models: ["Shared", "only-a"] } },
      { id: "b", models: ["shared", "only-b"], disabled: true },
    ]);
    expect(modelAvailability("shared", groups)).toBe("enabled");
    expect(modelAvailability("only-b", groups)).toBe("disabled-only");
    expect(modelAvailability("ghost", groups)).toBe("unadvertised");
    expect(availabilityNote("unadvertised")).toBe(
      "not advertised by any connection",
    );
    expect(availabilityNote("disabled-only")).toBe("disabled connections only");
    expect(availabilityNote("enabled")).toBe("");
    expect(containsModel(["Gemini-X"], "gemini-x")).toBe(true);
  });

  it("says which models take no separate reasoning effort", () => {
    const groups = modelGroups([
      {
        id: "claude-off",
        framework: "claude",
        models: ["opus-5.5", "claude-only-off"],
        disabled: true,
      },
      {
        id: "agy",
        framework: "antigravity",
        models: ["opus-5.5", "opus-5.5-high", "opus-5.5-xhigh"],
      },
      { id: "cx", framework: "codex", models: ["gpt-5.1-codex-max"] },
      { id: "untyped", framework: "", models: ["o3-mini-high"] },
    ]);
    expect(modelFramework("OPUS-5.5-high", groups)).toBe("antigravity");
    // Enabled connections win over an earlier disabled one serving the same ID.
    expect(modelFramework("opus-5.5", groups)).toBe("antigravity");
    expect(modelFramework("claude-only-off", groups)).toBe("claude");
    // Unadvertised or untyped models fall back to model.ModelFramework's prefixes.
    expect(modelFramework("gpt-5.1-codex-max", [])).toBe("codex");
    expect(modelFramework("codex-mini-latest", [])).toBe("codex");
    expect(modelFramework("o3-mini-high", groups)).toBe("codex");
    expect(modelFramework("o4-mini-high", [])).toBe("codex");
    expect(modelFramework("claude-opus-5-5", [])).toBe("claude");
    expect(modelFramework("gemini-3-pro", [])).toBe("antigravity");
    expect(modelFramework("ghost", groups)).toBe("");
    // Antigravity names its effort (low|medium|high|xhigh|max); Codex's -max is a model.
    expect(namedEffort("gemini-4-carbon-medium", "antigravity")).toBe("medium");
    expect(namedEffort("opus-5.5-xhigh", "antigravity")).toBe("xhigh");
    expect(namedEffort("OPUS-5.5-MAX", "antigravity")).toBe("max");
    expect(namedEffort("fable-5.2-high", "")).toBe("high");
    expect(namedEffort("gpt-5.1-codex-max", "codex")).toBe("");
    expect(namedEffort("gpt-5.1-codex-max", "")).toBe("");
    expect(namedEffort("gemini-3-pro", "antigravity")).toBe("");
    expect(noEffortReason("opus-5.5-high", "antigravity")).toBe(
      "Effort is part of the model name: high.",
    );
    expect(noEffortReason("opus-5.5-xhigh", "antigravity")).toBe(
      "Effort is part of the model name: xhigh.",
    );
    expect(noEffortReason("auto", "antigravity")).toContain("auto");
    expect(noEffortReason("claude-opus-5-5", "claude")).toBe(
      "Claude takes no reasoning effort.",
    );
    expect(
      noEffortReason("claude-opus-5-5", modelFramework("claude-opus-5-5", [])),
    ).toBe("Claude takes no reasoning effort.");
    expect(noEffortReason("claude-opus-5-5", "")).toBe(
      "Claude takes no reasoning effort.",
    );
    // Models that take an effort (including unadvertised Codex -max), and no model at all, get no reason.
    expect(noEffortReason("gpt-5.1-codex", "codex")).toBe("");
    expect(noEffortReason("gpt-5.1-codex-max", "codex")).toBe("");
    expect(
      noEffortReason(
        "gpt-5.1-codex-max",
        modelFramework("gpt-5.1-codex-max", []),
      ),
    ).toBe("");
    expect(noEffortReason("gpt-5.1-codex-max", "")).toBe("");
    expect(noEffortReason("opus-5.5", modelFramework("opus-5.5", groups))).toBe(
      "",
    );
    expect(noEffortReason("gemini-3-pro", "antigravity")).toBe("");
    expect(noEffortReason("", "antigravity")).toBe("");
  });
});
