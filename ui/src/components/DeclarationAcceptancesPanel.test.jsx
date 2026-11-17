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

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import DeclarationAcceptancesPanel from "./DeclarationAcceptancesPanel.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const gcp = 'gcp#gcp-key={"permission_set":"ar-writer"}';
const widened = {
  id: "schedule:acme:widgets:publish",
  kind: "schedule",
  repo: "acme/widgets",
  requested: [gcp, 'host-script#run={"script":"deploy"}'],
  accepted: [gcp],
  acceptedBy: "alice@example.com",
  acceptedCommit: "0123456789abcdef",
  acceptedAt: "2026-11-09T09:00:00Z",
  awaiting: true,
  unaccepted: ['host-script#run={"script":"deploy"}'],
  needsScopes: ["capabilities:admin"],
};

describe("DeclarationAcceptancesPanel", () => {
  beforeEach(() => {
    api.mockReset();
  });

  it("shows a widened declaration as awaiting acceptance and accepts what it was shown", async () => {
    api.mockImplementation((path) =>
      Promise.resolve(path === "/api/declarations" ? [widened] : {}),
    );
    render(<DeclarationAcceptancesPanel showError={() => {}} />);
    expect(await screen.findByText("Awaiting acceptance")).toBeInTheDocument();
    expect(screen.getByText("(new since it was accepted)")).toBeInTheDocument();
    expect(
      screen.getByText(/Accepted by alice@example.com/),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Accept" }));
    expect(api).toHaveBeenCalledWith("/api/declarations/accept", {
      method: "POST",
      body: JSON.stringify({ id: widened.id, seen: widened.requested }),
    });
  });

  it("revokes an accepted declaration, and offers no Revoke on one nobody accepted", async () => {
    const accepted = {
      ...widened,
      requested: [gcp],
      awaiting: false,
      unaccepted: [],
    };
    const fresh = {
      ...widened,
      id: "ci:acme/widgets:publish",
      kind: "ci-job",
      accepted: undefined,
      acceptedBy: undefined,
      acceptedCommit: undefined,
      acceptedAt: undefined,
    };
    api.mockImplementation((path) =>
      Promise.resolve(path === "/api/declarations" ? [accepted, fresh] : {}),
    );
    render(<DeclarationAcceptancesPanel showError={() => {}} />);
    const revoke = await screen.findAllByRole("button", { name: "Revoke" });
    expect(revoke).toHaveLength(1);
    await userEvent.click(revoke[0]);
    expect(api).toHaveBeenCalledWith("/api/declarations/revoke", {
      method: "POST",
      body: JSON.stringify({ id: accepted.id }),
    });
  });

  it("says so when no declaration has asked for anything", async () => {
    api.mockResolvedValue([]);
    render(<DeclarationAcceptancesPanel showError={() => {}} />);
    expect(await screen.findByTestId("declarations-empty")).toBeInTheDocument();
  });
});
