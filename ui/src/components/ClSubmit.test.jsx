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

import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import ClSubmitActions, { BehindBadge } from "./ClSubmit.jsx";

describe("ClSubmitActions", () => {
  it("renders nothing for a task with no live CL", () => {
    const { container } = render(
      <ClSubmitActions
        t={{ id: "1", state: "awaiting_submit" }}
        act={vi.fn()}
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("disables Submit with every reason, and still offers Sync", () => {
    const act = vi.fn();
    const t = {
      id: "7",
      state: "awaiting_submit",
      clSubmit: {
        target: "cl/42",
        canSubmit: false,
        reasons: ["review is not approved (in review)", "behind head: sync it"],
        canSync: true,
        behind: true,
      },
    };
    render(<ClSubmitActions t={t} act={act} />);
    expect(screen.getByTestId("cl-submit")).toBeDisabled();
    expect(screen.getByTestId("cl-submit-reasons")).toHaveTextContent(
      "Not submittable yet: review is not approved (in review); behind head: sync it.",
    );
    const sync = screen.getByTestId("cl-sync");
    expect(sync).toBeEnabled();
    fireEvent.click(sync);
    expect(act).toHaveBeenCalledTimes(1);
    expect(act.mock.calls[0][1]).toBe("7");
  });

  it("posts Submit when the CL is submittable, and disables Sync during a run", () => {
    const act = vi.fn();
    const t = {
      id: "8",
      state: "awaiting_submit",
      clSubmit: {
        target: "cl/43",
        canSubmit: true,
        canSync: false,
        syncReason: "a run is going",
      },
    };
    render(<ClSubmitActions t={t} act={act} />);
    expect(screen.queryByTestId("cl-submit-reasons")).toBeNull();
    expect(screen.getByTestId("cl-sync")).toBeDisabled();
    fireEvent.click(screen.getByTestId("cl-submit"));
    expect(act).toHaveBeenCalledTimes(1);
  });
});

describe("BehindBadge", () => {
  it("says behind or conflicting, and nothing when current", () => {
    const { rerender, container } = render(
      <BehindBadge submit={{ behind: false }} />,
    );
    expect(container).toBeEmptyDOMElement();
    rerender(<BehindBadge submit={{ behind: true }} />);
    expect(screen.getByTestId("cl-behind-badge")).toHaveTextContent(
      "behind head",
    );
    rerender(<BehindBadge submit={{ conflicted: true }} />);
    expect(screen.getByTestId("cl-behind-badge")).toHaveTextContent(
      "conflicts with head",
    );
  });
});
