import { describe, expect, it } from "vitest";
import { normalizeSections } from "./report.js";
import { sectionMove, sectionPlaces } from "./sectionOrder.js";

const report = (...sections) => normalizeSections(sections);

describe("section ordering", () => {
  const sections = report(
    { kind: "throughput", title: "Flow", orderKey: 10 },
    { kind: "latency", title: "Wait", orderKey: 20 },
    { kind: "tools", title: "Census", orderKey: 30 },
  );

  it("offers every other place", () => {
    expect(sectionPlaces(sections, "section-2")).toEqual([
      "section-0",
      "section-1",
    ]);
  });

  it("returns the board's whole sections array with a positive key", () => {
    expect(sectionMove(sections, "section-2", "section-1")).toEqual([
      { kind: "throughput", title: "Flow", orderKey: 10 },
      { kind: "tools", title: "Census", orderKey: 15 },
      { kind: "latency", title: "Wait", orderKey: 20 },
    ]);
  });

  it("steps below zero when landing ahead of a zero key", () => {
    const rows = sectionMove(
      report(
        { kind: "throughput", orderKey: 0 },
        { kind: "latency", orderKey: 1 },
      ),
      "section-1",
      "section-0",
    );
    expect(rows.map((row) => row.orderKey)).toEqual([-(1 << 20), 0]);
  });

  it("preserves the stored row vocabulary when writing", () => {
    const rows = report(
      { kind: "throughput", orderKey: 10 },
      { kind: "latency", title: "Wait", orderKey: 20 },
    );
    // Stands in for a row normalized by an older build before it is sent
    // through a whole-array rewrite.
    rows[1].stored.kind = "future-latency";
    expect(sectionMove(rows, "section-0", null)[0]).toMatchObject({
      kind: "future-latency",
      title: "Wait",
    });
  });
});
