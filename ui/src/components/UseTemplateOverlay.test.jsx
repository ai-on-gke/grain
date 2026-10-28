import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import UseTemplateOverlay from "./UseTemplateOverlay.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const template = {
  id: "template-1",
  name: "Dependency bump",
  title: "Bump dependencies",
  description: "",
  repo: "acme/widgets",
  autoMerge: true,
  reads: [],
  capabilities: [],
  createdAt: "2026-01-01T00:00:00Z",
};

function overlay(config) {
  return render(
    <UseTemplateOverlay
      templates={[template]}
      templateId={template.id}
      config={config}
      onClose={() => {}}
      onCreated={async () => {}}
      showError={(err) => {
        throw err;
      }}
    />,
  );
}

function filedBody() {
  const call = api.mock.calls.find(
    ([path, opts]) => path === "/api/tasks" && opts?.method === "POST",
  );
  return call && JSON.parse(call[1].body);
}

describe("UseTemplateOverlay", () => {
  afterEach(() => api.mockReset());

  it("offers the Approved box, ticked when the deployment approves by default", async () => {
    api.mockResolvedValue({});
    overlay({ actor: "mem", approvedByDefault: true });
    expect(screen.getByRole("checkbox", { name: /^Approved/ })).toBeChecked();
    await userEvent.click(screen.getByRole("button", { name: "Create task" }));
    expect(filedBody()).toMatchObject({
      templateId: "template-1",
      approved: true,
    });
  });
});
