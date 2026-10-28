import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import UpgradeReleaseNotes, {
  NOT_IN_THIS_BUILD,
} from "./UpgradeReleaseNotes.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// The preview waits PREVIEW_DELAY_MS before asking, so a find waits
// past it.
const waitPast = { timeout: 3000 };

describe("UpgradeReleaseNotes", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("shows the notes for every version the target brings", async () => {
    api.mockResolvedValue({
      branch: "v1.8.0",
      from: "v1.6.0",
      version: "v1.8.0",
      notes: [
        {
          version: "v1.8.0",
          markdown: "# v1.8.0\n\n- Separate connections.\n",
        },
        { version: "v1.7.0", markdown: "# v1.7.0\n\n- Faster recovery.\n" },
      ],
    });

    render(<UpgradeReleaseNotes branch="v1.8.0" />);

    expect(
      screen.getByText(/Reading the release notes v1.8.0 carries/),
    ).toBeInTheDocument();
    expect(
      await screen.findByText("Separate connections.", {}, waitPast),
    ).toBeInTheDocument();
    expect(screen.getByText("Faster recovery.")).toBeInTheDocument();
    expect(
      screen.getByText(/This deployment runs v1.6.0\. The target is v1.8.0\./),
    ).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith(
      "/api/upgrade/release-notes?branch=v1.8.0",
    );
    expect(
      screen.queryByText(new RegExp(NOT_IN_THIS_BUILD, "i")),
    ).not.toBeInTheDocument();
  });

  // A build older than `grain release-notes`, or a candidate cut before
  // its own version's notes were written.
  it("says the release notes are not in this build", async () => {
    api.mockResolvedValue({
      branch: "candidate",
      from: "v1.8.0",
      version: "v1.9.0-rc.2",
      notes: [],
      missing: "v1.9.0",
      notInThisBuild: true,
      message:
        "release notes are not in this build for v1.9.0: an image is built before the release cut writes its version's notes",
    });

    render(<UpgradeReleaseNotes branch="candidate" />);

    expect(
      await screen.findByText(
        /Release notes are not in this build for v1.9.0/,
        {},
        waitPast,
      ),
    ).toBeInTheDocument();
  });

  // Seen on grain demo (docs/test-runs/2026-10-12-release-notes-versions.md):
  // a target older than what runs was announced as "V1.9.0 ..." in a
  // green success alert.
  it("warns of a target no newer than what runs, keeping its version's name", async () => {
    api.mockResolvedValue({
      branch: "latest",
      from: "v1.10.0",
      version: "v1.9.0",
      notes: [],
      message: "v1.9.0 is not newer than v1.10.0, which this deployment runs",
    });

    render(<UpgradeReleaseNotes branch="latest" />);

    const said = await screen.findByText(
      "v1.9.0 is not newer than v1.10.0, which this deployment runs.",
      {},
      waitPast,
    );
    expect(said.closest(".MuiAlert-root")).toHaveClass(
      "MuiAlert-standardWarning",
    );
  });

  it("says when the target could not be asked", async () => {
    api.mockRejectedValue(
      new Error("pulling ghcr.io/x:main: manifest unknown"),
    );
    render(<UpgradeReleaseNotes branch="main" />);
    expect(
      await screen.findByText(
        /Could not read the release notes main carries: pulling/,
        {},
        waitPast,
      ),
    ).toBeInTheDocument();
  });

  it("asks nothing for an empty branch", async () => {
    render(<UpgradeReleaseNotes branch="  " />);
    await new Promise((resolve) => setTimeout(resolve, 1000));
    expect(api).not.toHaveBeenCalled();
    expect(
      screen.queryByTestId("upgrade-release-notes"),
    ).not.toBeInTheDocument();
  });
});
