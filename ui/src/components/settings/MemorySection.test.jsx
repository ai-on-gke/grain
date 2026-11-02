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
import MemorySection from "./MemorySection.jsx";
import api from "../../api.js";
import { leavingLosesEdits } from "../../unsavedEdits.js";

vi.mock("../../api.js", () => ({ default: vi.fn() }));

const config = { targetRepos: ["acme/widgets"] };

// Every layer a deployment holds: its own and each repo's.
const memories = [
  { repo: "", text: "deployment standing note" },
  { repo: "acme/widgets", text: "widgets repo note" },
];
const documents = [
  { repo: "acme/widgets", name: "widgets-doc", summary: "widgets summary" },
  { name: "deployment-doc", summary: "deployment summary" },
];

const section = (props = {}) => (
  <MemorySection config={config} tasks={[]} showError={() => {}} {...props} />
);

// Settings → Memory (design/design/settings-panel.design.md §3.2): MemoryPanel
// in a card, with the leave guard App reads.
describe("MemorySection", () => {
  beforeEach(() => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
    api.mockImplementation((path, opts) => {
      if (opts) return Promise.resolve({});
      if (path === "/api/memory") return Promise.resolve(memories);
      if (path === "/api/memory/documents") return Promise.resolve(documents);
      if (path.startsWith("/api/memory/documents/"))
        return Promise.resolve({ summary: "summary", text: "doc text" });
      return Promise.resolve({});
    });
  });

  afterEach(() => {
    api.mockReset();
    vi.restoreAllMocks();
  });

  // The access code is owner access (§6.1): every scope is writable,
  // with no name asked about.
  it("lets whoever reads it save every scope", async () => {
    render(section());
    await screen.findByDisplayValue("widgets repo note");
    expect(
      screen.getByRole("button", { name: "Save this deployment's memory" }),
    ).toBeInTheDocument();
  });

  it("shows every scope's notes and documents", async () => {
    render(section());

    expect(
      await screen.findByDisplayValue("deployment standing note"),
    ).toBeVisible();
    expect(screen.getByDisplayValue("widgets repo note")).toBeVisible();
    expect(screen.getByText("deployment-doc")).toBeInTheDocument();
    expect(screen.getByText("widgets-doc")).toBeInTheDocument();
  });

  it("writes the deployment's note with no team in the body", async () => {
    const user = userEvent.setup();
    render(section());

    const box = await screen.findByRole("textbox", {
      name: "Memory carried by every run",
    });
    await user.clear(box);
    await user.type(box, "corrected");
    await user.click(
      screen.getByRole("button", { name: "Save this deployment's memory" }),
    );
    expect(api).toHaveBeenCalledWith("/api/memory", {
      method: "PUT",
      body: JSON.stringify({ repo: "", text: "corrected" }),
    });
  });

  it("asks before leaving with an unsaved edit, and not once it is saved", async () => {
    const user = userEvent.setup();
    const { unmount } = render(section());

    const box = await screen.findByRole("textbox", {
      name: "Memory carried by every run",
    });
    expect(leavingLosesEdits()).toBe(false);
    await user.type(box, " and more");
    expect(leavingLosesEdits()).toBe(true);

    await user.click(
      screen.getByRole("button", { name: "Save this deployment's memory" }),
    );
    expect(leavingLosesEdits()).toBe(false);

    await user.type(box, " again");
    expect(leavingLosesEdits()).toBe(true);
    unmount();
    expect(leavingLosesEdits()).toBe(false);
  });
});
