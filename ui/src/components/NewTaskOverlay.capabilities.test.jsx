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
import { chooseOption } from "../searchSelectTesting.js";

vi.mock("../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));

// filed is the body of the form's POST /api/tasks: the form also asks
// POST /api/capability-grants/check about whatever it holds, so the
// filing is not necessarily the first call.
const filed = () =>
  JSON.parse(api.mock.calls.find(([path]) => path === "/api/tasks")[1].body);

describe("NewTaskOverlay", () => {
  afterEach(() => {
    api.mockReset();
    api.mockResolvedValue(null);
    resetCapabilityMethodsForTest();
  });

  it("adds depends-on tasks via the picker", async () => {
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
    await chooseOption(user, screen.getByLabelText(/^Repos/), "none");

    const dependsOnInput = screen.getByPlaceholderText(
      "Search tasks to depend on…",
    );
    await user.type(dependsOnInput, "12");
    await user.click(await screen.findByText("Fix the login bug"));
    await user.type(dependsOnInput, "15");
    await user.click(await screen.findByText("Add dark mode"));

    await user.click(screen.getByRole("button", { name: "Create task" }));

    const payload = filed();
    expect(payload.dependsOn).toEqual(["12", "15"]);
  });

  // The answer to the filing goes to onCreated, so the page behind the
  // form can say what it carries beyond "filed": the default grants the
  // capability policy kept off the task (DroppedDefaultsNotice).
  it("hands the filed task, dropped defaults and all, to onCreated", async () => {
    const created = {
      id: "41",
      droppedDefaults: [
        {
          grant: "https://grain.dev/capabilities/grain#journal",
          reason: "the capability policy denies it (rule 1)",
        },
      ],
    };
    api.mockImplementation(async (path) =>
      path === "/api/tasks" ? created : null,
    );
    const onCreated = vi.fn().mockResolvedValue(undefined);
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        tasks={[]}
        config={{}}
        onClose={() => {}}
        onCreated={onCreated}
        showError={() => {}}
      />,
    );
    await user.type(screen.getByLabelText(/Title/), "Ship");
    await chooseOption(user, screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Create task" }));
    await vi.waitFor(() => expect(onCreated).toHaveBeenCalledWith(created));
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
    await chooseOption(user, screen.getByLabelText(/^Repos/), "none");
    const note = await screen.findByTestId("new-task-catalogue-refused");
    expect(note).toHaveTextContent(
      "settings/capabilities.yml refuses gcp-key on a task with no repo, so that default is left off this task.",
    );
    const check = api.mock.calls.filter(
      ([path]) => path === "/api/capability-grants/check",
    );
    expect(JSON.parse(check.at(-1)[1].body).subject).toEqual({ repo: "" });

    await user.click(screen.getByRole("button", { name: "Create task" }));
    expect(filed()).not.toHaveProperty("capabilities");
    expect(filed().noRepo).toBe(true);
  });

  it("edits https://grain.dev/capabilities/api# defaults in GrantsPicker and keeps bare ones as they are", async () => {
    api.mockImplementation(async (path) => {
      if (path.split("?")[0] === "/api/capability-methods") {
        return [
          {
            ref: "https://grain.dev/capabilities/api#ci:read",
            capability: "https://grain.dev/capabilities/api",
            method: "ci:read",
          },
        ];
      }
      return [];
    });
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={{
          capabilityMethods: 1,
          capabilities: [
            { id: "self-debug", name: "Self debug" },
            { id: "api:ci:read", summary: "Read CI", description: "Read CI" },
          ],
          targetRepos: ["acme/widgets"],
          defaultTarget: "acme/widgets",
          defaultCapabilities: [
            "self-debug",
            'https://grain.dev/capabilities/api#ci:read={"repo":"acme/widgets"}',
          ],
        }}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    // No legacy checklist: the bare self-debug default has no picker of
    // its own and is filed exactly as the defaults held it.
    expect(screen.queryByLabelText("Legacy capabilities")).toBeNull();
    const grants = screen.getByTestId("new-task-grants");
    const apiGrant = within(grants).getByLabelText(
      "api:ci:read · repo: acme/widgets",
    );
    await user.click(within(apiGrant).getByRole("button", { name: /remove/i }));
    expect(
      within(grants).queryByLabelText("api:ci:read · repo: acme/widgets"),
    ).toBeNull();

    await user.type(screen.getByLabelText(/Title/), "Remove api ref default");
    await chooseOption(user, screen.getByLabelText(/^Repos/), "acme/widgets");
    await user.click(screen.getByRole("button", { name: "Create task" }));
    expect(filed().capabilities).toEqual(["self-debug"]);
  });
});
