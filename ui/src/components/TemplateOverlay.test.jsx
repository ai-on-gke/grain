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

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import TemplateOverlay from "./TemplateOverlay.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const saved = {
  id: "template-1",
  name: "Dependency bump",
  title: "Bump dependencies",
  description: "",
  repo: "",
  autoMerge: false,
  reads: [],
  writes: [],
  capabilities: [],
  priority: 7,
  revision: 3,
  createdAt: "2026-01-01T00:00:00Z",
};

function overlay(template) {
  const onSaved = vi.fn(async () => {});
  render(
    <TemplateOverlay
      template={template}
      config={{ actor: "mem" }}
      onClose={() => {}}
      onSaved={onSaved}
      showError={(err) => {
        throw err;
      }}
    />,
  );
  if (!template) {
    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
      target: { value: "Dependency bump" },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Task title" }), {
      target: { value: "Bump dependencies" },
    });
  }
  return onSaved;
}

// Submitted straight through the form rather than by clicking Save, so
// what is checked is the form's own reading of the box, not the
// browser's min and step.
function submitWithPriority(value) {
  const box = screen.getByRole("spinbutton", { name: "Task priority" });
  fireEvent.change(box, { target: { value } });
  fireEvent.submit(box.closest("form"));
}

function sentBody(method) {
  const call = api.mock.calls.find(([, opts]) => opts?.method === method);
  return call && JSON.parse(call[1].body);
}

describe("TemplateOverlay task priority", () => {
  afterEach(() => api.mockReset());

  it("leaves a blank priority out of a new template", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay(null);
    submitWithPriority("");
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("POST")).not.toHaveProperty("priority");
  });

  it("clears a saved priority with 0 when the box is emptied on an edit", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay(saved);
    submitWithPriority("");
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("PATCH")).toMatchObject({ priority: 0, revision: 3 });
  });

  it("sends a whole number from 1 up as it was typed", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay(null);
    submitWithPriority("100");
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("POST")).toMatchObject({ priority: 100 });
  });

  // As New task, Edit, Bulk edit, propose_task and the API read it: a
  // whole number written with an exponent is that number.
  it("sends 1e2 as the 100 the box takes it for", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay(null);
    submitWithPriority("1e2");
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("POST")).toMatchObject({ priority: 100 });
  });

  it.each(["0", "-1", "1.5", "1e-1", "1e20"])(
    "refuses %s rather than saving some other priority",
    async (value) => {
      api.mockResolvedValue(saved);
      overlay(saved);
      submitWithPriority(value);
      expect(
        await screen.findByText(
          "a whole number, 1 or more; default 5, higher numbers run first",
        ),
      ).toBeInTheDocument();
      expect(api).not.toHaveBeenCalled();

      // Editing the box again takes the refusal back down.
      fireEvent.change(
        screen.getByRole("spinbutton", { name: "Task priority" }),
        { target: { value: "8" } },
      );
      expect(
        screen.queryByText(
          "a whole number, 1 or more; default 5, higher numbers run first",
        ),
      ).not.toBeInTheDocument();
    },
  );
});

describe("TemplateOverlay hostd", () => {
  afterEach(() => api.mockReset());

  it("offers no hostd and sends none: a template's tasks have no hostd", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay(null);
    expect(screen.queryByRole("textbox", { name: "Host" })).toBeNull();
    fireEvent.submit(screen.getAllByRole("textbox")[0].closest("form"));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("POST")).not.toHaveProperty("hostd");
  });
});

// Keep running sits beside Interactive (2026-11-03 walkthrough: the API
// took keepRunning on a template, but the form had no box for it).
describe("TemplateOverlay keep running", () => {
  afterEach(() => api.mockReset());

  it("sends Keep running on a new template", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay(null);
    fireEvent.click(screen.getByRole("checkbox", { name: "Keep running" }));
    fireEvent.submit(
      screen.getByRole("textbox", { name: "Name" }).closest("form"),
    );
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("POST")).toMatchObject({ keepRunning: true });
  });

  it("reads a saved Keep running back and clears it on an edit", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay({ ...saved, keepRunning: true });
    const box = screen.getByRole("checkbox", { name: "Keep running" });
    expect(box).toBeChecked();
    fireEvent.click(box);
    fireEvent.submit(box.closest("form"));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("PATCH")).toMatchObject({ keepRunning: false });
  });
});

