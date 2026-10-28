import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import PackageCachePanel from "./PackageCachePanel.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const cache = [
  { kind: "apt", entries: 412, bytes: 3 * (1 << 30) },
  { kind: "pypi", entries: 0, bytes: 0 },
  { kind: "docker", entries: 1, bytes: 1 << 20 },
  { kind: "playwright", entries: 3, bytes: 450 * (1 << 20) },
];

describe("PackageCachePanel", () => {
  afterEach(() => api.mockReset());

  it("says what each cache holds", () => {
    render(<PackageCachePanel cache={cache} showError={() => {}} />);
    expect(
      screen.getByText(/Total space consumed: 3\.4 GiB/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/apt packages: 412 packages, 3\.0 GiB/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Python packages: 0 packages, 0 B/),
    ).toBeInTheDocument();
    // A layer is not a package, and calling it one in a list beside two
    // rows of packages is the kind of thing somebody has to go and check.
    expect(
      screen.getByText(/Docker Hub layers: 1 layer, 1\.0 MiB/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Playwright browsers: 3 browsers, 450\.0 MiB/),
    ).toBeInTheDocument();
  });

  it("flushes one ecosystem and redraws from the answer", async () => {
    const user = userEvent.setup();
    const flushed = vi.fn();
    api.mockResolvedValueOnce({
      entries: 412,
      bytes: 3 * (1 << 30),
      cache: [
        { kind: "apt", entries: 0, bytes: 0 },
        { kind: "pypi", entries: 0, bytes: 0 },
      ],
    });
    render(
      <PackageCachePanel
        cache={cache}
        showError={() => {}}
        onFlushed={flushed}
      />,
    );

    // One button per row, each named for the ecosystem it flushes, so a
    // screen reader tabbing down the list hears which one it is on.
    await user.click(
      screen.getByRole("button", { name: "Flush apt packages" }),
    );

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/package-cache/flush?kind=apt", {
        method: "POST",
      }),
    );
    expect(flushed).toHaveBeenCalledWith([
      { kind: "apt", entries: 0, bytes: 0 },
      { kind: "pypi", entries: 0, bytes: 0 },
    ]);
  });

  it("draws the page's next reading after a flush, not the flush's answer forever", async () => {
    const user = userEvent.setup();
    api.mockResolvedValueOnce({
      cache: [{ kind: "apt", entries: 0, bytes: 0 }],
    });
    const { rerender } = render(
      <PackageCachePanel cache={cache} showError={() => {}} />,
    );
    await user.click(
      screen.getByRole("button", { name: "Flush apt packages" }),
    );
    expect(
      await screen.findByText(/apt packages: 0 packages, 0 B/),
    ).toBeInTheDocument();

    // The page's poll comes round with apt filling up again.
    rerender(
      <PackageCachePanel
        cache={[{ kind: "apt", entries: 7, bytes: 1 << 20 }]}
        showError={() => {}}
      />,
    );
    expect(
      screen.getByText(/apt packages: 7 packages, 1\.0 MiB/),
    ).toBeInTheDocument();
  });

  it("flushes everything at once", async () => {
    const user = userEvent.setup();
    api.mockResolvedValueOnce({ entries: 412, bytes: 1, cache: [] });
    render(<PackageCachePanel cache={cache} showError={() => {}} />);

    await user.click(
      screen.getByRole("button", { name: "Flush the whole cache" }),
    );
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/package-cache/flush", {
        method: "POST",
      }),
    );
  });

  it("offers nothing to flush when the deployment runs no cache", () => {
    render(<PackageCachePanel cache={undefined} showError={() => {}} />);
    expect(screen.getByText(/not running a proxy cache/)).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("reports a failed flush rather than swallowing it", async () => {
    const user = userEvent.setup();
    const showError = vi.fn();
    api.mockRejectedValueOnce(new Error("the cache directory is read-only"));
    render(<PackageCachePanel cache={cache} showError={showError} />);

    await user.click(
      screen.getByRole("button", { name: "Flush the whole cache" }),
    );
    await waitFor(() => expect(showError).toHaveBeenCalled());
  });
});
