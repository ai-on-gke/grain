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

// The merge queue's overlap re-checks (GET /api/merge-batches), as the
// classic MergeBatchPanel.jsx shows them: a change whose diff overlaps
// what has landed on its base branch since its own run is merged onto a
// throwaway ref and judged by one grain CI run before it lands. /next/
// had no view of them at all (2026-11-16 repository walkthrough), so a
// change sitting in one read as merely "waiting".
//
// With task set it shows only the re-checks that task is in, and nothing
// when there are none; without it, every re-check of repo, or a line
// saying what would appear here.

import {
  ChangeDetectionStrategy,
  Component,
  OnDestroy,
  OnInit,
  computed,
  inject,
  input,
  signal,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import { GrainApi } from "../api/grain-api.service";
import { AccessService } from "../services/access.service";

export interface MergeBatchMember {
  task: string;
  title?: string;
  number?: number;
  headRef?: string;
  head?: string;
}

export interface MergeBatch {
  repo: string;
  branch: string;
  ref: string;
  baseCommit?: string;
  commit?: string;
  runId?: string;
  startedAt?: string;
  finishedAt?: string;
  running?: boolean;
  conclusion?: string;
  detail?: string;
  members: MergeBatchMember[];
}

interface MergeBatches {
  batches?: MergeBatch[];
  maxAgeSeconds?: number;
}

export interface Standing {
  label: string;
  tone: "planned" | "healthy" | "error" | "setup";
  title: string;
}

// batchAge is the classic panel's: elapsed against the queue's maximum
// while the run is going, and how long it ran once it has finished.
export function batchAge(
  batch: MergeBatch,
  maxAgeSeconds: number | undefined,
  now = Date.now(),
): string {
  if (!batch.startedAt) return "";
  const started = new Date(batch.startedAt).getTime();
  const until = batch.finishedAt ? new Date(batch.finishedAt).getTime() : now;
  const minutes = Math.max(0, Math.round((until - started) / 60000));
  const limit = maxAgeSeconds ? Math.round(maxAgeSeconds / 60) : 0;
  if (!batch.running) return `ran for ${minutes}m`;
  return limit ? `${minutes}m of ${limit}m` : `${minutes}m`;
}

export function batchStanding(batch: MergeBatch): Standing {
  if (batch.running) {
    return {
      label: "Testing",
      tone: "planned",
      title:
        "One grain CI run is judging the base branch with this change merged into it. It merges if the run passes; nothing has landed yet.",
    };
  }
  if (batch.conclusion === "success") {
    return {
      label: "Passed",
      tone: "healthy",
      title: "The re-check was green, so the change merges.",
    };
  }
  if (batch.conclusion === "failure") {
    return {
      label: "Failed",
      tone: "error",
      title:
        "The change broke grain CI when merged on top of the base branch. It goes back to its own agent for repair and the rest of the queue carries on.",
    };
  }
  return {
    label: "Abandoned",
    tone: "setup",
    title:
      "The run said nothing about the tree -- it errored, was cancelled, was lost, or took longer than the queue waits. No failure can be read out of that, so the re-check was discarded.",
  };
}

const REFRESH_MS = 10_000;

@Component({
  selector: "g-merge-recheck-panel",
  changeDetection: ChangeDetectionStrategy.OnPush,
  imports: [RouterLink],
  styles: `
    :host {
      display: block;
    }
    .mr-title {
      margin: 0 0 4px;
      font-size: 14px;
      font-weight: 600;
    }
    .mr-caption {
      margin: 0;
      font-size: 12px;
      line-height: 16px;
      color: var(--md-sys-color-on-surface-variant);
    }
    .mr-batch {
      margin-top: 8px;
      padding: 10px 12px;
      border: 1px solid var(--md-sys-color-outline-variant);
      border-radius: 12px;
      min-width: 0;
    }
    .mr-head {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: 8px;
    }
    .mr-branch {
      font-weight: 600;
      overflow-wrap: anywhere;
    }
    .mr-ref {
      margin-top: 4px;
      font-size: 12px;
      color: var(--md-sys-color-on-surface-variant);
      overflow-wrap: anywhere;
    }
    .mr-members {
      margin: 6px 0 0;
      padding-left: 20px;
      font-size: 13px;
    }
    .mr-members li {
      overflow-wrap: anywhere;
    }
    .mr-members li.is-this {
      font-weight: 600;
    }
  `,
  template: `
    @if (!task() || shown().length > 0) {
      <section data-testid="merge-recheck-panel" aria-labelledby="mr-title">
        <h3 class="mr-title" id="mr-title">
          {{ task() ? "Merge re-check" : "Merge re-checks" }}
        </h3>
        @if (!loaded()) {
          <p class="mr-caption" role="status">Loading merge re-checks…</p>
        } @else if (error()) {
          <p class="mr-caption" role="status">
            Merge re-checks could not be read: {{ error() }}
          </p>
        } @else if (shown().length === 0) {
          <p class="mr-caption">
            No re-check is running. When a finished change overlaps what has
            landed on its base branch since the commit its own run tested, grain
            merges it onto a throwaway ref and re-checks it before it lands.
            That re-check appears here while it runs.
          </p>
        }
        @for (b of shown(); track b.repo + " " + b.branch) {
          @let st = standing(b);
          <div class="mr-batch" data-testid="merge-recheck-batch">
            <div class="mr-head">
              <span class="mr-branch">{{ b.repo }} → {{ b.branch }}</span>
              <span
                class="status-pill"
                [class]="'status-pill ' + st.tone"
                [attr.title]="st.title"
                data-testid="merge-recheck-standing"
                >{{ st.label }}</span
              >
              <span
                class="mr-caption tabular"
                data-testid="merge-recheck-age"
                >{{ age(b) }}</span
              >
              @if (b.runId) {
                <a
                  class="mr-caption"
                  [routerLink]="['/ci', 'runs', b.runId]"
                  data-testid="merge-recheck-run"
                  >Show the run</a
                >
              }
            </div>
            <div
              class="mr-ref tabular"
              [attr.title]="
                'Built on ' +
                b.ref +
                ' from ' +
                b.branch +
                ' at ' +
                short(b.baseCommit) +
                '. Nothing merges from this ref: it is where the re-check runs, and what lands is the change\\'s own pull request.'
              "
            >
              {{ b.ref }} at {{ short(b.commit) }}
            </div>
            @if (b.detail && !b.running) {
              <p class="mr-caption">{{ b.detail }}</p>
            }
            <ol class="mr-members">
              @for (m of b.members; track m.task) {
                <li [class.is-this]="m.task === task()">
                  <a [routerLink]="['/task', m.task]">#{{ m.task }}</a>
                  {{ m.title }}
                  @if (m.number) {
                    <span class="mr-caption"> · PR #{{ m.number }}</span>
                  }
                </li>
              }
            </ol>
          </div>
        }
      </section>
    }
  `,
})
export class MergeRecheckPanelComponent implements OnInit, OnDestroy {
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);

  readonly repo = input<string>("");
  readonly task = input<string>("");

  protected readonly data = signal<MergeBatches | null>(null);
  protected readonly loaded = signal(false);
  protected readonly error = signal<string | null>(null);
  protected readonly now = signal(Date.now());
  private timer: ReturnType<typeof setInterval> | null = null;

  protected readonly shown = computed(() => {
    const all = this.data()?.batches ?? [];
    const repo = this.repo().toLowerCase();
    const task = this.task();
    return all.filter(
      (b) =>
        (!repo || b.repo.toLowerCase() === repo) &&
        (!task || b.members.some((m) => m.task === task)),
    );
  });

  ngOnInit(): void {
    if (!this.access.can("GET /api/merge-batches")) {
      this.loaded.set(true);
      this.error.set(this.access.deniedReason("GET /api/merge-batches"));
      return;
    }
    void this.refresh();
    this.timer = setInterval(() => void this.refresh(), REFRESH_MS);
  }

  ngOnDestroy(): void {
    if (this.timer) clearInterval(this.timer);
  }

  private async refresh(): Promise<void> {
    try {
      const repo = this.repo();
      const r = await this.api.get<MergeBatches>(
        "GET /api/merge-batches",
        undefined,
        repo ? `repo=${encodeURIComponent(repo)}` : undefined,
      );
      this.data.set(r ?? {});
      this.error.set(null);
    } catch (err) {
      this.error.set(err instanceof Error ? err.message : String(err));
    } finally {
      this.now.set(Date.now());
      this.loaded.set(true);
    }
  }

  protected standing(b: MergeBatch): Standing {
    return batchStanding(b);
  }

  protected age(b: MergeBatch): string {
    return batchAge(b, this.data()?.maxAgeSeconds, this.now());
  }

  protected short(sha: string | undefined): string {
    return sha ? sha.slice(0, 12) : "";
  }
}
