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
import { resetDetailMocks, baseTask, config } from "./testFixtures.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../../attachments.js", () => ({ default: vi.fn() }));

describe("DetailOverlay pending question", () => {
  afterEach(resetDetailMocks);

  const open = (task) =>
    render(
      <DetailOverlay
        task={task}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
        showError={vi.fn()}
      />,
    );

  // A question's one-line summary leads it on the task's own pane, bold,
  // with the whole question below it.
  it("shows the question's summary as its bold first line", () => {
    open({
      ...baseTask,
      state: "awaiting_reply",
      questionSummary: "Keep the v1 API or remove it?",
      comments: [
        {
          id: 3,
          author: "grain",
          authorKind: "automation",
          summary: "Keep the v1 API or remove it?",
          body: "The v1 API has two callers left.",
        },
      ],
    });
    const box = screen.getByTestId("pending-question");
    const line = box.querySelector(".question-summary-line strong");
    expect(line).toHaveTextContent("Keep the v1 API or remove it?");
    expect(box).toHaveTextContent("The v1 API has two callers left.");
    // Summary first, then the full text.
    expect(
      line.compareDocumentPosition(
        box.querySelector(".pending-question-body"),
      ) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("leads with the summary grain derived for a question asked without one", () => {
    open({
      ...baseTask,
      state: "awaiting_reply",
      questionSummary: "Which base branch?",
      comments: [{ id: 3, body: "Which base branch? It matters because..." }],
    });
    expect(
      screen
        .getByTestId("pending-question")
        .querySelector(".question-summary-line"),
    ).toHaveTextContent("Which base branch?");
  });
});
