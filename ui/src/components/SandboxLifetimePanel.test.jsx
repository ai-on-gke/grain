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

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import SandboxLifetimePanel, {
  countOf,
  hoursOf,
  sandboxKindLine,
  sandboxLifetimeSummary,
} from "./SandboxLifetimePanel.jsx";

// What a deployment's sandboxes have cost it, drawn (grain/task-315).
// The figures are cumulative and the date they are "since" is part of
// the sentence rather than a footnote: a count of sandboxes with no
// start date behind it is a number nobody can check.

const lifetime = {
  total: {
    attempted: 2416,
    created: 2413,
    failed: 3,
    finished: 2410,
    unaccounted: 3,
    seconds: 4334400,
    hours: 1204,
    meanSeconds: 1798.5,
    since: "2026-09-05T04:08:00Z",
  },
  kinds: [
    {
      kind: "agent",
      attempted: 46,
      created: 46,
      failed: 0,
      finished: 46,
      unaccounted: 0,
      hours: 3,
      meanSeconds: 234.8,
      since: "2026-09-05T04:08:00Z",
    },
    {
      kind: "ci",
      attempted: 2370,
      created: 2367,
      failed: 3,
      finished: 2364,
      unaccounted: 3,
      hours: 1201,
      meanSeconds: 1828.9,
      since: "2026-09-05T04:08:00Z",
    },
  ],
};

describe("SandboxLifetimePanel", () => {
  it("says how many were built, what they cost and since when", () => {
    render(<SandboxLifetimePanel lifetime={lifetime} />);
    expect(
      screen.getByText(/2,413 sandboxes built, 1,204 h of machine time/),
    ).toBeInTheDocument();
    expect(screen.getByText(/since/)).toBeInTheDocument();
    // Per kind, because "what does CI cost against agents" is the
    // question behind the total.
    expect(screen.getByText(/^Agent — 46 built/)).toBeInTheDocument();
    expect(screen.getByText(/^CI — 2,367 built/)).toBeInTheDocument();
  });

  it("names the two things the figures cannot cover, and only when they happen", () => {
    // A sandbox asked for that never came up, and one that came up and
    // was never seen to stop -- the second being the time missing from
    // the hours beside it.
    expect(sandboxKindLine(lifetime.kinds[1])).toMatch(/3 never started/);
    expect(sandboxKindLine(lifetime.kinds[1])).toMatch(/3 not seen to stop/);
    // Neither is printed as a nought on the deployments where it is one.
    expect(sandboxKindLine(lifetime.kinds[0])).not.toMatch(/never started/);
    expect(sandboxKindLine(lifetime.kinds[0])).not.toMatch(/not seen to stop/);
  });

  it("calls a deployment that has counted nothing counted rather than broken", () => {
    render(<SandboxLifetimePanel lifetime={{ total: { attempted: 0 } }} />);
    expect(
      screen.getByText(/No sandbox has been built since/),
    ).toBeInTheDocument();
    expect(sandboxLifetimeSummary({ total: { attempted: 0 } })).toBe("");
  });

  it("draws nothing where there is no counter to read", () => {
    const { container } = render(<SandboxLifetimePanel lifetime={null} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("writes the numbers the way somebody quoting them reads them", () => {
    // Thousands separated, since these run to four figures within a
    // fortnight of one deployment's CI.
    expect(countOf(2413)).toBe("2,413");
    // A decimal place while it means something, and none once it does
    // not: "2.4 h" on the first afternoon, "1,204 h" a month later.
    expect(hoursOf(2.43)).toBe("2.4 h");
    expect(hoursOf(1204.3)).toBe("1,204 h");
    expect(hoursOf(0)).toBe("0.0 h");
  });
});
