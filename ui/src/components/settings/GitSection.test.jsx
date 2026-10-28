import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import GitSection from "./GitSection.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

describe("GitSection", () => {
  beforeEach(() => {
    api.mockReset();
  });

  it("draws the deployment's git connections from its record", async () => {
    api.mockImplementation((path) => {
      if (path === "/api/deployment")
        return Promise.resolve({ id: "default", gitConnections: [] });
      return Promise.resolve([]);
    });
    render(<GitSection showError={vi.fn()} />);
    expect(await screen.findByTestId("git-connections-list")).toBeVisible();
    expect(api).toHaveBeenCalledWith("/api/deployment");
  });

  it("says so when the deployment's record cannot be read", async () => {
    api.mockImplementation((path) =>
      path === "/api/deployment"
        ? Promise.reject(new Error("boom"))
        : Promise.resolve([]),
    );
    render(<GitSection showError={vi.fn()} />);
    expect(
      await screen.findByText(/Could not load the deployment's connections/),
    ).toHaveTextContent("boom");
    expect(screen.queryByTestId("git-connections-list")).toBeNull();
  });
});
