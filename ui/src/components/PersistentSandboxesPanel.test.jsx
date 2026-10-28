import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import api from "../api.js";
import PersistentSandboxesPanel, {
  persistentCommitLine,
  persistentRefreshLine,
  persistentStateColor,
  persistentUptime,
} from "./PersistentSandboxesPanel.jsx";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const running = {
  name: "web",
  repo: "acme/web",
  ref: "main",
  command: "npm start",
  state: "running",
  sandbox: "p1234abcd",
  restarts: 2,
  startedAt: new Date().toISOString(),
  uiSubpath: "/staging/web",
  hostPort: 4280,
};

describe("PersistentSandboxesPanel helpers", () => {
  it("colours each state", () => {
    expect(persistentStateColor("running")).toBe("success");
    expect(persistentStateColor("restarting")).toBe("warning");
    expect(persistentStateColor("stopped")).toBe("default");
  });
  it("reports uptime only while running", () => {
    const now = Date.parse("2026-10-19T10:00:00Z");
    expect(
      persistentUptime(
        { state: "running", startedAt: "2026-10-19T09:58:00Z" },
        now,
      ),
    ).toBe("2m");
    expect(
      persistentUptime(
        { state: "running", startedAt: "2026-10-18T08:00:00Z" },
        now,
      ),
    ).toBe("1d 2h");
    expect(
      persistentUptime(
        { state: "stopped", startedAt: "2026-10-19T09:58:00Z" },
        now,
      ),
    ).toBe("");
  });
});

describe("PersistentSandboxesPanel refresh lines", () => {
  it("names the running commit and a policy other than manual", () => {
    expect(
      persistentCommitLine({
        commit: "1a2b3c4d5e6f7a8b9c0d",
        refresh: "after_green",
      }),
    ).toBe("running 1a2b3c4d5e6f · after_green");
    expect(persistentCommitLine({ commit: "abc", refresh: "manual" })).toBe(
      "running abc",
    );
    expect(persistentCommitLine({ refresh: "manual" })).toBe("");
  });
  it("says why it last refreshed, and nothing before it has", () => {
    const now = Date.parse("2026-10-19T10:00:00Z");
    expect(
      persistentRefreshLine(
        {
          lastRefresh: {
            at: "2026-10-19T08:00:00Z",
            from: "a",
            to: "b",
            reason: "candidate moved; CI green at b",
          },
        },
        now,
      ),
    ).toBe("refreshed 2h ago: candidate moved; CI green at b");
    expect(persistentRefreshLine({}, now)).toBe("");
  });
});

