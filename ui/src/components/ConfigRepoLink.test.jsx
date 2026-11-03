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
import { afterEach, describe, expect, it, vi } from "vitest";
import api from "../api.js";
import { ConfigManagedNotice } from "./ConfigRepoLink.jsx";
import ReadOnlyGrants from "./ReadOnlyGrants.jsx";

vi.mock("../api.js", () => ({ default: vi.fn() }));

describe("ConfigManagedNotice", () => {
  afterEach(() => api.mockReset());

  it("drops paths absent from /api/state-repo/files while keeping at least the first and de-duplicating", async () => {
    api.mockResolvedValue({
      entries: [
        { path: "README.md", size: 100 },
        { path: "settings/schedules.yml", size: 50 },
      ],
    });
    render(
      <ConfigManagedNotice
        paths={[
          "tables/grain_config.json",
          "tables/custom_field.json",
          "settings/capabilities.yml",
          "tables/grain_config.json",
        ]}
      />,
    );
    await waitFor(() =>
      expect(
        screen
          .getAllByTestId("config-repo-link")
          .map((a) => a.getAttribute("data-config-path")),
      ).toEqual(["tables/grain_config.json"]),
    );
  });

  it("keeps optional paths when the listing is truncated or empty", async () => {
    api.mockResolvedValue({
      truncated: true,
      entries: [{ path: "tables/grain_config.json", size: 200 }],
    });
    render(
      <ConfigManagedNotice
        paths={["tables/grain_config.json", "settings/capabilities.yml"]}
      />,
    );
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/state-repo/files"),
    );
    expect(
      screen
        .getAllByTestId("config-repo-link")
        .map((a) => a.getAttribute("data-config-path")),
    ).toEqual(["tables/grain_config.json", "settings/capabilities.yml"]);
  });
});

describe("ReadOnlyGrants", () => {
  afterEach(() => api.mockReset());

  it("hides the settings/capabilities.yml link when the exported tree has no such file", async () => {
    api.mockResolvedValue({
      entries: [{ path: "tables/repo_config.json", size: 200 }],
    });
    render(<ReadOnlyGrants grants={[]} />);
    await waitFor(() =>
      expect(screen.queryByTestId("config-repo-link")).toBeNull(),
    );
  });
});
