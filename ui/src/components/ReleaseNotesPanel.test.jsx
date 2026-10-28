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

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import ReleaseNotesPanel from "./ReleaseNotesPanel.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

// Every markdown the panel renders, in order: the real component, with
// what it was handed written down on the way through.
const markdownRendered = vi.hoisted(() => []);
vi.mock("./Markdown.jsx", async (importOriginal) => {
  const { default: Markdown } = await importOriginal();
  return {
    default: function RecordedMarkdown(props) {
      markdownRendered.push(String(props.children));
      return <Markdown {...props} />;
    },
  };
});

const stats = (period, over = {}) => ({
  period,
  since: "2026-09-14T00:00:00Z",
  until: "2026-09-21T00:00:00Z",
  pullRequestsMerged: 12,
  pullRequestsMeasured: 12,
  linesAdded: 4210,
  linesDeleted: 1875,
  tasksCompleted: 18,
  tasksFiled: 20,
  runs: 31,
  runsSucceeded: 27,
  agentHours: 41.25,
  repos: [
    {
      repo: "bwsalmon/grain",
      pullRequestsMerged: 9,
      pullRequestsMeasured: 9,
      linesAdded: 3900,
      linesDeleted: 1700,
      tasksCompleted: 12,
      tasksFiled: 14,
      runs: 22,
    },
    {
      repo: "bwsalmon/granule",
      pullRequestsMerged: 3,
      pullRequestsMeasured: 3,
      linesAdded: 310,
      linesDeleted: 175,
      tasksCompleted: 6,
      tasksFiled: 6,
      runs: 9,
    },
  ],
  ...over,
});

// Answered by path, so a test does not have to know which of the reads
// this panel issues first.
const answers = ({
  weeks = [],
  notes = {},
  counts = {},
  versions = { running: "", versions: [] },
  versionNotes = {},
} = {}) => {
  api.mockImplementation((path) => {
    if (path === "/api/release-notes") return Promise.resolve(weeks);
    if (path === "/api/release-notes/versions") {
      return Promise.resolve(versions);
    }
    if (path.startsWith("/api/release-notes/versions/")) {
      const v = decodeURIComponent(
        path.slice("/api/release-notes/versions/".length),
      );
      return versionNotes[v]
        ? Promise.resolve(versionNotes[v])
        : Promise.reject(new Error(`no notes for ${v}`));
    }
    if (path.startsWith("/api/release-notes/stats")) {
      const asked = new URLSearchParams(path.split("?")[1] || "").get("period");
      return Promise.resolve(counts[asked || "default"]);
    }
    const period = path.slice("/api/release-notes/".length);
    return Promise.resolve(notes[period] || { period });
  });
};

