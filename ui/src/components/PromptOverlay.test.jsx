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
import { afterEach, describe, expect, it, vi } from "vitest";
import PromptOverlay from "./PromptOverlay.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

describe("PromptOverlay", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("fetches and shows the prompt the agent was given", async () => {
    api.mockResolvedValueOnce({
      prompt: "Fix the thing\n\nWork in acme/widgets.",
      attempt: 2,
    });
    render(<PromptOverlay taskId="12" onClose={() => {}} />);

    expect(
      await screen.findByText(/Work in acme\/widgets\./),
    ).toBeInTheDocument();
    expect(api).toHaveBeenLastCalledWith("/api/tasks/12/prompt");
  });

  // Which attempt's prompt this is matters: a redispatched task's prompt
  // grows with its conversation, so "attempt #3" is part of the answer.
  it("names the attempt the prompt belongs to", async () => {
    api.mockResolvedValueOnce({ prompt: "the prompt", attempt: 3 });
    render(<PromptOverlay taskId="12" onClose={() => {}} />);

    expect(await screen.findByText(/attempt #3/)).toBeInTheDocument();
  });

  // The size is the reason to open this pane twice: a task's prompt
  // grows with its conversation, and a prompt that is a fifth of the
  // window is itself the explanation for a run behaving badly.
  it("says how big the prompt is and what share of the window that is", async () => {
    api.mockResolvedValueOnce({
      prompt: "the prompt",
      attempt: 7,
      bytes: 204800,
      tokens: 51200,
      contextTokens: 200000,
    });
    render(<PromptOverlay taskId="12" onClose={() => {}} />);

    expect(await screen.findByText(/200 KB/)).toBeInTheDocument();
    expect(
      screen.getByText(/26% of a 200,000-token window/),
    ).toBeInTheDocument();
  });

  // A daemon older than the measurement answers without one, and the
  // pane says nothing rather than reporting a prompt of nought bytes.
  it("says nothing about size when the response carries no measurement", async () => {
    api.mockResolvedValueOnce({ prompt: "the prompt", attempt: 1 });
    render(<PromptOverlay taskId="12" onClose={() => {}} />);

    expect(await screen.findByText(/attempt #1/)).toBeInTheDocument();
    expect(screen.queryByText(/tokens/)).not.toBeInTheDocument();
  });

  it("explains a task that has never been dispatched rather than showing nothing", async () => {
    api.mockResolvedValueOnce({ prompt: "", attempt: 0 });
    render(<PromptOverlay taskId="12" onClose={() => {}} />);

    expect(
      await screen.findByText(/No prompt recorded yet/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/attempt #/)).not.toBeInTheDocument();
  });

  it("reports a fetch failure in the pane itself", async () => {
    api.mockRejectedValueOnce(new Error("boom"));
    render(<PromptOverlay taskId="12" onClose={() => {}} />);

    expect(
      await screen.findByText(/Could not load this task's prompt: boom/),
    ).toBeInTheDocument();
  });
});
