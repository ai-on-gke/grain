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
import { lightTheme } from "./theme.js";

// The light palette's muted text tokens carry real text -- form hints,
// counts, task numbers, the board's empty-column note -- so each has to
// hold WCAG AA's 4.5:1 on every ground it is drawn on, and stay a
// visible step apart from its neighbour (2026-11-02 walkthrough, F4).
const read = (path) =>
  readFileSync(fileURLToPath(new URL(path, import.meta.url)), "utf8");
const styleCss = read("./style.css");

function rootTokens() {
  const block = styleCss.match(/:root\s*\{([^}]*)\}/)[1];
  const tokens = {};
  for (const m of block.matchAll(/(--[\w-]+):\s*(#[0-9a-f]{6});/gi)) {
    tokens[m[1]] = m[2];
  }
  return tokens;
}

function luminance(hex) {
  const [r, g, b] = [1, 3, 5].map((i) => {
    const c = parseInt(hex.slice(i, i + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

function contrast(a, b) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

describe("light theme text contrast", () => {
  const t = rootTokens();
  const grounds = [
    "--surface",
    "--canvas",
    "--surface-hover",
    "--surface-active",
  ];

  for (const fg of ["--text", "--text-secondary", "--text-tertiary"]) {
    it(`${fg} reaches 4.5:1 on every surface`, () => {
      for (const bg of grounds) {
        expect(contrast(t[fg], t[bg]), `${fg} on ${bg}`).toBeGreaterThanOrEqual(
          4.5,
        );
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
    expect(lightTheme.palette.text.secondary).toBe(t["--text-secondary"]);
  });
});
