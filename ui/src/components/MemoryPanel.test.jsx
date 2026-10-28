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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import MemoryPanel from "./MemoryPanel.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const daysAgo = (days) =>
  new Date(Date.now() - days * 24 * 60 * 60 * 1000).toISOString();

// The two listings the panel opens with, answered by path so a test does
// not have to know which of the two reads is issued first.
const listing = ({ memories = [], documents = [] } = {}) => {
  api.mockImplementation((path) => {
    if (path === "/api/memory") return Promise.resolve(memories);
    if (path === "/api/memory/documents") return Promise.resolve(documents);
    return Promise.resolve({});
  });
};

describe("MemoryPanel", () => {
  beforeEach(() => {
    vi.spyOn(window, "confirm").mockReturnValue(true);
  });

  afterEach(() => {
    api.mockReset();
    vi.restoreAllMocks();
  });

  it("shows each scope's memory, the deployment's first", async () => {
    listing({
      memories: [
        { text: "squash merges here", updatedAt: daysAgo(1) },
        {
          repo: "bwsalmon/granule",
          text: "boot the VM before the suite",
          updatedAt: daysAgo(3),
        },
      ],
    });
    render(<MemoryPanel showError={() => {}} />);

    expect(await screen.findByText("This deployment")).toBeInTheDocument();
    expect(screen.getByText("bwsalmon/granule")).toBeInTheDocument();
    expect(
      screen.getByDisplayValue("boot the VM before the suite"),
    ).toBeInTheDocument();
    expect(screen.getByText("written yesterday")).toBeInTheDocument();
    expect(screen.getByText("written 3 days ago")).toBeInTheDocument();
  });

  // Staleness is the failure mode nothing else in grain notices, so it
  // is the one thing this pane says of its own accord.
  it("marks a memory old enough that nothing has revisited it", async () => {
    listing({ memories: [{ text: "an old note", updatedAt: daysAgo(120) }] });
    render(<MemoryPanel showError={() => {}} />);

    expect(await screen.findByText("written 4 months ago")).toBeInTheDocument();
    expect(
      screen.getByText(/worth checking it is still true/),
    ).toBeInTheDocument();
  });

  // Who wrote a note, which is what nothing on this pane could say
  // before model.Memory.UpdatedBy existed: an operator editing here and
  // the memory agent make the same write, and the difference is what
  // decides whether a note is grain's own inference or somebody's
  // correction.
  it("says which notes a person wrote and which the memory agent did", async () => {
    listing({
      memories: [
        {
          text: "grain worked this out",
          updatedByKind: "agent",
          updatedBy: "a1b2",
        },
        {
          repo: "bwsalmon/granule",
          text: "somebody fixed this",
          updatedByKind: "human",
          updatedBy: "bwsalmon",
        },
      ],
      documents: [
        {
          name: "flaky-e2e",
          summary: "why the third retry fails",
          length: 12,
          updatedByKind: "human",
          updatedBy: "bwsalmon",
        },
      ],
    });
    render(<MemoryPanel showError={() => {}} />);

    expect(
      await screen.findByText("written by the memory agent"),
    ).toBeInTheDocument();
    // Twice: the repo's memory and the document filed under the
    // deployment, both written by hand.
    expect(screen.getAllByText("edited here")).toHaveLength(2);
  });

  // Unknown is not one of the two answers. Every row written before the
  // author was recorded has none, and a chip guessing at either would be
  // this pane inventing the very thing it exists to report.
  it("says nothing about a note whose author was never recorded", async () => {
    listing({ memories: [{ text: "from before the column existed" }] });
    render(<MemoryPanel showError={() => {}} />);

    expect(
      await screen.findByDisplayValue("from before the column existed"),
    ).toBeInTheDocument();
    expect(screen.queryByText("edited here")).not.toBeInTheDocument();
    expect(
      screen.queryByText("written by the memory agent"),
    ).not.toBeInTheDocument();
  });

  it("saves an edited memory through the same PUT the agent uses", async () => {
    const user = userEvent.setup();
    listing({ memories: [{ repo: "bwsalmon/grain", text: "old" }] });
    render(<MemoryPanel showError={() => {}} />);

    const box = await screen.findByLabelText("Memory about bwsalmon/grain");
    await user.clear(box);
    await user.type(box, "new");
    // Named for its scope, so this is the repo's Save and not the
    // deployment's, which is always there and always first.
    await user.click(
      screen.getByRole("button", { name: "Save bwsalmon/grain memory" }),
    );

    expect(api).toHaveBeenCalledWith("/api/memory", {
      method: "PUT",
      body: JSON.stringify({ repo: "bwsalmon/grain", text: "new" }),
    });
  });

  // Saving an empty memory deletes the row (model.Store.PutMemory), so
  // it is the one edit here that asks first.
  it("confirms before clearing a scope's memory", async () => {
    const user = userEvent.setup();
    window.confirm.mockReturnValue(false);
    listing({ memories: [{ text: "everything" }] });
    render(<MemoryPanel showError={() => {}} />);

    await user.clear(
      await screen.findByLabelText("Memory carried by every run"),
    );
    await user.click(
      screen.getByRole("button", { name: "Save this deployment's memory" }),
    );

    expect(window.confirm).toHaveBeenCalled();
    expect(api).not.toHaveBeenCalledWith("/api/memory", expect.anything());
  });

  // Every scope has a Save and a Revert, so each says which scope it
  // writes: a bare "Save" twice over is two controls nothing reading the
  // pane by name can tell apart.
  it("names each scope's Save and Revert for that scope", async () => {
    listing({
      memories: [
        { text: "everywhere" },
        { repo: "bwsalmon/grain", text: "here" },
      ],
    });
    render(<MemoryPanel showError={() => {}} />);

    await screen.findByLabelText("Memory about bwsalmon/grain");
    expect(
      screen.getByRole("button", { name: "Save this deployment's memory" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Save bwsalmon/grain memory" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", {
        name: "Revert bwsalmon/grain memory",
      }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });

  it("puts a typed memory back the way it was when it is reverted", async () => {
    const user = userEvent.setup();
    listing({ memories: [{ text: "as stored" }] });
    render(<MemoryPanel showError={() => {}} />);

    const box = await screen.findByLabelText("Memory carried by every run");
    await user.type(box, " and typed");
    await user.click(
      screen.getByRole("button", {
        name: "Revert this deployment's memory",
      }),
    );

    expect(box).toHaveValue("as stored");
  });

  it("lists a scope's documents with their age and size", async () => {
    listing({
      documents: [
        {
          name: "flaky-e2e",
          summary: "why the third retry fails",
          length: 2048,
          updatedAt: daysAgo(4),
        },
      ],
    });
    render(<MemoryPanel showError={() => {}} />);

    expect(await screen.findByText("flaky-e2e")).toBeInTheDocument();
    expect(screen.getByText("why the third retry fails")).toBeInTheDocument();
    expect(screen.getByText("2.0 KB")).toBeInTheDocument();
    expect(screen.getByText("written 4 days ago")).toBeInTheDocument();
  });

  it("says so when a scope keeps no documents", async () => {
    listing({ memories: [{ text: "something" }] });
    render(<MemoryPanel showError={() => {}} />);

    expect(
      await screen.findByText("No documents filed under this scope."),
    ).toBeInTheDocument();
  });

  // The listing carries every document's name and nobody's text, so
  // opening one is a read of its own -- which is the whole difference
  // between the two API calls.
  it("fetches a document's text only when it is opened", async () => {
    const user = userEvent.setup();
    listing({
      documents: [
        {
          repo: "bwsalmon/granule",
          name: "vm-boot",
          summary: "how the guest comes up",
          length: 40,
        },
      ],
    });
    api.mockImplementation((path) => {
      if (path === "/api/memory") return Promise.resolve([]);
      if (path === "/api/memory/documents")
        return Promise.resolve([
          {
            repo: "bwsalmon/granule",
            name: "vm-boot",
            summary: "how the guest comes up",
            length: 40,
          },
        ]);
      return Promise.resolve({
        name: "vm-boot",
        summary: "how the guest comes up",
        text: "the guest waits for virtiofs",
      });
    });
    render(<MemoryPanel showError={() => {}} />);

    await user.click(await screen.findByRole("button", { name: /^Open / }));

    expect(api).toHaveBeenCalledWith(
      "/api/memory/documents/vm-boot?repo=bwsalmon%2Fgranule",
    );
    expect(
      await screen.findByDisplayValue("the guest waits for virtiofs"),
    ).toBeInTheDocument();
  });

  it("saves an opened document back with its summary", async () => {
    const user = userEvent.setup();
    api.mockImplementation((path, opts) => {
      if (path === "/api/memory") return Promise.resolve([]);
      if (path === "/api/memory/documents" && !opts)
        return Promise.resolve([
          { name: "flaky-e2e", summary: "old summary", length: 5 },
        ]);
      if (path === "/api/memory/documents") return Promise.resolve({});
      return Promise.resolve({ summary: "old summary", text: "old text" });
    });
    render(<MemoryPanel showError={() => {}} />);

    await user.click(await screen.findByRole("button", { name: /^Open / }));
    const text = await screen.findByDisplayValue("old text");
    await user.clear(text);
    await user.type(text, "new text");
    await user.click(
      screen.getByRole("button", { name: /^Save document flaky-e2e \(/ }),
    );

    expect(api).toHaveBeenCalledWith("/api/memory/documents", {
      method: "PUT",
      body: JSON.stringify({
        repo: "",
        name: "flaky-e2e",
        summary: "old summary",
        text: "new text",
      }),
    });
  });

  // Deleting is the same write with no text, which is how update_memory
  // deletes one too.
  it("deletes a document after confirming", async () => {
    const user = userEvent.setup();
    listing({
      documents: [{ name: "flaky-e2e", summary: "stale", length: 5 }],
    });
    render(<MemoryPanel showError={() => {}} />);

    await user.click(await screen.findByRole("button", { name: /^Delete / }));

    expect(window.confirm).toHaveBeenCalled();
    expect(api).toHaveBeenCalledWith("/api/memory/documents", {
      method: "PUT",
      body: JSON.stringify({
        repo: "",
        name: "flaky-e2e",
        summary: "",
        text: "",
      }),
    });
  });

  it("leaves a document alone when the confirmation is declined", async () => {
    const user = userEvent.setup();
    window.confirm.mockReturnValue(false);
    listing({
      documents: [{ name: "flaky-e2e", summary: "stale", length: 5 }],
    });
    render(<MemoryPanel showError={() => {}} />);

    await user.click(await screen.findByRole("button", { name: /^Delete / }));

    expect(api).not.toHaveBeenCalledWith(
      "/api/memory/documents",
      expect.anything(),
    );
  });

  // A document saved with no text is deleted on the server, so emptying
  // the box and pressing Save is Delete by another route, and asks the
  // same question before it throws the document away.
  it("asks before a save with no text deletes the document", async () => {
    const user = userEvent.setup();
    window.confirm.mockReturnValue(false);
    api.mockImplementation((path, opts) => {
      if (path === "/api/memory") return Promise.resolve([]);
      if (path === "/api/memory/documents" && !opts)
        return Promise.resolve([
          { name: "flaky-e2e", summary: "old summary", length: 5 },
        ]);
      if (path === "/api/memory/documents") return Promise.resolve({});
      return Promise.resolve({ summary: "old summary", text: "old text" });
    });
    render(<MemoryPanel showError={() => {}} />);

    await user.click(await screen.findByRole("button", { name: /^Open / }));
    const text = await screen.findByDisplayValue("old text");
    await user.clear(text);
    await user.type(text, "   ");
    const save = screen.getByRole("button", {
      name: /^Save document flaky-e2e \(/,
    });
    await user.click(save);

    expect(window.confirm).toHaveBeenCalledWith(
      expect.stringMatching(/^Delete the memory document "flaky-e2e"\?/),
    );
    expect(api).not.toHaveBeenCalledWith(
      "/api/memory/documents",
      expect.objectContaining({ method: "PUT" }),
    );
    // Declined, the edit is still on screen to be put right.
    expect(text).toHaveValue("   ");

    window.confirm.mockReturnValue(true);
    await user.click(save);
    expect(api).toHaveBeenCalledWith("/api/memory/documents", {
      method: "PUT",
      body: JSON.stringify({
        repo: "",
        name: "flaky-e2e",
        summary: "",
        text: "",
      }),
    });
  });

  // Every document row carries the same two buttons, and a name can
  // repeat across scopes: each is named for its own document and scope,
  // so the destructive one is never one of several identical "Delete"s.
  it("names each document's buttons for that document and its scope", async () => {
    const user = userEvent.setup();
    api.mockImplementation((path) => {
      if (path === "/api/memory") return Promise.resolve([]);
      if (path === "/api/memory/documents")
        return Promise.resolve([
          { name: "flaky-e2e", summary: "deployment's", length: 5 },
          {
            repo: "acme/widgets",
            name: "flaky-e2e",
            summary: "the repo's",
            length: 5,
          },
        ]);
      return Promise.resolve({ summary: "s", text: "t" });
    });
    render(<MemoryPanel showError={() => {}} />);

    const open = await screen.findByRole("button", {
      name: "Open flaky-e2e (acme/widgets)",
    });
    expect(
      screen.getByRole("button", { name: "Open flaky-e2e (this deployment)" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Delete flaky-e2e (acme/widgets)" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", {
        name: "Delete flaky-e2e (this deployment)",
      }),
    ).toBeInTheDocument();
    expect(open).toHaveAttribute("aria-expanded", "false");

    await user.click(open);

    const close = await screen.findByRole("button", {
      name: "Close flaky-e2e (acme/widgets)",
    });
    expect(close).toHaveAttribute("aria-expanded", "true");
  });

  // The open document's two fields follow the buttons: with both
  // flaky-e2e documents open there were two boxes named "Summary" and
  // two named "Document: flaky-e2e" (2026-10-05 walkthrough).
  it("names each open document's fields for that document and its scope", async () => {
    const user = userEvent.setup();
    api.mockImplementation((path) => {
      if (path === "/api/memory") return Promise.resolve([]);
      if (path === "/api/memory/documents")
        return Promise.resolve([
          { name: "flaky-e2e", summary: "deployment's", length: 5 },
          {
            repo: "acme/widgets",
            name: "flaky-e2e",
            summary: "the repo's",
            length: 5,
          },
        ]);
      return Promise.resolve({ summary: "s", text: "t" });
    });
    render(<MemoryPanel showError={() => {}} />);

    await user.click(
      await screen.findByRole("button", {
        name: "Open flaky-e2e (acme/widgets)",
      }),
    );
    await user.click(
      screen.getByRole("button", { name: "Open flaky-e2e (this deployment)" }),
    );

    for (const scope of ["acme/widgets", "this deployment"]) {
      expect(
        await screen.findByRole("textbox", {
          name: `Summary of flaky-e2e (${scope})`,
        }),
      ).toBeInTheDocument();
      expect(
        screen.getByRole("textbox", {
          name: `Document: flaky-e2e (${scope})`,
        }),
      ).toBeInTheDocument();
    }
    expect(
      screen.queryByRole("textbox", { name: "Summary" }),
    ).not.toBeInTheDocument();
  });

  it("offers a row for a repo nothing has written a memory for yet", async () => {
    const user = userEvent.setup();
    listing({});
    render(
      <MemoryPanel showError={() => {}} repos={["bwsalmon/grain-home"]} />,
    );

    await user.click(
      await screen.findByLabelText("Add a repo to write a memory for"),
    );
    await user.click(await screen.findByText("bwsalmon/grain-home"));

    const card = screen
      .getByText("bwsalmon/grain-home", { selector: "h6,p,span,div" })
      .closest("div");
    expect(card).toBeTruthy();
    expect(
      screen.getByLabelText("Memory about bwsalmon/grain-home"),
    ).toBeInTheDocument();
  });

  it("reports a failed read through the error banner", async () => {
    const showError = vi.fn();
    api.mockRejectedValue(new Error("nope"));
    render(<MemoryPanel showError={showError} />);

    await vi.waitFor(() => expect(showError).toHaveBeenCalled());
    expect(screen.queryByText("This deployment")).not.toBeInTheDocument();
  });

  it("shows nothing remembered for a scope with no memory", async () => {
    listing({});
    render(<MemoryPanel showError={() => {}} />);

    const chip = await screen.findByText("nothing remembered");
    expect(chip).toBeInTheDocument();
    expect(
      within(screen.getByText("This deployment").closest("div")).queryByText(
        /written/,
      ),
    ).toBeNull();
  });

  // Memory is the deployment's (single-tenant reset, stage 4d): a repo
  // the picker does not offer still has its notes listed, since they are
  // still in that repo's runs' prompts.
  it("lists every repo's notes, not only the repos it offers", async () => {
    listing({
      memories: [
        { text: "deployment note" },
        { repo: "team-a/allowed", text: "team-a note" },
        { repo: "team-b/other", text: "other repo note" },
      ],
      documents: [
        { repo: "team-a/allowed", name: "allowed-doc", summary: "sum1" },
        { repo: "team-b/other", name: "other-doc", summary: "sum2" },
      ],
    });
    render(<MemoryPanel showError={() => {}} repos={["team-a/allowed"]} />);

    expect(await screen.findByText("This deployment")).toBeInTheDocument();
    expect(screen.getByDisplayValue("team-a note")).toBeInTheDocument();
    expect(screen.getByText("allowed-doc")).toBeInTheDocument();
    expect(screen.getByDisplayValue("other repo note")).toBeInTheDocument();
    expect(screen.getByText("other-doc")).toBeInTheDocument();
  });

  // grain/task-2414: every box here saves only on its own Save, inside a
  // pane whose other fields save as they are left -- so leaving the pane
  // (or a document's Close) threw a half-written correction away unasked.
  describe("unsaved edits", () => {
    const withDocument = () =>
      api.mockImplementation((path, opts) => {
        if (path === "/api/memory")
          return Promise.resolve([{ repo: "acme/widgets", text: "old note" }]);
        if (path === "/api/memory/documents" && !opts)
          return Promise.resolve([
            {
              repo: "acme/widgets",
              name: "flaky-e2e",
              summary: "old summary",
              length: 8,
            },
          ]);
        if (opts) return Promise.resolve({});
        return Promise.resolve({ summary: "old summary", text: "old text" });
      });

    it("reports a memory edited and not saved, and not once it is saved", async () => {
      const user = userEvent.setup();
      withDocument();
      const unsavedRef = { current: null };
      render(<MemoryPanel showError={() => {}} unsavedRef={unsavedRef} />);

      const box = await screen.findByDisplayValue("old note");
      expect(unsavedRef.current()).toBe(false);
      await user.type(box, " corrected");
      expect(unsavedRef.current()).toBe(true);
      await user.click(
        screen.getByRole("button", { name: "Save acme/widgets memory" }),
      );
      expect(unsavedRef.current()).toBe(false);
    });

    it("reports an open document edited and not saved, and not once it is saved", async () => {
      const user = userEvent.setup();
      withDocument();
      const unsavedRef = { current: null };
      render(<MemoryPanel showError={() => {}} unsavedRef={unsavedRef} />);

      await user.click(
        await screen.findByRole("button", {
          name: "Open flaky-e2e (acme/widgets)",
        }),
      );
      const text = await screen.findByDisplayValue("old text");
      expect(unsavedRef.current()).toBe(false);
      await user.type(text, " corrected");
      expect(unsavedRef.current()).toBe(true);
      await user.click(
        screen.getByRole("button", {
          name: "Save document flaky-e2e (acme/widgets)",
        }),
      );
      expect(unsavedRef.current()).toBe(false);
    });

    it("asks before Close drops an edited document, and keeps it open when declined", async () => {
      const user = userEvent.setup();
      withDocument();
      window.confirm.mockReturnValue(false);
      render(<MemoryPanel showError={() => {}} />);

      await user.click(
        await screen.findByRole("button", {
          name: "Open flaky-e2e (acme/widgets)",
        }),
      );
      await user.type(await screen.findByDisplayValue("old text"), " more");
      await user.click(
        screen.getByRole("button", { name: "Close flaky-e2e (acme/widgets)" }),
      );

      expect(window.confirm).toHaveBeenCalledWith(
        "You have unsaved changes here. Leave and discard them?",
      );
      expect(screen.getByDisplayValue("old text more")).toBeInTheDocument();
    });

    it("closes an unedited document without asking", async () => {
      const user = userEvent.setup();
      withDocument();
      render(<MemoryPanel showError={() => {}} />);

      await user.click(
        await screen.findByRole("button", {
          name: "Open flaky-e2e (acme/widgets)",
        }),
      );
      await screen.findByDisplayValue("old text");
      await user.click(
        screen.getByRole("button", { name: "Close flaky-e2e (acme/widgets)" }),
      );

      expect(window.confirm).not.toHaveBeenCalled();
      expect(screen.queryByDisplayValue("old text")).toBeNull();
    });
  });

  // The panel sits on Team settings → Models, so "the standing
  // instructions on the Models tab" pointed at the tab it was on, which
  // has none: they are a system admin's, on System → Agents & Task
  // Branches (2026-10-13 walkthrough).
  it("points at where standing instructions are actually edited", async () => {
    api.mockImplementation(() => Promise.resolve([]));
    render(<MemoryPanel showError={vi.fn()} />);
    const intro = await screen.findByText(/What grain has learned/);
    expect(intro).toHaveTextContent(
      "standing instructions (Settings → Agents)",
    );
    expect(intro).not.toHaveTextContent("Models tab");
  });
});
