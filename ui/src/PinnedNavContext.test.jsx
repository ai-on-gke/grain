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

import React from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, beforeEach } from "vitest";
import { PinnedNavProvider, usePinnedNav } from "./PinnedNavContext.jsx";

function TestConsumer() {
  const {
    pinnedItems,
    addPinnedItem,
    removePinnedItem,
    movePinnedItem,
    resetToDefault,
  } = usePinnedNav();

  return (
    <div>
      <ul data-testid="pinned-list">
        {pinnedItems.map((item, idx) => (
          <li key={`${item.kind}-${item.id}-${idx}`}>
            {item.kind}:{item.id}
          </li>
        ))}
      </ul>
      <button onClick={() => addPinnedItem({ kind: "view", id: "ci" })}>
        Pin CI
      </button>
      <button onClick={() => removePinnedItem(0)}>Remove First</button>
      <button onClick={() => movePinnedItem(1, 0)}>Move Second to First</button>
      <button onClick={resetToDefault}>Reset</button>
    </div>
  );
}

describe("PinnedNavContext", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("provides default pinned items", () => {
    render(
      <PinnedNavProvider>
        <TestConsumer />
      </PinnedNavProvider>,
    );

    const items = screen.getByTestId("pinned-list").querySelectorAll("li");
    expect(items).toHaveLength(1);
    expect(items[0]).toHaveTextContent("view:inbox");
  });

  it("adds and persists new pinned items", async () => {
    render(
      <PinnedNavProvider>
        <TestConsumer />
      </PinnedNavProvider>,
    );

    await userEvent.click(screen.getByRole("button", { name: "Pin CI" }));
    const items = screen.getByTestId("pinned-list").querySelectorAll("li");
    expect(items).toHaveLength(2);
    expect(items[1]).toHaveTextContent("view:ci");

    // One deployment-wide list, with no user or team in its key (§6.3).
    expect(localStorage.getItem("grain.pinned")).toContain('"id":"ci"');
  });

  it("removes, reorders, and resets pinned items", async () => {
    render(
      <PinnedNavProvider>
        <TestConsumer />
      </PinnedNavProvider>,
    );

    // Remove first (inbox)
    await userEvent.click(screen.getByRole("button", { name: "Remove First" }));
    let items = screen.getByTestId("pinned-list").querySelectorAll("li");
    expect(items).toHaveLength(0);

    // Pin CI
    await userEvent.click(screen.getByRole("button", { name: "Pin CI" }));
    items = screen.getByTestId("pinned-list").querySelectorAll("li");
    expect(items).toHaveLength(1);
    expect(items[0]).toHaveTextContent("view:ci");

    // Reset
    await userEvent.click(screen.getByRole("button", { name: "Reset" }));
    items = screen.getByTestId("pinned-list").querySelectorAll("li");
    expect(items).toHaveLength(1);
    expect(items[0]).toHaveTextContent("view:inbox");
  });
});
