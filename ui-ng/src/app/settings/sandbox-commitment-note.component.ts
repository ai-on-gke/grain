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

import {
  ChangeDetectionStrategy,
  Component,
  computed,
  input,
} from "@angular/core";

// SandboxCommitment is GET /api/settings' sandboxCommitment
// (ui.SandboxCommitment): what the worker, merger and CI slots commit of
// this host if every one of them is full.
export interface SandboxCommitment {
  level?: string;
  uncapped?: boolean;
  workers?: number;
  mergers?: number;
  ciJobs?: number;
  runs?: number;
  cpusPerSandbox?: number;
  cpusCommitted?: number;
  hostCpus?: number;
  cpuRatio?: number;
  memoryMbPerSandbox?: number;
  memoryMbCommitted?: number;
  hostMemoryMb?: number;
  memoryShare?: number;
  runsPerCpu?: number;
  budget?: {
    mode?: string;
    cpus?: number;
    cpusBounded?: boolean;
    cpuOvercommit?: number;
    memoryMb?: number;
    memoryBounded?: boolean;
    memoryOvercommit?: number;
    reserveMemoryMb?: number;
    reserveIsDefault?: boolean;
    diskBounded?: boolean;
    diskGb?: number;
    diskOvercommit?: number;
  };
}

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? "" : "s"}`;
}

function gib(mb: number | undefined): string {
  return (Number(mb ?? 0) / 1024).toFixed(1);
}

function fixed(n: number | undefined, digits: number): string {
  return Number(n ?? 0).toFixed(digits);
}

// commitmentText is the classic SandboxCommitmentNote's sentence, as
// plain text: one budget every agent run and CI job on the host shares,
// charged at the shape each one holds.
export function commitmentText(c: SandboxCommitment): string {
  const slots = `${plural(c.workers ?? 0, "worker")} + ${plural(c.mergers ?? 0, "merger")} + ${plural(c.ciJobs ?? 0, "CI job")}`;
  if (c.uncapped) {
    const runs = `${slots} = ${plural(c.runs ?? 0, "concurrent run")} on a host with ${c.hostCpus ?? 0} vCPU and ${gib(c.hostMemoryMb)} GiB, each with no cpu or memory limit at all (${fixed(c.runsPerCpu, 1)} runs per core).`;
    const head = `Running at once, if every slot is full: ${runs}`;
    if (c.level === "ok") return head;
    return `${head} Nothing here caps what one run takes, so the only limit is how many of them there are. Contention shows up as slower runs and as test suites timing out; fewer workers is the trade, or granule sandboxes, which do have a shape.`;
  }
  const b = c.budget;
  let cpus = `${slots} × ${c.cpusPerSandbox ?? 0} vCPU = ${c.cpusCommitted ?? 0} vCPU committed against the ${c.hostCpus ?? 0} this host has (${fixed(c.cpuRatio, 1)}×)`;
  if (b?.cpusBounded) {
    cpus += `, held against a ${Math.round(b.cpus ?? 0)} vCPU sandbox budget every agent run and CI job on this host shares`;
    if (b.mode === "auto" && b.cpuOvercommit != null) {
      cpus += ` (auto at ${fixed(b.cpuOvercommit, 2)}×)`;
    }
  }
  cpus += ".";
  let memory = `${c.runs ?? 0} × ${c.memoryMbPerSandbox ?? 0} MiB = ${gib(c.memoryMbCommitted)} GiB of memory, of the ${gib(c.hostMemoryMb)} GiB it has (${Math.round((c.memoryShare ?? 0) * 100)}%)`;
  if (b?.memoryBounded) {
    memory += `, held against a ${gib(b.memoryMb)} GiB sandbox budget — ${gib(c.hostMemoryMb)} GiB host, less ${gib(b.reserveMemoryMb)} GiB kept back for the host itself${b.reserveIsDefault ? " (grain's own reserve)" : ""}, at ${b.mode === "auto" ? "auto " : ""}${fixed(b.memoryOvercommit, 2)}×`;
  }
  memory += ".";
  const disk = b?.diskBounded
    ? ` Up to ${b.diskGb ?? 0} GiB of sandbox disk may be promised, at ${fixed(b.diskOvercommit, 2)}× the disk less its floor.`
    : "";
  const head = `Committed if every slot is full: ${cpus} ${memory}${disk}`;
  if (c.level === "ok") return head;
  return `${head} Some overcommitment is normal here — a run spends most of its time waiting on the model, not on a core — but contention shows up as slower runs and as test suites timing out. Fewer workers, or a smaller sandbox, is the trade.`;
}

// SandboxCommitmentNoteComponent says what the configured slots commit of
// this host: a quiet hint while that is fine, a banner once it is
// overcommitted ("info", a normal way to run) or past what the machine
// has ("warning"). Absent where the server sends no commitment.
@Component({
  selector: "g-sandbox-commitment-note",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (commitment(); as c) {
      @if (c.level === "ok") {
        <p class="s-hint s-num" data-testid="sandbox-commitment-note">
          {{ text() }}
        </p>
      } @else {
        <div
          class="s-banner s-num"
          [class.warn]="c.level === 'warning'"
          [class.readonly]="c.level !== 'warning'"
          role="status"
          data-testid="sandbox-commitment-note"
          [attr.data-level]="c.level"
        >
          <span>{{ text() }}</span>
        </div>
      }
    }
  `,
})
export class SandboxCommitmentNoteComponent {
  readonly commitment = input<SandboxCommitment | null | undefined>(null);
  protected readonly text = computed(() => {
    const c = this.commitment();
    return c ? commitmentText(c) : "";
  });
}
