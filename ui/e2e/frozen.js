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

// The classic suite is frozen (docs/test-plans/classic-ui-e2e-migration,
// "Bucket 3"; the rule itself is in e2e/README.md). Two lists beside this
// file take tests out of the gating `ui e2e` job without deleting them:
//
//   - QUARANTINE: tests that flaked. They are not fixed, they are moved
//     out of the gate.
//   - NONGATING: specs whose server half (Go, tests/uiapi) and ui-ng half
//     (ui-ng e2e) have both landed, so the classic copy no longer has to
//     hold a pull request back.
//
// Both run in the `ui e2e (quarantined)` job instead, which is
// continue-on-error: a regression in them still shows up, red, on the
// pull request, and does not block it.
//
// An entry is a spec file, or a spec file and the line its `test(` call
// starts on -- the same `file:line` Playwright's own command line takes
// -- relative to this directory:
//
//   director.spec.js
//   inbox.spec.js:142   # flaked 3 of 40 runs, 11-07..11-11
//
// playwright.config.js turns the entries into `grepInvert` patterns for
// the gating lane. grep matches a test's title path, not its location, so
// a `file:line` entry is resolved here to the title written on that line,
// and an entry whose title is not a plain literal (a loop's template
// string) is refused -- quarantine the whole file instead.
// tests/hygiene's TestClassicE2EFreezeLists checks the same, so a bad
// entry fails `go unit` before it reaches this job.

import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));

export const FROZEN_LISTS = ["QUARANTINE", "NONGATING"];

// The env var that picks the lane. Unset (or "gating") is the gating
// job and a person's own `npm run test:e2e`; anything else turns the
// grepInvert off, which the quarantined lane needs so that the
// file:line arguments run-frozen.mjs passes are not filtered back out.
export const LANE_ENV = "GRAIN_E2E_LANE";

// readFrozenList parses one list into { list, lineNo, file, line } entries.
// `#` starts a comment; blank lines are skipped.
export function readFrozenList(name, dir = here) {
  const text = fs.readFileSync(path.join(dir, name), "utf8");
  const entries = [];
  text.split("\n").forEach((raw, i) => {
    const spec = raw.replace(/#.*$/, "").trim();
    if (!spec) return;
    const m = /^([^\s:]+\.spec\.js)(?::(\d+))?$/.exec(spec);
    if (!m) {
      throw new Error(
        `e2e/${name}:${i + 1}: ${JSON.stringify(spec)} is not "<file>.spec.js" or "<file>.spec.js:<line>"`,
      );
    }
    entries.push({
      list: name,
      lineNo: i + 1,
      file: m[1],
      line: m[2] ? Number(m[2]) : undefined,
    });
  });
  return entries;
}

export function readFrozenEntries(dir = here) {
  return FROZEN_LISTS.flatMap((name) => readFrozenList(name, dir));
}

// A test(...) call's title, when it is a string literal: "…", '…', or a
// template with no ${…} in it. The call may break before the title, so
// the caller hands in a few lines from the one named.
const titleRe =
  /\btest(?:\.(?:only|fixme|fail|slow|skip))?\(\s*(?:"((?:\\.|[^"\\])*)"|'((?:\\.|[^'\\])*)'|`((?:\\.|[^`\\$])*)`)/;

function unescapeJS(s) {
  return s.replace(/\\(.)/g, (_, c) => ({ n: "\n", t: "\t" })[c] ?? c);
}

// testTitleAt returns the literal title of the test(...) call that starts
// on `line` (1-based) of `file`, or throws saying why it can't.
export function testTitleAt(entry, dir = here) {
  const where = `e2e/${entry.list}:${entry.lineNo}: ${entry.file}:${entry.line}`;
  let src;
  try {
    src = fs.readFileSync(path.join(dir, entry.file), "utf8").split("\n");
  } catch {
    throw new Error(`${where}: no such spec in ui/e2e`);
  }
  const first = src[entry.line - 1];
  if (first === undefined || !/\btest(?:\.\w+)?\(/.test(first)) {
    throw new Error(
      `${where}: that line does not start a test(...) call (the spec has moved? point it at the new line)`,
    );
  }
  const m = titleRe.exec(src.slice(entry.line - 1, entry.line + 2).join("\n"));
  if (!m) {
    throw new Error(
      `${where}: the test's title is not a plain string literal, so grepInvert cannot name it; list the whole file instead`,
    );
  }
  return unescapeJS(m[1] ?? m[2] ?? m[3]);
}

const esc = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

// grepInvertPatterns turns entries into patterns over Playwright's grep
// title: "<project> <file> <describe…> <title> <@tags…>", the file being
// its path under testDir.
export function grepInvertPatterns(entries, dir = here) {
  return entries.map((entry) => {
    const file = `^\\s*\\S+ ${esc(entry.file)}`;
    if (entry.line === undefined) return new RegExp(`${file}(?: |$)`);
    const title = testTitleAt(entry, dir);
    return new RegExp(`${file} (?:.* )?${esc(title)}(?: @\\S+)*$`);
  });
}

// gatingGrepInvert is what playwright.config.js sets: every frozen entry
// in the gating lane, nothing in any other.
export function gatingGrepInvert(env = process.env) {
  const lane = env[LANE_ENV] || "gating";
  if (lane !== "gating") return undefined;
  const patterns = grepInvertPatterns(readFrozenEntries());
  return patterns.length ? patterns : undefined;
}
