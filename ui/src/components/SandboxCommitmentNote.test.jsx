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

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import SandboxCommitmentNote from "./SandboxCommitmentNote.jsx";

// The deployment this exists for: 5 workers and 2 mergers of 3 vCPU /
// 3072 MiB each, on a 4 vCPU / 15.8 GiB host (grain-home task 141).
const overcommitted = {
  workers: 5,
  mergers: 2,
  ciJobs: 3,
  runs: 10,
  cpusPerSandbox: 3,
  memoryMbPerSandbox: 3072,
  cpusCommitted: 30,
  memoryMbCommitted: 30720,
  hostCpus: 4,
  hostMemoryMb: 16125,
  cpuRatio: 7.5,
  memoryShare: 1.905,
  cpuLevel: "warning",
  memoryLevel: "warning",
  level: "warning",
};

describe("SandboxCommitmentNote", () => {
  // The point of the whole thing: both totals and both host figures on
  // screen, so somebody can weigh the trade rather than be told a
  // verdict.
  it("says what is committed and what the host has", () => {
    render(<SandboxCommitmentNote commitment={overcommitted} />);

    const note = screen.getByRole("alert");
    expect(note).toHaveTextContent(
      "5 workers + 2 mergers + 3 CI jobs × 3 vCPU = 30 vCPU committed against the 4 this host has (7.5×)",
    );
    expect(note).toHaveTextContent(
      "10 × 3072 MiB = 30.0 GiB of memory, of the 15.7 GiB it has (191%)",
    );
  });

  it("warns loudly past what the machine has, and quietly when merely overcommitted", () => {
    const { unmount } = render(
      <SandboxCommitmentNote commitment={overcommitted} />,
    );
    expect(screen.getByRole("alert")).toHaveClass("MuiAlert-colorWarning");
    unmount();

    render(
      <SandboxCommitmentNote
        commitment={{ ...overcommitted, level: "caution" }}
      />,
    );
    expect(screen.getByRole("alert")).toHaveClass("MuiAlert-colorInfo");
  });

  // A commitment with nothing wrong with it is still printed: an
  // operator should be able to see where they are before they get
  // somewhere bad. Just not as an alert.
  it("prints a healthy commitment as plain helper text", () => {
    render(
      <SandboxCommitmentNote
        commitment={{
          ...overcommitted,
          workers: 2,
          mergers: 1,
          ciJobs: 0,
          runs: 3,
          cpusCommitted: 6,
          memoryMbCommitted: 9216,
          cpuRatio: 1.5,
          memoryShare: 0.57,
          cpuLevel: "ok",
          memoryLevel: "ok",
          level: "ok",
        }}
      />,
    );

    expect(screen.queryByRole("alert")).toBeNull();
    expect(
      screen.getByText(/6 vCPU committed against the 4 this host has/),
    ).toBeVisible();
  });

  // The same deployment on the backend that shapes nothing
  // (grain/task-285): no product to show, and the run count is the whole
  // of what it commits.
  const uncapped = {
    workers: 5,
    mergers: 2,
    ciJobs: 3,
    runs: 10,
    uncapped: true,
    hostCpus: 4,
    hostMemoryMb: 16125,
    runsPerCpu: 2.5,
    cpuLevel: "warning",
    level: "warning",
  };

  it("says the run count and the host where nothing caps a run", () => {
    render(<SandboxCommitmentNote commitment={uncapped} />);

    const note = screen.getByRole("alert");
    expect(note).toHaveTextContent(
      "5 workers + 2 mergers + 3 CI jobs = 10 concurrent runs on a host with 4 vCPU and 15.7 GiB, each with no cpu or memory limit at all (2.5 runs per core)",
    );
    // Nothing that would read as a limit somebody set: the stored shape
    // sizes nothing on this backend and is not a term of this sum.
    expect(note).not.toHaveTextContent("committed");
  });

  // grain/task-197 and grain/task-28: the same deployment with a budget
  // behind it, which is what makes those numbers a bound rather than an
  // observation. One budget, shared by every sandbox on the host -- no
  // reservation for the agent pool, and no share carved out for CI.
  const budgeted = {
    workers: 3,
    mergers: 1,
    ciJobs: 15,
    runs: 19,
    cpusPerSandbox: 2,
    memoryMbPerSandbox: 8192,
    cpusCommitted: 38,
    memoryMbCommitted: 155648,
    hostCpus: 32,
    hostMemoryMb: 131072,
    cpuRatio: 1.1875,
    memoryShare: 1.1875,
    cpuLevel: "caution",
    memoryLevel: "caution",
    level: "caution",
    budget: {
      cpuOvercommit: 2,
      memoryOvercommit: 1,
      reserveMemoryMb: 16384,
      cpus: 64,
      cpusBounded: true,
      memoryMb: 114688,
      memoryBounded: true,
    },
  };

  it("says what the slots commit and what budget holds them", () => {
    render(<SandboxCommitmentNote commitment={budgeted} />);

    const note = screen.getByRole("alert");
    expect(note).toHaveTextContent(
      "3 workers + 1 merger + 15 CI jobs × 2 vCPU = 38 vCPU committed against the 32 this host has (1.2×), held against a 64 vCPU sandbox budget every agent run and CI job on this host shares",
    );
    expect(note).toHaveTextContent(
      "held against a 112.0 GiB sandbox budget — 128.0 GiB host, less 16.0 GiB kept back for the host itself, at 1.00×",
    );
    // Nothing is reserved for anybody: the sentence that said so
    // described a rule this deployment no longer has (grain/task-28).
    expect(note).not.toHaveTextContent("reserved for agent runs");
    expect(note).not.toHaveTextContent("left for CI");
  });

  // Disk is said only where the budget had a disk to bound it by -- a
  // cluster's allocatable ephemeral storage -- and not at all otherwise.
  it("says the promised-disk budget where there is one", () => {
    const { unmount } = render(
      <SandboxCommitmentNote
        commitment={{
          ...budgeted,
          budget: {
            ...budgeted.budget,
            diskOvercommit: 10,
            diskGb: 1755,
            diskBounded: true,
          },
        }}
      />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Up to 1755 GiB of sandbox disk may be promised, at 10.00× the disk less its floor.",
    );
    unmount();
    render(<SandboxCommitmentNote commitment={budgeted} />);
    expect(screen.getByRole("alert")).not.toHaveTextContent("sandbox disk");
  });

  it("says grain's own reserve is grain's own", () => {
    render(
      <SandboxCommitmentNote
        commitment={{
          ...budgeted,
          budget: { ...budgeted.budget, reserveIsDefault: true },
        }}
      />,
    );
    expect(screen.getByRole("alert")).toHaveTextContent(
      "(grain's own reserve)",
    );
  });

  // Absent for every deployment the arithmetic does not apply to -- no
  // backend to speak for, no host reading.
  it("renders nothing without a commitment", () => {
    const { container } = render(
      <SandboxCommitmentNote commitment={undefined} />,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
