// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// The repos a new task names: the repos block and what its dropdown is
// built from, the extra repos added under it (grain/task-202), the
// read-only repos picked beside it, and the Base branch picking a repo
// prefills.
//
// Part of the NewTaskOverlay suite -- see NewTaskOverlay.test.jsx for
// why it is four files and what a test in one costs.

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import NewTaskOverlay from "./NewTaskOverlay.jsx";
import { clearBranchCache } from "./BranchField.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));

describe("NewTaskOverlay", () => {
  afterEach(() => {
    api.mockClear();
    // A repo's branches are cached for as long as the module is loaded
    // (BranchField.jsx), so one test's list would otherwise answer the
    // next test's box.
    clearBranchCache();
  });

  // A fresh install starts with no git connection (a Cloudtop's no
  // longer seeds depot): the Repos field says to add one first, and stops
  // saying so for a team that has one, or for a task with no repo.
  it("says to add a git connection first for a deployment that has none", async () => {
    const config = {
      targetRepos: ["acme/widgets"],
      deployment: { id: "default", hasGitCredential: false },
    };
    const user = userEvent.setup();
    const { unmount } = render(
      <NewTaskOverlay
        tasks={[]}
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );
    expect(screen.getByTestId("no-git-connection")).toHaveTextContent(
      "This deployment has no git connection yet",
    );
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    expect(screen.queryByTestId("no-git-connection")).not.toBeInTheDocument();
    unmount();

    // A connection on another host than github.com is still one.
    const connected = {
      ...config,
      deployment: {
        ...config.deployment,
        gitConnections: [
          { host: "depot.code.forge.test", credential: "ambient-forgecli" },
        ],
      },
    };
    render(
      <NewTaskOverlay
        tasks={[]}
        config={connected}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );
    expect(screen.queryByTestId("no-git-connection")).not.toBeInTheDocument();
  });

  // grain/task-370: a base branch is picked the way the repo above it
  // is. Both halves of the list are here -- the branches grain knows
  // about on the picked repo, and the ones this deployment's own tasks
  // are already working off there -- because neither is complete on its
  // own: grain's list is what it has been asked to create or adopt, and
  // the tasks' bases are only where work has already been done.
  it("offers the picked repo's branches under Base branch", async () => {
    const config = { targetRepos: ["acme/widgets"] };
    const tasks = [
      {
        id: "12",
        repo: "acme/widgets",
        base: "release/2",
        createdAt: "2026-08-01T00:00:00Z",
      },
    ];
    api.mockResolvedValue([{ repo: "acme/widgets", name: "rel.latest" }]);
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={tasks}
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    await user.clear(screen.getByLabelText("Base branch"));
    await user.click(screen.getByLabelText("Base branch"));

    expect(await screen.findByRole("menuitem", { name: "rel.latest" }));
    expect(
      screen.getByRole("menuitem", { name: "release/2" }),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("menuitem", { name: "rel.latest" }));
    expect(screen.getByLabelText("Base branch")).toHaveValue("rel.latest");
  });

  // grain/task-241: read-only repos are picked the way dependencies are,
  // over the same repo list the target-repo dropdown offers -- a repo
  // this deployment already works with is a click rather than something
  // to spell out from memory.
  it("picks read-only repos from the repos this deployment already knows", async () => {
    const config = { targetRepos: ["acme/widgets", "acme/shared-lib"] };
    const tasks = [
      { id: "12", title: "Fix the login bug", repo: "other/tooling" },
    ];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={tasks}
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Ship");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");

    const readsInput = screen.getByLabelText(/Read-only repos/);
    await user.click(readsInput);
    await user.click(
      await screen.findByRole("menuitem", { name: "acme/shared-lib" }),
    );
    await user.click(readsInput);
    await user.click(
      await screen.findByRole("menuitem", { name: "other/tooling" }),
    );
    // A repo neither the deployment nor any task has named yet still has
    // to be reachable, the gap RepoField's own "Other…" covers.
    await user.type(readsInput, "new/repo");
    await user.click(
      await screen.findByRole("menuitem", { name: "Add new/repo" }),
    );

    await user.click(screen.getByRole("button", { name: "Create task" }));

    // The create request is sent after an await; read it once it has

    // gone rather than in the same tick, which a loaded CI host loses.

    await waitFor(() => expect(api).toHaveBeenCalled());

    const payload = JSON.parse(api.mock.calls[0][1].body);
    expect(payload.reads).toEqual([
      "acme/shared-lib",
      "other/tooling",
      "new/repo",
    ]);
  });

  // grain/task-202: naming a second repo is not a second question. The
  // repos block holds the repo the run pushes to and the ones it may
  // only propose work in, one under the other, and nothing on screen
  // asks which kind a repo is -- the first one named is the task's own,
  // and the rest are filed as writes (ui.CreateTaskRequest.Writes)
  // without whoever filed it having had to say so.
  it("files the repos named in one block as the task's own repo and its extra writes", async () => {
    const config = {
      capabilities: [],
      targetRepos: ["acme/widgets", "acme/docs"],
    };
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Ship");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    // The same block, one box further down: no field of its own to find
    // and no "other repos" wording to work out first.
    await user.click(screen.getByLabelText(/Another repo/));
    await user.click(
      await screen.findByRole("menuitem", { name: "acme/docs" }),
    );
    await user.type(
      screen.getByLabelText("Base branch in acme/docs"),
      "publish",
    );

    await user.click(screen.getByRole("button", { name: "Create task" }));

    // The filing itself, not simply the first call: the branch box
    // beside acme/docs asks that repo what branches grain knows about
    // there on the way (BranchField.jsx), which is a GET before this
    // POST.
    const payload = JSON.parse(
      api.mock.calls.find(([path]) => path === "/api/tasks")[1].body,
    );
    expect(payload.repo).toBe("acme/widgets");
    expect(payload.writes).toEqual([{ repo: "acme/docs", base: "publish" }]);
  });

  // grain/task-2317: "Create a new branch" files the task onto the
  // branch it names and asks grain to cut that from Base branch, which
  // the box relabels "Create from".
  it("files a task onto a new branch cut from the picked base", async () => {
    const config = { capabilities: [], targetRepos: ["acme/widgets"] };
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Ship");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    await user.click(screen.getByLabelText("Create a new branch"));
    // Nothing to file onto until the branch is named.
    expect(screen.getByRole("button", { name: "Create task" })).toBeDisabled();
    await user.type(screen.getByTestId("new-branch-name"), "feature/x");
    await user.type(screen.getByLabelText(/^Create from/), "release/1.2");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const payload = JSON.parse(
      api.mock.calls.find(([path]) => path === "/api/tasks")[1].body,
    );
    expect(payload).toMatchObject({
      repo: "acme/widgets",
      base: "feature/x",
      newBranch: true,
      newBranchFrom: "release/1.2",
    });
  });

  it("files a plain base when the new-branch box is left unticked", async () => {
    const config = { capabilities: [], targetRepos: ["acme/widgets"] };
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Ship");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    expect(screen.queryByTestId("new-branch-name")).toBeNull();
    await user.type(screen.getByLabelText(/^Base branch/), "release/1.2");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const payload = JSON.parse(
      api.mock.calls.find(([path]) => path === "/api/tasks")[1].body,
    );
    expect(payload.base).toBe("release/1.2");
    expect(payload.newBranch).toBeUndefined();
    expect(payload.newBranchFrom).toBeUndefined();
  });

  // grain/task-85: "No repo" is one of the target repo list's own
  // options now, so picking a repo after it has to take the task back
  // off noRepo -- there is no checkbox left to untick.
  it("files against a repo picked after No repo, and drops the noRepo choice with it", async () => {
    const config = { capabilities: [], targetRepos: ["acme/widgets"] };
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Ship");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    // The create request is sent after an await; read it once it has

    // gone rather than in the same tick, which a loaded CI host loses.

    await waitFor(() => expect(api).toHaveBeenCalled());

    const payload = JSON.parse(api.mock.calls[0][1].body);
    expect(payload.repo).toBe("acme/widgets");
    expect(payload.noRepo).toBe(false);
  });

  // The 2026-10-13 walkthrough: picking a repo prefills Base branch from
  // that repo's last task, and switching to No repo afterwards still
  // sent that branch, so the no-repo task showed it as its target -- and
  // kept it, unasked, when a repo was later given to it in its pane.
  it("sends no base branch for a task switched to No repo after picking a repo", async () => {
    const config = { capabilities: [], targetRepos: ["acme/widgets"] };
    const tasks = [
      {
        id: "12",
        repo: "acme/widgets",
        base: "release-1.0",
        createdAt: "2026-08-01T00:00:00Z",
      },
    ];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={tasks}
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Think");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    expect(screen.getByLabelText("Base branch")).toHaveValue("release-1.0");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const post = api.mock.calls.find(([, opts]) => opts?.method === "POST");
    const payload = JSON.parse(post[1].body);
    expect(payload.noRepo).toBe(true);
    expect(payload.repo).toBe("");
    expect(payload.base).toBe("");
  });

  it("offers a repo dropdown built from existing tasks' repos, instead of a bare text field", async () => {
    // Unrestricted, which is where a task's own repo is the evidence the
    // dropdown is built from (state.js's targetableRepos).
    const config = { capabilities: [], targetRepos: [] };
    const tasks = [{ id: "1", title: "Old task", repo: "acme/other" }];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={tasks}
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Ship");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/other");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    // The create request is sent after an await; read it once it has

    // gone rather than in the same tick, which a loaded CI host loses.

    await waitFor(() => expect(api).toHaveBeenCalled());

    const payload = JSON.parse(api.mock.calls[0][1].body);
    expect(payload.repo).toBe("acme/other");
  });

  // The same dropdown on a restricted deployment: acme/other is a repo
  // an old task names and the allowlist does not, so filing there now
  // would only park (ui.(*Client).parkOffAllowlist). The reads picker
  // beside it is not gated that way and still offers it.
  it("leaves an off-allowlist repo out of the target dropdown, but not out of the reads picker", async () => {
    const config = { capabilities: [], targetRepos: ["acme/widgets"] };
    const tasks = [{ id: "1", title: "Old task", repo: "acme/other" }];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={tasks}
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    const target = screen.getByLabelText(/^Repos/);
    expect(
      within(target).queryByRole("option", { name: "acme/other" }),
    ).not.toBeInTheDocument();
    expect(
      within(target).getByRole("option", { name: "acme/widgets" }),
    ).toBeInTheDocument();

    await user.click(screen.getByLabelText(/Read-only repos/));
    expect(
      await screen.findByRole("menuitem", { name: "acme/other" }),
    ).toBeInTheDocument();
  });

  // The 2026-10-07 walkthrough: "Other…" takes any repo, and the server
  // files one off the list parked awaiting a reply rather than refusing
  // it (ui.Client.parkOffAllowlist). The form closed without a word, so
  // it says so before submit -- and says nothing about a listed repo, or
  // about the config repository every deployment may target.
  it("warns before filing on a repo off the target list", async () => {
    const config = { capabilities: [], targetRepos: ["acme/widgets"] };
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={[]}
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    expect(screen.queryByTestId("repo-off-allowlist")).not.toBeInTheDocument();

    await user.selectOptions(screen.getByLabelText(/^Repos/), "Other…");
    const typed = screen.getByPlaceholderText("owner/name");
    await user.clear(typed);
    await user.type(typed, "acme/gizmos");
    expect(screen.getByTestId("repo-off-allowlist")).toHaveTextContent(
      "acme/gizmos is not on this deployment's target repo list",
    );

    await user.clear(typed);
    await user.type(typed, "grain/config");
    expect(screen.queryByTestId("repo-off-allowlist")).not.toBeInTheDocument();
  });

  // grain/task-271: the repo picked at the top of the block is the one
  // the run pushes to, so the reads picker under it stops offering it --
  // it is the one suggestion the server would refuse
  // (ui.refuseTargetAmongReads), and it follows the dropdown rather than
  // being fixed at whatever was picked first.
  it("drops the repo picked as the target from the reads picker", async () => {
    const config = {
      capabilities: [],
      targetRepos: ["acme/widgets", "acme/docs"],
    };
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    await user.click(screen.getByLabelText(/Read-only repos/));
    expect(
      await screen.findByRole("menuitem", { name: "acme/docs" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("menuitem", { name: "acme/widgets" }),
    ).not.toBeInTheDocument();

    // Moving the target moves what is offered with it.
    await user.keyboard("{Escape}");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/docs");
    await user.click(screen.getByLabelText(/Read-only repos/));
    expect(
      await screen.findByRole("menuitem", { name: "acme/widgets" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("menuitem", { name: "acme/docs" }),
    ).not.toBeInTheDocument();
  });

  // bwsalmon/agents#641: picking a repo prefills Base branch from
  // whatever base that repo's own most recent task used, so a repo
  // whose work lives off a release branch doesn't make every new task
  // against it retype that branch name by hand.
  it("prefills Base branch from the picked repo's most recent task", async () => {
    const config = { capabilities: [], targetRepos: ["acme/widgets"] };
    const tasks = [
      {
        id: "1",
        title: "Old task",
        repo: "acme/widgets",
        base: "main",
        createdAt: "2026-01-01T00:00:00Z",
      },
      {
        id: "2",
        title: "Newer task",
        repo: "acme/widgets",
        base: "release/2.0",
        createdAt: "2026-06-01T00:00:00Z",
      },
      {
        id: "3",
        title: "Other repo task",
        repo: "acme/other",
        base: "main",
        createdAt: "2026-08-01T00:00:00Z",
      },
    ];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={tasks}
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Ship");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    // The create request is sent after an await; read it once it has

    // gone rather than in the same tick, which a loaded CI host loses.

    await waitFor(() => expect(api).toHaveBeenCalled());

    const payload = JSON.parse(api.mock.calls[0][1].body);
    expect(payload.base).toBe("release/2.0");
  });

  // The failure this guards against is self-perpetuating: a base branch
  // that merges and is deleted makes its task fail, that failed task is
  // then the repo's most recent one carrying that base, so the dead
  // branch is suggested again, and the next task fails the same way.
  it("does not prefill Base branch from a task that failed", async () => {
    const config = { capabilities: [], targetRepos: ["acme/widgets"] };
    const tasks = [
      {
        id: "1",
        title: "Good task",
        repo: "acme/widgets",
        base: "release/2.0",
        state: "completed",
        createdAt: "2026-01-01T00:00:00Z",
      },
      {
        id: "2",
        title: "Died on a deleted base",
        repo: "acme/widgets",
        base: "grain/issue-642",
        state: "failed",
        createdAt: "2026-06-01T00:00:00Z",
      },
    ];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={tasks}
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Ship");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    // The create request is sent after an await; read it once it has

    // gone rather than in the same tick, which a loaded CI host loses.

    await waitFor(() => expect(api).toHaveBeenCalled());

    const payload = JSON.parse(api.mock.calls[0][1].body);
    expect(payload.base).toBe("release/2.0");
  });

  it("prefills nothing when every task carrying a base for that repo failed", async () => {
    const config = { capabilities: [], targetRepos: ["acme/widgets"] };
    const tasks = [
      {
        id: "1",
        title: "Died",
        repo: "acme/widgets",
        base: "grain/issue-642",
        state: "failed",
        createdAt: "2026-06-01T00:00:00Z",
      },
      {
        id: "2",
        title: "Closed too",
        repo: "acme/widgets",
        base: "grain/issue-642",
        state: "closed",
        createdAt: "2026-06-02T00:00:00Z",
      },
    ];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={tasks}
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Ship");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    // The create request is sent after an await; read it once it has

    // gone rather than in the same tick, which a loaded CI host loses.

    await waitFor(() => expect(api).toHaveBeenCalled());

    const payload = JSON.parse(api.mock.calls[0][1].body);
    expect(payload.base).toBe("");
  });

  // A system-generated task -- a schedule firing, a suite pass, a
  // stacked fix, an agent's own propose_task -- picks a base for its own
  // reasons, and that choice is not a suggestion for the human filing
  // the next one.
  it("does not prefill Base branch from a task an agent or automation filed", async () => {
    const config = { capabilities: [], targetRepos: ["acme/widgets"] };
    const tasks = [
      {
        id: "1",
        title: "Filed by hand",
        repo: "acme/widgets",
        base: "release/2.0",
        authorKind: "human",
        createdAt: "2026-01-01T00:00:00Z",
      },
      {
        id: "2",
        title: "A suite pass",
        repo: "acme/widgets",
        base: "suite/run-7",
        authorKind: "automation",
        createdAt: "2026-06-01T00:00:00Z",
      },
      {
        id: "3",
        title: "An agent's proposal",
        repo: "acme/widgets",
        base: "grain/task-3",
        authorKind: "agent",
        createdAt: "2026-07-01T00:00:00Z",
      },
    ];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={tasks}
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Ship");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    // The create request is sent after an await; read it once it has

    // gone rather than in the same tick, which a loaded CI host loses.

    await waitFor(() => expect(api).toHaveBeenCalled());

    const payload = JSON.parse(api.mock.calls[0][1].body);
    expect(payload.base).toBe("release/2.0");
  });

  // The counterpart to the "no history" case below: a repo whose only
  // tasks are system-generated has nothing to prefill from either, so
  // picking it is the no-history case rather than the "most recent task
  // used the default branch" one, and must not clear the field.
  it("treats a repo whose only tasks are system-generated as having no history", async () => {
    const config = {
      capabilities: [],
      targetRepos: ["acme/widgets", "acme/robots"],
    };
    const tasks = [
      {
        id: "1",
        title: "Filed by hand",
        repo: "acme/widgets",
        base: "release/2.0",
        authorKind: "human",
        createdAt: "2026-01-01T00:00:00Z",
      },
      {
        id: "2",
        title: "A schedule fired",
        repo: "acme/robots",
        base: "nightly",
        authorKind: "automation",
        createdAt: "2026-06-01T00:00:00Z",
      },
    ];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={tasks}
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Ship");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    expect(screen.getByLabelText(/Base branch/)).toHaveValue("release/2.0");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/robots");
    expect(screen.getByLabelText(/Base branch/)).toHaveValue("release/2.0");
  });

  it("leaves a manually-typed Base branch alone when the picked repo has no task history", async () => {
    const config = {
      capabilities: [],
      targetRepos: ["acme/widgets", "acme/fresh"],
    };
    const tasks = [
      {
        id: "1",
        title: "Old task",
        repo: "acme/widgets",
        base: "release/2.0",
        createdAt: "2026-01-01T00:00:00Z",
      },
    ];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={tasks}
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Ship");
    await user.type(screen.getByLabelText(/Base branch/), "my-branch");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/fresh");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    // The create request is sent after an await; read it once it has

    // gone rather than in the same tick, which a loaded CI host loses.

    await waitFor(() => expect(api).toHaveBeenCalled());

    const payload = JSON.parse(api.mock.calls[0][1].body);
    expect(payload.base).toBe("my-branch");
  });

  it("leaves a manually-typed Base branch alone even when the picked repo has history", async () => {
    const config = { capabilities: [], targetRepos: ["acme/widgets"] };
    const tasks = [
      {
        id: "1",
        title: "Old task",
        repo: "acme/widgets",
        base: "release/2.0",
        createdAt: "2026-01-01T00:00:00Z",
      },
    ];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={tasks}
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Ship");
    await user.type(screen.getByLabelText(/Base branch/), "my-branch");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    // The create request is sent after an await; read it once it has

    // gone rather than in the same tick, which a loaded CI host loses.

    await waitFor(() => expect(api).toHaveBeenCalled());

    const payload = JSON.parse(api.mock.calls[0][1].body);
    expect(payload.base).toBe("my-branch");
  });

  // A repo's most recent task can itself have deliberately used the
  // deployment default (no base at all) -- that is a real suggestion
  // ("build off the default branch here"), not an absence of one, and
  // should clear out whatever base picking an earlier repo prefilled.
  it("clears a prefilled Base branch when the newly-picked repo's most recent task used the default branch", async () => {
    const config = {
      capabilities: [],
      targetRepos: ["acme/widgets", "acme/other"],
    };
    const tasks = [
      {
        id: "1",
        title: "Off a release branch",
        repo: "acme/widgets",
        base: "release/2.0",
        createdAt: "2026-01-01T00:00:00Z",
      },
      {
        id: "2",
        title: "Off the default branch",
        repo: "acme/other",
        base: "",
        createdAt: "2026-06-01T00:00:00Z",
      },
    ];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={tasks}
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Ship");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    expect(screen.getByLabelText(/Base branch/)).toHaveValue("release/2.0");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/other");
    expect(screen.getByLabelText(/Base branch/)).toHaveValue("");

    await user.click(screen.getByRole("button", { name: "Create task" }));
    // The create request is sent after an await; read it once it has
    // gone rather than in the same tick, which a loaded CI host loses.
    await waitFor(() => expect(api).toHaveBeenCalled());
    const payload = JSON.parse(api.mock.calls[0][1].body);
    expect(payload.base).toBe("");
  });
});
