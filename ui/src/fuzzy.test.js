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
import { fuzzyFilter, fuzzyScore } from "./fuzzy.js";

const models = [
  "gemini-3.1-pro",
  "gemini-3.7-flash",
  "gemini-3.7-flash-lite",
  "claude-sonnet-5",
  "gpt-5.1-codex",
];

describe("fuzzyScore", () => {
  it("matches characters in order, ignoring case and spaces", () => {
    expect(fuzzyScore("GEM flash", "gemini-3.7-flash")).toBeGreaterThan(0);
    expect(fuzzyScore("g37f", "gemini-3.7-flash")).toBeGreaterThan(0);
    expect(fuzzyScore("hsalf", "gemini-3.7-flash")).toBe(-1);
  });

  it("matches everything when the query is empty", () => {
    expect(fuzzyScore("", "anything")).toBe(0);
    expect(fuzzyFilter(models, "  ")).toEqual(models);
  });
});

describe("fuzzyFilter", () => {
  it("keeps only matches, best first", () => {
    expect(fuzzyFilter(models, "flash")).toEqual([
      "gemini-3.7-flash",
      "gemini-3.7-flash-lite",
    ]);
    expect(fuzzyFilter(models, "codex")).toEqual(["gpt-5.1-codex"]);
    expect(fuzzyFilter(models, "gpro")[0]).toBe("gemini-3.1-pro");
  });

  it("filters objects by the text it is given", () => {
    const options = models.map((id) => ({ id }));
    expect(fuzzyFilter(options, "sonnet", (o) => o.id)).toEqual([
      { id: "claude-sonnet-5" },
    ]);
  });
});
