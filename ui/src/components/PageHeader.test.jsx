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

import { useState } from "react";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import PageHeader, {
  GlobalButtons,
  PageHeaderActions,
  PageHeaderProvider,
  PageHeaderScope,
} from "./PageHeader.jsx";

// A page under the rail, the way App draws one: the rail's slot handed
// to everything below it.
function Framed({ children, title = "tasks", globals = null }) {
  const [slot, setSlot] = useState(null);
  return (
    <>
      <PageHeader title={title} globals={globals} onSlot={setSlot} />
      <main data-testid="body">
        <PageHeaderProvider slot={slot}>{children}</PageHeaderProvider>
      </main>
    </>
  );
}

describe("PageHeader", () => {
  it("splits the title into crumbs, heads with the 20px section glyph, and marks the last as the page", () => {
    const { container, rerender } = render(
      <PageHeader title="settings / models" />,
    );
    const title = screen.getByTestId("page-header-title");
    expect(title).toHaveTextContent(/^settings\/models$/);
    expect(screen.getByTestId("top-bar-page")).toHaveTextContent(/^models$/);
    expect(screen.getByTestId("top-bar-page")).toHaveAttribute(
      "aria-current",
      "page",
    );
    const icon = container.querySelector(
      '.header-icon svg[data-glyph="settings"]',
    );
    expect(icon).toBeInTheDocument();
    expect(icon).toHaveAttribute("width", "20");
    expect(icon).toHaveAttribute("height", "20");

    // Every crumb and slash shares the 1rem semibold heading style (§4.1).
    const spans = [...title.querySelectorAll("span.MuiTypography-root")];
    expect(spans).toHaveLength(3);
    for (const prop of [
      "font-family",
      "font-size",
      "line-height",
      "letter-spacing",
      "font-weight",
    ]) {
      const values = spans.map((s) =>
        getComputedStyle(s).getPropertyValue(prop),
      );
      expect(values[0], prop).not.toBe("");
      expect(new Set(values).size, prop).toBe(1);
    }

    rerender(<PageHeader title="task 123" />);
    expect(
      container.querySelector('.header-icon svg[data-glyph="tasks"]'),
    ).toBeInTheDocument();

    rerender(<PageHeader title="sessions / Director" />);
    expect(
      container.querySelector('.header-icon svg[data-glyph="director"]'),
    ).toBeInTheDocument();

    rerender(<PageHeader title="Back" variant="pane" />);
    expect(screen.getByTestId("pane-header")).toBeInTheDocument();
    expect(
      screen.getByRole("navigation", { name: "Pane" }),
    ).toBeInTheDocument();
  });

  it("draws a page's own actions in the rail, ahead of the globals", () => {
    render(
      <Framed
        globals={
          <GlobalButtons onOpenSystem={() => {}} onOpenSettings={() => {}} />
        }
      >
        <PageHeaderActions>
          <button type="button">New board</button>
        </PageHeaderActions>
        <p>body</p>
      </Framed>,
    );
    const slot = screen.getByTestId("page-header-actions");
    const button = screen.getByRole("button", { name: "New board" });
    expect(slot).toContainElement(button);
    expect(screen.getByTestId("body")).not.toContainElement(button);
    const settings = screen.getByTestId("top-bar-settings");
    expect(
      button.compareDocumentPosition(settings) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("keeps the actions in place with no rail, or inside a scope cut off from it", () => {
    render(
      <>
        <PageHeaderActions>
          <button type="button">Alone</button>
        </PageHeaderActions>
        <Framed>
          <PageHeaderScope>
            <PageHeaderActions>
              <button type="button">Panel</button>
            </PageHeaderActions>
          </PageHeaderScope>
        </Framed>
      </>,
    );
    expect(screen.getByRole("button", { name: "Alone" })).toBeInTheDocument();
    expect(screen.getByTestId("body")).toContainElement(
      screen.getByRole("button", { name: "Panel" }),
    );
  });

  it("draws System and Settings after the bell, selected with their view, and badges System when unbacked", () => {
    const onOpenSystem = vi.fn();
    const onOpenSettings = vi.fn();
    const { rerender } = render(
      <GlobalButtons
        view="tasks"
        alerts={<button type="button">Alerts</button>}
        onOpenSystem={onOpenSystem}
        onOpenSettings={onOpenSettings}
      />,
    );
    const globals = screen.getByTestId("page-header-globals");
    const bell = within(globals).getByRole("button", { name: "Alerts" });
    const system = screen.getByTestId("top-bar-system");
    const gear = screen.getByTestId("top-bar-settings");
    expect(bell).toBe(globals.firstElementChild);
    expect(
      bell.compareDocumentPosition(system) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(
      system.compareDocumentPosition(gear) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(system).toHaveAttribute("aria-pressed", "false");
    expect(gear).toHaveAttribute("aria-pressed", "false");

    fireEvent.click(system);
    fireEvent.click(gear);
    expect(onOpenSystem).toHaveBeenCalledTimes(1);
    expect(onOpenSettings).toHaveBeenCalledTimes(1);

    rerender(
      <GlobalButtons
        view="settings"
        onOpenSystem={onOpenSystem}
        onOpenSettings={onOpenSettings}
      />,
    );
    expect(screen.getByRole("button", { name: "Settings" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
    expect(screen.getByRole("button", { name: "System" })).toHaveAttribute(
      "aria-pressed",
      "false",
    );

    rerender(
      <GlobalButtons
        view="system"
        config={{ stateRepoResetNotice: true }}
        onOpenSystem={onOpenSystem}
        onOpenSettings={onOpenSettings}
      />,
    );
    const sys = screen.getByTestId("top-bar-system");
    expect(sys).toHaveAttribute("aria-pressed", "true");
    expect(sys).toContainElement(
      screen.getByRole("img", { name: /reset settings/ }),
    );
    expect(screen.getByTestId("top-bar-settings")).toHaveAttribute(
      "aria-pressed",
      "false",
    );
  });
});
