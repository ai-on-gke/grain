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
    if (path === "/api/settings") return Promise.resolve(JSON.parse(opts.body));
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

// docs/design/settings-panel.md §3.
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
    expect(labels).toEqual([
      "Models",
      "Sandboxes",
      "Executors",
      "Legacy capabilities",
      "Access",
      "Preferences",
      "Upgrade",
      "Caches",
      "Engine",
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
    expect(onSection).toHaveBeenCalledWith("sandboxes");
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

  // docs/design/config-repo-settings.md §2.1, §2.3: when the API sends
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
      const link = within(
        screen.getByTestId("config-managed-notice"),
      ).getByTestId("config-repo-link");
      expect(link).toHaveAttribute(
        "href",
        "/config?path=settings%2Fcapabilities.yml",
      );
      await user.click(link);
      expect(window.location.pathname).toBe("/config");
      expect(window.location.search).toBe("?path=settings%2Fcapabilities.yml");
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
    expect(onSection).toHaveBeenCalledWith("general");
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
          section="credentials"
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
    setup({ section: "general", config: { settingsLocked: false } });

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

  // settings-mutability.spec.md D-1, D-2: while settings are locked the
  // API refuses every config-managed grain_config field with 409, so the
  // UI draws those read-only and leaves the bootstrap ones editable.
  describe("with settings locked", () => {
    const locked = { configRepoOnly: true, settingsLocked: true };
    function setupLocked(section) {
      const lockedSettings = { ...settings, settingsLocked: true };
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
      expect(screen.getByLabelText("Merge batch size")).toBeDisabled();
      expect(screen.getByLabelText("Merge gather window")).toBeDisabled();
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

    it("locks Legacy capabilities' GitHub host and GCP project", async () => {
      setupLocked("credentials");
      expect(await screen.findByLabelText("GitHub host")).toBeDisabled();
      expect(screen.getByLabelText(/GCP project/)).toBeDisabled();
    });

    it("leaves Engine editable when settings are unlocked", async () => {
      setup({ section: "engine", config: { settingsLocked: false } });
      expect(await screen.findByLabelText("Poll interval")).toBeEnabled();
      expect(screen.getByLabelText("Merge batch size")).toBeEnabled();
      expect(screen.queryByTestId("config-managed-notice")).toBeNull();
    });
  });
});
