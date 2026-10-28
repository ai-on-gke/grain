import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import api from "../api.js";
import RepoWorkOwnership from "./RepoWorkOwnership.jsx";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const nightly = "repo:schedule:acme:widgets:nightly";

const work = {
  repo: "acme/widgets",
  default: "",
  defaultRuns: true,
  items: [
    { item: "release", kind: "release", override: "", runs: true },
    {
      item: nightly,
      kind: "schedule",
      title: "Nightly sweep",
      override: "off",
      runs: false,
      declaration: {
        repo: "acme/widgets",
        path: ".grain/schedules/nightly.yml",
      },
    },
  ],
};

function renderPane(over = {}) {
  const props = { repo: "acme/widgets", showError: vi.fn(), ...over };
  render(<RepoWorkOwnership {...props} />);
  return props;
}

describe("RepoWorkOwnership", () => {
  afterEach(() => api.mockReset());

  // The three states are three states. A pane that drew them with ticks
  // would have to spend one position on "nobody has said" and one on
  // "somebody said no", which are the two this setting exists to keep
  // apart.
  it("draws silence, an explicit exception and what each resolves to", async () => {
    api.mockResolvedValue(work);
    renderPane();

    // Twice over: the repository default says nothing, and so does the
    // release item under it -- which is a row that follows a silence
    // rather than a row that has been switched on.
    expect(await screen.findAllByText("Nobody has said")).toHaveLength(2);
    expect(screen.getByText("Runs this repository's work")).toBeInTheDocument();
    // The item that says nothing and the item that says no both have a
    // resolution, and the two readings are separate: the release follows
    // a default nobody has set, and the nightly is off because somebody
    // said so here.
    expect(screen.getByText("Off, set here")).toBeInTheDocument();
    expect(screen.getByText("Nightly sweep")).toBeInTheDocument();
    expect(screen.getByText(nightly)).toBeInTheDocument();
    expect(
      screen.getByText("declared in .grain/schedules/nightly.yml"),
    ).toBeInTheDocument();
    expect(screen.getByText("Not run")).toBeInTheDocument();
    expect(screen.getByText("Runs")).toBeInTheDocument();
  });

  // The chosen state is drawn filled; aria-pressed is what says so to
  // anything that cannot see the fill.
  it("marks which of the three states each row has chosen", async () => {
    api.mockResolvedValue(work);
    renderPane();

    const off = await screen.findByRole("button", {
      name: "Nightly sweep: off",
    });
    expect(off).toHaveAttribute("aria-pressed", "true");
    expect(
      screen.getByRole("button", { name: "Nightly sweep: on" }),
    ).toHaveAttribute("aria-pressed", "false");
    expect(
      screen.getByRole("button", { name: "Nightly sweep: say nothing" }),
    ).toHaveAttribute("aria-pressed", "false");
    expect(
      screen.getByRole("button", { name: "release: say nothing" }),
    ).toHaveAttribute("aria-pressed", "true");
  });

  it("writes the repository default", async () => {
    api.mockResolvedValueOnce(work).mockResolvedValueOnce({
      ...work,
      default: "off",
      defaultRuns: false,
      // Resolved by the server, which is why the release row moves with
      // the default while still saying that nobody has said anything
      // about it.
      items: [{ ...work.items[0], runs: false }, work.items[1]],
    });
    renderPane();

    await userEvent.click(
      await screen.findByRole("button", {
        name: "the repository default: off",
      }),
    );

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/repos/acme/widgets/work-ownership",
        { method: "PUT", body: JSON.stringify({ default: "off" }) },
      ),
    );
    expect(
      await screen.findByText("Does not run this repository's work"),
    ).toBeInTheDocument();
    // The release item said nothing and moved with the default: the two
    // readings are still separate, which is what says *why* it stopped.
    const release = screen.getByText("release").closest("div");
    expect(within(release).getByText("Nobody has said")).toBeInTheDocument();
    expect(within(release).getByText("Not run")).toBeInTheDocument();
  });

  // The default is the one "standing work off" switch (grain/task-3332),
  // drawn as a switch, with what it covers and what it leaves running
  // said beside it.
  it("turns the repository's standing work off with one switch", async () => {
    api.mockResolvedValueOnce(work).mockResolvedValueOnce({
      ...work,
      default: "off",
      defaultRuns: false,
      items: [{ ...work.items[0], runs: false }, work.items[1]],
    });
    renderPane();

    expect(
      await screen.findByText("Standing work on this deployment: on"),
    ).toBeInTheDocument();
    expect(screen.getByText(/the base-branch check/)).toBeInTheDocument();
    expect(
      screen.getByText(/tasks people file by hand against this repository/),
    ).toBeInTheDocument();

    await userEvent.click(
      screen.getByRole("switch", { name: "Standing work on this deployment" }),
    );
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/repos/acme/widgets/work-ownership",
        { method: "PUT", body: JSON.stringify({ default: "off" }) },
      ),
    );
    expect(
      await screen.findByText("Standing work on this deployment: off"),
    ).toBeInTheDocument();
  });

  // Clearing one item's exception is "follow the default again", and is
  // sent as the empty state rather than as "on" -- which would be a
  // decision of its own and would outlive the default being turned off.
  it("clears one item's exception without turning it on", async () => {
    api.mockResolvedValueOnce(work).mockResolvedValueOnce({
      ...work,
      items: [work.items[0], { ...work.items[1], override: "", runs: true }],
    });
    renderPane();

    await userEvent.click(
      await screen.findByRole("button", { name: "Nightly sweep: say nothing" }),
    );

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/repos/acme/widgets/work-ownership",
        {
          method: "PUT",
          body: JSON.stringify({ item: nightly, state: "" }),
        },
      ),
    );
    expect(screen.queryByText("Off, set here")).not.toBeInTheDocument();
  });

  // A schedule's own pause switch is not this setting, and saying so is
  // the difference between "this deployment has handed the work over"
  // and "somebody paused the chore".
  it("shows a paused schedule as paused rather than as unowned", async () => {
    api.mockResolvedValue({
      ...work,
      items: [
        {
          item: "local-chore",
          kind: "schedule",
          title: "Housekeeping",
          override: "",
          runs: true,
          paused: true,
        },
      ],
    });
    renderPane();

    const row = (await screen.findByText("Housekeeping")).closest("div");
    expect(within(row).getByText("Paused on its own row")).toBeInTheDocument();
    expect(within(row).getByText("Runs")).toBeInTheDocument();
  });

  // A title that is just the id again says nothing new, so the id is
  // not drawn a second time under it.
  it("shows the id only when it differs from the title", async () => {
    api.mockResolvedValue({
      ...work,
      items: [
        {
          item: "chore",
          kind: "schedule",
          title: "chore",
          override: "",
          runs: true,
        },
      ],
    });
    renderPane();

    expect(await screen.findAllByText("chore")).toHaveLength(1);
  });

  // An override outlives whatever it named. It has to stay listed, or
  // the decision goes on being made with nobody able to see it.
  it("lists an override whose item nothing here matches", async () => {
    api.mockResolvedValue({
      ...work,
      items: [
        { item: "withdrawn", kind: "unknown", override: "off", runs: false },
      ],
    });
    renderPane();

    expect(await screen.findByText("withdrawn")).toBeInTheDocument();
    expect(screen.getByText(/nothing here has this id/)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "withdrawn: say nothing" }),
    ).toBeInTheDocument();
  });

  it("reports a failed read rather than drawing an empty list", async () => {
    api.mockRejectedValue(new Error("nope"));
    const { showError } = renderPane();

    await waitFor(() => expect(showError).toHaveBeenCalled());
    expect(screen.queryByText("Nobody has said")).not.toBeInTheDocument();
  });

  it("links the standing-work switch to the read-only refusal note via aria-describedby", async () => {
    api.mockResolvedValue(work);
    renderPane({ readOnly: true, describedBy: "work-ownership-readonly" });

    const sw = await screen.findByRole("switch", {
      name: "Standing work on this deployment",
    });
    expect(sw).toBeDisabled();
    expect(sw).toHaveAttribute("aria-describedby", "work-ownership-readonly");
  });
});
