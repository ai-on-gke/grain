// Everything behind the form's "Advanced options" toggle: the per-task
// sandbox shape and the per-task overrides -- the model and the connection it is pinned to,
// the prompt extension and the review template.
//
// Part of the NewTaskOverlay suite -- see NewTaskOverlay.test.jsx for
// why it is four files and what a test in one costs.

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import NewTaskOverlay from "./NewTaskOverlay.jsx";
import api from "../api.js";
import { TASK_DRAFT_STORAGE_KEY } from "../taskDraft.js";

vi.mock("../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));

// createCall is the POST that files the task. A team with no repos
// attached also has the form ask GET /api/deployment/reachable-repos
// (grain/task-1850), so the create is not always the first call.
const createCall = () =>
  api.mock.calls.find(([, opts]) => opts && opts.method === "POST");

describe("NewTaskOverlay", () => {
  afterEach(() => {
    api.mockClear();
  });

  // Interactive sessions are started from the Sessions page
  // (NewSessionDialog.jsx), not filed from here: the form offers no
  // way to ask for one, and what it files is always ordinary work with
  // its approval and backlog end chosen as usual.
  it("files ordinary tasks only, with no interactive-session option", async () => {
    api.mockResolvedValueOnce({ id: "42" });
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={{ newestFirst: false }}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Talk");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    expect(screen.queryByLabelText(/Interactive session/)).toBeNull();
    expect(screen.getByLabelText(/Queue immediately/)).toBeInTheDocument();
    expect(screen.getByLabelText("Add to backlog")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const payload = JSON.parse(createCall()[1].body);
    expect(payload).not.toHaveProperty("interactive");
    expect(payload.atFront).toBe(false);
  });

  // bwsalmon/agents#534: a per-task sandbox shape override.
  // bwsalmon/agents#613: it now lives behind the "Advanced options" toggle
  // with the other per-task overrides, so open that first.
  it("includes a sandbox shape override when given", async () => {
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Fix");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    await user.type(screen.getByLabelText(/vCPUs/), "4");
    await user.type(screen.getByLabelText(/Memory \(MiB\)/), "8192");
    await user.type(screen.getByLabelText(/Disk \(GiB\)/), "40");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(api).toHaveBeenCalledWith(
      "/api/tasks",
      expect.objectContaining({
        body: expect.stringContaining(
          '"sandboxCpus":4,"sandboxMemoryMb":8192,"sandboxDiskGb":40',
        ),
      }),
    );
  });

  // grain/task-26: on a deployment whose sandboxes have no shape at all
  // (config.sandboxShapeIgnored) an override is refused rather than
  // ignored -- HostSandboxes.Acquire fails the run at sandbox setup --
  // so the form takes the fields away instead of letting a task be filed
  // in the one way that guarantees it dies.
  // A refused value behind the collapsed toggle -- a draft keeps one --
  // used to leave Create doing nothing at all: the browser cannot focus
  // a hidden field to say what is wrong with it (2026-10-23 intake
  // walkthrough). The section opens on the refusal instead.
  it("opens Advanced options when a field inside it refuses the submit", async () => {
    localStorage.setItem(
      TASK_DRAFT_STORAGE_KEY,
      JSON.stringify({ title: "Fix", noRepo: true, sandboxCpus: "-1" }),
    );
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    const toggle = screen.getByRole("button", { name: "Advanced options" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(createCall()).toBeUndefined();
    localStorage.removeItem(TASK_DRAFT_STORAGE_KEY);
  });

  it("disables the sandbox shape override where a shape would fail the run", async () => {
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={{ sandboxShapeIgnored: true }}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    expect(screen.getByLabelText(/vCPUs/)).toBeDisabled();
    expect(screen.getByLabelText(/Memory \(MiB\)/)).toBeDisabled();
    expect(screen.getByLabelText(/Disk \(GiB\)/)).toBeDisabled();
    expect(screen.getByText(/fail at sandbox setup/)).toBeVisible();
  });

  it("leaves the sandbox shape override alone where a shape applies", async () => {
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={{}}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    expect(screen.getByLabelText(/vCPUs/)).toBeEnabled();
    expect(screen.queryByText(/fail at sandbox setup/)).toBeNull();
  });

  // A task can be driven by a framework other than the deployment's own
  // (model.Task.AgentFramework) -- the picker for that sits with the
  // other per-task overrides, behind "Advanced options".
  // docs/connections-and-models.md: a task chooses a model, and may pin
  // one connection that serves it. The model's choices are what the
  // target team's connections advertise; a framework is no longer asked
  // for, because a model implies one.
  const connections = {
    agentFramework: "claude",
    deployment: { id: "default", defaultModel: "gpt-5.6-sol" },
    agentBackends: [
      {
        id: "codex-day",
        name: "Day account",
        framework: "codex",
        models: ["gpt-5.6-sol"],
      },
      {
        id: "claude-team",
        name: "Team account",
        framework: "claude",
        models: ["claude-opus-5", "claude-sonnet-5"],
      },
      {
        id: "claude-deep",
        name: "Deep account",
        framework: "claude",
        models: ["claude-opus-5"],
        disabled: true,
      },
    ],
  };

  const renderWith = (config) =>
    render(
      <NewTaskOverlay
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

  it("files a task on a model the deployment's connections advertise", async () => {
    api.mockResolvedValueOnce({ id: "42" });
    const user = userEvent.setup();
    renderWith(connections);

    await user.type(screen.getByLabelText(/Title/), "Port");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    expect(screen.queryByLabelText("Agent framework")).not.toBeInTheDocument();
    await user.click(screen.getByLabelText("Model"));
    await user.click(screen.getByRole("option", { name: "claude-sonnet-5" }));
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const payload = JSON.parse(createCall()[1].body);
    expect(payload.model).toBe("claude-sonnet-5");
    expect(payload.agentBackendId).toBe("");
    expect(payload).not.toHaveProperty("agentFramework");
  });

  // grain/task-2308: with every model a provider lists advertised, the
  // picker is searched by typing part of a name rather than scrolled.
  it("finds a model by typing part of its name", async () => {
    api.mockResolvedValueOnce({ id: "42" });
    const user = userEvent.setup();
    renderWith(connections);

    await user.type(screen.getByLabelText(/Title/), "Port");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    const picker = screen.getByLabelText("Model");
    await user.click(picker);
    await user.clear(picker);
    await user.type(picker, "cl snt");
    expect(
      screen.getByRole("option", { name: "claude-sonnet-5" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("option", { name: /claude-opus-5/ }),
    ).not.toBeInTheDocument();
    await user.keyboard("{Enter}");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(JSON.parse(createCall()[1].body).model).toBe("claude-sonnet-5");
  });

  // Leaving the picker alone must send "" and not the team's current
  // default: an empty model follows the team if its default changes
  // later, a chosen one does not.
  it("names the team default in the picker without choosing it for the task", async () => {
    api.mockResolvedValueOnce({ id: "42" });
    const user = userEvent.setup();
    renderWith(connections);

    await user.type(screen.getByLabelText(/Title/), "Fix");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    expect(screen.getByLabelText("Model")).toHaveValue(
      "Deployment default (gpt-5.6-sol)",
    );
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const payload = JSON.parse(createCall()[1].body);
    expect(payload.model).toBe("");
    expect(payload.agentBackendId).toBe("");
  });

  // A pin must advertise the task's model (CreateTask refuses the pair
  // otherwise), so the connection picker offers only those. A disabled
  // connection is among them and says it is pin-only: a pin is the only
  // way work reaches it.
  it("pins a task to a disabled connection that serves its model", async () => {
    api.mockResolvedValueOnce({ id: "42" });
    const user = userEvent.setup();
    renderWith(connections);

    await user.type(screen.getByLabelText(/Title/), "One expensive job");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByLabelText("Model"));
    await user.click(screen.getByRole("option", { name: "claude-opus-5" }));
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    await user.click(screen.getByLabelText("Specific connection"));
    expect(
      screen.getByRole("option", { name: "Team account · Claude" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("option", { name: /Day account/ }),
    ).not.toBeInTheDocument();
    await user.click(
      screen.getByRole("option", {
        name: "Deep account · Claude · disabled, pin only",
      }),
    );
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const payload = JSON.parse(createCall()[1].body);
    expect(payload.model).toBe("claude-opus-5");
    expect(payload.agentBackendId).toBe("claude-deep");
  });

  // With no model chosen, the pin is checked against the team's default,
  // and a model change that the pinned connection does not serve drops
  // the pin rather than filing a pair the server refuses.
  it("drops a pin the newly chosen model's connections do not include", async () => {
    api.mockResolvedValueOnce({ id: "42" });
    const user = userEvent.setup();
    renderWith(connections);

    await user.type(screen.getByLabelText(/Title/), "Switch");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    await user.click(screen.getByLabelText("Specific connection"));
    expect(
      screen.queryByRole("option", { name: /Team account/ }),
    ).not.toBeInTheDocument();
    await user.click(
      screen.getByRole("option", { name: "Day account · Codex" }),
    );
    await user.click(screen.getByLabelText("Model"));
    await user.click(screen.getByRole("option", { name: "claude-opus-5" }));
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const payload = JSON.parse(createCall()[1].body);
    expect(payload.model).toBe("claude-opus-5");
    expect(payload.agentBackendId).toBe("");
  });

  // Nothing to choose between on a team with no connection at all, so
  // neither picker is in the form, and there is no framework picker to
  // fall back on either.
  it("leaves the model and connection pickers out where the team has no connection", async () => {
    const user = userEvent.setup();
    renderWith({ agentFramework: "claude" });

    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    expect(screen.queryByLabelText("Model")).not.toBeInTheDocument();
    expect(
      screen.queryByLabelText("Specific connection"),
    ).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Agent framework")).not.toBeInTheDocument();
  });

  // grain/task-269: a task can be filed with no network at all, which is
  // the per-task half of the deployment-wide setting on the Sandbox tab.
  it("files a task with no network access when that is picked", async () => {
    api.mockResolvedValueOnce({ id: "42" });
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={{ networkAccess: "open" }}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Write memory");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    expect(screen.getByText("Deployment default (egress)")).toBeInTheDocument();
    await user.click(screen.getByLabelText("Network access"));
    expect(
      screen.getByRole("option", { name: "Egress (outbound internet only)" }),
    ).toBeInTheDocument();
    await user.click(
      screen.getByRole("option", { name: "None (this deployment only)" }),
    );
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(JSON.parse(createCall()[1].body).networkAccess).toBe("none");
  });

  // grain/task-1549: what a run ending on nothing_needed does to the
  // task. Close is the default; leaving it for acknowledgement is the
  // one other choice the select offers.
  it("files a task that leaves a nothing-needed ending for acknowledgement", async () => {
    api.mockResolvedValueOnce({ id: "42" });
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={{}}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Check the flag");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    const select = screen.getByLabelText("When the agent finds nothing to do");
    expect(select).toHaveTextContent("Close");
    await user.click(select);
    await user.click(
      screen.getByRole("option", { name: "Leave for acknowledgement" }),
    );
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(JSON.parse(createCall()[1].body).nothingNeededAction).toBe("ack");
  });

  // Left alone it sends "", which follows the deployment rather than
  // pinning this task to whatever the deployment happens to say today --
  // the same reading the framework picker above has.
  it("leaves the network override empty when the picker is not touched", async () => {
    api.mockResolvedValueOnce({ id: "42" });
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={{ networkAccess: "none" }}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Fix");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(JSON.parse(createCall()[1].body).networkAccess).toBe("");
  });

  // On a deployment whose sandboxes are directories on the daemon's own
  // machine the server refuses "none" outright, so the field is disabled
  // rather than offering the one choice that guarantees a failed run --
  // exactly what the sandbox-shape fields beside it do (grain/task-26).
  it("disables the network picker where the backend has no network to withhold", async () => {
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={{ sandboxShapeIgnored: true, networkAccess: "open" }}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    expect(screen.getByLabelText("Network access")).toHaveAttribute(
      "aria-disabled",
      "true",
    );
  });

  // Both pickers show a line of their own while nothing is picked --
  // "Deployment default (...)" and "No review" -- and MUI floats a
  // label clear of the box only for a field it counts as filled, which
  // an unpicked select never is. Left alone the label is painted across
  // that line, on the same baseline, and the field reads as garbled
  // text. data-shrink is the label's own account of where it is.
  it("keeps the labels clear of the defaults the pickers show", async () => {
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={connections}
        templates={[{ id: "tmpl-1", name: "Ultra review" }]}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    for (const name of ["Model", "Specific connection", "Review"]) {
      const label = screen
        .getByLabelText(name)
        .closest(".MuiFormControl-root")
        .querySelector("label");
      expect(label).toHaveAttribute("data-shrink", "true");
    }
  });

  // grain/task-114: a task can replace the standing instructions its
  // deployment and repo would otherwise give its runs -- the last of the
  // per-task overrides behind "Advanced options", and the only one that
  // replaces rather than adds, which is why the deployment's own text is
  // shown beside it.
  it("files a task with its own prompt extension when one is written", async () => {
    api.mockResolvedValueOnce({ id: "42" });
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={{ promptExtension: "Run `make lint` before you push." }}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Regen");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    expect(
      screen.getByText(/Deployment-wide, used when the box above is empty/),
    ).toHaveTextContent("Run `make lint` before you push.");
    await user.type(
      screen.getByLabelText(/Prompt extension override/),
      "Ignore the house rules.",
    );
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(JSON.parse(createCall()[1].body).promptExtension).toBe(
      "Ignore the house rules.",
    );
  });

  // grain/task-284: attaching a review to a task is picking the template
  // the reviewing agent's instructions come from, not writing them again
  // here -- so the picker lists templates by name and sends the id.
  it("attaches a review by picking the template it comes from", async () => {
    api.mockResolvedValueOnce({ id: "42" });
    const templates = [
      { id: "3", name: "Bug hunt" },
      { id: "7", name: "Security pass" },
    ];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        templates={templates}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Add");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    await user.click(screen.getByLabelText("Review"));
    await user.click(screen.getByRole("option", { name: "Security pass" }));
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(JSON.parse(createCall()[1].body).reviewTemplateId).toBe("7");
  });

  // grain/task-47: the same figure the Templates page, the schedule
  // editor's own picker and the task pane's Review row carry
  // (ItemGlyph.jsx, docs/brand.md), so what is picked here is
  // recognisably the thing that list holds. "No review" names no
  // template and holds the slot open instead.
  it("glyphs the review picker's templates, and holds the slot for No review", async () => {
    const templates = [{ id: "3", name: "Bug hunt" }];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        templates={templates}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    await user.click(screen.getByLabelText("Review"));

    expect(
      (await screen.findByRole("option", { name: "Bug hunt" })).querySelector(
        'svg[data-glyph="templates"]',
      ),
    ).toBeInTheDocument();
    expect(
      screen
        .getByRole("option", { name: "No review" })
        .querySelector("svg[data-glyph]"),
    ).toBeNull();
  });

  // No review is the default, and has to stay one: a task filed without
  // touching the picker must not start waiting for a review nobody asked
  // for before it can merge.
  it("files no review when the picker is left alone", async () => {
    const templates = [{ id: "3", name: "Bug hunt" }];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        templates={templates}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Fix");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    expect(screen.getByText("No review")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(JSON.parse(createCall()[1].body).reviewTemplateId).toBe("");
  });

  it("omits team field from payload when Default Team is selected", async () => {
    api.mockResolvedValueOnce({ id: "42" });
    const configWithTeams = {
      deployment: { id: "default" },
    };
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={configWithTeams}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Default task");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(JSON.parse(createCall()[1].body)).not.toHaveProperty("team");
  });
});
