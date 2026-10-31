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
import api from "../api.js";
import ConfigRefusalDialog, {
  configRepoRefusal,
  settingsSectionPath,
} from "./ConfigRefusal.jsx";

vi.mock("../api.js", () => ({ default: vi.fn() }));

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

// docs/design/config-repo-settings.md §2.3: a refusal of this
// deployment's own grain/config opens its file in the Config Repo
// Browser, and can open the config pull request itself.
describe("ConfigRefusalDialog for the internal grain/config", () => {
  const internal = {
    ...refusal,
    repo: "grain/config",
    branch: "main",
    url: "/repos/grain/config",
    internal: true,
    description: "set the deployment title",
  };
  afterEach(() => api.mockReset());

  it("links each file into the Config Repo Browser", () => {
    render(<ConfigRefusalDialog refusal={internal} onClose={() => {}} />);
    const link = screen.getByRole("link", {
      name: "Open in Config Browser → settings/schedules.yml",
    });
    expect(link).toHaveAttribute(
      "href",
      "/config?path=settings%2Fschedules.yml",
    );
  });

  it("opens a held config pull request carrying the refusal's change", async () => {
    api.mockResolvedValue({ taskId: "42", number: 42 });
    const user = userEvent.setup();
    render(<ConfigRefusalDialog refusal={internal} onClose={() => {}} />);
    await user.click(screen.getByRole("button", { name: "Create Config PR" }));
    expect(api).toHaveBeenCalledWith("/api/state-repo/pull-requests", {
      method: "POST",
      body: JSON.stringify({
        title: "Set the deployment title",
        changes: internal.change,
      }),
    });
    const opened = await screen.findByTestId("config-refusal-opened");
    expect(opened).toHaveTextContent("Opened config pull request #42");
    expect(screen.getByRole("link", { name: "task 42" })).toHaveAttribute(
      "href",
      "/tasks/42",
    );
    expect(
      screen.queryByRole("button", { name: "Create Config PR" }),
    ).toBeNull();
  });

  it("says why a config pull request could not be opened", async () => {
    api.mockRejectedValue(
      new Error("the config change does not pass validation"),
    );
    const user = userEvent.setup();
    render(<ConfigRefusalDialog refusal={internal} onClose={() => {}} />);
    await user.click(screen.getByRole("button", { name: "Create Config PR" }));
    await waitFor(() =>
      expect(screen.getByTestId("config-refusal-error")).toHaveTextContent(
        "the config change does not pass validation",
      ),
    );
  });

  it("offers no pull request for an external repository", () => {
    render(<ConfigRefusalDialog refusal={refusal} onClose={() => {}} />);
    expect(
      screen.queryByRole("button", { name: "Create Config PR" }),
    ).toBeNull();
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
