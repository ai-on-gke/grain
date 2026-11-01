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

// The two pickers a new task's dependencies come out of: the
// capabilities it runs with -- seeded from the deployment's defaults and
// the picked repo's own, and left alone once a human has touched it --
// and the tasks it waits on.
//
// Part of the NewTaskOverlay suite -- see NewTaskOverlay.test.jsx for
// why it is four files and what a test in one costs.

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import NewTaskOverlay from "./NewTaskOverlay.jsx";
import { resetCapabilityMethodsForTest } from "./capabilityMethods.js";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));

// filed is the body of the form's POST /api/tasks: the form also asks
// POST /api/capability-grants/check about whatever it holds, so the
// filing is not necessarily the first call.
const filed = () =>
  JSON.parse(api.mock.calls.find(([path]) => path === "/api/tasks")[1].body);

// ticked is what the Legacy capabilities picker shows as held.
const ticked = () => screen.getByLabelText("Legacy capabilities").textContent;

describe("NewTaskOverlay", () => {
  afterEach(() => {
    api.mockReset();
    api.mockResolvedValue(null);
    resetCapabilityMethodsForTest();
  });

  it("adds depends-on tasks via the picker and includes checked capabilities", async () => {
    const config = {
      capabilities: [
        { id: "web-search", name: "Web search" },
        { id: "shell", name: "Shell" },
      ],
    };
    const tasks = [
      { id: "12", title: "Fix the login bug" },
      { id: "15", title: "Add dark mode" },
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

    const dependsOnInput = screen.getByPlaceholderText(
      "Search tasks to depend on…",
    );
    await user.type(dependsOnInput, "12");
    await user.click(await screen.findByText("Fix the login bug"));
    await user.type(dependsOnInput, "15");
    await user.click(await screen.findByText("Add dark mode"));

    await user.click(screen.getByLabelText("Legacy capabilities"));
    await user.click(await screen.findByRole("option", { name: "Web search" }));
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const payload = filed();
    expect(payload.dependsOn).toEqual(["12", "15"]);
    expect(payload.capabilities).toEqual(["web-search"]);
  });

  // The picker used to offer every capability grain ships with no sign
  // of whether this deployment could honour it, so ticking gemini-key on
  // a deployment with no GCP project filed a task whose only symptom was
  // failing to dispatch later. It is not offered at all now: a row
  // nobody can act on here is worse than no row, and Settings >
  // Capabilities is where it is configured and where its own reason for
  // not being ready is spelled out.
  it("leaves a capability this deployment is not configured for out of the picker", async () => {
    const config = {
      capabilities: [
        {
          id: "gemini-key",
          name: "Gemini key",
          ready: false,
          needs: ["GCP project"],
        },
        { id: "self-debug", name: "Self debug", ready: true },
      ],
    };
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

    await user.click(screen.getByLabelText("Legacy capabilities"));
    expect(
      await screen.findByRole("option", { name: /Self debug/ }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("option", { name: /Gemini key/ }),
    ).not.toBeInTheDocument();
  });

  // The one unready capability a form still shows is one already ticked
  // -- a deployment default here -- because a grant with no row is one
  // nothing can take off again. It keeps its warning, and files.
  it("still offers, and warns about, an unready capability that is already ticked", async () => {
    const config = {
      capabilities: [
        {
          id: "gemini-key",
          name: "Gemini key",
          ready: false,
          needs: ["GCP project"],
        },
        { id: "self-debug", name: "Self debug", ready: true },
      ],
      defaultCapabilities: ["gemini-key"],
    };
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

    await user.click(screen.getByLabelText("Legacy capabilities"));
    const gemini = await screen.findByRole("option", { name: /Gemini key/ });
    expect(gemini).toHaveTextContent("GCP project");
    expect(gemini).toHaveTextContent(/fail to dispatch/);
    // A ready capability gets no such line.
    expect(
      screen.getByRole("option", { name: /Self debug/ }),
    ).not.toHaveTextContent(/Not ready/);

    await user.keyboard("{Escape}");
    await user.type(screen.getByLabelText(/Title/), "Ship");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Create task" }));
    // Untouched, so left to the server's own defaults, which hold it.
    expect(ticked()).toMatch(/Gemini key/);
    expect(filed()).not.toHaveProperty("capabilities");
  });

  // Unticking that default used to take its row away with it: the
  // option vanished under the click, so it could not be ticked back.
  it("keeps an unticked unready default in the picker so it can be re-ticked", async () => {
    const config = {
      capabilities: [
        { id: "gemini-key", name: "Gemini key", ready: false },
        { id: "self-debug", name: "Self debug", ready: true },
      ],
      defaultCapabilities: ["gemini-key"],
    };
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

    await user.click(screen.getByLabelText("Legacy capabilities"));
    await user.click(await screen.findByRole("option", { name: /Gemini key/ }));
    const gemini = screen.getByRole("option", { name: /Gemini key/ });
    expect(gemini).toHaveAttribute("aria-selected", "false");
    await user.click(gemini);
    expect(screen.getByRole("option", { name: /Gemini key/ })).toHaveAttribute(
      "aria-selected",
      "true",
    );
  });

  // grain/task-14: the deployment's own default capabilities arrive
  // on GET /api/config and open the form already ticked, so filing a
  // task on a deployment that defaults gcp-key gets one without anyone
  // remembering to ask. The payload leaves an untouched picker to the
  // server, which applies the same defaults for the task's own target
  // and drops one the capability catalogue refuses there; a picker
  // somebody has changed is named exactly (the test after this one).
  it("seeds the capability picker from the deployment's defaults", async () => {
    const config = {
      capabilities: [
        { id: "gcp-key", name: "GCP key" },
        { id: "gemini-key", name: "Gemini key" },
      ],
      defaultCapabilities: ["gcp-key"],
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

    await user.type(screen.getByLabelText(/Title/), "Key");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(ticked()).toMatch(/GCP key/);
    expect(filed()).not.toHaveProperty("capabilities");
  });

  it("files a task without a defaulted capability once it is unticked", async () => {
    const config = {
      capabilities: [
        { id: "gcp-key", name: "GCP key" },
        { id: "gemini-key", name: "Gemini key" },
      ],
      defaultCapabilities: ["gcp-key"],
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

    await user.type(screen.getByLabelText(/Title/), "No key");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByLabelText("Legacy capabilities"));
    await user.click(await screen.findByRole("option", { name: "GCP key" }));
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const payload = filed();
    expect(payload.capabilities).toEqual([]);
  });

  it("shows a picked dependency's whole task on hover", async () => {
    const tasks = [{ id: "12", title: "Fix the login bug" }];
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={tasks}
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(
      screen.getByPlaceholderText("Search tasks to depend on…"),
      "12",
    );
    await user.click(await screen.findByText("Fix the login bug"));

    await user.hover(screen.getByText("12 Fix the login bug"));
    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "12 Fix the login bug",
    );
  });

  // grain/task-24: a repo can default capabilities of its own on top of
  // the deployment's, so picking a repo re-seeds the picker with the
  // union -- the same answer CreateTask would resolve server-side for a
  // task filed against it.
  it("adds the picked repo's own default capabilities to the deployment's", async () => {
    const config = {
      capabilities: [
        { id: "gcp-key", name: "GCP key" },
        { id: "gemini-key", name: "Gemini key" },
      ],
      targetRepos: ["acme/widgets", "acme/gadgets"],
      defaultCapabilities: ["gemini-key"],
      repoDefaultCapabilities: { "acme/widgets": ["gcp-key"] },
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

    await user.type(screen.getByLabelText(/Title/), "Key");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    expect(ticked()).toMatch(/Gemini key.*GCP key/);
    await user.click(screen.getByRole("button", { name: "Create task" }));
    expect(filed()).not.toHaveProperty("capabilities");
  });

  // Switching away from a repo that adds one drops it again: leaving the
  // previous repo's extras ticked would file the task with capabilities
  // the repo it actually targets never asked for.
  it("re-seeds the picker when the repo changes", async () => {
    const config = {
      capabilities: [
        { id: "gcp-key", name: "GCP key" },
        { id: "gemini-key", name: "Gemini key" },
      ],
      targetRepos: ["acme/widgets", "acme/gadgets"],
      defaultCapabilities: ["gemini-key"],
      repoDefaultCapabilities: { "acme/widgets": ["gcp-key"] },
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

    await user.type(screen.getByLabelText(/Title/), "Else");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/gadgets");
    expect(ticked()).toMatch(/Gemini key/);
    expect(ticked()).not.toMatch(/GCP key/);
    await user.click(screen.getByRole("button", { name: "Create task" }));
    expect(filed()).not.toHaveProperty("capabilities");
  });

  // Once the picker has been touched the ticks are the human's own: a
  // re-seed that put back a capability they had just unticked would file
  // a task with something they had already said no to.
  it("leaves a hand-edited capability picker alone when the repo changes", async () => {
    const config = {
      capabilities: [
        { id: "gcp-key", name: "GCP key" },
        { id: "gemini-key", name: "Gemini key" },
      ],
      targetRepos: ["acme/widgets", "acme/gadgets"],
      defaultCapabilities: ["gemini-key"],
      repoDefaultCapabilities: { "acme/widgets": ["gcp-key"] },
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

    await user.type(screen.getByLabelText(/Title/), "Mine");
    await user.click(screen.getByLabelText("Legacy capabilities"));
    await user.click(await screen.findByRole("option", { name: "Gemini key" }));
    await user.keyboard("{Escape}");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(filed().capabilities).toEqual([]);
  });

  it("renders retired or unoffered capabilities from draft so they can be unticked", async () => {
    const config = {
      capabilities: [{ id: "self-debug", name: "Self debug", ready: true }],
    };
    localStorage.setItem(
      "grain.task.draft",
      JSON.stringify({
        title: "Draft task",
        capabilities: ["grain-ci", "self-debug"],
      }),
    );
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.click(screen.getByLabelText("Legacy capabilities"));
    const grainCIRow = await screen.findByRole("option", { name: /grain-ci/ });
    expect(grainCIRow).toBeInTheDocument();
    expect(grainCIRow).toHaveAttribute(
      "title",
      expect.stringMatching(/no longer offered/i),
    );

    // Untick grain-ci
    await user.click(grainCIRow);
    await user.keyboard("{Escape}");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(filed().capabilities).toEqual(["self-debug"]);
  });

  it("marks a capability method denied by the capability policy for agent tasks and links to the policy", async () => {
    api.mockImplementation(async (path) => {
      if (path === "/api/capability-methods") {
        return [
          {
            ref: "https://grain.dev/capabilities/config-map#config-map",
            capability: "https://grain.dev/capabilities/config-map",
            method: "config-map",
            requiresArgs: true,
            policy: { task: "denied" },
          },
          {
            ref: "https://grain.dev/capabilities/gcp#gcp-key",
            capability: "https://grain.dev/capabilities/gcp",
            method: "gcp-key",
            requiresArgs: true,
            policy: { task: "may_be_refused" },
          },
        ];
      }
      return null;
    });
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={{ capabilityMethods: 2 }}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    const grants = screen.getByTestId("new-task-grants");
    await user.click(
      within(grants).getByRole("button", { name: "Add grant…" }),
    );
    const deniedRow = await screen.findByRole("menuitem", {
      name: /config-map/,
    });
    expect(deniedRow).toHaveTextContent(
      "denied by the capability policy for agent tasks",
    );
    expect(
      within(deniedRow).getByRole("link", { name: "Settings → Capabilities" }),
    ).toHaveAttribute("href", "/settings/capabilities#capability-policy");

    const repoRow = screen.getByRole("menuitem", { name: /gcp-key/ });
    expect(repoRow).toHaveTextContent("may be refused");
    expect(
      within(repoRow).getByRole("link", { name: "Settings → Capabilities" }),
    ).toHaveAttribute("href", "/settings/capabilities#capability-policy");
  });
  // 2026-10-29 test run, Bug 3: a deployment default the capability
  // catalogue restricts to one repo, on a No repo task. The form asks
  // the server about what it holds for that target, marks the refused
  // default where it is ticked, and leaves the untouched list to the
  // server -- which drops it -- rather than naming it and being refused.
  const refusingCheck = async (path, opts) => {
    if (path !== "/api/capability-grants/check") return null;
    const body = JSON.parse(opts.body);
    return body.grants.map(({ grant }) => ({
      grant,
      recorded: grant,
      ...(grant === "gcp-key" && body.subject?.repo !== "acme/widgets"
        ? {
            catalogueRefusal:
              "capability gcp-key is not granted on a task with no repository (none) by settings/capabilities.yml (repos: acme/widgets)",
          }
        : {}),
    }));
  };
  const restrictedConfig = {
    capabilities: [
      { id: "gcp-key", name: "GCP key" },
      { id: "gemini-key", name: "Gemini key" },
    ],
    targetRepos: ["acme/widgets"],
    defaultCapabilities: ["gcp-key", "gemini-key"],
  };

  it("marks a default the catalogue refuses on No repo and leaves it to the server", async () => {
    api.mockImplementation(refusingCheck);
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={restrictedConfig}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Standalone");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    const note = await screen.findByTestId("new-task-catalogue-refused");
    expect(note).toHaveTextContent(
      "settings/capabilities.yml refuses gcp-key on a task with no repo, so that default is left off this task.",
    );
    const check = api.mock.calls.filter(
      ([path]) => path === "/api/capability-grants/check",
    );
    expect(JSON.parse(check.at(-1)[1].body).subject).toEqual({ repo: "" });
    expect(
      screen
        .getByLabelText("Legacy capabilities")
        .querySelectorAll("[data-catalogue-refused]"),
    ).toHaveLength(1);

    await user.click(screen.getByRole("button", { name: "Create task" }));
    expect(filed()).not.toHaveProperty("capabilities");
    expect(filed().noRepo).toBe(true);
  });

  it("asks for a refused grant to be unticked once the picker is the person's", async () => {
    api.mockImplementation(refusingCheck);
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={restrictedConfig}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByLabelText("Legacy capabilities"));
    await user.click(await screen.findByRole("option", { name: /Gemini key/ }));
    const gcp = screen.getByRole("option", { name: /GCP key/ });
    // Still ticked, so still enabled: its row is how it comes off.
    expect(gcp).not.toHaveAttribute("aria-disabled", "true");
    expect(gcp).toHaveTextContent("repos: acme/widgets");
    await user.keyboard("{Escape}");
    expect(
      await screen.findByTestId("new-task-catalogue-refused"),
    ).toHaveTextContent(
      "settings/capabilities.yml refuses gcp-key on a task with no repo: untick it, or the task will not be filed.",
    );

    // On the repo it is restricted to, nothing is refused.
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    await vi.waitFor(() =>
      expect(
        screen.queryByTestId("new-task-catalogue-refused"),
      ).not.toBeInTheDocument(),
    );
  });
});
