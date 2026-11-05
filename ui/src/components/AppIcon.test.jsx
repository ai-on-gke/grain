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
import { describe, expect, it } from "vitest";
import AlertsButton from "./AlertsButton.jsx";
import AppIcon, {
  CHROME_GLYPH_MAP,
  ExternalLinkIcon,
  NavGlyph,
  STANDARD_ICONS,
} from "./AppIcon.jsx";
import PageHeader, { GlobalButtons } from "./PageHeader.jsx";

const REQUIRED_STANDARD_MEANINGS = [
  "settings",
  "alerts",
  "notifications",
  "inbox",
  "search",
  "system",
  "health",
  "ci",
  "user",
  "account",
  "help",
  "signOut",
  "add",
  "edit",
  "delete",
  "more",
  "close",
  "externalLink",
  "copy",
  "refresh",
  "filter",
  "sort",
  "expand",
  "collapse",
  "sidebar",
];

describe("AppIcon and NavGlyph", () => {
  it.each(Object.keys(STANDARD_ICONS))(
    "renders standard icon %s with data-icon and aria-hidden by default",
    (name) => {
      const { container } = render(<AppIcon name={name} size={20} />);
      const svg = container.querySelector(`svg[data-icon="${name}"]`);
      expect(svg).toBeInTheDocument();
      expect(svg).toHaveAttribute("aria-hidden", "true");
      expect(svg).toHaveAttribute("width", "20");
      expect(svg).toHaveAttribute("height", "20");
      expect(svg.querySelector("path")).toBeInTheDocument();
    },
  );

  it("covers every required standard meaning", () => {
    for (const meaning of REQUIRED_STANDARD_MEANINGS) {
      expect(STANDARD_ICONS).toHaveProperty(meaning);
    }
  });

  it("exposes role=img and aria-label when title is provided", () => {
    render(<AppIcon name="settings" title="Settings" />);
    const icon = screen.getByRole("img", { name: "Settings" });
    expect(icon).toHaveAttribute("data-icon", "settings");
    expect(icon).not.toHaveAttribute("aria-hidden");
  });

  it("renders standard icons for chrome destinations and keeps Chladni glyphs for item kinds", () => {
    for (const [place, expected] of Object.entries(CHROME_GLYPH_MAP)) {
      const { container, unmount } = render(
        <NavGlyph kind={place} size={20} />,
      );
      expect(
        container.querySelector(`svg[data-icon="${expected}"]`),
      ).toBeInTheDocument();
      expect(container.querySelector("svg[data-glyph]")).toBeNull();
      unmount();
    }

    for (const itemKind of [
      "tasks",
      "repos",
      "branches",
      "boards",
      "schedules",
      "templates",
      "suites",
      "processes",
      "sandboxes",
      "sessions",
      "director",
    ]) {
      const { container, unmount } = render(
        <NavGlyph kind={itemKind} size={20} />,
      );
      expect(
        container.querySelector(`svg[data-glyph="${itemKind}"]`),
      ).toBeInTheDocument();
      expect(container.querySelector("svg[data-icon]")).toBeNull();
      unmount();
    }
  });

  it("renders ExternalLinkIcon as decorative externalLink", () => {
    const { container } = render(<ExternalLinkIcon />);
    const svg = container.querySelector('svg[data-icon="externalLink"]');
    expect(svg).toBeInTheDocument();
    expect(svg).toHaveAttribute("aria-hidden", "true");
  });

  it("renders standard icons across the header rail and leading expand toggle", () => {
    const { container } = render(
      <PageHeader
        title="settings / models"
        onExpandSidebar={() => {}}
        globals={
          <GlobalButtons
            alerts={<AlertsButton alerts={[]} />}
            onOpenSystem={() => {}}
            onOpenSettings={() => {}}
          />
        }
      />,
    );

    const icons = [...container.querySelectorAll("svg[data-icon]")].map((el) =>
      el.getAttribute("data-icon"),
    );
    expect(icons).toEqual([
      "sidebar",
      "settings",
      "alerts",
      "system",
      "settings",
    ]);
  });
});
