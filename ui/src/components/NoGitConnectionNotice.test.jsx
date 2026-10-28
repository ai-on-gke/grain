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
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import NoGitConnectionNotice from "./NoGitConnectionNotice.jsx";

// The "add a git connection first" state a fresh install -- a Cloudtop's
// among them, which no longer seeds depot -- shows wherever a repository
// is picked.
describe("NoGitConnectionNotice", () => {
  it("tells a deployment with no git connection to add one, and links to where", async () => {
    const onOpenSettings = vi.fn();
    const user = userEvent.setup();
    render(
      <NoGitConnectionNotice
        deployment={{ id: "default", name: "Default", hasGitCredential: false }}
        onOpenSettings={onOpenSettings}
      />,
    );
    const notice = screen.getByTestId("no-git-connection");
    expect(notice).toHaveTextContent(
      "This deployment has no git connection yet",
    );
    expect(notice).toHaveTextContent("Add a git connection first");
    expect(notice).toHaveTextContent(
      "a GitHub token or GitHub App for github.com",
    );
    const link = screen.getByRole("link", {
      name: "Settings → Git",
    });
    expect(link.getAttribute("href")).toBe("/settings/git");

    // Modified clicks open in a new tab instead of intercepting in-app.
    const root = link.closest("body > div");
    const leftToBrowser = [];
    const follow = (e) => {
      leftToBrowser.push(!e.defaultPrevented);
      e.preventDefault();
    };
    root.addEventListener("click", follow);
    fireEvent.click(link, { ctrlKey: true });
    fireEvent.click(link, { metaKey: true });
    fireEvent.click(link, { shiftKey: true });
    fireEvent.click(link, { altKey: true });
    fireEvent.click(link, { button: 1 });
    root.removeEventListener("click", follow);
    expect(leftToBrowser).toEqual([true, true, true, true, true]);
    expect(onOpenSettings).not.toHaveBeenCalled();

    await user.click(link);
    expect(onOpenSettings).toHaveBeenCalledWith("git");
  });

  it("says nothing once the deployment has a connection on any host", () => {
    const { container } = render(
      <>
        <NoGitConnectionNotice
          deployment={{ id: "a", hasGitCredential: true }}
        />
        <NoGitConnectionNotice
          deployment={{
            id: "b",
            hasGitCredential: false,
            gitConnections: [
              { host: "depot.code.forge.test", credential: "ambient-forgecli" },
            ],
          }}
        />
        <NoGitConnectionNotice deployment={null} />
      </>,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("does not count an anonymous connection, which pushes nothing", () => {
    render(
      <NoGitConnectionNotice
        deployment={{
          id: "c",
          hasGitCredential: false,
          gitConnections: [{ host: "github.com", credential: "anonymous" }],
        }}
      />,
    );
    expect(screen.getByTestId("no-git-connection")).toBeInTheDocument();
  });
});
