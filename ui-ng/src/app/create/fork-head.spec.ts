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
import { forkHeadHelp, parseForkHead } from "./fork-head";

describe("parseForkHead", () => {
  it("leaves an ordinary branch name alone", () => {
    expect(parseForkHead("main")).toBeNull();
    expect(parseForkHead("release/v1.2")).toBeNull();
    expect(parseForkHead("")).toBeNull();
  });

  it("reads a well-formed label and explains it", () => {
    const h = parseForkHead(" octo-fork:feature/x ");
    expect(h).toEqual({ owner: "octo-fork", branch: "feature/x" });
    expect(
      forkHeadHelp(h as { owner: string; branch: string }, "acme/widgets"),
    ).toContain("branch feature/x on octo-fork's fork");
  });

  it("refuses a malformed label in the server's words", () => {
    expect(parseForkHead("bad::label")?.error).toContain(
      "has more than one ':'",
    );
    expect(parseForkHead(":x")?.error).toContain("both an owner and a branch");
    expect(parseForkHead("-bad:x")?.error).toContain("not a GitHub login");
    expect(parseForkHead("ok:a..b")?.error).toContain("not a usable branch");
  });
});
