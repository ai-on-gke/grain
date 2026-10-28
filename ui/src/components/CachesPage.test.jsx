import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import CachesPage from "./CachesPage.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

describe("CachesPage", () => {
  afterEach(() => api.mockReset());

  it("lists what both proxy and actions caches hold", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        cache: [
          { kind: "apt", entries: 412, bytes: 3 * (1 << 30) },
          { kind: "npm", entries: 7, bytes: 1 << 20 },
        ],
      })
      .mockResolvedValueOnce({
        enabled: true,
        usedByJobs: true,
        entries: 12,
        bytes: 3 * (1 << 30),
        maxBytes: 20 * (1 << 30),
        repos: [
          { repo: "acme/widgets", bytes: 2 * (1 << 30) },
          { repo: "acme/gadgets", bytes: 1 << 30 },
        ],
      });

    render(<CachesPage showError={() => {}} />);

    expect(
      await screen.findByRole("heading", { name: "Caches" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: "Proxy cache" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: "Actions cache" }),
    ).toBeInTheDocument();

    expect(api).toHaveBeenCalledWith("/api/package-cache");
    expect(api).toHaveBeenCalledWith("/api/actions-cache");

    expect(
      screen.getByText("apt packages: 412 packages, 3.0 GiB"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("npm packages: 7 packages, 1.0 MiB"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Total space consumed: 3\.0 GiB/),
    ).toBeInTheDocument();

    expect(
      screen.getByText("12 entries, 3.0 GiB of 20.0 GiB"),
    ).toBeInTheDocument();
    expect(screen.getByText("acme/widgets: 2.0 GiB")).toBeInTheDocument();
    expect(screen.getByText("acme/gadgets: 1.0 GiB")).toBeInTheDocument();
  });

  it("flushes one proxy cache ecosystem and redraws from what the flush answered", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        cache: [{ kind: "apt", entries: 412, bytes: 3 * (1 << 30) }],
      })
      .mockResolvedValueOnce({
        enabled: true,
        usedByJobs: true,
        entries: 0,
        bytes: 0,
        maxBytes: 20 * (1 << 30),
        repos: [],
      });

    const user = userEvent.setup();
    render(<CachesPage showError={() => {}} />);
    await screen.findByText("apt packages: 412 packages, 3.0 GiB");

    api.mockResolvedValueOnce({
      kind: "apt",
      entries: 412,
      bytes: 3 * (1 << 30),
      cache: [{ kind: "apt", entries: 0, bytes: 0 }],
    });
    await user.click(
      screen.getByRole("button", { name: "Flush apt packages" }),
    );

    expect(api).toHaveBeenCalledWith("/api/package-cache/flush?kind=apt", {
      method: "POST",
    });
    expect(
      await screen.findByText("apt packages: 0 packages, 0 B"),
    ).toBeInTheDocument();
  });

  it("flushes one actions cache repository and redraws from what the flush answered", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        cache: [],
      })
      .mockResolvedValueOnce({
        enabled: true,
        usedByJobs: true,
        entries: 12,
        bytes: 3 * (1 << 30),
        maxBytes: 20 * (1 << 30),
        repos: [{ repo: "acme/widgets", bytes: 3 * (1 << 30) }],
      });

    const user = userEvent.setup();
    render(<CachesPage showError={() => {}} />);
    await screen.findByText("acme/widgets: 3.0 GiB");

    api.mockResolvedValueOnce({
      repo: "acme/widgets",
      entries: 12,
      bytes: 3 * (1 << 30),
      cache: {
        enabled: true,
        usedByJobs: true,
        entries: 0,
        bytes: 0,
        maxBytes: 20 * (1 << 30),
        repos: [],
      },
    });
    await user.click(
      screen.getByRole("button", {
        name: "Flush the Actions cache of acme/widgets",
      }),
    );

    expect(api).toHaveBeenCalledWith(
      "/api/actions-cache/flush?repo=acme%2Fwidgets",
      { method: "POST" },
    );
    expect(
      await screen.findByText("empty (0 B of 20.0 GiB)"),
    ).toBeInTheDocument();
  });

  it("says so when this deployment runs neither cache", async () => {
    api
      .mockResolvedValueOnce({ enabled: false })
      .mockResolvedValueOnce({ enabled: false });

    render(<CachesPage showError={() => {}} />);

    expect(
      await screen.findByText(/not running a proxy cache/),
    ).toBeInTheDocument();
    expect(
      await screen.findByText(/keeps no Actions cache/),
    ).toBeInTheDocument();
  });

  it("warns when the actions cache is kept but no job is handed it", async () => {
    api
      .mockResolvedValueOnce({ enabled: true, cache: [] })
      .mockResolvedValueOnce({
        enabled: true,
        usedByJobs: false,
        entries: 5,
        bytes: 1 << 30,
        maxBytes: 20 * (1 << 30),
        repos: [{ repo: "acme/widgets", bytes: 1 << 30 }],
      });

    render(<CachesPage showError={() => {}} />);

    expect(
      await screen.findByText(/No CI job is being handed this cache/),
    ).toBeInTheDocument();
  });

  it("distinguishes empty caches from disabled caches", async () => {
    api
      .mockResolvedValueOnce({ enabled: true, cache: null })
      .mockResolvedValueOnce({
        enabled: true,
        usedByJobs: true,
        entries: 0,
        bytes: 0,
        maxBytes: 20 * (1 << 30),
        repos: [],
      });

    render(<CachesPage showError={() => {}} />);

    expect(
      await screen.findByText(/Total space consumed: 0 B/),
    ).toBeInTheDocument();
    expect(screen.getByText("empty (0 B of 20.0 GiB)")).toBeInTheDocument();
    expect(screen.getByText(/Nothing cached yet/)).toBeInTheDocument();
    expect(
      screen.queryByText(/not running a proxy cache/),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByText(/keeps no Actions cache/),
    ).not.toBeInTheDocument();
  });

  // Whoever holds the access code is the owner, so nobody is shown a
  // flush button they may not press.
  it("enables flush buttons for whoever is signed in", async () => {
    api
      .mockResolvedValueOnce({
        enabled: true,
        cache: [{ kind: "apt", entries: 412, bytes: 3 * (1 << 30) }],
      })
      .mockResolvedValueOnce({
        enabled: true,
        usedByJobs: true,
        entries: 12,
        bytes: 3 * (1 << 30),
        maxBytes: 20 * (1 << 30),
        repos: [{ repo: "acme/widgets", bytes: 3 * (1 << 30) }],
      });

    render(<CachesPage showError={() => {}} />);
    await screen.findByText("apt packages: 412 packages, 3.0 GiB");

    const buttons = screen.getAllByRole("button", {
      name: /^Flush (?!the whole cache$)/,
    });
    // Every row's button is named for its row, six ecosystems and one
    // repository, rather than seven buttons all called "Flush".
    expect(buttons.map((b) => b.getAttribute("aria-label"))).toContain(
      "Flush the Actions cache of acme/widgets",
    );
    for (const btn of buttons) {
      expect(btn).toBeEnabled();
    }
    const wholeCacheButtons = screen.getAllByRole("button", {
      name: "Flush the whole cache",
    });
    for (const btn of wholeCacheButtons) {
      expect(btn).toBeEnabled();
    }
  });
});

// A first read that failed used to spin "Loading caches" for as long as
// the pane was open.
describe("CachesPage when its read fails", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("replaces the spinner with what went wrong", async () => {
    api.mockRejectedValue(new Error("cache stats down"));
    const showError = vi.fn();
    render(<CachesPage showError={showError} />);

    expect(
      await screen.findByText(/caches could not be read \(cache stats down\)/),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText("Loading caches")).not.toBeInTheDocument();
    expect(showError).toHaveBeenCalledTimes(1);
  });
});
