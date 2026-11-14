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

// The in-process providers the 2026-11-04 range retired kept their bare
// names as aliases for one release, and Settings -> Capabilities said so.
// Every promoted release since v3.3.0 carries each conversion, so the
// aliases and their notices are gone.
describe("CapabilitiesSection deprecation notices", () => {
  it("shows none", () => {
    const { container } = render(<CapabilitiesSection />);
    expect(
      container.querySelector('[data-testid$="-deprecation-notice"]'),
    ).toBeNull();
    expect(container.textContent).not.toMatch(/alias(es)? for one release/);
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
