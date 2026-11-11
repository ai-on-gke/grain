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

import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import DetailOverlay from "./DetailOverlay.jsx";
import InboxPage from "./InboxPage.jsx";
import PullRequestPage from "./PullRequestPage.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({
  default: vi.fn(() => Promise.resolve({})),
}));

const baseTask = {
  id: "101",
  title: "Harden auth middleware",
  repo: "acme/widgets",
  base: "main",
  branch: "grain/task-101",
  state: "awaiting_submit",
  pullRequest: "acme/widgets#42",
  autoMerge: false,
  approved: true,
  attempts: [],
  comments: [],
};

describe("Merge gates UI", () => {
  beforeEach(() => {
    api.mockClear();
    api.mockImplementation((path) => {
      if (String(path).includes("/compare?")) {
        return Promise.resolve({
          base: "main",
          head: "grain/task-101",
          merged: false,
          diff: "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n",
        });
      }
      if (String(path) === "/api/local-script-approvals") {
        return Promise.resolve([]);
      }
      return Promise.resolve({});
    });
  });

  it("lists every gate on the task pane, labels Submit accurately while security review runs, and replaces misleading hold wording", async () => {
    const user = userEvent.setup();
    const act = vi.fn((fn) => fn());
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          ci: {
            commit: "1234567890abcdef",
            checks: [{ name: "test", status: "passing" }],
          },
          securityHold: "touches pkg/auth",
          securityReviewTask: "102",
        }}
        tasks={[]}
        config={{ scopes: ["tasks:write", "merge:force"] }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    const gates = screen.getByTestId("merge-gates");
    expect(within(gates).getByTestId("merge-gate-ci")).toHaveTextContent(
      "CI \u2713",
    );
    expect(
      within(gates).getByTestId("merge-gate-security-review"),
    ).toHaveTextContent("Security review: running");
    expect(within(gates).getByTestId("merge-gate-approval")).toHaveTextContent(
      "Approval: needed",
    );

    const hold = screen.getByTestId("security-hold");
    expect(hold).not.toHaveTextContent(/without waiting/i);
    expect(hold).toHaveTextContent(
      /Submit records your approval and merges automatically once the security review and CI pass/i,
    );

    await user.click(
      screen.getByRole("button", {
        name: "Submit (merges when the reviews pass)",
      }),
    );
    expect(api).toHaveBeenCalledWith("/api/tasks/101/submit", {
      method: "POST",
    });
  });

  it("hides Submit once human approval is recorded and requires explicit PR + reason confirmation for gate override", async () => {
    const user = userEvent.setup();
    const act = vi.fn((fn) => fn());
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          ci: {
            commit: "abcdef1234567890",
            checks: [{ name: "test", status: "passing" }],
          },
          humanApprovedBy: "alice",
          securityReviewTask: "102",
          reviewFindings: [
            {
              gate: "security-review",
              reviewTask: "102",
              summary: "unescaped header value",
            },
          ],
          humanReviewHold: [
            {
              source: "security-findings",
              category: "security-findings",
              reason: "security review #102 left findings",
              reviewTask: "102",
            },
          ],
        }}
        tasks={[]}
        config={{ scopes: ["tasks:write", "merge:force"] }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={act}
      />,
    );

    const gates = screen.getByTestId("merge-gates");
    expect(
      within(gates).getByTestId("merge-gate-security-review"),
    ).toHaveTextContent("Security review: findings");
    expect(within(gates).getByTestId("merge-gate-approval")).toHaveTextContent(
      "Approval: given by alice",
    );
    expect(screen.queryByTestId("submit-task")).not.toBeInTheDocument();
    expect(
      screen.queryByTestId("release-human-review"),
    ).not.toBeInTheDocument();

    // Explicit override button is distinct and gated behind confirmation modal.
    await user.click(
      screen.getByRole("button", { name: "Merge without the security review" }),
    );
    const dialog = screen.getByTestId("gate-override-dialog");
    expect(
      within(dialog).getByTestId("override-skipped-gates"),
    ).toHaveTextContent("Security review: findings");
    const confirmBtn = within(dialog).getByTestId("confirm-gate-override");
    expect(confirmBtn).toBeDisabled();

    // Wrong PR number keeps confirm disabled.
    const prInput = within(dialog).getByTestId("override-pr-input");
    const reasonInput = within(dialog).getByTestId("override-reason-input");
    await user.type(prInput, "99");
    await user.type(reasonInput, "mitigated at ingress");
    expect(confirmBtn).toBeDisabled();

    // Whitespace-only reason keeps confirm disabled even when PR matches.
    await user.clear(prInput);
    await user.type(prInput, "42");
    await user.clear(reasonInput);
    await user.type(reasonInput, "   ");
    expect(confirmBtn).toBeDisabled();

    // Matching PR + non-empty reason sends explicit overrideGates.
    await user.clear(reasonInput);
    await user.type(reasonInput, "mitigated at ingress");
    expect(confirmBtn).toBeEnabled();
    await user.click(confirmBtn);

    expect(api).toHaveBeenCalledWith("/api/tasks/101/force-merge", {
      method: "POST",
      body: JSON.stringify({
        reason: "mitigated at ingress",
        head: "abcdef1234567890",
        overrideGates: ["security-review"],
      }),
    });
  });

  it("hides gate override from users without merge:force scope", () => {
    render(
      <DetailOverlay
        task={{
          ...baseTask,
          securityHold: "touches pkg/auth",
          securityReviewTask: "102",
        }}
        tasks={[]}
        config={{
          adminScoped: true,
          adminRepos: [],
          scopes: ["tasks:write"],
        }}
        onClose={() => {}}
        onOpenTask={() => {}}
        act={vi.fn()}
      />,
    );
    expect(
      screen.queryByRole("button", {
        name: "Merge without the security review",
      }),
    ).not.toBeInTheDocument();
  });

  it("renders merge gates on PullRequestPage and records overrides prominently in Inbox", async () => {
    const overriddenTask = {
      ...baseTask,
      state: "closed",
      merged: true,
      gateOverride: {
        gates: ["security-review"],
        by: "bob",
        reason: "emergency production hotfix",
      },
    };

    render(
      <PullRequestPage
        repo="acme/widgets"
        comparison={{ base: "main", head: "grain/task-101" }}
        tasks={[overriddenTask]}
        onOpenTask={() => {}}
        onBack={() => {}}
        showError={() => {}}
      />,
    );
    const prGates = await screen.findByTestId("pr-merge-gates");
    expect(
      within(prGates).getByTestId("merge-gate-security-review"),
    ).toHaveTextContent("Security review: overridden by bob");
    expect(
      within(prGates).getByTestId("gate-override-banner"),
    ).toHaveTextContent("emergency production hotfix");

    render(
      <InboxPage
        tasks={[overriddenTask]}
        config={{ scopes: ["tasks:write", "merge:force"] }}
        onOpenTask={() => {}}
        onAct={vi.fn()}
        onActBatch={vi.fn()}
      />,
    );
    const inboxOverrides = screen.getByTestId("inbox-gate-overrides");
    expect(inboxOverrides).toHaveTextContent("Harden auth middleware");
    expect(inboxOverrides).toHaveTextContent(
      "Overridden by bob (security-review): emergency production hotfix",
    );
  });
});
