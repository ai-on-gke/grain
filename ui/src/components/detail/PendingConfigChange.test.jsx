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
import PendingConfigChange from "./PendingConfigChange.jsx";
import api from "../../api.js";

vi.mock("../../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));

const diff = `diff --git a/settings/templates.yml b/settings/templates.yml
index 1111111..2222222 100644
--- a/settings/templates.yml
+++ b/settings/templates.yml
@@ -1,2 +1,2 @@
 templates:
-  max_parallel: 2
+  max_parallel: 4
`;

const pending = (over = {}) => ({
  branch: "grain/config/42/raise-parallelism",
  baseSha: "a".repeat(40),
  headSha: "b".repeat(40),
  summary: "Raise max_parallel to 4",
  files: ["settings/templates.yml"],
  diff,
  validation: { ok: true, warnings: ["Secret gcp-keys/new-sa is not set yet"] },
  ...over,
});

// act runs what it is handed, the way DetailOverlay's does.
const act = vi.fn(async (fn) => fn());

describe("PendingConfigChange", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it("renders nothing when no change is pending", () => {
    const { container } = render(
      <PendingConfigChange t={{ id: "42" }} act={act} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the summary, file links, validation and approves the commit shown", async () => {
    const user = userEvent.setup();
    render(
      <PendingConfigChange
        t={{ id: "42", pendingConfigChange: pending() }}
        act={act}
      />,
    );

    expect(
      screen.getByText("Config change waiting for your approval"),
    ).toBeInTheDocument();
    expect(screen.getByText("Raise max_parallel to 4")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "settings/templates.yml" }),
    ).toHaveAttribute("href", "/config?path=settings%2Ftemplates.yml");
    expect(screen.getByTestId("config-change-validation")).toHaveTextContent(
      "Dry-run apply succeeded",
    );
    expect(
      screen.getByText("Secret gcp-keys/new-sa is not set yet"),
    ).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Approve & Apply" }));
    expect(api).toHaveBeenCalledWith("/api/tasks/42/config-change/approve", {
      method: "POST",
      body: JSON.stringify({ headSha: "b".repeat(40) }),
    });
  });

  it("rejects with the reason typed", async () => {
    const user = userEvent.setup();
    render(
      <PendingConfigChange
        t={{ id: "42", pendingConfigChange: pending() }}
        act={act}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Reject" }));
    await user.type(
      screen.getByLabelText("Why are you rejecting it?"),
      "too many runs",
    );
    await user.click(screen.getByRole("button", { name: "Reject change" }));
    expect(api).toHaveBeenCalledWith("/api/tasks/42/config-change/reject", {
      method: "POST",
      body: JSON.stringify({
        headSha: "b".repeat(40),
        reason: "too many runs",
      }),
    });
  });

  it("will not approve a change that failed validation", () => {
    render(
      <PendingConfigChange
        t={{
          id: "42",
          pendingConfigChange: pending({
            validation: { ok: false, error: "the change would lock dispatch" },
          }),
        }}
        act={act}
      />,
    );
    expect(screen.getByTestId("config-change-validation")).toHaveTextContent(
      "the change would lock dispatch",
    );
    expect(
      screen.getByRole("button", { name: "Approve & Apply" }),
    ).toBeDisabled();
  });
});
