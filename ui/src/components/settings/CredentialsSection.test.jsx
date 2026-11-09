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
import { afterEach, describe, expect, it, vi } from "vitest";
import api from "../../api.js";
import CredentialsSection from "./CredentialsSection.jsx";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

function makeSectionState(overrides = {}) {
  return {
    settings: {
      capabilities: [
        {
          id: "self-debug",
          name: "Self debug",
          description: "Read grain's own source",
          ready: true,
        },
      ],
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
    onSaved: vi.fn(),
    readOnly: false,
    configLocked: false,
    ...overrides,
  };
}

describe("CredentialsSection GCP help", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("points GCP settings and minter secret help at the gcp capability server and Other secrets rather than retired cards", async () => {
    api.mockResolvedValue({ enabled: true, secrets: [] });

    const { container } = render(
      <CredentialsSection s={makeSectionState()} claimedSecrets={[]} />,
    );

    const gcpServerHref =
      "/settings/capabilities/https%3A%2F%2Fgrain.dev%2Fcapabilities%2Fgcp";

    const deploymentWide = screen.getByTestId("deployment-credentials");
    expect(deploymentWide).not.toHaveTextContent(
      /GCP key capability's page, in the list above/i,
    );
    expect(deploymentWide).not.toHaveTextContent(
      /Either capability is not ready until it is set/i,
    );
    expect(deploymentWide).not.toHaveTextContent(
      /more than one capability above reads[\s\S]*gcp-key/i,
    );

    const leadLink = within(deploymentWide).getByRole("link", {
      name: /gcp capability server's/i,
    });
    expect(leadLink).toHaveAttribute("href", gcpServerHref);

    const projectField = container.querySelector(
      '[data-setting-id="gcpProject"]',
    );
    expect(projectField).toHaveTextContent(/gcp-key/);
    expect(projectField).toHaveTextContent(/gemini-key/);
    expect(
      within(projectField).getByRole("link", { name: "gcp capability server" }),
    ).toHaveAttribute("href", gcpServerHref);

    const emailField = container.querySelector(
      '[data-setting-id="gcpServiceAccountEmail"]',
    );
    expect(emailField).toHaveTextContent(/gcp-key-minter/);
    expect(emailField).toHaveTextContent(/gcp-key-minter-<name>/);
    expect(
      within(emailField).getByRole("link", { name: "gcp capability server" }),
    ).toHaveAttribute("href", gcpServerHref);
    expect(
      within(emailField).getByRole("link", { name: "Other secrets" }),
    ).toHaveAttribute("href", "#other-secrets");

    await waitFor(() =>
      expect(container.querySelector("#other-secrets")).not.toBeNull(),
    );
    expect(
      within(container.querySelector("#other-secrets")).getByText(
        "Other secrets",
      ),
    ).toBeInTheDocument();
  });

  it("wraps GitHub and GCP config fields in ReadOnlyFieldset when settings are locked while leaving secrets editable", async () => {
    api.mockResolvedValue({ enabled: true, secrets: [] });

    const { container } = render(
      <CredentialsSection
        s={makeSectionState({ configLocked: true })}
        claimedSecrets={[]}
      />,
    );

    const insecureCheckbox = container.querySelector(
      'input[name="githubInsecureHttp"]',
    );
    expect(insecureCheckbox).toBeDisabled();
    expect(insecureCheckbox.closest("[data-settings-readonly]")).not.toBeNull();
    expect(
      container
        .querySelector('input[name="githubHost"]')
        .closest("[data-settings-readonly]"),
    ).not.toBeNull();

    await waitFor(() =>
      expect(container.querySelector("#other-secrets")).not.toBeNull(),
    );
    expect(
      container
        .querySelector("#other-secrets")
        .closest("[data-settings-readonly]"),
    ).toBeNull();
  });
});

