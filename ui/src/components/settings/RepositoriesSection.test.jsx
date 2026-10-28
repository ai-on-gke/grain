import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import RepositoriesSection, { connectionText } from "./RepositoriesSection.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

const config = { targetRepos: ["acme/widgets", "acme/gadgets"] };

const reach = {
  available: true,
  repos: [
    { repo: "acme/widgets", push: true },
    { repo: "acme/gadgets", push: false },
  ],
  connections: [{ credential: "github-token", host: "github.com", ok: true }],
};

function routeApi() {
  api.mockImplementation((path, opts) => {
    if (path === "/api/deployment/reachable-repos")
      return Promise.resolve(reach);
    if (path === "/api/repos/acme/widgets/declarations")
      return Promise.resolve({ declarations: true, ciMode: "grain" });
    if (
      opts &&
      path === "/api/repos/acme/widgets/base-repair-bypasses-human-gates"
    )
      return Promise.resolve({
        declarations: true,
        ciMode: "grain",
        baseRepairBypassesHumanGates: JSON.parse(opts.body)
          .baseRepairBypassesHumanGates,
      });
    if (path === "/api/repos/acme/widgets/release")
      return Promise.resolve({ baseBranches: ["main", "candidate"] });
    if (path === "/api/repos/acme/gadgets/declarations")
      return Promise.resolve({ declarations: false, ignoreGitHubCI: false });
    if (path === "/api/repos/acme/gadgets/release")
      return Promise.reject(new Error("boom"));
    return Promise.reject(new Error(`unexpected ${path}`));
  });
}

const row = (repo) =>
  screen
    .getAllByTestId("settings-repo-row")
    .find((r) => r.dataset.repo === repo);

describe("RepositoriesSection", () => {
  beforeEach(() => {
    api.mockReset();
  });

  it("shows each repository's base branch, declarations, CI mode and connection", async () => {
    routeApi();
    render(<RepositoriesSection config={config} tasks={[]} />);
    const widgets = row("acme/widgets");
    expect(
      await within(widgets).findByText("main, candidate"),
    ).toBeInTheDocument();
    expect(
      within(widgets).getByTestId("settings-repo-declarations"),
    ).toHaveTextContent("Read");
    expect(within(widgets).getByTestId("settings-repo-ci")).toHaveTextContent(
      "Grain CI",
    );
    expect(within(widgets).getByTestId("settings-repo-git")).toHaveTextContent(
      "github-token",
    );

    const gadgets = row("acme/gadgets");
    expect(await within(gadgets).findByText("Unknown")).toBeInTheDocument();
    expect(await within(gadgets).findByText("Not read")).toBeInTheDocument();
    expect(within(gadgets).getByTestId("settings-repo-ci")).toHaveTextContent(
      "GitHub CI",
    );
    expect(within(gadgets).getByTestId("settings-repo-git")).toHaveTextContent(
      "github-token (read only)",
    );
  });

  it("links each fact to the tab of the repo's page that changes it", async () => {
    routeApi();
    const onOpenRepo = vi.fn();
    const onSection = vi.fn();
    render(
      <RepositoriesSection
        config={config}
        tasks={[]}
        onOpenRepo={onOpenRepo}
        onSection={onSection}
      />,
    );
    const ci = within(row("acme/widgets")).getByTestId("settings-repo-ci");
    const link = await within(ci).findByRole("link", { name: "Grain CI" });
    expect(link).toHaveAttribute("href", "/repos/acme/widgets/settings/ci");
    expect(
      within(row("acme/widgets")).getByRole("link", { name: "acme/widgets" }),
    ).toHaveAttribute("href", "/repos/acme/widgets");

    // Modified clicks open in a new tab instead of intercepting in-app.
    const gitLink = within(
      within(row("acme/widgets")).getByTestId("settings-repo-git"),
    ).getByRole("link", { name: "github-token" });
    const root = link.closest("body > div");
    const leftToBrowser = [];
    const follow = (e) => {
      leftToBrowser.push(!e.defaultPrevented);
      e.preventDefault();
    };
    root.addEventListener("click", follow);
    fireEvent.click(link, { ctrlKey: true });
    fireEvent.click(gitLink, { metaKey: true });
    root.removeEventListener("click", follow);
    expect(leftToBrowser).toEqual([true, true]);
    expect(onOpenRepo).not.toHaveBeenCalled();
    expect(onSection).not.toHaveBeenCalled();

    const user = userEvent.setup();
    await user.click(link);
    expect(onOpenRepo).toHaveBeenCalledWith("acme/widgets", "ci");
    await user.click(gitLink);
    expect(onSection).toHaveBeenCalledWith("git");
  });

  it("toggles base-branch repair human-gate bypass and explains the risk", async () => {
    routeApi();
    const user = userEvent.setup();
    render(<RepositoriesSection config={config} tasks={[]} />);
    const box = await screen.findByLabelText(
      "acme/widgets: Base-branch repair PRs may merge without a human when their CI is green",
    );
    await waitFor(() => expect(box).not.toBeDisabled());
    expect(box).not.toBeChecked();
    expect(
      screen.getByText(
        /Risk: while a base branch is red, an agent repairing it can merge changes to workflow declarations/,
      ),
    ).toBeInTheDocument();
    await user.click(box);
    expect(api).toHaveBeenCalledWith(
      "/api/repos/acme/widgets/base-repair-bypasses-human-gates",
      {
        method: "PUT",
        body: JSON.stringify({ baseRepairBypassesHumanGates: true }),
      },
    );
    expect(box).toBeChecked();
  });

  it("says when no connection lists the repository", () => {
    expect(connectionText(null, "acme/widgets")).toBe("…");
    expect(connectionText(false, "acme/widgets")).toBe("Unknown");
    expect(connectionText({ available: false }, "acme/widgets")).toBe(
      "Not listed",
    );
    expect(connectionText(reach, "acme/other")).toBe("Not reached");
  });
});
