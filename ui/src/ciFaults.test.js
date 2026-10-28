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
  faultDetail,
  faultFixes,
  faultSource,
  faultSourceLabel,
  faultSummary,
  faultsForRun,
  fixSummary,
  fixWhere,
  otherRefs,
} from "./ciFaults.js";

const fault = {
  repo: "gke-labs/granule",
  jobId: "image",
  jobName: "image",
  step: "build the image",
  digest: "d1",
  refs: ["grain/task-238", "grain/task-246", "grain/task-251"],
  runs: [
    { runId: "r1", ref: "grain/task-238", at: "2026-09-18T16:50:00Z" },
    { runId: "r2", ref: "grain/task-246", at: "2026-09-18T16:20:00Z" },
    { runId: "r3", ref: "grain/task-251", at: "2026-09-18T15:30:00Z" },
  ],
  paths: ["scripts/granule/guest-inputs.sh"],
  fixes: [
    {
      pullRequest: "gke-labs/granule#26",
      task: "281",
      title: "Point debootstrap back at the http mirror",
      files: ["scripts/granule/guest-inputs.sh"],
    },
  ],
};

describe("faultsForRun", () => {
  it("finds the fault a run is a member of", () => {
    expect(faultsForRun([fault], { id: "r2" })).toEqual([fault]);
  });

  it("never joins a GitHub fault onto a grain run", () => {
    const ghFault = { ...fault, source: "github" };
    expect(faultsForRun([ghFault], { id: "r2" })).toEqual([]);
    expect(faultsForRun([ghFault], { id: "r2", source: "github" })).toEqual([
      ghFault,
    ]);
  });

  it("labels grain and GitHub fault sources", () => {
    expect(faultSource(fault)).toBe("grain");
    expect(faultSourceLabel(fault)).toBe("grain CI");
    expect(faultSource({ ...fault, source: "github" })).toBe("github");
    expect(faultSourceLabel({ ...fault, source: "github" })).toBe("GitHub CI");
  });

  it("says nothing about a run in no fault", () => {
    expect(faultsForRun([fault], { id: "r9" })).toEqual([]);
    expect(faultsForRun(undefined, { id: "r1" })).toEqual([]);
    expect(faultsForRun([fault], null)).toEqual([]);
  });
});

describe("faultSummary", () => {
  // The claim is about elsewhere: the reader's own branch being red is
  // the thing they are already looking at.
  it("counts the other branches, not this one", () => {
    expect(otherRefs(fault, "grain/task-238")).toEqual([
      "grain/task-246",
      "grain/task-251",
    ]);
    expect(faultSummary(fault, "grain/task-238")).toBe(
      "image is failing the same way on 2 other branches right now — one fault, not this branch's.",
    );
  });

  it("names the job, so a branch with a real failure too is not told it is fine", () => {
    expect(faultSummary(fault, "grain/task-246")).toMatch(/^image is failing/);
  });

  it("reads as one branch when there is one", () => {
    const pair = { ...fault, refs: ["grain/task-238", "grain/task-246"] };
    expect(faultSummary(pair, "grain/task-238")).toContain("1 other branch ");
  });

  // A fault of one branch is not a shared fault, and a row must say
  // nothing rather than something it cannot support.
  it("says nothing when the reader's branch is the only one in it", () => {
    expect(
      faultSummary({ ...fault, refs: ["grain/task-238"] }, "grain/task-238"),
    ).toBe(null);
  });
});

describe("faultDetail", () => {
  it("keeps the evidence one gesture away", () => {
    const detail = faultDetail(fault, "grain/task-238");
    expect(detail).toContain("Step: build the image");
    expect(detail).toContain("grain/task-246, grain/task-251");
    expect(detail).toContain("scripts/granule/guest-inputs.sh");
    expect(detail).not.toContain("grain/task-238");
  });
});

describe("faultFixes", () => {
  it("offers the open pull request grain named, and says why", () => {
    const [fix] = faultFixes(fault);
    expect(fix.pullRequest).toBe("gke-labs/granule#26");
    expect(fixWhere(fix)).toBe("touches scripts/granule/guest-inputs.sh");
    expect(fixSummary(fix)).toBe(
      "gke-labs/granule#26 touches scripts/granule/guest-inputs.sh",
    );
  });

  it("is empty where grain would have had to guess", () => {
    expect(faultFixes({ ...fault, fixes: undefined })).toEqual([]);
    expect(faultFixes(undefined)).toEqual([]);
  });

  it("does not list a dozen files on a row", () => {
    expect(fixWhere({ pullRequest: "a/b#1", files: ["x.go", "y.go"] })).toBe(
      "touches 2 of the files this failure names",
    );
  });
});