// 2026-11-08 deployment credentials walkthrough: the card follows
// Capabilities' capabilities:admin, but its writes go to PUT /api/settings
// (settings:write) and /api/secrets (secrets:write). A role holding the
// first without the others was offered fields and buttons the server
// refused with 403.
describe("CredentialsSection role gating", () => {
  afterEach(() => {
    api.mockReset();
  });

  const secrets = {
    enabled: true,
    secrets: [{ name: "github-app", keys: ["app-id", "private-key"] }],
  };

  it("draws the GitHub host and GCP fields read-only, saying why, for a role without settings:write", async () => {
    api.mockResolvedValue(secrets);
    const { container } = render(
      <CredentialsSection
        s={makeSectionState({
          settingsWriteRefusal:
            "Needs settings:write, which your role capadmin does not grant.",
        })}
        claimedSecrets={[]}
      />,
    );
    await screen.findByText("github-app");
    for (const name of ["githubHost", "gcpProject", "gcpServiceAccountEmail"]) {
      expect(container.querySelector(`input[name=${name}]`)).toBeDisabled();
    }
    expect(screen.getByTestId("credentials-read-only")).toHaveTextContent(
      /Needs settings:write, which your role capadmin does not grant/,
    );
    // secrets:write is held, so the secrets stay usable.
    expect(screen.getByRole("button", { name: "Set secret" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Delete secret" })).toBeEnabled();
  });

  it("draws the secrets list and form read-only for a role without secrets:write", async () => {
    api.mockResolvedValue(secrets);
    const { container } = render(
      <CredentialsSection
        s={makeSectionState({
          secretsWriteRefusal:
            "Needs secrets:write, which your role capadmin does not grant.",
        })}
        claimedSecrets={[]}
      />,
    );
    await screen.findByText("github-app");
    expect(screen.getByRole("button", { name: "Set secret" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Delete secret" }),
    ).toBeDisabled();
    expect(container.querySelector(".MuiChip-deletable")).toBeNull();
    expect(screen.getByTestId("credentials-read-only")).toHaveTextContent(
      /Needs secrets:write/,
    );
    expect(container.querySelector("input[name=githubHost]")).toBeEnabled();
  });

  // F5 of the same walkthrough: the page's s.readOnly folds in
  // Capabilities' capabilities:admin, which this card does not follow. A
  // role holding settings:write and secrets:write without it (secretsonly)
  // had the whole card read-only though every write returned 200.
  it("stays editable for a role refused only capabilities:admin", async () => {
    api.mockResolvedValue(secrets);
    const { container } = render(
      <CredentialsSection
        s={makeSectionState({ readOnly: true, readOnlyForAnyRole: false })}
        claimedSecrets={[]}
      />,
    );
    await screen.findByText("github-app");
    for (const name of ["githubHost", "gcpProject", "gcpServiceAccountEmail"]) {
      expect(container.querySelector(`input[name=${name}]`)).toBeEnabled();
    }
    expect(screen.queryByTestId("credentials-read-only")).toBeNull();
    expect(screen.getByRole("button", { name: "Set secret" })).toBeEnabled();
    expect(screen.getByRole("button", { name: "Delete secret" })).toBeEnabled();
  });

  it("still holds the fields read-only where every role is refused them", async () => {
    api.mockResolvedValue(secrets);
    const { container } = render(
      <CredentialsSection
        s={makeSectionState({ readOnly: true, readOnlyForAnyRole: true })}
        claimedSecrets={[]}
      />,
    );
    await screen.findByText("github-app");
    expect(container.querySelector("input[name=githubHost]")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Set secret" })).toBeEnabled();
  });

  it("says nothing extra when the role holds both write scopes", async () => {
    api.mockResolvedValue(secrets);
    render(<CredentialsSection s={makeSectionState()} claimedSecrets={[]} />);
    await screen.findByText("github-app");
    expect(screen.queryByTestId("credentials-read-only")).toBeNull();
    expect(screen.getByRole("button", { name: "Set secret" })).toBeEnabled();
  });
});
