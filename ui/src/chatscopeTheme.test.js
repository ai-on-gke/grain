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

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  CHATSCOPE_PALETTE,
  canonical,
  chatscopeVariables,
  themeChatscopeCss,
  unmappedColors,
} from "./chatscopeTheme.js";

// These two are the ones that matter: the vendored stylesheet the app
// actually imports, and this app's own, which has to define what the
// rewrite leaves behind. Read from disk rather than imported, because
// what is being asserted is the text of them.
const read = (path) =>
  readFileSync(fileURLToPath(new URL(path, import.meta.url)), "utf8");
const kit = read(
  "../node_modules/@chatscope/chat-ui-kit-styles/dist/default/styles.css",
);
const app = read("./style.css");

describe("canonical", () => {
  it("reads one colour out of its several spellings", () => {
    expect(canonical("#FFF")).toBe(canonical("#ffffff"));
    expect(canonical("white")).toBe(canonical("#fff"));
    expect(canonical("rgba(0,0,0,.87)")).toBe(canonical("rgba(0, 0, 0, 0.87)"));
    expect(canonical("rgb(110, 169, 215)")).not.toBe(
      canonical("rgba(110, 169, 215, 0.6)"),
    );
  });

  it("declines anything that is not a colour", () => {
    expect(canonical("#12345")).toBeNull();
    expect(canonical("rgb(none)")).toBeNull();
  });
});

describe("themeChatscopeCss", () => {
  it("rewrites the kit's palette to custom properties", () => {
    const themed = themeChatscopeCss(kit);
    expect(themed).toContain("var(--cs-color-primary)");
    expect(themed).toContain("var(--cs-color-primary-dark)");
    expect(themed).toContain("var(--cs-bg)");
  });

  // The scroll bar of task 267 and the composer of task 23, named here
  // because they are the two that were reported: both are the kit's
  // pale blue, and neither is a colour in the output any more.
  it("leaves no library blue in the composer or the scroll bar", () => {
    const themed = themeChatscopeCss(kit);
    const thumb = themed.match(/\.ps__thumb-y\s*\{[^}]*\}/)[0];
    expect(thumb).toContain("background-color: var(--cs-color-primary)");
    const editor = themed.match(
      /\.cs-message-input__content-editor\s*\{[^}]*\}/,
    )[0];
    expect(editor).toContain("var(--cs-color-primary)");
    expect(themed).not.toMatch(/#c6e3fa|#6ea9d7|#f6fbff|#d1dbe3/i);
  });

  it("keeps everything that is not a colour", () => {
    // The kit does use the word the colour keyword could be mistaken
    // for; a rewrite that broke this would silently unstyle bubbles.
    expect(themeChatscopeCss(kit)).toContain("white-space: pre-wrap");
    expect(themeChatscopeCss("a { color: transparent }")).toBe(
      "a { color: transparent }",
    );
  });

  it("is stable when run twice", () => {
    const once = themeChatscopeCss(kit);
    expect(themeChatscopeCss(once)).toBe(once);
  });
});

// The point of the table is that it is exhaustive: a colour the kit
// paints and this file has not accounted for is a colour that renders
// in the library's palette instead of grain's, which is the bug this
// whole arrangement exists to stop recurring. An upgrade of the kit
// that brings a new one fails here, with the literal in the message,
// rather than months later as a report about one blue element.
it("accounts for every colour in the vendored stylesheet", () => {
  expect(unmappedColors(kit)).toEqual([]);
});

// And the other half of the same claim: a property the rewrite emits
// but style.css never defines resolves to nothing at all.
it("defines every custom property the rewrite emits", () => {
  const defined = new Set(
    [...app.matchAll(/(--cs-[a-z0-9-]+)\s*:/g)].map(([, name]) => name),
  );
  expect(chatscopeVariables().filter((name) => !defined.has(name))).toEqual([]);
});

// Nothing in the mapping may point at a literal colour: the values are
// what makes the widget follow the light/dark choice, and a hex here
// would be a corner of it that does not.
it("maps to theme properties rather than to colours", () => {
  for (const value of Object.values(CHATSCOPE_PALETTE)) {
    expect(value).toMatch(/^var\(--cs-[a-z0-9-]+\)$/);
  }
});
