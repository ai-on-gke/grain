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
import RepoField, { NO_REPO } from "./RepoField.jsx";

describe("RepoField", () => {
  it("renders a plain text input when there are no known repos", () => {
    render(<RepoField name="repo" options={[]} />);
    const field = screen.getByRole("textbox");
    expect(field.tagName).toBe("INPUT");
  });

  it("renders a dropdown of the known repos, plus a blank option when not required", () => {
    render(<RepoField name="repo" options={["acme/widgets", "acme/other"]} />);
    const field = screen.getByRole("combobox");
    expect(field.tagName).toBe("SELECT");
    expect(
      screen.getByRole("option", { name: "acme/widgets" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("option", { name: "acme/other" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("option", { name: "—" })).toBeInTheDocument();
  });

  // grain/task-320: the repos figure goes in front of the field rather
  // than on each option, because an <option> can hold no markup and this
  // stays a native <select> -- but it is the same ring the Repos page,
  // the nav rail and the read-only repos picker carry (ItemGlyph.jsx,
  // docs/brand.md), in both of this field's two states.
  it("marks the field with the repos glyph, as a dropdown and as free text", async () => {
    const user = userEvent.setup();
    const { container } = render(
      <RepoField name="repo" options={["acme/widgets"]} />,
    );

    expect(
      container.querySelector('.repo-field svg[data-glyph="repos"]'),
    ).toBeInTheDocument();

    await user.selectOptions(screen.getByRole("combobox"), "Other…");
    expect(
      container.querySelector('.repo-field svg[data-glyph="repos"]'),
    ).toBeInTheDocument();
  });

  it("omits the blank option when required", () => {
    render(<RepoField name="repo" options={["acme/widgets"]} required />);
    expect(screen.queryByRole("option", { name: "—" })).not.toBeInTheDocument();
  });

  it("starts as a dropdown pre-selecting a default value that is one of the options", () => {
    render(
      <RepoField
        name="repo"
        options={["acme/widgets", "acme/other"]}
        defaultValue="acme/other"
      />,
    );
    expect(screen.getByRole("combobox")).toHaveValue("acme/other");
  });

  it("starts as a text input when the default value is not one of the options", () => {
    render(
      <RepoField
        name="repo"
        options={["acme/widgets"]}
        defaultValue="acme/unlisted"
      />,
    );
    expect(screen.getByRole("textbox")).toHaveValue("acme/unlisted");
  });

  it("switches to a free-text input after picking Other…, and back after Choose from list", async () => {
    const user = userEvent.setup();
    render(<RepoField name="repo" options={["acme/widgets"]} />);

    await user.selectOptions(screen.getByRole("combobox"), "Other…");
    const field = screen.getByRole("textbox");
    await user.type(field, "acme/new-repo");
    expect(field).toHaveValue("acme/new-repo");

    await user.click(screen.getByRole("button", { name: "Choose from list" }));
    expect(screen.getByRole("combobox")).toBeInTheDocument();
  });

  it("reports the picked value to onChange as the dropdown selection changes", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <RepoField
        name="repo"
        options={["acme/widgets", "acme/other"]}
        onChange={onChange}
      />,
    );

    await user.selectOptions(screen.getByRole("combobox"), "acme/other");
    expect(onChange).toHaveBeenCalledWith("acme/other");
  });

  it("reports the picked value to onChange while typing in the free-text input", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<RepoField name="repo" options={[]} onChange={onChange} />);

    await user.type(screen.getByRole("textbox"), "a");
    expect(onChange).toHaveBeenCalledWith("a");
  });

  // grain/task-328: onCommit is the reading a field that saves each edit
  // needs -- the value somebody settled on, rather than every keystroke
  // on the way to it. A pick from the list is settled the moment it is
  // made, so it reports both.
  it("reports a dropdown pick to onCommit as well as to onChange", async () => {
    const onCommit = vi.fn();
    const user = userEvent.setup();
    render(
      <RepoField
        name="repo"
        options={["acme/widgets", "acme/other"]}
        onCommit={onCommit}
      />,
    );

    await user.selectOptions(screen.getByRole("combobox"), "acme/other");
    expect(onCommit).toHaveBeenCalledWith("acme/other");
  });

  it("reports free text to onCommit only once the box is left", async () => {
    const onCommit = vi.fn();
    const user = userEvent.setup();
    render(<RepoField name="repo" options={[]} onCommit={onCommit} />);

    await user.type(screen.getByRole("textbox"), "acme/new-repo");
    expect(onCommit).not.toHaveBeenCalled();

    await user.tab();
    expect(onCommit).toHaveBeenCalledExactlyOnceWith("acme/new-repo");
  });

  it("reports free text to onCommit on Enter, without leaving the box", async () => {
    const onCommit = vi.fn();
    const user = userEvent.setup();
    render(<RepoField name="repo" options={[]} onCommit={onCommit} />);

    await user.type(screen.getByRole("textbox"), "acme/new-repo{Enter}");
    expect(onCommit).toHaveBeenCalledExactlyOnceWith("acme/new-repo");
  });

  // Enter is only swallowed for a caller that asked for onCommit: in the
  // forms this field started in (NewTaskOverlay, TemplateOverlay) it is
  // the key that submits, and taking that away from a field whose caller
  // reads the value at submit time would be a change nobody asked for.
  it("leaves Enter alone for a caller that takes no onCommit", async () => {
    const submit = vi.fn((e) => e.preventDefault());
    const user = userEvent.setup();
    render(
      <form onSubmit={submit}>
        <RepoField name="repo" options={[]} />
      </form>,
    );

    await user.type(screen.getByRole("textbox"), "acme/new-repo{Enter}");
    expect(submit).toHaveBeenCalled();
  });

  // grain/task-85: "no repo at all" is an option in this same list
  // rather than a checkbox beside the field, so it is picked the way
  // every other target is.
  it("offers No repo as an option, and reports it to onChange", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <RepoField
        name="repo"
        options={["acme/widgets"]}
        allowNoRepo
        onChange={onChange}
      />,
    );

    await user.selectOptions(screen.getByRole("combobox"), NO_REPO);
    expect(onChange).toHaveBeenCalledWith(NO_REPO);
  });

  // Without a repo list there is still something to choose from once
  // "No repo" is one of the choices -- a deployment filing its very
  // first task should not have to type a repo name to find out it can
  // say there isn't one.
  it("still offers the dropdown with no known repos when No repo is allowed", () => {
    render(<RepoField name="repo" options={[]} allowNoRepo />);
    expect(screen.getByRole("combobox")).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /No repo/ })).toBeInTheDocument();
  });

  // Picking "Other…" has to take the caller off NO_REPO too: the field
  // now shows a text box for a repo name, so a caller still holding
  // "no repo" would be tracking a choice nothing on screen reflects.
  it("reports the free-text input's own value when Other… is picked", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <RepoField
        name="repo"
        options={["acme/widgets"]}
        allowNoRepo
        onChange={onChange}
      />,
    );

    await user.selectOptions(screen.getByRole("combobox"), NO_REPO);
    await user.selectOptions(screen.getByRole("combobox"), "Other…");
    expect(onChange).toHaveBeenLastCalledWith("");
  });

  // options is knownRepos(config, tasks), and config comes from a fetch:
  // a pane rendered before that fetch lands -- a deep link straight into
  // a task, which has no list page in front of it -- gets an empty list
  // and then a full one a moment later. The field has to follow, rather
  // than staying on the text path it was born on and offering a "Choose
  // from list" link nobody would have needed. That link is also 19px
  // tall, which is why e2e/phone.spec.js's thumb-target rule caught this
  // and not a person.
  it("takes the dropdown when the known repos arrive after the first render", () => {
    const { rerender } = render(<RepoField name="repo" options={[]} />);
    expect(screen.getByRole("textbox").tagName).toBe("INPUT");
    expect(screen.queryByRole("button", { name: "Choose from list" })).toBe(
      null,
    );

    rerender(<RepoField name="repo" options={["acme/widgets"]} />);
    expect(screen.getByRole("combobox").tagName).toBe("SELECT");
    expect(screen.queryByRole("button", { name: "Choose from list" })).toBe(
      null,
    );
  });

  // ...and a repo the list does not name yet does the same: the field is
  // free text only for as long as the value it holds is one nothing can
  // offer.
  it("takes the dropdown when its own value arrives in the list", () => {
    const { rerender } = render(
      <RepoField
        name="repo"
        options={["acme/other"]}
        defaultValue="acme/widgets"
      />,
    );
    expect(screen.getByRole("textbox").tagName).toBe("INPUT");

    rerender(
      <RepoField
        name="repo"
        options={["acme/other", "acme/widgets"]}
        defaultValue="acme/widgets"
      />,
    );
    expect(screen.getByRole("combobox").tagName).toBe("SELECT");
  });

  // A choice somebody made still wins over any of that: "Other…" is a
  // decision to type, and it survives the list growing under it.
  it("stays on the text path after Other…, whatever the list does next", async () => {
    const user = userEvent.setup();
    const { rerender } = render(
      <RepoField name="repo" options={["acme/widgets"]} />,
    );

    await user.selectOptions(screen.getByRole("combobox"), "Other…");
    expect(screen.getByRole("textbox").tagName).toBe("INPUT");

    rerender(
      <RepoField name="repo" options={["acme/widgets", "acme/other"]} />,
    );
    expect(screen.getByRole("textbox").tagName).toBe("INPUT");
  });

  // A phone capitalizes the first letter of a text box unless the box
  // says not to (src/identifierField.js), and "Acme/widgets" is not a repo --
  // it is a task filed against a repo that does not exist.
  it("does not let a phone capitalize a typed repo name", async () => {
    const user = userEvent.setup();
    render(<RepoField name="repo" options={["acme/widgets"]} />);

    await user.selectOptions(screen.getByRole("combobox"), "Other…");
    expect(screen.getByRole("textbox")).toHaveAttribute(
      "autocapitalize",
      "off",
    );
  });

  // 2026-10-23 intake walkthrough (task 271): "Choose from list" put the
  // dropdown back without telling the caller, which went on holding --
  // and filing against -- the repo typed into "Other…".
  it("reports the list's value when Choose from list abandons a typed repo", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <RepoField name="repo" options={["acme/widgets"]} onChange={onChange} />,
    );
    await user.selectOptions(screen.getByRole("combobox"), "Other…");
    await user.type(screen.getByRole("textbox"), "unlisted/custom-repo");
    expect(onChange).toHaveBeenLastCalledWith("unlisted/custom-repo");

    await user.click(screen.getByRole("button", { name: "Choose from list" }));
    expect(screen.getByRole("combobox")).toHaveValue("");
    expect(onChange).toHaveBeenLastCalledWith("");
  });

  it("goes back to the seeded repo, not the typed one, when the default is listed", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <RepoField
        name="repo"
        options={["acme/widgets", "acme/gadgets"]}
        defaultValue="acme/gadgets"
        onChange={onChange}
      />,
    );
    await user.selectOptions(screen.getByRole("combobox"), "Other…");
    await user.type(screen.getByRole("textbox"), "x/y");
    await user.click(screen.getByRole("button", { name: "Choose from list" }));
    expect(screen.getByRole("combobox")).toHaveValue("acme/gadgets");
    expect(onChange).toHaveBeenLastCalledWith("acme/gadgets");
  });
});
