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

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import api from "../api.js";
import {
  guardLeaving,
  leavingLosesEdits,
  useUnsavedEdits,
} from "../unsavedEdits.js";
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

  // The editor the refused write came from stays open behind the
  // dialog; once the pull request carries its edit, leaving it loses
  // nothing, so following "task N" must not ask DISCARD_PROMPT.
  function Editor() {
    const { formRef, isDirty } = useUnsavedEdits({});
    useEffect(() => guardLeaving(isDirty), [isDirty]);
    return (
      <form ref={formRef}>
        <input name="name" aria-label="Name" />
      </form>
    );
  }

  it("settles the editor behind it once the pull request opens", async () => {
    api.mockResolvedValue({ taskId: "42", number: 42 });
    const user = userEvent.setup();
    render(
      <>
        <Editor />
        <ConfigRefusalDialog refusal={internal} onClose={() => {}} />
      </>,
    );
    // The dialog is modal; type behind it as the person did before Save.
    screen.getByLabelText("Name", { hidden: true }).value = "Held template";
    expect(leavingLosesEdits()).toBe(true);
    await user.click(screen.getByRole("button", { name: "Create Config PR" }));
    await screen.findByTestId("config-refusal-opened");
    expect(leavingLosesEdits()).toBe(false);
    // An edit made after the pull request is one it does not carry.
    screen.getByLabelText("Name", { hidden: true }).value = "Changed again";
    expect(leavingLosesEdits()).toBe(true);
  });

  it("leaves the editor's edit guarded when the pull request fails", async () => {
    api.mockRejectedValue(new Error("no"));
    const user = userEvent.setup();
    render(
      <>
        <Editor />
        <ConfigRefusalDialog refusal={internal} onClose={() => {}} />
      </>,
    );
    screen.getByLabelText("Name", { hidden: true }).value = "Held template";
    await user.click(screen.getByRole("button", { name: "Create Config PR" }));
    await screen.findByTestId("config-refusal-error");
    expect(leavingLosesEdits()).toBe(true);
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

  it("lists what validation found rather than the server's one line", async () => {
    const err = new Error(
      "the config change does not pass validation: staterepo: the change would lock dispatch: max_parallel is 0, which lets no run start",
    );
    err.status = 422;
    err.validation = {
      liveness: [
        "max_parallel is 0, which lets no run start",
        "every model connection is disabled, so no run has anything to run on",
      ],
    };
    api.mockRejectedValue(err);
    const user = userEvent.setup();
    render(<ConfigRefusalDialog refusal={internal} onClose={() => {}} />);
    await user.click(screen.getByRole("button", { name: "Create Config PR" }));
    const shown = await screen.findByTestId("config-refusal-error");
    expect(shown).toHaveTextContent("This change does not pass validation");
    expect(shown).not.toHaveTextContent("staterepo:");
    expect(
      within(shown)
        .getAllByRole("listitem")
        .map((li) => li.textContent),
    ).toEqual(err.validation.liveness);
    // Opening it again could pass once what it sets is fixed elsewhere.
    expect(
      screen.getByRole("button", { name: "Create Config PR" }),
    ).toBeTruthy();
  });

  it("asks for the change again when grain/config moved since the refusal", async () => {
    const err = new Error(
      "the config change does not pass validation: the change does not apply to main: staterepo: applying the change to settings/schedules.yml: the patch does not match the file at line 5",
    );
    err.status = 422;
    err.code = "config_change_stale";
    api.mockRejectedValue(err);
    const user = userEvent.setup();
    render(<ConfigRefusalDialog refusal={internal} onClose={() => {}} />);
    await user.click(screen.getByRole("button", { name: "Create Config PR" }));
    const shown = await screen.findByTestId("config-refusal-error");
    expect(shown).toHaveTextContent(
      "grain/config has changed since this was refused",
    );
    expect(shown).toHaveTextContent("make the change again");
    // The same patch can only fail the same way.
    expect(
      screen.queryByRole("button", { name: "Create Config PR" }),
    ).toBeNull();
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
