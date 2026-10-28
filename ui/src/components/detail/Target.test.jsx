import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import DetailOverlay from "../DetailOverlay.jsx";
import api from "../../api.js";
import { resetDetailMocks, baseTask, config, patches } from "./testFixtures.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../../attachments.js", () => ({ default: vi.fn() }));

describe("DetailOverlay target", () => {
  afterEach(resetDetailMocks);

  // grain/task-328: repo, base and auto-merge are the controls that set
  // them now, not three static rows under Declared -- so the pane shows
  // each of them as what the task currently says, ready to be changed.
  it("seeds the repo picker, the base box and the auto-merge box from the task", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          repo: "acme/widgets",
          base: "release/2",
          autoMerge: true,
        }}
        tasks={[]}
        config={{ ...config, targetRepos: ["acme/widgets", "acme/other"] }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    expect(screen.getByLabelText("Repo")).toHaveValue("acme/widgets");
    expect(screen.getByLabelText("Base branch")).toHaveValue("release/2");
    expect(
      screen.getByRole("checkbox", { name: /Auto-merge once checks pass/ }),
    ).toBeChecked();
  });

  it("patches the repo when a different one is picked", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "acme/widgets" }}
        tasks={[]}
        config={{ ...config, targetRepos: ["acme/widgets", "acme/other"] }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    await user.selectOptions(screen.getByLabelText("Repo"), "acme/other");

    expect(api).toHaveBeenCalledWith("/api/tasks/12", {
      method: "PATCH",
      body: JSON.stringify({ repo: "acme/other" }),
    });
  });

  // The picker keeps its own uncontrolled value, so a refused edit has to
  // put it back on the repo the task actually has -- otherwise it goes on
  // showing a repo nothing ever accepted, with no server change to
  // correct it.
  it("puts the repo picker back when the server refuses the edit", async () => {
    const showError = vi.fn();
    const user = userEvent.setup();
    api.mockRejectedValueOnce(new Error("repo: not a repo"));
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "acme/widgets" }}
        tasks={[]}
        config={{ ...config, targetRepos: ["acme/widgets", "acme/other"] }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={showError}
      />,
    );

    await user.selectOptions(screen.getByLabelText("Repo"), "acme/other");

    expect(showError).toHaveBeenCalled();
    expect(await screen.findByLabelText("Repo")).toHaveValue("acme/widgets");
  });

  // Saved when the box is left, not per keystroke: a branch name typed
  // into a field that PATCHed on change would be sent one letter at a
  // time and refused all the way to the last one.
  it("patches the base branch once the box is left", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "acme/widgets", base: "main" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    const box = screen.getByLabelText("Base branch");
    await user.clear(box);
    await user.type(box, "release/3");
    // Nothing is *saved* while it is being typed. The box does ask the
    // repo which branches grain knows about there, to suggest them
    // (BranchField.jsx), so the assertion is about the edit rather than
    // about every request.
    expect(patches()).toEqual([]);

    await user.tab();

    expect(patches()).toEqual([
      [
        "/api/tasks/12",
        { method: "PATCH", body: JSON.stringify({ base: "release/3" }) },
      ],
    ]);
  });

  // grain/task-202: the repos a task may change are one section of the
  // pane, not a "Target" section and a second one further down the
  // column. The repo it is filed against is the first thing in it and
  // the others are added under it, so reading or changing "which repos
  // does this task touch" is one place to look rather than two.
  it("keeps the other repos this task may change in the same section as its own", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          repo: "acme/widgets",
          writes: [{ repo: "acme/docs", base: "publish" }],
        }}
        tasks={[]}
        config={{ ...config, targetRepos: ["acme/widgets", "acme/gadgets"] }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    const repos = screen.getByLabelText("Repo").closest("fieldset");
    expect(within(repos).getByText("acme/docs")).toBeInTheDocument();
    expect(
      within(repos).getByLabelText("Base branch in acme/docs"),
    ).toHaveValue("publish");

    await user.click(within(repos).getByLabelText(/Another repo/));
    await user.click(
      await screen.findByRole("menuitem", { name: "acme/gadgets" }),
    );

    expect(api).toHaveBeenCalledWith("/api/tasks/12", {
      method: "PATCH",
      body: JSON.stringify({
        writes: [
          { repo: "acme/docs", base: "publish" },
          { repo: "acme/gadgets", base: "" },
        ],
      }),
    });
  });

  it("sends nothing when the base box is left unchanged", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "acme/widgets", base: "main" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    await user.click(screen.getByLabelText("Base branch"));
    await user.tab();

    expect(patches()).toEqual([]);
  });

  // The same suggestion NewTaskOverlay prefills a new task's base with
  // (lastBaseForRepo), offered rather than applied: changing the repo is
  // the moment the base already on the task stops meaning anything, and
  // rewriting it unasked would be an edit nobody made.
  it("offers the base other tasks on a newly picked repo branch off", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "acme/widgets", base: "main" }}
        tasks={[
          {
            id: "13",
            repo: "acme/other",
            base: "release/2",
            state: "queued",
            createdAt: "2026-01-02T00:00:00Z",
          },
        ]}
        config={{ ...config, targetRepos: ["acme/widgets", "acme/other"] }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    await user.selectOptions(screen.getByLabelText("Repo"), "acme/other");
    await user.click(await screen.findByRole("button", { name: "use it" }));

    expect(api).toHaveBeenLastCalledWith("/api/tasks/12", {
      method: "PATCH",
      body: JSON.stringify({ base: "release/2" }),
    });
  });

  it("suggests no base for a repo nothing else has a task against", async () => {
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "acme/widgets", base: "main" }}
        tasks={[]}
        config={{ ...config, targetRepos: ["acme/widgets", "acme/other"] }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    await user.selectOptions(screen.getByLabelText("Repo"), "acme/other");

    expect(
      screen.queryByRole("button", { name: "use it" }),
    ).not.toBeInTheDocument();
  });

  // An edit can name a repo off the deployment's list -- the picker's
  // "Other…" takes any text, and ui.Client.UpdateTask does not park the
  // task the way filing one off the list does -- so the pane is where
  // that gets said, rather than a dispatch that fails with nothing to
  // clone.
  it("warns when the target repo is off the deployment's list", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "acme/elsewhere" }}
        tasks={[]}
        config={{ ...config, targetRepos: ["acme/widgets"] }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    expect(
      screen.getByText(/not on this deployment's target repo list/),
    ).toBeInTheDocument();
  });

  // The config repository is not on targetRepos and never can be -- it
  // is the controller's own checkout (model.ConfigRepo), which is why
  // both the server (model.RepoAllowed) and the picker above
  // (targetableRepos) admit it on any deployment. Warning about it told
  // an operator configuring a task against their own settings that
  // nothing here could clone it, and asked them to widen a list the repo
  // does not belong on.
  it("leaves that warning off the config repository", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "grain/config" }}
        tasks={[]}
        config={{
          ...config,
          configRepo: "grain/config",
          targetRepos: ["acme/widgets"],
        }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    expect(
      screen.queryByText(/not on this deployment's target repo list/),
    ).not.toBeInTheDocument();
  });

  // Two spellings of one repository are one repository to GitHub, and
  // the list is matched that way everywhere else (model.RepoRef.Same,
  // state.js's repoKey) -- so a repo on the list in another case is on
  // the list.
  it("leaves that warning off a listed repo spelled in another case", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "Acme/Widgets" }}
        tasks={[]}
        config={{ ...config, targetRepos: ["acme/widgets"] }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    expect(
      screen.queryByText(/not on this deployment's target repo list/),
    ).not.toBeInTheDocument();
  });

  it("leaves that warning off a deployment with no repo list at all", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, repo: "acme/elsewhere" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    expect(
      screen.queryByText(/not on this deployment's target repo list/),
    ).not.toBeInTheDocument();
  });

  // grain/task-2310: a running task's run was dispatched against the
  // repo and base it has, so the pane says what they are rather than
  // offering pickers whose edit could not reach that run.
  it("shows a running task's repo and base as read-only rows", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "running",
          repo: "acme/widgets",
          base: "release/2",
        }}
        tasks={[]}
        config={{ ...config, targetRepos: ["acme/widgets", "acme/other"] }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={() => {}}
      />,
    );

    expect(screen.queryByLabelText("Repo")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Base branch")).not.toBeInTheDocument();
    expect(screen.getByText("acme/widgets")).toBeInTheDocument();
    expect(screen.getByText("release/2")).toBeInTheDocument();
  });
});
