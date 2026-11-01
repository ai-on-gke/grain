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
import { afterEach, describe, expect, it, vi } from "vitest";
import api from "../../api.js";
import ExecutorsSection from "./ExecutorsSection.jsx";
import { SettingsReadOnlyContext } from "./SettingsCard.jsx";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

describe("ExecutorsSection", () => {
  afterEach(() => api.mockReset());

  // settings-mutability.spec.md D-9: a join token is a runtime
  // credential the config repo never holds, and POST
  // /api/executors/tokens takes it in external mode, so the card is not
  // drawn read-only there.
  it("issues a join token while the page is read-only for an external config repo", async () => {
    const user = userEvent.setup();
    api.mockImplementation((path, opts) => {
      if (path === "/api/executors/tokens" && opts?.method === "POST")
        return Promise.resolve({ id: "tok-1", name: "gpu-1", token: "s3cr3t" });
      return Promise.resolve([]);
    });
    render(
      <SettingsReadOnlyContext.Provider value={true}>
        <ExecutorsSection showError={vi.fn()} />
      </SettingsReadOnlyContext.Provider>,
    );

    const card = screen.getByRole("region", { name: "Executor join tokens" });
    expect(card.querySelector("[data-settings-readonly]")).toBeNull();
    const issue = await screen.findByRole("button", { name: "Issue Token" });
    expect(issue).toBeEnabled();
    await user.click(issue);
    await user.type(
      screen.getByLabelText(/Token Name \/ Description/),
      "gpu-1",
    );
    await user.click(screen.getByRole("button", { name: "Generate Token" }));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/executors/tokens", {
        method: "POST",
        body: JSON.stringify({ name: "gpu-1" }),
      }),
    );
  });
});
