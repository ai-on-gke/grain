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

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import api from "../../api.js";
import HubSection from "./HubSection.jsx";
import { TimeZoneProvider } from "../../TimeZoneContext.jsx";

vi.mock("../../api.js", () => ({
  default: vi.fn(),
}));

describe("HubSection", () => {
  it("renders connected state, heartbeat metadata, advertised scopes, and triggers heartbeat", async () => {
    api.mockImplementation((path, opts) => {
      if (path === "/api/registry") {
        return Promise.resolve({
          enabled: true,
          state: "connected",
          effectiveHostKind: "workstation",
          lastHeartbeatAt: "2026-11-05T07:50:00Z",
          heartbeatIntervalSeconds: 15,
          health: "ok",
          activeTasks: 2,
          awaitingReplyTasks: 1,
          advertisedScopes: [
            {
              repo: "acme/widgets",
              pathPrefix: "",
              role: "primary",
              origin: "repo_config",
            },
          ],
          scopeConflicts: [
            {
              repo: "acme/shared",
              path_prefix: "docs",
              conflicting_entry: "other-grain",
            },
          ],
        });
      }
      if (path === "/api/registry/heartbeat" && opts?.method === "POST") {
        return Promise.resolve({
          enabled: true,
          state: "connected",
          lastHeartbeatAt: "2026-11-05T07:51:00Z",
          heartbeatIntervalSeconds: 15,
          health: "ok",
          activeTasks: 3,
          awaitingReplyTasks: 0,
          advertisedScopes: [],
        });
      }
      return Promise.resolve({});
    });

    const saveChoice = vi.fn();
    const s = {
      settings: {
        registryTokenSet: true,
        registry: {
          url: "https://registry.grain.example.com",
          entry: "acme-widgets",
          authMode: "token",
          displayName: "Acme Widgets Grain",
          purpose: "Widgets tasks",
          listed: true,
        },
      },
      saveChoice,
      onEnterKey: () => {},
      reloadSettings: vi.fn(),
      showError: vi.fn(),
      onSaved: vi.fn(),
    };

    render(<HubSection s={s} />);

    await waitFor(() =>
      expect(screen.getByTestId("registry-state-chip")).toHaveTextContent(
        "Connected",
      ),
    );
    expect(screen.getByTestId("registry-advertised-scopes")).toHaveTextContent(
      "acme/widgets",
    );
    expect(screen.getByTestId("registry-scope-conflicts")).toHaveTextContent(
      "other-grain",
    );

    fireEvent.click(screen.getByTestId("registry-heartbeat-now"));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/registry/heartbeat", {
        method: "POST",
      }),
    );
  });

  // A locked Registry drew its config-managed cards in a bare disabled
  // fieldset, which a MUI Select does not obey: its selects stayed in the
  // tab order, unmarked, and still opened and saved (2026-11-07 settings
  // walkthrough). ReadOnlyFieldset takes them out of reach.
  it("keeps locked selects out of reach of the keyboard", async () => {
    api.mockResolvedValue({ enabled: false, state: "disabled" });
    const saveChoice = vi.fn();
    const s = {
      configLocked: true,
      settings: {
        registry: {
          url: "https://registry.example.com",
          entry: "acme",
          authMode: "token",
        },
      },
      saveChoice,
      onEnterKey: () => {},
      reloadSettings: vi.fn(),
      showError: vi.fn(),
      onSaved: vi.fn(),
    };
    const { container } = render(<HubSection s={s} />);
    const selects = await waitFor(() => {
      const found = container.querySelectorAll(".MuiSelect-select");
      expect(found.length).toBeGreaterThan(0);
      return found;
    });
    await waitFor(() => {
      for (const el of selects) {
        expect(el).toHaveAttribute("aria-disabled", "true");
        expect(el).toHaveAttribute("tabindex", "-1");
      }
    });
    fireEvent.keyDown(selects[0], { key: "ArrowDown" });
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(saveChoice).not.toHaveBeenCalled();
  });

  // "Last heartbeat" printed a raw ISO UTC timestamp whatever time zone
  // this browser chose (2026-11-08 settings walkthrough).
  it("prints the last heartbeat in this browser's chosen time zone", async () => {
    const hb = "2026-11-05T07:50:00Z";
    api.mockResolvedValue({
      enabled: true,
      state: "connected",
      lastHeartbeatAt: hb,
      heartbeatIntervalSeconds: 15,
      health: "ok",
      activeTasks: 0,
      awaitingReplyTasks: 0,
    });
    const s = {
      settings: {
        registry: {
          url: "https://registry.grain.example.com",
          entry: "acme-widgets",
        },
      },
      saveChoice: vi.fn(),
      onEnterKey: () => {},
      reloadSettings: vi.fn(),
      showError: vi.fn(),
      onSaved: vi.fn(),
    };
    render(
      <TimeZoneProvider zone="Asia/Tokyo">
        <HubSection s={s} />
      </TimeZoneProvider>,
    );
    const inTokyo = new Date(hb).toLocaleString(undefined, {
      timeZone: "Asia/Tokyo",
    });
    await screen.findByText(
      new RegExp(
        `Last heartbeat ${inTokyo.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`,
      ),
    );
  });

  it("saves edits under the hub settings key", async () => {
    api.mockResolvedValue({ enabled: true, state: "connected" });
    const saveChoice = vi.fn();
    const s = {
      settings: {
        hubTokenSet: true,
        hub: {
          url: "https://hub.example.com",
          entry: "acme-widgets",
          authMode: "token",
          listed: true,
          scopes: [],
        },
      },
      saveChoice,
      onEnterKey: () => {},
      reloadSettings: vi.fn(),
      showError: vi.fn(),
      onSaved: vi.fn(),
    };

    render(<HubSection s={s} />);

    const urlInput = screen.getByLabelText("Hub URL");
    fireEvent.change(urlInput, {
      target: { value: "https://hub-next.example.com" },
    });
    fireEvent.blur(urlInput, {
      target: { value: "https://hub-next.example.com" },
    });

    expect(saveChoice).toHaveBeenCalledTimes(1);
    expect(saveChoice).toHaveBeenCalledWith({
      hub: {
        url: "https://hub-next.example.com",
        entry: "acme-widgets",
        authMode: "token",
        listed: true,
        scopes: [],
      },
    });
    expect(saveChoice.mock.calls[0][0]).not.toHaveProperty("registry");

    fireEvent.change(screen.getByLabelText("Repository (owner/name)"), {
      target: { value: "acme/monorepo" },
    });
    fireEvent.change(screen.getByLabelText("Path prefix (optional)"), {
      target: { value: "/services/billing/" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add rule" }));

    expect(saveChoice).toHaveBeenCalledTimes(2);
    expect(saveChoice).toHaveBeenLastCalledWith({
      hub: {
        url: "https://hub.example.com",
        entry: "acme-widgets",
        authMode: "token",
        listed: true,
        scopes: [
          {
            repo: "acme/monorepo",
            pathPrefix: "services/billing",
            role: "primary",
          },
        ],
      },
    });
  });
});