describe("PersistentSandboxesPanel", () => {
  beforeEach(() => {
    api.mockReset();
  });

  it("explains persistent sandboxes and links to the director when none are defined", () => {
    const onOpenSession = vi.fn();
    render(
      <PersistentSandboxesPanel sandboxes={[]} onOpenSession={onOpenSession} />,
    );
    const panel = screen.getByTestId("persistent-sandboxes");
    expect(panel).toHaveTextContent("Persistent sandboxes");
    expect(panel).toHaveTextContent(
      /Persistent sandboxes keep a long-lived service running/,
    );
    expect(panel).toHaveTextContent(
      /ask the director to set one up — e\.g\. 'set up a staging environment for bwsalmon\/grain on main'/i,
    );
    expect(panel).toHaveTextContent("tables/persistent_sandbox.json");
    expect(screen.queryByRole("table")).toBeNull();

    const directorLink = screen.getByRole("link", { name: "director" });
    expect(directorLink).toHaveAttribute("href", "/session/director");
    fireEvent.click(directorLink);
    expect(onOpenSession).toHaveBeenCalledWith("director");

    const docsLink = screen.getByRole("link", {
      name: "docs/persistent-sandboxes.md",
    });
    expect(docsLink).toHaveAttribute(
      "href",
      "https://github.com/bwsalmon/grain/blob/main/docs/persistent-sandboxes.md",
    );
  });

  it("shows where a sandbox is reached and posts its controls", async () => {
    api.mockResolvedValue({ status: "ok", logs: "listening on 8080\n" });
    const onChange = vi.fn();
    render(
      <PersistentSandboxesPanel sandboxes={[running]} onChange={onChange} />,
    );
    expect(screen.getByRole("link", { name: "/staging/web/" })).toHaveAttribute(
      "href",
      "/staging/web/",
    );
    expect(screen.getByText("host port 4280")).toBeInTheDocument();
    expect(screen.getByText("acme/web@main")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Rebuild" }));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/persistent-sandboxes/web/rebuild",
        { method: "POST" },
      ),
    );
    await waitFor(() => expect(onChange).toHaveBeenCalled());

    fireEvent.click(screen.getByRole("button", { name: "Logs" }));
    expect(
      await screen.findByText(/listening on 8080/, {
        selector: "[data-testid=persistent-sandbox-logs]",
      }),
    ).toBeInTheDocument();
  });

  it("shows the running commit and the last refresh", () => {
    render(
      <PersistentSandboxesPanel
        sandboxes={[
          {
            ...running,
            commit: "1a2b3c4d5e6f7a8b",
            refresh: "after_green",
            lastRefresh: {
              at: new Date().toISOString(),
              to: "1a2b3c4d5e6f7a8b",
              reason: "main moved; CI green at 1a2b3c4d5e6f",
            },
          },
        ]}
      />,
    );
    expect(screen.getByTestId("persistent-commit")).toHaveTextContent(
      "running 1a2b3c4d5e6f · after_green",
    );
    expect(screen.getByTestId("persistent-refresh")).toHaveTextContent(
      "main moved; CI green at 1a2b3c4d5e6f",
    );
  });

  it("says whether the sandbox goes through the package cache", () => {
    render(
      <PersistentSandboxesPanel
        sandboxes={[
          {
            ...running,
            packageCache:
              "its sandbox fetches apt, pip, npm and Go packages, and Docker Hub images, through this deployment's package cache at http://192.168.9.1:9729",
          },
        ]}
      />,
    );
    expect(screen.getByTestId("persistent-package-cache")).toHaveTextContent(
      /Package cache: its sandbox fetches .* at http:\/\/192\.168\.9\.1:9729/,
    );
  });

  it("offers Start for a stopped sandbox", () => {
    render(
      <PersistentSandboxesPanel
        sandboxes={[{ ...running, state: "stopped" }]}
      />,
    );
    expect(screen.getByRole("button", { name: "Start" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Restart" })).toBeNull();
  });
});

describe("PersistentSandboxesPanel links", () => {
  beforeEach(() => {
    api.mockReset();
  });

  const hostOnly = {
    name: "grain-staging",
    repo: "bwsalmon/grain",
    ref: "candidate",
    command: "grain demo",
    state: "restarting",
    sandbox: "p-grain-staging",
    restarts: 3,
    hostPort: 4280,
    lastError: "exited with status 1",
    events: [
      { kind: "exit", message: "the command exited with status 1 after 3s" },
      { kind: "start", message: 'running "grain demo"' },
    ],
  };

  it("links a host port, shows the last restart's reason and a logs link", () => {
    render(
      <PersistentSandboxesPanel
        sandboxes={[hostOnly]}
        config={{ listenPort: Number(window.location.port) || 80 }}
      />,
    );
    expect(screen.getByRole("link", { name: "port 4280" })).toHaveAttribute(
      "href",
      `${window.location.protocol}//${window.location.hostname}:4280/`,
    );
    expect(
      screen.getByTestId("persistent-sandbox-restart-reason"),
    ).toHaveTextContent("the command exited with status 1 after 3s");
    expect(screen.getByRole("link", { name: "Raw" })).toHaveAttribute(
      "href",
      "/api/persistent-sandboxes/grain-staging/logs?format=text",
    );
    expect(
      screen.queryByTestId("persistent-sandbox-proxy-warning"),
    ).not.toBeInTheDocument();
  });

  it("warns that a host port will not load through a proxy", async () => {
    api.mockResolvedValue({ listenPort: 8080, proxied: true });
    render(<PersistentSandboxesPanel sandboxes={[hostOnly]} />);
    await waitFor(() =>
      expect(
        screen.getByTestId("persistent-sandbox-proxy-warning"),
      ).toBeInTheDocument(),
    );
    expect(api).toHaveBeenCalledWith("/api/config");
  });
});
