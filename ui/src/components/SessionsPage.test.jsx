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

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import api from "../api.js";
import SessionsPage from "./SessionsPage.jsx";

vi.mock("../api.js", () => ({ default: vi.fn(async () => ({})) }));

describe("SessionsPage", () => {
  const sessions = [
    { id: "director", title: "Director", team: "default" },
    { id: "s-1", title: "Support Bot", team: "ops" },
  ];

  afterEach(() => {
    vi.clearAllMocks();
    vi.unstubAllGlobals();
  });

  it("renders the list of sessions and handles clicks", async () => {
    const onOpen = vi.fn();
    const onNew = vi.fn();
    const user = userEvent.setup();

    render(
      <SessionsPage
        sessions={sessions}
        onOpenSession={onOpen}
        onNewSession={onNew}
      />,
    );

    expect(
      screen.getByRole("heading", { name: /sessions/i }),
    ).toBeInTheDocument();
    expect(screen.getByText("Director")).toBeInTheDocument();
    expect(screen.getByText("Support Bot")).toBeInTheDocument();

    await user.click(screen.getByText("Support Bot"));
    expect(onOpen).toHaveBeenCalledWith("s-1");

    await user.click(screen.getByRole("button", { name: "+ New session" }));
    expect(onNew).toHaveBeenCalled();
  });

  it("filters sessions using search bar", async () => {
    const user = userEvent.setup();

    render(<SessionsPage sessions={sessions} />);

    const searchInput = screen.getByPlaceholderText("Search sessions…");
    await user.type(searchInput, "Support");

    expect(screen.getByText("Support Bot")).toBeInTheDocument();
    expect(screen.queryByText("Director")).not.toBeInTheDocument();
  });

  it("opens a session from the keyboard alone", async () => {
    const onOpen = vi.fn();
    const user = userEvent.setup();
    render(<SessionsPage sessions={sessions} onOpenSession={onOpen} />);

    const row = screen.getByRole("button", {
      name: "Open session Support Bot",
    });
    row.focus();
    await user.keyboard("{Enter}");
    expect(onOpen).toHaveBeenCalledWith("s-1");

    screen.getByRole("button", { name: "Open session Director" }).focus();
    await user.keyboard(" ");
    expect(onOpen).toHaveBeenLastCalledWith("director");
  });

  // The 2026-10-26 walkthrough's axe pass: the row was an <li
  // role="button"> with the delete button inside it -- a control nested
  // in a control, and a list whose items were not list items.
  it("keeps each row a list item whose open and delete controls sit side by side", async () => {
    const onOpen = vi.fn();
    const user = userEvent.setup();
    render(
      <SessionsPage
        sessions={[sessions[0], { ...sessions[1], taskId: "7" }]}
        tasks={[{ id: "7", state: "awaiting_reply" }]}
        onOpenSession={onOpen}
        onDeleted={vi.fn()}
      />,
    );
    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(2);
    for (const button of screen.getAllByRole("button")) {
      expect(button.parentElement.closest("button, [role=button]")).toBe(null);
    }
    const open = screen.getByRole("button", {
      name: "Open session Support Bot",
    });
    expect(open.tagName).toBe("BUTTON");
    expect(open).toHaveAccessibleDescription("Asked");
    // The rest of the row still opens it for a pointer.
    await user.click(screen.getByText("Asked"));
    expect(onOpen).toHaveBeenCalledWith("s-1");
  });

  it("says when a session asked for by id does not exist", () => {
    const { rerender } = render(<SessionsPage sessions={sessions} />);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();

    rerender(<SessionsPage sessions={sessions} missingSessionId="gone" />);
    expect(screen.getByRole("alert")).toHaveTextContent(
      /no session .gone. here/i,
    );
  });

  it("shows empty state when no sessions exist", () => {
    render(<SessionsPage sessions={[]} />);
    expect(
      screen.getByText("No interactive sessions yet."),
    ).toBeInTheDocument();
  });

  it("says whether each session is working or waiting on you", () => {
    render(
      <SessionsPage
        sessions={[
          { ...sessions[0], taskId: "7" },
          { ...sessions[1], taskId: "8" },
        ]}
        tasks={[
          { id: "7", state: "awaiting_reply" },
          { id: "8", state: "running" },
        ]}
      />,
    );
    expect(screen.getByText("Asked")).toBeInTheDocument();
    expect(screen.getByText("Working")).toBeInTheDocument();
  });

  // grain/task-15: a session is deleted from the list, not only from
  // inside its own pane.
  describe("deleting from the list", () => {
    it("offers no delete control without a handler to report it to", () => {
      render(<SessionsPage sessions={sessions} />);
      expect(
        screen.queryByRole("button", { name: /^Delete session/ }),
      ).not.toBeInTheDocument();
    });

    it("deletes the row's session once confirmed, without opening it", async () => {
      const confirm = vi.fn(() => true);
      vi.stubGlobal("confirm", confirm);
      const onOpen = vi.fn();
      const onDeleted = vi.fn(async () => {});
      const user = userEvent.setup();

      render(
        <SessionsPage
          sessions={sessions}
          onOpenSession={onOpen}
          onDeleted={onDeleted}
        />,
      );

      await user.click(
        screen.getByRole("button", { name: "Delete session Support Bot" }),
      );

      expect(confirm).toHaveBeenCalledWith(
        expect.stringContaining('Delete session "Support Bot"?'),
      );
      await waitFor(() =>
        expect(api).toHaveBeenCalledWith("/api/console-sessions/s-1", {
          method: "DELETE",
        }),
      );
      await waitFor(() => expect(onDeleted).toHaveBeenCalledWith("s-1"));
      // The click on the bin stopped at the bin: the row under it did
      // not open the session the reader just deleted.
      expect(onOpen).not.toHaveBeenCalled();
    });

    it("keeps the session when the confirmation is dismissed", async () => {
      vi.stubGlobal(
        "confirm",
        vi.fn(() => false),
      );
      const onDeleted = vi.fn(async () => {});
      const user = userEvent.setup();

      render(<SessionsPage sessions={sessions} onDeleted={onDeleted} />);
      await user.click(
        screen.getByRole("button", { name: "Delete session Director" }),
      );

      expect(api).not.toHaveBeenCalled();
      expect(onDeleted).not.toHaveBeenCalled();
      expect(screen.getByText("Director")).toBeInTheDocument();
    });

    it("reports a failed delete and leaves the row usable", async () => {
      vi.stubGlobal(
        "confirm",
        vi.fn(() => true),
      );
      api.mockRejectedValueOnce(new Error("boom"));
      const onDeleted = vi.fn(async () => {});
      const showError = vi.fn();
      const user = userEvent.setup();

      render(
        <SessionsPage
          sessions={sessions}
          onDeleted={onDeleted}
          showError={showError}
        />,
      );
      const bin = screen.getByRole("button", {
        name: "Delete session Support Bot",
      });
      await user.click(bin);

      await waitFor(() => expect(showError).toHaveBeenCalled());
      expect(showError.mock.calls[0][0].message).toBe("boom");
      expect(onDeleted).not.toHaveBeenCalled();
      await waitFor(() => expect(bin).toBeEnabled());
    });

    it("deletes from the keyboard without opening the row", async () => {
      vi.stubGlobal(
        "confirm",
        vi.fn(() => true),
      );
      const onOpen = vi.fn();
      const onDeleted = vi.fn(async () => {});
      const user = userEvent.setup();

      render(
        <SessionsPage
          sessions={sessions}
          onOpenSession={onOpen}
          onDeleted={onDeleted}
        />,
      );
      screen
        .getByRole("button", { name: "Delete session Support Bot" })
        .focus();
      await user.keyboard("{Enter}");

      await waitFor(() => expect(onDeleted).toHaveBeenCalledWith("s-1"));
      expect(onOpen).not.toHaveBeenCalled();
    });
  });
});
