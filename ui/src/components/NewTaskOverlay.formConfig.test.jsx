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

// The form as settings/new-task-form.yml configures it
// (docs/design/config-repo-settings.md §6): placements, labels, defaults
// that follow the repo, and hidden fields filed with their defaults.

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import NewTaskOverlay from "./NewTaskOverlay.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));

const createCall = () =>
  api.mock.calls.find(([, opts]) => opts && opts.method === "POST");

const renderForm = (newTaskForm, props = {}) =>
  render(
    <NewTaskOverlay
      config={{ newTaskForm, targetRepos: ["a/one", "b/two"] }}
      onClose={() => {}}
      onCreated={() => Promise.resolve()}
      showError={() => {}}
      {...props}
    />,
  );

describe("NewTaskOverlay with settings/new-task-form.yml", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => api.mockClear());

  it("leaves hidden fields out and files their defaults", async () => {
    const user = userEvent.setup();
    renderForm({
      default: {
        fields: {
          autoMerge: { visibility: "hidden", default: true },
          approved: { visibility: "hidden", default: false },
          networkAccess: { visibility: "hidden", default: "none" },
        },
      },
    });
    expect(screen.queryByLabelText(/Auto-merge/)).toBeNull();
    expect(screen.queryByLabelText(/Queue immediately/)).toBeNull();
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    expect(screen.queryByLabelText("Network access")).toBeNull();
    await user.type(screen.getByLabelText(/Title/), "Hidden");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Create task" }));
    const payload = JSON.parse(createCall()[1].body);
    expect(payload).toMatchObject({
      autoMerge: true,
      approved: false,
      networkAccess: "none",
    });
  });

  it("moves fields between the form and Advanced options, and drops an empty Advanced options", async () => {
    const user = userEvent.setup();
    const { unmount } = renderForm({
      default: {
        fields: {
          approved: { visibility: "more", label: "File it approved" },
          networkAccess: { visibility: "shown", order: 15 },
        },
      },
    });
    // Network access, shown at order 15, sits between Title and Description.
    const title = screen.getByLabelText(/Title/);
    const network = screen.getByLabelText("Network access");
    const description = screen.getByLabelText("Description");
    expect(
      title.compareDocumentPosition(network) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(
      network.compareDocumentPosition(description) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    // Advanced options holds it (collapsed, its content stays mounted).
    expect(
      screen.getByLabelText("File it approved").closest(".MuiAccordion-root"),
    ).not.toBeNull();
    expect(network.closest(".MuiAccordion-root")).toBeNull();
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    unmount();

    const hideAll = Object.fromEntries(
      [
        "agentBackendId",
        "networkAccess",
        "nothingNeededAction",
        "reviewTemplateId",
        "promptExtension",
        "sandboxShape",
      ].map((id) => [id, { visibility: "hidden" }]),
    );
    renderForm({ default: { fields: hideAll } });
    expect(
      screen.queryByRole("button", { name: "Advanced options" }),
    ).toBeNull();
  });

  it("re-applies a repo's defaults to untouched fields when the repo changes", async () => {
    const user = userEvent.setup();
    renderForm({
      repos: {
        "b/two": {
          fields: { autoMerge: { default: true }, noPort: { default: true } },
        },
      },
    });
    const autoMerge = screen.getByLabelText(/Auto-merge/);
    const noPort = screen.getByLabelText(/Don't port/);
    expect(autoMerge).not.toBeChecked();
    // Ticked by hand, so the repo's default leaves it alone.
    await user.click(noPort);
    await user.click(noPort);
    expect(noPort).not.toBeChecked();
    await user.selectOptions(screen.getByLabelText(/^Repos/), "b/two");
    expect(autoMerge).toBeChecked();
    expect(noPort).not.toBeChecked();
  });

  it("offers only the configured network choices and sandbox presets", async () => {
    const user = userEvent.setup();
    renderForm({
      default: {
        fields: {
          networkAccess: { offered: ["", "none"] },
          sandboxShape: {
            default_preset: "large",
            allow_custom_numbers: false,
            presets: [
              {
                id: "large",
                label: "Large",
                cpus: 8,
                memory_mb: 16384,
                disk_gb: 60,
              },
            ],
          },
        },
      },
    });
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    expect(screen.queryByLabelText(/vCPUs/)).toBeNull();
    await user.click(screen.getByLabelText("Network access"));
    expect(screen.queryByRole("option", { name: /Egress/ })).toBeNull();
    expect(screen.getByRole("option", { name: /None/ })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await user.type(screen.getByLabelText(/Title/), "Shape");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Create task" }));
    const payload = JSON.parse(createCall()[1].body);
    expect(payload).toMatchObject({
      sandboxCpus: 8,
      sandboxMemoryMb: 16384,
      sandboxDiskGb: 60,
    });
  });
});
