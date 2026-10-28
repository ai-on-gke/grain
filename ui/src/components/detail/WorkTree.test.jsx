import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import DetailOverlay from "../DetailOverlay.jsx";
import api from "../../api.js";
import { resetDetailMocks, baseTask, config } from "./testFixtures.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));
vi.mock("../../attachments.js", () => ({ default: vi.fn() }));

// GET /api/tasks/{id}/tree's own shape (pkg/ui/work_tree.go): a chat at
// the root, two tasks it filed, and one filed by the second of those.
const tree = {
  origin: "task:12",
  rootTask: "12",
  done: 1,
  total: 3,
  nodes: [
    {
      id: "12",
      title: "Director Session",
      status: "running",
      done: false,
      root: true,
      children: [
        {
          id: "13",
          title: "Bump the Go toolchain",
          status: "merged",
          pullRequest: "acme/widgets#101",
          done: true,
        },
        {
          id: "14",
          title: "Add pagination",
          status: "PR open",
          pullRequest: "acme/widgets#104",
          done: false,
          children: [
            {
              id: "15",
              title: "Extract the query helper",
              status: "queued",
              done: false,
            },
          ],
        },
      ],
    },
  ],
};

function renderPane(task, onOpenTask = vi.fn()) {
  render(
    <DetailOverlay
      task={task}
      tasks={[]}
      config={config}
      onClose={() => {}}
      onOpenTask={onOpenTask}
      act={vi.fn()}
    />,
  );
  return onOpenTask;
}

describe("DetailOverlay work tree", () => {
  afterEach(resetDetailMocks);

  it("starts collapsed by default and expands on click to draw the task's tree nested, with links to each task and pull request and the n-of-m line", async () => {
    api.mockImplementation((path) =>
      Promise.resolve(path === "/api/tasks/14/tree" ? tree : null),
    );
    const user = userEvent.setup();
    const onOpenTask = renderPane({
      ...baseTask,
      id: "14",
      origin: "task:12",
    });

    const section = await screen.findByRole("region", { name: "Work tree" });
    expect(section).toHaveTextContent("Work from task 12: 1 of 3 done");

    // Collapsed by default: the summary toggle is shown, but neither the
    // provenance links nor the nested TaskTree rows are rendered yet.
    const toggle = within(section).getByRole("button", {
      name: /Work from task 12: 1 of 3 done/,
    });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(section.querySelector('li[data-task-id="12"]')).toBeNull();
    expect(screen.queryByText("Add pagination")).toBeNull();

    // Expanding shows the provenance links and the nested task tree.
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");

    // Nested: 15 sits inside 14, which sits inside the root, 12.
    const root = section.querySelector('li[data-task-id="12"]');
    const pagination = root.querySelector('li[data-task-id="14"]');
    expect(pagination.querySelector('li[data-task-id="15"]')).not.toBeNull();
    expect(
      root.querySelector('li[data-task-id="13"] li[data-task-id="15"]'),
    ).toBeNull();

    const row = within(pagination.querySelector(".task-tree-row"));
    expect(row.getByText("Add pagination")).toBeInTheDocument();
    expect(row.getByText("PR open")).toBeInTheDocument();
    expect(row.getByRole("link", { name: "acme/widgets#104" })).toHaveAttribute(
      "href",
      "https://github.com/acme/widgets/pull/104",
    );
    expect(
      within(root.querySelector('li[data-task-id="13"]')).getByLabelText(
        "done",
      ),
    ).toBeInTheDocument();
    // The task being read is the one marked as here.
    expect(pagination.querySelector(".task-tree-row")).toHaveClass(
      "task-tree-current",
    );
    // ...and said to be, for a screen reader, on that row alone.
    expect(pagination.querySelector(".task-tree-row")).toHaveAttribute(
      "aria-current",
      "true",
    );
    expect(root.querySelectorAll('[aria-current="true"]')).toHaveLength(1);

    await user.click(
      within(root.querySelector('li[data-task-id="15"]')).getByRole("link", {
        name: "15",
      }),
    );
    expect(onOpenTask).toHaveBeenCalledWith("15");
  });

  it("resets the expanded work tree state when switching tasks", async () => {
    api.mockImplementation((path) =>
      Promise.resolve(
        path === "/api/tasks/14/tree" || path === "/api/tasks/15/tree"
          ? tree
          : null,
      ),
    );
    const user = userEvent.setup();
    const { rerender } = render(
      <DetailOverlay
        task={{ ...baseTask, id: "14", origin: "task:12" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    const section = await screen.findByRole("region", { name: "Work tree" });
    await user.click(within(section).getByRole("button"));
    expect(section.querySelector('li[data-task-id="12"]')).not.toBeNull();

    rerender(
      <DetailOverlay
        task={{ ...baseTask, id: "15", origin: "task:12" }}
        tasks={[]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );

    const nextSection = await screen.findByRole("region", {
      name: "Work tree",
    });
    expect(within(nextSection).getByRole("button")).toHaveAttribute(
      "aria-expanded",
      "false",
    );
    expect(nextSection.querySelector('li[data-task-id="12"]')).toBeNull();
  });

  it("asks about the tree of a task that has filed work, though it has no origin, and refreshes when another child task arrives", async () => {
    let currentTree = {
      ...tree,
      done: 0,
      total: 1,
    };
    api.mockImplementation((path) =>
      Promise.resolve(path === "/api/tasks/12/tree" ? currentTree : null),
    );
    const { rerender } = render(
      <DetailOverlay
        task={{ ...baseTask, id: "12" }}
        tasks={[{ ...baseTask, id: "13", generatedFrom: "12" }]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    const section = await screen.findByRole("region", { name: "Work tree" });
    expect(section).toHaveTextContent("0 of 1 done");
    expect(section.querySelector('li[data-task-id="12"]')).toBeNull();

    currentTree = tree;
    rerender(
      <DetailOverlay
        task={{ ...baseTask, id: "12" }}
        tasks={[
          { ...baseTask, id: "13", generatedFrom: "12", state: "closed" },
          { ...baseTask, id: "14", generatedFrom: "12", state: "running" },
        ]}
        config={config}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    await vi.waitFor(() => expect(section).toHaveTextContent("1 of 3 done"));
  });

  it("neither asks nor draws for a task in no tree", () => {
    renderPane(baseTask);
    expect(api).not.toHaveBeenCalledWith(`/api/tasks/${baseTask.id}/tree`);
    expect(screen.queryByRole("region", { name: "Work tree" })).toBeNull();
    expect(
      screen.queryByRole("button", { name: /Provenance/ }),
    ).not.toBeInTheDocument();
  });

  it("draws nothing when the tree has nothing in it but the task", async () => {
    api.mockImplementation((path) =>
      Promise.resolve(
        path === "/api/tasks/14/tree"
          ? {
              origin: "task:14",
              rootTask: "14",
              done: 0,
              total: 0,
              nodes: [{ id: "14", title: "x", status: "queued" }],
            }
          : null,
      ),
    );
    renderPane({ ...baseTask, id: "14", origin: "task:14" });
    await vi.waitFor(() =>
      expect(api).toHaveBeenCalledWith("/api/tasks/14/tree"),
    );
    expect(screen.queryByRole("region", { name: "Work tree" })).toBeNull();
  });
});
