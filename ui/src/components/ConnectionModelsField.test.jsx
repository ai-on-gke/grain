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

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import api from "../api.js";
import ConnectionModelsField from "./ConnectionModelsField.jsx";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const pinned = {
  id: "conn-1",
  framework: "antigravity",
  models: ["gemini-2.5-pro", "gemini-2.5-flash"],
};

describe("ConnectionModelsField", () => {
  beforeEach(() => api.mockReset());

  // "Advertise all models" with nothing listed by the provider advertised
  // none: it threw the pinned list away and left the connection serving
  // no model (docs/test-runs/2026-10-29-user-system-settings.md).
  it("keeps a pinned list when the provider has listed nothing", async () => {
    api.mockResolvedValue({ available: false });
    const onChange = vi.fn();
    render(<ConnectionModelsField backend={pinned} onChange={onChange} />);

    await screen.findByText(/cannot ask this provider for its models/);
    const all = screen.getByRole("button", { name: "Advertise all models" });
    expect(all).toBeDisabled();
    expect(screen.getByTestId("advertise-all-unavailable")).toBeVisible();
    expect(onChange).not.toHaveBeenCalled();
  });

  it("advertises everything the provider lists once it has listed some", async () => {
    const user = userEvent.setup();
    api.mockResolvedValue({
      available: true,
      models: [{ id: "gemini-2.5-pro" }, { id: "gemini-3.1-pro" }],
    });
    const onChange = vi.fn();
    render(<ConnectionModelsField backend={pinned} onChange={onChange} />);

    const all = screen.getByRole("button", { name: "Advertise all models" });
    await screen.findByLabelText("Discovered models");
    expect(all).toBeEnabled();
    expect(screen.queryByTestId("advertise-all-unavailable")).toBeNull();
    await user.click(all);
    expect(onChange).toHaveBeenCalledWith({ models: [], model: undefined });
  });
});
