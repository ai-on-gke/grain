import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import WriteReposField from "./WriteReposField.jsx";

const options = ["owner/schema", "owner/shared-lib", "other/tooling"];

describe("WriteReposField", () => {
  it("adds a picked repo with no branch, meaning that repo's default", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <WriteReposField options={options} value={[]} onChange={onChange} />,
    );

    await user.click(screen.getByLabelText(/Another repo/));
    await user.click(screen.getByText("owner/schema"));

    expect(onChange).toHaveBeenCalledWith([{ repo: "owner/schema", base: "" }]);
  });

  // The branch is half of what a write target says (model.WriteTarget),
  // so every picked repo carries a box for it rather than a chip that
  // has nowhere to put one.
  it("edits the base branch of a picked repo", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <WriteReposField
        options={options}
        value={[{ repo: "owner/schema", base: "" }]}
        onChange={onChange}
      />,
    );

    await user.type(screen.getByLabelText("Base branch in owner/schema"), "d");

    expect(onChange).toHaveBeenCalledWith([
      { repo: "owner/schema", base: "d" },
    ]);
  });

  it("removes a picked repo, branch and all", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <WriteReposField
        options={options}
        value={[
          { repo: "owner/schema", base: "deploy" },
          { repo: "other/tooling", base: "" },
        ]}
        onChange={onChange}
      />,
    );

    await user.click(screen.getByTitle("Remove owner/schema"));

    expect(onChange).toHaveBeenCalledWith([
      { repo: "other/tooling", base: "" },
    ]);
  });

  // A task may configure additional base branches for its own target repository
  // or multiple branches for another repository.
  it("offers the repo the task itself is filed against to allow multi-branch targets", async () => {
    const user = userEvent.setup();
    render(
      <WriteReposField
        options={options}
        value={[]}
        onChange={() => {}}
        target="owner/schema"
      />,
    );

    await user.click(screen.getByLabelText(/Another repo/));

    expect(screen.getByText("owner/shared-lib")).toBeInTheDocument();
    expect(screen.getByText("owner/schema")).toBeInTheDocument();
  });

  it("allows multiple branches for the same repository without cross-mutation", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <WriteReposField
        options={options}
        value={[
          { repo: "owner/schema", base: "main" },
          { repo: "owner/schema", base: "release/1.0" },
        ]}
        onChange={onChange}
      />,
    );

    const inputs = screen.getAllByLabelText("Base branch in owner/schema");
    expect(inputs).toHaveLength(2);

    // Typing in the second row updates only the second entry.
    await user.type(inputs[1], "x");
    expect(onChange).toHaveBeenCalledWith([
      { repo: "owner/schema", base: "main" },
      { repo: "owner/schema", base: "release/1.0x" },
    ]);
  });

  it("removes a specific branch entry without removing other branches of the same repo", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <WriteReposField
        options={options}
        value={[
          { repo: "owner/schema", base: "main" },
          { repo: "owner/schema", base: "release/1.0" },
        ]}
        onChange={onChange}
      />,
    );

    const removeButtons = screen.getAllByTitle("Remove owner/schema");
    expect(removeButtons).toHaveLength(2);

    await user.click(removeButtons[0]);
    expect(onChange).toHaveBeenCalledWith([
      { repo: "owner/schema", base: "release/1.0" },
    ]);
  });

  // onCommit is what a caller that PATCHes each edit listens to: onChange
  // fires per keystroke, which would be one request per character of a
  // branch name, each racing the next.
  it("commits on add, on remove and on leaving the branch box -- not per keystroke", async () => {
    const onCommit = vi.fn();
    const user = userEvent.setup();
    const { rerender } = render(
      <WriteReposField
        options={options}
        value={[]}
        onChange={() => {}}
        onCommit={onCommit}
      />,
    );

    await user.click(screen.getByLabelText(/Another repo/));
    await user.click(screen.getByText("owner/schema"));
    expect(onCommit).toHaveBeenCalledWith([{ repo: "owner/schema", base: "" }]);

    const value = [{ repo: "owner/schema", base: "deploy" }];
    rerender(
      <WriteReposField
        options={options}
        value={value}
        onChange={() => {}}
        onCommit={onCommit}
      />,
    );
    onCommit.mockClear();
    await user.type(screen.getByLabelText("Base branch in owner/schema"), "x");
    expect(onCommit).not.toHaveBeenCalled();

    await user.tab();
    expect(onCommit).toHaveBeenCalledWith(value);
  });
});
