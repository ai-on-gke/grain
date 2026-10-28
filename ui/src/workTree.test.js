import { describe, expect, it } from "vitest";
import { forestFromTasks } from "./workTree.js";

const shape = (nodes) =>
  nodes
    .map((n) => n.id + (n.children.length ? `(${shape(n.children)})` : ""))
    .join(" ");

describe("forestFromTasks", () => {
  it("nests rows under the task that filed them, newest root first", () => {
    const rows = [
      { id: "2", title: "chat", state: "running" },
      { id: "9", title: "c", state: "queued", generatedFrom: "3" },
      { id: "3", title: "a", state: "completed", generatedFrom: "2" },
      { id: "4", title: "b", state: "proposed", generatedFrom: "2" },
      { id: "10", title: "lone", state: "queued" },
      // Its parent is not among the rows (narrowed away): a root.
      { id: "7", title: "orphan", state: "queued", generatedFrom: "1" },
    ];
    const forest = forestFromTasks(rows);
    expect(shape(forest)).toBe("10 7 2(3(9) 4)");
    const done = forest[2].children[0];
    expect(done.done).toBe(true);
    expect(done.state).toBeTruthy();
    expect(done.status).toBeTruthy();
  });

  it("keeps the rows' own order when they arrive sorted", () => {
    const rows = [
      { id: "2", title: "chat", state: "running" },
      { id: "4", title: "b", state: "proposed", generatedFrom: "2" },
      { id: "3", title: "a", state: "completed", generatedFrom: "2" },
      { id: "10", title: "lone", state: "queued" },
    ];
    expect(shape(forestFromTasks(rows, { ordered: true }))).toBe("2(4 3) 10");
    expect(shape(forestFromTasks(rows))).toBe("10 2(3 4)");
  });

  it("still draws rows that file each other in a loop", () => {
    const forest = forestFromTasks([
      { id: "5", title: "a", state: "queued", generatedFrom: "6" },
      { id: "6", title: "b", state: "queued", generatedFrom: "5" },
    ]);
    expect(shape(forest)).toBe("5(6)");
  });
});
