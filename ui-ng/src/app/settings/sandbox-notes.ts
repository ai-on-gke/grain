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

// The sentences classic Settings says under the sandbox shape and budget
// (ui/src/components/SandboxCommitmentNote.jsx, DiskFloorNote.jsx): the
// server's own arithmetic in words, never recomputed here.

/** GET /api/settings sandboxCommitment (pkg/ui/sandbox_commitment.go). */
export interface SandboxCommitmentDto {
  level?: string;
  uncapped?: boolean;
  workers?: number;
  mergers?: number;
  ciJobs?: number;
  runs?: number;
  runsPerCpu?: number;
  hostCpus?: number;
  hostMemoryMb?: number;
  cpusPerSandbox?: number;
  memoryMbPerSandbox?: number;
  cpusCommitted?: number;
  memoryMbCommitted?: number;
  cpuRatio?: number;
  memoryShare?: number;
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
    overcommit?: {
      cpuOvercommit?: number;
      memoryOvercommit?: number;
      last?: {
        cpuSome?: number;
        cpuPressure?: number;
        memorySome?: number;
        memoryPressure?: number;
      };
    };
  } | null;
}

/** One entry of GET /api/settings sandboxDiskFloors (pkg/ui DiskUsage). */
export interface DiskFloorDto {
  path: string;
  totalMB?: number;
  floorMB?: number;
  reclaimMB?: number;
}

const n = (v: unknown): number => (typeof v === "number" ? v : Number(v) || 0);
const gib = (mb: unknown): string => (n(mb) / 1024).toFixed(1);
const plural = (count: number, word: string): string =>
  `${count} ${word}${count === 1 ? "" : "s"}`;

function slots(c: SandboxCommitmentDto): string {
  return `${plural(n(c.workers), "worker")} + ${plural(n(c.mergers), "merger")} + ${plural(n(c.ciJobs), "CI job")}`;
}

// commitmentText is what the note says: the same sentences as classic,
// so an operator moving between the two UIs reads one account of it.
export function commitmentText(c: SandboxCommitmentDto): string {
  if (c.uncapped) {
    const runs = `${slots(c)} = ${plural(n(c.runs), "concurrent run")} on a host with ${n(c.hostCpus)} vCPU and ${gib(c.hostMemoryMb)} GiB, each with no cpu or memory limit at all (${n(c.runsPerCpu).toFixed(1)} runs per core).`;
    const head = `Running at once, if every slot is full: ${runs}`;
    return c.level === "ok"
      ? head
      : `${head} Nothing here caps what one run takes, so the only limit is how many of them there are. Contention shows up as slower runs and as test suites timing out; fewer workers is the trade, or granule sandboxes, which do have a shape.`;
  }
  const b = c.budget;
  let cpus = `${slots(c)} × ${n(c.cpusPerSandbox)} vCPU = ${n(c.cpusCommitted)} vCPU committed against the ${n(c.hostCpus)} this host has (${n(c.cpuRatio).toFixed(1)}×)`;
  if (b?.cpusBounded) {
    cpus += `, held against a ${Math.round(n(b.cpus))} vCPU sandbox budget every agent run and CI job on this host shares`;
    if (b.mode === "auto" && b.cpuOvercommit != null) {
      cpus += ` (auto at ${n(b.cpuOvercommit).toFixed(2)}×)`;
    }
  }
  let memory = `${n(c.runs)} × ${n(c.memoryMbPerSandbox)} MiB = ${gib(c.memoryMbCommitted)} GiB of memory, of the ${gib(c.hostMemoryMb)} GiB it has (${Math.round(n(c.memoryShare) * 100)}%)`;
  if (b?.memoryBounded) {
    memory += `, held against a ${gib(b.memoryMb)} GiB sandbox budget — ${gib(c.hostMemoryMb)} GiB host, less ${gib(b.reserveMemoryMb)} GiB kept back for the host itself${b.reserveIsDefault ? " (grain's own reserve)" : ""}, at ${b.mode === "auto" ? "auto " : ""}${n(b.memoryOvercommit).toFixed(2)}×`;
  }
  const disk = b?.diskBounded
    ? ` Up to ${n(b.diskGb)} GiB of sandbox disk may be promised, at ${n(b.diskOvercommit).toFixed(2)}× the disk less its floor.`
    : "";
  const head = `Committed if every slot is full: ${cpus}. ${memory}.${disk}`;
  return c.level === "ok"
    ? head
    : `${head} Some overcommitment is normal here — a run spends most of its time waiting on the model, not on a core — but contention shows up as slower runs and as test suites timing out. Fewer workers, or a smaller sandbox, is the trade.`;
}

/** formatSpace is classic SandboxHealthPage's: MB under a GiB, else GB. */
export function formatSpace(mb: unknown): string {
  if (mb === null || mb === undefined || Number.isNaN(Number(mb))) return "—";
  const v = Number(mb);
  if (v < 1024) return `${Math.round(v)} MB`;
  return `${(v / 1024).toFixed(1)} GB`;
}

// diskFloorText is what the two disk floor boxes come to on this
// deployment's own volumes, or "" where the server resolved none.
export function diskFloorText(
  floors: readonly DiskFloorDto[] | null | undefined,
  share: number,
  minMb: number,
  maxShare: number,
): string {
  if (!floors || floors.length === 0) return "";
  if (!share && !minMb) {
    return `No disk floor: nothing holds work back as ${floors.map((d) => d.path).join(", ")} fill, and a volume with no room left takes this deployment's own database down with it.`;
  }
  const lines = floors
    .filter((d) => n(d.totalMB) > 0)
    .map(
      (d) =>
        `${d.path} (${formatSpace(d.totalMB)}) holds ${formatSpace(d.floorMB)} free, sweeping back to ${formatSpace(d.reclaimMB)}.`,
    );
  return `Kept free on this deployment's own volumes: ${lines.join(" ")}${lines.length ? " " : ""}Never more than ${Math.round(maxShare * 100)}% of a volume is kept free, whatever is set here.`;
}

// SandboxCommitmentNoteComponent is quiet helper text while the
// commitment is fine and a banner once it is overcommitted ("info") or
// past what the machine has ("warning"): the second is a normal way to
// run a deployment, so it is not drawn as an error.
@Component({
  selector: "g-sandbox-commitment-note",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (text(); as t) {
      @if (commitment()?.level === "ok") {
        <p class="s-hint s-note" data-testid="sandbox-commitment-note">
          {{ t }}
        </p>
      } @else {
        <div
          class="s-banner s-note"
          [class.readonly]="commitment()?.level !== 'warning'"
          [class.warn]="commitment()?.level === 'warning'"
          role="note"
          data-testid="sandbox-commitment-note"
          [attr.data-level]="commitment()?.level || 'info'"
        >
          {{ t }}
        </div>
      }
    }
  `,
})
export class SandboxCommitmentNoteComponent {
  readonly commitment = input<SandboxCommitmentDto | null | undefined>(null);
  protected readonly text = computed(() => {
    const c = this.commitment();
    return c && typeof c === "object" ? commitmentText(c) : "";
  });
}
