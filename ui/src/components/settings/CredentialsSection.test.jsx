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

import { render, screen, within } from "@testing-library/react";
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

    const deploymentWide = screen.getByTestId(
      "legacy-capabilities-deployment-wide",
    );
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

    expect(await screen.findByText("Other secrets")).toBeInTheDocument();
    expect(container.querySelector("#other-secrets")).not.toBeNull();
  });
});
