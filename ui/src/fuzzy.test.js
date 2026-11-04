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
import { fuzzyFilter, fuzzyScore, searchFilter, searchMatch } from "./fuzzy.js";

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

// Made-up names shaped like the ones the operator searches: a host-style
// three-segment repo among plain owner/name ones.
const repos = [
  "acme/widgets",
  "ai-on-gke/grain",
  "bwsalmon/silo",
  "depot.example.dev/experimental/grain",
  "gke-labs/granule",
  "grain-tools/docs",
];
const found = (query) => searchFilter(repos, query).map((m) => m.item);

describe("searchMatch", () => {
  it("matches a substring anywhere, case ignored", () => {
    expect(searchMatch("GRANULE", "gke-labs/granule")).not.toBeNull();
    expect(searchMatch("labs", "gke-labs/granule")).not.toBeNull();
    expect(searchMatch("nope", "gke-labs/granule")).toBeNull();
  });

  it("needs every space-separated token, in any order", () => {
    const text = "depot.example.dev/experimental/grain";
    expect(searchMatch("exp grain", text)).not.toBeNull();
    expect(searchMatch("grain exp", text)).not.toBeNull();
    expect(searchMatch("grain silo", text)).toBeNull();
  });

  it("ranks a token that starts a segment above one in the middle", () => {
    expect(searchMatch("grain", "ai-on-gke/grain").tier).toBe(0);
    expect(searchMatch("rain", "ai-on-gke/grain").tier).toBe(1);
  });

  it("reports the matched runs, merged and in order", () => {
    expect(searchMatch("grain", "ai-on-gke/grain").ranges).toEqual([[10, 15]]);
    // The segment-start occurrence wins over the first one.
    expect(searchMatch("grain", "x-grain/grain").ranges).toEqual([[8, 13]]);
    expect(searchMatch("gke ai", "ai-on-gke/grain").ranges).toEqual([
      [0, 2],
      [6, 9],
    ]);
  });
});

describe("searchFilter", () => {
  it("finds every repo named grain, wherever the name sits", () => {
    expect(found("grain")).toEqual([
      "ai-on-gke/grain",
      "grain-tools/docs",
      "depot.example.dev/experimental/grain",
    ]);
    expect(found("granule")).toEqual(["gke-labs/granule"]);
    expect(found("exp grain")).toEqual([
      "depot.example.dev/experimental/grain",
    ]);
  });

  it("ranks segment starts before other substrings", () => {
    expect(found("ra")).toEqual([
      "ai-on-gke/grain",
      "gke-labs/granule",
      "grain-tools/docs",
      "depot.example.dev/experimental/grain",
    ]);
    expect(found("silo")).toEqual(["bwsalmon/silo"]);
    expect(found("salmon")).toEqual(["bwsalmon/silo"]);
  });

  it("falls back to a subsequence only when no substring matches", () => {
    // "gnl" is in no name as written, but is in gke-labs/granule in order.
    expect(found("gnl")).toContain("gke-labs/granule");
    expect(searchFilter(repos, "gnl")[0].ranges.length).toBeGreaterThan(0);
    // "grain" matches by substring, so the scattered g..r..a..i..n in
    // gke-labs/granule is not offered beside the real matches.
    expect(found("grain")).not.toContain("gke-labs/granule");
    expect(found("zzz")).toEqual([]);
  });

  it("keeps every item, unhighlighted, for an empty query", () => {
    expect(searchFilter(repos, "  ")).toEqual(
      repos.map((item) => ({ item, ranges: [] })),
    );
  });

  it("filters objects by the text it is given", () => {
    const options = repos.map((r) => ({ r }));
    expect(
      searchFilter(options, "silo", (o) => o.r).map((m) => m.item),
    ).toEqual([{ r: "bwsalmon/silo" }]);
  });
});
