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
import { afterEach, describe, expect, it, vi } from "vitest";
import { SETTINGS_SECTIONS, SYSTEM_TABS } from "../settingsSections.js";
import CachesPage from "./CachesPage.jsx";
import SandboxHealthPage from "./SandboxHealthPage.jsx";
import CachesSection from "./settings/CachesSection.jsx";
import TasksSection from "./settings/TasksSection.jsx";
import { resumeCancelsUpgrade } from "../alerts.js";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// The System panes send an operator elsewhere for the knobs behind what
// they show -- "how large it may grow is on System → Sandbox &
// Infrastructure". Those sentences are prose, so nothing ties them to the
// tab strip, and they had drifted: the Sandboxes page sent CI history to
// "Settings → General" (it is on Engine & Concurrency), called the job
// cache and artifact bounds daemon flags while the panels under them said
// they were editable, and every pointer named a "Settings" pane that is
// called Admin. Each pointer these panes render has to name a tab that is
// really there.
const LABELS = {
  Settings: SETTINGS_SECTIONS.map((t) => t.label),
  System: SYSTEM_TABS.map((t) => t.label),
};

function pointers(text) {
  return [...text.matchAll(/(\S+) → ([^.,;]+)/g)].map(([, pane, rest]) => ({
    pane,
    rest: rest.trim(),
  }));
}

function expectRealTabs(text) {
  const found = pointers(text);
  expect(found.length).toBeGreaterThan(0);
  for (const { pane, rest } of found) {
    expect(["Settings", "System"], `"${pane} → ${rest}"`).toContain(pane);
    expect(
      LABELS[pane].some((label) => rest.startsWith(label)),
      `"${rest}" starts with no ${pane} section's name`,
    ).toBe(true);
  }
  return found;
}

const host = {
  loadAverage1: 0.5,
  loadAverage5: 0.4,
  loadAverage15: 0.3,
  cpus: 4,
  memoryUsedMB: 512,
  memoryTotalMB: 1024,
  disks: [],
};

describe("pointers to Settings sections and System tabs", () => {
  afterEach(() => api.mockReset());

  it("on the Sandboxes page name tabs that exist", async () => {
    api.mockResolvedValue({
      enabled: true,
      sandboxes: [],
      host,
      packageCache: [{ kind: "apt", entries: 1, bytes: 1 << 20 }],
      ciHistory: {
        bytes: 1 << 20,
        runs: 3,
        steps: 9,
        ceiling: 64 << 20,
        ageDays: 14,
        runsKept: 5000,
      },
      ciJobCache: {
        bytes: 1 << 20,
        entries: 2,
        maxBytes: 256 << 20,
        maxEntries: 4096,
        maxAgeSeconds: 30 * 86400,
        hits: 1,
        misses: 1,
      },
      ciArtifacts: {
        bytes: 1 << 20,
        runs: 1,
        artifacts: 1,
        maxBytes: 2 << 30,
        maxRunBytes: 128 << 20,
        maxArtifactBytes: 32 << 20,
        maxAgeSeconds: 14 * 86400,
      },
    });
    render(<SandboxHealthPage showError={() => {}} />);
    await screen.findByText(/CI history -- the step output/);

    const found = expectRealTabs(document.body.textContent);
    const where = found.map((p) => p.rest);
    expect(where.some((w) => w.startsWith("CI"))).toBe(true);
    expect(where.some((w) => w.startsWith("Caches"))).toBe(true);
    expect(document.body.textContent).not.toMatch(/-ci-(cache|artifact)-max/);
  });

  it("on the Caches page name tabs that exist, cache on or off", async () => {
    api.mockImplementation((path) =>
      Promise.resolve(
        path.includes("actions")
          ? {
              enabled: true,
              usedByJobs: false,
              entries: 0,
              bytes: 0,
              repos: [],
            }
          : { enabled: false },
      ),
    );
    render(<CachesPage showError={() => {}} />);
    await screen.findByText(/No CI job is being handed this cache/);

    expectRealTabs(document.body.textContent);
  });

  it("on the Caches and Tasks settings sections name tabs that exist", () => {
    const stubS = {
      settings: { packageCacheEnabled: true, capabilities: [] },
      saveChoice: () => {},
      onIntBlur: () => () => {},
      onEnterKey: () => {},
    };
    const { unmount } = render(<CachesSection s={stubS} />);
    const cacheRefs = expectRealTabs(document.body.textContent);
    expect(cacheRefs).toContainEqual({ pane: "System", rest: "Caches" });
    expect(document.body.textContent).not.toMatch(
      /Caches page in the sidebar/i,
    );
    unmount();

    render(<TasksSection s={stubS} config={{}} tasks={[]} />);
    expect(document.body.textContent).not.toMatch(/Integrations & Auth/);
  });

  it("on scheduled-upgrade resume prompts name tabs that exist", async () => {
    const upgradeFound = expectRealTabs(
      resumeCancelsUpgrade({ reason: "upgrading to v2.1.0" }),
    );
    expect(
      upgradeFound.some(
        (p) => p.pane === "Settings" && p.rest.startsWith("Upgrade"),
      ),
    ).toBe(true);
  });
});
