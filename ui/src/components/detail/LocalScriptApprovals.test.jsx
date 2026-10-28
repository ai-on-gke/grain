import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import LocalScriptApprovals, {
  callLine,
  stdinLine,
} from "./LocalScriptApprovals.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn(() => Promise.resolve({})) }));

const waiting = {
  id: "lsa-1",
  taskId: "42",
  call: "1",
  tool: "local_deploy",
  script: "deploy",
  args: ["main", "$(rm -rf /)"],
  stdinBytes: 0,
  status: "pending",
};

describe("LocalScriptApprovals", () => {
  it("shows the call exactly, every argument quoted", () => {
    expect(callLine(waiting)).toBe('deploy "main" "$(rm -rf /)"');
    expect(callLine({ ...waiting, args: [], stdinBytes: 12 })).toBe(
      "deploy (and 12 bytes on stdin)",
    );
  });

  it("shows the start of stdin and the hash of all of it", () => {
    const a = {
      ...waiting,
      stdinBytes: 6000,
      stdinPreview: "deploy everything\n",
      stdinTruncated: true,
      stdinSha256: "abc123",
    };
    expect(stdinLine(a)).toBe(
      "On stdin: 6000 bytes, sha256 abc123 (truncated: first 18 of 6000 bytes shown)",
    );
    const { container } = render(
      <LocalScriptApprovals
        t={{ id: "42", localScriptApprovals: [a] }}
        act={vi.fn()}
      />,
    );
    expect(container.querySelector(".local-script-stdin").textContent).toBe(
      "deploy everything\n\n[… truncated]",
    );
    expect(
      container.querySelector(".local-script-stdin-line").textContent,
    ).toContain("sha256 abc123");
  });

  it("shows no stdin block for a call with none", () => {
    const { container } = render(
      <LocalScriptApprovals
        t={{ id: "42", localScriptApprovals: [waiting] }}
        act={vi.fn()}
      />,
    );
    expect(container.querySelector(".local-script-stdin")).toBeNull();
  });

  it("renders nothing for a task with no waiting call", () => {
    const { container } = render(
      <LocalScriptApprovals
        t={{
          id: "42",
          localScriptApprovals: [{ ...waiting, status: "approved" }],
        }}
        act={vi.fn()}
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("approves and denies through the task's answer route", async () => {
    const act = vi.fn((fn) => fn());
    const user = userEvent.setup();
    render(
      <LocalScriptApprovals
        t={{ id: "42", localScriptApprovals: [waiting] }}
        act={act}
      />,
    );
    await user.click(screen.getByRole("button", { name: "Approve" }));
    expect(api).toHaveBeenLastCalledWith(
      "/api/tasks/42/local-script-approvals/lsa-1/answer",
      { method: "POST", body: JSON.stringify({ approve: true }) },
    );
    await user.click(screen.getByRole("button", { name: "Deny" }));
    expect(api).toHaveBeenLastCalledWith(
      "/api/tasks/42/local-script-approvals/lsa-1/answer",
      { method: "POST", body: JSON.stringify({ approve: false }) },
    );
  });
});
