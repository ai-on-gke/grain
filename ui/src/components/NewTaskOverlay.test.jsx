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

// Filing a task from the New task form: what a bare submit sends, the
// boxes the deployment's own defaults arrive already set, which end of
// the backlog the task joins, and what the form does when the request
// fails.
//
// This file and its three siblings were one 1,369-line file until
// grain/task-169, and the reason for splitting it is the same one that
// split App.test.jsx (see its header, and appTestHarness.js's note on
// what a query costs): a test in here pays
//
//     (renders it causes) x (CSS rules in the document)
//
// and both halves grow as a file runs. MUI's emotion runtime inserts one
// <style> element per rule the first time some component needs it and
// never takes one out, so every picker a test opens leaves its rules
// behind for every test after it -- this form starts at 406 rules on
// mount and the old single file ended at 590. jsdom answers
// getComputedStyle by matching every one of those rules against the
// element, which is what makes each call cost about 11ms by the end.
// And the calls are not mostly the tests' own queries: the Description
// box is a MUI TextareaAutosize, which measures itself with
// getComputedStyle in a layout effect on every render, and every
// keystroke typed into any field re-renders the whole form. Measured on
// the old file: 45% of its 74s inside getComputedStyle, three quarters
// of that under TextareaAutosize rather than under any query.
//
// One rule follows from each half, and both are worth keeping to:
//
//   - A test file gets its own jsdom document, so a file only pays for
//     the rules of the pickers it opens itself. Keep a new test with the
//     file whose area it belongs to rather than starting a fifth, and
//     watch what a file opens -- it is the union of the pickers, not the
//     count of the tests, that sets the price.
//   - Type short strings into the form. What the text says is arbitrary
//     everywhere but the payload-shape test below, which keeps a real
//     title to show one making the round trip, and every character of it
//     is another render of the whole form: "Fix the thing" costs ten
//     renders more than "Fix", a fifth of a second, for nothing.
//
// Nothing has been dropped or narrowed to make any of this cheaper: the
// same forty tests, the same interactions, the same assertions.
//
// The other three, all of them rendering the same <NewTaskOverlay />:
//
//   NewTaskOverlay.test.jsx               filing a task, and the form itself
//   NewTaskOverlay.repos.test.jsx         target repo, read-only repos, Base branch
//   NewTaskOverlay.capabilities.test.jsx  the capability and depends-on pickers
//   NewTaskOverlay.advanced.test.jsx      everything behind Advanced options

import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import NewTaskOverlay from "./NewTaskOverlay.jsx";
import api from "../api.js";
import fileToAttachment from "../attachments.js";
import { TASK_DRAFT_STORAGE_KEY } from "../taskDraft.js";

