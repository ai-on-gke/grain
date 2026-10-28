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

// A template with one hole of each kind: a text box and a picker
// (model.TemplateField, grain/task-58).
const deploy = {
  id: "template-1",
  name: "Deploy",
  title: "Deploy {{service}} to {{environment}}",
  description: "",
  autoMerge: false,
  reads: [],
  capabilities: [],
  fields: [
    { name: "service", label: "Service", required: true },
    {
      name: "environment",
      options: ["staging", "production"],
      default: "staging",
    },
  ],
  createdAt: "2026-01-01T00:00:00Z",
};

const noop = () => {};

// The Processes page narrowed to templates, which is what /templates
// opens now (docs/design/settings-panel.md §5.4): its New button is the
// template form's, and each row has its Use button.
function ControlledTemplatesList({ onRefresh, ...props }) {
  const [openTemplateId, setOpenTemplateId] = useState(null);
  return (
    <ProcessesList
      query={{ kinds: ["template"] }}
      openTemplateId={openTemplateId}
      onOpenTemplate={setOpenTemplateId}
      onRefreshTemplates={onRefresh}
      {...props}
    />
  );
}

// The API fake for this file: a composition for whatever the preview
// asks, the filed task for a POST, and nothing worth saying for
// anything else. Routed by path rather than queued, since the preview
// fires on its own and a queue would make its timing part of every
// assertion.
function routeApi(onCreate) {
  api.mockImplementation((path, opts = {}) => {
    if (path.includes("/composition")) {
      const query = new URLSearchParams(path.split("?")[1] || "");
      const title = `Deploy ${query.get("field.service") || ""} to ${
        query.get("field.environment") || ""
      }`;
      return Promise.resolve({
        kind: "task",
        tasks: [
          {
            title,
            fields: [{ name: "title", values: [title], source: "template" }],
          },
        ],
      });
    }
    if (path === "/api/tasks" && opts.method === "POST") {
      if (onCreate) onCreate(JSON.parse(opts.body));
      return Promise.resolve({ id: "7" });
    }
    return Promise.resolve({});
  });
}

describe("a template's input fields", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("declares an input from the template's own form", async () => {
    routeApi();
    const user = userEvent.setup();
    render(
      <ControlledTemplatesList
        templates={[]}
        onRefresh={noop}
        showError={noop}
      />,
    );

    await user.click(screen.getByRole("button", { name: "+ New template" }));
    await user.type(screen.getByLabelText(/Name/), "Deploy");
    await user.type(screen.getByLabelText(/Task title/), "Deploy {{service}}");
    await user.click(screen.getByRole("button", { name: "+ Add input" }));
    await user.type(screen.getByLabelText("Input 1 name"), "service");
    await user.type(
      screen.getByLabelText("Input 1 options"),
      "staging{enter}production",
    );
    await user.click(screen.getByRole("button", { name: "Add template" }));

    const body = JSON.parse(api.mock.calls.at(-1)[1].body);
    expect(body.fields).toEqual([
      {
        name: "service",
        label: "",
        // One option per line, since a comma is something an option may
        // legitimately contain.
        options: ["staging", "production"],
        default: "",
      },
    ]);
  });

  it("opens the template's inputs from a row's Use button and files the task", async () => {
    const filed = vi.fn();
    routeApi(filed);
    const onOpenTask = vi.fn();
    const onRefreshTasks = vi.fn();
    const user = userEvent.setup();
    render(
      <ControlledTemplatesList
        templates={[deploy]}
        onRefresh={noop}
        onRefreshTasks={onRefreshTasks}
        onOpenTask={onOpenTask}
        showError={noop}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Use…" }));
    expect(
      screen.getByRole("heading", { name: "File a task from a template" }),
    ).toBeInTheDocument();

    // A text box for the field with no options, a picker for the one
    // with them -- and the picker already holding the declared default.
    await user.type(screen.getByLabelText(/Service/), "payments");
    expect(screen.getByText("staging")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(filed).toHaveBeenCalledWith({
      templateId: "template-1",
      fields: { service: "payments", environment: "staging" },
      repo: "",
      base: "",
      approved: false,
    });
    // Straight into what was filed, and the list behind it told to
    // catch up.
    await waitFor(() => expect(onOpenTask).toHaveBeenCalledWith("7"));
    expect(onRefreshTasks).toHaveBeenCalled();
  });

  // The preview is the server's answer, not the browser's: nothing here
  // substitutes a {{name}} itself, so what it shows is a composition
  // asked for with one `field.<name>` parameter per value.
  it("previews what filing it would produce", async () => {
    routeApi();
    const user = userEvent.setup();
    render(
      <ControlledTemplatesList
        templates={[deploy]}
        onRefresh={noop}
        showError={noop}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Use…" }));
    await user.type(screen.getByLabelText(/Service/), "payments");
    // The preview follows a value somebody has settled on -- a box
    // blurred -- rather than every keystroke.
    await user.tab();

    await waitFor(() =>
      expect(
        screen.getByText("Deploy payments to staging"),
      ).toBeInTheDocument(),
    );
    const asked = api.mock.calls
      .map(([path]) => path)
      .filter((path) => path.includes("/composition"));
    expect(asked.at(-1)).toContain("field.service=payments");
    expect(asked.at(-1)).toContain("/api/templates/template-1/composition");
  });

  // A bound template decides where its firings go, so the form says
  // where instead of offering a box whose answer would be ignored.
  it("says where a bound template files, instead of asking", async () => {
    routeApi();
    const user = userEvent.setup();
    render(
      <ControlledTemplatesList
        templates={[{ ...deploy, repo: "acme/widgets", base: "release" }]}
        onRefresh={noop}
        showError={noop}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Use…" }));

    expect(
      screen.getByText(/Files against acme\/widgets @ release/),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText(/Base branch/)).not.toBeInTheDocument();
  });
});
