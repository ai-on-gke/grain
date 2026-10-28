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
