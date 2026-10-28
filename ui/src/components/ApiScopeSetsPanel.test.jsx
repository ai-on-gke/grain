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
});
