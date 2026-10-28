import { describe, expect, it } from "vitest";
import {
  FALLBACK_TIME_ZONES,
  browserTimeZone,
  formatDateTime,
  formatTime,
  timeZoneOptions,
  zoneAbbreviation,
} from "./time.js";

const noon = "2026-01-15T20:00:00Z";

describe("formatDateTime", () => {
  // The whole point: the same instant reads as a different wall-clock
  // time depending on the deployment's zone, and the browser's own zone
  // is not what decides it.
  // Compared against Intl rather than against a literal string, so this
  // says "the zone decides" rather than "this machine's locale prints
  // it this way" -- what matters is that the two zones disagree.
  it("formats an instant on the given zone's clock", () => {
    const inZone = (zone) =>
      new Date(noon).toLocaleString(undefined, { timeZone: zone });
    expect(formatDateTime(noon, "UTC")).toBe(inZone("UTC"));
    expect(formatDateTime(noon, "America/Los_Angeles")).toBe(
      inZone("America/Los_Angeles"),
    );
    expect(inZone("UTC")).not.toBe(inZone("America/Los_Angeles"));
  });

  it("falls back to the browser's own zone when none is given", () => {
    expect(formatDateTime(noon, "")).toBe(new Date(noon).toLocaleString());
  });

  // A zone this browser has never heard of throws inside Intl rather
  // than falling back on its own; the time is still worth printing.
  it("still prints a time when the zone is one this browser rejects", () => {
    expect(formatDateTime(noon, "Mars/Olympus_Mons")).toBe(
      new Date(noon).toLocaleString(),
    );
  });

  // These are printed inside sentences ("started ..."), where "Invalid
  // Date" would read as data rather than as the absence of it.
  it("renders nothing for a value that is not a time", () => {
    expect(formatDateTime(undefined, "UTC")).toBe("");
    expect(formatDateTime("not a date", "UTC")).toBe("");
  });

  it("takes a Date as readily as an ISO string", () => {
    expect(formatDateTime(new Date(noon), "UTC")).toBe(
      formatDateTime(noon, "UTC"),
    );
  });
});

describe("formatTime", () => {
  it("gives the clock time alone, on the given zone", () => {
    const clock = (zone) =>
      new Date(noon).toLocaleString(undefined, {
        hour: "numeric",
        minute: "2-digit",
        second: "2-digit",
        timeZone: zone,
      });
    expect(formatTime(noon, "UTC")).toBe(clock("UTC"));
    expect(formatTime(noon, "America/Los_Angeles")).toBe(
      clock("America/Los_Angeles"),
    );
    // The date is not in it -- that is the difference from formatDateTime.
    expect(formatTime(noon, "UTC")).not.toContain("2026");
  });
});

describe("zoneAbbreviation", () => {
  // Not asserted as the literal "PST"/"PDT", which is what an en-US
  // browser happens to call them: what this is for is that the label
  // follows daylight saving rather than being fixed once.
  it("names what a clock in that zone is called right now", () => {
    const winter = zoneAbbreviation("America/Los_Angeles", new Date(noon));
    const summer = zoneAbbreviation(
      "America/Los_Angeles",
      new Date("2026-07-15T20:00:00Z"),
    );
    expect(winter).not.toBe("");
    expect(summer).not.toBe("");
    expect(winter).not.toBe(summer);
  });

  it("says nothing for no zone, or one the browser rejects", () => {
    expect(zoneAbbreviation("")).toBe("");
    expect(zoneAbbreviation("Mars/Olympus_Mons")).toBe("");
  });
});

describe("timeZoneOptions", () => {
  it("offers a long list of real zones, sorted, including UTC", () => {
    const zones = timeZoneOptions();
    expect(zones).toContain("UTC");
    expect(zones).toContain("America/Los_Angeles");
    expect(zones.length).toBeGreaterThan(FALLBACK_TIME_ZONES.length - 1);
    expect([...zones].sort()).toEqual(zones);
  });

  // A deployment set to a zone this browser has never heard of must
  // still show what it is set to, rather than silently reading as
  // whichever zone happens to sort first.
  it("always includes the zone currently in effect", () => {
    expect(timeZoneOptions("Mars/Olympus_Mons")).toContain("Mars/Olympus_Mons");
  });
});

describe("browserTimeZone", () => {
  it("reports the zone this browser is in", () => {
    expect(browserTimeZone()).toBe(
      Intl.DateTimeFormat().resolvedOptions().timeZone,
    );
  });
});
