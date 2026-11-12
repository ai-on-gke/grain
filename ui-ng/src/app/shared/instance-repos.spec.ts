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
import {
  configuredInstanceRepos,
  hasExplicitTargetAllowlist,
} from "./instance-repos";

describe("configuredInstanceRepos", () => {
  it("collects configured targetRepos and per-repo config lists with case-insensitive deduplication", () => {
    const res = configuredInstanceRepos({
      targetRepos: ["acme/widgets", "acme/infra"],
      defaultTarget: "ACME/Widgets",
      configRepo: "grain/config",
      reposWithWorkflows: ["acme/external-ci", "Acme/Infra"],
      reposWithPromptExtension: ["acme/docs"],
      repoDefaultCapabilities: {
        "acme/empty": [],
        "acme/caps": ["self-debug"],
      },
    });

    expect(res.hasTargetAllowlist).toBe(true);
    expect(res.repos).toEqual([
      "acme/caps",
      "acme/docs",
      "acme/external-ci",
      "acme/infra",
      "acme/widgets",
    ]);
  });

  it("reports hasTargetAllowlist false when targetRepos is empty or unset", () => {
    expect(hasExplicitTargetAllowlist(null)).toBe(false);
    expect(hasExplicitTargetAllowlist({ targetRepos: [] })).toBe(false);
    expect(
      configuredInstanceRepos({
        targetRepos: [],
        reposWithWorkflows: ["acme/theirs"],
      }),
    ).toEqual({
      hasTargetAllowlist: false,
      repos: ["acme/theirs"],
    });
  });
});
