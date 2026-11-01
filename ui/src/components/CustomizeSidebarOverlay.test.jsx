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
import userEvent from "@testing-library/user-event";
import { describe, expect, it, beforeEach, vi } from "vitest";
import CustomizeSidebarOverlay from "./CustomizeSidebarOverlay.jsx";
import { PinnedNavProvider } from "../PinnedNavContext.jsx";

const sessions = [
  { id: "director", title: "Director" },
  { id: "reviewer", title: "Code Reviewer" },
];

const boards = [
  { id: "board-1", name: "Roadmap" },
  { id: "board-2", name: "Sprint 42" },
];

function renderWithProviders(ui) {
  return render(<PinnedNavProvider>{ui}</PinnedNavProvider>);
}

describe("CustomizeSidebarOverlay", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("renders default pinned items", () => {
    renderWithProviders(
      <CustomizeSidebarOverlay
        open={true}
        onClose={vi.fn()}
        sessions={sessions}
        boards={boards}
      />,
    );

    expect(screen.getByText("Customize sidebar")).toBeInTheDocument();
    expect(screen.getByText("Inbox")).toBeInTheDocument();
    expect(screen.queryByText("Director")).not.toBeInTheDocument();
  });

  it("adds a view to pinned list and saves", async () => {
    const onSave = vi.fn();
    const onClose = vi.fn();

    renderWithProviders(
      <CustomizeSidebarOverlay
        open={true}
        onClose={onClose}
        onSave={onSave}
        sessions={sessions}
        boards={boards}
      />,
    );

    // Pick "CI" from view dropdown
    const itemSelect = screen.getByLabelText("Item");
    await userEvent.click(itemSelect);
    await userEvent.click(screen.getByRole("option", { name: "CI" }));

    // Click Pin item
    await userEvent.click(screen.getByRole("button", { name: /Pin item/ }));

    // Once as the CI section's toggle, once as the new pin.
    expect(screen.getAllByText("CI")).toHaveLength(2);

    // Click Save
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith([
      { kind: "view", id: "inbox" },
      { kind: "view", id: "ci" },
    ]);
    expect(onClose).toHaveBeenCalled();
  });

  it("offers Processes in place of Schedules, Templates and Suites", async () => {
    // design/design/settings-panel.design.md §5.6: the three views are one
    // Processes view, which pins as itself and has its own glyph.
    const onSave = vi.fn();

    renderWithProviders(
      <CustomizeSidebarOverlay
        open={true}
        onClose={vi.fn()}
        onSave={onSave}
        sessions={sessions}
        boards={boards}
      />,
    );

    await userEvent.click(screen.getByLabelText("Item"));
    for (const gone of ["Schedules", "Templates", "Suites"]) {
      expect(
        screen.queryByRole("option", { name: gone }),
      ).not.toBeInTheDocument();
    }
    await userEvent.click(screen.getByRole("option", { name: "Processes" }));

    await userEvent.click(screen.getByLabelText("Icon (optional)"));
    expect(
      screen.getByRole("option", {
        name: /Processes \(Loop through three nodes\)/,
      }),
    ).toBeInTheDocument();
    await userEvent.keyboard("{Escape}");

    await userEvent.click(screen.getByRole("button", { name: /Pin item/ }));
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith([
      { kind: "view", id: "inbox" },
      { kind: "view", id: "processes" },
    ]);
  });

  it("customizes icon and color for a pinned item and saves", async () => {
    const onSave = vi.fn();
    const onClose = vi.fn();

    renderWithProviders(
      <CustomizeSidebarOverlay
        open={true}
        onClose={onClose}
        onSave={onSave}
        sessions={sessions}
        boards={boards}
      />,
    );

    // Click customize button on Inbox
    const customizeInboxBtn = screen.getByRole("button", {
      name: "Customize Inbox",
    });
    await userEvent.click(customizeInboxBtn);

    // Select custom icon: "Tasks (Solid mass)"
    const iconSelect = screen.getByLabelText("Icon");
    await userEvent.click(iconSelect);
    await userEvent.click(screen.getByRole("option", { name: /Tasks/ }));

    // Select color: "Indigo"
    const indigoBtn = screen.getByRole("button", { name: "Color Indigo" });
    await userEvent.click(indigoBtn);

    // Save
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith([
      { kind: "view", id: "inbox", icon: "tasks", color: "#6366f1" },
    ]);
    const stored = JSON.parse(localStorage.getItem("grain.pinned"));
    expect(stored).toEqual([
      { kind: "view", id: "inbox", icon: "tasks", color: "#6366f1" },
    ]);
  });

  it("customizes item with custom hex color input", async () => {
    const onSave = vi.fn();

    renderWithProviders(
      <CustomizeSidebarOverlay
        open={true}
        onClose={vi.fn()}
        onSave={onSave}
        sessions={sessions}
        boards={boards}
      />,
    );

    // Customize Inbox
    await userEvent.click(
      screen.getByRole("button", { name: "Customize Inbox" }),
    );

    const hexInput = screen.getByLabelText("Custom Hex");
    await userEvent.clear(hexInput);
    await userEvent.type(hexInput, "#123456");

    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith([
      { kind: "view", id: "inbox", color: "#123456" },
    ]);
  });

  it("adds an item with custom icon and saves", async () => {
    const onSave = vi.fn();

    renderWithProviders(
      <CustomizeSidebarOverlay
        open={true}
        onClose={vi.fn()}
        onSave={onSave}
        sessions={sessions}
        boards={boards}
      />,
    );

    // Add Session "Director" with custom icon "Director (Portico)"
    const typeSelect = screen.getByLabelText("Type");
    await userEvent.click(typeSelect);
    await userEvent.click(screen.getByRole("option", { name: "Session" }));

    const itemSelect = screen.getByLabelText("Item");
    await userEvent.click(itemSelect);
    await userEvent.click(screen.getByRole("option", { name: "Director" }));

    const iconSelect = screen.getByLabelText("Icon (optional)");
    await userEvent.click(iconSelect);
    await userEvent.click(screen.getByRole("option", { name: /Director/ }));

    await userEvent.click(screen.getByRole("button", { name: /Pin item/ }));

    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    expect(onSave).toHaveBeenCalledWith([
      { kind: "view", id: "inbox" },
      { kind: "session", id: "director", icon: "director" },
    ]);
  });

  it("unpins an item and moves items up/down", async () => {
    renderWithProviders(
      <CustomizeSidebarOverlay
        open={true}
        onClose={vi.fn()}
        sessions={sessions}
        boards={boards}
      />,
    );

    // Unpin Inbox
    const unpinInbox = screen.getByRole("button", {
      name: "Unpin Inbox",
    });
    await userEvent.click(unpinInbox);
    expect(screen.queryByText("Inbox")).not.toBeInTheDocument();

    // Add a board
    const typeSelect = screen.getByLabelText("Type");
    await userEvent.click(typeSelect);
    await userEvent.click(screen.getByRole("option", { name: "Board" }));

    const itemSelect = screen.getByLabelText("Item");
    await userEvent.click(itemSelect);
    await userEvent.click(screen.getByRole("option", { name: "Sprint 42" }));

    await userEvent.click(screen.getByRole("button", { name: /Pin item/ }));
    expect(screen.getByText("Sprint 42")).toBeInTheDocument();

    // Add a session
    await userEvent.click(typeSelect);
    await userEvent.click(screen.getByRole("option", { name: "Session" }));
    const sessionSelect = screen.getByLabelText("Item");
    await userEvent.click(sessionSelect);
    await userEvent.click(screen.getByRole("option", { name: "Director" }));
    await userEvent.click(screen.getByRole("button", { name: /Pin item/ }));
    expect(screen.getByText("Director")).toBeInTheDocument();

    // Move Director up
    const moveUpButtons = screen.getAllByRole("button", { name: "Move up" });
    // Second item's move up is enabled
    await userEvent.click(moveUpButtons[1]);

    // Save
    await userEvent.click(screen.getByRole("button", { name: "Save" }));

    const stored = JSON.parse(localStorage.getItem("grain.pinned"));
    expect(stored).toEqual([
      { kind: "session", id: "director" },
      { kind: "board", id: "board-2" },
    ]);
  });

  it("prompts before discarding unsaved changes on Escape and keeps focus on arrow/remove buttons", async () => {
    const onClose = vi.fn();
    renderWithProviders(
      <CustomizeSidebarOverlay
        open={true}
        onClose={onClose}
        sessions={sessions}
        boards={boards}
      />,
    );

    // Pin a second item so the draft is dirty
    await userEvent.click(screen.getByLabelText("Item"));
    await userEvent.click(screen.getByRole("option", { name: "CI" }));
    await userEvent.click(screen.getByRole("button", { name: /Pin item/ }));

    // Moving the second item ("CI") up to index 0 disables its Move up button
    // and shifts focus to its Move down button instead of dropping to body.
    const moveUpButtons = screen.getAllByRole("button", { name: "Move up" });
    moveUpButtons[1].focus();
    await userEvent.click(moveUpButtons[1]);
    await new Promise((r) => requestAnimationFrame(r));
    const moveDownButtons = screen.getAllByRole("button", {
      name: "Move down",
    });
    expect(document.activeElement).toBe(moveDownButtons[0]);

    // Pressing Escape when dirty asks window.confirm before closing
    const confirmSpy = vi
      .spyOn(window, "confirm")
      .mockReturnValueOnce(false)
      .mockReturnValueOnce(true);
    try {
      await userEvent.keyboard("{Escape}");
      expect(confirmSpy).toHaveBeenCalledWith(
        "You have unsaved changes here. Leave and discard them?",
      );
      expect(onClose).not.toHaveBeenCalled();

      await userEvent.keyboard("{Escape}");
      expect(onClose).toHaveBeenCalled();
    } finally {
      confirmSpy.mockRestore();
    }
  });

  it("matches pinned System tabs and Settings sections by target so top-level System/Settings stay pinnable and duplicate keys are avoided", async () => {
    const errorSpy = vi.spyOn(console, "error").mockImplementation(() => {});
    const onSave = vi.fn();
    try {
      renderWithProviders(
        <CustomizeSidebarOverlay
          open={true}
          onClose={vi.fn()}
          onSave={onSave}
          sessions={sessions}
          boards={boards}
        />,
      );

      const itemSelect = screen.getByLabelText("Item");
      const pinButton = screen.getByRole("button", { name: /Pin item/ });

      // Pin "System → Sandboxes"
      await userEvent.click(itemSelect);
      await userEvent.click(
        screen.getByRole("option", { name: "System → Sandboxes" }),
      );
      expect(pinButton).toBeEnabled();
      await userEvent.click(pinButton);

      // Selecting "System → Sandboxes" again disables "Pin item"
      await userEvent.click(itemSelect);
      await userEvent.click(
        screen.getByRole("option", { name: "System → Sandboxes" }),
      );
      expect(pinButton).toBeDisabled();

      // Selecting top-level "System" remains enabled even though a System tab is pinned
      await userEvent.click(itemSelect);
      await userEvent.click(
        screen.getByRole("option", { name: "System", exact: true }),
      );
      expect(pinButton).toBeEnabled();
      await userEvent.click(pinButton);

      // Pinning a second System tab ("System → Caches") works without duplicate React key warnings
      await userEvent.click(itemSelect);
      await userEvent.click(
        screen.getByRole("option", { name: "System → Caches" }),
      );
      expect(pinButton).toBeEnabled();
      await userEvent.click(pinButton);

      const dupKeyErrors = errorSpy.mock.calls.filter((args) =>
        args.some((a) =>
          String(a).includes("Encountered two children with the same key"),
        ),
      );
      expect(dupKeyErrors).toEqual([]);

      await userEvent.click(screen.getByRole("button", { name: "Save" }));
      expect(onSave).toHaveBeenCalledWith([
        { kind: "view", id: "inbox" },
        { kind: "view", id: "system", tab: "sandboxes" },
        { kind: "view", id: "system" },
        { kind: "view", id: "system", tab: "caches" },
      ]);
    } finally {
      errorSpy.mockRestore();
    }
  });
});
