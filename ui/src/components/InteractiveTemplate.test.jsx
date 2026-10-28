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

import { useState } from "react";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import ProcessesList from "./ProcessesList.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// A template whose tasks are live chats (model.Template.Interactive).
const cluster = {
  id: "template-1",
  name: "GKE cluster",
  title: "Set up a GKE cluster",
  description: "",
  autoMerge: false,
  interactive: true,
  reads: [],
  capabilities: [],
  createdAt: "2026-01-01T00:00:00Z",
};

const noop = () => {};

// TemplateFields.test.jsx's own harness: the Processes page narrowed to
// templates, with its New button and each row's Use button.
function ControlledTemplatesList({ onRefresh, ...props }) {
  const [openTemplateId, setOpenTemplateId] = useState(null);
  return (
    <ProcessesList
      query={{ kinds: ["template"] }}
      openTemplateId={openTemplateId}
      onOpenTemplate={setOpenTemplateId}
      onRefreshTemplates={onRefresh}
      {...props}
    />
  );
}

function routeApi(onCreate) {
  api.mockImplementation((path, opts = {}) => {
    if (path.includes("/composition"))
      return Promise.resolve({ kind: "task", tasks: [] });
    if (path === "/api/tasks" && opts.method === "POST") {
      if (onCreate) onCreate(JSON.parse(opts.body));
      return Promise.resolve({ id: "7", interactive: true });
    }
    return Promise.resolve({});
  });
}

describe("an interactive template", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("is declared from the template form's Interactive box", async () => {
    routeApi();
    const user = userEvent.setup();
    render(
      <ControlledTemplatesList
        templates={[]}
        onRefresh={noop}
        showError={noop}
      />,
    );

    await user.click(screen.getByRole("button", { name: "+ New template" }));
    const box = screen.getByRole("checkbox", {
      name: "Interactive (agent can chat with you)",
    });
    expect(box).not.toBeChecked();
    await user.type(screen.getByLabelText(/Name/), "GKE cluster");
    await user.type(screen.getByLabelText(/Task title/), "Set up a cluster");
    await user.click(box);
    await user.click(screen.getByRole("button", { name: "Add template" }));

    const body = JSON.parse(api.mock.calls.at(-1)[1].body);
    expect(body.interactive).toBe(true);
  });

  it("files a chat from Use… and opens it", async () => {
    const filed = vi.fn();
    routeApi(filed);
    const onOpenTask = vi.fn();
    const user = userEvent.setup();
    render(
      <ControlledTemplatesList
        templates={[cluster]}
        approvedByDefault={false}
        onRefresh={noop}
        onRefreshTasks={noop}
        onOpenTask={onOpenTask}
        showError={noop}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Use…" }));
    // A chat is approved and queued at once, so there is no box to say
    // otherwise -- the form says what will happen instead, and asks the
    // composition preview for approved=true even when approvedByDefault is false.
    expect(
      screen.queryByRole("checkbox", { name: /^Approved/ }),
    ).not.toBeInTheDocument();
    expect(screen.getByText(/files a live chat/)).toBeInTheDocument();
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith(
        expect.stringMatching(
          /\/api\/templates\/template-1\/composition\?.*approved=true/,
        ),
      ),
    );

    await user.click(screen.getByRole("button", { name: "Create task" }));
    expect(
      api.mock.calls.some(
        ([path]) =>
          path.includes("/api/templates/template-1/composition?") &&
          path.includes("approved=true"),
      ),
    ).toBe(true);
    expect(filed).toHaveBeenCalledWith(
      expect.objectContaining({ templateId: "template-1", approved: true }),
    );
    // Straight into the new task, whose pane is its chat.
    await waitFor(() => expect(onOpenTask).toHaveBeenCalledWith("7"));
  });
});
