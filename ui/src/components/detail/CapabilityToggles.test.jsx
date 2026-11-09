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
      "api:ci:read on acme/widgets",
    );
    await user.click(within(apiLine).getByRole("button", { name: /remove/i }));
    expect(api).toHaveBeenCalledWith("/api/tasks/12/capabilities", {
      method: "POST",
      body: JSON.stringify({ id: refGrant, attach: false }),
    });

    const bareLine = within(grants).getByLabelText("web-search");
    await user.click(within(bareLine).getByRole("button", { name: /remove/i }));
    expect(api).toHaveBeenCalledWith("/api/tasks/12/capabilities", {
      method: "POST",
      body: JSON.stringify({ grant: "web-search", attach: false }),
    });
  });
});
