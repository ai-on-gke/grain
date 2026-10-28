import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import ApiGrantPicker from "./ApiGrantPicker.jsx";
import { groupApiRows, withoutApiRows } from "../apiGrants.js";

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
});
