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

import { useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import {
  MoveHandle,
  MoveSlot,
  MovingBar,
  edgeScroll,
  gapsOf,
  nearestSlot,
} from "./ReorderPickup.jsx";

// The lists that use these pieces test the gesture end to end
// (TaskList.test.jsx, TaskBoard.test.jsx, SchedulesList.test.jsx); what
// is worth testing here is the arithmetic behind which slots a list is
// offered at all, which is the one part with cases the lists themselves
// would each have to re-derive.
describe("gapsOf", () => {
  it("offers every position except the one the row is already in", () => {
    // [a, b, c] with b up: above a, and at the end. Not between a and
    // c, which is where b already is.
    expect(gapsOf(["a", "b", "c"], ["b"])).toEqual([0, 2]);
  });

  it("offers only the far side for a row at either end", () => {
    expect(gapsOf(["a", "b"], ["a"])).toEqual([1]);
    expect(gapsOf(["a", "b"], ["b"])).toEqual([0]);
  });

  it("offers nothing at all for the only row there is", () => {
    expect(gapsOf(["a"], ["a"])).toEqual([]);
  });

  it("treats a contiguous block as one thing", () => {
    // [a, b, c, d] with b and c up: the rows left are [a, d], so the
    // positions are above a and after d. The gap between them is where
    // the block already sits.
    expect(gapsOf(["a", "b", "c", "d"], ["b", "c"])).toEqual([0, 2]);
  });

  it("offers every gap to a block that is not contiguous", () => {
    // [a, b, c] with a and c up: there is nowhere to put them that
    // leaves the list as it was, since b sits between them now.
    expect(gapsOf(["a", "b", "c"], ["a", "c"])).toEqual([0, 1]);
  });

  it("offers no gaps when none of the picked ids are in the list", () => {
    expect(gapsOf([], ["a"])).toEqual([]);
    expect(gapsOf(["a", "b"], ["c"])).toEqual([]);
  });
});

describe("MoveHandle", () => {
  it("says whether the row is picked up, and never lets the click reach the row", async () => {
    const onToggle = vi.fn();
    const onRowClick = vi.fn();
    const user = userEvent.setup();
    render(
      <div onClick={onRowClick}>
        <MoveHandle label="Move task 7" picked={false} onToggle={onToggle} />
      </div>,
    );

    const handle = screen.getByRole("button", { name: "Move task 7" });
    expect(handle).toHaveAttribute("aria-pressed", "false");

    await user.click(handle);
    expect(onToggle).toHaveBeenCalled();
    // Every list that carries a handle opens the thing the row names
    // when the row is clicked; picking it up is not that.
    expect(onRowClick).not.toHaveBeenCalled();
  });

  it("reads as pressed once the row is up", () => {
    render(<MoveHandle label="Stop moving task 7" picked onToggle={vi.fn()} />);
    expect(
      screen.getByRole("button", { name: "Stop moving task 7" }),
    ).toHaveAttribute("aria-pressed", "true");
  });

  // A keyboard user presses Enter on a "Move here" slot; the slot is
  // gone by the next render, and focus with it. Without this, focus fell
  // to <body> and the next Tab started from the top of the page.
  it("takes focus back when the row is put down from a slot that is now gone", async () => {
    const user = userEvent.setup();
    function List() {
      const [picked, setPicked] = useState(true);
      return (
        <ul>
          <li>
            <MoveHandle
              label="Move acme/widgets"
              picked={picked}
              onToggle={() => setPicked((p) => !p)}
            />
          </li>
          {picked && (
            <MoveSlot
              label="Move here, at the top"
              onMove={() => setPicked(false)}
            />
          )}
        </ul>
      );
    }
    render(<List />);
    screen.getByRole("button", { name: "Move here, at the top" }).focus();
    await user.keyboard("{Enter}");
    expect(
      screen.queryByRole("button", { name: "Move here, at the top" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Move acme/widgets" }),
    ).toHaveFocus();
  });

  it("leaves focus alone when it went somewhere else on the way down", () => {
    const { rerender } = render(
      <>
        <MoveHandle label="Move task 7" picked onToggle={vi.fn()} />
        <input aria-label="search" />
      </>,
    );
    screen.getByLabelText("search").focus();
    rerender(
      <>
        <MoveHandle label="Move task 7" picked={false} onToggle={vi.fn()} />
        <input aria-label="search" />
      </>,
    );
    expect(screen.getByLabelText("search")).toHaveFocus();
  });
});

describe("nearestSlot", () => {
  // Two slots a row apart, the way a list lays them out.
  const rects = [
    { left: 0, right: 200, top: 0, bottom: 40 },
    { left: 0, right: 200, top: 100, bottom: 140 },
  ];

  it("answers the slot the finger is inside", () => {
    expect(nearestSlot(rects, 100, 20)).toBe(0);
    expect(nearestSlot(rects, 100, 120)).toBe(1);
  });

  it("answers the nearer one for a finger in the gap between them", () => {
    expect(nearestSlot(rects, 100, 60)).toBe(0);
    expect(nearestSlot(rects, 100, 90)).toBe(1);
  });

  it("answers nothing at all for a finger nowhere near the list", () => {
    // Sideways, which is a board's other column or the page beside a
    // narrow list, and far below, which is the empty space under a short
    // one. Neither is a drop.
    expect(nearestSlot(rects, 900, 20)).toBe(-1);
    expect(nearestSlot(rects, 100, 900)).toBe(-1);
  });

  it("has nothing to answer with when the row was put down mid-drag", () => {
    expect(nearestSlot([], 100, 20)).toBe(-1);
  });
});

describe("edgeScroll", () => {
  // A 600px-tall list, with the default 72px bands at each end.
  it("leaves a finger in the middle of the list alone", () => {
    expect(edgeScroll(0, 600, 300)).toBe(0);
  });

  it("pulls the list up at the top and down at the bottom", () => {
    expect(edgeScroll(0, 600, 10)).toBeLessThan(0);
    expect(edgeScroll(0, 600, 590)).toBeGreaterThan(0);
  });

  it("ramps with how far into the band the finger is", () => {
    expect(Math.abs(edgeScroll(0, 600, 70))).toBeLessThan(
      Math.abs(edgeScroll(0, 600, 10)),
    );
  });

  it("goes no faster than its own speed past the edge", () => {
    // A finger dragged off the end of the screen entirely.
    expect(edgeScroll(0, 600, -200, 72, 16)).toBe(-16);
    expect(edgeScroll(0, 600, 800, 72, 16)).toBe(16);
  });

  it("never scrolls a box too short to have a middle", () => {
    // Every position in a 100px box is inside a band; a list that
    // scrolls wherever it is touched is worse than one that does not.
    expect(edgeScroll(0, 100, 10)).toBe(0);
    expect(edgeScroll(0, 100, 90)).toBe(0);
  });
});

// The gesture itself (grain/task-51): press the handle, drag, let go on
// a slot. jsdom has no PointerEvent and no layout, so the events are
// built by hand out of MouseEvent and the slots are told where they are
// -- which is enough, because everything this hook decides is decided
// from those coordinates. That it works against a real browser with a
// real finger is ui/e2e/phone.spec.js's half of the story.
describe("MoveHandle, dragged with a finger", () => {
  const pointer = (type, { x = 0, y = 0, id = 1, kind = "touch" } = {}) => {
    const e = new MouseEvent(type, {
      bubbles: true,
      cancelable: true,
      clientX: x,
      clientY: y,
    });
    Object.defineProperty(e, "pointerId", { value: id });
    Object.defineProperty(e, "pointerType", { value: kind });
    return e;
  };

  // A handle and the two slots a pick-up would have put on screen, each
  // told the rectangle it occupies.
  function bench({ picked = false } = {}) {
    const onToggle = vi.fn();
    const onMove = vi.fn();
    render(
      <div>
        <MoveHandle label="Move task 7" picked={picked} onToggle={onToggle} />
        <ul>
          <MoveSlot label="Move here, above 7" onMove={() => onMove("above")} />
          <MoveSlot
            label="Move here, at the end"
            onMove={() => onMove("end")}
          />
        </ul>
      </div>,
    );
    const slots = screen.getAllByRole("button", { name: /^Move here/ });
    const rects = [
      { left: 0, right: 200, top: 0, bottom: 40 },
      { left: 0, right: 200, top: 100, bottom: 140 },
    ];
    slots.forEach((el, i) => {
      el.getBoundingClientRect = () => rects[i];
    });
    return {
      handle: screen.getByRole("button", { name: "Move task 7" }),
      slots,
      onToggle,
      onMove,
    };
  }

  it("picks the row up on the way down, and drops it where the finger lifts", () => {
    const { handle, slots, onToggle, onMove } = bench();

    fireEvent(handle, pointer("pointerdown", { x: 10, y: 200 }));
    // On the press rather than on the release: the slots have to be on
    // screen before there is anything to drag towards.
    expect(onToggle).toHaveBeenCalledTimes(1);

    fireEvent(window, pointer("pointermove", { x: 60, y: 160 }));
    fireEvent(window, pointer("pointermove", { x: 100, y: 120 }));
    // The one it would land in says so, and only that one.
    expect(slots[1]).toHaveClass("reorder-slot-button-over");
    expect(slots[0]).not.toHaveClass("reorder-slot-button-over");

    fireEvent(window, pointer("pointerup", { x: 100, y: 120 }));
    expect(onMove).toHaveBeenCalledWith("end");
    // Nothing is left marked once the finger is off the screen.
    expect(slots[1]).not.toHaveClass("reorder-slot-button-over");
  });

  it("leaves a press that never travels as the tap it always was", () => {
    const { handle, onToggle, onMove } = bench();

    fireEvent(handle, pointer("pointerdown", { x: 10, y: 200 }));
    fireEvent(window, pointer("pointermove", { x: 12, y: 202 }));
    fireEvent(window, pointer("pointerup", { x: 12, y: 202 }));

    // Picked up and left up, waiting for a slot to be tapped -- and the
    // click the browser sends after a touch does not pick it up again.
    fireEvent.click(handle);
    expect(onToggle).toHaveBeenCalledTimes(1);
    expect(onMove).not.toHaveBeenCalled();
  });

  it("puts a row that was already up back down when it is tapped again", () => {
    const { handle, onToggle } = bench({ picked: true });

    fireEvent(handle, pointer("pointerdown", { x: 10, y: 200 }));
    expect(onToggle).not.toHaveBeenCalled();
    fireEvent(window, pointer("pointerup", { x: 10, y: 200 }));
    expect(onToggle).toHaveBeenCalledTimes(1);
  });

  it("moves nothing when the finger lifts away from every slot", () => {
    const { handle, onToggle, onMove } = bench();

    fireEvent(handle, pointer("pointerdown", { x: 10, y: 200 }));
    fireEvent(window, pointer("pointermove", { x: 900, y: 900 }));
    fireEvent(window, pointer("pointerup", { x: 900, y: 900 }));

    // The row stays up rather than landing somewhere nobody aimed at:
    // the bar and its slots are still there to tap.
    expect(onMove).not.toHaveBeenCalled();
    expect(onToggle).toHaveBeenCalledTimes(1);
  });

  it("drops nothing when the system takes the finger away", () => {
    const { handle, slots, onMove } = bench();

    fireEvent(handle, pointer("pointerdown", { x: 10, y: 200 }));
    fireEvent(window, pointer("pointermove", { x: 100, y: 120 }));
    // A call, a notification, the browser deciding this is a page
    // gesture after all.
    fireEvent(window, pointer("pointercancel", { x: 100, y: 120 }));

    expect(onMove).not.toHaveBeenCalled();
    expect(slots[1]).not.toHaveClass("reorder-slot-button-over");
  });

  it("refuses the row's own drag while a finger is on the handle", () => {
    const { handle } = bench();

    fireEvent(handle, pointer("pointerdown", { x: 10, y: 200 }));
    const drag = new MouseEvent("dragstart", {
      bubbles: true,
      cancelable: true,
    });
    fireEvent(handle, drag);
    // iPadOS starts a native drag of the <li> from a long press, which
    // would be the browser and this gesture fighting over one finger.
    expect(drag.defaultPrevented).toBe(true);
  });

  it("leaves a mouse to the drag it already had", () => {
    const { handle, onToggle } = bench();

    // No pick-up on the press: a mouse press is the start of the HTML5
    // drag, and the click that follows it is what picks a row up.
    fireEvent(handle, pointer("pointerdown", { x: 10, y: 200, kind: "mouse" }));
    expect(onToggle).not.toHaveBeenCalled();
    fireEvent.click(handle);
    expect(onToggle).toHaveBeenCalledTimes(1);
  });

  it("does nothing at all on a handle with nowhere to go", () => {
    const onToggle = vi.fn();
    render(<MoveHandle label="Move task 7" disabled onToggle={onToggle} />);
    const handle = screen.getByRole("button", { name: "Move task 7" });

    fireEvent(handle, pointer("pointerdown", { x: 10, y: 200 }));
    fireEvent(window, pointer("pointerup", { x: 10, y: 200 }));
    expect(onToggle).not.toHaveBeenCalled();
  });
});

describe("MovingBar", () => {
  it("counts what is up, in the caller's own noun", () => {
    render(<MovingBar count={1} noun="schedule" onCancel={vi.fn()} />);
    expect(screen.getByRole("status")).toHaveTextContent(
      "Moving 1 schedule — choose where it goes",
    );
  });

  it("says it in the plural for a block", () => {
    render(<MovingBar count={3} onCancel={vi.fn()} />);
    expect(screen.getByRole("status")).toHaveTextContent(
      "Moving 3 tasks — choose where they go",
    );
  });
});
