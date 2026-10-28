import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import ReportBoard from "./ReportBoard.jsx";
import api from "../api.js";
import { normalizeBoard } from "../boards.js";
import { boardNarrowing } from "../boards.js";
import { sectionMove } from "../sectionOrder.js";
import { metricsReport } from "../appTestHarness.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// A report board (grain/task-83): the measurement report over the tasks a
// board's query names. What is worth pinning here is what the page asks
// the daemon for -- the scope is the whole of what makes this board about
// anything -- what it prints when the query says more than a report can
// use, and that a board naming sections prints those and not the rest.

const board = (over) =>
  normalizeBoard({
    id: "board-1",
    name: "How payments is going",
    kind: "report",
    ...over,
  });

// The page holds no tasks, so a harness for it is a board, a narrowing
// and a fake endpoint. The narrowing is held in state the way App holds
// it, so narrowing on the toolbar re-asks the endpoint here as it does
// there.
function Harness({ board: row, onMoveSection, tasks }) {
  const [narrowing, setNarrowing] = useState(() => boardNarrowing(row));
  const narrow = (patch) => setNarrowing((n) => ({ ...n, ...patch }));
  return (
    <>
      {/* Stands in for the toolbar composing a condition. What the
          toolbar does with the narrowing it builds is NarrowingBar's own
          tested behaviour; what this pins is what this page does when the
          narrowing it is handed changes. */}
      <button
        type="button"
        onClick={() =>
          narrow({
            filters: [{ attr: "repo", op: "any", values: ["acme/docs"] }],
          })
        }
      >
        narrow to docs
      </button>
      <ReportBoard
        board={row}
        config={{}}
        tasks={tasks}
        narrowing={narrowing}
        onNarrow={narrow}
        onOpenTask={() => {}}
        showError={() => {}}
        onMoveSection={onMoveSection}
      />
    </>
  );
}

// asked is the query string of the last report the page fetched, parsed.
const asked = () => {
  const path = api.mock.calls.at(-1)[0];
  return new URLSearchParams(path.slice(path.indexOf("?")));
};

