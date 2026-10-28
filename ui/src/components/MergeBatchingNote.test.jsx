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
import MergeBatchingNote from "./MergeBatchingNote.jsx";

// The note under the merge batch size: what the batches that size has
// produced actually cost. The cases that matter are the two readings a
// person changes the number on -- batches paying off, and batches
// failing -- plus the one that says the number is beside the point.
const batching = {
  batches: 12,
  changes: 27,
  fresh: 11,
  firstTry: 9,
  split: 1,
  culprits: 1,
  abandoned: 1,
  biggest: 3,
  atLimit: 5,
  suites: 24,
  serialSuites: 27,
  saved: 3,
  firstAt: "2026-09-18T12:00:00Z",
  lastAt: "2026-09-19T09:00:00Z",
};

describe("MergeBatchingNote", () => {
  it("says how the bet has gone and what it has saved at most", () => {
    render(<MergeBatchingNote batching={batching} limit={3} />);

    expect(
      screen.getByText(/9 of 11 formed from the queue passed first time/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/2 failed and had to be bisected/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/27 changes landed on 24 suites/),
    ).toBeInTheDocument();
    expect(screen.getByText(/3 suites saved at most/)).toBeInTheDocument();
    // At most, because a post-merge run judges every merge that lands
    // while it is going: the serial side of the comparison is tip moves
    // rather than suites (grain/task-501).
    expect(screen.getByText(/would have spent at most 27/)).toBeInTheDocument();
    expect(
      screen.getByText(/5 batches took every member the limit of 3 allowed/),
    ).toBeInTheDocument();
  });

  it("says plainly when batching has cost more than it saved", () => {
    render(
      <MergeBatchingNote
        batching={{
          ...batching,
          changes: 2,
          suites: 7,
          serialSuites: 2,
          saved: -5,
        }}
        limit={3}
      />,
    );

    expect(
      screen.getByText(/5 suites more than the queue would have spent/),
    ).toBeInTheDocument();
  });

  // A deployment that does not test its base branches after a merge has
  // nothing for a batch to save: saying so is not the same as reporting a
  // loss, which is what a bare negative number would look like.
  it("says when there was no post-merge suite to save in the first place", () => {
    render(
      <MergeBatchingNote
        batching={{ ...batching, suites: 12, serialSuites: 0, saved: -12 }}
        limit={3}
      />,
    );

    expect(
      screen.getByText(/Nothing here tests a base branch after a merge/),
    ).toBeInTheDocument();
  });

  // The commonest reading of all on a quiet deployment, and the one that
  // stops somebody tuning a number that is not binding anything.
  it("says when no batch has ever reached the limit", () => {
    render(
      <MergeBatchingNote
        batching={{ ...batching, atLimit: 0, biggest: 2 }}
        limit={3}
      />,
    );

    expect(
      screen.getByText(/No batch has yet been held down by the limit of 3/),
    ).toBeInTheDocument();
  });

  // Nothing at all before the first batch has finished: zeroes drawn as a
  // rate would read as "every batch failed".
  it("shows nothing on a deployment that has never batched", () => {
    const { container } = render(
      <MergeBatchingNote batching={null} limit={3} />,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
