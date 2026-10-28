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
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import AccessSection, { actorName } from "./AccessSection.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

// Settings → Access says how this browser is signed in and offers the
// way out of it (docs/design/settings-panel.md §3.2). grain has no users
// (§6.1): there is no Username box, and the only name shown is the actor
// the server reports for an IAP sign-in.
const section = (props = {}) => (
  <AccessSection showError={() => {}} {...props} />
);

describe("AccessSection", () => {
  beforeEach(() => {
    api.mockResolvedValue({});
  });
  afterEach(() => {
    api.mockReset();
    localStorage.clear();
  });

  it("says the access code signed this browser in, and logs out", async () => {
    const user = userEvent.setup();
    const onLogout = vi.fn();
    render(
      section({
        config: { authenticated: true, actor: "alice", authMethod: "header" },
        onLogout,
      }),
    );

    expect(
      screen.getByText("Signed in with the access code."),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Change password" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Log out" }));
    expect(onLogout).toHaveBeenCalled();
  });

  it("names the server-reported IAP actor, without its prefix, and offers no log out", () => {
    localStorage.setItem("grain.username", "alice");
    render(
      section({
        config: {
          authenticated: true,
          actor: "user:iap@example.com",
          authMethod: "iap",
        },
      }),
    );

    expect(
      screen.getByText(/Signed in through IAP as iap@example\.com\./),
    ).toBeInTheDocument();
    expect(screen.queryByText(/alice/)).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Log out" }),
    ).not.toBeInTheDocument();
  });

  it("has no Username box, whatever this browser once stored", () => {
    localStorage.setItem("grain.username", "alice");
    for (const config of [
      {},
      { authenticated: false, actor: "debian" },
      { authenticated: true, actor: "alice" },
    ]) {
      const { unmount } = render(section({ config }));
      expect(screen.queryByLabelText("Username")).toBeNull();
      expect(screen.queryByText("alice")).toBeNull();
      unmount();
    }
  });
});

describe("actorName", () => {
  it("strips the user: kind and reads either shape of actor", () => {
    expect(actorName({ actor: "user:ann@example.com" })).toBe(
      "ann@example.com",
    );
    expect(actorName({ actor: { id: "user:ann" } })).toBe("ann");
    expect(actorName({ actor: "" })).toBe("");
    expect(actorName(null)).toBe("");
  });
});
