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
import { MergesSection } from "./MetricsSections.jsx";

const merges = (landedByForge) => ({
  landed: 3,
  landedPerHour: 3 / 168,
  landedPerDay: 3 / 7,
  landedByForge,
  batches: { batches: 0 },
  repairs: { passes: 0, runs: 0 },
});

describe("MergesSection landings by forge", () => {
  it("splits landings by forge once a CL has landed", () => {
    render(
      <MergesSection report={{ merges: merges({ github: 2, demo: 1 }) }} />,
    );
    const row = screen.getByTestId("merges-by-forge");
    expect(row.textContent).toContain("github: 2 landed");
    expect(row.textContent).toContain("demo: 1 landed");
    // Most landings first.
    expect(row.textContent.indexOf("github")).toBeLessThan(
      row.textContent.indexOf("demo"),
    );
  });

  it("shows no split for a deployment that only merges pull requests", () => {
    render(<MergesSection report={{ merges: merges({ github: 3 }) }} />);
    expect(screen.queryByTestId("merges-by-forge")).toBeNull();
  });
});
