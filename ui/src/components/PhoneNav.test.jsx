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

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useState } from "react";
import { Button, List, ListItemButton, Menu, MenuItem } from "@mui/material";
import PhoneNav from "./PhoneNav.jsx";

// A rail with the real one's Create options split button: a trigger
// that opens a menu anchored to itself.
function MenuRail({ onPick }) {
  const [anchor, setAnchor] = useState(null);
  return (
    <div>
      <Button
        aria-label="Create options"
        aria-haspopup="true"
        onClick={(e) => setAnchor(e.currentTarget)}
      >
        v
      </Button>
      <Menu
        open={Boolean(anchor)}
        anchorEl={anchor}
        onClose={() => setAnchor(null)}
      >
        <MenuItem
          onClick={() => {
            setAnchor(null);
            onPick();
          }}
        >
          New session
        </MenuItem>
      </Menu>
    </div>
  );
}

// The rail stands in for the real Sidebar: what PhoneNav does with it is
// the same whatever it holds -- it is handed one element, keeps it out of
// the way until the menu is tapped, and puts it away again on the way out.
function Rail({ onNavigate = () => {} }) {
  return (
    <div>
      <button onClick={onNavigate}>Board</button>
    </div>
  );
}

describe("PhoneNav", () => {
  it("keeps the rail off the screen until the menu is tapped", async () => {
    render(
      <PhoneNav config={{}} running={false} onOpenNewTask={() => {}}>
        <Rail />
      </PhoneNav>,
    );

    // The bar is what is on screen; the rail's own entries are not.
    expect(screen.getByRole("heading", { name: "grain" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Board" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Open navigation" }));

    expect(
      await screen.findByRole("button", { name: "Board" }),
    ).toBeInTheDocument();
  });

  // The open drawer is a modal over the page, and says so: a dialog
  // named Navigation, holding the rail. Before this its name sat on
  // MUI's role="presentation" Modal root, where nothing reads it.
  it("names the open drawer as a Navigation dialog", async () => {
    render(
      <PhoneNav config={{}} running={false} onOpenNewTask={() => {}}>
        <Rail />
      </PhoneNav>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Open navigation" }));

    const drawer = await screen.findByRole("dialog", {
      name: "Navigation",
      exact: true,
    });
    expect(drawer).toHaveAttribute("aria-modal", "true");
    expect(drawer).toContainElement(
      screen.getByRole("button", { name: "Board" }),
    );
  });

  // A tap on the rail's menu trigger opens its menu rather than closing
  // the drawer out from under it, and picking from the menu -- by tap or
  // by Enter, which MenuItem turns into onClick with no click event --
  // closes the drawer as any other choice does (2026-10-23 responsive
  // walkthrough, bug 3).
  it.each([["click"], ["Enter"]])(
    "opens the rail's create menu and closes on a pick by %s",
    async (how) => {
      const onPick = vi.fn();
      render(
        <PhoneNav config={{}} running={false} onOpenNewTask={() => {}}>
          <MenuRail onPick={onPick} />
        </PhoneNav>,
      );

      fireEvent.click(screen.getByRole("button", { name: "Open navigation" }));
      fireEvent.click(
        await screen.findByRole("button", { name: "Create options" }),
      );
      // Past the drawer's own exit transition (225ms): closing the drawer
      // on this tap unmounted the menu's anchor once that had run.
      await new Promise((resolve) => setTimeout(resolve, 500));
      const item = await screen.findByRole("menuitem", { name: "New session" });
      // The drawer is still open under the menu (which, as a modal of
      // its own, hides it from the accessibility tree meanwhile).
      expect(
        screen.getByRole("button", { name: "Create options", hidden: true }),
      ).toBeInTheDocument();

      if (how === "click") fireEvent.click(item);
      else fireEvent.keyDown(item, { key: "Enter" });

      expect(onPick).toHaveBeenCalledTimes(1);
      await waitFor(() =>
        expect(
          screen.queryByRole("button", {
            name: "Create options",
            hidden: true,
          }),
        ).toBeNull(),
      );
    },
  );

  // Every control in the rail is a navigation, so the drawer closes on
  // any tap that lands inside it rather than staying open over the page
  // it just navigated to.
  it("closes the drawer when a nav entry inside it is tapped", async () => {
    const onNavigate = vi.fn();
    render(
      <PhoneNav config={{}} running={false} onOpenNewTask={() => {}}>
        <Rail onNavigate={onNavigate} />
      </PhoneNav>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Open navigation" }));
    fireEvent.click(await screen.findByRole("button", { name: "Board" }));

    // The entry's own handler still ran: closing the drawer is in
    // addition to navigating, not instead of it.
    expect(onNavigate).toHaveBeenCalled();
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Board" })).toBeNull(),
    );
  });

  // The real rail's entries are ListItemButtons, which ButtonBase
  // activates from the keyboard by calling onClick directly -- no click
  // event -- so a keyboard pick navigated and left the drawer open over
  // the page (grain/task-1630).
  it.each([
    ["Enter", "keyDown"],
    [" ", "keyUp"],
  ])(
    "closes the drawer when a nav entry is chosen with %j",
    async (key, phase) => {
      const onNavigate = vi.fn();
      render(
        <PhoneNav config={{}} running={false} onOpenNewTask={() => {}}>
          <List>
            <ListItemButton onClick={onNavigate}>Schedules</ListItemButton>
          </List>
        </PhoneNav>,
      );

      fireEvent.click(screen.getByRole("button", { name: "Open navigation" }));
      const entry = await screen.findByRole("button", { name: "Schedules" });
      if (phase === "keyUp") fireEvent.keyDown(entry, { key });
      fireEvent[phase](entry, { key });

      expect(onNavigate).toHaveBeenCalledTimes(1);
      await waitFor(() =>
        expect(screen.queryByRole("button", { name: "Schedules" })).toBeNull(),
      );
    },
  );

  it("files a task straight from the bar, without opening the drawer", () => {
    const onOpenNewTask = vi.fn();
    render(
      <PhoneNav config={{}} running={false} onOpenNewTask={onOpenNewTask}>
        <Rail />
      </PhoneNav>,
    );

    fireEvent.click(screen.getByRole("button", { name: "New task" }));

    expect(onOpenNewTask).toHaveBeenCalled();
  });

  // Which deployment this is, the one piece of the rail's identity block
  // that has to stay visible with the rail itself put away: an operator
  // with a staging tab and a production tab open is one tap from acting
  // on the wrong one, and the two are otherwise identical.
  it("carries the deployment's environment on the bar", () => {
    render(
      <PhoneNav
        config={{ environmentName: "staging" }}
        running={false}
        onOpenNewTask={() => {}}
      >
        <Rail />
      </PhoneNav>,
    );

    expect(screen.getByText("staging")).toBeInTheDocument();
  });

  it("marks the menu when a configuration reset needs attention", () => {
    const { rerender } = render(
      <PhoneNav
        config={{ stateRepoResetNotice: true }}
        running={false}
        onOpenNewTask={() => {}}
      >
        <Rail />
      </PhoneNav>,
    );
    expect(document.querySelector(".MuiBadge-badge")).not.toHaveClass(
      "MuiBadge-invisible",
    );

    rerender(
      <PhoneNav config={{}} running={false} onOpenNewTask={() => {}}>
        <Rail />
      </PhoneNav>,
    );
    expect(document.querySelector(".MuiBadge-badge")).toHaveClass(
      "MuiBadge-invisible",
    );
  });

  // docs/design/settings-panel.md §4.1: the gear sits on the bar,
  // between the bell and "+", and opens Settings without the drawer.
  it("puts the gear on the bar between the bell and New task", () => {
    const onOpenSettings = vi.fn();
    render(
      <PhoneNav
        config={{}}
        alerts={<button type="button">Alerts</button>}
        onOpenNewTask={() => {}}
        onOpenSettings={onOpenSettings}
      >
        <div>rail</div>
      </PhoneNav>,
    );
    const bell = screen.getByRole("button", { name: "Alerts" });
    const gear = screen.getByTestId("phone-settings");
    const add = screen.getByRole("button", { name: "New task" });
    expect(
      bell.compareDocumentPosition(gear) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(
      gear.compareDocumentPosition(add) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    fireEvent.click(gear);
    expect(onOpenSettings).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("rail")).toBeNull();
  });
});
