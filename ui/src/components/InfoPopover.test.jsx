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

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import InfoPopover from "./InfoPopover.jsx";

describe("InfoPopover", () => {
  // ButtonBase clicks a non-native button on Enter's keydown and on
  // Space's keyup; a key handler that toggled as well undid it, so the
  // keyboard could close a popover but never open one.
  it("toggles once per Enter and per Space, and closes on Escape", async () => {
    const parentKey = vi.fn();
    render(
      <div onKeyDown={parentKey}>
        <InfoPopover title="What the method does." label="cloud-key" />
      </div>,
    );
    const button = screen.getByRole("button", { name: "About cloud-key" });
    const open = () => screen.queryByRole("tooltip") !== null;
    const closed = () => waitFor(() => expect(open()).toBe(false));

    fireEvent.keyDown(button, { key: "Enter" });
    expect(open()).toBe(true);
    fireEvent.keyDown(button, { key: "Enter" });
    await closed();

    fireEvent.keyDown(button, { key: " " });
    fireEvent.keyUp(button, { key: " " });
    expect(open()).toBe(true);
    fireEvent.keyDown(button, { key: "Escape" });
    await closed();

    expect(parentKey).toHaveBeenCalledTimes(1); // Escape only
  });

  it("opens on focus and closes on blur", async () => {
    render(<InfoPopover title="What the field means." label="project" />);
    const button = screen.getByRole("button", { name: "About project" });
    fireEvent.focus(button);
    expect(screen.getByRole("tooltip")).toHaveTextContent(
      "What the field means.",
    );
    fireEvent.blur(button);
    await waitFor(() => expect(screen.queryByRole("tooltip")).toBeNull());
  });
});
