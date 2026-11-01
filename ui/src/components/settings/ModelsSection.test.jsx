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

import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import ModelsSection from "./ModelsSection.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

// Settings → Models reads and writes the deployment's own routes:
// /api/deployment for the default model and model settings, and
// /api/deployment/agent-backends/{id} for a connection.
describe("ModelsSection", () => {
  const deployment = {
    id: "default",
    defaultModel: "gemini-3.1-pro",
    modelSettings: [],
    gitConnections: [],
  };
  const backends = [
    {
      id: "conn-1",
      name: "Primary Antigravity",
      framework: "antigravity",
      models: ["gemini-3.1-pro", "gemini-2.5-flash"],
    },
  ];

  beforeEach(() => {
    api.mockReset();
    api.mockImplementation((path, options) => {
      if (path === "/api/settings" && !options)
        return Promise.resolve({
          agentBackends: backends,
          capabilities: [],
          defaultCapabilities: [],
        });
      if (path === "/api/deployment" && !options)
        return Promise.resolve(deployment);
      if (path === "/api/deployment")
        return Promise.resolve({ ...deployment, ...JSON.parse(options.body) });
      return Promise.resolve({});
    });
  });

  it("saves the default model on the deployment's record, never the deployment settings", async () => {
    const user = userEvent.setup();
    render(<ModelsSection config={{}} showError={vi.fn()} />);

    expect(
      await screen.findByText("The deployment's provider connections.", {
        exact: false,
      }),
    ).toBeInTheDocument();
    const picker = await screen.findByLabelText("Default model");
    await waitFor(() => expect(picker).toHaveValue("gemini-3.1-pro"));
    await user.click(picker);
    await user.click(screen.getByRole("option", { name: "gemini-2.5-flash" }));

    expect(api).toHaveBeenCalledWith("/api/deployment", {
      method: "PUT",
      body: JSON.stringify({ defaultModel: "gemini-2.5-flash" }),
    });
    expect(api).not.toHaveBeenCalledWith(
      "/api/settings",
      expect.objectContaining({ method: "PUT" }),
    );
  });

  // A config pull request disabling every connection is refused as
  // locking dispatch; the same change made here is written, so the page
  // says what it did (the 2026-10-31 config-repo write-path test run).
  it("warns when every connection is disabled", async () => {
    backends[0].disabled = true;
    try {
      render(<ModelsSection config={{}} showError={vi.fn()} />);
      expect(
        await screen.findByTestId("all-connections-disabled"),
      ).toHaveTextContent("Every provider connection is disabled");
    } finally {
      delete backends[0].disabled;
    }
  });

  it("does not warn while a connection is enabled", async () => {
    render(<ModelsSection config={{}} showError={vi.fn()} />);
    await screen.findByText("Primary Antigravity");
    expect(
      screen.queryByTestId("all-connections-disabled"),
    ).not.toBeInTheDocument();
  });

  it("removes a connection through the deployment's agent-backends route", async () => {
    const user = userEvent.setup();
    vi.spyOn(window, "confirm").mockReturnValue(true);
    render(<ModelsSection config={{}} showError={vi.fn()} />);

    await screen.findByText("Primary Antigravity");
    await user.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        "/api/deployment/agent-backends/conn-1",
        { method: "DELETE" },
      ),
    );
  });

  // Remove deletes the credential stored with the connection, so it asks
  // first; the 2026-10-23 settings walkthrough found it deleting on one
  // click.
  it("asks before removing a connection and keeps it when declined", async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    render(<ModelsSection config={{}} showError={vi.fn()} />);

    await screen.findByText("Primary Antigravity");
    await user.click(screen.getByRole("button", { name: "Remove" }));
    expect(confirm).toHaveBeenCalledWith(
      expect.stringContaining("Remove the connection Primary Antigravity?"),
    );
    expect(api).not.toHaveBeenCalledWith(
      "/api/deployment/agent-backends/conn-1",
      { method: "DELETE" },
    );
    expect(screen.getByText("Primary Antigravity")).toBeInTheDocument();
    confirm.mockRestore();
  });

  // A whole connection's usage-limit pause could only be lifted from the
  // alerts panel; Settings → Models offered Resume for a paused model of
  // it but not for the connection itself
  // (docs/test-runs/2026-10-29-user-system-settings.md).
  it("resumes a paused connection from its row", async () => {
    const user = userEvent.setup();
    const onSaved = vi.fn();
    const pause = {
      connection: "conn-1",
      connectionName: "Primary Antigravity",
      team: "default",
      framework: "antigravity",
      until: "2099-01-01T00:00:00Z",
      reason: "usage limit reached",
      canLift: true,
    };
    render(
      <ModelsSection
        config={{ agentPause: { paused: true, pauses: [pause] } }}
        showError={vi.fn()}
        onSaved={onSaved}
      />,
    );
    await screen.findByText("Paused");
    await user.click(screen.getByRole("button", { name: "Resume" }));
    expect(api).toHaveBeenCalledWith("/api/pause?connection=conn-1", {
      method: "DELETE",
    });
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
  });

  it("offers no Resume for a pause this reader cannot lift", async () => {
    render(
      <ModelsSection
        config={{
          agentPause: {
            paused: true,
            pauses: [
              {
                connection: "conn-1",
                team: "default",
                until: "2099-01-01T00:00:00Z",
                canLift: false,
              },
            ],
          },
        }}
        showError={vi.fn()}
      />,
    );
    await screen.findByText("Paused");
    expect(screen.queryByRole("button", { name: "Resume" })).toBeNull();
  });

  it("lists every connection, whichever team a stored one names", async () => {
    api.mockImplementation((path, options) => {
      if (path === "/api/settings" && !options)
        return Promise.resolve({
          agentBackends: [
            ...backends,
            { id: "conn-2", name: "Legacy", framework: "claude", team: "apps" },
          ],
        });
      if (path === "/api/deployment" && !options)
        return Promise.resolve(deployment);
      return Promise.resolve({});
    });
    render(<ModelsSection config={{}} showError={vi.fn()} />);
    expect(await screen.findByText("Primary Antigravity")).toBeVisible();
    expect(screen.getByText("Legacy")).toBeVisible();
    expect(screen.getByText(/Model servers/i)).toBeVisible();
    expect(screen.getByText("grain model-server")).toBeVisible();
  });
});
