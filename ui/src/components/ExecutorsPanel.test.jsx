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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ExecutorsPanel from "./ExecutorsPanel.jsx";
import api from "../api.js";
import { TimeZoneProvider } from "../TimeZoneContext.jsx";

vi.mock("../api.js", () => ({ default: vi.fn() }));

describe("ExecutorsPanel", () => {
  const activeToken = {
    id: "gxt-1",
    name: "gpu-node-1",
    team: "infra",
    prefix: "gxt_1234",
    created_at: "2026-09-24T00:00:00Z",
    last_connected_at: "2026-09-24T01:00:00Z",
    revoked_at: null,
  };

  const revokedToken = {
    id: "gxt-2",
    name: "old-worker",
    team: "infra",
    prefix: "gxt_5678",
    created_at: "2026-09-20T00:00:00Z",
    last_connected_at: null,
    revoked_at: "2026-09-22T00:00:00Z",
  };

  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    api.mockReset();
    vi.restoreAllMocks();
  });

  it("fetches and renders executor tokens list", async () => {
    api.mockResolvedValueOnce([activeToken, revokedToken]);
    const showError = vi.fn();

    render(<ExecutorsPanel showError={showError} />);

    expect(api).toHaveBeenCalledWith("/api/executors/tokens");
    expect(await screen.findByText("gpu-node-1")).toBeInTheDocument();
    expect(screen.getByText("old-worker")).toBeInTheDocument();
    expect(screen.getByText("Active")).toBeInTheDocument();
    expect(screen.getByText("Revoked")).toBeInTheDocument();
    expect(screen.getByText(/gxt_1234/)).toBeInTheDocument();
    expect(
      screen.getByText(
        `Created: ${new Date("2026-09-24T00:00:00Z").toLocaleString()}`,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        `Last Connected: ${new Date("2026-09-24T01:00:00Z").toLocaleString()}`,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        `Revoked: ${new Date("2026-09-22T00:00:00Z").toLocaleString()}`,
      ),
    ).toBeInTheDocument();
  });

  it("prints token times in the deployment's zone, not the browser's", async () => {
    api.mockResolvedValueOnce([activeToken, revokedToken]);
    const inZone = (iso) =>
      new Date(iso).toLocaleString(undefined, { timeZone: "Asia/Kolkata" });

    render(
      <TimeZoneProvider zone="Asia/Kolkata">
        <ExecutorsPanel showError={vi.fn()} />
      </TimeZoneProvider>,
    );

    expect(await screen.findByText("gpu-node-1")).toBeInTheDocument();
    // 00:00 UTC is 05:30 in Kolkata, a different time of day in every
    // zone a test runner is likely to be in, UTC included.
    expect(
      screen.getByText(`Created: ${inZone("2026-09-24T00:00:00Z")}`),
    ).toBeInTheDocument();
    expect(
      screen.getByText(`Last Connected: ${inZone("2026-09-24T01:00:00Z")}`),
    ).toBeInTheDocument();
    expect(
      screen.getByText(`Revoked: ${inZone("2026-09-22T00:00:00Z")}`),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(
        `Created: ${new Date("2026-09-24T00:00:00Z").toLocaleString(undefined, { timeZone: "UTC" })}`,
      ),
    ).not.toBeInTheDocument();
  });

  it("handles camelCase token properties as fallback", async () => {
    const legacyToken = {
      id: "gxt-legacy",
      name: "legacy-worker",
      team: "infra",
      prefix: "gxt_9999",
      createdAt: "2026-09-21T00:00:00Z",
      lastConnectedAt: "2026-09-21T01:00:00Z",
      revokedAt: "2026-09-23T00:00:00Z",
    };
    api.mockResolvedValueOnce([legacyToken]);

    render(<ExecutorsPanel showError={vi.fn()} />);

    expect(await screen.findByText("legacy-worker")).toBeInTheDocument();
    expect(screen.getByText("Revoked")).toBeInTheDocument();
    expect(
      screen.getByText(
        `Created: ${new Date("2026-09-21T00:00:00Z").toLocaleString()}`,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        `Last Connected: ${new Date("2026-09-21T01:00:00Z").toLocaleString()}`,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        `Revoked: ${new Date("2026-09-23T00:00:00Z").toLocaleString()}`,
      ),
    ).toBeInTheDocument();
  });

  it("issues a new token and displays raw token once", async () => {
    const user = userEvent.setup();
    api.mockResolvedValueOnce([]); // initial list
    render(<ExecutorsPanel showError={vi.fn()} />);

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/executors/tokens"),
    );

    // Click "Issue Token": the dialog asks only for a name.
    const issueBtn = screen.getByRole("button", { name: /issue token/i });
    await user.click(issueBtn);

    expect(screen.getByText("Issue Executor Token")).toBeInTheDocument();

    // Fill form
    const nameInput = screen.getByLabelText(/token name/i);
    await user.type(nameInput, "worker-node-x");

    // Mock token creation response and subsequent list refresh
    api.mockResolvedValueOnce({
      token: "gxt_abcdef1234567890abcdef1234567890",
      record: {
        id: "gxt-3",
        name: "worker-node-x",
        team: "default",
        prefix: "gxt_abcd",
        created_at: "2026-09-24T02:00:00Z",
        revoked_at: null,
      },
    });
    api.mockResolvedValueOnce([
      {
        id: "gxt-3",
        name: "worker-node-x",
        team: "default",
        prefix: "gxt_abcd",
        created_at: "2026-09-24T02:00:00Z",
        revoked_at: null,
      },
    ]);

    expect(screen.queryByLabelText(/team scope/i)).toBeNull();
    const submitBtn = screen.getByRole("button", { name: /generate token/i });
    await user.click(submitBtn);
    expect(api).toHaveBeenCalledWith("/api/executors/tokens", {
      method: "POST",
      body: JSON.stringify({ name: "worker-node-x" }),
    });

    // Verify dialog with raw token appears
    expect(
      await screen.findByText("Executor Token Generated"),
    ).toBeInTheDocument();
    expect(
      screen.getByDisplayValue("gxt_abcdef1234567890abcdef1234567890"),
    ).toBeInTheDocument();
    expect(screen.getByText(/grain executor --token/)).toBeInTheDocument();

    // Close generated token dialog
    const doneBtn = screen.getByRole("button", { name: /done/i });
    await user.click(doneBtn);
  });

  it("revokes an active token after confirmation", async () => {
    const user = userEvent.setup();
    api.mockResolvedValueOnce([activeToken]);

    render(<ExecutorsPanel showError={vi.fn()} />);
    expect(await screen.findByText("gpu-node-1")).toBeInTheDocument();

    const revokeBtn = screen.getByRole("button", { name: /revoke/i });
    await user.click(revokeBtn);

    expect(screen.getByText("Revoke Executor Token")).toBeInTheDocument();

    api.mockResolvedValueOnce({
      ...activeToken,
      revoked_at: "2026-09-24T03:00:00Z",
    });
    api.mockResolvedValueOnce([
      {
        ...activeToken,
        revoked_at: "2026-09-24T03:00:00Z",
      },
    ]);

    const confirmRevokeBtn = screen.getByRole("button", {
      name: "Revoke Token",
    });
    await user.click(confirmRevokeBtn);

    await waitFor(() => {
      expect(api).toHaveBeenCalledWith("/api/executors/tokens/gxt-1/revoke", {
        method: "POST",
      });
    });
  });

  it("explains that deployment admins manage tokens when the caller is not one", async () => {
    const showError = vi.fn();
    render(
      <ExecutorsPanel
        showError={showError}
        selectedTeam="payments"
        canManage={false}
      />,
    );

    expect(
      screen.getByText(/Executor tokens are managed by deployment admins/),
    ).toBeInTheDocument();
    expect(api).not.toHaveBeenCalled();
    expect(showError).not.toHaveBeenCalled();
    expect(
      screen.queryByRole("button", { name: /issue token/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByText(/No executor tokens found/),
    ).not.toBeInTheDocument();
  });

  it("answers a 403 from the list with the same explanation, not a toast", async () => {
    const showError = vi.fn();
    api.mockRejectedValueOnce(
      Object.assign(
        new Error('user "bob" does not have administrator privileges'),
        { status: 403 },
      ),
    );
    render(<ExecutorsPanel showError={showError} selectedTeam="default" />);

    expect(
      await screen.findByText(
        /Executor tokens are managed by deployment admins/,
      ),
    ).toBeInTheDocument();
    expect(showError).not.toHaveBeenCalled();
    expect(
      screen.queryByRole("button", { name: /issue token/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByText(/No executor tokens found/),
    ).not.toBeInTheDocument();
  });

  it("still reports other list failures", async () => {
    const showError = vi.fn();
    const err = Object.assign(new Error("boom"), { status: 500 });
    api.mockRejectedValueOnce(err);
    render(<ExecutorsPanel showError={showError} />);

    await waitFor(() => expect(showError).toHaveBeenCalledWith(err));
    expect(
      screen.getByRole("button", { name: /issue token/i }),
    ).toBeInTheDocument();
  });
});
