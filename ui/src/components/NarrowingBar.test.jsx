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
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import NarrowingBar from "./NarrowingBar.jsx";
import {
  NO_NARROWING,
  defaultFilters,
  defaultNarrowing,
} from "../taskFilters.js";

// NarrowingBar is the toolbar both task views wear, so what it does with
// one view's tasks is tested through that view (TaskList.test.jsx's
// filter and sort blocks). What is here is the editor itself: the two
// panels, the rows they hold, and the vocabulary the menus are built
// from -- including the custom attributes a deployment declares, which
// no view has any other way of showing.
const config = {
  capabilities: [{ id: "gcp", name: "Google Cloud" }],
};

const tasks = [
  {
    id: "1",
    title: "Fix the flake",
    state: "queued",
    repo: "acme/widgets",
    capabilities: [],
    blocked: false,
  },
  {
    id: "2",
    title: "Ship it",
    state: "queued",
    repo: "acme/gadgets",
    capabilities: ["gcp"],
    blocked: false,
  },
];

// Narrowed holds the narrowing the way App does, so a change made in one
// control is on screen for the next -- starting, as App does, from the
// deployment's own default rather than from nothing at all
// (taskFilters.js's defaultNarrowing). A test about the editor's own
// rows passes NO_NARROWING instead, so that the row it adds is filter 1
// rather than the second row of a list that already has one.
function Narrowed({ narrowing, ...props }) {
  const cfg = props.config || config;
  const [value, setValue] = useState(narrowing || defaultNarrowing(cfg));
  return (
    <NarrowingBar
      tasks={tasks}
      config={config}
      narrowing={value}
      defaults={defaultFilters(cfg)}
      onNarrow={(patch) => setValue((prev) => ({ ...prev, ...patch }))}
      {...props}
    />
  );
}

const FILTER_BUTTON = "Add and edit the conditions this list is narrowed by";
const SORT_BUTTON = "Choose the order this list is shown in";

