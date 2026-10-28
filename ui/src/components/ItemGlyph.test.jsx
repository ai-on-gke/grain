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

import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ITEM_GLYPH_PATHS } from "../brand/item-glyph-paths.js";
import { GLYPH_KINDS } from "../brand/item-glyphs.js";
import ItemGlyph from "./ItemGlyph.jsx";

const glyphOf = (container) => container.querySelector("svg");

describe("ItemGlyph", () => {
  it("draws each kind's own figure", () => {
    const drawn = new Set();
    for (const kind of GLYPH_KINDS) {
      const { container } = render(<ItemGlyph kind={kind} />);
      const svg = glyphOf(container);
      expect(svg).toHaveAttribute("data-glyph", kind);
      const d = svg.querySelector("path").getAttribute("d");
      expect(d).toBe(ITEM_GLYPH_PATHS[kind]);
      drawn.add(d);
    }
    // Four kinds and three destinations, seven different paths -- not
    // one figure reused.
    expect(drawn.size).toBe(GLYPH_KINDS.length);
  });

  it("draws Processes a figure of its own, not the Schedules posts", () => {
    // docs/design/settings-panel.md §5.7: the page lists templates and
    // triggers as well as schedules, so it does not borrow the figure
    // that reads as "time".
    const { container } = render(<ItemGlyph kind="processes" />);
    const svg = glyphOf(container);
    expect(svg).toHaveAttribute("data-glyph", "processes");
    const d = svg.querySelector("path").getAttribute("d");
    expect(d).toBe(ITEM_GLYPH_PATHS.processes);
    for (const old of ["schedules", "templates", "suites"]) {
      expect(d).not.toBe(ITEM_GLYPH_PATHS[old]);
    }
  });

  it("takes the colour of the text it sits beside", () => {
    // The whole reason these are inline SVG rather than an <img>: a
    // selected nav entry brightens its label, and the glyph goes with
    // it, in either theme and with no second asset.
    const { container } = render(<ItemGlyph kind="repos" />);
    expect(glyphOf(container).querySelector("path")).toHaveAttribute(
      "fill",
      "currentColor",
    );
  });

  it("is decorative beside its own label, and named when it stands alone", () => {
    const { container: beside } = render(<ItemGlyph kind="suites" />);
    expect(glyphOf(beside)).toHaveAttribute("aria-hidden", "true");

    const { container: alone } = render(
      <ItemGlyph kind="suites" title="Suites" />,
    );
    expect(glyphOf(alone)).not.toHaveAttribute("aria-hidden");
    expect(glyphOf(alone)).toHaveAttribute("aria-label", "Suites");
  });

  it("renders at the size asked for", () => {
    const { container } = render(<ItemGlyph kind="schedules" size={24} />);
    expect(glyphOf(container)).toHaveAttribute("width", "24");
    expect(glyphOf(container)).toHaveAttribute("height", "24");
  });

  it("draws nothing for a kind with no figure", () => {
    // A new list page renders before anyone has cut it a glyph, rather
    // than throwing on the way past.
    const { container } = render(<ItemGlyph kind="nothing-yet" />);
    expect(glyphOf(container)).toBeNull();
  });
});
