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

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ThemeProvider } from "@mui/material";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { clearMermaidCache } from "../mermaidRender.js";
import { PHONE_QUERY } from "../phone.js";
import { darkTheme, lightTheme } from "../theme.js";
import Markdown from "./Markdown.jsx";

function renderWithTheme(ui, theme = lightTheme) {
  return render(<ThemeProvider theme={theme}>{ui}</ThemeProvider>);
}

describe("Mermaid diagrams in Markdown", () => {
  beforeEach(() => {
    clearMermaidCache();
  });

  it("renders a standalone ```mermaid block into an SVG, toggles source, and expands with zoom", async () => {
    const user = userEvent.setup();
    const markdown = [
      "## Plan",
      "",
      "```mermaid Request pipeline",
      "flowchart LR",
      "  Ingress[HTTP Ingress] --> Queue[Task Queue]",
      "```",
    ].join("\n");

    const { container } = renderWithTheme(<Markdown>{markdown}</Markdown>);

    const region = screen.getByRole("region", { name: "Request pipeline" });
    await waitFor(
      () => {
        expect(region.querySelector(".mermaid-svg-wrap svg")).not.toBeNull();
      },
      { timeout: 10000 },
    );
    expect(region).toHaveTextContent("HTTP Ingress");
    // Standalone diagram is block-level, never nested inside <pre>.
    expect(container.querySelector("pre .mermaid-block")).toBeNull();

    await user.click(screen.getByRole("button", { name: "Source" }));
    expect(region.querySelector(".mermaid-svg-wrap svg")).toBeNull();
    expect(region.querySelector("pre.mermaid-source")).toHaveTextContent(
      "Ingress[HTTP Ingress] --> Queue[Task Queue]",
    );

    await user.click(screen.getByRole("button", { name: "Diagram" }));
    expect(region.querySelector(".mermaid-svg-wrap svg")).not.toBeNull();

    await user.click(screen.getByRole("button", { name: "Expand diagram" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.querySelector(".mermaid-svg-wrap svg")).not.toBeNull();

    await user.click(screen.getByRole("button", { name: "Zoom in" }));
    expect(
      screen.getByRole("button", { name: "Reset zoom" }),
    ).toHaveTextContent("125%");
    await user.click(screen.getByRole("button", { name: "Reset zoom" }));
    expect(
      screen.getByRole("button", { name: "Reset zoom" }),
    ).toHaveTextContent("100%");

    await user.click(screen.getByRole("button", { name: "Close diagram" }));
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).toBeNull();
    });
  });

  it("renders Before and After architecture diagrams side by side and flips between states", async () => {
    const user = userEvent.setup();
    const markdown = [
      "### Before: Synchronous store write",
      "```mermaid",
      "flowchart LR",
      "  Handler[HTTP Handler] --> LegacyDB[(SQLite)]",
      "```",
      "",
      "### After: Queued outbox worker",
      "```mermaid",
      "flowchart LR",
      "  Handler[HTTP Handler] --> Outbox[(Outbox)] --> Worker[Dispatcher] --> PrimaryDB[(Postgres)]",
      "```",
    ].join("\n");

    renderWithTheme(<Markdown>{markdown}</Markdown>, darkTheme);

    const comparison = screen.getByRole("region", {
      name: "Before and after architecture diagrams",
    });

    await waitFor(
      () => {
        expect(
          comparison.querySelectorAll(".mermaid-svg-wrap svg"),
        ).toHaveLength(2);
      },
      { timeout: 10000 },
    );
    expect(comparison).toHaveTextContent("Before: Synchronous store write");
    expect(comparison).toHaveTextContent("After: Queued outbox worker");
    expect(comparison).toHaveTextContent("SQLite");
    expect(comparison).toHaveTextContent("Outbox");

    // Switch to single-pane Before / After views to compare states in place.
    await user.click(screen.getByRole("button", { name: "Before" }));
    expect(comparison.querySelectorAll(".mermaid-svg-wrap svg")).toHaveLength(
      1,
    );
    expect(comparison).toHaveTextContent("SQLite");
    expect(comparison).not.toHaveTextContent("Outbox");

    await user.click(screen.getByRole("button", { name: "After" }));
    expect(comparison.querySelectorAll(".mermaid-svg-wrap svg")).toHaveLength(
      1,
    );
    expect(comparison).toHaveTextContent("Outbox");
    expect(comparison).not.toHaveTextContent("SQLite");

    // Expand full-screen and flip between Before and After with arrow keys.
    await user.click(
      screen.getByRole("button", { name: "Expand diagram comparison" }),
    );
    const dialog = screen.getByRole("dialog");
    expect(dialog.querySelectorAll(".mermaid-svg-wrap svg")).toHaveLength(2);

    fireEvent.keyDown(window, { key: "ArrowLeft" });
    expect(dialog.querySelectorAll(".mermaid-svg-wrap svg")).toHaveLength(1);
    expect(dialog).toHaveTextContent("SQLite");

    fireEvent.keyDown(window, { key: "ArrowRight" });
    expect(dialog.querySelectorAll(".mermaid-svg-wrap svg")).toHaveLength(1);
    expect(dialog).toHaveTextContent("Outbox");
  });

  it("defaults Before/After comparisons to single-pane Before view on phone viewports", async () => {
    const originalMatchMedia = window.matchMedia;
    window.matchMedia = vi.fn().mockImplementation((query) => ({
      matches: query === PHONE_QUERY,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    }));

    try {
      const markdown = [
        "```mermaid before",
        "flowchart LR\n  OldNode --> Store",
        "```",
        "",
        "```mermaid after",
        "flowchart LR\n  NewNode --> Cache --> Store",
        "```",
      ].join("\n");

      renderWithTheme(<Markdown>{markdown}</Markdown>);
      expect(screen.getByRole("button", { name: "Before" })).toHaveAttribute(
        "aria-pressed",
        "true",
      );
      await waitFor(
        () => {
          expect(
            document.querySelectorAll(".mermaid-svg-wrap svg"),
          ).toHaveLength(1);
        },
        { timeout: 10000 },
      );
    } finally {
      window.matchMedia = originalMatchMedia;
    }
  });

  it("falls back to showing the raw Mermaid source when syntax is invalid", async () => {
    const bad = "```mermaid\nflowchart ??? broken syntax\n```";
    renderWithTheme(<Markdown>{bad}</Markdown>);

    await waitFor(() => {
      expect(
        screen.getByText(/Diagram could not be rendered/i),
      ).toBeInTheDocument();
    });
    expect(document.querySelector("pre.mermaid-source")).toHaveTextContent(
      "flowchart ??? broken syntax",
    );
  });

  it("keeps raw HTML <mermaid-diagram> tags and javascript: click directives inert", async () => {
    const body = [
      '<mermaid-diagram data-code="flowchart LR; A-->B"></mermaid-diagram>',
      "",
      "```mermaid",
      "%%{init: {'securityLevel': 'loose'}}%%",
      "flowchart LR",
      '  A["<img src=x onerror=alert(1)>Safe"] --> B[Done]',
      '  click A "javascript:alert(1)"',
      "```",
    ].join("\n");

    const { container } = renderWithTheme(<Markdown>{body}</Markdown>);

    // Only the genuine ```mermaid fence mounts a Mermaid region; the raw
    // <mermaid-diagram> HTML tag stays escaped.
    expect(screen.getAllByRole("region")).toHaveLength(1);
    await waitFor(
      () => {
        expect(container.querySelector(".mermaid-svg-wrap svg")).not.toBeNull();
      },
      { timeout: 10000 },
    );

    for (const el of container.querySelectorAll("*")) {
      for (const { name, value } of el.attributes) {
        expect(name).not.toMatch(/^on/i);
        expect(value).not.toMatch(/^\s*(javascript|data|vbscript):/i);
      }
    }
    expect(container.querySelector("script")).toBeNull();
    expect(container.querySelector("iframe")).toBeNull();
  });
});
