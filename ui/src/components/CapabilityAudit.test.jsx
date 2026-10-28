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
import { afterEach, describe, expect, it, vi } from "vitest";
import CapabilityAudit from "./CapabilityAudit.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));

describe("CapabilityAudit", () => {
  afterEach(() => vi.clearAllMocks());

  it("fetches nothing and shows nothing for an attempt with no records", () => {
    const { container } = render(
      <CapabilityAudit taskId="12" attempt={{ number: 1 }} />,
    );
    expect(container).toBeEmptyDOMElement();
    expect(api).not.toHaveBeenCalled();
  });

  it("lists each record's fields, argv verbatim, and grain's notes", async () => {
    api.mockResolvedValue({
      records: [
        {
          grant: "host-script#host-script",
          attemptId: "e1-12-2-0",
          at: "2026-10-23T18:00:00Z",
          record: {
            time: "2026-10-23T18:00:00Z",
            attempt_id: "e1-12-2-0",
            script: "deploy",
            argv: ["--dry-run", "two words", ""],
            exit_code: 0,
          },
        },
        {
          grant: "host-script#host-script",
          attemptId: "e1-12-2-0",
          at: "2026-10-23T18:00:05Z",
          note: "the capability server left out 3 further record(s)",
        },
      ],
    });
    render(
      <CapabilityAudit
        taskId="12"
        attempt={{ number: 2, capabilityAuditCount: 2 }}
      />,
    );
    expect(
      screen.getByText("Capability audit · 2 records"),
    ).toBeInTheDocument();
    expect(
      await screen.findByText('["--dry-run","two words",""]'),
    ).toBeInTheDocument();
    expect(screen.getByText("deploy")).toBeInTheDocument();
    expect(screen.getByText("exit_code")).toBeInTheDocument();
    expect(screen.queryByText("attempt_id")).not.toBeInTheDocument();
    expect(screen.getByText(/left out 3 further/)).toBeInTheDocument();
    expect(api).toHaveBeenCalledWith(
      "/api/tasks/12/attempts/2/capability-audit",
    );
  });
});
