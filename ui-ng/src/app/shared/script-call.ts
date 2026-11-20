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

import type { LocalScriptApproval } from "../api/models";

// How a host-script call waiting for approval is written, wherever a
// person can approve it (the task page's card and the Inbox row and
// inspector), as classic's LocalScriptApprovals.jsx writes it.
//
// scriptCallLine quotes every argument, so what is shown is exactly what
// the script gets: "a b" is one argument and a b is two, and nothing a
// shell would expand ($(...), globs) reads as if it were going to be.
export function scriptCallLine(
  a: Pick<LocalScriptApproval, "script" | "tool" | "args">,
): string {
  const args = (a.args ?? []).map((arg) => JSON.stringify(arg)).join(" ");
  return `${a.script || a.tool || "script"}${args ? " " + args : ""}`;
}

// stdinLine says what the script gets on stdin -- how much and the
// SHA-256 of all of it, and how much of it the preview shows when that
// is cut -- or "" when it gets nothing. It is shown whenever there is
// stdin, preview or not: a script that reads its instructions from stdin
// is approved on them too.
export function stdinLine(
  a: Pick<
    LocalScriptApproval,
    "stdinBytes" | "stdinSha256" | "stdinTruncated" | "stdinPreview"
  >,
): string {
  const bytes = a.stdinBytes ?? 0;
  if (bytes <= 0) return "";
  let line = `On stdin: ${bytes} bytes, sha256 ${a.stdinSha256 || "unknown"}`;
  if (a.stdinTruncated) {
    const shown = new TextEncoder().encode(a.stdinPreview || "").length;
    line += ` (truncated: first ${shown} of ${bytes} bytes shown)`;
  }
  return line;
}
