import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import StateRepoBrowser from "./StateRepoBrowser.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const tree = {
  head: "ff335f52c0c1d3",
  entries: [
    { path: "README.md", size: 412 },
    { path: "memory", dir: true },
    { path: "memory/deployment.md", size: 1200 },
    { path: "memory/repos", dir: true },
    { path: "memory/repos/grain.md", size: 3872 },
    { path: "schema-version", size: 3 },
    { path: "tables", dir: true },
    { path: "tables/grain_config.json", size: 2048 },
  ],
};

// The listing is not read until somebody asks for it: this hangs off a
// Settings pane people open to change a setting, not to read a working
// tree.
async function show(props) {
  const user = userEvent.setup();
  render(<StateRepoBrowser showError={() => {}} {...props} />);
  await user.click(screen.getByRole("button", { name: "Show files" }));
  return user;
}

describe("StateRepoBrowser", () => {
  afterEach(() => api.mockReset());

  it("reads nothing until it is asked to", async () => {
    api.mockResolvedValue(tree);
    render(<StateRepoBrowser showError={() => {}} status={{}} />);
    expect(api).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Show files" }));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/state-repo/files"),
    );
  });

  it("lists the exported content and says it cannot be edited here", async () => {
    api.mockResolvedValue(tree);
    await show({ status: {} });

    expect(await screen.findByText("tables")).toBeInTheDocument();
    expect(screen.getByText("grain_config.json")).toBeInTheDocument();
    expect(screen.getByText("deployment.md")).toBeInTheDocument();
    // Read-only, and said rather than implied by the absence of an edit
    // box that could not have worked anyway.
    expect(screen.getByText(/pull request/)).toBeInTheDocument();
  });

  it("says which commit is on screen, and when it was exported", async () => {
    api.mockResolvedValue(tree);
    await show({ status: { syncedAt: "2026-09-19T05:26:00Z" } });

    expect(await screen.findByText(/showing ff335f5/)).toBeInTheDocument();
    expect(screen.getByText(/exported .*5:26/)).toBeInTheDocument();
  });

  // The failure a pane like this invites is somebody reading a file the
  // export loop wrote an hour ago and concluding the wrong thing from
  // it. So the commit follows an export.
  it("reports the commit changing after an export", async () => {
    api.mockResolvedValue(tree);
    const user = await show({ status: {}, onSync: () => Promise.resolve() });
    expect(await screen.findByText(/showing ff335f5/)).toBeInTheDocument();

    api.mockResolvedValue({ ...tree, head: "0a1b2c3d4e5f" });
    await user.click(screen.getByRole("button", { name: "Export now" }));
    expect(await screen.findByText(/showing 0a1b2c3/)).toBeInTheDocument();
  });

  it("ignores a file read that lands after another file was opened", async () => {
    let answerSlow;
    api.mockImplementation((path) => {
      if (path === "/api/state-repo/files") return Promise.resolve(tree);
      if (path.includes("grain_config.json")) {
        return new Promise((resolve) => {
          answerSlow = () =>
            resolve({
              path: "tables/grain_config.json",
              size: 30,
              content: '[{"id":1,"time_zone":"Europe/London"}]',
            });
        });
      }
      return Promise.resolve({
        path: "memory/repos/grain.md",
        size: 40,
        content: "# grain\n\nNothing has published a guest image.\n",
      });
    });
    const user = await show({ status: {} });

    await user.click(await screen.findByText("grain_config.json"));
    await user.click(screen.getByText("grain.md"));
    expect(
      await screen.findByRole("heading", { name: "grain" }),
    ).toBeInTheDocument();

    await act(async () => answerSlow());
    expect(screen.getByRole("heading", { name: "grain" })).toBeInTheDocument();
    expect(screen.queryByText(/Europe\/London/)).not.toBeInTheDocument();
  });

  it("pretty-prints a table dump and renders memory as markdown", async () => {
    api.mockImplementation((path) => {
      if (path === "/api/state-repo/files") return Promise.resolve(tree);
      if (path.includes("grain_config.json")) {
        return Promise.resolve({
          path: "tables/grain_config.json",
          size: 30,
          content: '[{"id":1,"time_zone":"Europe/London"}]',
        });
      }
      return Promise.resolve({
        path: "memory/repos/grain.md",
        size: 40,
        content: "# grain\n\nNothing has published a guest image.\n",
      });
    });
    const user = await show({ status: {} });

    await user.click(await screen.findByText("grain_config.json"));
    // Pretty-printed: the key is on a line of its own, which it is not
    // in the file grain writes.
    expect(
      await screen.findByText(/"time_zone": "Europe\/London"/),
    ).toBeInTheDocument();

    await user.click(screen.getByText("grain.md"));
    // Rendered as markdown: the heading is a heading, not a hash.
    expect(
      await screen.findByRole("heading", { name: "grain" }),
    ).toBeInTheDocument();
  });

  it("says when a file came back truncated", async () => {
    api.mockImplementation((path) =>
      Promise.resolve(
        path === "/api/state-repo/files"
          ? tree
          : {
              path: "tables/grain_config.json",
              size: 4 * 1024 * 1024,
              content: "[{",
              truncated: true,
            },
      ),
    );
    const user = await show({ status: {} });
    await user.click(await screen.findByText("grain_config.json"));
    expect(await screen.findByText(/Showing the first/)).toBeInTheDocument();
    expect(screen.getByText(/4.0 MB/)).toBeInTheDocument();
  });

  // A refusal is shown where the file would have been, in the server's
  // own words -- which name what is browsable instead.
  it("shows a refusal beside the file it was asked for", async () => {
    api.mockImplementation((path) =>
      path === "/api/state-repo/files"
        ? Promise.resolve(tree)
        : Promise.reject(
            new Error(
              '".git/config" is not part of grain\'s exported configuration; ' +
                "this repository is browsable at README.md, schema-version and under memory/, tables/",
            ),
          ),
    );
    const user = await show({ status: {} });
    await user.click(await screen.findByText("README.md"));
    expect(
      await screen.findByText(/browsable at README.md/),
    ).toBeInTheDocument();
  });

  it("collapses a directory rather than scrolling every file of it", async () => {
    api.mockResolvedValue(tree);
    const user = await show({ status: {} });

    expect(await screen.findByText("deployment.md")).toBeInTheDocument();
    await user.click(screen.getByTitle("memory"));
    await waitFor(() =>
      expect(screen.queryByText("deployment.md")).not.toBeInTheDocument(),
    );
    // What is under it goes with it, however deep.
    expect(screen.queryByText("grain.md")).not.toBeInTheDocument();
    expect(screen.getByText("grain_config.json")).toBeInTheDocument();
  });

  // The pane this hangs off is mounted without an error reporter in at
  // least one place (SettingsOverlay's State tab), so a failed read must
  // not take the page down with it.
  it("survives a listing that fails with nobody to report it to", async () => {
    api.mockRejectedValue(new Error("the daemon is not answering"));
    const user = userEvent.setup();
    render(<StateRepoBrowser status={{}} />);
    await user.click(screen.getByRole("button", { name: "Show files" }));
    await waitFor(() => expect(api).toHaveBeenCalled());
    expect(
      screen.getByRole("button", { name: "Hide files" }),
    ).toBeInTheDocument();
  });
});
