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

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import ApiGrantPicker from "./ApiGrantPicker.jsx";
import { optionValues } from "../searchSelectTesting.js";
import {
  apiShortName,
  groupApiRows,
  isApiGrant,
  splitApiGrant,
  storedApiGrant,
  withoutApiRows,
} from "../apiGrants.js";

const config = {
  targetRepos: ["acme/widgets"],
  capabilities: [
    { id: "gcp-key", name: "GCP key", description: "a key" },
    {
      id: "api:task-admin",
      name: "API preset: task-admin",
      description: "Edit tasks (tasks:read, tasks:write)",
      summary: "Edit tasks",
      preset: true,
      scopes: ["tasks:read", "tasks:write"],
    },
    {
      id: "api:tasks:read",
      name: "API: tasks:read",
      description: "Read tasks",
      summary: "Read tasks",
      scopes: ["tasks:read"],
    },
    {
      id: "api:tasks:admin",
      name: "API: tasks:admin (dangerous)",
      description: "Dangerous -- approve tasks",
      summary: "Approve tasks",
      scopes: ["tasks:admin"],
      dangerous: true,
    },
    {
      id: "api:ci:read",
      name: "API: ci:read",
      description: "Read CI",
      summary: "Read CI",
      scopes: ["ci:read"],
    },
  ],
};

function open() {
  fireEvent.click(screen.getByText("Grain API"));
}

describe("apiGrants", () => {
  it("groups presets first and scopes by area, and keeps them out of the flat list", () => {
    const { presets, areas } = groupApiRows(config.capabilities);
    expect(presets.map((r) => r.id)).toEqual(["api:task-admin"]);
    expect(areas.map((a) => a.area)).toEqual(["tasks", "ci"]);
    expect(withoutApiRows(config.capabilities).map((r) => r.id)).toEqual([
      "gcp-key",
    ]);
  });

  it("reads https://grain.dev/capabilities/api#<scope> references alongside api:<scope>", () => {
    expect(isApiGrant("api:ci:write")).toBe(true);
    expect(isApiGrant("api:ci:write@acme/widgets")).toBe(true);
    expect(isApiGrant("https://grain.dev/capabilities/api#ci:write")).toBe(
      true,
    );
    expect(
      isApiGrant(
        'https://grain.dev/capabilities/api#ci:write={"repo":"acme/widgets"}',
      ),
    ).toBe(true);
    expect(isApiGrant("https://grain.dev/capabilities/api")).toBe(false);
    expect(isApiGrant("https://grain.dev/capabilities/gcp#gcp-key")).toBe(
      false,
    );
    expect(isApiGrant("gcp-key")).toBe(false);

    expect(
      splitApiGrant("https://grain.dev/capabilities/api#ci:write"),
    ).toEqual(["api:ci:write", ""]);
    expect(
      splitApiGrant("https://grain.dev/capabilities/api#ci:write={}"),
    ).toEqual(["api:ci:write", ""]);
    expect(
      splitApiGrant(
        'https://grain.dev/capabilities/api#ci:write={"repo":"acme/widgets"}',
      ),
    ).toEqual(["api:ci:write", "acme/widgets"]);
    expect(
      splitApiGrant(
        'https://grain.dev/capabilities/api#ci:write={"repo":"a/b","extra":1}',
      ),
    ).toEqual([
      'https://grain.dev/capabilities/api#ci:write={"repo":"a/b","extra":1}',
      "",
    ]);
    expect(
      splitApiGrant("https://grain.dev/capabilities/api#ci:write=not-json"),
    ).toEqual(["https://grain.dev/capabilities/api#ci:write=not-json", ""]);

    expect(apiShortName("https://grain.dev/capabilities/api#ci:write")).toBe(
      "ci:write",
    );
    expect(
      apiShortName(
        'https://grain.dev/capabilities/api#ci:write={"repo":"acme/widgets"}',
      ),
    ).toBe("ci:write");
    expect(apiShortName("https://grain.dev/capabilities/api#task-admin")).toBe(
      "task-admin",
    );
    expect(
      apiShortName('https://grain.dev/capabilities/api#ci:write={"bogus":1}'),
    ).toBe('ci:write={"bogus":1}');

    expect(
      storedApiGrant(
        'https://grain.dev/capabilities/api#ci:write={"repo":"acme/widgets"}',
      ),
    ).toBe("api:ci:write@acme/widgets");
    expect(
      withoutApiRows([
        { id: "gcp-key" },
        { id: "https://grain.dev/capabilities/api#ci:read" },
        { id: "api:tasks:read" },
      ]).map((r) => r.id),
    ).toEqual(["gcp-key"]);
  });
});

