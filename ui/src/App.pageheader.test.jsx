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

// The one header rail every page in the main pane has
// (components/PageHeader.jsx, grain/task-4915): the global top bar that
// used to sit over the rail and the page is gone, and each page's own
// header row is drawn into this rail, which ends in the bell, System
// and the Settings gear.
//
// Part of the App suite -- see App.test.jsx for why it is several files.

import { render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import App from "./App.jsx";
import api from "./api.js";
import { resetAppTestState, setupApi } from "./appTestHarness.js";

vi.mock("./api.js", async (importOriginal) => {
  const actual = await importOriginal();
  return { ...actual, default: vi.fn() };
});

// Every page the main pane can show, by address, and the title its rail
// names it by.
const PAGES = [
  ["/", "tasks", "tasks"],
  ["/inbox", "inbox", "inbox"],
  ["/boards", "boards", "boards"],
  ["/sessions", "sessions", "sessions"],
  ["/processes", "processes", "processes"],
  ["/ci", "ci", "ci"],
  ["/repos", "repositories", "repos"],
  ["/repos/acme/widgets", "repositories/acme/widgets", "repos"],
  [
    "/repos/acme/widgets/releases",
    "repositories/acme/widgets/releases",
    "repos",
  ],
  [
    "/repos/acme/widgets/settings/ci",
    "repositories/acme/widgets/ci",
    "repos",
  ],
  [
    "/repos/acme/widgets/settings/merge-queue",
    "repositories/acme/widgets/merge queue",
    "repos",
  ],
  ["/settings", "settings/general", "settings"],
  ["/system", "system/health", "system"],
  ["/system/logs", "system/logs", "system"],
];

describe("App page header", () => {
  afterEach(resetAppTestState);

  it.each(PAGES)(
    "%s has one header rail, titled %s with the %s glyph, ending in the global buttons",
    async (path, title, glyph) => {
      window.history.replaceState(null, "", path);
      setupApi();
      const { container } = render(<App />);
      const header = await screen.findByTestId("page-header");
      await waitFor(() =>
        expect(
          within(header).getByTestId("page-header-title"),
        ).toHaveTextContent(title),
      );
      expect(
        header.querySelector(`.header-icon svg[data-glyph="${glyph}"]`),
      ).not.toBeNull();
      expect(screen.getAllByTestId("page-header")).toHaveLength(1);
      expect(screen.queryByTestId("top-bar")).toBeNull();
      expect(container.querySelector(".top-bar")).toBeNull();

      const globals = within(header).getByTestId("page-header-globals");
      expect(within(globals).getByTestId("alerts-button")).toBeInTheDocument();
      expect(within(globals).getByTestId("top-bar-system")).toBeInTheDocument();
      expect(
        within(globals).getByTestId("top-bar-settings"),
      ).toBeInTheDocument();
      expect(screen.getAllByTestId("top-bar-settings")).toHaveLength(1);
      expect(screen.getAllByTestId("top-bar-system")).toHaveLength(1);
      expect(within(header).getByTestId("top-bar-page")).toHaveAttribute(
        "aria-current",
        "page",
      );

      // Page actions first, then the buttons every page shares, so Tab
      // reaches the page's own controls before them.
      const slot = within(header).getByTestId("page-header-actions");
      expect(
        slot.compareDocumentPosition(globals) &
          Node.DOCUMENT_POSITION_FOLLOWING,
      ).toBeTruthy();

      // Nothing above the rail and the page but the rows of the shell.
      const shell = container.querySelector(".app-shell");
      expect(shell.firstElementChild).toHaveClass("app-body");
    },
  );

  it("draws the task list's header row inside the rail", async () => {
    window.history.replaceState(null, "", "/");
    setupApi();
    render(<App />);
    await screen.findByText("Fix bug");
    const slot = screen.getByTestId("page-header-actions");
    await waitFor(() =>
      expect(slot.querySelector(".content-header")).not.toBeNull(),
    );
    expect(slot.querySelector(".task-list-toolbar")).not.toBeNull();
  });

  it("titles a standing session's header rail by its title rather than its id slug", async () => {
    window.history.replaceState(null, "", "/session/night-shift-2");
    setupApi();
    const rest = api.getMockImplementation();
    api.mockImplementation((path, opts) => {
      if (path === "/api/console-sessions") {
        return Promise.resolve({
          sessions: [
            { id: "director", title: "Director", taskId: "1" },
            { id: "night-shift-2", title: "Night shift", taskId: "2" },
          ],
        });
      }
      return rest(path, opts);
    });
    render(<App />);
    const header = await screen.findByTestId("page-header");
    await waitFor(() =>
      expect(within(header).getByTestId("page-header-title")).toHaveTextContent(
        "sessions/Night shift",
      ),
    );
    expect(within(header).getByTestId("top-bar-page")).toHaveTextContent(
      "Night shift",
    );
    expect(
      header.querySelector('.header-icon svg[data-glyph="sessions"]'),
    ).not.toBeNull();
  });
});