describe("NarrowingBar", () => {
  it("opens on a toolbar of three controls, whatever the vocabulary", () => {
    render(<Narrowed />);
    expect(screen.getByPlaceholderText("Search tasks…")).toBeInTheDocument();
    // The condition a task view opens narrowed by is a condition like
    // any other: the button counts it and its chip says what it is,
    // where state used to be two checkboxes at the row's end.
    expect(screen.getByTitle(FILTER_BUTTON)).toHaveTextContent("Filters (1)");
    expect(
      screen.getByText("State is none of Deferred, Closed"),
    ).toBeInTheDocument();
    expect(screen.getByTitle(SORT_BUTTON)).toHaveTextContent("Backlog order");
    // Nothing to clear while the list is still asking exactly that.
    expect(screen.queryByRole("button", { name: "Clear" })).toBeNull();
  });

  // Clear is how you get back to the list you opened, which is not the
  // same as a list narrowed by nothing at all now that the default is a
  // condition: clearing to nothing would fill the list with closed
  // tasks and leave no gesture for undoing it.
  it("clears back to the default condition rather than to none", async () => {
    const user = userEvent.setup();
    render(<Narrowed narrowing={{ search: "flake", sort: [], filters: [] }} />);

    await user.click(screen.getByRole("button", { name: "Clear" }));

    expect(screen.getByPlaceholderText("Search tasks…")).toHaveValue("");
    expect(
      screen.getByText("State is none of Deferred, Closed"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Clear" })).toBeNull();
  });

  // The × inside the search box, which is the way out of a search that
  // does not also throw the filters away.
  it("clears the search box from the button inside it", async () => {
    const user = userEvent.setup();
    render(<Narrowed narrowing={{ search: "", sort: [], filters: [] }} />);

    const box = screen.getByPlaceholderText("Search tasks…");
    expect(screen.queryByTitle("Clear the search box")).toBeNull();

    await user.type(box, "flake");
    await user.click(screen.getByTitle("Clear the search box"));

    expect(box).toHaveValue("");
    expect(box).toHaveFocus();
    // Gone again with the text it was there to remove.
    expect(screen.queryByTitle("Clear the search box")).toBeNull();
  });

  it("offers every attribute the deployment has, custom ones included", async () => {
    const user = userEvent.setup();
    render(
      <Narrowed
        narrowing={NO_NARROWING}
        config={{
          ...config,
          customFields: [{ name: "team", label: "Team", type: "text" }],
        }}
      />,
    );

    await user.click(screen.getByTitle(FILTER_BUTTON));
    await user.click(screen.getByRole("button", { name: "Filter by…" }));

    const offered = screen.getAllByRole("menuitem").map((i) => i.textContent);
    expect(offered).toContain("Repo");
    expect(offered).toContain("Created");
    expect(offered).toContain("Team");
  });

  // The rest of the way through the editor for a declared field, which
  // is where a namespaced id or a missing value control would show: the
  // field is picked from the menu, its values are offered, one is chosen
  // and the chip reads back as the sentence it is.
  it("narrows on a declared field, offering the values in view", async () => {
    const user = userEvent.setup();
    const priorities = tasks.map((t, i) => ({
      ...t,
      customFields: { priority: i === 0 ? "high" : "low" },
    }));
    render(
      <Narrowed
        narrowing={NO_NARROWING}
        tasks={priorities}
        config={{
          ...config,
          customFields: [
            {
              name: "priority",
              label: "Priority",
              type: "enum",
              options: ["low", "medium", "high"],
            },
          ],
        }}
      />,
    );

    await user.click(screen.getByTitle(FILTER_BUTTON));
    await user.click(screen.getByRole("button", { name: "Filter by…" }));
    await user.click(await screen.findByRole("menuitem", { name: "Priority" }));
    await user.click(screen.getByLabelText("Value for filter 1"));

    // The declared order rather than the alphabetical one, and only the
    // two values the tasks in view carry -- "medium" is declared but
    // nothing here has it, and a choice that would come back empty is
    // not offered.
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "low",
      "high",
    ]);

    await user.click(screen.getByRole("option", { name: "high" }));
    await user.keyboard("{Escape}");
    await user.click(screen.getByTitle(FILTER_BUTTON));

    // Closed again, the chip says what the list is narrowed by -- under
    // the field's declared label, not its namespaced id.
    expect(screen.getByText("Priority is any of high")).toBeInTheDocument();
  });

  // Sorting by one, which is the half of this a filter cannot stand in
  // for -- and the direction is worded from the field's declared type,
  // the same as a built-in of that type.
  it("sorts by a declared field, in the words its type reads", async () => {
    const user = userEvent.setup();
    render(
      <Narrowed
        config={{
          ...config,
          customFields: [
            { name: "due", label: "Due", type: "date" },
            {
              name: "priority",
              label: "Priority",
              type: "enum",
              options: ["low", "medium", "high"],
            },
          ],
        }}
      />,
    );

    await user.click(screen.getByTitle(SORT_BUTTON));
    await user.click(screen.getByRole("button", { name: "Sort by…" }));
    await user.click(await screen.findByRole("menuitem", { name: "Priority" }));
    await user.click(screen.getByRole("button", { name: "Then by…" }));
    await user.click(await screen.findByRole("menuitem", { name: "Due" }));

    // An enum with declared options is an order to run through; a date
    // opens on its useful end, the same as "Created" does.
    expect(screen.getByTitle(SORT_BUTTON)).toHaveTextContent(
      "Priority First to last, then Due Newest first",
    );
  });

  // A link's sort key naming no attribute here orders nothing and has no
  // row in the editor, so the button says the view's own order rather
  // than nothing at all, and the editor offers "Sort by…" with the note.
  it("labels the sort by the keys it knows, not by a link's unknown one", async () => {
    const user = userEvent.setup();
    render(
      <Narrowed
        narrowing={{ ...NO_NARROWING, sort: [{ attr: "bogus", dir: "asc" }] }}
      />,
    );

    expect(screen.getByTitle(SORT_BUTTON)).toHaveTextContent("Backlog order");
    await user.click(screen.getByTitle(SORT_BUTTON));
    expect(screen.queryByLabelText("Attribute for sort 1")).toBeNull();
    expect(
      screen.getByRole("button", { name: "Sort by…" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "The backlog's own order, which is the one a drag changes.",
      ),
    ).toBeInTheDocument();
  });

  it("adds a condition, and takes it away again", async () => {
    const user = userEvent.setup();
    render(<Narrowed narrowing={NO_NARROWING} />);

    await user.click(screen.getByTitle(FILTER_BUTTON));
    await user.click(screen.getByRole("button", { name: "Filter by…" }));
    await user.click(await screen.findByRole("menuitem", { name: "Repo" }));

    expect(screen.getByLabelText("Attribute for filter 1")).toBeInTheDocument();
    expect(screen.getByTitle(FILTER_BUTTON)).toHaveTextContent("Filters (1)");

    await user.click(screen.getByRole("button", { name: "Remove filter 1" }));

    expect(screen.queryByLabelText("Attribute for filter 1")).toBeNull();
    expect(screen.getByTitle(FILTER_BUTTON)).toHaveTextContent("Filter");
  });

  // Closing the editor leaves the question on screen rather than hiding
  // it: a narrowed list should say what it is narrowed by.
  it("shows the conditions as chips once the editor is closed", async () => {
    const user = userEvent.setup();
    render(
      <Narrowed
        narrowing={{
          search: "",
          sort: [],
          filters: [
            { attr: "capability", op: "any", values: ["gcp"] },
            { attr: "repo", op: "empty", values: [] },
          ],
        }}
      />,
    );

    expect(
      screen.getByText("Capability is any of Google Cloud"),
    ).toBeInTheDocument();
    expect(screen.getByText("Repo is empty")).toBeInTheDocument();

    // Clicking a chip opens the editor on the conditions it summarises,
    // and the chips give way to the rows.
    await user.click(screen.getByText("Repo is empty"));
    expect(screen.getByLabelText("Attribute for filter 2")).toBeInTheDocument();
    expect(screen.queryByText("Repo is empty")).toBeNull();
  });

  // A condition whose attribute this deployment does not have is not
  // applied -- and says so, rather than leaving a list that shows more
  // than it was asked for looking like one whose question matched it
  // all (grain/task-1421).
  it("names a condition it cannot apply, and drops it on request", async () => {
    const user = userEvent.setup();
    render(
      <Narrowed
        narrowing={{
          search: "",
          sort: [],
          filters: [
            { attr: "repo", op: "empty", values: [] },
            { attr: "nosuchattr", op: "any", values: ["x"] },
          ],
        }}
      />,
    );

    expect(screen.getByText("Repo is empty")).toBeInTheDocument();
    expect(
      screen.getByText("Unknown condition: nosuchattr"),
    ).toBeInTheDocument();
    // Only one condition is in force, and the button counts that one.
    expect(
      screen.getByRole("button", { name: /Filters \(1\)/ }),
    ).toBeInTheDocument();

    // The editor has no row for it -- there is no attribute to put in
    // one -- so it stays named while the editor is open.
    await user.click(screen.getByText("Repo is empty"));
    expect(
      screen.getByText("Unknown condition: nosuchattr"),
    ).toBeInTheDocument();

    const chip = screen
      .getByText("Unknown condition: nosuchattr")
      .closest(".MuiChip-root");
    await user.click(chip.querySelector(".MuiChip-deleteIcon"));
    expect(screen.queryByText(/Unknown condition/)).toBeNull();
    expect(screen.getByLabelText("Attribute for filter 1")).toBeInTheDocument();
  });

  // Before the config arrives a declared field is not an attribute yet,
  // so a link narrowed by one must not flash a warning about it.
  it("names nothing before the config has arrived", () => {
    render(
      <NarrowingBar
        tasks={tasks}
        config={null}
        narrowing={{
          search: "",
          sort: [],
          filters: [{ attr: "custom.squad", op: "any", values: ["x"] }],
        }}
        onNarrow={() => {}}
      />,
    );
    expect(screen.queryByText(/Unknown condition/)).toBeNull();
  });

  it("adds sort keys, says what order they come to, and drops them again", async () => {
    const user = userEvent.setup();
    render(<Narrowed />);

    await user.click(screen.getByTitle(SORT_BUTTON));
    expect(
      screen.getByText(
        "The backlog's own order, which is the one a drag changes.",
      ),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Sort by…" }));
    await user.click(await screen.findByRole("menuitem", { name: "State" }));
    await user.click(screen.getByRole("button", { name: "Then by…" }));
    await user.click(await screen.findByRole("menuitem", { name: "Created" }));

    expect(screen.getByTitle(SORT_BUTTON)).toHaveTextContent(
      "State First to last, then Created Newest first",
    );

    await user.click(screen.getByRole("button", { name: "Remove sort 1" }));
    expect(screen.getByTitle(SORT_BUTTON)).toHaveTextContent(
      "Created Newest first",
    );
  });

  // Each kind of attribute brings its own comparisons and its own value
  // control, which is what makes one editor enough for all of them.
  it("puts the control the chosen comparison needs beside it", async () => {
    const user = userEvent.setup();
    render(
      <Narrowed
        narrowing={{
          search: "",
          sort: [],
          filters: [{ attr: "created", op: "within", values: [] }],
        }}
      />,
    );

    await user.click(screen.getByTitle(FILTER_BUTTON));
    expect(screen.getByLabelText("Value for filter 1")).toHaveAttribute(
      "type",
      "number",
    );
    expect(screen.getByText("days")).toBeInTheDocument();

    await user.click(screen.getByLabelText("Comparison for filter 1"));
    await user.click(await screen.findByRole("option", { name: "is after" }));
    expect(screen.getByLabelText("Value for filter 1")).toHaveAttribute(
      "type",
      "date",
    );

    await user.click(screen.getByLabelText("Comparison for filter 1"));
    await user.click(await screen.findByRole("option", { name: "is set" }));
    expect(screen.queryByLabelText("Value for filter 1")).toBeNull();
  });

  // Whatever the view puts at the end of the row -- the list's "show
  // closed tasks" checkboxes, the board's Columns button -- sits in the
  // same toolbar rather than in one of its own.
  it("carries the view's own controls at the end of the toolbar", () => {
    render(
      <Narrowed>
        <button type="button">Columns</button>
      </Narrowed>,
    );
    expect(
      screen
        .getByRole("button", { name: "Columns" })
        .closest(".task-list-toolbar"),
    ).not.toBeNull();
  });
});
