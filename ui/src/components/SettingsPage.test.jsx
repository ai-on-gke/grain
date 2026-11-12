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
import { afterEach, describe, expect, it, vi } from "vitest";
import api from "../api.js";
import SettingsPage, { SETTINGS_GROUPS } from "./SettingsPage.jsx";
import { PHONE_QUERY } from "../phone.js";
import { PreferencesProvider } from "../PreferencesContext.jsx";
import { chooseOption, optionValues } from "../searchSelectTesting.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const settings = {
  configured: true,
  pollInterval: "30s",
  promptExtension: "Run make test.",
  sandboxCpuOvercommit: 2,
  sandboxMemoryOvercommit: 1,
  maxAgentTurns: 0,
  capabilities: [],
};

function setup(props = {}) {
  api.mockImplementation((path, opts) => {
    if (path === "/api/settings" && !opts) return Promise.resolve(settings);
    if (path === "/api/settings") {
      // The server answers a write of the lock with configRepoOnly for
      // the row it wrote (no directConfigWrites here).
      const body = JSON.parse(opts.body);
      if (typeof body.settingsLocked === "boolean")
        body.configRepoOnly = body.settingsLocked;
      return Promise.resolve(body);
    }
    return Promise.resolve({});
  });
  const onSection = vi.fn();
  const view = render(
    <PreferencesProvider>
      <SettingsPage
        config={{}}
        section="engine"
        onSection={onSection}
        showError={vi.fn()}
        {...props}
      />
    </PreferencesProvider>,
  );
  return { ...view, onSection };
}

