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
import { describe, expect, it, vi } from "vitest";
import api from "../../api.js";
import CapabilitiesSection from "./CapabilitiesSection.jsx";
import { SettingsRoleRefusalContext } from "./SettingsCard.jsx";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

const panelProps = vi.hoisted(() => ({
  servers: null,
  policy: null,
  scopeSets: null,
}));

vi.mock("../CapabilityServersPanel.jsx", () => ({
  default: (props) => {
    panelProps.servers = props;
    return null;
  },
  serverPath: (id) => `/settings/capabilities/${encodeURIComponent(id)}`,
}));
vi.mock("../CapabilityPolicyEditor.jsx", () => ({
  default: (props) => {
    panelProps.policy = props;
    return null;
  },
}));
vi.mock("../ApiScopeSetsPanel.jsx", () => ({
  default: (props) => {
    panelProps.scopeSets = props;
    return null;
  },
}));

// Every in-process provider the 2026-11-04 range retired keeps its bare
// name as a one-release alias, and Settings -> Capabilities says so for
// each (docs/test-plans/2026-11-04.md §8). The release server's three
// methods and grain#ci had no notice
// (docs/test-runs/2026-11-04-capabilities.md, F3).
describe("CapabilitiesSection deprecation notices", () => {
  it.each([
    ["gcp-key-deprecation-notice", ["gcp-key", "gcp#gcp-key"]],
    [
      "model-proxy-deprecation-notice",
      ["claude-proxy", "model-proxy#model-proxy"],
    ],
    ["gemini-key-deprecation-notice", ["gemini-key", "gcp#gemini-key"]],
    [
      "release-deprecation-notice",
      [
        "image-signing",
        "registry-push",
        "package-publish",
        "https://grain.dev/capabilities/release",
        "release#<method>",
      ],
    ],
    [
      "grain-ci-deprecation-notice",
      ["grain-ci", "https://grain.dev/capabilities/grain#ci"],
    ],
    [
      "bootstrap-playbooks-deprecation-notice",
      ["bootstrap-playbooks", "config-map#config-map"],
    ],
    [
      "github-sandbox-deprecation-notice",
      [
        "github-sandbox",
        "https://grain.dev/capabilities/github#sandbox-repo",
        "github#sandbox-repo",
      ],
    ],
  ])("shows %s", (testId, texts) => {
    render(<CapabilitiesSection />);
    const notice = screen.getByTestId(testId);
    expect(notice.textContent).toMatch(/^Deprecated:/);
    expect(notice.textContent).toMatch(/alias(es)? for one release/);
    for (const text of texts) expect(notice.textContent).toContain(text);
  });
});

// 2026-11-08 deployment credentials walkthrough, F5: the Deployment
// credentials card writes through PUT /api/settings and /api/secrets, so
// Capabilities' own capabilities:admin refusal is not what holds it
// read-only -- its fields follow settings:write and its secrets
// secrets:write (CredentialsSection).
describe("CapabilitiesSection Deployment credentials gating", () => {
  const sectionState = (overrides = {}) => ({
    settings: {
      githubHost: "github.com",
      githubInsecureHttp: false,
      gcpProject: "",
      gcpServiceAccountEmail: "",
      gcpServiceAccountCount: 1,
      restartRequired: [],
    },
    saveChoice: vi.fn(),
    onTextBlur: () => vi.fn(),
    onIntBlur: () => vi.fn(),
    onEnterKey: vi.fn(),
    reloadSettings: vi.fn(),
    showError: vi.fn(),
    readOnly: true,
    readOnlyForAnyRole: false,
    ...overrides,
  });
  const renderRefused = (s) =>
    render(
      <SettingsRoleRefusalContext.Provider value="Needs capabilities:admin, which your role secretsonly does not grant.">
        <CapabilitiesSection s={s} claimedSecrets={[]} />
      </SettingsRoleRefusalContext.Provider>,
    );

  it("leaves the card editable to a role refused only capabilities:admin", async () => {
    api.mockResolvedValue({
      enabled: true,
      secrets: [{ name: "cosign-key", keys: ["key"] }],
    });
    const { container } = renderRefused(sectionState());
    await screen.findByText("cosign-key");
    const card = container.querySelector("#credentials");
    expect(card.querySelector("input[name=githubHost]")).toBeEnabled();
    expect(card.querySelector("fieldset[disabled]")).toBeNull();
    expect(screen.getByRole("button", { name: "Set secret" })).toBeEnabled();
    expect(screen.queryByTestId("credentials-read-only")).toBeNull();
    // The cards that do follow capabilities:admin stay read-only.
    expect(
      container.querySelector("#capability-policy fieldset[disabled]"),
    ).not.toBeNull();
  });

  it("holds the card to its own scopes where those are refused too", async () => {
    api.mockResolvedValue({
      enabled: true,
      secrets: [{ name: "cosign-key", keys: ["key"] }],
    });
    const { container } = renderRefused(
      sectionState({
        settingsWriteRefusal:
          "Needs settings:write, which your role viewer does not grant.",
        secretsWriteRefusal:
          "Needs secrets:write, which your role viewer does not grant.",
      }),
    );
    await screen.findByText("cosign-key");
    const card = container.querySelector("#credentials");
    expect(card.querySelector("input[name=githubHost]")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Set secret" })).toBeDisabled();
    expect(screen.getByTestId("credentials-read-only")).toHaveTextContent(
      /Needs settings:write.*Needs secrets:write/,
    );
  });

  // 2026-11-09 connections walkthrough: saving or removing an API scope set,
  // capability policy or capability server refreshes App's config (s.onSaved)
  // so GrantsPicker menus reflect added, edited and deleted entries without a
  // full page reload.
  it("forwards s.onSaved to ApiScopeSetsPanel, CapabilityPolicyEditor and CapabilityServersPanel", () => {
    const onSaved = vi.fn();
    render(
      <CapabilitiesSection s={sectionState({ onSaved })} claimedSecrets={[]} />,
    );
    expect(panelProps.scopeSets.onSaved).toBe(onSaved);
    expect(panelProps.policy.onSaved).toBe(onSaved);
    panelProps.servers.onChanged();
    expect(onSaved).toHaveBeenCalledTimes(1);
  });
});