describe("ReleaseNotesPanel", () => {
  afterEach(() => {
    api.mockReset();
  });

  it("leads with the week's own numbers and the repositories behind them", async () => {
    answers({
      weeks: [
        { period: "2026-W38", title: "A busy week on grain", length: 40 },
      ],
      counts: { default: stats("2026-W38") },
      notes: {
        "2026-W38": {
          period: "2026-W38",
          title: "A busy week on grain",
          text: "Twelve pull requests landed.",
          updatedAt: "2026-09-21T03:10:00Z",
          updatedByKind: "agent",
          updatedBy: "task-412",
        },
      },
    });

    render(<ReleaseNotesPanel showError={() => {}} />);

    expect(
      await screen.findByText("Pull requests merged: 12"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Agent runs: 31 (27 succeeded)"),
    ).toBeInTheDocument();
    expect(screen.getByText("Agent hours: 41.3")).toBeInTheDocument();
    expect(screen.getByText("Lines changed: +4210 -1875")).toBeInTheDocument();
    expect(screen.getByText("bwsalmon/grain")).toBeInTheDocument();
    expect(screen.getByText(/\(\+3900 -1700\)/)).toBeInTheDocument();
    expect(
      await screen.findByText("Twelve pull requests landed."),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Written by grain's release-notes job/),
    ).toBeInTheDocument();
  });

  // The week nobody has written up is the ordinary case on a fresh
  // deployment, and the one the numbers still have something to say
  // about -- so it must not read as a pane that failed to load.
  it("shows the week's numbers even when nothing has been written about it", async () => {
    answers({
      weeks: [],
      counts: { default: stats("2026-W38", { pullRequestsMerged: 0 }) },
      notes: {},
    });

    render(<ReleaseNotesPanel showError={() => {}} />);

    expect(
      await screen.findByText("Pull requests merged: 0"),
    ).toBeInTheDocument();
    expect(
      await screen.findByText(/Nothing has been written up for this week yet/),
    ).toBeInTheDocument();
  });

  // Grain has only recorded a merged pull request's size since
  // September 2026, so a week can hold merges it has no size for. The
  // chip has to say how much of the week its figure covers, and a week
  // with nothing measured has to show no figure at all rather than a
  // confident zero.
  it("says how many merges its lines-changed figure covers", async () => {
    answers({
      weeks: [],
      counts: { default: stats("2026-W38", { pullRequestsMeasured: 9 }) },
      notes: {},
    });

    render(<ReleaseNotesPanel showError={() => {}} />);

    expect(
      await screen.findByText("Lines changed: +4210 -1875 (9 of 12 merges)"),
    ).toBeInTheDocument();
  });

  it("shows no lines-changed figure for a week none of whose merges were measured", async () => {
    answers({
      weeks: [],
      counts: {
        default: stats("2026-W38", {
          pullRequestsMeasured: 0,
          linesAdded: 0,
          linesDeleted: 0,
          repos: [],
        }),
      },
      notes: {},
    });

    render(<ReleaseNotesPanel showError={() => {}} />);

    expect(
      await screen.findByText("Pull requests merged: 12"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Lines changed/)).not.toBeInTheDocument();
  });

  it("reads an earlier week when one is picked", async () => {
    answers({
      weeks: [
        { period: "2026-W38", title: "This week" },
        { period: "2026-W37", title: "Last week" },
      ],
      counts: {
        default: stats("2026-W38"),
        "2026-W37": stats("2026-W37", {
          pullRequestsMerged: 4,
          pullRequestsMeasured: 4,
        }),
      },
      notes: {
        "2026-W38": { period: "2026-W38", title: "This week", text: "Now." },
        "2026-W37": { period: "2026-W37", title: "Last week", text: "Then." },
      },
    });

    render(<ReleaseNotesPanel showError={() => {}} />);
    expect(await screen.findByText("Now.")).toBeInTheDocument();

    await userEvent.click(screen.getByLabelText("week"));
    await userEvent.click(
      await screen.findByRole("option", { name: /Week 37/ }),
    );

    expect(await screen.findByText("Then.")).toBeInTheDocument();
    expect(
      await screen.findByText("Pull requests merged: 4"),
    ).toBeInTheDocument();
  });

  // The per-version notes this build carries, beside the weekly ones:
  // newest first, the running version marked, a version's notes on
  // click (grain/task-3092).
  it("lists grain's versions, marks the running one and renders a version's notes on click", async () => {
    answers({
      counts: { default: stats("2026-W38") },
      versions: {
        running: "v1.8.0",
        versions: [
          { version: "v1.8.0", title: "v1.8.0", date: "2026-10-11" },
          { version: "v1.7.0", title: "v1.7.0", date: "2026-10-04" },
        ],
      },
      versionNotes: {
        "v1.7.0": {
          version: "v1.7.0",
          title: "v1.7.0",
          markdown: "# v1.7.0\n\n## Highlights\n\n- **Faster CI.** Reruns.\n",
        },
      },
    });

    render(<ReleaseNotesPanel showError={() => {}} />);

    const list = await screen.findByRole("list", { name: "grain versions" });
    const items = within(list).getAllByRole("button");
    expect(items.map((item) => item.textContent)).toEqual([
      expect.stringContaining("v1.8.0"),
      expect.stringContaining("v1.7.0"),
    ]);
    expect(within(items[0]).getByText("running")).toBeInTheDocument();
    expect(within(items[1]).queryByText("running")).not.toBeInTheDocument();
    expect(
      within(items[0]).getByText("Released 2026-10-11"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/This deployment runs v1.8.0\./),
    ).toBeInTheDocument();

    expect(items[1]).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(items[1]);
    expect(
      await screen.findByRole("heading", { name: "Highlights" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Faster CI.")).toBeInTheDocument();
    // A disclosure says it is open, and which notes it opened.
    const notes = screen.getByRole("region", { name: "v1.7.0 release notes" });
    expect(items[1]).toHaveAttribute("aria-expanded", "true");
    expect(items[1]).toHaveAttribute("aria-controls", notes.id);
    expect(items[0]).toHaveAttribute("aria-expanded", "false");

    await userEvent.click(items[1]);
    expect(items[1]).toHaveAttribute("aria-expanded", "false");
    expect(
      screen.queryByRole("region", { name: "v1.7.0 release notes" }),
    ).not.toBeInTheDocument();
  });

  // Seen on grain demo (docs/test-runs/2026-10-12-release-notes-versions.md):
  // right after a click from one version to the next, the region named
  // for the new version still held the old version's notes until the
  // new ones were asked for.
  it("never shows one version's notes under another's name while switching", async () => {
    let answerV17;
    answers({
      counts: { default: stats("2026-W38") },
      versions: {
        running: "",
        versions: [
          { version: "v1.8.0", title: "v1.8.0" },
          { version: "v1.7.0", title: "v1.7.0" },
        ],
      },
      versionNotes: {
        "v1.8.0": { version: "v1.8.0", markdown: "# v1.8.0\n\n- Eight.\n" },
      },
    });
    const base = api.getMockImplementation();
    api.mockImplementation((path) =>
      path === "/api/release-notes/versions/v1.7.0"
        ? new Promise((resolve) => {
            answerV17 = resolve;
          })
        : base(path),
    );

    render(<ReleaseNotesPanel showError={() => {}} />);
    const list = await screen.findByRole("list", { name: "grain versions" });
    const items = within(list).getAllByRole("button");
    await userEvent.click(items[0]);
    expect(await screen.findByText("Eight.")).toBeInTheDocument();

    // act() has run the effects by the time the click resolves, so what
    // the click's own render showed is read from what was rendered.
    markdownRendered.length = 0;
    await userEvent.click(items[1]);
    expect(markdownRendered.filter((md) => md.includes("Eight."))).toEqual([]);
    expect(items[1]).toHaveAttribute("aria-expanded", "true");
    expect(screen.queryByText("Eight.")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("region", { name: "v1.7.0 release notes" }),
    ).not.toBeInTheDocument();

    answerV17({ version: "v1.7.0", markdown: "# v1.7.0\n\n- Seven.\n" });
    const notes = await screen.findByRole("region", {
      name: "v1.7.0 release notes",
    });
    expect(within(notes).getByText("Seven.")).toBeInTheDocument();
  });

  it("says which part is weekly and which is per version", async () => {
    answers({ counts: { default: stats("2026-W38") } });
    render(<ReleaseNotesPanel showError={() => {}} />);
    expect(
      await screen.findByText(/what this deployment shipped each week/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/what each release of grain itself brought/),
    ).toBeInTheDocument();
    expect(
      await screen.findByText("This build carries no release notes."),
    ).toBeInTheDocument();
  });

  it("says when the running version has no notes in this build", async () => {
    answers({
      counts: { default: stats("2026-W38") },
      versions: {
        running: "v1.9.0-rc.2",
        versions: [{ version: "v1.8.0", title: "v1.8.0" }],
      },
    });
    render(<ReleaseNotesPanel showError={() => {}} />);
    expect(
      await screen.findByText(
        /runs v1.9.0-rc.2, which has no notes of its own/,
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText("running")).not.toBeInTheDocument();
  });
});
