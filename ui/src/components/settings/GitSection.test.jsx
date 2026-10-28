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
import { beforeEach, describe, expect, it, vi } from "vitest";
import GitSection from "./GitSection.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

describe("GitSection", () => {
  beforeEach(() => {
    api.mockReset();
  });

  it("draws the deployment's git connections from its record", async () => {
    api.mockImplementation((path) => {
      if (path === "/api/deployment")
        return Promise.resolve({ id: "default", gitConnections: [] });
      return Promise.resolve([]);
    });
    render(<GitSection showError={vi.fn()} />);
    expect(await screen.findByTestId("git-connections-list")).toBeVisible();
    expect(api).toHaveBeenCalledWith("/api/deployment");
  });

  it("says so when the deployment's record cannot be read", async () => {
    api.mockImplementation((path) =>
      path === "/api/deployment"
        ? Promise.reject(new Error("boom"))
        : Promise.resolve([]),
    );
    render(<GitSection showError={vi.fn()} />);
    expect(
      await screen.findByText(/Could not load the deployment's connections/),
    ).toHaveTextContent("boom");
    expect(screen.queryByTestId("git-connections-list")).toBeNull();
  });
});