describe("ReportBoard", () => {
  beforeEach(() => {
    api.mockReset();
    api.mockResolvedValue(metricsReport);
  });

  // The error banner clears itself after a few seconds; the page under
  // it used to go on spinning "Loading the report" for good, so a report
  // that failed read as one still on its way (2026-10-03 walkthrough).
  it("says the report could not be loaded instead of spinning for good", async () => {
    api.mockRejectedValueOnce(new Error("Failed to fetch"));
    render(<Harness board={board({ query: "f=repo:any:acme/widgets" })} />);
    expect(
      await screen.findByText(
        /The report could not be loaded \(Failed to fetch\)/,
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("progressbar", { name: "Loading the report" }),
    ).toBeNull();
    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
    await waitFor(() =>
      expect(screen.queryByText(/could not be loaded/)).toBeNull(),
    );
    expect(screen.getAllByText("Tasks filed").length).toBeGreaterThan(0);
  });

  // A whole report whose daemon could not read one input comes back with
  // that section named as failed rather than as a 500 for everything; the
  // page says so instead of drawing the empty section as a quiet window.
  it("names a section the daemon could not build", async () => {
    api.mockResolvedValueOnce({
      ...metricsReport,
      failed: [
        {
          section: "prompts",
          error: "internal server error (ref abc123)",
          ref: "abc123",
        },
      ],
    });
    render(<Harness board={board()} />);
    expect(
      await screen.findByText(
        /Not available in this report: prompts \(internal server error \(ref abc123\)\)/,
      ),
    ).toBeInTheDocument();
    expect(screen.getAllByText("Tasks filed").length).toBeGreaterThan(0);
  });

  it("asks for the report scoped to the repo the board's query names", async () => {
    render(<Harness board={board({ query: "f=repo:any:acme/widgets" })} />);
    await waitFor(() => expect(api).toHaveBeenCalled());
    expect(asked().getAll("repo")).toEqual(["acme/widgets"]);
    // ...and says so on the page, since the whole point of a scoped
    // report is that its numbers are not the deployment's.
    expect(
      await screen.findByText(/Measured over the tasks in acme\/widgets/),
    ).toBeInTheDocument();
  });

  // A date range in the query is the report's window. It is the reading
  // report.js argues for, and the dates the page prints are the proof of
  // which one it took.
  it("measures over the span the query names", async () => {
    render(<Harness board={board({ query: "f=created:within:30" })} />);
    await waitFor(() => expect(api).toHaveBeenCalled());
    expect(asked().get("window")).toBe("30d");
  });

  it("defaults to the week every other report here opens on", async () => {
    render(<Harness board={board({})} />);
    await waitFor(() => expect(api).toHaveBeenCalled());
    expect(asked().get("window")).toBe("7d");
  });

  // The conditions a report cannot use are named rather than dropped in
  // silence: a board saved off a narrowed list usually carries one.
  it("says which conditions it could not use", async () => {
    render(
      <Harness
        board={board({ query: "f=state:any:running&f=repo:any:acme/widgets" })}
      />,
    );
    expect(await screen.findByText(/not in force/)).toHaveTextContent(
      "State (running)",
    );
  });

  it("says nothing about conditions when it used all of them", async () => {
    render(<Harness board={board({ query: "f=repo:any:acme/widgets" })} />);
    await screen.findByText(/Measured over the tasks/);
    expect(screen.queryByText(/not in force/)).toBeNull();
  });

  // The board's own sections, and only those: a report board is how
  // somebody asks for a shorter report than all of it.
  it("prints the sections the board names and no others", async () => {
    render(
      <Harness
        board={board({
          query: "",
          sections: [{ kind: "latency", title: "How long it takes" }],
        })}
      />,
    );
    expect(await screen.findByText("How long it takes")).toBeInTheDocument();
    expect(screen.getByText("Latency")).toBeInTheDocument();
    expect(screen.queryByText("Throughput")).toBeNull();
  });

  // A board's own title for a section sat at h6 while every section's
  // own name is an h3, so a heading list jumped from the board's h2 to
  // an h6 (2026-10-06 walkthrough).
  it("heads a section the board titled at the level of the others", async () => {
    render(
      <Harness
        board={board({
          query: "",
          sections: [{ kind: "latency", title: "How long it takes" }],
        })}
      />,
    );
    expect(
      await screen.findByRole("heading", {
        level: 3,
        name: "How long it takes",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { level: 3, name: "Latency" }),
    ).toBeInTheDocument();
  });

  // The handle is how a keyboard or screen-reader user finds a section to
  // move, so it has to name the heading they will find under it. The
  // "runs" section's handle read "Attempts" over a body headed
  // "Capacity".
  it("names a section's handle after the heading drawn under it", async () => {
    render(
      <Harness
        board={board({
          sections: [
            { kind: "throughput", orderKey: 10 },
            { kind: "runs", orderKey: 20 },
          ],
        })}
        onMoveSection={() => {}}
      />,
    );
    expect(
      await screen.findByRole("heading", { level: 3, name: "Capacity" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Move section 2, “Capacity”" }),
    ).toBeInTheDocument();
  });

  // A report board orders its own sections, and need not have a
  // capacity one at all, so the backlog's caption cannot point "above"
  // (2026-10-15 walkthrough: it did, over a report whose capacity
  // section was drawn last).
  it("does not place capacity above the backlog on a board that reorders them", async () => {
    render(
      <Harness
        board={board({
          query: "",
          sections: [
            { kind: "backlog", orderKey: 10 },
            { kind: "runs", orderKey: 20 },
          ],
        })}
      />,
    );
    expect(
      await screen.findByText(/right now, not over the window/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/capacity above/i)).toBeNull();
  });

  it("prints the whole report for a board that names no sections", async () => {
    render(<Harness board={board({})} onMoveSection={() => {}} />);
    expect(await screen.findByText("Throughput")).toBeInTheDocument();
    expect(screen.getByText("Latency")).toBeInTheDocument();
    expect(screen.getByText("Backlog")).toBeInTheDocument();
    // A board that names no sections has no rows in tables/board_section.json
    // to reorder or explain as missing.
    expect(
      screen.queryByRole("button", { name: /^Move section / }),
    ).not.toBeInTheDocument();
    expect(screen.queryByText(/nothing to report over this window/)).toBeNull();
  });

  it("moves a section above the slot the reader taps", async () => {
    const onMoveSection = vi.fn();
    const row = board({
      sections: [
        { kind: "throughput", orderKey: 10 },
        { kind: "latency", orderKey: 20 },
        { kind: "tools", orderKey: 30 },
      ],
    });
    render(<Harness board={row} onMoveSection={onMoveSection} />);
    await screen.findByText("Throughput");
    await userEvent.click(
      screen.getByRole("button", { name: "Move section 3, “Tool use”" }),
    );
    await userEvent.click(
      screen.getByRole("button", {
        name: "Move here, above section 1, “Throughput”",
      }),
    );
    expect(onMoveSection).toHaveBeenCalledWith(
      expect.objectContaining({ id: "board-1" }),
      "section-2",
      "section-0",
    );
  });

  // The panel test of the same name says why: a section rendered by its
  // position kept focus where it had been, under the next section's name.
  it("leaves focus on the moved section's own handle", async () => {
    function Movable() {
      const [row, setRow] = useState(() =>
        board({
          sections: [
            { kind: "throughput", orderKey: 10 },
            { kind: "latency", orderKey: 20 },
            { kind: "tools", orderKey: 30 },
          ],
        }),
      );
      return (
        <Harness
          board={row}
          onMoveSection={(b, key, beforeKey) =>
            setRow(
              normalizeBoard({
                ...b,
                sections: sectionMove(b.sections, key, beforeKey),
              }),
            )
          }
        />
      );
    }
    const user = userEvent.setup();
    render(<Movable />);
    await screen.findByText("Throughput");
    screen.getByRole("button", { name: "Move section 3, “Tool use”" }).focus();
    await user.keyboard("{Enter}");
    screen
      .getByRole("button", { name: "Move here, above section 1, “Throughput”" })
      .focus();
    await user.keyboard("{Enter}");
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Move section 1, “Tool use”" }),
      ).toHaveFocus(),
    );
  });

  // The toolbar narrows the report itself -- it re-asks the endpoint
  // with a different scope, since there is nothing on this page to
  // filter (grain/task-111's gesture, on the one board where it has to
  // reach the daemon to mean anything).
  it("re-asks the daemon when the reader narrows it", async () => {
    const user = userEvent.setup();
    render(<Harness board={board({ query: "f=repo:any:acme/widgets" })} />);
    await waitFor(() =>
      expect(asked().getAll("repo")).toEqual(["acme/widgets"]),
    );

    await user.click(screen.getByRole("button", { name: "narrow to docs" }));
    // Nothing on this page is filtered: what changes is the question
    // the daemon is asked, which is the only place a distribution over
    // some of the tasks can come from.
    await waitFor(() => expect(asked().getAll("repo")).toEqual(["acme/docs"]));
    expect(
      await screen.findByText(/Measured over the tasks in acme\/docs/),
    ).toBeInTheDocument();
  });
  // The 2026-10-11 boards walkthrough: the page handed its toolbar no
  // tasks, so "Repo" in the filter editor offered "No values to choose
  // from" and a report could only be scoped to a repo by editing the
  // address bar. The menu is built from the tasks the page is handed.
  it("offers the repos of the tasks it is handed to scope the report by", async () => {
    const user = userEvent.setup();
    render(
      <Harness
        board={board()}
        tasks={[
          { id: "1", title: "Widgets", state: "queued", repo: "acme/widgets" },
          { id: "2", title: "Docs", state: "running", repo: "acme/docs" },
        ]}
      />,
    );
    await waitFor(() => expect(api).toHaveBeenCalled());

    await user.click(
      screen.getByTitle("Add and edit the conditions this list is narrowed by"),
    );
    await user.click(screen.getByRole("button", { name: "Filter by…" }));
    await user.click(await screen.findByRole("menuitem", { name: "Repo" }));
    await user.click(screen.getByLabelText("Value for filter 1"));
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "acme/docs",
      "acme/widgets",
    ]);

    await user.click(screen.getByRole("option", { name: "acme/docs" }));
    await waitFor(() => expect(asked().getAll("repo")).toEqual(["acme/docs"]));
  });

  // grain/task-1356: a section with nothing to say draws nothing, which
  // on a board left the section's own title over an empty space -- read
  // as a section that had failed to load. A board names its sections, so
  // it says which of them had nothing to report.
  it("says a section it names had nothing to report rather than leaving its title bare", async () => {
    render(
      <Harness
        board={board({
          sections: [
            { kind: "throughput" },
            { kind: "merges", title: "What landed" },
          ],
        })}
      />,
    );
    expect(await screen.findByText("What landed")).toBeInTheDocument();
    expect(
      screen.getByText("Merge queue: nothing to report over this window."),
    ).toBeInTheDocument();
    // ...and only that one: the throughput section had something to say.
    expect(screen.queryByText(/^Throughput: nothing to report/)).toBeNull();
  });

  // The "cycles" section was offered by the New board prompt, accepted by
  // model.ReportSections and served by /api/metrics, but had no renderer,
  // so a board asking for it printed nothing at all (grain/task-1356).
  it("draws the scheduling section a board names", async () => {
    const dist = (p50) => ({
      n: 40,
      minSeconds: p50 / 2,
      p50Seconds: p50,
      p90Seconds: p50 * 2,
      p99Seconds: p50 * 3,
      maxSeconds: p50 * 4,
      meanSeconds: p50,
    });
    api.mockResolvedValue({
      ...metricsReport,
      cycles: {
        enabled: true,
        n: 40,
        observed: 40,
        truncated: true,
        tick: dist(0.5),
        interval: dist(10),
        dispatchWait: dist(0.1),
        commits: { n: 40, total: 12 },
        commitFree: 31,
        reconcilers: [{ name: "dispatch", duration: dist(0.2), failures: 2 }],
      },
    });
    render(<Harness board={board({ sections: [{ kind: "cycles" }] })} />);
    expect(await screen.findByText("Scheduling")).toBeInTheDocument();
    expect(screen.getByText("Tick")).toBeInTheDocument();
    expect(screen.getByText("Dispatch wait")).toBeInTheDocument();
    expect(screen.getByText("dispatch (2 failed)")).toBeInTheDocument();
    expect(screen.getByText(/31 committed nothing/)).toBeInTheDocument();
    expect(screen.queryByText(/nothing to report/)).toBeNull();
  });

  it("says the scheduling section has nothing to report when there is no daemon to ask", async () => {
    render(<Harness board={board({ sections: [{ kind: "cycles" }] })} />);
    expect(
      await screen.findByText(
        "Scheduling: nothing to report over this window.",
      ),
    ).toBeInTheDocument();
  });

  it("renders a custom created:within day window in the Window control", async () => {
    render(<Harness board={board({ query: "f=created:within:14" })} />);
    await waitFor(() => expect(asked().get("window")).toBe("14d"));
    expect(screen.getByLabelText("Window")).toHaveTextContent("Last 14 days");
  });
});
