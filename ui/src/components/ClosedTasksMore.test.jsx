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

import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ClosedTasksMore from "./ClosedTasksMore.jsx";

// The end of a task view that can show closed work (grain task 3051):
// scrolling to it asks for the next page, the button still does, and a
// view that cannot show closed work draws nothing at all.

let observers;
class FakeObserver {
  constructor(callback, options) {
    this.callback = callback;
    this.options = options;
    this.observed = [];
    observers.push(this);
  }
  observe(el) {
    this.observed.push(el);
  }
  disconnect() {
    this.disconnected = true;
  }
  // scrollTo is the sentinel coming into (or near) the viewport.
  scrollTo() {
    this.callback(
      this.observed.map((target) => ({ target, isIntersecting: true })),
    );
  }
}

const pager = (over = {}) => ({
  hasMore: true,
  loading: false,
  searching: false,
  loadMore: vi.fn(),
  ...over,
});

describe("ClosedTasksMore", () => {
  beforeEach(() => {
    observers = [];
    vi.stubGlobal("IntersectionObserver", FakeObserver);
  });
  afterEach(() => vi.unstubAllGlobals());

  it("asks for the next page when the end of the list scrolls into view", () => {
    const closed = pager();
    render(<ClosedTasksMore closed={closed} />);
    expect(observers).toHaveLength(1);
    expect(observers[0].options.rootMargin).toMatch(/px/);
    act(() => observers[0].scrollTo());
    expect(closed.loadMore).toHaveBeenCalledTimes(1);
  });

  it("watches nothing while a page is on its way, and again once it lands", () => {
    const closed = pager({ loading: true });
    const { rerender } = render(<ClosedTasksMore closed={closed} />);
    expect(observers).toHaveLength(0);
    expect(
      screen.getByRole("button", { name: "Loading closed tasks…" }),
    ).toBeDisabled();
    rerender(<ClosedTasksMore closed={{ ...closed, loading: false }} />);
    expect(observers).toHaveLength(1);
  });

  it("keeps the button for a reader who would rather press it", async () => {
    const closed = pager({ searching: true });
    render(<ClosedTasksMore closed={closed} />);
    await userEvent.click(
      screen.getByRole("button", { name: "Search older closed tasks" }),
    );
    expect(closed.loadMore).toHaveBeenCalled();
  });

  it("draws nothing for a view that cannot show closed work, or once every page is in", () => {
    const { container, rerender } = render(
      <ClosedTasksMore closed={pager()} active={false} />,
    );
    expect(container).toBeEmptyDOMElement();
    expect(observers).toHaveLength(0);
    rerender(<ClosedTasksMore closed={pager({ hasMore: false })} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("says it is searching while a search's first page is on its way", () => {
    render(
      <ClosedTasksMore
        closed={pager({ hasMore: false, loading: true, searching: true })}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent(
      "Searching closed tasks…",
    );
  });
});
