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

// A narrowed task list (grain/task-317): how the list is narrowed is
// App's state and the URL's, so it can be linked to, reloaded into and
// come back to.
//
// Part of the App suite -- see App.test.jsx for why it is six files.

import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import App from "./App.jsx";
import { initialTasks, resetAppTestState, setupApi } from "./appTestHarness.js";

// A pass-through: only the default fetch wrapper is faked, so
// isUnreachable/unreachableReason -- which App.jsx's poll and showError
// call on whatever this mock rejects with -- stay the real, pure
// functions rather than becoming undefined (App.test.jsx's own mock,
// same reason).
vi.mock("./api.js", async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, default: vi.fn() };
});

describe("App", () => {
  afterEach(resetAppTestState);

  // grain/task-317: how the list is narrowed is App's state and the
  // URL's, so a narrowed list is somewhere you can link to, reload into
  // and come back to -- rather than a set of menus that reset the next
  // time TaskList mounts.
  describe("a narrowed task list", () => {
    it("puts the search text in the address bar as it is typed", async () => {
      setupApi();
      const user = userEvent.setup();
      render(<App />);
      await screen.findByText("Fix bug");

      await user.type(screen.getByPlaceholderText("Search tasks…"), "feature");

      expect(screen.queryByText("Fix bug")).not.toBeInTheDocument();
      expect(screen.getByText("Add feature")).toBeInTheDocument();
      // A list nobody has narrowed lives at "/" and lets the deployment
      // decide what that means; from the first change onwards the query
      // is the whole of what the list is narrowed by, the condition it
      // opened with included (taskFilters.js's defaultFilters). So the
      // link reopens this list rather than one narrowed by whatever the
      // deployment's default happens to be by then.
      expect(window.location.pathname + window.location.search).toBe(
        "/?q=feature&f=state:none:deferred|closed",
      );
    });

    // The rail's own state entries and the toolbar's State condition are
    // one question asked in two places, now that the states a list opens
    // without are an ordinary condition (App's selectStateFilter): a
    // list narrowed to "State is none of Deferred, Closed" and then
    // asked for "Closed" in the rail would otherwise have nothing in it
    // at all.
    it("shows closed tasks when the rail asks for them, and hides them again", async () => {
      setupApi([
        ...initialTasks,
        {
          id: "3",
          title: "Long done",
          state: "closed",
          repo: "acme/widgets",
          blocked: false,
          capabilities: [],
          reads: [],
        },
      ]);
      const user = userEvent.setup();
      render(<App />);
      await screen.findByText("Fix bug");
      expect(screen.queryByText("Long done")).not.toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: /^Closed/ }));

      // This used to time out at one second (grain/task-1697), then at
      // five (grain/task-1859), then at ten, on loaded CI runners. A
      // longer wait was never going to help: the rail's state entries
      // were remounted on every render of the rail, so a re-render
      // between the getByRole above and the click took the button out
      // of the document and the click did nothing at all
      // (grain/task-3942, Sidebar.jsx's NavItem). The headroom stays,
      // because it costs a passing run nothing.
      expect(
        await screen.findByText("Long done", {}, { timeout: 10000 }),
      ).toBeInTheDocument();
      expect(screen.queryByText("Fix bug")).not.toBeInTheDocument();

      // ...and back out of it: "Tasks" is not a state, so it puts
      // the condition the list opens with back rather than leaving the
      // archive on screen.
      await user.click(screen.getByRole("button", { name: /^Tasks/ }));

      expect(
        await screen.findByText("Fix bug", {}, { timeout: 10000 }),
      ).toBeInTheDocument();
      expect(screen.queryByText("Long done")).not.toBeInTheDocument();
    });

    // grain/task-1355: the rail's pick used to live in App alone, and
    // taking the State condition out of the narrowing left a URL that
    // reloaded into All tasks with the closed tasks mixed back in. It is
    // in the query now, so it reloads, links and steps back like the
    // rest of the narrowing.
    it("keeps the rail's state view in the URL, cold and across Back", async () => {
      const closed = {
        id: "3",
        title: "Long done",
        state: "closed",
        repo: "acme/widgets",
        blocked: false,
        capabilities: [],
        reads: [],
      };
      window.history.replaceState(null, "", "/?state=closed");
      setupApi([...initialTasks, closed]);
      const user = userEvent.setup();
      render(<App />);

      expect(await screen.findByText("Long done")).toBeInTheDocument();
      expect(screen.queryByText("Fix bug")).not.toBeInTheDocument();

      const before = window.history.length;
      await user.click(screen.getByRole("button", { name: /^Tasks/ }));
      expect(await screen.findByText("Fix bug")).toBeInTheDocument();
      expect(window.location.search).not.toContain("state=");
      // A different list, so a history entry of its own.
      expect(window.history.length).toBe(before + 1);

      window.history.replaceState(null, "", "/?state=closed");
      fireEvent.popState(window);

      expect(await screen.findByText("Long done")).toBeInTheDocument();
      expect(screen.queryByText("Fix bug")).not.toBeInTheDocument();
    });

    // The Merged list is a history of what landed, so it opens newest
    // merged first rather than in backlog order -- from the rail and
    // from a link naming only the state -- and puts backlog order back
    // when the reader moves on to a state of its own.
    describe("the Merged view", () => {
      const merged = [
        {
          id: "3",
          title: "Merged long ago",
          state: "closed",
          closedReason: "merged",
          prMergedAt: "2026-09-01T10:00:00Z",
          blocked: false,
          capabilities: [],
          reads: [],
        },
        {
          id: "4",
          title: "Merged just now",
          state: "closed",
          closedReason: "merged",
          prMergedAt: "2026-10-20T10:00:00Z",
          blocked: false,
          capabilities: [],
          reads: [],
        },
      ];
      const titles = () =>
        [...document.querySelectorAll(".task-row .task-title")].map(
          (el) => el.textContent,
        );

      it("sorts by merged time, newest first, when picked in the rail", async () => {
        setupApi([...initialTasks, ...merged]);
        const user = userEvent.setup();
        render(<App />);
        await screen.findByText("Fix bug");

        await user.click(screen.getByRole("button", { name: /^Merged/ }));

        expect(
          await screen.findByText("Merged just now", {}, { timeout: 10000 }),
        ).toBeInTheDocument();
        expect(titles()).toEqual(["Merged just now", "Merged long ago"]);
        expect(window.location.search).toContain("sort=merged:desc");
        // An order nobody chose is not a narrowing to be rescued from.
        expect(
          screen.queryByRole("button", { name: "Clear" }),
        ).not.toBeInTheDocument();

        // ...and a list with no order of its own goes back to the
        // backlog's.
        await user.click(screen.getByRole("button", { name: /^Tasks/ }));
        await waitFor(() =>
          expect(window.location.search).not.toContain("sort="),
        );
      });

      it("sorts by merged time from a link that names only the state", async () => {
        window.history.replaceState(null, "", "/?state=merged");
        setupApi([...initialTasks, ...merged]);
        render(<App />);

        expect(await screen.findByText("Merged just now")).toBeInTheDocument();
        expect(titles()).toEqual(["Merged just now", "Merged long ago"]);
      });

      it("keeps an order the link asked for", async () => {
        window.history.replaceState(null, "", "/?state=merged&sort=merged:asc");
        setupApi([...initialTasks, ...merged]);
        render(<App />);

        expect(await screen.findByText("Merged just now")).toBeInTheDocument();
        expect(titles()).toEqual(["Merged long ago", "Merged just now"]);
      });
    });

    // A hand-written link naming only the state still opens on the
    // deployment's default narrowing, which sets the closed tasks aside;
    // the rail's state wins over that, as a click on it would have.
    it("opens a state view from a link that names nothing else", async () => {
      window.history.replaceState(null, "", "/?state=closed");
      setupApi([
        ...initialTasks,
        {
          id: "3",
          title: "Long done",
          state: "closed",
          repo: "acme/widgets",
          blocked: false,
          capabilities: [],
          reads: [],
        },
      ]);
      const user = userEvent.setup();
      render(<App />);

      expect(await screen.findByText("Long done")).toBeInTheDocument();
      await user.type(screen.getByPlaceholderText("Search tasks…"), "long");
      expect(screen.getByText("Long done")).toBeInTheDocument();
      expect(window.location.search).toBe("?state=closed&q=long");
    });

    it("opens a narrowed list from the URL alone", async () => {
      window.history.replaceState(null, "", "/?f=repo:any:acme/other");
      setupApi();
      render(<App />);

      expect(await screen.findByText("Add feature")).toBeInTheDocument();
      expect(screen.queryByText("Fix bug")).not.toBeInTheDocument();
      expect(window.location.search).toBe("?f=repo:any:acme/other");
    });

    // The narrowing a build before grain/task-42 wrote -- one parameter
    // per attribute -- still opens the list it names, as the condition
    // it always meant.
    it("opens a list narrowed the way an older build wrote it", async () => {
      window.history.replaceState(null, "", "/?repo=acme/other");
      setupApi();
      render(<App />);

      expect(await screen.findByText("Add feature")).toBeInTheDocument();
      expect(screen.queryByText("Fix bug")).not.toBeInTheDocument();
      await waitFor(() =>
        expect(window.location.search).toBe("?f=repo:any:acme/other"),
      );
    });

    it("names the picked repo in the URL when it is chosen in the editor", async () => {
      setupApi();
      const user = userEvent.setup();
      render(<App />);
      await screen.findByText("Fix bug");

      await user.click(
        screen.getByTitle(
          "Add and edit the conditions this list is narrowed by",
        ),
      );
      // Filter 1 is the condition the list opened with, so the one being
      // added here is the second row of the editor.
      await user.click(screen.getByRole("button", { name: "Add filter" }));
      await user.click(await screen.findByRole("menuitem", { name: "Repo" }));
      await user.click(screen.getByLabelText("Value for filter 2"));
      await user.click(
        within(screen.getByRole("listbox")).getByText("acme/other"),
      );

      expect(screen.queryByText("Fix bug")).not.toBeInTheDocument();
      await waitFor(() =>
        expect(window.location.search).toBe(
          "?f=state:none:deferred|closed&f=repo:any:acme/other",
        ),
      );
    });

    // A link somebody kept naming a repo that has since gone quiet, or
    // a capability that was removed: the list it names is the whole
    // list, not an empty one -- the value is offered in its own menu so
    // it can be taken back off, and matches nothing while it is there.
    it("shows only what a link naming a repo no task carries can match", async () => {
      window.history.replaceState(null, "", "/?f=repo:any:acme/gone");
      setupApi();
      render(<App />);

      expect(
        await screen.findByText("No tasks match these filters."),
      ).toBeInTheDocument();

      // The empty state can be up before the task list has arrived, and
      // the toolbar Clear sits in only draws once it has (TaskList's
      // tasks.length > 0), so wait for the button rather than assume it.
      const user = userEvent.setup();
      await user.click(
        await screen.findByRole("button", { name: "Clear" }, { timeout: 5000 }),
      );
      expect(await screen.findByText("Fix bug")).toBeInTheDocument();
      expect(screen.getByText("Add feature")).toBeInTheDocument();
    });

    it("keeps the narrowing across a trip to another view and back", async () => {
      setupApi();
      const user = userEvent.setup();
      render(<App />);
      await screen.findByText("Fix bug");

      await user.type(screen.getByPlaceholderText("Search tasks…"), "feature");
      await user.click(screen.getByRole("button", { name: /^Repos/ }));
      await screen.findByText("acme/widgets");
      await user.click(screen.getByRole("button", { name: /^Tasks/ }));

      expect(await screen.findByText("Add feature")).toBeInTheDocument();
      expect(screen.queryByText("Fix bug")).not.toBeInTheDocument();
      expect(window.location.search).toBe(
        "?q=feature&f=state:none:deferred|closed",
      );
    });

    // Going to a Kanban board is not one of those trips, and this is
    // what changed when the fixed board left the rail (grain/task-162).
    // The board on the rail was the same tasks in columns and inherited
    // whatever the toolbar was asking; a *saved* board is a question of
    // its own, so opening one replaces the narrowing rather than
    // carrying it (App's openBoard, boards.js). The board draws the same
    // columns either way -- what it asks is the difference.
    it("asks a saved board's own question rather than the list's", async () => {
      setupApi(
        initialTasks,
        [],
        [],
        [],
        [
          {
            id: "board-1",
            name: "Widgets",
            kind: "kanban",
            query: "f=repo:any:acme/widgets",
            folder: "",
            orderKey: 1,
          },
        ],
      );
      const user = userEvent.setup();
      render(<App />);
      await screen.findByText("Fix bug");

      await user.type(screen.getByPlaceholderText("Search tasks…"), "feature");
      await user.click(screen.getByRole("button", { name: /^Boards/ }));
      await user.click(await screen.findByText("Widgets"));

      await screen.findByRole("heading", { name: "Board" });
      expect(await screen.findByText("Fix bug")).toBeInTheDocument();
      expect(screen.queryByText("Add feature")).not.toBeInTheDocument();
      expect(window.location.pathname + window.location.search).toBe(
        "/boards/board-1?f=repo:any:acme/widgets",
      );
    });

    // Narrowing replaces rather than pushes -- a history step per
    // keystroke would make Back mean "delete a letter" -- but a
    // narrowed URL is still an entry the Back button restores in full
    // once something else has navigated away from it.
    it("restores the narrowing when the browser navigates back to it", async () => {
      window.history.replaceState(null, "", "/?q=feature");
      setupApi();
      const user = userEvent.setup();
      render(<App />);
      await screen.findByText("Add feature");

      await user.click(screen.getByRole("button", { name: /^Repos/ }));
      await screen.findByText("acme/widgets");
      await waitFor(() => expect(window.location.pathname).toBe("/repos"));

      window.history.replaceState(null, "", "/?q=feature");
      fireEvent.popState(window);

      expect(await screen.findByText("Add feature")).toBeInTheDocument();
      expect(screen.queryByText("Fix bug")).not.toBeInTheDocument();
      expect(screen.getByPlaceholderText("Search tasks…")).toHaveValue(
        "feature",
      );
    });

    it("clears the narrowing, and the query, with the toolbar's Clear", async () => {
      window.history.replaceState(null, "", "/?q=feature");
      setupApi();
      const user = userEvent.setup();
      render(<App />);
      await screen.findByText("Add feature");

      await user.click(screen.getByRole("button", { name: "Clear" }));

      expect(await screen.findByText("Fix bug")).toBeInTheDocument();
      // Clear is "back to the list I opened", which is the condition the
      // list opens narrowed by rather than no conditions at all -- so
      // the query names it, and the link still reopens this list.
      expect(window.location.pathname + window.location.search).toBe(
        "/?f=state:none:deferred|closed",
      );
    });

    it("clears a narrowed state view back to that state's own defaults", async () => {
      window.history.replaceState(null, "", "/?state=closed");
      setupApi([
        ...initialTasks,
        {
          id: "3",
          title: "Long done",
          state: "closed",
          repo: "acme/widgets",
          blocked: false,
          capabilities: [],
          reads: [],
        },
      ]);
      const user = userEvent.setup();
      render(<App />);

      expect(await screen.findByText("Long done")).toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: "Clear" }),
      ).not.toBeInTheDocument();

      await user.type(screen.getByPlaceholderText("Search tasks…"), "nothing");
      expect(screen.queryByText("Long done")).not.toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: "Clear" }));

      expect(await screen.findByText("Long done")).toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: "Clear" }),
      ).not.toBeInTheDocument();
      // The closed view's own default asks nothing once its State
      // condition is taken out, so the cleared list is written as the
      // empty narrowing -- which reopens on the same list.
      expect(window.location.pathname + window.location.search).toBe(
        "/?state=closed&f=none",
      );
    });
  });
});
