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
import { darkTheme, lightTheme } from "./theme.js";

// Each palette's muted text tokens carry real text -- form hints,
// counts, task numbers, the board's empty-column note -- so each has to
// hold WCAG AA's 4.5:1 on every ground it is drawn on, and stay a
// visible step apart from its neighbour (2026-11-02 walkthrough, F4).
const read = (path) =>
  readFileSync(fileURLToPath(new URL(path, import.meta.url)), "utf8");
const styleCss = read("./style.css");

function tokens(selector) {
  const block = styleCss.match(new RegExp(`${selector}\\s*\\{([^}]*)\\}`))[1];
  const found = {};
  for (const m of block.matchAll(/(--[\w-]+):\s*(#[0-9a-f]{6});/gi)) {
    found[m[1]] = m[2];
  }
  return found;
}

function luminance(hex) {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

// lighten mixes hex toward white the way MUI's standard Alert draws its
// ground.
function lighten(hex, k) {
  return `#${[1, 3, 5]
    .map((i) => {
      const c = parseInt(hex.slice(i, i + 2), 16);
      return Math.round(c + (255 - c) * k)
        .toString(16)
        .padStart(2, "0");
    })
    .join("")}`;
}

function contrast(a, b) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

const lightTokens = tokens(":root");
const darkTokens = {
  ...lightTokens,
  ...tokens(':root\\[data-theme="dark"\\]'),
};
const grounds = [
  "--surface",
  "--canvas",
  "--surface-hover",
  "--surface-active",
];
// The dark task pane is an MUI Dialog, whose Paper MUI lightens with a
// white background-image for its elevation: #3a3c3f is what that paints
// over --surface, and it is the lightest ground dark text sits on.
// ui/e2e/dark-contrast.spec.js checks the same pair off rendered pixels.
const darkPane = "#3a3c3f";

for (const [name, t, theme] of [
  ["light", lightTokens, lightTheme],
  ["dark", darkTokens, darkTheme],
]) {
  describe(`${name} theme text contrast`, () => {
    for (const fg of ["--text", "--text-secondary", "--text-tertiary"]) {
      it(`${fg} reaches 4.5:1 on every surface`, () => {
        for (const bg of grounds) {
          expect(
            contrast(t[fg], t[bg]),
            `${fg} on ${bg}`,
          ).toBeGreaterThanOrEqual(4.5);
        }
        if (name === "dark") {
          expect(
            contrast(t[fg], darkPane),
            `${fg} on the task pane`,
          ).toBeGreaterThanOrEqual(4.5);
        }
      });
    }

    it("keeps secondary a visible step between text and tertiary", () => {
      expect(contrast(t["--text"], t["--text-secondary"])).toBeGreaterThan(1.5);
      expect(
        contrast(t["--text-secondary"], t["--text-tertiary"]),
      ).toBeGreaterThan(1.3);
    });

    it("gives MUI the same secondary text colour as style.css", () => {
      expect(theme.palette.text.secondary).toBe(t["--text-secondary"]);
    });
  });
}

// The light accent is link text as well as a button's ground, and a link
// can sit in an Alert's tint (Settings -> Credentials' "How the two tabs
// differ"); a filled success chip ("ready") puts white on success.main;
// an outlined error button ("decline") writes its label in error.main.
describe("light theme accent, success and error contrast", () => {
  it("gives MUI the same accent as style.css", () => {
    expect(lightTheme.palette.primary.main).toBe(lightTokens["--accent"]);
  });

  it("holds 4.5:1 for accent link text on every ground and alert tint", () => {
    const alertTints = ["success", "info", "warning", "error"].map(
      (c) => lightTheme.palette[c].main,
    );
    const tinted = alertTints.map((hex) => lighten(hex, 0.9));
    for (const bg of [...grounds.map((g) => lightTokens[g]), ...tinted]) {
      expect(
        contrast(lightTokens["--accent"], bg),
        `accent on ${bg}`,
      ).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("holds 4.5:1 for an outlined warning chip's label on every ground", () => {
    for (const bg of grounds) {
      expect(
        contrast(lightTheme.palette.warning.main, lightTokens[bg]),
        `warning on ${bg}`,
      ).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("gives MUI the same error colour as style.css", () => {
    expect(lightTheme.palette.error.main).toBe(lightTokens["--danger"]);
  });

  it("holds 4.5:1 for an outlined error button's label on every ground", () => {
    for (const bg of [...grounds, "--danger-subtle"]) {
      expect(
        contrast(lightTheme.palette.error.main, lightTokens[bg]),
        `error on ${bg}`,
      ).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("holds 4.5:1 for a filled error button's label", () => {
    const { main, contrastText } = lightTheme.palette.error;
    const label = contrastText.replace(/^#(\w)(\w)(\w)$/, "#$1$1$2$2$3$3");
    expect(contrast(label, main)).toBeGreaterThanOrEqual(4.5);
  });

  it("holds 4.5:1 for a filled success chip's label", () => {
    const { main, contrastText } = lightTheme.palette.success;
    // MUI writes white as "#fff".
    const label = contrastText.replace(/^#(\w)(\w)(\w)$/, "#$1$1$2$2$3$3");
    expect(contrast(label, main)).toBeGreaterThanOrEqual(4.5);
  });
});
