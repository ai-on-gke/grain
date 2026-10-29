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
import ReviewBadge, { ReviewActions, reviewLabel } from "./ClReview.jsx";
import Declared from "./detail/Declared.jsx";
import EditTaskForm from "./detail/EditTaskForm.jsx";
import api from "../api.js";

vi.mock("../api.js", () => ({ default: vi.fn().mockResolvedValue(null) }));

describe("ReviewBadge", () => {
  it("renders nothing for a CL whose review nobody has observed", () => {
    const { container } = render(<ReviewBadge review={null} />);
    expect(container).toBeEmptyDOMElement();
  });

  it.each([
    [{ state: "draft", needsMailing: true }, "needs mailing"],
    [{ state: "in_review" }, "in review"],
    [{ state: "changes_requested" }, "changes requested"],
    [
      { state: "needs_approval", needsApproval: ["owners", "readability"] },
      "needs approval: owners, readability",
    ],
    [{ state: "approved", lgtm: ["alice"] }, "LGTM alice"],
  ])("labels %o as %s", (review, label) => {
    expect(reviewLabel(review)).toBe(label);
    render(<ReviewBadge review={review} />);
    expect(screen.getByTestId("cl-review-badge")).toHaveTextContent(label);
  });

  it("names LGTMs and missing approvals for a screen reader", () => {
    render(
      <ReviewBadge
        review={{
          state: "needs_approval",
          lgtm: ["alice"],
          needsApproval: ["owners"],
        }}
      />,
    );
    expect(screen.getByTestId("cl-review-badge")).toHaveAttribute(
      "aria-label",
      "Review: LGTM: alice. Missing approval: owners",
    );
  });
});

describe("ReviewActions", () => {
  it("disables what does not apply and posts what does", () => {
    const act = vi.fn();
    const t = {
      id: "12",
      clReview: {
        state: "in_review",
        canMail: false,
        mailReason: "already mailed",
        canPublish: true,
        drafts: 2,
        publishReason: "not mailed by grain",
      },
    };
    render(<ReviewActions t={t} act={act} />);
    expect(screen.getByTestId("cl-mail")).toBeDisabled();
    const publish = screen.getByTestId("cl-publish");
    expect(publish).toBeEnabled();
    expect(publish).toHaveTextContent("Publish replies (2)");
    fireEvent.click(publish);
    expect(act).toHaveBeenCalledTimes(1);
    expect(act.mock.calls[0][1]).toBe("12");
  });
});

describe("Reviewers in Declared and EditTaskForm", () => {
  it("shows reviewers in Declared and edits them in EditTaskForm", async () => {
    const t = {
      id: "12",
      title: "Fix CL",
      description: "Desc",
      reviewers: ["alice", "bob"],
    };
    render(<Declared t={t} config={null} act={vi.fn()} />);
    expect(screen.getByText("Reviewers")).toBeInTheDocument();
    expect(screen.getByText("alice, bob")).toBeInTheDocument();

    const act = vi.fn(async (fn) => {
      await fn();
      return true;
    });
    const onDone = vi.fn();
    render(
      <EditTaskForm
        t={t}
        config={null}
        templates={[]}
        act={act}
        onDone={onDone}
      />,
    );
    const input = screen.getByLabelText("Reviewers");
    expect(input).toHaveValue("alice, bob");
    fireEvent.change(input, { target: { value: "carol, dave" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(act).toHaveBeenCalledTimes(1);
    const body = JSON.parse(api.mock.calls.at(-1)[1].body);
    expect(body.reviewers).toEqual(["carol", "dave"]);
  });
});
