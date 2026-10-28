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
