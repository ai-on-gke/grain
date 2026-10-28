import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import BranchField, { clearBranchCache } from "./BranchField.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const grainBranches = [
  { repo: "acme/widgets", name: "release/2", status: "adopted" },
  { repo: "acme/widgets", name: "rel.latest", status: "created" },
];

describe("BranchField", () => {
  afterEach(() => {
    api.mockReset();
    clearBranchCache();
  });

  it("offers the branches grain knows about as soon as the box is focused", async () => {
    api.mockResolvedValue(grainBranches);
    const user = userEvent.setup();
    render(<BranchField repo="acme/widgets" />);

    await user.click(screen.getByLabelText("Base branch"));

    expect(await screen.findByText("release/2")).toBeInTheDocument();
    expect(screen.getByText("rel.latest")).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/branches");
  });

  it("asks about a host-qualified repo with its host in the owner segment", async () => {
    api.mockResolvedValue([]);
    const user = userEvent.setup();
    render(<BranchField repo="git.forge.example/experimental/bsalmon-test" />);

    await user.click(screen.getByLabelText("Base branch"));

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/repos/git.forge.example%2Fexperimental/bsalmon-test/branches",
      ),
    );
    expect(api).toHaveBeenCalledWith(
      "/api/repos/git.forge.example%2Fexperimental/bsalmon-test/github-branches",
    );
  });

  it("merges GitHub branches after the caller and grain branches", async () => {
    api.mockImplementation((path) =>
      Promise.resolve(
        path.endsWith("/github-branches")
          ? ["topic/new", "release/2"]
          : grainBranches,
      ),
    );
    const user = userEvent.setup();
    render(<BranchField repo="acme/widgets" options={["main"]} />);

    await user.click(screen.getByLabelText("Base branch"));
    await screen.findByText("topic/new");

    expect(screen.getAllByRole("menuitem").map((li) => li.textContent)).toEqual(
      ["main", "release/2", "rel.latest", "topic/new"],
    );
    expect(api).toHaveBeenCalledWith("/api/repos/acme/widgets/github-branches");
  });

  it("keeps grain suggestions when GitHub cannot be read", async () => {
    api.mockImplementation((path) =>
      path.endsWith("/github-branches")
        ? Promise.reject(new Error("no credential"))
        : Promise.resolve(grainBranches),
    );
    const user = userEvent.setup();
    render(<BranchField repo="acme/widgets" />);

    await user.click(screen.getByLabelText("Base branch"));

    expect(await screen.findByText("release/2")).toBeInTheDocument();
  });

  // grain/task-372: every row here names a branch, and a branch has a
  // figure now -- which is also what tells this popper apart from the
  // repo picker and the task picker it is shaped like, on a form
  // carrying two of the three at once.
  it("marks each suggestion with the branches figure", async () => {
    api.mockResolvedValue(grainBranches);
    const user = userEvent.setup();
    render(<BranchField repo="acme/widgets" />);

    await user.click(screen.getByLabelText("Base branch"));

    expect(
      (await screen.findByText("release/2"))
        .closest("li")
        .querySelector('svg[data-glyph="branches"]'),
    ).toBeInTheDocument();
  });

  it("offers the caller's own branches ahead of grain's, without repeating one", async () => {
    api.mockResolvedValue(grainBranches);
    const user = userEvent.setup();
    render(<BranchField repo="acme/widgets" options={["main", "release/2"]} />);

    await user.click(screen.getByLabelText("Base branch"));
    await screen.findByText("rel.latest");

    const rows = screen.getAllByRole("menuitem").map((li) => li.textContent);
    expect(rows).toEqual(["main", "release/2", "rel.latest"]);
  });

  it("narrows the list to what has been typed, case-insensitively", async () => {
    api.mockResolvedValue(grainBranches);
    const user = userEvent.setup();
    render(<BranchField repo="acme/widgets" options={["main"]} />);

    await user.type(screen.getByLabelText("Base branch"), "REL");
    await waitFor(() =>
      expect(screen.getByText("rel.latest")).toBeInTheDocument(),
    );

    expect(screen.getByText("release/2")).toBeInTheDocument();
    expect(screen.queryByText("main")).not.toBeInTheDocument();
  });

  it("reports a clicked branch as both a change and a settled value", async () => {
    api.mockResolvedValue(grainBranches);
    const onChange = vi.fn();
    const onCommit = vi.fn();
    const user = userEvent.setup();
    render(
      <BranchField
        repo="acme/widgets"
        onChange={onChange}
        onCommit={onCommit}
      />,
    );

    await user.click(screen.getByLabelText("Base branch"));
    await user.click(await screen.findByText("release/2"));

    expect(onChange).toHaveBeenLastCalledWith("release/2");
    expect(onCommit).toHaveBeenLastCalledWith("release/2");
    expect(screen.getByLabelText("Base branch")).toHaveValue("release/2");
  });

  it("picks the highlighted branch with the arrow keys and Enter", async () => {
    api.mockResolvedValue(grainBranches);
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(<BranchField repo="acme/widgets" onChange={onChange} />);

    await user.click(screen.getByLabelText("Base branch"));
    await screen.findByText("release/2");
    await user.keyboard("{ArrowDown}{Enter}");

    expect(onChange).toHaveBeenLastCalledWith("rel.latest");
  });

  // The list is the branch names, so Enter on it must never be the key
  // that submits the form the box sits in.
  it("does not submit the form when Enter takes a suggestion", async () => {
    api.mockResolvedValue(grainBranches);
    const onSubmit = vi.fn((e) => e.preventDefault());
    const user = userEvent.setup();
    render(
      <form onSubmit={onSubmit}>
        <BranchField repo="acme/widgets" />
      </form>,
    );

    await user.click(screen.getByLabelText("Base branch"));
    await screen.findByText("release/2");
    await user.keyboard("{Enter}");

    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Base branch")).toHaveValue("release/2");
  });

  it("keeps a typed branch nothing suggests, and commits it on blur", async () => {
    api.mockResolvedValue(grainBranches);
    const onCommit = vi.fn();
    const user = userEvent.setup();
    render(
      <>
        <BranchField repo="acme/widgets" onCommit={onCommit} />
        <button type="button">elsewhere</button>
      </>,
    );

    await user.type(screen.getByLabelText("Base branch"), "wip/nothing-knows");
    await user.click(screen.getByRole("button", { name: "elsewhere" }));

    expect(onCommit).toHaveBeenLastCalledWith("wip/nothing-knows");
    expect(screen.getByLabelText("Base branch")).toHaveValue(
      "wip/nothing-knows",
    );
  });

  it("asks about no branches at all until the box is opened", async () => {
    api.mockResolvedValue(grainBranches);
    render(<BranchField repo="acme/widgets" />);

    expect(api).not.toHaveBeenCalled();
  });

  it("asks nothing when there is no repo to ask about", async () => {
    api.mockResolvedValue(grainBranches);
    const user = userEvent.setup();
    render(<BranchField repo="" options={["main"]} />);

    await user.click(screen.getByLabelText("Base branch"));

    expect(await screen.findByText("main")).toBeInTheDocument();
    expect(api).not.toHaveBeenCalled();
  });

  it("stays an ordinary text box when the branches cannot be read", async () => {
    api.mockRejectedValue(new Error("repository not found"));
    const user = userEvent.setup();
    render(<BranchField repo="acme/widgets" />);

    await user.type(screen.getByLabelText("Base branch"), "main");

    expect(screen.getByLabelText("Base branch")).toHaveValue("main");
    expect(screen.queryByRole("menuitem")).not.toBeInTheDocument();
  });

  it("asks for a repo's branches once, however many boxes want them", async () => {
    api.mockResolvedValue(grainBranches);
    const user = userEvent.setup();
    render(
      <>
        <BranchField repo="acme/widgets" ariaLabel="Base branch one" />
        <BranchField repo="acme/widgets" ariaLabel="Base branch two" />
      </>,
    );

    await user.click(screen.getByLabelText("Base branch one"));
    await screen.findByText("release/2");
    await user.click(screen.getByLabelText("Base branch two"));
    await screen.findAllByText("release/2");

    expect(api).toHaveBeenCalledTimes(2);
  });

  // A list whose only row is the branch already typed has nothing left
  // to offer, and would sit over whatever button comes next.
  it("closes the list once the box holds the only branch it matches", async () => {
    api.mockResolvedValue(grainBranches);
    const user = userEvent.setup();
    render(<BranchField repo="acme/widgets" />);

    await user.type(screen.getByLabelText("Base branch"), "rel.");
    expect(await screen.findByRole("menuitem", { name: "rel.latest" }));

    await user.type(screen.getByLabelText("Base branch"), "latest");

    expect(screen.queryByRole("menuitem")).not.toBeInTheDocument();
  });

  it("holds the value for a caller that reads the form at submit", async () => {
    api.mockResolvedValue([]);
    const user = userEvent.setup();
    render(
      <form aria-label="run">
        <BranchField name="base" repo="acme/widgets" defaultValue="main" />
      </form>,
    );

    await user.type(screen.getByLabelText("Base branch"), "-2");

    expect(
      new FormData(screen.getByRole("form", { name: "run" })).get("base"),
    ).toBe("main-2");
  });
  // grain/task-1620: a form that asks is warned about a branch GitHub
  // does not have, and only when GitHub actually answered.
  describe("warnMissing", () => {
    const github = (refs) => (path) =>
      path.endsWith("/github-branches")
        ? refs instanceof Error
          ? Promise.reject(refs)
          : Promise.resolve(refs)
        : Promise.resolve(grainBranches);

    it("warns about a branch GitHub's listing does not name", async () => {
      api.mockImplementation(github(["main"]));
      render(
        <BranchField
          repo="acme/widgets"
          defaultValue="no-such-branch"
          helperText="optional"
          warnMissing="each firing fails until it is cut"
        />,
      );
      expect(
        await screen.findByText(
          'No branch "no-such-branch" on acme/widgets: each firing fails until it is cut',
        ),
      ).toBeInTheDocument();
      expect(screen.queryByText("optional")).not.toBeInTheDocument();
    });

    it("stays quiet for a branch GitHub or grain has", async () => {
      api.mockImplementation(github(["main"]));
      const { rerender } = render(
        <BranchField
          repo="acme/widgets"
          defaultValue="main"
          helperText="optional"
          warnMissing="x"
        />,
      );
      await waitFor(() =>
        expect(api).toHaveBeenCalledWith(
          "/api/repos/acme/widgets/github-branches",
        ),
      );
      rerender(
        <BranchField
          repo="acme/widgets"
          value="release/2"
          helperText="optional"
          warnMissing="x"
        />,
      );
      await new Promise((r) => setTimeout(r, 600));
      expect(screen.getByText("optional")).toBeInTheDocument();
      expect(screen.queryByText(/No branch/)).not.toBeInTheDocument();
    });

    it("stays quiet when GitHub cannot be read", async () => {
      api.mockImplementation(github(new Error("no credential")));
      render(
        <BranchField
          repo="acme/widgets"
          defaultValue="no-such-branch"
          helperText="optional"
          warnMissing="x"
        />,
      );
      await waitFor(() =>
        expect(api).toHaveBeenCalledWith(
          "/api/repos/acme/widgets/github-branches",
        ),
      );
      await new Promise((r) => setTimeout(r, 100));
      expect(screen.getByText("optional")).toBeInTheDocument();
    });

    it("never asks when the caller did not opt in", async () => {
      api.mockImplementation(github(["main"]));
      render(<BranchField repo="acme/widgets" defaultValue="no-such-branch" />);
      await new Promise((r) => setTimeout(r, 600));
      expect(api).not.toHaveBeenCalled();
    });
  });

  // As RepoPicker's: the Escape that closes the list must not also close
  // the New task drawer the field sits in.
  it("keeps the Escape that closes the list from the drawer around it", async () => {
    api.mockResolvedValue(grainBranches);
    const user = userEvent.setup();
    const onDrawerKey = vi.fn();
    render(
      <div onKeyDown={(e) => e.key === "Escape" && onDrawerKey()}>
        <BranchField repo="acme/widgets" />
      </div>,
    );

    await user.click(screen.getByLabelText("Base branch"));
    expect(await screen.findByText("release/2")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(screen.queryByText("release/2")).not.toBeInTheDocument();
    expect(onDrawerKey).not.toHaveBeenCalled();

    await user.keyboard("{Escape}");
    expect(onDrawerKey).toHaveBeenCalledTimes(1);
  });
});
