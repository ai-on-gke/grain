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

// A registered non-git change target choice in the repo picker: offered
// only where the binary registers the change target kind, and filed as
// one -- no repo, no branch, never auto-merged.
//
// Part of the NewTaskOverlay suite -- see NewTaskOverlay.test.jsx.

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import NewTaskOverlay from "./NewTaskOverlay.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));

function renderForm(config) {
  return render(
    <NewTaskOverlay
      tasks={[]}
      config={config}
      onClose={() => {}}
      onCreated={() => Promise.resolve()}
      showError={() => {}}
    />,
  );
}

describe("NewTaskOverlay changeTargets", () => {
  afterEach(() => api.mockClear());

  it("does not offer a change target where the binary has not registered it", () => {
    renderForm({ capabilities: [], targetRepos: ["acme/widgets"] });
    expect(
      screen.queryByRole("option", { name: /changelist in a/ }),
    ).not.toBeInTheDocument();
  });

  it("files a change target pick with no repo, branch or auto-merge", async () => {
    const user = userEvent.setup();
    renderForm({
      capabilities: [],
      targetRepos: ["acme/widgets"],
      changeTargets: ["monorepo"],
      autoMergeByDefault: true,
    });

    await user.type(screen.getByLabelText(/Title/), "Fix the frobnicator");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "monorepo");
    expect(screen.getByTestId("change-target-note")).toHaveTextContent(
      "monorepo changelist in this task's own workspace",
    );
    expect(
      screen.queryByRole("checkbox", { name: /Auto-merge/ }),
    ).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Base branch")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const post = api.mock.calls.find(([, opts]) => opts?.method === "POST");
    const payload = JSON.parse(post[1].body);
    expect(payload.changeTarget).toEqual({ kind: "monorepo" });
    expect(payload.noRepo).toBe(false);
    expect(payload.repo).toBe("");
    expect(payload.base).toBe("");
    expect(payload.autoMerge).toBe(false);
  });

  it("drops the change target choice when a repo is picked after it", async () => {
    const user = userEvent.setup();
    renderForm({
      capabilities: [],
      targetRepos: ["acme/widgets"],
      changeTargets: ["monorepo"],
    });

    await user.type(screen.getByLabelText(/Title/), "Back to git");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "monorepo");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const post = api.mock.calls.find(([, opts]) => opts?.method === "POST");
    const payload = JSON.parse(post[1].body);
    expect(payload.changeTarget).toBeUndefined();
    expect(payload.repo).toBe("acme/widgets");
  });
});