vi.mock("../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
// fileToAttachment reads a real File with FileReader -- mocked the same
// way api.js is, so a test that attaches a file exercises the component's
// own wiring without depending on jsdom's own FileReader behaviour.
vi.mock("../attachments.js", () => ({ default: vi.fn() }));

describe("NewTaskOverlay", () => {
  afterEach(() => {
    localStorage.clear();
    api.mockClear();
  });

  it("submits a minimal task with the expected defaults", async () => {
    const onCreated = vi.fn().mockResolvedValue(undefined);
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        onClose={onClose}
        onCreated={onCreated}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Fix the thing");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(api).toHaveBeenCalledWith("/api/tasks", {
      method: "POST",
      body: JSON.stringify({
        title: "Fix the thing",
        description: "",
        repo: "",
        noRepo: true,
        base: "",
        autoMerge: false,
        sandboxCpus: 0,
        sandboxMemoryMb: 0,
        sandboxDiskGb: 0,
        model: "",
        agentBackendId: "",
        networkAccess: "",
        nothingNeededAction: "close",
        promptExtension: "",
        reviewTemplateId: "",
        dependsOn: [],
        reads: [],
        writes: [],
        approved: false,
        atFront: false,
        attachments: [],
      }),
    });
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onCreated).toHaveBeenCalledTimes(1);
  });

  // A /new link whose values the form drops says so beside the fields
  // rather than filing a different task without a word (2026-10-30
  // intake walkthrough). The values it keeps raise nothing.
  it("says which of a link's field values it left out", () => {
    const config = {
      customFields: [
        {
          name: "priority",
          label: "Priority",
          type: "enum",
          options: ["low", "high"],
        },
      ],
    };
    const { unmount } = render(
      <NewTaskOverlay
        config={config}
        fields={{ priority: "bogus", nosuch: "1" }}
        onClose={() => {}}
        onCreated={() => {}}
        showError={() => {}}
      />,
    );
    const notice = screen.getByTestId("unfiled-link-values");
    expect(notice).toHaveTextContent(
      "Priority: “bogus” is not one of its values",
    );
    expect(notice).toHaveTextContent(
      "“nosuch” is not a field on this deployment",
    );
    unmount();

    render(
      <NewTaskOverlay
        config={config}
        fields={{ priority: "high" }}
        onClose={() => {}}
        onCreated={() => {}}
        showError={() => {}}
      />,
    );
    expect(screen.queryByTestId("unfiled-link-values")).toBeNull();
  });

  // grain/task-3451: a task filed to fix a red base branch says so, so
  // grain never makes it wait on that branch's own repair task. A task
  // with no repo has no base to fix, and is not offered the box.
  it("files a task marked as fixing its red base branch", async () => {
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={{ targetRepos: ["acme/widgets"] }}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "De flake tests");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    expect(screen.queryByLabelText(/This fixes a red/)).toBeNull();
    await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/widgets");
    await user.click(screen.getByLabelText(/This fixes a red main/));
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const create = api.mock.calls.find(([url]) => url === "/api/tasks");
    expect(JSON.parse(create[1].body).fixesBaseBranch).toBe(true);
  });

  // bwsalmon/agents#612: both checkboxes are seeded from the
  // deployment's own defaults (GET /api/config), which are on unless an
  // operator has turned one off -- so the ordinary task files queued and
  // set to auto-merge without anyone touching either box.
  it("seeds Queue immediately and Auto-merge from the deployment's defaults", async () => {
    const config = { approvedByDefault: true, autoMergeByDefault: true };
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={config}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    expect(screen.getByLabelText(/Queue immediately/)).toBeChecked();
    expect(screen.getByLabelText(/Auto-merge once checks pass/)).toBeChecked();

    await user.type(screen.getByLabelText(/Title/), "Fix");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const payload = JSON.parse(api.mock.calls[0][1].body);
    expect(payload.approved).toBe(true);
    expect(payload.autoMerge).toBe(true);
  });

  // grain/task-202: which end of the backlog a new task joins is a
  // choice on this form rather than only a setting buried in Settings,
  // and the form opens on the end the last task added went to --
  // config.newestFirst, which the server rewrites on every filing that
  // states one (ui.CreateTaskRequest.AtFront).
  it("seeds the backlog-end picker from what the last task added chose", async () => {
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={{ newestFirst: true }}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    expect(screen.getByLabelText("Add to backlog")).toHaveTextContent(/^Front/);

    await user.type(screen.getByLabelText(/Title/), "Fix");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(JSON.parse(api.mock.calls[0][1].body).atFront).toBe(true);
  });

  it("files at the other end of the backlog when that end is picked", async () => {
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={{ newestFirst: true }}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Fix");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByLabelText("Add to backlog"));
    await user.click(screen.getByRole("option", { name: /^End/ }));
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(JSON.parse(api.mock.calls[0][1].body).atFront).toBe(false);
  });

  it("prevents another task while creation is still in progress", async () => {
    api.mockImplementationOnce(() => new Promise(() => {}));
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Fix");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    const createButton = screen.getByRole("button", { name: "Create task" });
    await user.click(createButton);

    expect(createButton).toBeDisabled();
    fireEvent.click(createButton);
    expect(api).toHaveBeenCalledTimes(1);
  });

  // Found in the 2026-10-15 intake walkthrough: the form opened with
  // focus on nothing a person could type into, so a title typed straight
  // after "+ New task" was lost.
  it("opens with the Title box focused, so typing starts the title", async () => {
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    expect(screen.getByLabelText(/Title/)).toHaveFocus();
    await user.keyboard("Fix");
    expect(screen.getByLabelText(/Title/)).toHaveValue("Fix");
  });

  it("greys out Create task until the title and target repo are both filled", async () => {
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );
    const createButton = screen.getByRole("button", { name: "Create task" });

    expect(createButton).toBeDisabled();
    await user.type(screen.getByLabelText(/Title/), "Fix");
    expect(createButton).toBeDisabled();
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    expect(createButton).toBeEnabled();

    // Going back to the empty option reinstates the requirement to name
    // a repo: "no repo" is a choice made in this same list, not the
    // absence of one.
    await user.selectOptions(screen.getByLabelText(/^Repos/), "");
    expect(createButton).toBeDisabled();
  });

  it("reads and includes a picked file as an attachment", async () => {
    const upload = {
      filename: "screenshot.png",
      contentType: "image/png",
      content: "ZmFrZQ==",
    };
    fileToAttachment.mockResolvedValueOnce(upload);
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Fix");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    // grain/task-85: the paperclip sits inside the description box --
    // the same element the field itself is in -- rather than on a row of
    // its own beneath it.
    expect(
      screen.getByRole("button", { name: "Attach files" }).parentElement,
    ).toContainElement(screen.getByLabelText("Description"));
    const file = new File(["fake"], "screenshot.png", { type: "image/png" });
    await user.upload(document.querySelector('input[type="file"]'), file);
    expect(await screen.findByText("screenshot.png")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(fileToAttachment).toHaveBeenCalledWith(file);
    const payload = JSON.parse(api.mock.calls[0][1].body);
    expect(payload.attachments).toEqual([upload]);
  });

  it("attaches an image pasted into the description field", async () => {
    const upload = {
      filename: "pasted.png",
      contentType: "image/png",
      content: "cGFzdGVk",
    };
    fileToAttachment.mockResolvedValueOnce(upload);
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Paste task");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");

    const file = new File(["pasted"], "pasted.png", { type: "image/png" });
    const descriptionBox = screen.getByLabelText("Description");
    fireEvent.paste(descriptionBox, {
      clipboardData: {
        items: [
          {
            kind: "file",
            type: "image/png",
            getAsFile: () => file,
          },
        ],
      },
    });

    expect(await screen.findByText("pasted.png")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(fileToAttachment).toHaveBeenCalledWith(file);
    const payload = JSON.parse(api.mock.calls[0][1].body);
    expect(payload.attachments).toEqual([upload]);
  });

  it("reports the error and leaves the overlay open when the request fails", async () => {
    api.mockRejectedValueOnce(new Error("title is required"));
    const showError = vi.fn();
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        onClose={onClose}
        onCreated={() => {}}
        showError={showError}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "x");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(showError).toHaveBeenCalledWith(
      expect.objectContaining({ message: "title is required" }),
    );
    expect(onClose).not.toHaveBeenCalled();
  });

  it("reports an attachment that cannot be read and lets the form be submitted again", async () => {
    fileToAttachment.mockRejectedValueOnce(new Error("file is gone"));
    const showError = vi.fn();
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        onClose={onClose}
        onCreated={() => {}}
        showError={showError}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "x");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    const file = new File(["fake"], "gone.png", { type: "image/png" });
    await user.upload(document.querySelector('input[type="file"]'), file);
    await user.click(screen.getByRole("button", { name: "Create task" }));

    expect(showError).toHaveBeenCalledWith(
      expect.objectContaining({ message: "file is gone" }),
    );
    expect(api).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Create task" })).toBeEnabled();
  });

  // The two halves of src/identifierField.js's rule, on the one form that
  // carries both kinds of box: Base branch holds a name git has to match
  // exactly, so a phone must neither turn "main" into "Main" nor decide
  // a branch name was a typo for a word it knows; Title holds a sentence
  // somebody is writing, and helping with that is the whole point of the
  // defaults.
  it("keeps a phone keyboard off Base branch, and leaves it on Title", () => {
    render(
      <NewTaskOverlay
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    const branch = screen.getByLabelText(/Base branch/);
    expect(branch).toHaveAttribute("autocapitalize", "off");
    expect(branch).toHaveAttribute("autocorrect", "off");

    const title = screen.getByLabelText(/Title/);
    expect(title).not.toHaveAttribute("autocapitalize");
    expect(title).not.toHaveAttribute("autocorrect");
  });

  it("persists typed draft to localStorage and restores it when reopened", async () => {
    const user = userEvent.setup();
    const { unmount } = render(
      <NewTaskOverlay
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    expect(
      screen.queryByRole("button", { name: "Discard draft" }),
    ).not.toBeInTheDocument();

    await user.type(screen.getByLabelText(/Title/), "My draft");
    await user.type(screen.getByLabelText("Description"), "Draft details");

    expect(
      screen.getByRole("button", { name: "Discard draft" }),
    ).toBeInTheDocument();

    const stored = JSON.parse(localStorage.getItem(TASK_DRAFT_STORAGE_KEY));
    expect(stored.title).toBe("My draft");
    expect(stored.description).toBe("Draft details");

    unmount();

    // Reopening the overlay without prefill restores the draft
    render(
      <NewTaskOverlay
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    expect(screen.getByLabelText(/Title/)).toHaveValue("My draft");
    expect(screen.getByLabelText("Description")).toHaveValue("Draft details");
    expect(
      screen.getByRole("button", { name: "Discard draft" }),
    ).toBeInTheDocument();
  });

  it("discards draft and resets form when Discard draft is clicked", async () => {
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Draft to discard");
    await user.type(screen.getByLabelText("Description"), "Some notes");
    expect(localStorage.getItem(TASK_DRAFT_STORAGE_KEY)).not.toBeNull();

    const discardBtn = screen.getByRole("button", { name: "Discard draft" });
    await user.click(discardBtn);

    expect(screen.getByLabelText(/Title/)).toHaveValue("");
    expect(screen.getByLabelText("Description")).toHaveValue("");
    expect(localStorage.getItem(TASK_DRAFT_STORAGE_KEY)).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Discard draft" }),
    ).not.toBeInTheDocument();
  });

  it("clears stored draft when task is created successfully", async () => {
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "Draft created");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    expect(localStorage.getItem(TASK_DRAFT_STORAGE_KEY)).not.toBeNull();

    await user.click(screen.getByRole("button", { name: "Create task" }));
    expect(api).toHaveBeenCalledTimes(1);
    expect(localStorage.getItem(TASK_DRAFT_STORAGE_KEY)).toBeNull();
  });

  it("prioritizes prefill over stored draft", () => {
    localStorage.setItem(
      TASK_DRAFT_STORAGE_KEY,
      JSON.stringify({
        title: "Draft Title",
        description: "Draft Description",
      }),
    );

    render(
      <NewTaskOverlay
        config={null}
        prefill={{ title: "Prefill Title", description: "Prefill Description" }}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    expect(screen.getByLabelText(/Title/)).toHaveValue("Prefill Title");
    expect(screen.getByLabelText("Description")).toHaveValue(
      "Prefill Description",
    );
  });

  // grain/task-1585: a draft started from one repo's page and reopened
  // from another's. The repo it was opened with was only that page's
  // default, so the page in front of the operator wins and what they
  // typed comes with it; a repo they picked by hand is kept, and the
  // form says so rather than quietly filing somewhere else.
  describe("a draft reopened from a different repo's page", () => {
    const config = { targetRepos: ["acme/gadgets", "acme/widgets"] };
    const open = (defaultRepo) =>
      render(
        <NewTaskOverlay
          config={config}
          defaultRepo={defaultRepo}
          onClose={() => {}}
          onCreated={() => Promise.resolve()}
          showError={() => {}}
        />,
      );

    it("files into the page's repo when the draft's was only its page default", async () => {
      const user = userEvent.setup();
      const { unmount } = open("acme/gadgets");
      expect(screen.getByLabelText(/^Repos/)).toHaveValue("acme/gadgets");
      await user.type(screen.getByLabelText(/Title/), "Gadgets draft");
      await user.type(screen.getByLabelText("Base branch"), "feature/x");
      const stored = JSON.parse(localStorage.getItem(TASK_DRAFT_STORAGE_KEY));
      expect(stored.repo).toBe("acme/gadgets");
      expect(stored.repoEdited).toBe(false);
      unmount();

      open("acme/widgets");
      expect(screen.getByLabelText(/^Repos/)).toHaveValue("acme/widgets");
      expect(screen.getByLabelText(/Title/)).toHaveValue("Gadgets draft");
      // Typed by hand, so it stays -- as it does when the repo is
      // re-picked on the form itself.
      expect(screen.getByLabelText("Base branch")).toHaveValue("feature/x");
      expect(screen.queryByTestId("draft-repo-notice")).not.toBeInTheDocument();

      await user.click(screen.getByRole("button", { name: "Create task" }));
      const body = JSON.parse(api.mock.calls.at(-1)[1].body);
      expect(body.repo).toBe("acme/widgets");
    });

    it("keeps a picked repo and says so, until the draft is discarded", async () => {
      const user = userEvent.setup();
      const { unmount } = open("acme/widgets");
      await user.type(screen.getByLabelText(/Title/), "Gadgets draft");
      await user.selectOptions(screen.getByLabelText(/^Repos/), "acme/gadgets");
      expect(
        JSON.parse(localStorage.getItem(TASK_DRAFT_STORAGE_KEY)).repoEdited,
      ).toBe(true);
      unmount();

      open("acme/widgets");
      expect(screen.getByLabelText(/^Repos/)).toHaveValue("acme/gadgets");
      expect(screen.getByTestId("draft-repo-notice")).toHaveTextContent(
        "Restored a draft for acme/gadgets. Discard it to file in acme/widgets.",
      );

      await user.click(screen.getByRole("button", { name: "Discard draft" }));
      expect(screen.getByLabelText(/^Repos/)).toHaveValue("acme/widgets");
      expect(screen.getByLabelText(/Title/)).toHaveValue("");
      expect(screen.queryByTestId("draft-repo-notice")).not.toBeInTheDocument();
    });

    it("reads a draft saved before repoEdited existed as picked", () => {
      localStorage.setItem(
        TASK_DRAFT_STORAGE_KEY,
        JSON.stringify({ title: "Old draft", repo: "acme/gadgets" }),
      );
      open("acme/widgets");
      expect(screen.getByLabelText(/^Repos/)).toHaveValue("acme/gadgets");
      expect(screen.getByTestId("draft-repo-notice")).toBeInTheDocument();
    });
  });

  it("round-trips person-set reviewers through the draft and submit payload", async () => {
    const user = userEvent.setup();
    const { unmount } = render(
      <NewTaskOverlay
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    await user.type(screen.getByLabelText(/Title/), "CL task");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.type(screen.getByLabelText("Reviewers"), "alice, bob");
    unmount();

    render(
      <NewTaskOverlay
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );
    expect(screen.getByLabelText("Reviewers")).toHaveValue("alice, bob");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const body = JSON.parse(api.mock.calls.at(-1)[1].body);
    expect(body.reviewers).toEqual(["alice", "bob"]);
  });

  it("defaults Task priority to 5 and sends a custom priority in the payload", async () => {
    const user = userEvent.setup();
    render(
      <NewTaskOverlay
        config={null}
        onClose={() => {}}
        onCreated={() => Promise.resolve()}
        showError={() => {}}
      />,
    );

    const priorityInput = screen.getByLabelText("Task priority");
    expect(priorityInput).toHaveValue(5);

    await user.type(screen.getByLabelText(/Title/), "Urgent fix");
    await user.selectOptions(screen.getByLabelText(/^Repos/), "none");
    await user.clear(priorityInput);
    await user.type(priorityInput, "8");
    await user.click(screen.getByRole("button", { name: "Create task" }));

    const body = JSON.parse(api.mock.calls.at(-1)[1].body);
    expect(body.priority).toBe(8);
  });
});
