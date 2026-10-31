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
import { describe, expect, it, vi } from "vitest";
import TasksSection from "./TasksSection.jsx";
import api from "../../api.js";
import { resetCapabilityMethodsForTest } from "../capabilityMethods.js";

vi.mock("../../api.js", () => ({ default: vi.fn(async () => ({})) }));

// cloudKey is a capability server method with arguments, so a grant of it
// is an inline section of GrantsPicker's.
const cloudKey = {
  ref: "https://example.com/caps/cloud#cloud-key",
  method: "cloud-key",
  args: {
    type: "object",
    required: ["account"],
    properties: {
      account: { type: "string", minLength: 1 },
      ttl: { type: "integer", default: 60, maximum: 720 },
    },
  },
};
const cloudGrant = (w) => `${cloudKey.ref}=${JSON.stringify(w)}`;

function serveMethods() {
  api.mockImplementation(async (path, opts) => {
    if (path === "/api/capability-methods") return [cloudKey];
    if (path === "/api/capability-grants/check")
      return JSON.parse(opts.body).grants.map(
        ({ grant, capability, with: w }) => ({
          grant,
          recorded: grant || `${capability}=${JSON.stringify(w)}`,
        }),
      );
    return {};
  });
}

function section(settings, extra = {}) {
  return {
    settings,
    saveChoice: vi.fn(() => Promise.resolve(true)),
    onIntBlur: () => () => {},
    onEnterKey: () => {},
    ...extra,
  };
}

// Settings → Tasks saves through the page's useSettingsSection, as the
// sections beside it do.
describe("TasksSection", () => {
  it("saves a task default through saveChoice", async () => {
    const user = userEvent.setup();
    const s = section({ approvedByDefault: true, capabilities: [] });
    render(<TasksSection s={s} config={{}} />);
    await user.click(
      screen.getByRole("checkbox", { name: /Auto-merge new tasks by default/ }),
    );
    expect(s.saveChoice).toHaveBeenCalledWith({ autoMergeByDefault: true });
  });

  // The server dropped the deployment-wide ignoreGitHubCI (each repo's CI
  // mode decides instead), so a box for it saved nothing and read back
  // unchecked after a reload -- the 2026-10-23 settings walkthrough.
  it("offers no deployment-wide GitHub-CI box, and points at each repo's CI mode", () => {
    render(<TasksSection s={section({ capabilities: [] })} config={{}} />);
    expect(
      screen.queryByRole("checkbox", { name: /gate everywhere/i }),
    ).not.toBeInTheDocument();
    expect(screen.getByTestId("ci-gate-per-repo")).toHaveTextContent(
      "each repository’s own CI mode",
    );
  });

  it("offers a build default capability missing from the defaults", async () => {
    const user = userEvent.setup();
    const s = section({
      defaultCapabilities: ["gcp-key"],
      capabilities: [
        { id: "gcp-key", name: "GCP key" },
        { id: "gemini-key", name: "Gemini key", buildDefault: true },
      ],
    });
    render(<TasksSection s={s} config={{}} />);
    await user.click(screen.getByRole("button", { name: "Add to defaults" }));
    expect(s.saveChoice).toHaveBeenCalledWith({
      defaultCapabilities: ["gcp-key", "gemini-key"],
    });
  });

  it("lists the deployment's task fields and the tasks a field strands", async () => {
    const user = userEvent.setup();
    const onShow = vi.fn();
    render(
      <TasksSection
        s={section({ capabilities: [] })}
        config={{
          customFields: [
            { name: "tier", label: "Tier", type: "enum", options: ["gold"] },
          ],
        }}
        tasks={[{ customFields: { tier: "silver" } }]}
        onShowStrandedValues={onShow}
      />,
    );
    expect(screen.getByText("Tier")).toBeVisible();
    await user.click(
      screen.getByRole("button", {
        name: "1 task still carries a value this field no longer offers",
      }),
    );
    expect(onShow).toHaveBeenCalledWith("tier");
  });

  // Default capabilities hold capability server grants and api:* grants
  // as well as the legacy ids: the legacy picker shows only the latter,
  // and GrantsPicker the rest, each save keeping the other's entries.
  it("edits default method and api grants with GrantsPicker, keeping the legacy ids", async () => {
    resetCapabilityMethodsForTest();
    serveMethods();
    const user = userEvent.setup();
    const held = cloudGrant({ account: "staging", ttl: 60 });
    const s = section({
      defaultCapabilities: ["gcp-key", "api:tasks:read", held],
      capabilities: [{ id: "gcp-key", name: "GCP key" }],
    });
    render(
      <TasksSection
        s={s}
        config={{
          capabilityMethods: 1,
          capabilities: [{ id: "api:tasks:read", name: "Read tasks" }],
        }}
      />,
    );
    const legacy = document.querySelector(
      '[data-setting-id="defaultCapabilities"]',
    );
    expect(within(legacy).getByText("GCP key")).toBeInTheDocument();
    expect(legacy).not.toHaveTextContent("api:tasks:read");
    expect(legacy).not.toHaveTextContent("cloud-key");
    expect(legacy).toHaveTextContent("Default legacy capabilities");

    const grants = screen.getByTestId("default-grants");
    expect(within(grants).getByLabelText("api:tasks:read")).toBeInTheDocument();
    const sectionEl = await waitFor(() => {
      const el = grants.querySelector(`[data-grant='${held}']`);
      expect(el?.querySelector("input")).toBeTruthy();
      return el;
    });
    const ttl = within(sectionEl).getByLabelText(/^ttl/);
    await user.clear(ttl);
    await user.type(ttl, "90");
    await waitFor(() =>
      expect(s.saveChoice).toHaveBeenCalledWith({
        defaultCapabilities: [
          "gcp-key",
          "api:tasks:read",
          cloudGrant({ account: "staging", ttl: 90 }),
        ],
      }),
    );

    await user.click(
      within(grants).getByRole("button", { name: /^Remove api:tasks:read/ }),
    );
    expect(s.saveChoice).toHaveBeenLastCalledWith({
      defaultCapabilities: ["gcp-key", held],
    });
  });

  // docs/design/config-repo-settings.md §7.4: under the config repo the
  // defaults are settings/capabilities.yml's, listed as chips beside a
  // link to it rather than edited with a GrantsPicker.
  it("lists default method and api grants as read-only chips linked to settings/capabilities.yml", async () => {
    resetCapabilityMethodsForTest();
    serveMethods();
    const held = cloudGrant({ account: "staging", ttl: 60 });
    const s = section({
      defaultCapabilities: ["gcp-key", "api:tasks:read", held],
      capabilities: [{ id: "gcp-key", name: "GCP key" }],
    });
    render(
      <TasksSection
        s={{ ...s, readOnly: true }}
        config={{
          capabilityMethods: 1,
          capabilities: [{ id: "api:tasks:read", name: "Read tasks" }],
        }}
      />,
    );
    expect(screen.queryByTestId("default-grants")).toBeInTheDocument();
    const grants = screen.getByTestId("default-grants-list");
    expect(grants.querySelectorAll("[data-grant-chip]")).toHaveLength(2);
    expect(grants.querySelector("input")).toBeNull();
    expect(
      within(grants).queryByRole("button", { name: /^Remove/ }),
    ).toBeNull();
    expect(within(grants).getByTestId("config-repo-link")).toHaveAttribute(
      "href",
      "/config?path=settings%2Fcapabilities.yml",
    );
    expect(s.saveChoice).not.toHaveBeenCalled();
  });
});
