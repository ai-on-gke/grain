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
import MergePolicy from "./MergePolicy.jsx";

const deploy = {
  Mode: "both",
  GrainFullSuite: true,
  GitHubChecks: ["build", "cla/google"],
  Matched: ["deploy"],
  Head: "0123456789abcdef",
  Requirements: [
    { Field: "ci.mode", Value: "grain", From: ["repository setting"] },
    { Field: "auto_merge", Value: "never", From: ["deploy"] },
    { Field: "stall_timeout", Value: "30m", From: ["deploy"] },
  ],
};

describe("MergePolicy", () => {
  it("lists each requirement with the rule behind it", () => {
    render(<MergePolicy t={{ state: "completed", mergePolicy: deploy }} />);
    const lines = screen
      .getAllByTestId("merge-requirement")
      .map((li) => li.textContent);
    expect(lines).toEqual([
      "ci.mode: grain — from repository setting",
      "auto_merge: never — from rule deploy",
      "stall_timeout: 30m — from rule deploy",
    ]);
    expect(screen.getByTestId("merge-policy")).toHaveTextContent(
      "GitHub CI is narrowed to build, cla/google",
    );
    expect(screen.getByTestId("merge-policy")).toHaveTextContent(
      "(matched deploy) for 0123456789ab",
    );
  });

  it("draws nothing without rules", () => {
    const { container } = render(<MergePolicy t={{ state: "completed" }} />);
    expect(container).toBeEmptyDOMElement();
  });
});