describe("ApiGrantPicker", () => {
  it("shows what a preset expands to and flags dangerous scopes", () => {
    render(<ApiGrantPicker config={config} onChange={() => {}} idPrefix="t" />);
    open();
    const presets = screen.getByRole("group", { name: "API presets" });
    const grants = within(presets).getByLabelText("task-admin grants");
    expect(within(grants).getByText("tasks:write")).toBeTruthy();
    const tasks = screen.getByRole("group", { name: "API scopes: tasks" });
    const admin = tasks.querySelector('[data-api-grant="api:tasks:admin"]');
    expect(within(admin).getByText("dangerous")).toBeTruthy();
    const read = tasks.querySelector('[data-api-grant="api:tasks:read"]');
    expect(within(read).queryByText("dangerous")).toBeNull();
  });

  it("writes a repo-limited grant once a repository is picked", () => {
    const onChange = vi.fn();
    render(
      <ApiGrantPicker
        config={config}
        selected={["api:ci:read"]}
        onChange={onChange}
        idPrefix="t"
      />,
    );
    open();
    fireEvent.mouseDown(screen.getByLabelText("Limit what you tick to"));
    fireEvent.click(screen.getByRole("option", { name: "acme/widgets" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "tasks:read" }));
    expect(onChange).toHaveBeenLastCalledWith([
      "api:ci:read",
      "api:tasks:read@acme/widgets",
    ]);
  });

  // grain/task-2910: the repository list grows with the deployment, so
  // it is searched: a word from the middle of a name finds it.
  it("finds the repository to limit to by any part of its name", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <ApiGrantPicker
        config={{
          ...config,
          targetRepos: ["acme/widgets", "acme/gadgets", "other/widgets-docs"],
        }}
        selected={[]}
        onChange={onChange}
        idPrefix="t"
      />,
    );
    open();
    const box = screen.getByRole("combobox", {
      name: "Limit what you tick to",
    });
    await user.click(box);
    await user.clear(box);
    await user.type(box, "gadg");
    expect(optionValues(box)).toEqual(["acme/gadgets"]);
    await user.keyboard("{Enter}");
    await user.click(screen.getByRole("checkbox", { name: "tasks:read" }));
    expect(onChange).toHaveBeenLastCalledWith(["api:tasks:read@acme/gadgets"]);
  });

  it("removes a held grant from its chip, a retired one included", () => {
    const onChange = vi.fn();
    render(
      <ApiGrantPicker
        config={config}
        selected={["api:ci:read", "api:tasks:fly"]}
        onChange={onChange}
        idPrefix="t"
      />,
    );
    expect(screen.getByText("2 granted")).toBeTruthy();
    open();
    const chip = screen.getByText("tasks:fly").closest(".MuiChip-root");
    fireEvent.click(chip.querySelector(".MuiChip-deleteIcon"));
    expect(onChange).toHaveBeenLastCalledWith(["api:ci:read"]);
  });

  it("displays and unticks held https://grain.dev/capabilities/api# grants", () => {
    const onChange = vi.fn();
    render(
      <ApiGrantPicker
        config={config}
        selected={[
          "https://grain.dev/capabilities/api#tasks:admin",
          'https://grain.dev/capabilities/api#ci:read={"repo":"acme/widgets"}',
          'https://grain.dev/capabilities/api#ci:read={"bogus":true}',
        ]}
        onChange={onChange}
        idPrefix="t"
      />,
    );
    expect(screen.getByText("3 granted")).toBeTruthy();
    open();
    expect(screen.getByText("ci:read on acme/widgets")).toBeTruthy();
    expect(screen.getByRole("checkbox", { name: "tasks:admin" }).checked).toBe(
      true,
    );
    fireEvent.click(screen.getByRole("checkbox", { name: "tasks:admin" }));
    expect(onChange).toHaveBeenLastCalledWith([
      'https://grain.dev/capabilities/api#ci:read={"repo":"acme/widgets"}',
      'https://grain.dev/capabilities/api#ci:read={"bogus":true}',
    ]);

    const invalidChip = screen
      .getByText('ci:read={"bogus":true}')
      .closest(".MuiChip-root");
    fireEvent.click(invalidChip.querySelector(".MuiChip-deleteIcon"));
    expect(onChange).toHaveBeenLastCalledWith([
      "https://grain.dev/capabilities/api#tasks:admin",
      'https://grain.dev/capabilities/api#ci:read={"repo":"acme/widgets"}',
    ]);
  });
});
