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

import { describe, it, expect } from "vitest";
import {
  overrideReviewButtonLabel,
  reviewGates,
  unsatisfiedReviewGates,
} from "./MergeGatesList.jsx";

// The server's merge gates name a targeted review (a sensitive trigger's
// review with no template of its own) apart from the code review
// (model.GateTargetedReview).
describe("targeted review gate", () => {
  const t = {
    id: "1",
    pullRequest: "acme/widgets#7",
    mergeGates: [
      { id: "ci", label: "CI", state: "passed" },
      { id: "review", label: "Code review", state: "passed" },
      {
        id: "targeted-review",
        label: "Targeted review",
        state: "running",
        reviewTask: "9",
      },
    ],
  };

  it("is a review gate, and the one a force merge would skip", () => {
    expect(reviewGates(t).map((g) => g.label)).toEqual([
      "Code review",
      "Targeted review",
    ]);
    const skipped = unsatisfiedReviewGates(t);
    expect(skipped.map((g) => g.name)).toEqual(["targeted-review"]);
    expect(overrideReviewButtonLabel(skipped)).toBe(
      "Merge without the targeted review",
    );
  });

  it("names every review a force merge skips", () => {
    expect(
      overrideReviewButtonLabel([
        { name: "review" },
        { name: "targeted-review" },
      ]),
    ).toBe("Merge without the reviews");
    expect(overrideReviewButtonLabel([{ name: "review" }])).toBe(
      "Merge without the code review",
    );
  });
});
