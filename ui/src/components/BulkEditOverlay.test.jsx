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
import { afterEach, describe, expect, it, vi } from "vitest";
import BulkEditOverlay from "./BulkEditOverlay.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn() }));

const config = { targetRepos: ["acme/widgets", "acme/gadgets"] };
const templates = [{ id: "tmpl-1", name: "Careful review" }];

// Two tasks that agree on their repo and disagree on everything else --
// the shape the pane is written for, since a selection made off a list
// is mostly "these five, in this repo" (see BulkEditOverlay's own doc
// comment on what pre-filling means here).
const selection = [
  { id: "1", repo: "acme/widgets", base: "main", state: "queued" },
  { id: "2", repo: "acme/widgets", base: "release/2", state: "queued" },
];

function open(props = {}) {
  return render(
    <BulkEditOverlay
      selection={selection}
      tasks={selection}
      config={config}
      templates={templates}
      onClose={() => {}}
      onRun={() => {}}
      {...props}
    />,
  );
}

const tickField = (user, name) =>
  user.click(screen.getByRole("checkbox", { name: new RegExp(`^${name}`) }));

describe("BulkEditOverlay", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    api.mockReset();
  });

  it("says how many tasks the edit is about", () => {
    open();
    expect(screen.getByText(/2 selected tasks/)).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Apply to 2 tasks" }),
    ).toBeInTheDocument();
  });

  it("marks the fields the selection disagrees on, and not the ones it shares", () => {
    open();
    expect(
      screen.getByRole("checkbox", { name: /^Base branch \(mixed/ }),
    ).toBeInTheDocument();
    expect(screen.getByRole("checkbox", { name: "Repo" })).toBeInTheDocument();
  });

  it("pre-fills a field the whole selection agrees on", async () => {
    const user = userEvent.setup();
    open({
      selection: [
        { id: "1", repo: "acme/widgets", base: "release/2" },
        { id: "2", repo: "acme/gadgets", base: "release/2" },
      ],
    });

    await tickField(user, "Base branch");

    expect(screen.getByRole("textbox")).toHaveValue("release/2");
  });

  it("applies nothing until a field is ticked", async () => {
    const user = userEvent.setup();
    open();

    const apply = screen.getByRole("button", { name: "Apply to 2 tasks" });
    expect(apply).toBeDisabled();

    await tickField(user, "Base branch");

    expect(apply).toBeEnabled();
  });

  it("PATCHes only the ticked fields, once per selected task", async () => {
    const user = userEvent.setup();
    const onRun = vi.fn((mutate) => {
      mutate("1");
      mutate("2");
    });
    const onClose = vi.fn();
    open({ onRun, onClose });

    await tickField(user, "Base branch");
    const base = screen.getByRole("textbox");
    await user.clear(base);
    await user.type(base, "main");
    await user.click(screen.getByRole("button", { name: "Apply to 2 tasks" }));

    expect(onRun).toHaveBeenCalledTimes(1);
    // One PATCH per selected task and nothing else of the kind. The
    // branch box also *reads* -- it asks the repo the selection shares
    // which branches grain knows about there, to suggest them
    // (BranchField.jsx) -- so the count is of the edits.
    expect(
      api.mock.calls.filter(([, opts]) => opts?.method === "PATCH"),
    ).toHaveLength(2);
    expect(api).toHaveBeenCalledWith("/api/tasks/1", {
      method: "PATCH",
      body: JSON.stringify({ base: "main" }),
    });
    expect(api).toHaveBeenCalledWith("/api/tasks/2", {
      method: "PATCH",
      body: JSON.stringify({ base: "main" }),
    });
    expect(onClose).toHaveBeenCalled();
  });

  // An emptied box is a real edit -- "back to the repo's default
  // branch" -- and reaches the server as one, which is the whole reason
  // each field has a tick of its own rather than being inferred from
  // whether it looks filled in.
  it("sends an emptied field as the clearing edit it is", async () => {
    const user = userEvent.setup();
    const onRun = vi.fn((mutate) => mutate("1"));
    open({
      selection: [{ id: "1", repo: "acme/widgets", base: "release/2" }],
      onRun,
    });

    await tickField(user, "Base branch");
    await user.clear(screen.getByRole("textbox"));
    await user.click(screen.getByRole("button", { name: "Apply to 1 task" }));

    expect(api).toHaveBeenCalledWith("/api/tasks/1", {
      method: "PATCH",
      body: JSON.stringify({ base: "" }),
    });
  });

  it("refuses to apply an emptied repo, which no task can be dispatched with", async () => {
    const user = userEvent.setup();
    open({
      selection: [{ id: "1", repo: "acme/widgets" }],
      // No repo options at all, so RepoField falls back to its text
      // input and the box can actually be emptied.
      config: {},
      tasks: [],
    });

    await tickField(user, "Repo");
    await user.clear(screen.getByRole("textbox"));

    expect(
      screen.getByRole("button", { name: "Apply to 1 task" }),
    ).toBeDisabled();
    expect(screen.getByText(/cannot be dispatched/)).toBeInTheDocument();
  });

  it("sets auto-merge on every selected task", async () => {
    const user = userEvent.setup();
    const onRun = vi.fn((mutate) => mutate("1"));
    open({ onRun });

    await tickField(user, "Auto-merge");
    await user.click(screen.getByRole("combobox", { name: "Auto-merge" }));
    await user.click(screen.getByRole("option", { name: "On" }));
    await user.click(screen.getByRole("button", { name: "Apply to 2 tasks" }));

    expect(api).toHaveBeenCalledWith("/api/tasks/1", {
      method: "PATCH",
      body: JSON.stringify({ autoMerge: true }),
    });
  });

  it("attaches a review from a template to the whole selection", async () => {
    const user = userEvent.setup();
    const onRun = vi.fn((mutate) => mutate("7"));
    open({ onRun });

    await tickField(user, "Review");
    await user.click(screen.getByRole("combobox", { name: "Review" }));
    await user.click(screen.getByRole("option", { name: "Careful review" }));
    await user.click(screen.getByRole("button", { name: "Apply to 2 tasks" }));

    expect(api).toHaveBeenCalledWith("/api/tasks/7", {
      method: "PATCH",
      body: JSON.stringify({ reviewTemplateId: "tmpl-1" }),
    });
  });

  // 2026-11-01 intake walkthrough O3: reprioritising a batch took one
  // Edit per task.
  it("sets one task priority on every selected task and nothing else", async () => {
    const user = userEvent.setup();
    const onRun = vi.fn((mutate) => {
      mutate("1");
      mutate("2");
    });
    open({
      onRun,
      selection: [
        { id: "1", repo: "acme/widgets", priority: 3 },
        { id: "2", repo: "acme/widgets" },
      ],
    });

    expect(
      screen.getByRole("checkbox", { name: /^Task priority \(mixed/ }),
    ).toBeInTheDocument();
    await tickField(user, "Task priority");
    const box = screen.getByRole("spinbutton", { name: "Task priority" });
    expect(box).toHaveValue(null);
    await user.type(box, "8");
    await user.click(screen.getByRole("button", { name: "Apply to 2 tasks" }));

    for (const id of ["1", "2"]) {
      expect(api).toHaveBeenCalledWith(`/api/tasks/${id}`, {
        method: "PATCH",
        body: JSON.stringify({ priority: 8 }),
      });
    }
  });

  it("pre-fills a shared priority, unset reading as the default 5", async () => {
    const user = userEvent.setup();
    open({
      selection: [
        { id: "1", repo: "acme/widgets", priority: 5 },
        { id: "2", repo: "acme/widgets" },
      ],
    });

    expect(
      screen.getByRole("checkbox", { name: "Task priority" }),
    ).toBeInTheDocument();
    await tickField(user, "Task priority");
    expect(
      screen.getByRole("spinbutton", { name: "Task priority" }),
    ).toHaveValue(5);
  });

  it.each(["", "0", "2.5", "-1"])(
    "refuses to apply a priority of %j",
    async (typed) => {
      const user = userEvent.setup();
      open();

      await tickField(user, "Task priority");
      const box = screen.getByRole("spinbutton", { name: "Task priority" });
      await user.clear(box);
      if (typed) await user.type(box, typed);

      expect(
        screen.getByRole("button", { name: "Apply to 2 tasks" }),
      ).toBeDisabled();
      expect(screen.getByText(/a whole number, 1 or more/)).toBeInTheDocument();
    },
  );

  // grain/task-110: moving a batch onto one model, and optionally one
  // connection -- a connection kept out of the rotation is precisely the
  // one an operator sends a hand-picked batch to.
  it("moves every selected task onto one model and connection", async () => {
    const user = userEvent.setup();
    const onRun = vi.fn((mutate) => mutate("1"));
    open({
      onRun,
      config: {
        ...config,
        deployment: { id: "default", defaultModel: "gpt-5.1" },
        agentBackends: [
          {
            id: "codex-day",
            name: "Day account",
            framework: "codex",
            models: ["gpt-5.1"],
          },
          {
            id: "claude-deep",
            name: "Deep account",
            framework: "claude",
            models: ["claude-opus-5"],
            disabled: true,
          },
        ],
      },
    });

    await tickField(user, "Model");
    await user.click(screen.getByRole("combobox", { name: "Model" }));
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "Deployment default (gpt-5.1)",
      "gpt-5.1",
      "claude-opus-5 (disabled connections only)",
    ]);
    await user.click(
      screen.getByRole("option", {
        name: "claude-opus-5 (disabled connections only)",
      }),
    );
    await user.click(
      screen.getByRole("combobox", { name: "Specific connection" }),
    );
    // Only the connections that serve the chosen model.
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual([
      "Any enabled connection serving the model",
      "Deep account · Claude · disabled, pin only",
    ]);
    await user.click(
      screen.getByRole("option", {
        name: "Deep account · Claude · disabled, pin only",
      }),
    );
    await user.click(screen.getByRole("button", { name: "Apply to 2 tasks" }));

    expect(api).toHaveBeenCalledWith("/api/tasks/1", {
      method: "PATCH",
      body: JSON.stringify({
        model: "claude-opus-5",
        agentBackendId: "claude-deep",
      }),
    });
  });

  // Nothing to pick between where no models are configured, so the row
  // stays off the pane rather than offering an empty list.
  it("leaves the model row out where no models are configured", () => {
    open();
    expect(
      screen.queryByRole("checkbox", { name: /^Model/ }),
    ).not.toBeInTheDocument();
  });

  // Both pickers draw a line of their own while nothing is picked --
  // "Deployment default (...)" and "No review" -- and MUI floats a label
  // clear of the box only for a field it counts as filled, which a
  // select holding "" never is. Left alone the label lands on that line,
  // on the same baseline, and the field reads as two strings on top of
  // each other; data-shrink is the label's own account of where it is.
  // The same assertion guards the same two pickers on the new-task form
  // (NewTaskOverlay.test.jsx) -- this pane grew its own copies of them
  // beside that fix rather than after it.
  it("keeps the labels clear of the defaults the pickers show", async () => {
    const user = userEvent.setup();
    open();

    await tickField(user, "Agent framework");
    await tickField(user, "Review");

    for (const name of ["Agent framework", "Review"]) {
      // By role: the tick that opens each row carries the field's name
      // as its own label too, so "the thing labelled Review" is two
      // controls here and only one of them is the select.
      const label = screen
        .getByRole("combobox", { name })
        .closest(".MuiFormControl-root")
        .querySelector("label");
      expect(label).toHaveAttribute("data-shrink", "true");
    }
  });

  it("warns that a repo or base change is refused for the runs already up", async () => {
    const user = userEvent.setup();
    open({
      selection: [
        { id: "1", repo: "acme/widgets", base: "main", state: "running" },
        { id: "2", repo: "acme/widgets", base: "main", state: "queued" },
      ],
    });

    expect(screen.queryByText(/will be refused/)).toBeNull();

    await tickField(user, "Base branch");

    // ui.Client.UpdateTask refuses the running one's PATCH (grain/task-2310),
    // so the form must not promise it reaches that task's next run.
    expect(
      screen.getByText(
        /1 of these is running now\. A repo or base change will be refused for it until its run ends, and the rest are changed\./,
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/reaches each task.s next run/)).toBeNull();
  });

  // The fields a deployment declares for itself (model.CustomField):
  // the half of a task a selection most often has in common, and the
  // reason a bulk edit is reached for at all ("these six are all for
  // Globex").
  describe("declared fields", () => {
    const fieldConfig = {
      targetRepos: ["acme/widgets"],
      customFields: [
        {
          name: "priority",
          label: "Priority",
          type: "enum",
          options: ["low", "high"],
        },
        { name: "customer", type: "text" },
        { name: "teams", type: "text", multiple: true },
        { name: "escalated", label: "Escalated", type: "boolean" },
      ],
    };
    const withFields = [
      {
        id: "1",
        repo: "acme/widgets",
        customFields: { priority: "low", customer: "acme", teams: ["ui"] },
      },
      {
        id: "2",
        repo: "acme/widgets",
        customFields: { priority: "high", customer: "acme" },
      },
    ];
    const openFields = (props = {}) =>
      open({
        config: fieldConfig,
        selection: withFields,
        tasks: withFields,
        ...props,
      });

    it("offers no fields section on a deployment that declares none", () => {
      open();
      expect(screen.queryByText("Fields")).toBeNull();
    });

    it("marks a field the selection disagrees on, and pre-fills one it shares", async () => {
      const user = userEvent.setup();
      openFields();

      expect(
        screen.getByRole("checkbox", { name: /^Priority \(mixed/ }),
      ).toBeInTheDocument();

      await tickField(user, "customer");

      expect(screen.getByRole("textbox", { name: "customer" })).toHaveValue(
        "acme",
      );
    });

    // The body carries every field the task has a value for, not just
    // the ticked one: PATCH reads the map as the task's whole answer
    // about its declared fields, so a body naming only `priority` would
    // set a priority on both and wipe the customer off both
    // (ui.mergeCustomFields).
    it("sets a field on every selected task without disturbing the rest", async () => {
      const user = userEvent.setup();
      const onRun = vi.fn((mutate) => {
        mutate("1");
        mutate("2");
      });
      openFields({ onRun });

      await tickField(user, "Priority");
      await user.click(screen.getByRole("combobox", { name: "Priority" }));
      await user.click(screen.getByRole("option", { name: "high" }));
      await user.click(
        screen.getByRole("button", { name: "Apply to 2 tasks" }),
      );

      expect(api).toHaveBeenCalledWith("/api/tasks/1", {
        method: "PATCH",
        body: JSON.stringify({
          customFields: {
            priority: "high",
            customer: "acme",
            teams: ["ui"],
          },
        }),
      });
      expect(api).toHaveBeenCalledWith("/api/tasks/2", {
        method: "PATCH",
        body: JSON.stringify({
          customFields: { priority: "high", customer: "acme" },
        }),
      });
    });

    it("clears a field ticked and left empty", async () => {
      const user = userEvent.setup();
      const onRun = vi.fn((mutate) => mutate("1"));
      openFields({ onRun });

      await tickField(user, "customer");
      await user.clear(screen.getByRole("textbox", { name: "customer" }));
      await user.click(
        screen.getByRole("button", { name: "Apply to 2 tasks" }),
      );

      expect(api).toHaveBeenCalledWith("/api/tasks/1", {
        method: "PATCH",
        body: JSON.stringify({
          customFields: { priority: "low", customer: "", teams: ["ui"] },
        }),
      });
    });

    // A set is replaced rather than added to, which is what every other
    // row in this pane does -- and the only reading under which a bulk
    // edit can take a value off a selection.
    it("replaces the whole set of a multi-valued field", async () => {
      const user = userEvent.setup();
      const onRun = vi.fn((mutate) => mutate("1"));
      openFields({ onRun });

      await tickField(user, "teams");
      await user.type(
        screen.getByRole("combobox", { name: "teams" }),
        "web{enter}",
      );
      await user.click(
        screen.getByRole("button", { name: "Apply to 2 tasks" }),
      );

      expect(api).toHaveBeenCalledWith("/api/tasks/1", {
        method: "PATCH",
        body: JSON.stringify({
          customFields: { priority: "low", customer: "acme", teams: ["web"] },
        }),
      });
    });

    // A boolean's own input elsewhere is a checkbox, which under this
    // pane's tick would be a second checkbox saying something else --
    // and could not say "clear it" at all.
    it("offers a boolean as a picker, cleared value included", async () => {
      const user = userEvent.setup();
      const onRun = vi.fn((mutate) => mutate("2"));
      openFields({ onRun });

      await tickField(user, "Escalated");
      await user.click(screen.getByRole("combobox", { name: "Escalated" }));
      await user.click(screen.getByRole("option", { name: "Yes" }));
      await user.click(
        screen.getByRole("button", { name: "Apply to 2 tasks" }),
      );

      expect(api).toHaveBeenCalledWith("/api/tasks/2", {
        method: "PATCH",
        body: JSON.stringify({
          customFields: {
            priority: "high",
            customer: "acme",
            escalated: "true",
          },
        }),
      });
    });

    // A declaration that changed under values already stored: the pane
    // restates every unticked field, so what it restates has to be the
    // whole of what each task holds. Keeping the first value of a set
    // left behind by a field that has stopped being `multiple` deleted
    // the rest off every task in the selection, and restating a retired
    // enum option used to fail the whole edit (model.
    // NormalizeCustomFieldValuesFrom).
    describe("over values a changed declaration stranded", () => {
      const stranded = {
        targetRepos: ["acme/widgets"],
        customFields: [
          {
            name: "priority",
            label: "Priority",
            type: "enum",
            options: ["low", "high"],
          },
          // `teams` has stopped holding a set, and the tasks below were
          // filed while it did.
          { name: "teams", type: "text" },
        ],
      };
      const carrying = [
        {
          id: "1",
          repo: "acme/widgets",
          customFields: { priority: "critical", teams: ["ui", "daemon"] },
        },
        {
          id: "2",
          repo: "acme/widgets",
          customFields: { priority: "critical", teams: ["ui", "daemon"] },
        },
      ];

      it("restates every value an untouched field holds", async () => {
        const user = userEvent.setup();
        const onRun = vi.fn((mutate) => mutate("1"));
        open({
          config: stranded,
          selection: carrying,
          tasks: carrying,
          onRun,
        });

        await tickField(user, "Base branch");
        await user.type(
          screen.getByRole("textbox", { name: "Base branch" }),
          "main",
        );
        await tickField(user, "Priority");
        await user.click(screen.getByRole("combobox", { name: "Priority" }));
        await user.click(screen.getByRole("option", { name: "high" }));
        await user.click(
          screen.getByRole("button", { name: "Apply to 2 tasks" }),
        );

        expect(api).toHaveBeenCalledWith("/api/tasks/1", {
          method: "PATCH",
          body: JSON.stringify({
            base: "main",
            customFields: { priority: "high", teams: ["ui", "daemon"] },
          }),
        });
      });

      it("shows a stranded set rather than one value out of it", async () => {
        const user = userEvent.setup();
        open({ config: stranded, selection: carrying, tasks: carrying });

        await tickField(user, "teams");

        expect(screen.getByText("ui")).toBeInTheDocument();
        expect(screen.getByText("daemon")).toBeInTheDocument();
        expect(screen.getByText(/remove all but one/)).toBeInTheDocument();
      });

      it("shows a retired option the whole selection holds", async () => {
        const user = userEvent.setup();
        const onRun = vi.fn((mutate) => mutate("2"));
        open({
          config: stranded,
          selection: carrying,
          tasks: carrying,
          onRun,
        });

        await tickField(user, "Priority");
        expect(
          screen.getByRole("combobox", { name: "Priority" }),
        ).toHaveTextContent("critical");

        // Applying without touching it restates what those tasks
        // already carry, which is exactly what the server grandfathers.
        await user.click(
          screen.getByRole("button", { name: "Apply to 2 tasks" }),
        );
        expect(api).toHaveBeenCalledWith("/api/tasks/2", {
          method: "PATCH",
          body: JSON.stringify({
            customFields: { priority: "critical", teams: ["ui", "daemon"] },
          }),
        });
      });
    });

    // A field declared `repo` is a field, not the tick on the built-in
    // row of that name (taskAttributes.js's CUSTOM_PREFIX).
    it("keeps a field named after a built-in row apart from it", async () => {
      const user = userEvent.setup();
      const onRun = vi.fn((mutate) => mutate("1"));
      openFields({
        config: {
          targetRepos: ["acme/widgets"],
          customFields: [{ name: "repo", label: "Upstream", type: "text" }],
        },
        onRun,
      });

      await tickField(user, "Upstream");
      await user.type(
        screen.getByRole("textbox", { name: "Upstream" }),
        "acme/upstream",
      );
      await user.click(
        screen.getByRole("button", { name: "Apply to 2 tasks" }),
      );

      expect(api).toHaveBeenCalledWith("/api/tasks/1", {
        method: "PATCH",
        body: JSON.stringify({ customFields: { repo: "acme/upstream" } }),
      });
    });

    // A row the list has stopped reporting between opening the form and
    // pressing Apply: its values are no longer known here, and a body
    // built without them would clear the fields this edit was never
    // about, so it gets the rest of the edit and none of the fields.
    it("leaves the fields of a row that has left the selection alone", async () => {
      const user = userEvent.setup();
      const onRun = vi.fn((mutate) => mutate("gone"));
      openFields({ onRun });

      await tickField(user, "Base branch");
      await user.type(
        screen.getByRole("textbox", { name: "Base branch" }),
        "main",
      );
      await tickField(user, "Priority");
      await user.click(screen.getByRole("combobox", { name: "Priority" }));
      await user.click(screen.getByRole("option", { name: "high" }));
      await user.click(
        screen.getByRole("button", { name: "Apply to 2 tasks" }),
      );

      expect(api).toHaveBeenCalledWith("/api/tasks/gone", {
        method: "PATCH",
        body: JSON.stringify({ base: "main" }),
      });
    });

    it("sends the ticked built-in fields alongside them", async () => {
      const user = userEvent.setup();
      const onRun = vi.fn((mutate) => mutate("2"));
      openFields({ onRun });

      await tickField(user, "Base branch");
      await user.type(
        screen.getByRole("textbox", { name: "Base branch" }),
        "main",
      );
      await tickField(user, "customer");
      await user.clear(screen.getByRole("textbox", { name: "customer" }));
      await user.type(
        screen.getByRole("textbox", { name: "customer" }),
        "globex",
      );
      await user.click(
        screen.getByRole("button", { name: "Apply to 2 tasks" }),
      );

      expect(api).toHaveBeenCalledWith("/api/tasks/2", {
        method: "PATCH",
        body: JSON.stringify({
          base: "main",
          customFields: { priority: "high", customer: "globex" },
        }),
      });
    });
  });

  it("leaves without sending anything when cancelled", async () => {
    const user = userEvent.setup();
    const onRun = vi.fn();
    const onClose = vi.fn();
    open({ onRun, onClose });

    await tickField(user, "Base branch");
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(onRun).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalled();
  });

  it("asks before discarding ticked fields on Escape or Close dialog, and closes unprompted when clean", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(false);
    open({ onClose });

    // No fields ticked yet: Escape closes without asking.
    await user.keyboard("{Escape}");
    expect(confirmSpy).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalledTimes(1);

    onClose.mockClear();
    await tickField(user, "Base branch");

    // Declining confirm keeps the dialog open.
    await user.keyboard("{Escape}");
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(onClose).not.toHaveBeenCalled();

    // Accepting confirm closes the dialog.
    confirmSpy.mockReturnValueOnce(true);
    await user.click(screen.getByRole("button", { name: "Close dialog" }));
    expect(confirmSpy).toHaveBeenCalledTimes(2);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  // 2026-10-23 intake walkthrough (task 271): the Repo picker and every
  // row's mixed note read live state, so a repo typed into "Other…" was
  // what Choose from list and a re-tick fell back to, and an edited then
  // unticked mixed field lost its note.
  it("falls back to the selection's own repo after a typed one is abandoned", async () => {
    const user = userEvent.setup();
    const onRun = vi.fn();
    open({ onRun });

    await tickField(user, "Repo");
    await user.selectOptions(screen.getByRole("combobox"), "Other…");
    await user.type(screen.getByPlaceholderText("owner/name"), "custom/bulk");
    await user.click(screen.getByRole("button", { name: "Choose from list" }));
    expect(screen.getByRole("combobox")).toHaveValue("acme/widgets");

    await user.selectOptions(screen.getByRole("combobox"), "Other…");
    await user.type(screen.getByPlaceholderText("owner/name"), "custom/bulk");
    await tickField(user, "Repo");
    await tickField(user, "Repo");
    expect(screen.queryByPlaceholderText("owner/name")).not.toBeInTheDocument();
    expect(screen.getByRole("combobox")).toHaveValue("acme/widgets");

    api.mockResolvedValue({});
    await user.click(screen.getByRole("button", { name: "Apply to 2 tasks" }));
    const run = onRun.mock.calls[0][0];
    await run("1");
    expect(JSON.parse(api.mock.calls[0][1].body)).toEqual({
      repo: "acme/widgets",
    });
  });

  it("keeps a mixed field's note after it is edited and unticked", async () => {
    const user = userEvent.setup();
    open();
    await tickField(user, "Base branch");
    await user.clear(screen.getByRole("textbox"));
    await user.type(screen.getByRole("textbox"), "main");
    await tickField(user, "Base branch");
    expect(
      screen.getByRole("checkbox", { name: /^Base branch \(mixed/ }),
    ).toBeInTheDocument();
  });
});