// Admission (model.Template.Admission) is a column on templates as on
// tasks, and a template that declared one showed nowhere in this form
// (2026-11-07 walkthrough).
describe("TemplateOverlay admission", () => {
  afterEach(() => api.mockReset());

  it("sends no admission on a new template left standard", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay(null);
    fireEvent.submit(
      screen.getByRole("textbox", { name: "Name" }).closest("form"),
    );
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("POST")).not.toHaveProperty("admission");
  });

  it("reads a saved admission back and sends a change on an edit", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay({ ...saved, admission: "bypass" });
    const select = screen.getByRole("combobox", { name: "Admission" });
    expect(select).toHaveTextContent("Bypass queue");
    fireEvent.mouseDown(select);
    fireEvent.click(await screen.findByRole("option", { name: "Durable" }));
    fireEvent.submit(
      screen.getByRole("textbox", { name: "Name" }).closest("form"),
    );
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("PATCH")).toMatchObject({ admission: "durable" });
  });

  it("does not resend an unchanged admission", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay({ ...saved, admission: "durable" });
    fireEvent.submit(
      screen.getByRole("textbox", { name: "Name" }).closest("form"),
    );
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("PATCH")).not.toHaveProperty("admission");
  });
});

// Hosting (model.Template.Hosting) is a column on templates as on tasks,
// the setting that replaced the console capability
// (design/design/unified-grants.design.md §4.1).
describe("TemplateOverlay hosting", () => {
  afterEach(() => api.mockReset());

  it("sends no hosting on a new template left unattended", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay(null);
    fireEvent.submit(
      screen.getByRole("textbox", { name: "Name" }).closest("form"),
    );
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("POST")).not.toHaveProperty("hosting");
  });

  it("reads a saved hosting back and sends a change on an edit", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay({ ...saved, hosting: "console" });
    const select = screen.getByRole("combobox", { name: "Hosting" });
    expect(select).toHaveTextContent("Interactive console");
    fireEvent.mouseDown(select);
    fireEvent.click(await screen.findByRole("option", { name: "Unattended" }));
    fireEvent.submit(
      screen.getByRole("textbox", { name: "Name" }).closest("form"),
    );
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("PATCH")).toMatchObject({ hosting: "unattended" });
  });

  it("does not resend an unchanged hosting", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay({ ...saved, hosting: "console" });
    fireEvent.submit(
      screen.getByRole("textbox", { name: "Name" }).closest("form"),
    );
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("PATCH")).not.toHaveProperty("hosting");
  });
});

// The sandbox shape every task the template files asks for
// (ui.Template.sandbox, grain/task-4591).
describe("TemplateOverlay sandbox shape", () => {
  afterEach(() => api.mockReset());

  it("shows a saved shape in its boxes", () => {
    overlay({ ...saved, sandbox: { cpus: 4, diskGb: 100 } });
    expect(
      screen.getByRole("spinbutton", { name: "Sandbox vCPUs" }),
    ).toHaveValue(4);
    expect(
      screen.getByRole("spinbutton", { name: "Sandbox memory (MiB)" }),
    ).toHaveValue(null);
    expect(
      screen.getByRole("spinbutton", { name: "Sandbox disk (GiB)" }),
    ).toHaveValue(100);
  });

  it("leaves an unsized new template's shape out", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay(null);
    submitWithPriority("");
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("POST")).not.toHaveProperty("sandbox");
  });

  it("sends the dimensions that are filled in", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay(null);
    fireEvent.change(
      screen.getByRole("spinbutton", { name: "Sandbox disk (GiB)" }),
      { target: { value: "100" } },
    );
    submitWithPriority("");
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("POST").sandbox).toEqual({ diskGb: 100 });
  });

  it("clears a saved shape with {} when every box is emptied", async () => {
    api.mockResolvedValue(saved);
    const onSaved = overlay({ ...saved, sandbox: { diskGb: 100 } });
    fireEvent.change(
      screen.getByRole("spinbutton", { name: "Sandbox disk (GiB)" }),
      { target: { value: "" } },
    );
    submitWithPriority("");
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sentBody("PATCH").sandbox).toEqual({});
  });
});
