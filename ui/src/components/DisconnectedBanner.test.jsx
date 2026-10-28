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

import { render, screen, waitFor, within } from "@testing-library/react";
import { Dialog } from "@mui/material";
import { describe, expect, it } from "vitest";
import DisconnectedBanner from "./DisconnectedBanner.jsx";
import { RESTARTING_REASON } from "../api.js";

describe("DisconnectedBanner", () => {
  it("renders nothing while connected", () => {
    const { container } = render(<DisconnectedBanner disconnection={null} />);
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the reason and when the daemon was last reached", () => {
    render(
      <DisconnectedBanner
        disconnection={{
          reason:
            "This deployment cannot be reached: the connection to it failed.",
          since: new Date(Date.now() - 5 * 60 * 1000),
        }}
      />,
    );

    expect(
      screen.getByText(/This deployment cannot be reached/),
    ).toBeInTheDocument();
    expect(screen.getByText(/Unreachable since/)).toBeInTheDocument();
    expect(screen.getByText(/5m ago/)).toBeInTheDocument();
    expect(screen.getByText(/retrying automatically/i)).toBeInTheDocument();
  });

  it("says grain is restarting when the daemon said it was", () => {
    render(
      <DisconnectedBanner
        disconnection={{
          reason: RESTARTING_REASON,
          restarting: true,
          since: new Date(Date.now() - 30 * 1000),
        }}
      />,
    );

    expect(screen.getByText(/grain is restarting/)).toBeInTheDocument();
    expect(screen.queryByText(/cannot be reached/)).not.toBeInTheDocument();
    expect(screen.getByText(/once grain is back/)).toBeInTheDocument();
  });

  // Found in the 2026-10-15 intake walkthrough: MUI's Modal hides every
  // sibling of its mount from assistive technology, the banner included.
  it("is still a status a screen reader can reach while a modal is open", async () => {
    const disconnection = {
      reason: "This deployment cannot be reached: the connection to it failed.",
      since: new Date(Date.now() - 5 * 60 * 1000),
    };
    const { rerender } = render(
      <>
        <DisconnectedBanner disconnection={null} />
        <Dialog open PaperProps={{ "aria-label": "Task" }}>
          pane
        </Dialog>
      </>,
    );
    // There, empty, before anything is wrong, so the change is announced.
    const status = await screen.findByRole("status");
    expect(screen.getByRole("dialog", { name: "Task" })).toContainElement(
      status,
    );
    expect(status).toHaveTextContent("");

    rerender(
      <>
        <DisconnectedBanner disconnection={disconnection} />
        <Dialog open PaperProps={{ "aria-label": "Task" }}>
          pane
        </Dialog>
      </>,
    );
    // The visible banner is behind the modal's aria-hidden sweep; the
    // copy inside the dialog is the one status left in the tree.
    const statuses = screen.getAllByRole("status");
    expect(statuses).toHaveLength(1);
    expect(
      within(screen.getByRole("dialog")).getByRole("status"),
    ).toHaveTextContent(
      /cannot be reached: the connection to it failed\. Unreachable since .* -- retrying automatically/,
    );
    // No ticking age in a live region.
    expect(statuses[0]).not.toHaveTextContent(/ago/);
    expect(screen.getByTestId("disconnected-banner")).toHaveTextContent(
      /5m ago/,
    );
  });

  // Found in the 2026-10-26 responsive navigation walkthrough: a pane
  // covers the shell banner, so in a Dialog the copy is what a sighted
  // reader sees too -- first in the paper, above its header.
  it("is a visible strip at the top of a Dialog's paper while disconnected", async () => {
    const disconnection = {
      reason: "This deployment cannot be reached: the connection to it failed.",
      since: new Date(),
    };
    const pane = (
      <Dialog open PaperProps={{ "aria-label": "Task" }}>
        <button>Back</button>
      </Dialog>
    );
    const { rerender } = render(
      <>
        <DisconnectedBanner disconnection={null} />
        {pane}
      </>,
    );
    const empty = await screen.findByTestId("disconnected-banner-mirror");
    expect(empty).toHaveStyle({ position: "absolute", width: "1px" });
    rerender(
      <>
        <DisconnectedBanner disconnection={disconnection} />
        {pane}
      </>,
    );
    const mirror = screen.getByTestId("disconnected-banner-mirror");
    // The same live region, now laid out in the flow.
    expect(mirror).toBe(empty);
    expect(mirror).not.toHaveStyle({ position: "absolute" });
    expect(screen.getByRole("dialog").firstElementChild).toContainElement(
      mirror,
    );
  });

  it("puts nothing anywhere once the modal closes", async () => {
    const disconnection = {
      reason: "This deployment cannot be reached: the connection to it failed.",
      since: new Date(),
    };
    const { rerender } = render(
      <>
        <DisconnectedBanner disconnection={disconnection} />
        <Dialog open>pane</Dialog>
      </>,
    );
    await screen.findByTestId("disconnected-banner-mirror");
    rerender(
      <>
        <DisconnectedBanner disconnection={disconnection} />
        <Dialog open={false} transitionDuration={0}>
          pane
        </Dialog>
      </>,
    );
    await waitFor(() =>
      expect(
        screen.queryByTestId("disconnected-banner-mirror"),
      ).not.toBeInTheDocument(),
    );
    expect(screen.getByRole("status")).toBe(
      screen.getByTestId("disconnected-banner"),
    );
  });
});
