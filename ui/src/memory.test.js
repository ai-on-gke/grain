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
  STALE_AFTER_DAYS,
  documentSize,
  isStaleMemory,
  memoryAge,
  memoryAgeDays,
  memoryAuthor,
  memoryScopes,
  scopeLabel,
} from "./memory.js";

const NOW = Date.parse("2026-09-08T12:00:00Z");
const daysAgo = (days) =>
  new Date(NOW - days * 24 * 60 * 60 * 1000).toISOString();

describe("memoryAge", () => {
  // The same words model.MemoryAge puts in every prompt, deliberately:
  // an operator reading this pane is reading what the agents are told.
  it("says how old a note is in the prompt's own words", () => {
    expect(memoryAge(daysAgo(0), NOW)).toBe("written today");
    expect(memoryAge(daysAgo(1), NOW)).toBe("written yesterday");
    expect(memoryAge(daysAgo(12), NOW)).toBe("written 12 days ago");
    expect(memoryAge(daysAgo(120), NOW)).toBe("written 4 months ago");
  });

  it("says nothing about a note with no timestamp or a future one", () => {
    expect(memoryAge("", NOW)).toBe("");
    expect(memoryAge(undefined, NOW)).toBe("");
    expect(memoryAge("not a time", NOW)).toBe("");
    expect(memoryAge(new Date(NOW + 60_000).toISOString(), NOW)).toBe("");
    expect(memoryAgeDays(new Date(NOW + 60_000).toISOString(), NOW)).toBeNull();
  });
});

describe("isStaleMemory", () => {
  it("marks a note once grain itself has started counting it in months", () => {
    expect(isStaleMemory(daysAgo(STALE_AFTER_DAYS - 1), NOW)).toBe(false);
    expect(isStaleMemory(daysAgo(STALE_AFTER_DAYS), NOW)).toBe(true);
  });

  // Unknown and old are different things, and the second is a claim this
  // pane has nothing to base on a row with no timestamp.
  it("does not call a note with no timestamp stale", () => {
    expect(isStaleMemory(undefined, NOW)).toBe(false);
  });
});

describe("memoryAuthor", () => {
  // The distinction the column exists for: a person editing the pane and
  // the memory agent make the same write, so this is the only thing that
  // can tell a reader which one a note came from.
  it("tells an operator's own edit from what the memory agent wrote", () => {
    expect(memoryAuthor("human", "bwsalmon")).toMatchObject({
      label: "edited here",
      title: "Edited by bwsalmon",
    });
    expect(memoryAuthor("agent", "a1b2")).toMatchObject({
      label: "written by the memory agent",
    });
    expect(memoryAuthor("automation", "grain").label).toBe(
      "written by the memory agent",
    );
  });

  // Unknown is not a third kind of author to dress up as one of the two.
  // Every row written before model.Memory.UpdatedBy existed has no
  // author, and guessing "the memory agent" would be right most of the
  // time and wrong exactly where it matters.
  it("says nothing about a note whose author was never recorded", () => {
    expect(memoryAuthor(undefined, undefined)).toBeNull();
    expect(memoryAuthor("", "")).toBeNull();
  });

  it("leaves out the tooltip when the author has no name", () => {
    expect(memoryAuthor("human", "").title).toBeUndefined();
  });
});

describe("scopeLabel", () => {
  it("names the deployment scope rather than leaving it blank", () => {
    expect(scopeLabel("")).toBe("This deployment");
    expect(scopeLabel("bwsalmon/grain")).toBe("bwsalmon/grain");
  });
});

describe("memoryScopes", () => {
  it("puts the deployment first and the repos after it in name order", () => {
    const rows = memoryScopes(
      [
        { repo: "bwsalmon/granule", text: "granule", updatedAt: daysAgo(1) },
        { text: "everywhere", updatedAt: daysAgo(2) },
        { repo: "bwsalmon/bazzite", text: "bazzite" },
      ],
      [],
    );
    expect(rows.map((r) => r.repo)).toEqual([
      "",
      "bwsalmon/bazzite",
      "bwsalmon/granule",
    ]);
    expect(rows[0].text).toBe("everywhere");
    expect(rows[0].updatedAt).toBe(daysAgo(2));
  });

  // Who wrote it is a fact about the note, so it has to reach the card
  // the same way the age does.
  it("carries each memory's author onto its row", () => {
    const rows = memoryScopes(
      [{ text: "everywhere", updatedBy: "bwsalmon", updatedByKind: "human" }],
      [],
    );
    expect(
      memoryAuthor(rows[0].updatedByKind, rows[0].updatedBy),
    ).toMatchObject({ label: "edited here" });
  });

  // The two listings are written independently, so a scope can have
  // documents and no memory -- and that note is exactly the one nothing
  // in the UI could show before this pane.
  it("gives a scope with documents and no memory a row of its own", () => {
    const rows = memoryScopes(
      [],
      [
        {
          repo: "bwsalmon/granule",
          name: "flaky-e2e",
          summary: "why",
          length: 9,
        },
      ],
    );
    expect(rows.map((r) => r.repo)).toEqual(["", "bwsalmon/granule"]);
    expect(rows[1].text).toBe("");
    expect(rows[1].documents).toHaveLength(1);
  });

  it("always has the deployment row, even with nothing stored anywhere", () => {
    const rows = memoryScopes([], []);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ repo: "", text: "", documents: [] });
  });

  // repoRows' rule, one page over: GitHub resolves owner/name without
  // regard to case, so two spellings are one repository and its memory
  // and its documents belong on one row.
  it("matches a repo's memory and its documents case-insensitively", () => {
    const rows = memoryScopes(
      [{ repo: "bwsalmon/Grain", text: "note" }],
      [{ repo: "bwsalmon/grain", name: "one", summary: "s", length: 4 }],
    );
    expect(rows).toHaveLength(2);
    expect(rows[1].text).toBe("note");
    expect(rows[1].documents).toHaveLength(1);
  });

  it("adds a row for a repo asked for but never written about", () => {
    const rows = memoryScopes([], [], ["bwsalmon/new"]);
    expect(rows.map((r) => r.repo)).toEqual(["", "bwsalmon/new"]);
    expect(rows[1].text).toBe("");
  });

  it("filters memory scopes to allowedRepos when provided", () => {
    const memories = [
      { repo: "", text: "deployment standing memory" },
      { repo: "allowed/repo1", text: "note for repo 1" },
      { repo: "other/repo2", text: "leaked note" },
    ];
    const documents = [
      { repo: "allowed/repo1", name: "doc1", summary: "sum1" },
      { repo: "other/repo2", name: "doc2", summary: "sum2" },
    ];
    const extraRepos = ["allowed/repo3", "other/repo4"];
    const rows = memoryScopes(memories, documents, extraRepos, [
      "allowed/repo1",
      "allowed/repo3",
    ]);

    expect(rows.map((r) => r.repo)).toEqual([
      "",
      "allowed/repo1",
      "allowed/repo3",
    ]);
    expect(rows[0].text).toBe("deployment standing memory");
    expect(rows[1].text).toBe("note for repo 1");
    expect(rows[1].documents).toHaveLength(1);
    expect(rows[2].text).toBe("");
  });
});

describe("documentSize", () => {
  it("keeps short notes in bytes and long ones in kilobytes", () => {
    expect(documentSize(0)).toBe("0 B");
    expect(documentSize(912)).toBe("912 B");
    expect(documentSize(2048)).toBe("2.0 KB");
    expect(documentSize(undefined)).toBe("0 B");
  });
});
