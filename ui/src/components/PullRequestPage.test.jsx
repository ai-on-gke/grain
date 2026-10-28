import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import PullRequestPage from "./PullRequestPage.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// A one-line change to one file, as git writes it -- the shape the
// compare API returns and DiffView.jsx parses.
const patch = (file, before, after) =>
  [
    `diff --git a/${file} b/${file}`,
    "index 1111111..2222222 100644",
    `--- a/${file}`,
    `+++ b/${file}`,
    "@@ -1,1 +1,1 @@",
    `-${before}`,
    `+${after}`,
    "",
  ].join("\n");

function renderPage(overrides = {}) {
  const props = {
    repo: "acme/widgets",
    comparison: { base: "", head: "grain/task-12" },
    onBack: vi.fn(),
    showError: vi.fn(),
    ...overrides,
  };
  render(<PullRequestPage {...props} />);
  return props;
}

describe("PullRequestPage", () => {
  it("draws the change a branch brings to the base the server resolved", async () => {
    api.mockResolvedValue({
      base: "trunk",
      head: "grain/task-12",
      mergeBase: "fork4200000000",
      merged: false,
      diff: patch("notes.txt", "settled", "work in progress"),
    });
    renderPage();

    // DiffView.jsx draws it: its own suite covers the rendering, and
    // what this page owes is handing it the diff the API answered with.
    const changes = await screen.findByLabelText("Branch changes");
    expect(changes).toHaveTextContent("notes.txt");
    expect(changes).toHaveTextContent("work in progress");
    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/compare?base=&head=grain%2Ftask-12",
    );
    // The base the server resolved, not the empty one that was asked
    // for: only the server knows what this repo's default branch is.
    expect(screen.getByText("trunk")).toBeInTheDocument();
    expect(screen.getByText("Open")).toBeInTheDocument();
    expect(screen.getByText(/forked at fork4200/)).toBeInTheDocument();
  });

  // The whole point of grain/task-375: a task's own pull-request link is
  // most often followed after the task has finished, and the comparison
  // between two branch tips is empty by then. The server rebuilds the
  // change from the merge, and this page says that is what is on screen.
  it("says so when the branch has already merged, and still shows what it changed", async () => {
    api.mockResolvedValue({
      base: "main",
      head: "grain/task-6",
      mergeBase: "fork9900000000",
      merged: true,
      diff: patch("tables/template.json", "old name", "internal change"),
    });
    renderPage({
      repo: "grain/config",
      comparison: { base: "main", head: "grain/task-6" },
    });

    expect(await screen.findByLabelText("Branch changes")).toHaveTextContent(
      "internal change",
    );
    expect(api).toHaveBeenCalledWith(
      "/api/repos/grain/config/compare?base=main&head=grain%2Ftask-6",
    );
    expect(screen.getByText("Merged")).toBeInTheDocument();
    expect(screen.getByText(/Merged into/)).toBeInTheDocument();
  });

  it("says when two refs really do differ by nothing", async () => {
    api.mockResolvedValue({
      base: "main",
      head: "grain/task-12",
      merged: false,
      diff: "",
    });
    renderPage();

    expect(
      await screen.findByText("There are no changes between these branches."),
    ).toBeInTheDocument();
  });

  // A failed read is the page here, not a corner of it: a branch nobody
  // has pushed, a ref that has gone, a daemon whose GitHub client has
  // not started. Said on the page as well as through showError, which is
  // where every other failure in the app is reported.
  it("shows the read failing where the diff would have been", async () => {
    api.mockRejectedValue(new Error('branch "grain/task-12" does not exist'));
    const { showError } = renderPage();

    expect(
      await screen.findByText(/does not exist/, { selector: "*" }),
    ).toBeInTheDocument();
    expect(showError).toHaveBeenCalled();
    expect(screen.queryByLabelText("Branch changes")).not.toBeInTheDocument();
  });

  it("goes back to the repo the pull request belongs to", async () => {
    api.mockResolvedValue({
      base: "main",
      head: "grain/task-12",
      diff: patch("a.txt", "one", "two"),
    });
    const { onBack } = renderPage();
    await screen.findByLabelText("Branch changes");

    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: /acme\/widgets/ }));
    expect(onBack).toHaveBeenCalled();
  });
});
