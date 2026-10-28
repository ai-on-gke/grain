import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { TimeZoneProvider, useTimeZone } from "./TimeZoneContext.jsx";

function Zone() {
  return <span>zone: {useTimeZone() || "(none)"}</span>;
}

describe("TimeZoneContext", () => {
  it("hands the deployment's zone to whatever is under it", () => {
    render(
      <TimeZoneProvider zone="America/Los_Angeles">
        <Zone />
      </TimeZoneProvider>,
    );
    expect(screen.getByText("zone: America/Los_Angeles")).toBeInTheDocument();
  });

  // A component rendered outside a provider -- which is every component
  // test in this suite that does not opt in -- sees "", which time.js
  // reads as "format in whatever zone this browser is in". That is what
  // every timestamp did before this existed, so a missing provider is
  // the old behaviour rather than a crash.
  it("reads as no zone at all outside a provider", () => {
    render(<Zone />);
    expect(screen.getByText("zone: (none)")).toBeInTheDocument();
  });

  // A deployment that reports no zone (a settings response from before
  // the field existed) is the same case, rather than the string
  // "undefined" reaching Intl.
  it("treats a missing zone as none", () => {
    render(
      <TimeZoneProvider>
        <Zone />
      </TimeZoneProvider>,
    );
    expect(screen.getByText("zone: (none)")).toBeInTheDocument();
  });
});