// design/design/settings-panel.design.md §3.
describe("SettingsPage", () => {
  afterEach(() => {
    api.mockReset();
    delete window.matchMedia;
  });

  it("lists the groups and sections in the design's order", () => {
    setup();
    const nav = screen.getByTestId("settings-nav");
    const labels = within(nav)
      .getAllByRole("button")
      .map((b) => b.textContent);
    expect(labels).toEqual(
      SETTINGS_GROUPS.flatMap((g) => g.sections.map((s) => s.label)),
    );
    // No Legacy capabilities: that tab was retired, its Deployment
    // credentials card moved onto Capabilities.
    expect(labels).not.toContain("Legacy capabilities");
    expect(labels).toEqual([
      "Models",
      "Sandboxes",
      "Access",
      "Hosts",
      "CLI",
      "Audit",
      "Preferences",
      "Upgrade",
      "Caches",
      "Engine",
      "Hub",
      "Config repo",
      "General",
      "Git",
      "Repositories",
      "Tasks",
      "Agents",
      "Capabilities",
      "CI",
      "Memory",
    ]);
  });

  // axe, 2026-10-26 walkthrough: the nav's lists held their buttons as
  // direct children, which a screen reader does not read as a list.
  it("puts every section and filter result in a list item of its own", async () => {
    const user = userEvent.setup();
    setup();
    const nav = screen.getByTestId("settings-nav");
    const strays = () =>
      [...nav.querySelectorAll("ul")].flatMap((ul) =>
        [...ul.children].filter((c) => c.tagName !== "LI"),
      );
    expect(strays()).toEqual([]);
    for (const button of within(nav).getAllByRole("button")) {
      expect(button.parentElement.tagName).toBe("LI");
    }
    await user.type(screen.getByLabelText("Filter settings"), "overcommit");
    expect(screen.getAllByTestId("settings-result").length).toBeGreaterThan(0);
    expect(strays()).toEqual([]);
  });

  it("marks the open section and opens another from the nav", async () => {
    const user = userEvent.setup();
    const { onSection } = setup();
    expect(await screen.findByLabelText("Poll interval")).toBeInTheDocument();
    expect(screen.getByTestId("settings-nav-engine")).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(
      screen.getByRole("heading", { name: "Engine", level: 1 }),
    ).toBeInTheDocument();
    expect(screen.getByTestId("settings-status")).toHaveTextContent(
      "All changes saved",
    );
    await user.click(screen.getByTestId("settings-nav-ci"));
    expect(onSection).toHaveBeenCalledWith("ci");
  });

  it("filters to matching fields, and a pick opens their section", async () => {
    const user = userEvent.setup();
    const { onSection } = setup();
    await user.type(screen.getByLabelText("Filter settings"), "overcommit");
    const results = screen.getAllByTestId("settings-result");
    expect(results.map((r) => r.textContent)).toEqual([
      "Overcommit mode, Sandboxes",
      "CPU overcommit, Sandboxes",
      "Disk overcommit, Sandboxes",
      "Memory overcommit, Sandboxes",
      "Auto CPU minimum, Sandboxes",
      "Auto CPU maximum, Sandboxes",
      "Auto memory minimum, Sandboxes",
      "Auto memory maximum, Sandboxes",
      "CPU pressure target (%), Sandboxes",
      "Memory pressure target (%), Sandboxes",
    ]);
    await user.click(results[0]);
    expect(onSection).toHaveBeenCalledWith("sandboxes", "sandboxOvercommit");
    // The groups are back once a pick is made.
    expect(screen.getByTestId("settings-nav-general")).toBeInTheDocument();
  });

  it("says so when nothing matches, and Escape clears the filter", async () => {
    const user = userEvent.setup();
    setup();
    const box = screen.getByLabelText("Filter settings");
    await user.type(box, "zzqqxx");
    expect(screen.getByTestId("settings-no-match")).toHaveTextContent(
      "No setting matches 'zzqqxx'.",
    );
    await user.keyboard("{Escape}");
    expect(box).toHaveValue("");
    expect(screen.getByTestId("settings-nav-general")).toBeInTheDocument();
  });

  it("marks an edited standing instruction dirty, asks before leaving, and Discard clears it", async () => {
    const user = userEvent.setup();
    const { onSection } = setup({ section: "agents" });
    const box = await screen.findByLabelText(
      "Standing instructions for every run",
    );
    await user.type(box, " And lint.");
    expect(screen.getByTestId("settings-status")).toHaveTextContent(
      "Unsaved changes in Standing instructions",
    );
    expect(screen.getByTestId("settings-dirty-dot")).toBeInTheDocument();

    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    await user.click(screen.getByTestId("settings-nav-engine"));
    expect(confirm).toHaveBeenCalled();
    expect(onSection).not.toHaveBeenCalled();

    // Picking a filter match in another section also respects the cancel
    // and keeps the current URL.
    window.history.replaceState(null, "", "/settings/agents");
    await user.type(screen.getByLabelText("Filter settings"), "overcommit");
    await user.click(screen.getAllByTestId("settings-result")[0]);
    expect(onSection).not.toHaveBeenCalled();
    expect(window.location.pathname).toBe("/settings/agents");
    await user.click(screen.getByLabelText("Clear filter"));
    confirm.mockRestore();

    await user.click(screen.getByTestId("promptExtension-discard"));
    expect(box).toHaveValue("Run make test.");
    expect(screen.getByTestId("settings-status")).toHaveTextContent(
      "All changes saved",
    );
  });

  it("clears the dirty dot on the left-behind section when discard is confirmed", async () => {
    const user = userEvent.setup();
    const { rerender, onSection } = setup({ section: "agents" });
    const box = await screen.findByLabelText(
      "Standing instructions for every run",
    );
    await user.type(box, " And lint.");
    expect(screen.getByTestId("settings-dirty-dot")).toBeInTheDocument();

    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    await user.click(screen.getByTestId("settings-nav-engine"));
    expect(confirm).toHaveBeenCalled();
    expect(onSection).toHaveBeenCalledWith("engine");
    confirm.mockRestore();

    rerender(
      <PreferencesProvider>
        <SettingsPage
          config={{}}
          section="engine"
          onSection={onSection}
          showError={vi.fn()}
        />
      </PreferencesProvider>,
    );
    expect(screen.queryByTestId("settings-dirty-dot")).toBeNull();
  });

  it("asks before switching away from Memory with an unsaved memory edit", async () => {
    const user = userEvent.setup();
    api.mockImplementation((path) => {
      if (path === "/api/memory")
        return Promise.resolve([{ repo: "", text: "existing note" }]);
      if (path === "/api/memory/documents") return Promise.resolve([]);
      return Promise.resolve({});
    });
    const onSection = vi.fn();
    render(
      <PreferencesProvider>
        <SettingsPage
          config={{}}
          section="memory"
          onSection={onSection}
          showError={vi.fn()}
        />
      </PreferencesProvider>,
    );
    const box = await screen.findByRole("textbox", {
      name: "Memory carried by every run",
    });
    await user.type(box, " edited");
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    await user.click(screen.getByTestId("settings-nav-general"));
    expect(confirm).toHaveBeenCalled();
    expect(onSection).not.toHaveBeenCalled();
    confirm.mockRestore();
  });

  it("saves a standing instruction on its own Save", async () => {
    const user = userEvent.setup();
    setup({ section: "agents" });
    const box = await screen.findByLabelText(
      "Standing instructions for every run",
    );
    await user.clear(box);
    await user.type(box, "Use conventional commits.");
    await user.click(screen.getByTestId("promptExtension-save"));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/settings", {
        method: "PUT",
        body: JSON.stringify({ promptExtension: "Use conventional commits." }),
      }),
    );
    expect(screen.queryByTestId("promptExtension-save")).toBeNull();
  });

  // design/design/config-repo-settings.design.md §2.1, §2.3: when the API sends
  // these sections' writes to the config repo (GET /api/config's
  // configRepoOnly), a section shows what the config repo holds,
  // read-only, and links to the files in the Config Repo Browser.
  it("draws a config-repo-only section read-only with links into the Config Repo Browser", async () => {
    setup({ section: "agents", config: { configRepoOnly: true } });
    const box = await screen.findByLabelText(
      "Standing instructions for every run",
    );
    expect(box).toHaveValue("Run make test.");
    expect(box).toBeDisabled();
    const notice = screen.getByTestId("config-managed-notice");
    const links = within(notice).getAllByTestId("config-repo-link");
    expect(links.map((a) => a.getAttribute("href"))).toEqual([
      "/config?path=tables%2Fgrain_config.json",
    ]);
    expect(links[0]).toHaveTextContent(
      "View in config repo → tables/grain_config.json",
    );
    expect(screen.queryByTestId("external-config-repo-banner")).toBeNull();
  });

  it("leaves Models, Sandboxes and the operational sections editable", async () => {
    for (const section of ["models", "sandboxes", "engine", "upgrade"]) {
      const { unmount } = setup({ section, config: { configRepoOnly: true } });
      expect(screen.queryByTestId("config-managed-notice")).toBeNull();
      unmount();
    }
  });

  it("keeps a section editable where there is no config repo to send it to", async () => {
    setup({ section: "agents" });
    const box = await screen.findByLabelText(
      "Standing instructions for every run",
    );
    expect(box).toBeEnabled();
    expect(screen.queryByTestId("config-managed-notice")).toBeNull();
  });

  it("opens a config-repo link in the app, without reloading the page", async () => {
    const user = userEvent.setup();
    setup({ section: "capabilities", config: { configRepoOnly: true } });
    const popped = vi.fn();
    window.addEventListener("popstate", popped);
    try {
      const links = within(
        screen.getByTestId("config-managed-notice"),
      ).getAllByTestId("config-repo-link");
      expect(links.map((a) => a.getAttribute("href"))).toEqual([
        "/config?path=tables%2Fgrain_config.json",
        "/config?path=tables%2Fapi_scope_set.json",
        "/config?path=settings%2Fcapabilities.yml",
      ]);
      await user.click(links[0]);
      expect(window.location.pathname).toBe("/config");
      expect(window.location.search).toBe("?path=tables%2Fgrain_config.json");
      expect(popped).toHaveBeenCalled();
    } finally {
      window.removeEventListener("popstate", popped);
      window.history.replaceState(null, "", "/");
    }
  });

  it("shows the section list on a phone at the bare /settings, and a section under a back arrow", async () => {
    window.matchMedia = (query) => ({
      matches: query === PHONE_QUERY,
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
    });
    const user = userEvent.setup();
    const { onSection, unmount } = setup({ section: "" });
    expect(screen.getByTestId("settings-nav")).toBeInTheDocument();
    expect(screen.queryByTestId("settings-section")).toBeNull();
    await user.click(screen.getByTestId("settings-nav-preferences"));
    expect(onSection).toHaveBeenCalledWith("preferences");
    unmount();

    const again = setup({ section: "preferences" });
    expect(screen.queryByTestId("settings-nav")).toBeNull();
    await user.click(screen.getByTestId("settings-back"));
    expect(again.onSection).toHaveBeenCalledWith("");
  });

  it("switches sections via the searchable Section picker on tablet widths", async () => {
    window.matchMedia = (query) => ({
      matches: query !== PHONE_QUERY && query.includes("899"),
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
    });
    const user = userEvent.setup();
    const { onSection } = setup({ section: "engine" });
    const picker = screen.getByRole("combobox", { name: "Section" });
    await user.click(picker);
    await user.clear(picker);
    await user.type(picker, "prefs");
    expect(optionValues(picker)).toContain("preferences");
    await chooseOption(user, picker, "preferences");
    expect(onSection).toHaveBeenCalledWith("preferences");
  });

  it("keeps the tablet Section picker on the current section when a dirty leave is cancelled", async () => {
    window.matchMedia = (query) => ({
      matches: query !== PHONE_QUERY && query.includes("899"),
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
    });
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
    try {
      const user = userEvent.setup();
      const { onSection } = setup({ section: "agents" });
      const promptBox = await screen.findByLabelText(
        "Standing instructions for every run",
      );
      await user.type(promptBox, " stay here");

      const picker = screen.getByRole("combobox", { name: "Section" });
      expect(picker).toHaveValue("Agents");
      await chooseOption(user, picker, "general");
      expect(confirmSpy).toHaveBeenCalled();
      expect(onSection).not.toHaveBeenCalled();
      expect(picker).toHaveValue("Agents");
    } finally {
      confirmSpy.mockRestore();
    }
  });

  // Agents' Commit identity and CLI's Sign in pointers were bare hrefs:
  // they reloaded the app and skipped the dirty-card prompt.
  it("opens Git from Agents and Agents from CLI in place, asking first when dirty", async () => {
    const confirmSpy = vi
      .spyOn(window, "confirm")
      .mockReturnValueOnce(false)
      .mockReturnValueOnce(true);
    try {
      const user = userEvent.setup();
      const { onSection, rerender } = setup({ section: "agents" });
      const promptBox = await screen.findByLabelText(
        "Standing instructions for every run",
      );
      await user.type(promptBox, " dirty edit");

      const gitLink = screen.getByRole("link", { name: /Settings → Git/ });
      await user.click(gitLink);
      expect(confirmSpy).toHaveBeenCalledTimes(1);
      expect(onSection).not.toHaveBeenCalled();

      await user.click(gitLink);
      expect(confirmSpy).toHaveBeenCalledTimes(2);
      expect(onSection).toHaveBeenCalledWith("git");

      onSection.mockClear();
      rerender(
        <PreferencesProvider>
          <SettingsPage
            config={{}}
            section="cli"
            onSection={onSection}
            showError={vi.fn()}
          />
        </PreferencesProvider>,
      );
      await user.click(
        await screen.findByTestId("cli-external-agent-api-link"),
      );
      expect(onSection).toHaveBeenCalledWith("agents", "external-agents");
    } finally {
      confirmSpy.mockRestore();
    }
  });

  it("edits max worker agents (10 by default) in the Engine section", async () => {
    const user = userEvent.setup();
    setup({ section: "engine" });
    const box = await screen.findByLabelText("Max worker agents");
    expect(box).toHaveValue(10);
    await user.clear(box);
    await user.type(box, "6");
    await user.tab();
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/settings", {
        method: "PUT",
        body: JSON.stringify({ maxWorkers: 6 }),
      }),
    );
  });

  it("shows a read-only banner and disables repo-managed setting inputs when externalConfigRepo is set", async () => {
    api.mockImplementation((path) => {
      if (path === "/api/settings") {
        return Promise.resolve({
          ...settings,
          externalConfigRepo: "https://github.com/acme/grain-config.git",
          externalConfigBranch: "trunk",
        });
      }
      return Promise.resolve({});
    });
    render(
      <PreferencesProvider>
        <SettingsPage
          config={{}}
          section="engine"
          onSection={vi.fn()}
          showError={vi.fn()}
        />
      </PreferencesProvider>,
    );
    expect(
      await screen.findByText(
        "Configuration is read-only and managed in https://github.com/acme/grain-config.git (trunk). Switch to the internal config repository in Config Repo settings to edit settings in the UI.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Poll interval")).toBeDisabled();
    expect(screen.getByLabelText("Max worker agents")).toBeDisabled();
  });

  // settings-mutability.spec.md D-9: external mode leaves secret entry
  // in the UI. A model connection's credential and an executor join
  // token are secrets the config repo never holds and their routes take
  // in external mode; the connection's own fields are the config repo's
  // and stay read-only.
  describe("under an external config repo", () => {
    const external = {
      ...settings,
      externalConfigRepo: "https://github.com/acme/grain-config.git",
      externalConfigBranch: "main",
      agentBackends: [
        {
          id: "conn-1",
          name: "Primary Claude",
          framework: "claude",
          authMode: "api_key",
          models: ["claude-opus-5-5"],
        },
      ],
    };
    const renderSection = (section) => {
      api.mockImplementation((path) => {
        if (path === "/api/settings") return Promise.resolve(external);
        if (path === "/api/deployment")
          return Promise.resolve({ id: "default", modelSettings: [] });
        return Promise.resolve({});
      });
      render(
        <PreferencesProvider>
          <SettingsPage
            config={{
              externalConfigRepo: external.externalConfigRepo,
              externalConfigBranch: external.externalConfigBranch,
            }}
            section={section}
            onSection={vi.fn()}
            showError={vi.fn()}
          />
        </PreferencesProvider>,
      );
    };

    it("keeps a model connection's credential usable and the connection read-only", async () => {
      const user = userEvent.setup();
      renderSection("models");
      await screen.findByTestId("external-config-repo-banner");
      await screen.findByText("Primary Claude");
      expect(
        screen.getByRole("button", { name: "New connection" }),
      ).toBeDisabled();
      expect(screen.getByRole("button", { name: "Disable" })).toBeDisabled();
      expect(screen.getByRole("button", { name: "Remove" })).toBeDisabled();
      // The deployment's model defaults are config and stay in the
      // read-only fieldset.
      expect(
        screen
          .getByRole("region", { name: "Models" })
          .querySelector("[data-settings-readonly]"),
      ).not.toBeNull();

      await user.click(screen.getByRole("button", { name: "Edit" }));
      expect(screen.getByLabelText("Name")).toBeDisabled();
      expect(screen.getByLabelText("Concurrency")).toBeDisabled();
      const credential = screen.getByLabelText("Claude Code OAuth token", {
        selector: "input[type=password]",
      });
      expect(credential).toBeEnabled();
      await user.type(credential, "sk-test");
      await user.click(screen.getByRole("button", { name: "Set credential" }));
      await waitFor(() =>
        expect(api).toHaveBeenCalledWith(
          "/api/agent-backends/conn-1/credential",
          { method: "PUT", body: JSON.stringify({ value: "sk-test" }) },
        ),
      );
      expect(
        screen.getByRole("button", { name: "Clear credential" }),
      ).toBeEnabled();
    });
  });

  // Upgrade moved here from System (/system/upgrade): it is an action,
  // not a setting, so it carries no save state and is not read-only
  // under an external config repo, and its pointer to the upgrade-check
  // settings opens General's Updates card.
  it("draws Upgrade as an action, outside the save model", async () => {
    const user = userEvent.setup();
    api.mockImplementation((path) => {
      if (path === "/api/upgrade") return Promise.resolve({ enabled: true });
      return Promise.resolve({});
    });
    const onSection = vi.fn();
    render(
      <PreferencesProvider>
        <SettingsPage
          config={{
            externalConfigRepo: "https://github.com/acme/grain-config.git",
          }}
          section="upgrade"
          onSection={onSection}
          showError={vi.fn()}
        />
      </PreferencesProvider>,
    );
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent(
      "Upgrade",
    );
    expect(screen.getByTestId("upgrade-section")).toBeInTheDocument();
    expect(screen.queryByTestId("settings-status")).toBeNull();
    expect(screen.queryByTestId("external-config-repo-banner")).toBeNull();
    expect(api).not.toHaveBeenCalledWith("/api/settings");
    await user.click(screen.getByTestId("upgrade-check-settings-link"));
    expect(onSection).toHaveBeenCalledWith("general", "updates");
  });

  it("marks the Memory section dirty in the header and nav when a memory note is edited", async () => {
    const user = userEvent.setup();
    api.mockImplementation((path) => {
      if (path === "/api/memory") {
        return Promise.resolve([{ repo: "acme/widgets", text: "Existing" }]);
      }
      if (path === "/api/memory/documents") return Promise.resolve([]);
      if (path === "/api/settings") return Promise.resolve(settings);
      return Promise.resolve({});
    });
    render(
      <PreferencesProvider>
        <SettingsPage
          config={{}}
          section="memory"
          onSection={vi.fn()}
          showError={vi.fn()}
        />
      </PreferencesProvider>,
    );
    const box = await screen.findByLabelText("Memory about acme/widgets");
    expect(screen.getByTestId("settings-status")).toHaveTextContent(
      "All changes saved",
    );
    expect(screen.queryByTestId("settings-dirty-dot")).toBeNull();

    await user.type(box, " note");
    expect(screen.getByTestId("settings-status")).toHaveTextContent(
      "Unsaved changes in Memory",
    );
    expect(screen.getByTestId("settings-dirty-dot")).toBeInTheDocument();

    await user.click(
      screen.getByRole("button", { name: "Revert acme/widgets memory" }),
    );
    expect(screen.getByTestId("settings-status")).toHaveTextContent(
      "All changes saved",
    );
    expect(screen.queryByTestId("settings-dirty-dot")).toBeNull();
  });

  it("clears dirty state when saving a multiline setting whose edit only added whitespace", async () => {
    const user = userEvent.setup();
    setup({ section: "agents" });
    const box = await screen.findByLabelText(
      "Standing instructions for every run",
    );
    await user.type(box, "   ");
    expect(screen.getByTestId("settings-status")).toHaveTextContent(
      "Unsaved changes in Standing instructions",
    );
    await user.click(screen.getByTestId("promptExtension-save"));
    await waitFor(() =>
      expect(screen.getByTestId("settings-status")).toHaveTextContent(
        "All changes saved",
      ),
    );
    expect(screen.queryByTestId("promptExtension-save")).toBeNull();
  });

  it("reloads settings and config when a secret is set in Credentials", async () => {
    const user = userEvent.setup();
    let settingsReads = 0;
    const onSaved = vi.fn();
    api.mockImplementation((path, init) => {
      if (path === "/api/settings" && !init?.method) {
        settingsReads += 1;
        return Promise.resolve(settings);
      }
      if (path === "/api/secrets" && !init?.method) {
        return Promise.resolve({ enabled: true, secrets: [] });
      }
      return Promise.resolve({});
    });
    render(
      <PreferencesProvider>
        <SettingsPage
          config={{}}
          section="capabilities"
          onSection={vi.fn()}
          onSaved={onSaved}
          showError={vi.fn()}
        />
      </PreferencesProvider>,
    );
    await screen.findByLabelText("Secret");
    expect(settingsReads).toBe(1);

    await user.type(screen.getByLabelText("Secret"), "my-secret");
    await user.type(screen.getByLabelText("Key"), "token");
    await user.type(screen.getByLabelText("Value"), "secret-value");
    await user.click(screen.getByRole("button", { name: "Set secret" }));

    await waitFor(() => expect(settingsReads).toBe(2));
    expect(onSaved).toHaveBeenCalledTimes(1);
  });

  // 2026-11-01 walkthrough: the config-map server's own secret was listed
  // under Other secrets, and its Delete broke the server. GET /api/secrets
  // now names the servers using each secret (task 2263), and the pane
  // lists it with them, without a Delete.
  it("lists a capability server's own secret with its server, not under Other secrets", async () => {
    const id = "https://grain.dev/capabilities/config-map";
    api.mockImplementation((path, init) => {
      if (path === "/api/settings" && !init?.method) {
        return Promise.resolve(settings);
      }
      if (path === "/api/secrets" && !init?.method) {
        return Promise.resolve({
          enabled: true,
          secrets: [
            {
              name: "capability-server-1f6d92c4",
              keys: ["secret_values"],
              capabilityServers: [id],
            },
            { name: "walk-throwaway", keys: ["token"] },
          ],
        });
      }
      return Promise.resolve({});
    });
    render(
      <PreferencesProvider>
        <SettingsPage
          config={{}}
          section="capabilities"
          onSection={vi.fn()}
          showError={vi.fn()}
        />
      </PreferencesProvider>,
    );
    expect(await screen.findByText("walk-throwaway")).toBeInTheDocument();
    const used = screen.getByRole("list", {
      name: "Secrets used by capability servers",
    });
    expect(used).toHaveTextContent("capability-server-1f6d92c4");
    expect(within(used).getByRole("link", { name: id })).toBeInTheDocument();
    expect(
      within(used).queryByRole("button", { name: "Delete secret" }),
    ).not.toBeInTheDocument();
  });

  it("keeps secrets from retired legacy capability statuses visible under Other secrets", async () => {
    api.mockImplementation((path, init) => {
      if (path === "/api/settings" && !init?.method) {
        return Promise.resolve({
          ...settings,
          agentBackends: [{ id: "gemini-1", framework: "gemini" }],
          capabilities: [
            {
              id: "github-sandbox",
              secrets: [
                {
                  name: "github-app/app-id",
                  secret: "github-app",
                  key: "app-id",
                  set: true,
                },
                {
                  name: "github-app/private-key",
                  secret: "github-app",
                  key: "private-key",
                  set: true,
                },
              ],
            },
            {
              id: "registry-push",
              secrets: [
                {
                  name: "registry-push",
                  secret: "registry-push",
                  key: "value",
                  set: true,
                },
              ],
            },
          ],
        });
      }
      if (path === "/api/secrets" && !init?.method) {
        return Promise.resolve({
          enabled: true,
          secrets: [
            { name: "github-app", keys: ["app-id", "private-key"] },
            { name: "registry-push", keys: ["value"] },
            { name: "registry-registration-token", keys: ["value"] },
            { name: "agent-backend-gemini-1", keys: ["value"] },
          ],
        });
      }
      return Promise.resolve({});
    });
    render(
      <PreferencesProvider>
        <SettingsPage
          config={{}}
          section="capabilities"
          onSection={vi.fn()}
          showError={vi.fn()}
        />
      </PreferencesProvider>,
    );
    const card = await screen.findByTestId("deployment-credentials");
    expect(await within(card).findByText("github-app")).toBeInTheDocument();
    expect(within(card).getByText("app-id")).toBeInTheDocument();
    expect(within(card).getByText("private-key")).toBeInTheDocument();
    expect(within(card).getByText("registry-push")).toBeInTheDocument();
    expect(
      within(card).queryByText("registry-registration-token"),
    ).not.toBeInTheDocument();
    expect(
      within(card).queryByText("agent-backend-gemini-1"),
    ).not.toBeInTheDocument();
  });

  it("marks an edited Memory note dirty in the header and nav, asks before switching sections, and clears the dot after confirming", async () => {
    const user = userEvent.setup();
    api.mockImplementation((path) => {
      if (path === "/api/settings") return Promise.resolve(settings);
      if (path === "/api/memory") {
        return Promise.resolve([
          {
            repo: "",
            text: "Use conventional commits.",
            updatedAt: "2026-10-20T00:00:00Z",
          },
        ]);
      }
      if (path === "/api/memory/documents") return Promise.resolve([]);
      return Promise.resolve({});
    });
    const onSection = vi.fn();
    const { rerender } = render(
      <PreferencesProvider>
        <SettingsPage
          config={{}}
          section="memory"
          onSection={onSection}
          showError={vi.fn()}
        />
      </PreferencesProvider>,
    );

    const box = await screen.findByRole("textbox", {
      name: "Memory carried by every run",
    });
    await user.type(box, " Always run tests.");
    expect(screen.getByTestId("settings-status")).toHaveTextContent(
      "Unsaved changes in Memory",
    );
    expect(screen.getByTestId("settings-dirty-dot")).toBeInTheDocument();

    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false);
    await user.click(screen.getByTestId("settings-nav-engine"));
    expect(confirm).toHaveBeenCalled();
    expect(onSection).not.toHaveBeenCalled();

    confirm.mockReturnValueOnce(true);
    await user.click(screen.getByTestId("settings-nav-engine"));
    expect(onSection).toHaveBeenCalledWith("engine");
    confirm.mockRestore();

    rerender(
      <PreferencesProvider>
        <SettingsPage
          config={{}}
          section="engine"
          onSection={onSection}
          showError={vi.fn()}
        />
      </PreferencesProvider>,
    );
    await waitFor(() =>
      expect(screen.queryByTestId("settings-dirty-dot")).toBeNull(),
    );
  });

  it("keeps the filter open when cancelling a jump to another section from a dirty card", async () => {
    const user = userEvent.setup();
    const { onSection } = setup({ section: "agents" });
    const box = await screen.findByLabelText(
      "Standing instructions for every run",
    );
    await user.type(box, " And lint.");

    const filterBox = screen.getByLabelText("Filter settings");
    await user.type(filterBox, "overcommit");
    const results = screen.getAllByTestId("settings-result");

    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    await user.click(results[0]);
    expect(confirm).toHaveBeenCalled();
    expect(onSection).not.toHaveBeenCalled();
    expect(filterBox).toHaveValue("overcommit");
    confirm.mockRestore();
  });

  it("reloads settings and clears the externalConfigRepo read-only state when Config repo disconnects", async () => {
    const user = userEvent.setup();
    let externalRepo = "https://github.com/acme/grain-config.git";
    api.mockImplementation((path, opts) => {
      if (path === "/api/settings") {
        return Promise.resolve({
          ...settings,
          externalConfigRepo: externalRepo,
          externalConfigBranch: "main",
        });
      }
      if (path === "/api/state-repo") {
        return Promise.resolve({
          available: true,
          remote: externalRepo,
          branch: "main",
          commits: [],
        });
      }
      if (path === "/api/state-repo/remote" && opts?.method === "POST") {
        externalRepo = "";
        return Promise.resolve({ available: true, remote: "" });
      }
      return Promise.resolve({});
    });

    const onSaved = vi.fn();
    const { rerender } = render(
      <PreferencesProvider>
        <SettingsPage
          config={{
            externalConfigRepo: "https://github.com/acme/grain-config.git",
          }}
          section="config-repo"
          onSection={vi.fn()}
          onSaved={onSaved}
          showError={vi.fn()}
        />
      </PreferencesProvider>,
    );

    const switchBtn = await screen.findByRole("button", {
      name: /Switch to internal config repository/i,
    });
    await user.click(switchBtn);
    await waitFor(() => expect(onSaved).toHaveBeenCalled());

    rerender(
      <PreferencesProvider>
        <SettingsPage
          config={{
            externalConfigRepo: "https://github.com/acme/grain-config.git",
          }}
          section="engine"
          onSection={vi.fn()}
          onSaved={onSaved}
          showError={vi.fn()}
        />
      </PreferencesProvider>,
    );
    expect(await screen.findByLabelText("Poll interval")).not.toBeDisabled();
  });

  it("renders overcommit mode, bounds, targets, and live effective factors in Sandboxes and saves sandboxOvercommit", async () => {
    const user = userEvent.setup();
    api.mockImplementation((path, init) => {
      if (path === "/api/settings" && !init?.method) {
        return Promise.resolve({
          ...settings,
          sandboxOvercommit: {
            mode: "auto",
            cpuMin: 1,
            cpuMax: 5,
            memoryMin: 1,
            memoryMax: 1.8,
            cpuPressureTarget: 20,
            memoryPressureTarget: 4,
          },
          sandboxCommitment: {
            workers: 4,
            mergers: 1,
            ciJobs: 1,
            runs: 6,
            cpusPerSandbox: 2,
            memoryMbPerSandbox: 2048,
            cpusCommitted: 12,
            memoryMbCommitted: 12288,
            hostCpus: 8,
            hostMemoryMb: 16384,
            cpuRatio: 1.5,
            memoryShare: 0.75,
            level: "ok",
            budget: {
              mode: "auto",
              cpuOvercommit: 3.25,
              memoryOvercommit: 1.35,
              cpus: 26,
              cpusBounded: true,
              memoryMb: 19388,
              memoryBounded: true,
              reserveMemoryMb: 2048,
              overcommit: {
                mode: "auto",
                cpuOvercommit: 3.25,
                memoryOvercommit: 1.35,
                last: { cpuSome: 11.2, memorySome: 1.8 },
              },
            },
          },
        });
      }
      if (path === "/api/settings" && init?.method === "PUT") {
        const body = JSON.parse(init.body);
        return Promise.resolve({ ...settings, ...body });
      }
      return Promise.resolve({});
    });

    render(
      <PreferencesProvider>
        <SettingsPage
          config={{}}
          section="sandboxes"
          onSection={vi.fn()}
          showError={vi.fn()}
        />
      </PreferencesProvider>,
    );

    const modeSelect = await screen.findByLabelText("Overcommit mode");
    expect(modeSelect).toHaveValue("auto");
    expect(screen.getByTestId("auto-overcommit-effective")).toHaveTextContent(
      /CPU 3\.25×.*memory 1\.35×.*PSI: CPU 11\.2%, memory 1\.8%/,
    );

    const cpuMaxInput = screen.getByLabelText("Auto CPU maximum");
    expect(cpuMaxInput).toHaveValue(5);
    await user.clear(cpuMaxInput);
    await user.type(cpuMaxInput, "4.5");
    await user.tab();

    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/settings", {
        method: "PUT",
        body: JSON.stringify({
          sandboxOvercommit: {
            mode: "auto",
            cpuMin: 1,
            cpuMax: 4.5,
            memoryMin: 1,
            memoryMax: 1.8,
            cpuPressureTarget: 20,
            memoryPressureTarget: 4,
          },
        }),
      }),
    );
  });

  it("allows locking and unlocking settings in the UI from General", async () => {
    const user = userEvent.setup();
    setup({
      section: "general",
      config: { configRepo: "grain/config", settingsLocked: false },
    });

    const envBox = await screen.findByLabelText("Environment name");
    const lockToggle = screen.getByRole("checkbox", {
      name: /Lock settings in UI \(require config repo PRs\)/i,
    });
    expect(envBox).toBeEnabled();
    expect(lockToggle).not.toBeChecked();
    expect(screen.queryByTestId("config-managed-notice")).toBeNull();

    // Lock settings in UI
    await user.click(lockToggle);
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/settings", {
        method: "PUT",
        body: JSON.stringify({ settingsLocked: true }),
      }),
    );
    expect(lockToggle).toBeChecked();
    expect(screen.getByLabelText("Environment name")).toBeDisabled();
    expect(screen.getByTestId("config-managed-notice")).toBeInTheDocument();
    expect(lockToggle).toBeEnabled();

    // Unlock settings in UI again
    await user.click(lockToggle);
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/settings", {
        method: "PUT",
        body: JSON.stringify({ settingsLocked: false }),
      }),
    );
    expect(lockToggle).not.toBeChecked();
    expect(screen.getByLabelText("Environment name")).toBeEnabled();
    expect(screen.queryByTestId("config-managed-notice")).toBeNull();
  });

  // settings-mutability D-10: with directConfigWrites in state-repo.json
  // the server accepts every write though settings_locked is on, and
  // says so with configRepoOnly: false; the sections stay editable.
  it("keys the lock on configRepoOnly, not settingsLocked", async () => {
    api.mockImplementation((path) =>
      Promise.resolve(
        path === "/api/settings"
          ? { ...settings, settingsLocked: true, configRepoOnly: false }
          : {},
      ),
    );
    render(
      <PreferencesProvider>
        <SettingsPage
          config={{
            configRepo: "grain/config",
            settingsLocked: true,
            configRepoOnly: false,
          }}
          section="general"
          onSection={vi.fn()}
          showError={vi.fn()}
        />
      </PreferencesProvider>,
    );
    expect(await screen.findByLabelText("Environment name")).toBeEnabled();
    expect(
      screen.getByRole("checkbox", {
        name: /Lock settings in UI \(require config repo PRs\)/i,
      }),
    ).toBeChecked();
    expect(screen.queryByTestId("config-managed-notice")).toBeNull();
  });

  // settings-mutability D-12: without a config repository (`grain demo`),
  // General disables the Settings lock card rather than offering a lock
  // toward a grain/config that does not exist.
  it("disables Lock settings in UI when no configRepo is reported", async () => {
    setup({ section: "general", config: {} });
    expect(await screen.findByLabelText("Environment name")).toBeEnabled();
    const lockToggle = screen.getByRole("checkbox", {
      name: /Lock settings in UI \(require config repo PRs\)/i,
    });
    expect(lockToggle).toBeDisabled();
    expect(
      screen.getByText(/Unavailable without a config repository/i),
    ).toBeInTheDocument();
  });

  // settings-mutability.spec.md D-1, D-2: while settings are locked the
  // API refuses every config-managed grain_config field with 409, so the
  // UI draws those read-only and leaves the bootstrap ones editable.
  describe("with settings locked", () => {
    const locked = { configRepoOnly: true, settingsLocked: true };
    function setupLocked(section) {
      const lockedSettings = {
        ...settings,
        settingsLocked: true,
        configRepoOnly: true,
      };
      api.mockImplementation((path, opts) => {
        if (path === "/api/settings" && !opts)
          return Promise.resolve(lockedSettings);
        return Promise.resolve({});
      });
      return render(
        <PreferencesProvider>
          <SettingsPage
            config={locked}
            section={section}
            onSection={vi.fn()}
            showError={vi.fn()}
          />
        </PreferencesProvider>,
      );
    }

    it("locks Engine's poll interval and merge queue but not max workers", async () => {
      setupLocked("engine");
      expect(await screen.findByLabelText("Poll interval")).toBeDisabled();
      expect(
        screen.getByLabelText("Max merges per repo branch"),
      ).toBeDisabled();
      expect(screen.getByLabelText("Max worker agents")).toBeEnabled();
      expect(screen.getByTestId("config-managed-notice")).toBeInTheDocument();
    });

    it("draws Caches read-only", async () => {
      setupLocked("caches");
      const box = await screen.findByRole("checkbox", {
        name: /Cache apt, pip/,
      });
      expect(box).toBeDisabled();
      expect(screen.getByTestId("config-managed-notice")).toBeInTheDocument();
    });

    it("locks the session sandbox idle TTL but not sandbox sizes", async () => {
      setupLocked("sandboxes");
      expect(
        await screen.findByLabelText("Session sandbox idle TTL"),
      ).toBeDisabled();
      expect(screen.getByLabelText("Sandbox vCPUs")).toBeEnabled();
    });

    it("locks Deployment credentials' GitHub host and GCP project", async () => {
      setupLocked("capabilities");
      expect(await screen.findByLabelText("GitHub host")).toBeDisabled();
      expect(screen.getByLabelText(/GCP project/)).toBeDisabled();
    });

    it("leaves Engine editable when settings are unlocked", async () => {
      setup({ section: "engine", config: { settingsLocked: false } });
      expect(await screen.findByLabelText("Poll interval")).toBeEnabled();
      expect(screen.getByLabelText("Max merges per repo branch")).toBeEnabled();
      expect(screen.queryByTestId("config-managed-notice")).toBeNull();
    });

    it("omits settings/capabilities.yml from Tasks and Capabilities notices when not exported, and draws each Tasks link once", async () => {
      const lockedSettings = {
        ...settings,
        settingsLocked: true,
        configRepoOnly: true,
      };
      const baseEntries = [
        { path: "tables/grain_config.json", size: 200 },
        { path: "tables/custom_field.json", size: 80 },
        { path: "tables/api_scope_set.json", size: 80 },
        { path: "settings/templates.yml", size: 40 },
      ];
      api.mockImplementation((path, opts) => {
        if (path === "/api/settings" && !opts)
          return Promise.resolve(lockedSettings);
        if (path === "/api/state-repo/files")
          return Promise.resolve({ entries: baseEntries });
        return Promise.resolve({});
      });

      const { unmount } = render(
        <PreferencesProvider>
          <SettingsPage
            config={locked}
            section="tasks"
            onSection={vi.fn()}
            showError={vi.fn()}
          />
        </PreferencesProvider>,
      );
      await waitFor(() =>
        expect(
          screen
            .getAllByTestId("config-repo-link")
            .map((a) => a.getAttribute("data-config-path")),
        ).toEqual(["tables/grain_config.json", "tables/custom_field.json"]),
      );
      unmount();

      render(
        <PreferencesProvider>
          <SettingsPage
            config={locked}
            section="capabilities"
            onSection={vi.fn()}
            showError={vi.fn()}
          />
        </PreferencesProvider>,
      );
      await waitFor(() =>
        expect(
          screen
            .getAllByTestId("config-repo-link")
            .map((a) => a.getAttribute("data-config-path")),
        ).toEqual(["tables/grain_config.json", "tables/api_scope_set.json"]),
      );
    });

    it("draws each Tasks config-repo link once when settings/capabilities.yml is exported", async () => {
      const lockedSettings = {
        ...settings,
        settingsLocked: true,
        configRepoOnly: true,
      };
      api.mockImplementation((path, opts) => {
        if (path === "/api/settings" && !opts)
          return Promise.resolve(lockedSettings);
        if (path === "/api/state-repo/files") {
          return Promise.resolve({
            entries: [
              { path: "tables/grain_config.json", size: 200 },
              { path: "tables/custom_field.json", size: 80 },
              { path: "settings/capabilities.yml", size: 120 },
            ],
          });
        }
        return Promise.resolve({});
      });

      render(
        <PreferencesProvider>
          <SettingsPage
            config={locked}
            section="tasks"
            onSection={vi.fn()}
            showError={vi.fn()}
          />
        </PreferencesProvider>,
      );
      expect(await screen.findByText("Default grants")).toBeInTheDocument();
      expect(
        screen
          .getAllByTestId("config-repo-link")
          .map((a) => a.getAttribute("data-config-path")),
      ).toEqual([
        "tables/grain_config.json",
        "tables/custom_field.json",
        "settings/capabilities.yml",
      ]);
    });
  });

  it("passes cross-section filter picks to onSection without overwriting history, while updating same-section hashes in place", async () => {
    const user = userEvent.setup();
    window.history.replaceState(null, "", "/settings/engine");
    const replaceSpy = vi.spyOn(window.history, "replaceState");
    try {
      const { onSection } = setup({ section: "engine" });
      await screen.findByLabelText("Poll interval");

      await user.type(screen.getByLabelText("Filter settings"), "house style");
      await user.click(screen.getAllByTestId("settings-result")[0]);
      expect(onSection).toHaveBeenCalledWith("agents", "promptExtension");
      expect(replaceSpy).not.toHaveBeenCalled();

      onSection.mockClear();
      await user.type(
        screen.getByLabelText("Filter settings"),
        "Max worker agents",
      );
      await user.click(screen.getAllByTestId("settings-result")[0]);
      expect(onSection).not.toHaveBeenCalled();
      expect(window.location.hash).toBe("#maxWorkers");
    } finally {
      replaceSpy.mockRestore();
      window.history.replaceState(null, "", "/");
    }
  });

  it("reveals and highlights the parent card when filtering for a conditionally hidden setting", async () => {
    const user = userEvent.setup();
    setup({ section: "general" });
    await screen.findByLabelText("Environment name");

    await user.type(
      screen.getByLabelText("Filter settings"),
      "Automatic upgrade time of day",
    );
    await user.click(screen.getAllByTestId("settings-result")[0]);

    const updatesCard = document.getElementById("updates");
    expect(updatesCard).not.toBeNull();
    expect(updatesCard).toHaveClass("setting-highlight");
  });

  it("exits an open section item subpage when clicking the section nav or picking a same-section filter match", async () => {
    const user = userEvent.setup();
    const { onSection } = setup({
      section: "config-repo",
      item: "tables/grain_config.json",
    });

    await user.click(screen.getByTestId("settings-nav-config-repo"));
    expect(onSection).toHaveBeenCalledWith("config-repo");

    onSection.mockClear();
    await user.type(screen.getByLabelText("Filter settings"), "state repo");
    await user.click(screen.getAllByTestId("settings-result")[0]);
    expect(onSection).toHaveBeenCalledWith("config-repo", "config-repo");
  });

  it("renders Settings → Access without reading GET /api/settings when the caller lacks settings:read", async () => {
    const showError = vi.fn();
    api.mockImplementation((path) => {
      if (path === "/api/settings") {
        const err = new Error(
          "role access-admins holds no scope settings:read",
        );
        err.status = 403;
        return Promise.reject(err);
      }
      if (path === "/api/access/principals") {
        return Promise.resolve({
          entries: [{ entry: "user:alice@example.com", role: "grain/viewer" }],
          roles: [{ name: "grain/viewer", builtin: true }],
        });
      }
      if (path === "/api/access/history") {
        return Promise.resolve([]);
      }
      return Promise.resolve({});
    });

    render(
      <PreferencesProvider>
        <SettingsPage
          config={{
            authenticated: true,
            actor: "user:alice@example.com",
            authMethod: "iap",
            access: {
              role: "access-admins",
              roles: ["access-admins"],
              full: false,
              scopes: ["access:read", "access:write"],
            },
          }}
          section="access"
          onSection={vi.fn()}
          showError={showError}
        />
      </PreferencesProvider>,
    );

    expect(
      await screen.findByTestId("principal-row-user:alice@example.com"),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/Deployment settings could not be read/),
    ).toBeNull();
    expect(
      api.mock.calls.filter(([path]) => path === "/api/settings"),
    ).toHaveLength(0);
    expect(showError).not.toHaveBeenCalled();
  });
});
