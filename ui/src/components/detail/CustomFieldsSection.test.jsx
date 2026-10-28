import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import DetailOverlay from "../DetailOverlay.jsx";
import { baseTask } from "./testFixtures.js";

const customFieldConfig = {
  customFields: [
    {
      name: "customer",
      label: "Customer",
      kind: "string",
    },
  ],
};

describe("CustomFieldsSection unsaved edits", () => {
  it("asks before Escape discards edited custom fields, and stays when told no", async () => {
    const onClose = vi.fn();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, customFields: { customer: "Acme" } }}
        tasks={[]}
        config={customFieldConfig}
        onClose={onClose}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Edit fields" }));
    const input = screen.getByLabelText("Customer");
    await user.clear(input);
    await user.type(input, "Globex");

    await user.keyboard("{Escape}");

    expect(confirm).toHaveBeenCalledWith(
      "You have unsaved changes here. Leave and discard them?",
    );
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Customer")).toBeInTheDocument();
    confirm.mockRestore();
  });

  it("closes when discarding edited custom fields is confirmed", async () => {
    const onClose = vi.fn();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, customFields: { customer: "Acme" } }}
        tasks={[]}
        config={customFieldConfig}
        onClose={onClose}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Edit fields" }));
    const input = screen.getByLabelText("Customer");
    await user.clear(input);
    await user.type(input, "Globex");

    await user.keyboard("{Escape}");

    expect(confirm).toHaveBeenCalledWith(
      "You have unsaved changes here. Leave and discard them?",
    );
    expect(onClose).toHaveBeenCalledTimes(1);
    confirm.mockRestore();
  });

  it("does not ask on Escape when custom fields editor is open but untouched", async () => {
    const onClose = vi.fn();
    const confirm = vi.spyOn(window, "confirm");
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, customFields: { customer: "Acme" } }}
        tasks={[]}
        config={customFieldConfig}
        onClose={onClose}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Edit fields" }));
    await user.keyboard("{Escape}");

    expect(confirm).not.toHaveBeenCalled();
    confirm.mockRestore();
  });

  it("does not ask on Escape after canceling field edits via Cancel button", async () => {
    const onClose = vi.fn();
    const confirm = vi.spyOn(window, "confirm");
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, customFields: { customer: "Acme" } }}
        tasks={[]}
        config={customFieldConfig}
        onClose={onClose}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Edit fields" }));
    const input = screen.getByLabelText("Customer");
    await user.clear(input);
    await user.type(input, "Globex");
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    await user.keyboard("{Escape}");

    expect(confirm).not.toHaveBeenCalled();
    confirm.mockRestore();
  });

  it("opens the new-task form on a plain click of 'File a task with these fields', and leaves a modified click to the browser", async () => {
    const onNewTaskFrom = vi.fn();
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, customFields: { customer: "Acme" } }}
        tasks={[]}
        config={customFieldConfig}
        onClose={() => {}}
        onOpenTask={() => {}}
        onNewTaskFrom={onNewTaskFrom}
        act={vi.fn()}
      />,
    );

    const link = screen.getByRole("link", {
      name: "File a task with these fields",
    });
    await user.click(link);
    expect(onNewTaskFrom).toHaveBeenCalledWith({ customer: "Acme" });

    onNewTaskFrom.mockClear();
    const root = link.closest("body > div");
    let leftToBrowser;
    const follow = (e) => {
      leftToBrowser = !e.defaultPrevented;
      e.preventDefault();
    };
    root.addEventListener("click", follow);
    fireEvent.click(link, { ctrlKey: true });
    root.removeEventListener("click", follow);
    expect(leftToBrowser).toBe(true);
    expect(onNewTaskFrom).not.toHaveBeenCalled();
  });

  it("keeps the custom fields editor open with typed values when save fails", async () => {
    const act = vi.fn().mockResolvedValue(false);
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{ ...baseTask, customFields: { customer: "Acme" } }}
        tasks={[]}
        config={customFieldConfig}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Edit fields" }));
    const input = screen.getByLabelText("Customer");
    await user.clear(input);
    await user.type(input, "Globex");
    await user.click(screen.getByRole("button", { name: "Save fields" }));

    expect(act).toHaveBeenCalledTimes(1);
    expect(screen.getByLabelText("Customer")).toHaveValue("Globex");
  });

  it("guards in-pane task and new-task navigation when task fields or custom fields have unsaved edits", async () => {
    const onOpenTask = vi.fn();
    const onNewTaskFrom = vi.fn();
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(false);
    const user = userEvent.setup();
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          customFields: { customer: "Acme" },
          dependsOn: ["99"],
        }}
        tasks={[{ id: "99", title: "Blocker task", state: "queued" }]}
        config={customFieldConfig}
        onClose={() => {}}
        onOpenTask={onOpenTask}
        onNewTaskFrom={onNewTaskFrom}
        act={vi.fn()}
      />,
    );

    // Dirty EditTaskForm and verify "File a task with these fields" asks before leaving.
    await user.click(screen.getByRole("button", { name: "Edit" }));
    const titleInput = screen.getByLabelText("Title");
    await user.type(titleInput, " (edited)");

    await user.click(
      screen.getByRole("link", { name: "File a task with these fields" }),
    );
    expect(confirm).toHaveBeenCalledWith(
      "You have unsaved changes here. Leave and discard them?",
    );
    expect(onNewTaskFrom).not.toHaveBeenCalled();

    // Dirty CustomFieldsSection too and verify clicking a blocker chip in Dependencies asks before leaving.
    confirm.mockClear();
    await user.click(screen.getByRole("button", { name: "Edit fields" }));
    const customerInput = screen.getByLabelText("Customer");
    await user.clear(customerInput);
    await user.type(customerInput, "Initech");

    await user.click(screen.getByRole("button", { name: /99 Blocker task/ }));
    expect(confirm).toHaveBeenCalledWith(
      "You have unsaved changes here. Leave and discard them?",
    );
    expect(onOpenTask).not.toHaveBeenCalled();

    // Once confirmed, navigation proceeds.
    confirm.mockReturnValue(true);
    await user.click(screen.getByRole("button", { name: /99 Blocker task/ }));
    expect(onOpenTask).toHaveBeenCalledWith("99");
    confirm.mockRestore();
  });
});
