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
import DetailOverlay from "../DetailOverlay.jsx";
import api from "../../api.js";
import { resetDetailMocks, baseTask, config } from "./testFixtures.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../../attachments.js", () => ({ default: vi.fn() }));

describe("DetailOverlay live attempt transcript", () => {
  afterEach(resetDetailMocks);

  it("renders embedded live transcript immediately for running task with active attempt", async () => {
    api.mockResolvedValue({ transcript: "agent is thinking...\nran command" });
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "running",
          attempts: [
            {
              number: 1,
              startedAt: "2026-08-28T12:00:00Z",
            },
          ],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByText("Live Attempt #1 Transcript")).toBeInTheDocument();
    expect(
      await screen.findByText(/agent is thinking\.\.\./),
    ).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith("/api/tasks/12/attempts/1/transcript");
  });

  it("displays placeholder when running task active attempt has no output yet", async () => {
    api.mockResolvedValue({ transcript: "" });
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "running",
          attempts: [
            {
              number: 2,
              startedAt: "2026-08-28T12:00:00Z",
            },
          ],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(screen.getByText("Live Attempt #2 Transcript")).toBeInTheDocument();
    expect(
      await screen.findByText("Still running -- nothing written yet."),
    ).toBeInTheDocument();
  });

  it("does not render embedded live transcript for queued or completed task", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          state: "queued",
          attempts: [],
        }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

    expect(
      screen.queryByText(/Live Attempt #\d+ Transcript/),
    ).not.toBeInTheDocument();
  });
});
