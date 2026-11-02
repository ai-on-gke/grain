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
// browser's min and step (which let "1e2" through regardless).
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

  it.each(["0", "-1", "1.5", "1e2"])(
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
