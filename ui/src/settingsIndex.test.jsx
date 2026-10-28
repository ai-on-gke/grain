import { render, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { SETTINGS_INDEX, matchSettings } from "./settingsIndex.js";
import { SETTINGS_SECTIONS } from "./settingsSections.js";
import GeneralSection from "./components/settings/GeneralSection.jsx";
import AgentsSection from "./components/settings/AgentsSection.jsx";
import EngineSection from "./components/settings/EngineSection.jsx";
import CISection from "./components/settings/CISection.jsx";
import SandboxesSection from "./components/settings/SandboxesSection.jsx";
import CachesSection from "./components/settings/CachesSection.jsx";
import CapabilitiesSection from "./components/settings/CapabilitiesSection.jsx";
import CredentialsSection from "./components/settings/CredentialsSection.jsx";
import ModelsSection from "./components/settings/ModelsSection.jsx";
import TasksSection from "./components/settings/TasksSection.jsx";
import GitSection from "./components/settings/GitSection.jsx";
import UpgradeSection from "./components/settings/UpgradeSection.jsx";

// Models and Git read their own /api/settings and /api/deployment; an
// empty answer to each is enough for every field they own to draw.
vi.mock("./api.js", () => ({
  default: vi.fn((path) =>
    Promise.resolve(path === "/api/deployment" ? { id: "default" } : {}),
  ),
}));

// Every switch on, so every field a section can draw is drawn.
const settings = {
  configured: true,
  sandboxBackends: ["docker", "gvisor"],
  sandboxBackend: "docker",
  packageCacheEnabled: true,
  actionsCacheEnabled: true,
  upgradeCheckModes: ["off", "approve", "auto"],
  upgradeCheckMode: "auto",
  capabilities: [],
};

const s = {
  settings,
  saveChoice: vi.fn(),
  onTextBlur: () => () => {},
  onIntBlur: () => () => {},
  onFloatBlur: () => () => {},
  onEnterKey: () => {},
  markDirty: () => {},
  reloadSettings: vi.fn(),
  showError: vi.fn(),
  onSaved: vi.fn(),
};

const SECTIONS = {
  general: <GeneralSection s={s} config={{}} />,
  agents: <AgentsSection s={s} />,
  engine: <EngineSection s={s} />,
  ci: <CISection s={s} />,
  sandboxes: <SandboxesSection s={s} />,
  caches: <CachesSection s={s} />,
  capabilities: <CapabilitiesSection showError={vi.fn()} />,
  credentials: <CredentialsSection s={s} claimedSecrets={[]} />,
  models: <ModelsSection config={{}} showError={vi.fn()} />,
  tasks: <TasksSection s={s} config={{}} />,
  git: <GitSection showError={vi.fn()} />,
  upgrade: <UpgradeSection config={{}} showError={vi.fn()} />,
};

// renderedIds is every data-setting-id a section draws once it has
// settled: Models and Git draw theirs after reads of their own, so the
// set is compared until it matches (or waitFor gives up on it).
async function expectRenderedIds(element, expected) {
  const { container, unmount } = render(element);
  const ids = () =>
    [
      ...new Set(
        [...container.querySelectorAll("[data-setting-id]")].map((el) =>
          el.getAttribute("data-setting-id"),
        ),
      ),
    ].sort();
  try {
    await waitFor(() => expect(ids()).toEqual(expected));
  } finally {
    unmount();
  }
}

// docs/design/settings-panel.md §3.3: the filter is only as good as its
// index, so a field added to a section without an entry here fails.
describe("settingsIndex", () => {
  it.each(Object.keys(SECTIONS))(
    "lists exactly the fields Settings → %s draws",
    async (section) => {
      const indexed = new Set(
        SETTINGS_INDEX.filter((e) => e.section === section && !e.card).map(
          (e) => e.id,
        ),
      );
      await expectRenderedIds(SECTIONS[section], [...indexed].sort());
    },
  );

  // A filter pick scrolls to the card's anchor (or, for a field, to the
  // field itself): an entry naming an anchor its section never draws is
  // a result that goes nowhere.
  it.each(Object.keys(SECTIONS))(
    "points every Settings → %s entry at a card the section draws",
    async (section) => {
      const { container, unmount } = render(SECTIONS[section]);
      try {
        for (const entry of SETTINGS_INDEX.filter(
          (e) => e.section === section,
        )) {
          await waitFor(() =>
            expect(
              container.querySelector(`[id="${entry.anchor}"]`),
              entry.id,
            ).not.toBeNull(),
          );
        }
      } finally {
        unmount();
      }
    },
  );

  it("names only sections that exist, and every section at least once", () => {
    const sections = new Set(SETTINGS_SECTIONS.map((sec) => sec.id));
    for (const entry of SETTINGS_INDEX) {
      expect(sections.has(entry.section), entry.id).toBe(true);
    }
    for (const sec of sections) {
      expect(
        SETTINGS_INDEX.some((e) => e.section === sec),
        sec,
      ).toBe(true);
    }
  });

  it("has one entry per id", () => {
    const ids = SETTINGS_INDEX.map((e) => e.id);
    expect(new Set(ids).size).toBe(ids.length);
  });

  it("finds CPU and memory overcommit by label", () => {
    const found = matchSettings("overcommit").map((e) => e.id);
    expect(found).toEqual(
      expect.arrayContaining([
        "sandboxCpuOvercommit",
        "sandboxMemoryOvercommit",
        "sandboxDiskOvercommit",
      ]),
    );
    expect(found.every((id) => /Overcommit/.test(id))).toBe(true);
  });

  it("finds a field by a keyword of its helper text, after label matches", () => {
    const found = matchSettings("disk");
    expect(found[0].label).toMatch(/disk/i);
    expect(found.map((e) => e.id)).toContain("ciCacheMb");
  });

  // Upgrade moved into Settings from System: searching for it lands on
  // the section, ahead of the upgrade-check fields that mention it.
  it("finds Settings → Upgrade first for 'upgrade' and by 'rollback'", () => {
    expect(matchSettings("upgrade")[0]).toMatchObject({
      section: "upgrade",
      anchor: "upgrade",
    });
    expect(matchSettings("rollback").map((e) => e.section)).toContain(
      "upgrade",
    );
  });

  it("matches nothing for an empty query or a value", () => {
    expect(matchSettings("")).toEqual([]);
    expect(matchSettings("zzqqxx")).toEqual([]);
  });
});
