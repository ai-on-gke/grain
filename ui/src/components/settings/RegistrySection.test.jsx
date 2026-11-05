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
import RegistrySection from "./RegistrySection.jsx";

vi.mock("../../api.js", () => ({
  default: vi.fn(),
}));

describe("RegistrySection", () => {
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
              conflict_with: "other-grain",
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

    render(<RegistrySection s={s} />);

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
});
