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
import { describe, expect, it, vi } from "vitest";
import ReadOnlyReposField from "./ReadOnlyReposField.jsx";

const options = ["owner/schema", "owner/shared-lib", "other/tooling"];

describe("ReadOnlyReposField", () => {
  it("offers the known repos as soon as the box is focused", async () => {
    const user = userEvent.setup();
    render(
      <ReadOnlyReposField options={options} value={[]} onChange={() => {}} />,
    );

    await user.click(screen.getByLabelText(/Read-only repos/));

    for (const repo of options)
      expect(screen.getByText(repo)).toBeInTheDocument();
  });

  // grain/task-320: this popper is TaskPicker's, applied to repos --
  // same box, same eight rows under it -- so each row carries the repos
  // figure (ItemGlyph.jsx, docs/brand.md) to say what kind of thing is
  // being listed. The typed "Add owner/name" row names repos too, so it
  // carries one as well rather than being the odd row out.
  it("marks every result row with the repos glyph, typed ones included", async () => {
    const user = userEvent.setup();
    render(
      <ReadOnlyReposField options={options} value={[]} onChange={() => {}} />,
    );

    await user.click(screen.getByLabelText(/Read-only repos/));
    for (const repo of options)
      expect(
        screen.getByText(repo).closest("li").querySelector("svg[data-glyph]"),
      ).toHaveAttribute("data-glyph", "repos");

    await user.type(screen.getByLabelText(/Read-only repos/), "typed/repo");
    expect(
      screen
        .getByText("Add typed/repo")
        .closest("li")
        .querySelector('svg[data-glyph="repos"]'),
    ).toBeInTheDocument();
  });

  it("filters the options as the user types, case-insensitively", async () => {
    const user = userEvent.setup();
    render(
      <ReadOnlyReposField options={options} value={[]} onChange={() => {}} />,
    );

    await user.type(screen.getByLabelText(/Read-only repos/), "SHARED");

    expect(screen.getByText("owner/shared-lib")).toBeInTheDocument();
    expect(screen.queryByText("owner/schema")).not.toBeInTheDocument();
  });

  it("adds a clicked repo to the picked set", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <ReadOnlyReposField
        options={options}
        value={["owner/schema"]}
        onChange={onChange}
      />,
    );

    await user.type(screen.getByLabelText(/Read-only repos/), "shared");
    await user.click(screen.getByText("owner/shared-lib"));

    expect(onChange).toHaveBeenCalledWith(["owner/schema", "owner/shared-lib"]);
    expect(screen.getByLabelText(/Read-only repos/)).toHaveValue("");
  });

  it("picks the highlighted option with the arrow keys and Enter", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <ReadOnlyReposField options={options} value={[]} onChange={onChange} />,
    );

    await user.type(screen.getByLabelText(/Read-only repos/), "owner");
    await user.keyboard("{ArrowDown}");
    await user.keyboard("{Enter}");

    expect(onChange).toHaveBeenCalledWith(["owner/shared-lib"]);
  });

  it("keeps a repo already picked out of the results, so it cannot be added twice", async () => {
    const user = userEvent.setup();
    render(
      <ReadOnlyReposField
        options={options}
        value={["owner/schema"]}
        onChange={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Read-only repos/), "schema");

    // The chip above the box is the only "owner/schema" on screen.
    expect(screen.getAllByText("owner/schema")).toHaveLength(1);
    expect(screen.getByText(/No matching repos/)).toBeInTheDocument();
  });

  // grain/task-271: the repo the task is filed against is cloned into
  // its sandbox and pushed to, so "may read but never push to" cannot be
  // true of it -- the server refuses the combination
  // (ui.refuseTargetAmongReads) and the prompt has never said both about
  // one repo (orchestrator.readOnlyNames), so offering it here would be
  // offering the one suggestion submit is guaranteed to reject.
  it("does not offer the repo the task itself is filed against", async () => {
    const user = userEvent.setup();
    render(
      <ReadOnlyReposField
        options={options}
        value={[]}
        onChange={() => {}}
        target="Owner/Schema"
      />,
    );

    await user.click(screen.getByLabelText(/Read-only repos/));

    expect(screen.getByText("owner/shared-lib")).toBeInTheDocument();
    expect(screen.queryByText("owner/schema")).not.toBeInTheDocument();
  });

  // Only the suggestions: a task that arrived carrying this combination
  // from somewhere the API does not police (a template's Reads, a
  // backport carrying the original's) still shows what it has, and the
  // chip is how it comes off.
  it("still shows the target as a chip when the task already carries it", () => {
    render(
      <ReadOnlyReposField
        options={options}
        value={["owner/schema"]}
        onChange={() => {}}
        target="owner/schema"
      />,
    );

    expect(screen.getByTitle("Remove owner/schema")).toBeInTheDocument();
  });

  it("removes a picked repo when its chip is dismissed", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <ReadOnlyReposField
        options={options}
        value={["owner/schema", "other/tooling"]}
        onChange={onChange}
      />,
    );

    await user.click(screen.getByTitle("Remove owner/schema"));

    expect(onChange).toHaveBeenCalledWith(["other/tooling"]);
  });

  // options is never the full universe of valid repos (RepoField's own
  // "Other…" exists for the same gap), so a repo nothing has used yet has
  // to be nameable outright.
  it("offers typed text that parses as owner/name as a repo of its own", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <ReadOnlyReposField options={options} value={[]} onChange={onChange} />,
    );

    await user.type(
      screen.getByLabelText(/Read-only repos/),
      "someone/brand-new",
    );
    await user.click(screen.getByText("Add someone/brand-new"));

    expect(onChange).toHaveBeenCalledWith(["someone/brand-new"]);
  });

  it("does not offer to add text that is not owner/name", async () => {
    const user = userEvent.setup();
    render(
      <ReadOnlyReposField options={options} value={[]} onChange={() => {}} />,
    );

    await user.type(screen.getByLabelText(/Read-only repos/), "brand-new");

    expect(screen.queryByText(/^Add /)).not.toBeInTheDocument();
    expect(screen.getByText(/No matching repos/)).toBeInTheDocument();
  });

  // This field was a plain "owner/name, comma-separated" text input
  // before it was a picker, so a pasted list still has to land in one go.
  it("adds every repo in a comma-separated list at once", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <ReadOnlyReposField options={options} value={[]} onChange={onChange} />,
    );

    await user.type(screen.getByLabelText(/Read-only repos/), "a/one, b/two");
    await user.click(screen.getByText("Add a/one, b/two"));

    expect(onChange).toHaveBeenCalledWith(["a/one", "b/two"]);
  });

  // Typing the whole name and moving straight on to the next field is
  // exactly what the old text input taught people to do; dropping the
  // text at that point would file the task without the repo.
  it("adds a repo left typed in the box when it loses focus", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <div>
        <ReadOnlyReposField options={options} value={[]} onChange={onChange} />
        <button>elsewhere</button>
      </div>,
    );

    await user.type(
      screen.getByLabelText(/Read-only repos/),
      "someone/brand-new",
    );
    await user.click(screen.getByText("elsewhere"));

    expect(onChange).toHaveBeenCalledWith(["someone/brand-new"]);
  });

  it("leaves text that is not a repo in the box rather than adding or discarding it", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <div>
        <ReadOnlyReposField options={options} value={[]} onChange={onChange} />
        <button>elsewhere</button>
      </div>,
    );

    await user.type(screen.getByLabelText(/Read-only repos/), "brand-new");
    await user.click(screen.getByText("elsewhere"));

    expect(onChange).not.toHaveBeenCalled();
    expect(screen.getByLabelText(/Read-only repos/)).toHaveValue("brand-new");
  });

  // Clicking a result blurs the box on its way to picking, so a field
  // that committed the query on blur as well would add the half-typed
  // prefix alongside the repo actually clicked.
  it("adds only the clicked repo when the query is itself a prefix that parses", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <ReadOnlyReposField options={options} value={[]} onChange={onChange} />,
    );

    await user.type(screen.getByLabelText(/Read-only repos/), "owner/s");
    await user.click(screen.getByText("owner/shared-lib"));

    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith(["owner/shared-lib"]);
  });

  // The box sits inside a form whose submit button is one Enter away.
  it("does not submit the surrounding form when Enter picks a repo", async () => {
    const onSubmit = vi.fn((e) => e.preventDefault());
    const user = userEvent.setup();
    render(
      <form onSubmit={onSubmit}>
        <ReadOnlyReposField options={options} value={[]} onChange={() => {}} />
        <button type="submit">Save</button>
      </form>,
    );

    await user.type(screen.getByLabelText(/Read-only repos/), "owner/schema");
    await user.keyboard("{Enter}");

    expect(onSubmit).not.toHaveBeenCalled();
  });

  it("closes the results on Escape", async () => {
    const user = userEvent.setup();
    render(
      <ReadOnlyReposField options={options} value={[]} onChange={() => {}} />,
    );

    await user.type(screen.getByLabelText(/Read-only repos/), "owner");
    expect(screen.getByText("owner/schema")).toBeInTheDocument();

    await user.keyboard("{Escape}");
    expect(screen.queryByText("owner/schema")).not.toBeInTheDocument();
  });

  // Escape with the list open closes the list and nothing else: the New
  // task drawer this field sits in closes on an Escape that reaches it,
  // which took the whole form away (2026-10-05 walkthrough).
  it("keeps the Escape that closes the list from the drawer around it", async () => {
    const user = userEvent.setup();
    const onDrawerKey = vi.fn();
    render(
      <div onKeyDown={(e) => e.key === "Escape" && onDrawerKey()}>
        <ReadOnlyReposField options={options} value={[]} onChange={() => {}} />
      </div>,
    );

    await user.click(screen.getByLabelText(/Read-only repos/));
    expect(screen.getByText("owner/schema")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByText("owner/schema")).not.toBeInTheDocument();
    expect(onDrawerKey).not.toHaveBeenCalled();

    await user.keyboard("{Escape}");
    expect(onDrawerKey).toHaveBeenCalledTimes(1);
  });
});
