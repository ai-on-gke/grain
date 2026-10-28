import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import CIHistoryPanel, {
  ciHistoryBounds,
  ciHistorySummary,
  oldestAge,
} from "./CIHistoryPanel.jsx";

const NOW = new Date("2026-09-18T12:00:00Z").getTime();
const daysAgo = (days) =>
  new Date(NOW - days * 24 * 60 * 60 * 1000).toISOString();

const history = {
  bytes: 48 * (1 << 20),
  runs: 37,
  steps: 104,
  oldestAt: daysAgo(3),
  ceiling: 64 * (1 << 20),
  ageDays: 14,
  runsKept: 5000,
};

describe("CIHistoryPanel", () => {
  it("says what is stored, against the ceiling that sweeps it", () => {
    render(<CIHistoryPanel history={history} now={NOW} />);
    expect(screen.getByText(/48\.0 MiB of 64\.0 MiB/)).toBeInTheDocument();
    expect(
      screen.getByText(/37 runs still have step output/),
    ).toBeInTheDocument();
    // The answer to "will Friday's failure still be readable on Monday".
    expect(screen.getByText(/the oldest of it 3 days old/)).toBeInTheDocument();
    expect(
      screen.getByText(
        /Kept: up to 64\.0 MiB of it, none older than 14 days, 5000 runs' records behind it\./,
      ),
    ).toBeInTheDocument();
  });

  it("reads a bound an operator has switched off as no bound", () => {
    const summary = ciHistorySummary({ ...history, ceiling: 0, runs: 1 }, NOW);
    expect(summary).toMatch(/48\.0 MiB, no ceiling/);
    // One run says "has", not "have": this line is read by somebody
    // deciding whether to touch a setting, and it should read like prose.
    expect(summary).toMatch(/1 run still has step output/);
    expect(
      ciHistoryBounds({ ...history, ceiling: 0, ageDays: 0, runsKept: 0 }),
    ).toBe("Kept: every byte of it, every run's record behind it.");
  });

  it("calls an empty history empty rather than missing", () => {
    render(
      <CIHistoryPanel
        history={{ bytes: 0, runs: 0, ceiling: 64 * (1 << 20), ageDays: 14 }}
        now={NOW}
      />,
    );
    expect(
      screen.getByText(/0 B of 64\.0 MiB · no run is holding step output/),
    ).toBeInTheDocument();
  });

  it("says nothing it cannot measure", () => {
    render(<CIHistoryPanel history={null} />);
    expect(screen.getByText(/could not be read/)).toBeInTheDocument();
  });

  it("ages the oldest output in the units somebody asks in", () => {
    expect(oldestAge(daysAgo(1), NOW)).toBe("1 day");
    expect(
      oldestAge(new Date(NOW - 5 * 60 * 60 * 1000).toISOString(), NOW),
    ).toBe("5 hours");
    expect(oldestAge(new Date(NOW - 60 * 1000).toISOString(), NOW)).toBe(
      "under an hour",
    );
    expect(oldestAge(null, NOW)).toBe("");
  });
});
