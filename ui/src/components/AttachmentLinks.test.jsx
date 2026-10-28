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
import { describe, expect, it, vi } from "vitest";
import AttachmentLinks from "./AttachmentLinks.jsx";
import Overlay from "./Overlay.jsx";

const png = { id: 7, filename: "screenshot.png", contentType: "image/png" };
const zip = { id: 8, filename: "repro.zip", contentType: "application/zip" };

describe("AttachmentLinks", () => {
  it("shows an image over the UI instead of navigating to it", async () => {
    const user = userEvent.setup();
    render(<AttachmentLinks taskId="42" attachments={[png]} />);

    // The chip is a button, not a link: following a link to the raw
    // image is the dead end this replaced (grain/task-78).
    const chip = screen.getByText("screenshot.png");
    expect(chip.closest("a")).toBeNull();
    await user.click(chip);

    expect(screen.getByAltText("screenshot.png")).toHaveAttribute(
      "src",
      "/api/tasks/42/attachments/7",
    );
  });

  it("closes the image from the X in its corner", async () => {
    const user = userEvent.setup();
    render(<AttachmentLinks taskId="42" attachments={[png]} />);

    await user.click(screen.getByText("screenshot.png"));
    await user.click(screen.getByRole("button", { name: "Close image" }));

    expect(screen.queryByAltText("screenshot.png")).not.toBeInTheDocument();
  });

  it("closes the image from a click beside it, and from Escape", async () => {
    const user = userEvent.setup();
    render(<AttachmentLinks taskId="42" attachments={[png]} />);

    await user.click(screen.getByText("screenshot.png"));
    // A click on the ground around the picture is a way out; a click on
    // the picture itself is not.
    await user.click(screen.getByAltText("screenshot.png"));
    expect(screen.getByAltText("screenshot.png")).toBeInTheDocument();
    await user.click(screen.getByAltText("screenshot.png").parentElement);
    expect(screen.queryByAltText("screenshot.png")).not.toBeInTheDocument();

    await user.click(screen.getByText("screenshot.png"));
    await user.keyboard("{Escape}");
    expect(screen.queryByAltText("screenshot.png")).not.toBeInTheDocument();
  });

  // An attachment is nearly always read inside a task's own pane, which
  // is a Dialog too: leaving the image has to leave the image, not the
  // task under it.
  it("closes only the image when it is opened over another dialog", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(
      <Overlay onClose={onClose}>
        <AttachmentLinks taskId="42" attachments={[png]} />
      </Overlay>,
    );

    await user.click(screen.getByText("screenshot.png"));
    await user.keyboard("{Escape}");

    expect(screen.queryByAltText("screenshot.png")).not.toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByText("screenshot.png")).toBeInTheDocument();
  });

  it("offers a download of the image being viewed", async () => {
    const user = userEvent.setup();
    render(<AttachmentLinks taskId="42" attachments={[png]} />);

    await user.click(screen.getByText("screenshot.png"));

    const link = screen.getByRole("link", { name: "Download" });
    expect(link).toHaveAttribute("href", "/api/tasks/42/attachments/7");
    expect(link).toHaveAttribute("download", "screenshot.png");
  });

  it("leaves anything that is not an image a link to its own content", () => {
    render(<AttachmentLinks taskId="42" attachments={[zip]} />);

    const link = screen.getByRole("link", { name: "repro.zip" });
    expect(link).toHaveAttribute("href", "/api/tasks/42/attachments/8");
    expect(link).toHaveAttribute("target", "_blank");
  });

  it("links an SVG rather than opening it in the image viewer", () => {
    // The server serves SVG as an octet-stream download (it can carry
    // script), so the viewer's <img> could only ever show it broken.
    const svg = {
      id: 9,
      filename: "diagram.svg",
      contentType: "image/svg+xml",
    };
    render(<AttachmentLinks taskId="42" attachments={[svg]} />);

    const link = screen.getByRole("link", { name: "diagram.svg" });
    expect(link).toHaveAttribute("href", "/api/tasks/42/attachments/9");
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("renders nothing without attachments", () => {
    const { container } = render(
      <AttachmentLinks taskId="42" attachments={[]} />,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
