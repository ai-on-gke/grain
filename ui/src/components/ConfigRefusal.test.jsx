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
import { describe, expect, it } from "vitest";
import ConfigRefusalDialog, {
  configRepoRefusal,
  settingsSectionPath,
} from "./ConfigRefusal.jsx";

const refusal = {
  code: "config_managed_externally",
  repo: "acme/grain-config",
  branch: "trunk",
  url: "https://github.com/acme/grain-config",
  path: ["settings/schedules.yml"],
  change: [
    {
      path: "settings/schedules.yml",
      op: "modify",
      patch:
        "--- a/settings/schedules.yml\n+++ b/settings/schedules.yml\n@@ -1,1 +1,1 @@\n-    title: nightly\n+    title: weekly\n",
      fragment: "    title: weekly\n",
    },
  ],
  docs: "https://example.com/docs",
};

describe("ConfigRefusalDialog", () => {
  it("says which file to edit where, linked, with the patch to copy", () => {
    render(<ConfigRefusalDialog refusal={refusal} onClose={() => {}} />);
    const where = screen.getByTestId("config-refusal-where");
    expect(where.textContent).toContain(
      "Edit settings/schedules.yml in acme/grain-config (trunk)",
    );
    expect(
      screen.getByRole("link", { name: "settings/schedules.yml" }),
    ).toHaveAttribute(
      "href",
      "https://github.com/acme/grain-config/blob/trunk/settings/schedules.yml",
    );
    expect(screen.getByTestId("config-refusal-patch").textContent).toContain(
      "+    title: weekly",
    );
    expect(
      screen.getByRole("button", { name: "Copy patch" }),
    ).toBeInTheDocument();
  });

  it("falls back to the description for a route with no patch", () => {
    render(
      <ConfigRefusalDialog
        refusal={{
          ...refusal,
          path: ["tables/gcp_key.json"],
          change: [],
          description: "edit the gcp_key entries",
        }}
        onClose={() => {}}
      />,
    );
    expect(screen.queryByTestId("config-refusal-patch")).toBeNull();
    expect(screen.getByText("edit the gcp_key entries.")).toBeInTheDocument();
  });

  it("renders nothing without a refusal", () => {
    const { container } = render(<ConfigRefusalDialog refusal={null} />);
    expect(container.textContent).toBe("");
  });
});

describe("configRepoRefusal", () => {
  it("reads the repository and its page off the remote", () => {
    expect(configRepoRefusal("git@github.com:acme/cfg.git", "")).toEqual({
      repo: "acme/cfg",
      remote: "git@github.com:acme/cfg.git",
      branch: "main",
      url: "https://github.com/acme/cfg",
    });
    expect(
      configRepoRefusal("https://git.example.com/acme/cfg.git", "b").url,
    ).toBe("");
    expect(
      configRepoRefusal("https://git.example.com/acme/cfg.git", "b").repo,
    ).toBe("acme/cfg");
  });

  it("maps a settings section to its file", () => {
    expect(settingsSectionPath("repositories")).toBe("tables/repo_config.json");
    expect(settingsSectionPath("general")).toBe("tables/grain_config.json");
  });
});
