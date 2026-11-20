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

import { describe, expect, it } from "vitest";
import { classifyLocalScript } from "../inbox/inbox-classifier";
import { scriptCallLine, stdinLine } from "./script-call";

describe("scriptCallLine", () => {
  it("quotes every argument, so one argument with a space is not two", () => {
    expect(
      scriptCallLine({ script: "deploy", args: ["a b", "$(rm -rf /)"] }),
    ).toBe('deploy "a b" "$(rm -rf /)"');
    expect(scriptCallLine({ script: "deploy", args: ["a", "b"] })).toBe(
      'deploy "a" "b"',
    );
  });

  it("falls back to the tool name and leaves no trailing space", () => {
    expect(scriptCallLine({ tool: "local_deploy" })).toBe("local_deploy");
  });

  it("is what the Inbox row shows", () => {
    const entry = classifyLocalScript({
      id: "lsa-1",
      taskId: "7",
      script: "deploy",
      args: ["a b"],
    });
    expect(entry.summary).toBe('deploy "a b"');
  });
});

describe("stdinLine", () => {
  it("is empty when the script gets nothing on stdin", () => {
    expect(stdinLine({ stdinBytes: 0 })).toBe("");
  });

  it("names the size and hash even with no preview", () => {
    expect(stdinLine({ stdinBytes: 12, stdinSha256: "abc" })).toBe(
      "On stdin: 12 bytes, sha256 abc",
    );
  });

  it("says how much of a cut preview is shown", () => {
    expect(
      stdinLine({
        stdinBytes: 5000,
        stdinSha256: "abc",
        stdinTruncated: true,
        stdinPreview: "héllo",
      }),
    ).toBe(
      "On stdin: 5000 bytes, sha256 abc (truncated: first 6 of 5000 bytes shown)",
    );
  });
});
