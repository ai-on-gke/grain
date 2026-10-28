import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import MergeGatheringNote from "./MergeGatheringNote.jsx";

// The note under the merge gather window: what the waits that window
// allows have actually done. The cases that matter are the reading a
// person shortens the window on -- holds sitting out the whole five
// minutes for nothing -- and the one that says the window is not what is
// bounding anything.
const gathering = {
  holds: 12,
  batched: 4,
  released: 5,
  expired: 3,
  batchedWaitSeconds: 47,
  expiredWaitSeconds: 302,
  expiredTotalSeconds: 906,
  windowSeconds: 300,
  firstAt: "2026-09-20T12:00:00Z",
  lastAt: "2026-09-21T09:00:00Z",
};

describe("MergeGatheringNote", () => {
  it("says how the waits ended and what the expiries cost", () => {
    render(<MergeGatheringNote gathering={gathering} />);

    expect(screen.getByText(/12 holds since/)).toBeInTheDocument();
    expect(screen.getByText(/4 ended in a batch/)).toBeInTheDocument();
    expect(
      screen.getByText(/3 ran the full window and expired/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/A wait that paid off took 47s on average/),
    ).toBeInTheDocument();
    // The line the window is tuned on: what the waits that bought
    // nothing cost, each and altogether.
    expect(
      screen.getByText(/The 3 holds that expired waited 5m 2s each/),
    ).toBeInTheDocument();
    expect(screen.getByText(/15m 6s of merge latency in all/)).toBeVisible();
  });

  // A window nothing has ever run out is not one that is bounding
  // anything, whatever the holds are doing -- the same reading as "no
  // batch has ever reached the limit" next door.
  it("says when no hold has ever run its whole window", () => {
    render(
      <MergeGatheringNote
        gathering={{ ...gathering, expired: 0, expiredTotalSeconds: 0 }}
      />,
    );

    expect(
      screen.getByText(/No hold has yet run its whole window/),
    ).toBeInTheDocument();
  });

  // Waits that have only ever cost: said as what was not measured
  // rather than as a mean over no samples.
  it("says when no wait has yet ended in a batch", () => {
    render(
      <MergeGatheringNote
        gathering={{ ...gathering, batched: 0, batchedWaitSeconds: 0 }}
      />,
    );

    expect(
      screen.getByText(/No wait here has yet ended in a batch/),
    ).toBeInTheDocument();
  });

  // Nothing at all before the first hold has ended, which includes every
  // deployment with the wait turned off.
  it("shows nothing on a deployment whose queue has never held anything", () => {
    const { container } = render(<MergeGatheringNote gathering={null} />);
    expect(container).toBeEmptyDOMElement();
  });
});
