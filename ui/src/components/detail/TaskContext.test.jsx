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

import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
} from "@testing-library/react";
import {
  BriefSection,
  ContextSection,
  handoverAgeText,
  useTaskContext,
} from "./TaskContext.jsx";
import Timeline from "./Timeline.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn(() => Promise.resolve({})) }));

const t = { id: "7", state: "deferred", admission: "durable" };
const act = (mutate) => mutate().then(() => true);

const context = {
  task_id: "7",
  compacted_through: 2,
  compacted_at: "2026-11-08T10:00:00Z",
  compacted_by: "dana@example.com",
  handover_version: 3,
  current_brief: "Keep the changelog current.",
  brief_updated_at: "2026-11-09T10:00:00Z",
  brief_updated_by: "dana@example.com",
  original_description: "Stage 1: write the changelog.",
  handover_age: { exists: true, runs: 11, comments: 2 },
  handover_stale: true,
  summarised: 2,
  brief_edits: [
    {
      id: 1,
      at: "2026-11-01T10:00:00Z",
      by: "agent",
      reason: "stage 1 done",
      brief: "Stage 2.",
    },
    {
      id: 2,
      at: "2026-11-09T10:00:00Z",
      by: "dana@example.com",
      reason: "standing work now",
      brief: "Keep the changelog current.",
    },
  ],
};

beforeEach(() => api.mockClear());

describe("useTaskContext", () => {
  it("reads a durable task's context and nothing for any other", async () => {
    api.mockResolvedValueOnce(context);
    const { result } = renderHook(() => useTaskContext(t));
    await waitFor(() => expect(result.current[0]).toEqual(context));
    expect(api).toHaveBeenCalledWith("/api/tasks/7/context");

    api.mockClear();
    const plain = renderHook(() => useTaskContext({ id: "8" }));
    expect(plain.result.current[0]).toBeNull();
    expect(api).not.toHaveBeenCalled();
  });
});

describe("BriefSection", () => {
  const description = <div className="description">Stage 1</div>;

  it("is the description alone without a context", () => {
    render(
      <BriefSection t={t} context={null} act={act} description={description} />,
    );
    expect(screen.getByText("Stage 1")).toBeTruthy();
    expect(screen.queryByTestId("brief-section")).toBeNull();
  });

  it("shows the brief in place of the description, with the original and history", () => {
    render(
      <BriefSection
        t={t}
        context={context}
        act={act}
        description={description}
      />,
    );
    // The brief, and not the description, is what is shown open.
    const shown = screen.getByTestId("brief-section").firstChild;
    expect(shown.nextSibling.textContent).toContain(
      "Keep the changelog current.",
    );
    expect(
      screen
        .getByTestId("brief-original")
        .contains(screen.getByText("Stage 1")),
    ).toBe(true);
    expect(screen.getByTestId("brief-meta").textContent).toContain(
      "by dana@example.com",
    );
    expect(screen.getByTestId("brief-meta").textContent).toContain(
      "standing work now",
    );
    expect(screen.getByTestId("brief-original").textContent).toContain(
      "Stage 1",
    );
    expect(screen.getByTestId("brief-history").textContent).toContain(
      "2 edits",
    );
    expect(screen.getByTestId("brief-history").textContent).toContain(
      "stage 1 done",
    );
  });

  it("saves an edit with its reason", async () => {
    const setContext = vi.fn();
    api.mockResolvedValueOnce({ ...context, current_brief: "New." });
    render(
      <BriefSection
        t={t}
        context={context}
        setContext={setContext}
        act={act}
        description={description}
      />,
    );
    fireEvent.click(screen.getByTestId("brief-edit"));
    const save = screen.getByText("Save brief");
    expect(save.closest("button").disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("Current brief"), {
      target: { value: "New." },
    });
    fireEvent.change(screen.getByLabelText(/Why it changed/), {
      target: { value: "scope moved" },
    });
    fireEvent.click(save);
    await waitFor(() => expect(setContext).toHaveBeenCalled());
    expect(api).toHaveBeenCalledWith("/api/tasks/7/brief", {
      method: "POST",
      body: JSON.stringify({ brief: "New.", reason: "scope moved" }),
    });
  });
});

describe("ContextSection", () => {
  it("shows the handover's version, age and staleness", () => {
    render(<ContextSection t={t} context={context} act={act} />);
    expect(screen.getByTestId("context-handover").textContent).toContain("v3");
    expect(screen.getByTestId("handover-stale")).toBeTruthy();
    expect(screen.getByTestId("context-compaction").textContent).toContain(
      "2 comments summarised",
    );
  });

  it("asks for a compaction, or compacts to a handover at once", async () => {
    const setContext = vi.fn();
    api.mockResolvedValue({ ...context });
    render(
      <ContextSection
        t={t}
        context={{ ...context, handover_stale: false }}
        setContext={setContext}
        act={act}
      />,
    );
    expect(screen.queryByTestId("handover-stale")).toBeNull();
    fireEvent.click(screen.getByTestId("compact"));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/tasks/7/compact", {
        method: "POST",
        body: "{}",
      }),
    );
    fireEvent.click(screen.getByText("Write handover…"));
    fireEvent.change(screen.getByLabelText("Handover"), {
      target: { value: "Where things stand." },
    });
    fireEvent.click(screen.getByText("Compact now"));
    await waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/tasks/7/compact", {
        method: "POST",
        body: JSON.stringify({ handover: "Where things stand." }),
      }),
    );
  });

  it("says a standing request and holds Compact until it is answered", () => {
    render(
      <ContextSection
        t={t}
        context={{
          ...context,
          compact_requested_at: "2026-11-10T10:00:00Z",
          compact_requested_by: "grain (auto-compaction)",
        }}
        act={act}
      />,
    );
    expect(screen.getByTestId("context-requested").textContent).toContain(
      "grain (auto-compaction)",
    );
    expect(screen.getByTestId("compact").disabled).toBe(true);
  });

  it("words a handover's age", () => {
    expect(handoverAgeText({ handover_age: { exists: false } })).toBe(
      "No handover yet.",
    );
    expect(
      handoverAgeText({ handover_age: { exists: true, runs: 1, comments: 0 } }),
    ).toBe("1 run and 0 comments since it was written or confirmed.");
  });
});

describe("Timeline on a compacted task", () => {
  it("collapses summarised comments and marks a stale handover", () => {
    const task = {
      ...t,
      comments: [
        { id: 1, body: "old one", authorKind: "human", author: "dana" },
        { id: 2, body: "old two", authorKind: "human", author: "dana" },
        {
          id: 3,
          body: "the handover",
          authorKind: "agent",
          author: "agent",
          handover: true,
        },
      ],
    };
    const { container } = render(
      <Timeline t={task} context={context} act={vi.fn()} showError={vi.fn()} />,
    );
    const summarised = container.querySelectorAll(".timeline-summarised");
    expect(summarised.length).toBe(2);
    expect(summarised[0].open).toBe(false);
    const handover = container.querySelector("#comment-3");
    expect(handover.textContent).toContain("handover · stale");
    expect(handover.querySelector(".timeline-summarised")).toBeNull();
  });
});
