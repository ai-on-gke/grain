import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import GitConnections from "./GitConnections.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

// The Repositories section under the deployment's git connections
// (grain/task-3929): each of the deployment's repositories says which
// connection it resolves to, and a picker records another as a
// connection scoped to that repository, or removes it for "Default".

const deployment = {
  id: "default",
  targetRepos: ["acme/widgets", "other/thing"],
};

const view = (credential, scope = "", host = "github.com") => ({
  credential,
  host,
  kind: "github",
  scope,
  authType: "pat",
  status: { configured: true, type: "pat" },
});

// fakeServer answers the connections list and the picker's PUT the way
// ui.Client.SetTeamRepoGitConnection does: the repository's own entry
// replaced, first in the list, or removed for an empty credential.
function fakeServer(initial) {
  let connections = [...initial];
  api.mockImplementation((url, opts) => {
    if (url === "/api/deployment/git-connections" && !opts) {
      return Promise.resolve(connections);
    }
    const m = url.match(
      /^\/api\/deployment\/repos\/([^/]+)\/([^/]+)\/git-connection$/,
    );
    if (m && opts?.method === "PUT") {
      const { credential } = JSON.parse(opts.body);
      const scope = `${m[1]}/${m[2]}`;
      const rest = connections.filter((c) => c.scope !== scope);
      const picked = connections.find((c) => c.credential === credential);
      connections = credential ? [{ ...picked, scope }, ...rest] : rest;
      return Promise.resolve({ id: "infra" });
    }
    return Promise.resolve({});
  });
}

const renderPanel = (props = {}) =>
  render(
    <GitConnections
      deployment={deployment}
      onSave={vi.fn()}
      onDeploymentChanged={vi.fn()}
      showError={props.showError || vi.fn()}
      canManageTokens={props.canManageTokens ?? true}
    />,
  );

const row = (repo) => screen.getByTestId(`repo-connection-${repo}`);

describe("GitConnections repositories", () => {
  beforeEach(() => {
    api.mockReset();
  });

  it("no longer offers a single token or per-repository tokens", async () => {
    fakeServer([view("team-default")]);
    renderPanel();
    await screen.findByTestId("repo-connection-resolved-acme/widgets");
    expect(screen.queryByText("GitHub token")).not.toBeInTheDocument();
    expect(screen.queryByText("Per-repository tokens")).not.toBeInTheDocument();
    expect(api).not.toHaveBeenCalledWith(
      expect.stringContaining("github-credential"),
    );
    expect(api).not.toHaveBeenCalledWith(
      expect.stringContaining("github-credential"),
      expect.anything(),
    );
  });

  it("says which connection each repository resolves to: repository, then owner, then the host default", async () => {
    fakeServer([
      view("team-default"),
      view("acme-owner", "acme"),
      view("narrow", "other/thing"),
    ]);
    renderPanel();
    expect(
      await screen.findByTestId("repo-connection-resolved-acme/widgets"),
    ).toHaveTextContent("acme on github.com (PAT), the owner's connection");
    expect(
      screen.getByTestId("repo-connection-resolved-other/thing"),
    ).toHaveTextContent(
      "other/thing on github.com (PAT), chosen for this repository",
    );
  });

  it("picks a connection for a repository, and puts it back to the default", async () => {
    const user = userEvent.setup();
    fakeServer([view("team-default"), view("acme-owner", "acme")]);
    renderPanel();
    expect(
      await screen.findByTestId("repo-connection-resolved-acme/widgets"),
    ).toHaveTextContent("the owner's connection");

    await user.click(within(row("acme/widgets")).getByRole("combobox"));
    await user.click(
      await screen.findByRole("option", {
        name: "github.com default (PAT)",
      }),
    );
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/deployment/repos/acme/widgets/git-connection",
        {
          method: "PUT",
          body: JSON.stringify({
            credential: "team-default",
            host: "github.com",
          }),
        },
      ),
    );
    await waitFor(() =>
      expect(
        screen.getByTestId("repo-connection-resolved-acme/widgets"),
      ).toHaveTextContent(
        "acme/widgets on github.com (PAT), chosen for this repository",
      ),
    );
    // The picked entry is the repository's choice, not another
    // connection: the list still shows the credential once.
    expect(screen.getAllByTestId("connection-row-team-default")).toHaveLength(
      1,
    );

    await user.click(within(row("acme/widgets")).getByRole("combobox"));
    await user.click(await screen.findByRole("option", { name: "Default" }));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/deployment/repos/acme/widgets/git-connection",
        {
          method: "PUT",
          body: JSON.stringify({ credential: "", host: "github.com" }),
        },
      ),
    );
    await waitFor(() =>
      expect(
        screen.getByTestId("repo-connection-resolved-acme/widgets"),
      ).toHaveTextContent("the owner's connection"),
    );
  });

  it("says when no connection reaches a repository and its tasks are held", async () => {
    fakeServer([
      view("gitlab", "", "gitlab.example.com"),
      view("acme", "acme"),
    ]);
    renderPanel();
    expect(
      await screen.findByTestId("repo-connection-none-other/thing"),
    ).toHaveTextContent("its tasks are held");
    expect(
      screen.getByTestId("repo-connection-resolved-acme/widgets"),
    ).toBeInTheDocument();
  });

  it("shows a plain member what each repository uses, with the picker disabled", async () => {
    fakeServer([view("team-default")]);
    renderPanel({ canManageTokens: false });
    expect(
      await screen.findByTestId("repo-connection-resolved-acme/widgets"),
    ).toHaveTextContent("the host's default");
    expect(within(row("acme/widgets")).getByRole("combobox")).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    expect(
      screen.queryByTestId("add-connection-button"),
    ).not.toBeInTheDocument();
  });
});
