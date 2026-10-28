import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import api from "../../api.js";
import GitConnections, { deploymentRepos } from "./GitConnections.jsx";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

// Who the deployment's agents push and commit as. Its git access is its
// connections (GitConnections.connections.test.jsx and
// GitConnections.repos.test.jsx).

const deployment = {
  id: "default",
  targetRepos: ["acme/widgets"],
  repos: [{ repo: "acme/gadgets" }, { repo: "acme/widgets" }],
};

beforeEach(() => {
  api.mockReset();
  api.mockImplementation((path) => {
    if (path === "/api/deployment/repos/acme/gadgets/config") {
      return Promise.resolve({
        config: { gitCredential: "grain-team-override" },
      });
    }
    return Promise.resolve({});
  });
});

describe("GitConnections", () => {
  it("lists the deployment's repos once each, target repos first", () => {
    expect(deploymentRepos(deployment)).toEqual([
      "acme/widgets",
      "acme/gadgets",
    ]);
    expect(deploymentRepos({ repos: [{ repo: "acme/*" }] })).toEqual([]);
  });

  it("says where to add repos when the deployment names none", () => {
    render(<GitConnections deployment={{ id: "default" }} onSave={vi.fn()} />);
    expect(
      screen.getByText(/names no target repositories yet/),
    ).toBeInTheDocument();
  });

  it("names grain's own identity as the fallback for a credential that names nobody", () => {
    render(
      <GitConnections
        deployment={{ id: "default" }}
        deploymentGitName="grain agent"
        deploymentGitEmail="grain-agent@localhost"
      />,
    );

    expect(screen.getByTestId("commit-author-derived")).toHaveTextContent(
      "commits as grain agent <grain-agent@localhost>.",
    );
    expect(
      screen.queryByRole("textbox", { name: /commit author/i }),
    ).toBeNull();
  });

  it("says so rather than drawing an empty form when the deployment has not arrived", () => {
    render(<GitConnections deployment={null} onSave={vi.fn()} />);
    expect(
      screen.getByText(/has not answered with its connections/),
    ).toBeInTheDocument();
  });
});

// The typed editor (grain/task-2366): PAT, SSH key, GitHub App or none,
// each saved write-only, with only the type and what is not secret said
// back.
