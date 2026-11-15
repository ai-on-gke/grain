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
import { commitmentText, diskFloorText } from "./sandbox-notes";
import {
  OVERCOMMIT_DEFAULTS,
  k8sFieldError,
  overcommitRangeError,
  readNumberBox,
  resolveOvercommit,
} from "./sandbox-settings";

describe("readNumberBox", () => {
  const box = { key: "k", label: "Auto CPU minimum", min: 0.25 };
  it("reads blank as no value and a number as itself", () => {
    expect(readNumberBox("  ", box)).toEqual({ value: null });
    expect(readNumberBox("1.5", box)).toEqual({ value: 1.5 });
  });
  it("refuses a value under the minimum by the box's label", () => {
    expect(readNumberBox("0.1", box).error).toBe(
      "Auto CPU minimum must be a number of at least 0.25.",
    );
  });
  it("refuses half-typed input a number box reports as blank", () => {
    expect(readNumberBox("", box, true).error).toContain("Auto CPU minimum");
  });
  it("states a range and whole numbers where the box has them", () => {
    expect(
      readNumberBox("101", { key: "t", label: "Target", min: 0.5, max: 100 })
        .error,
    ).toBe("Target must be a number from 0.5 to 100.");
    expect(
      readNumberBox("2.5", { key: "m", label: "Min", min: 0, integer: true })
        .error,
    ).toBe("Min must be a whole number of at least 0.");
  });
});

describe("overcommit bounds", () => {
  it("fills grain's defaults into what the server leaves at 0", () => {
    expect(resolveOvercommit({ mode: "auto", cpuMax: 8, cpuMin: 0 })).toEqual({
      ...OVERCOMMIT_DEFAULTS,
      mode: "auto",
      cpuMax: 8,
    });
  });
  it("refuses a minimum above its maximum", () => {
    const o = { ...OVERCOMMIT_DEFAULTS, memoryMin: 3 };
    expect(overcommitRangeError(o, "memory")).toBe(
      "Auto memory minimum (3) is above Auto memory maximum (2).",
    );
    expect(overcommitRangeError(o, "cpu")).toBeNull();
  });
});

describe("k8sFieldError", () => {
  it("takes blank and names the box for what the API server refuses", () => {
    expect(k8sFieldError("label", "Kubernetes namespace", "")).toBeNull();
    expect(k8sFieldError("label", "Kubernetes namespace", "grain")).toBeNull();
    expect(k8sFieldError("label", "Kubernetes namespace", "Bad NS")).toContain(
      'Kubernetes namespace "Bad NS"',
    );
    expect(k8sFieldError("priority", "P", "grain.ci")).toBeNull();
    expect(k8sFieldError("path", "W", "rel")).toContain("absolute path");
    expect(k8sFieldError("image", "I", "https://x/y")).toContain("OCI image");
    expect(k8sFieldError("selector", "S", "a=b, c")).toBe(
      'S entry "c" must be key=value.',
    );
  });
});

describe("notes", () => {
  it("says an uncapped deployment's runs and has no budget", () => {
    expect(
      commitmentText({
        uncapped: true,
        level: "ok",
        workers: 1,
        mergers: 1,
        ciJobs: 1,
        runs: 3,
        hostCpus: 2,
        hostMemoryMb: 4096,
        runsPerCpu: 1.5,
      }),
    ).toBe(
      "Running at once, if every slot is full: 1 worker + 1 merger + 1 CI job = 3 concurrent runs on a host with 2 vCPU and 4.0 GiB, each with no cpu or memory limit at all (1.5 runs per core).",
    );
  });
  it("says there is no floor when both are 0, and nothing with no volumes", () => {
    expect(diskFloorText(null, 0.1, 5120, 0.25)).toBe("");
    expect(diskFloorText([{ path: "/a" }, { path: "/b" }], 0, 0, 0.25)).toBe(
      "No disk floor: nothing holds work back as /a, /b fill, and a volume with no room left takes this deployment's own database down with it.",
    );
  });
});
