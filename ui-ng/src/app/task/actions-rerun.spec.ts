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
import type { CIRun } from "../api/models";
import { actionsRunsToRerun } from "./task-changes-tab.component";

const run = (
  id: string,
  commit: string,
  conclusion: string,
  startedAt: string,
  status = "completed",
): CIRun => ({ id, commit, conclusion, startedAt, status }) as CIRun;

describe("actionsRunsToRerun", () => {
  // Multi-workflow Actions repositories: the newest run at the head may be
  // a passing workflow while another workflow at the same head failed.
  const runs = [
    run("gha:acme:web:3", "bbb", "success", "2026-11-19T00:03:00Z"),
    run("gha:acme:web:2", "bbb", "failure", "2026-11-19T00:02:00Z"),
    run("gha:acme:web:4", "bbb", "", "2026-11-19T00:01:00Z", "in_progress"),
    run("gha:acme:web:1", "aaa", "failure", "2026-11-18T00:00:00Z"),
  ];

  it("re-runs only the failed workflow runs at the newest head", () => {
    expect(actionsRunsToRerun(runs, true)?.map((r) => r.id)).toEqual([
      "gha:acme:web:2",
    ]);
  });

  it("re-runs every concluded workflow run at the newest head", () => {
    expect(actionsRunsToRerun(runs, false)?.map((r) => r.id)).toEqual([
      "gha:acme:web:3",
      "gha:acme:web:2",
    ]);
  });

  it("returns null for grain CI runs", () => {
    expect(
      actionsRunsToRerun([run("r1", "bbb", "failure", "x")], true),
    ).toBeNull();
  });
});
