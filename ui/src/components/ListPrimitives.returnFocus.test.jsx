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
import { describe, expect, it } from "vitest";
import TaskLink from "./detail/TaskLink.jsx";
import { FollowLink } from "./ListPrimitives.jsx";

// Two dashboard panels following the dashboard's own question each have
// an "Open" to the same list. Coming Back mounts both again, and focus
// belongs on the one that was followed (2026-11-09 boards walkthrough).
function TwoOpens({ onFollow = () => {} }) {
  return (
    <>
      <FollowLink href="/?q=x" tag="panel-0:open" onFollow={onFollow}>
        Open first
      </FollowLink>
      <FollowLink href="/?q=x" tag="panel-1:open" onFollow={onFollow}>
        Open second
      </FollowLink>
    </>
  );
}

describe("useReturnFocus", () => {
  it("puts focus back on the followed link, not another with its href", async () => {
    const user = userEvent.setup();
    const { unmount } = render(<TwoOpens />);
    await user.click(screen.getByRole("link", { name: "Open second" }));
    unmount();
    document.body.focus();

    render(<TwoOpens />);
    expect(screen.getByRole("link", { name: "Open second" })).toHaveFocus();
  });

  it("follows in-app on a plain click", async () => {
    const user = userEvent.setup();
    const followed = [];
    render(<TwoOpens onFollow={() => followed.push("in-app")} />);
    await user.click(screen.getByRole("link", { name: "Open first" }));
    expect(followed).toEqual(["in-app"]);
  });

  it("leaves a TaskLink that did not opt in alone", async () => {
    const user = userEvent.setup();
    const { unmount } = render(
      <TaskLink id="7" onOpenTask={() => {}} returnFocus="report:backlog" />,
    );
    await user.click(screen.getByRole("link", { name: "7" }));
    unmount();
    document.body.focus();

    // A task pane's own link to the same task mounts first: it never
    // takes focus, so the report's link still can.
    render(
      <>
        <TaskLink id="7" onOpenTask={() => {}}>
          pane link
        </TaskLink>
        <TaskLink id="7" onOpenTask={() => {}} returnFocus="report:backlog">
          report link
        </TaskLink>
      </>,
    );
    expect(screen.getByRole("link", { name: "report link" })).toHaveFocus();
  });
});
