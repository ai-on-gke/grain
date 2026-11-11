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

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import DetailOverlay from "../DetailOverlay.jsx";
import api from "../../api.js";
import { AccessConfigProvider } from "../../AccessContext.jsx";
import { resetDetailMocks, baseTask, config } from "./testFixtures.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../../attachments.js", () => ({ default: vi.fn() }));

describe("DetailOverlay capabilities", () => {
  afterEach(resetDetailMocks);

  it("edits https://grain.dev/capabilities/api# grants in GrantsPicker, with no legacy checklist", async () => {
    const act = vi.fn((mutate) => mutate());
    const user = userEvent.setup();
    const refGrant =
      'https://grain.dev/capabilities/api#ci:read={"repo":"acme/widgets"}';
    const task = {
      ...baseTask,
      capabilities: ["web-search", refGrant],
    };
    render(
      <DetailOverlay
        task={task}
        tasks={[]}
        config={{
          ...config,
          capabilities: [
            ...(config.capabilities || []),
            { id: "api:ci:read", summary: "Read CI", description: "Read CI" },
          ],
        }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    expect(screen.queryByLabelText("Legacy capabilities")).toBeNull();
    const grants = screen.getByTestId("task-12-grants");
    const apiLine = within(grants).getByLabelText(
      "api:ci:read · repo: acme/widgets",
    );
    await user.click(within(apiLine).getByRole("button", { name: /remove/i }));
    expect(api).toHaveBeenCalledWith("/api/tasks/12/capabilities", {
      method: "POST",
      body: JSON.stringify({ grant: refGrant, attach: false }),
    });

    const bareLine = within(grants).getByLabelText("web-search");
    await user.click(within(bareLine).getByRole("button", { name: /remove/i }));
    expect(api).toHaveBeenCalledWith("/api/tasks/12/capabilities", {
      method: "POST",
      body: JSON.stringify({ grant: "web-search", attach: false }),
    });
  });

  // A viewer's task pane (2026-11-11 responsive-navigation walkthrough):
  // grant Remove buttons, "Add grant…", Add goal and the owner's Change
  // were all live, each a write the server refuses a grain/viewer
  // (capabilities:admin, tasks:write). They are read only, or disabled
  // with the reason, as the pane's actions already were.
  it("offers a viewer no grant, goal or owner edits", async () => {
    const viewer = {
      ...config,
      access: {
        role: "grain/viewer",
        roles: ["grain/viewer"],
        full: false,
        scopes: ["tasks:read", "repos:read", "settings:read"],
      },
    };
    render(
      <AccessConfigProvider config={viewer}>
        <DetailOverlay
          task={{ ...baseTask, owner: "alice", capabilities: ["web-search"] }}
          tasks={[]}
          config={viewer}
          onClose={() => {}}
          onOpenTask={() => {}}
          act={vi.fn()}
        />
      </AccessConfigProvider>,
    );

    const grants = screen.getByTestId("task-12-grants");
    const bareLine = within(grants).getByLabelText("web-search");
    expect(
      within(bareLine).queryByRole("button", { name: /remove/i }),
    ).toBeNull();
    expect(screen.queryByRole("button", { name: /Add grant/ })).toBeNull();
    for (const name of ["Add goal", "Change owner"]) {
      const button = screen.getByRole("button", { name });
      expect(button).toBeDisabled();
      expect(button.closest("[data-access-denied]")).toHaveAttribute(
        "data-access-denied",
        "tasks:write",
      );
    }
    expect(api).not.toHaveBeenCalledWith(
      "/api/tasks/12/capabilities",
      expect.anything(),
    );
  });

  it("still offers the operator the same edits", () => {
    render(
      <DetailOverlay
        task={{ ...baseTask, owner: "alice", capabilities: ["web-search"] }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    const bareLine = within(
      screen.getByTestId("task-12-grants"),
    ).getByLabelText("web-search");
    expect(
      within(bareLine).getByRole("button", { name: /remove/i }),
    ).toBeEnabled();
    expect(screen.getByRole("button", { name: /Add grant/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Add goal" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Change owner" })).toBeEnabled();
  });
});
