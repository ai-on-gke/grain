import { render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import CompositionPanel from "./CompositionPanel.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// The panel's whole job is to make the combination legible: the value
// *and* the declaration that decided it, and a readable answer where the
// value is empty on purpose (no branch means the repo's default branch).

const field = (name, source, values) => ({ name, source, values });

const composition = {
  kind: "suite",
  suiteId: "suite-1",
  suiteName: "Nightly",
  repo: "acme/widgets",
  base: "main",
  mode: "until_clean",
  maxPasses: 4,
  tasks: [
    {
      templateId: "template-1",
      templateName: "Sweep",
      title: "Sweep the repo",
      fields: [
        field("title", "template", ["Sweep the repo"]),
        field("repo", "suite", ["acme/widgets"]),
        field("base", "template", []),
        field("network", "deployment", []),
        field("autoMerge", "suite", ["off"]),
        field("approval", "suite", ["waits for approval"]),
      ],
    },
  ],
};

describe("CompositionPanel", () => {
  beforeEach(() => {
    api.mockReset();
  });

  it("shows each field's value and where it came from", async () => {
    api.mockResolvedValue(composition);
    render(<CompositionPanel path="/api/suites/suite-1/composition" />);

    expect(await screen.findByText("Sweep the repo")).toBeInTheDocument();
    expect(screen.getByText("acme/widgets")).toBeInTheDocument();
    expect(screen.getByText("off")).toBeInTheDocument();
    // The same value can come from different places, so the source is
    // the point: two fields here are the suite's, one the template's.
    expect(screen.getAllByText("from the suite").length).toBe(3);
    expect(screen.getAllByText("from the template").length).toBe(2);
    expect(api).toHaveBeenCalledWith("/api/suites/suite-1/composition");
  });

  it("says what an empty value means rather than leaving it blank", async () => {
    api.mockResolvedValue(composition);
    render(<CompositionPanel path="/api/suites/suite-1/composition" />);

    expect(
      await screen.findByText("the repo's default branch"),
    ).toBeInTheDocument();
    expect(screen.getByText("no override")).toBeInTheDocument();
    expect(screen.getByText("the deployment decides")).toBeInTheDocument();
  });

  it("names every field and layer in words, never by its wire name", async () => {
    api.mockResolvedValue({
      kind: "template",
      tasks: [
        {
          templateId: "template-1",
          templateName: "Sweep",
          title: "Sweep",
          fields: [
            field("repo", "filing", ["acme/widgets"]),
            field("capabilities", "repo", ["grain-ci"]),
            field("network", "template", ["open"]),
            field("nothingNeeded", "template", ["ack"]),
            field("model", "team", []),
          ],
        },
      ],
    });
    render(<CompositionPanel path="/api/templates/template-1/composition" />);

    expect(await screen.findByText("Model")).toBeInTheDocument();
    expect(screen.queryByText("model")).not.toBeInTheDocument();
    expect(
      screen.getByText("the deployment's default model"),
    ).toBeInTheDocument();
    expect(screen.getByText("the team decides")).toBeInTheDocument();
    expect(screen.getByText("chosen here")).toBeInTheDocument();
    expect(screen.queryByText("filing")).not.toBeInTheDocument();
    expect(screen.getByText("from the repo")).toBeInTheDocument();
    expect(screen.queryByText("repo")).not.toBeInTheDocument();
    expect(
      screen.getByText("egress (outbound internet only)"),
    ).toBeInTheDocument();
    expect(screen.getByText("leave for acknowledgement")).toBeInTheDocument();
  });

  it("pairs each capability group with its own source when values come from multiple layers", async () => {
    api.mockResolvedValue({
      kind: "task",
      scheduleId: "sched-2",
      repo: "acme/widgets",
      base: "main",
      tasks: [
        {
          title: "Weekly Dependency & Vulnerability Audit",
          fields: [
            {
              name: "capabilities",
              source: "schedule",
              sources: ["schedule", "repo"],
              values: ["self-debug", "grain-ci"],
            },
          ],
        },
      ],
    });
    render(<CompositionPanel path="/api/schedules/sched-2/composition" />);

    expect(await screen.findByText("Capabilities")).toBeInTheDocument();
    expect(screen.getAllByText("Capabilities")).toHaveLength(1);
    expect(screen.getByText("self-debug")).toBeInTheDocument();
    expect(screen.getByText("from the schedule")).toBeInTheDocument();
    expect(screen.getByText("grain-ci")).toBeInTheDocument();
    expect(screen.getByText("from the repo")).toBeInTheDocument();
  });

  it("says a suite's unset branch is the run's, not the repo's default", async () => {
    api.mockResolvedValue({
      ...composition,
      tasks: [
        {
          ...composition.tasks[0],
          fields: [field("base", "suite", [])],
        },
      ],
    });
    render(<CompositionPanel path="/api/suites/suite-1/composition" />);

    expect(
      await screen.findByText("the branch this run targets"),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("the repo's default branch"),
    ).not.toBeInTheDocument();
  });

  it("says a filing with no repo goes to the deployment's default repo", async () => {
    api.mockResolvedValue({
      kind: "template",
      tasks: [
        {
          templateId: "template-1",
          templateName: "Sweep",
          title: "Sweep",
          fields: [field("repo", "filing", [])],
        },
      ],
    });
    render(<CompositionPanel path="/api/templates/template-1/composition" />);

    expect(
      await screen.findByText("the deployment's default repo"),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("the repo this run targets"),
    ).not.toBeInTheDocument();
  });

  it("says the deployment's default model includes its name when the team has one", async () => {
    api.mockResolvedValue({
      kind: "template",
      tasks: [
        {
          templateId: "template-1",
          templateName: "Sweep",
          title: "Sweep",
          fields: [field("model", "team", ["team-default-model"])],
        },
      ],
    });
    render(<CompositionPanel path="/api/templates/template-1/composition" />);

    expect(
      await screen.findByText(
        "the deployment's default model (team-default-model)",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("the team decides")).toBeInTheDocument();
  });

  it("says the deployment's default model when the team has no default model", async () => {
    api.mockResolvedValue({
      kind: "template",
      tasks: [
        {
          templateId: "template-1",
          templateName: "Sweep",
          title: "Sweep",
          fields: [field("model", "deployment", [])],
        },
      ],
    });
    render(<CompositionPanel path="/api/templates/template-1/composition" />);

    expect(
      await screen.findByText("the deployment's default model"),
    ).toBeInTheDocument();
    expect(screen.getByText("the deployment decides")).toBeInTheDocument();
  });

  it("says how many passes the tasks are filed in", async () => {
    api.mockResolvedValue(composition);
    render(<CompositionPanel path="/api/suites/suite-1/composition" />);

    expect(
      await screen.findByText(/up to 4 passes/, { exact: false }),
    ).toBeInTheDocument();
  });

  it("renders nothing at all with no path to compose", () => {
    const { container } = render(<CompositionPanel path={null} />);
    expect(container).toBeEmptyDOMElement();
    expect(api).not.toHaveBeenCalled();
  });

  it("reports a composition that cannot be resolved in place", async () => {
    api.mockRejectedValue(new Error("template template-9 no longer exists"));
    render(<CompositionPanel path="/api/suites/suite-1/composition" />);

    await waitFor(() =>
      expect(
        screen.getByText("template template-9 no longer exists"),
      ).toBeInTheDocument(),
    );
  });

  // A draft is the form itself rather than a row: POSTed to the draft
  // endpoint, which composes it and writes nothing, so a schedule or a
  // suite that does not exist yet still has a preview.
  it("composes a draft by POSTing the body it was given", async () => {
    api.mockResolvedValue(composition);
    const body = { templateIds: ["template-1"], mode: "until_clean" };
    render(<CompositionPanel path="/api/suites/composition" body={body} />);

    expect(await screen.findByText("Sweep the repo")).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith("/api/suites/composition", {
      method: "POST",
      body: JSON.stringify(body),
    });
  });

  it("recomposes when the draft changes, and not when it does not", async () => {
    api.mockResolvedValue(composition);
    const first = { templateIds: ["template-1"], mode: "until_clean" };
    const { rerender } = render(
      <CompositionPanel path="/api/suites/composition" body={first} />,
    );
    await screen.findByText("Sweep the repo");
    expect(api).toHaveBeenCalledTimes(1);

    // A re-render with an equal body is a new object every time -- the
    // overlays around this build one per render -- and must not be a
    // second request.
    rerender(
      <CompositionPanel
        path="/api/suites/composition"
        body={{ templateIds: ["template-1"], mode: "until_clean" }}
      />,
    );
    await waitFor(() => expect(api).toHaveBeenCalledTimes(1));

    rerender(
      <CompositionPanel
        path="/api/suites/composition"
        body={{ templateIds: ["template-1", "template-2"], mode: "count" }}
      />,
    );
    await waitFor(() => expect(api).toHaveBeenCalledTimes(2));
    expect(api.mock.lastCall[1].body).toBe(
      JSON.stringify({
        templateIds: ["template-1", "template-2"],
        mode: "count",
      }),
    );
  });

  // Typing costs one request rather than one per letter: the panel waits
  // for the form to stand still before it asks.
  it("composes a draft once for a burst of edits", async () => {
    api.mockResolvedValue(composition);
    const { rerender } = render(
      <CompositionPanel
        path="/api/schedules/composition"
        body={{ title: "N" }}
      />,
    );
    for (const title of ["Ni", "Nig", "Nigh", "Night"]) {
      rerender(
        <CompositionPanel path="/api/schedules/composition" body={{ title }} />,
      );
    }

    await screen.findByText("Sweep the repo");
    expect(api).toHaveBeenCalledTimes(1);
    expect(api.mock.lastCall[1].body).toBe(JSON.stringify({ title: "Night" }));
  });

  // A half-filled form the server cannot resolve says so in place, the
  // same as a saved row that no longer composes -- it is a preview
  // nobody pressed anything for, and it must not read as the edit
  // somebody is in the middle of having failed.
  it("reports a draft the server refuses in place", async () => {
    api.mockRejectedValue(new Error("unknown template template-9"));
    render(
      <CompositionPanel
        path="/api/schedules/composition"
        body={{ templateId: "template-9" }}
      />,
    );

    await waitFor(() =>
      expect(
        screen.getByText("unknown template template-9"),
      ).toBeInTheDocument(),
    );
  });
});
