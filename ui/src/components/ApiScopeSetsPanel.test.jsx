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
import ApiScopeSetsPanel from "./ApiScopeSetsPanel.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const BUILTIN = {
  name: "grain/ci-read",
  description: "Read grain CI runs, logs and artifacts",
  scopes: ["ci:read"],
  resolved: ["ci:read"],
  builtin: true,
  person: false,
  source: "builtin",
};
const TRIAGE = {
  name: "ops/triage",
  description: "",
  scopes: [],
  includes: ["grain/ci-read", "grain/task-admin"],
  repos: ["bwsalmon/grain"],
  deny: ["tasks:admin"],
  resolved: ["ci:read", "tasks:write"],
  repoLimits: {
    "ci:read": ["bwsalmon/grain"],
    "tasks:write": ["bwsalmon/grain"],
  },
  builtin: false,
  person: false,
  version: 1,
  source: "config",
  path: "tables/api_scope_set.json",
};

describe("ApiScopeSetsPanel", () => {
  afterEach(() => api.mockReset());

  it("lists built-in and config-repo sets with their resolved scopes and source", async () => {
    api.mockResolvedValue({
      sets: [
        BUILTIN,
        TRIAGE,
        {
          ...TRIAGE,
          name: "ops/keys",
          person: true,
          resolved: ["secrets:write"],
          repoLimits: null,
        },
      ],
      path: "tables/api_scope_set.json",
    });
    render(<ApiScopeSetsPanel showError={() => {}} />);
    const builtin = await screen.findByTestId("api-scope-set-grain/ci-read");
    expect(within(builtin).getByText("built-in")).toBeTruthy();
    expect(within(builtin).queryByRole("button", { name: "Edit" })).toBeNull();
    const triage = screen.getByTestId("api-scope-set-ops/triage");
    expect(
      within(triage).getByText("config repo · tables/api_scope_set.json"),
    ).toBeTruthy();
    expect(
      within(triage).getByText("tasks:write @ bwsalmon/grain"),
    ).toBeTruthy();
    expect(within(triage).queryByText("requires a person")).toBeNull();
    const keys = screen.getByTestId("api-scope-set-ops/keys");
    expect(within(keys).getByText("requires a person")).toBeTruthy();
  });

  it("says when the config repo's sets are not in effect", async () => {
    api.mockResolvedValue({
      sets: [BUILTIN],
      error: 'scope set "a": includes form a cycle: a -> a',
    });
    render(<ApiScopeSetsPanel showError={() => {}} />);
    expect(
      await screen.findByText(/previous ones stay in effect/),
    ).toBeTruthy();
  });

  it("writes a new set through PUT /api/api-scope-sets", async () => {
    const calls = [];
    api.mockImplementation(async (path, opts) => {
      calls.push([
        `${opts?.method || "GET"} ${path}`,
        opts?.body ? JSON.parse(opts.body) : undefined,
      ]);
      return { sets: [BUILTIN] };
    });
    render(<ApiScopeSetsPanel showError={() => {}} />);
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: "New scope set" }),
    );
    await user.type(screen.getByLabelText("Name"), "ops/triage");
    await user.type(
      screen.getByLabelText("Includes"),
      "grain/ci-read, grain/task-admin",
    );
    await user.type(screen.getByLabelText("Deny"), "tasks:admin");
    await user.click(screen.getByRole("button", { name: "Save" }));
    const put = calls.find(([what]) => what === "PUT /api/api-scope-sets");
    expect(put[1].set).toMatchObject({
      name: "ops/triage",
      includes: ["grain/ci-read", "grain/task-admin"],
      deny: ["tasks:admin"],
      scopes: [],
    });
  });

  it("names what holds a set before removing it, and Cancel keeps it", async () => {
    const calls = [];
    api.mockImplementation(async (path, opts) => {
      calls.push(`${opts?.method || "GET"} ${path}`);
      if (path === "/api/api-scope-sets/usage")
        return [
          { name: "grain/ci-read", holders: [] },
          {
            name: "ops/triage",
            holders: [
              {
                kind: "task",
                id: "42",
                name: "Triage nightly",
                grant: "api:ops/triage",
              },
              {
                kind: "template",
                id: "t1",
                name: "triage",
                grant: "api:ops/triage@bwsalmon/grain",
              },
            ],
          },
        ];
      return { sets: [BUILTIN, TRIAGE] };
    });
    render(<ApiScopeSetsPanel showError={() => {}} />);
    const user = userEvent.setup();
    const row = await screen.findByTestId("api-scope-set-ops/triage");
    await user.click(within(row).getByRole("button", { name: "Remove" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("Remove scope set ops/triage?"),
    ).toBeTruthy();
    expect(
      within(dialog).getByText(
        "2 grants hold api:ops/triage, and will grant nothing once it is removed:",
      ),
    ).toBeTruthy();
    expect(
      within(dialog).getByText("api:ops/triage@bwsalmon/grain"),
    ).toBeTruthy();
    expect(calls).not.toContain("POST /api/api-scope-sets/delete");

    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(calls).not.toContain("POST /api/api-scope-sets/delete");

    await user.click(within(row).getByRole("button", { name: "Remove" }));
    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "Remove",
      }),
    );
    expect(calls).toContain("POST /api/api-scope-sets/delete");
  });

  it("still confirms when the usage cannot be read", async () => {
    api.mockImplementation(async (path) => {
      if (path === "/api/api-scope-sets/usage") throw new Error("boom");
      return { sets: [BUILTIN, TRIAGE] };
    });
    render(<ApiScopeSetsPanel showError={() => {}} />);
    const user = userEvent.setup();
    const row = await screen.findByTestId("api-scope-set-ops/triage");
    await user.click(within(row).getByRole("button", { name: "Remove" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText(/could not count/)).toBeTruthy();
  });
});
