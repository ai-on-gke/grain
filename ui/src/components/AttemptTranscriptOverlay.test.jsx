import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import AttemptTranscriptOverlay from "./AttemptTranscriptOverlay.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// jsdom lays nothing out, so a <pre> it renders is 0px tall with 0px of
// content and "scroll to the bottom" is indistinguishable from "stay at
// the top". These give one element the measurements of a pane showing
// 300px of a 900px transcript, so scrollTop can say which end it is at.
function measure(el, { scrollHeight = 900, clientHeight = 300 } = {}) {
  Object.defineProperty(el, "scrollHeight", {
    configurable: true,
    value: scrollHeight,
  });
  Object.defineProperty(el, "clientHeight", {
    configurable: true,
    value: clientHeight,
  });
}

describe("AttemptTranscriptOverlay", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("fetches and shows the attempt's transcript", async () => {
    api.mockResolvedValueOnce({
      transcript: "> read_file(out.txt)\nPONG\n\nfound it",
    });
    render(
      <AttemptTranscriptOverlay
        taskId="12"
        attempt={{ number: 1, finishedAt: "2026-08-28T12:10:00Z" }}
        onClose={() => {}}
        showError={() => {}}
      />,
    );

    expect(await screen.findByText(/found it/)).toBeInTheDocument();
    expect(screen.getByText("Attempt #1 transcript")).toBeInTheDocument();
    expect(api).toHaveBeenLastCalledWith("/api/tasks/12/attempts/1/transcript");
  });

  it("says which model the attempt ran on", async () => {
    api.mockResolvedValueOnce({ transcript: "done" });
    render(
      <AttemptTranscriptOverlay
        taskId="12"
        attempt={{
          number: 2,
          finishedAt: "2026-08-28T12:10:00Z",
          model: "gemini-3.1-flash",
          agentFramework: "antigravity",
        }}
        onClose={() => {}}
        showError={() => {}}
      />,
    );

    expect(await screen.findByText(/done/)).toBeInTheDocument();
    expect(
      document.querySelector(".attempt-transcript-model").textContent,
    ).toBe("Ran on gemini-3.1-flash (antigravity)");
  });

  it("says nothing about a model for an attempt that recorded none", async () => {
    api.mockResolvedValueOnce({ transcript: "done" });
    render(
      <AttemptTranscriptOverlay
        taskId="12"
        attempt={{ number: 1, finishedAt: "2026-08-28T12:10:00Z" }}
        onClose={() => {}}
        showError={() => {}}
      />,
    );

    expect(await screen.findByText(/done/)).toBeInTheDocument();
    expect(document.querySelector(".attempt-transcript-model")).toBeNull();
  });

  it("shows a placeholder for a finished attempt with nothing recorded", async () => {
    api.mockResolvedValueOnce({ transcript: "" });
    render(
      <AttemptTranscriptOverlay
        taskId="12"
        attempt={{ number: 1, finishedAt: "2026-08-28T12:10:00Z" }}
        onClose={() => {}}
        showError={() => {}}
      />,
    );

    expect(
      await screen.findByText("(no transcript recorded)"),
    ).toBeInTheDocument();
  });

  it("tells a still-running attempt apart from a finished one with nothing recorded", async () => {
    api.mockResolvedValueOnce({ transcript: "" });
    render(
      <AttemptTranscriptOverlay
        taskId="12"
        attempt={{ number: 2 }}
        onClose={() => {}}
        showError={() => {}}
      />,
    );

    expect(await screen.findByText(/Still running/)).toBeInTheDocument();
  });

  it("opens at the end of the transcript rather than the top", async () => {
    api.mockResolvedValueOnce({ transcript: "first line\nlast line" });
    render(
      <AttemptTranscriptOverlay
        taskId="12"
        attempt={{ number: 1, finishedAt: "2026-08-28T12:10:00Z" }}
        onClose={() => {}}
        showError={() => {}}
      />,
    );
    const view = document.querySelector("pre.logs-view");
    measure(view);

    expect(await screen.findByText(/last line/)).toBeInTheDocument();
    expect(view.scrollTop).toBe(900);
  });

  it("stops following the tail once the reader scrolls up", async () => {
    // shouldAdvanceTime keeps Testing Library's own waitFor polling
    // working while the component's 5s refresh interval is under this
    // test's control.
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      api.mockResolvedValue({ transcript: "first line\nlast line" });
      render(
        <AttemptTranscriptOverlay
          taskId="12"
          attempt={{ number: 2 }}
          onClose={() => {}}
          showError={() => {}}
        />,
      );
      const view = document.querySelector("pre.logs-view");
      measure(view);
      await waitFor(() => expect(view.scrollTop).toBe(900));

      // Back to the top to read something, then let the poll bring in
      // more of a still-running attempt's transcript.
      view.scrollTop = 0;
      fireEvent.scroll(view);
      api.mockResolvedValue({ transcript: "first line\nlast line\nand more" });
      await act(async () => {
        vi.advanceTimersByTime(5000);
      });

      expect(await screen.findByText(/and more/)).toBeInTheDocument();
      expect(view.scrollTop).toBe(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it("reports a fetch error through showError", async () => {
    const showError = vi.fn();
    api.mockRejectedValueOnce(new Error("boom"));
    render(
      <AttemptTranscriptOverlay
        taskId="12"
        attempt={{ number: 1, finishedAt: "2026-08-28T12:10:00Z" }}
        onClose={() => {}}
        showError={showError}
      />,
    );

    await waitFor(() =>
      expect(showError).toHaveBeenCalledWith(new Error("boom")),
    );
  });
});
