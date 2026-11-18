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

// e2e/frozen.js resolves QUARANTINE / NONGATING file:line entries to the
// titles playwright.config.js's grepInvert drops. It lives in e2e/, which
// vitest skips and which takes no new spec files, so its cases are here.
// tests/hygiene's TestCheckFreezeLine holds the same cases for the Go
// mirror.

import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { grepInvertPatterns, testTitleAt } from "../e2e/frozen.js";

let dir;
afterEach(() => {
  if (dir) fs.rmSync(dir, { recursive: true, force: true });
  dir = undefined;
});

function at(src, line) {
  dir = fs.mkdtempSync(path.join(os.tmpdir(), "frozen-"));
  fs.writeFileSync(path.join(dir, "a.spec.js"), src);
  return { list: "QUARANTINE", lineNo: 1, file: "a.spec.js", line };
}

describe("testTitleAt", () => {
  it("names a test(...) on the line", () => {
    const e = at(
      'test("one", async () => {});\ntest("two", async () => {});\n',
      2,
    );
    expect(testTitleAt(e, dir)).toBe("two");
  });

  it("takes a modifier and a title on the next line", () => {
    const e = at('test.fixme(\n  "one",\n  async () => {},\n);\n', 1);
    expect(testTitleAt(e, dir)).toBe("one");
  });

  it("refuses a test.describe line rather than naming its first test", () => {
    const e = at(
      'test.describe("group", () => {\n  test("inner one", async () => {});\n  test("inner two", async () => {});\n});\n',
      1,
    );
    expect(() => testTitleAt(e, dir)).toThrow(/list the whole file instead/);
  });

  it("refuses a hook line", () => {
    const e = at(
      'test.beforeEach(async () => {\n  test("x", async () => {});\n});\n',
      1,
    );
    expect(() => testTitleAt(e, dir)).toThrow(/does not start a test/);
  });

  it("does not take the title of a call on a later line", () => {
    const e = at(
      'test(name, async () => {});\ntest("two", async () => {});\n',
      1,
    );
    expect(() => testTitleAt(e, dir)).toThrow(/not a plain string literal/);
  });

  it("refuses a title grep cannot tell from another test's", () => {
    for (const src of [
      'test("saves", async () => {});\ntest("auto saves", async () => {});\n',
      'test("saves", async () => {});\ntest.describe("g", () => {\n  test("saves", async () => {});\n});\n',
      'test("auto saves", async () => {});\ntest.describe("auto", () => {\n  test("saves", async () => {});\n});\n',
    ]) {
      expect(() => testTitleAt(at(src, 1), dir)).toThrow(/grep cannot tell/);
    }
  });

  it("allows a title that only ends another without a space", () => {
    const e = at(
      'test("saves", async () => {});\ntest("autosaves", async () => {});\n',
      1,
    );
    const [re] = grepInvertPatterns([e], dir);
    expect(re.test("chromium a.spec.js saves")).toBe(true);
    expect(re.test("chromium a.spec.js autosaves")).toBe(false);
  });
});
