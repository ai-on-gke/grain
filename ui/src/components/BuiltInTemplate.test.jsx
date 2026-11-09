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

import { useState } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import ProcessesList from "./ProcessesList.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// One of grain's own built-in templates (ui.Template.System): filed from
// by hand, never edited, and repo-less unless it is bound.
const gke = {
  id: "system:gke-sandbox-cluster",
  name: "Set up a GKE sandbox cluster",
  title: "Set up a GKE sandbox cluster and attach it to grain",
  description: "Walk me through it.",
  fields: [{ name: "project", label: "GCP sandbox project ID" }],
  autoMerge: false,
  interactive: true,
  system: true,
  reads: [],
  capabilities: [
    'https://grain.dev/capabilities/config-map#config-map={"config_maps":["bootstrap"]}',
    "https://grain.dev/capabilities/grain#host-exec",
  ],
  createdAt: "0001-01-01T00:00:00Z",
};

const noop = () => {};

function ControlledList(props) {
  const [openTemplateId, setOpenTemplateId] = useState(null);
  return (
    <ProcessesList
      openTemplateId={openTemplateId}
      onOpenTemplate={setOpenTemplateId}
      onRefreshTemplates={noop}
      onRefreshTasks={noop}
      showError={noop}
      {...props}
    />
  );
}

function routeApi(onCreate) {
  api.mockImplementation((path, opts = {}) => {
    if (path.includes("/composition"))
      return Promise.resolve({ kind: "task", tasks: [] });
    if (path === "/api/tasks" && opts.method === "POST") {
      onCreate?.(JSON.parse(opts.body));
      return Promise.resolve({ id: "7", interactive: true });
    }
    return Promise.resolve({});
  });
}

describe("a built-in template", () => {
  afterEach(() => api.mockReset());

  it("is marked System and files with no repo from Use…", async () => {
    const filed = vi.fn();
    routeApi(filed);
    const user = userEvent.setup();
    render(
      <ControlledList query={{ kinds: ["template"] }} templates={[gke]} />,
    );

    expect(screen.getByText("🔒 System")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Use…" }));
    expect(screen.getByText(/Files with no repo/)).toBeInTheDocument();
    expect(screen.queryByText(/owner\/name/)).not.toBeInTheDocument();

    await user.type(
      screen.getByLabelText(/GCP sandbox project ID/),
      "acme-sandboxes",
    );
    await user.click(screen.getByRole("button", { name: "Create task" }));
    await waitFor(() =>
      expect(filed).toHaveBeenCalledWith(
        expect.objectContaining({
          templateId: "system:gke-sandbox-cluster",
          repo: "",
          fields: { project: "acme-sandboxes" },
        }),
      ),
    );
  });

  it("opens read-only, with no Save, Delete or GrantsPicker edit buttons", async () => {
    routeApi();
    const user = userEvent.setup();
    render(
      <ControlledList query={{ kinds: ["template"] }} templates={[gke]} />,
    );

    await user.click(screen.getByText("Set up a GKE sandbox cluster"));
    expect(screen.getByText(/built into grain/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Delete" })).toBeDisabled();
    expect(
      screen.queryByRole("button", { name: "Add grant…" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Remove .*host-exec/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Add another/i }),
    ).not.toBeInTheDocument();
  });

  it("omits GrantsPicker edit controls on repository-declared templates and schedules", async () => {
    routeApi();
    const user = userEvent.setup();
    const declaredTemplate = {
      id: "repo:template:acme:widgets:bump",
      name: "Declared dependency bump",
      title: "Bump dependencies",
      description: "Upgrade dependencies.",
      repo: "acme/widgets",
      capabilities: ["api:tasks:read"],
      declaredIn: {
        repo: "acme/widgets",
        path: ".grain/templates/bump.yml",
      },
    };
    const declaredSchedule = {
      id: "repo:schedule:acme:widgets:raw",
      title: "Declared raw audit",
      description: "Audit repo.",
      repo: "acme/widgets",
      recurrence: { kind: "daily", timeOfDay: "03:00" },
      enabled: true,
      capabilities: ["api:tasks:read"],
      declaredIn: {
        repo: "acme/widgets",
        path: ".grain/schedules/raw.yml",
      },
    };
    const [openScheduleId, setOpenScheduleId] = [
      "repo:schedule:acme:widgets:raw",
      vi.fn(),
    ];

    const { unmount } = render(
      <ControlledList
        query={{ kinds: ["template"] }}
        templates={[declaredTemplate]}
      />,
    );
    await user.click(screen.getByText("Declared dependency bump"));
    expect(
      screen.getByText(/cannot be edited or deleted here/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Add grant…" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Remove api:tasks:read/i }),
    ).not.toBeInTheDocument();
    unmount();

    render(
      <ProcessesList
        schedules={[declaredSchedule]}
        openScheduleId={openScheduleId}
        onOpenSchedule={setOpenScheduleId}
        onRefreshSchedules={noop}
        showError={noop}
      />,
    );
    expect(
      screen.getByText(/cannot be edited or deleted here/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Add grant…" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Remove api:tasks:read/i }),
    ).not.toBeInTheDocument();
  });

  it("is not offered to a new schedule", async () => {
    routeApi();
    const user = userEvent.setup();
    render(
      <ControlledList
        query={{ kinds: ["schedule"] }}
        templates={[gke]}
        schedules={[]}
      />,
    );
    await user.click(screen.getByRole("button", { name: "+ New schedule" }));
    await user.click(screen.getByRole("combobox", { name: "Template" }));
    expect(
      screen.getByRole("option", { name: /None -- fill in/ }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("option", { name: /GKE sandbox cluster/ }),
    ).not.toBeInTheDocument();
  });
});
