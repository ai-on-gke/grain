import { describe, expect, it } from "vitest";
import { releaseNoteAuthor, weekLabel } from "./releaseNotes.js";

describe("weekLabel", () => {
  it("reads an ISO week back as a week and a year", () => {
    expect(weekLabel("2026-W38")).toBe("Week 38, 2026");
    // The zero is padding in the key, not part of the number a reader
    // is shown.
    expect(weekLabel("2026-W07")).toBe("Week 7, 2026");
  });

  it("leaves anything that is not one alone", () => {
    expect(weekLabel("last week")).toBe("last week");
    expect(weekLabel(undefined)).toBe("");
  });
});

describe("releaseNoteAuthor", () => {
  it("tells a week somebody corrected from one the job wrote", () => {
    expect(releaseNoteAuthor({ updatedByKind: "human" })).toBe(
      "Written by hand",
    );
    expect(releaseNoteAuthor({ updatedByKind: "agent" })).toBe(
      "Written by grain's release-notes job",
    );
  });

  // A row written before the author was recorded claims neither, for
  // the reason memoryAuthor keeps the same silence: guessing "the job"
  // would be right most of the time and wrong exactly where it matters.
  it("claims neither for a week whose author was never recorded", () => {
    expect(releaseNoteAuthor({})).toBe("Published");
    expect(releaseNoteAuthor(null)).toBe("Published");
  });
});
