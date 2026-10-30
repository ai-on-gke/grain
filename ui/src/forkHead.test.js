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
import { forkHeadHelp, parseForkHead } from "./forkHead.js";

describe("parseForkHead", () => {
  it("reads a fork head label", () => {
    expect(parseForkHead("andrzejfudro:chore/bump-agy-1.4.9")).toEqual({
      owner: "andrzejfudro",
      branch: "chore/bump-agy-1.4.9",
    });
  });

  it("leaves a plain branch, and anything no label looks like, alone", () => {
    for (const s of ["", "main", "chore/bump-agy-1.4.9", "also bad ~^:"]) {
      expect(parseForkHead(s)).toBeNull();
    }
  });

  it("says what is wrong with a malformed label", () => {
    expect(parseForkHead("a:b:c").error).toMatch(/more than one ':'/);
    expect(parseForkHead(":main").error).toMatch(/both an owner and a branch/);
    expect(parseForkHead("andrzejfudro:").error).toMatch(
      /both an owner and a branch/,
    );
    expect(parseForkHead("-x:main").error).toMatch(/not a GitHub login/);
    expect(parseForkHead("me:bad..ref").error).toMatch(
      /not a usable branch name/,
    );
  });

  it("explains a label against its repo", () => {
    expect(
      forkHeadHelp(parseForkHead("andrzejfudro:x"), "ai-on-gke/grain"),
    ).toMatch(/pull request on ai-on-gke\/grain it heads/);
  });
});
