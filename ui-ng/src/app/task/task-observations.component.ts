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
  input,
  signal,
  DestroyRef,
  inject,
  NgZone,
} from "@angular/core";
import { DatePipe, JsonPipe, KeyValuePipe } from "@angular/common";
import type {
  GateObservations,
  GateObservationDiagnostics,
} from "../api/models";

@Component({
  selector: "g-task-observations",
  imports: [DatePipe, JsonPipe, KeyValuePipe],
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: [
    `
      :host {
        display: block;
      }
      section {
        border: 1px solid var(--md-sys-color-outline-variant);
        border-radius: var(--md-sys-shape-corner-medium);
        padding: 16px;
        margin-block: 12px;
        color: var(--md-sys-color-on-surface);
      }
      h2,
      h3,
      p {
        margin: 0 0 8px;
      }
      h2,
      h3 {
        font-weight: 600;
        font-size: 14px;
        line-height: 20px;
      }
      p,
      summary,
      dt,
      dd,
      li {
        font-size: 14px;
        line-height: 20px;
        overflow-wrap: anywhere;
      }
      summary {
        min-height: 24px;
        padding-block: 4px;
        cursor: pointer;
      }
      summary:focus-visible {
        outline: 2px solid var(--md-sys-color-primary);
        outline-offset: 2px;
      }
      .muted {
        color: var(--md-sys-color-on-surface-variant);
      }
      article {
        border-top: 1px solid var(--md-sys-color-outline-variant);
        padding-block: 12px;
      }
      dl {
        margin: 8px 0;
      }
      dd {
        margin: 0 0 8px;
      }
      .version {
        font-family: var(--md-sys-typescale-code-font, monospace);
        font-size: 12px;
        line-height: 16px;
        font-variant-numeric: tabular-nums;
      }
      pre {
        white-space: pre-wrap;
        overflow-wrap: anywhere;
        font-size: 12px;
        line-height: 16px;
      }
    `,
  ],
  template: `
    @if (observations(); as view) {
      <section aria-label="Gate observations" data-testid="gate-observations">
        <h2 [attr.aria-level]="pane() ? 3 : null">
          Built-in gates · Observation only · Shadow predictions
        </h2>
        <p class="muted">
          Legacy services remain authoritative. These observations do not
          establish readiness or successful completion and cannot change task
          behavior.
        </p>
        <p>
          Compared {{ view.compared_at | date: "medium" }} · legacy state then:
          <span class="version">{{ view.legacy_state }}</span>
        </p>
        @if (view.dirty) {
          <p>Unknown: collection is dirty or unavailable.</p>
        }
        @for (
          entry of view.results;
          track entry.evaluation_id;
          let i = $index
        ) {
          <article>
            <p class="muted">Shadow prediction · no operational effects</p>
            <h3 [attr.aria-level]="pane() ? 4 : null">
              <span class="version"
                >{{ entry.result.type }} v{{ entry.result.version }}</span
              >
              ·
              {{ entry.result.status }} · shadow
              {{ profile(view.bindings[i]?.config) }}
            </h3>
            @if (entry.result.type === "checks") {
              <p class="muted">
                Shadow CI prediction ·
                {{ checksLabel(entry.result.binding_id) }}
              </p>
            }
            @if (entry.result.type !== "checks") {
              <p class="muted">{{ profileLabel($index) }}</p>
            }
            <p>{{ entry.result.summary }}</p>
            @if (entry.result.blockers?.length) {
              <ul aria-label="Observed blockers (shadow only)">
                @for (blocker of entry.result.blockers; track $index) {
                  <li>
                    <span class="version">{{ blocker.code }}</span
                    >: {{ blocker.summary }}
                  </li>
                }
              </ul>
            }
            @if (entry.result.error; as error) {
              <p>Observation error: {{ error.code }} · {{ error.message }}</p>
            }
            @if (stale(entry.result.valid_until)) {
              <p>
                Stale observation: refresh the task to collect current evidence.
              </p>
            }
            @if (entry.invalidation_reason) {
              <p>Invalidated: {{ entry.invalidation_reason }}</p>
            }
            @if (entry.result.agent_text) {
              <p>{{ entry.result.agent_text }}</p>
            }
            @if (entry.result.human_text) {
              <p>{{ entry.result.human_text }}</p>
            }
            @for (wait of entry.result.waits; track wait.key ?? $index) {
              <p>
                Predicted wait only: {{ wait.event }} · owned by
                {{ wait.owner }} · evidence expires
                {{ wait.deadline | date: "medium" }}. No operational wait is
                installed.
              </p>
            }
            @if (
              !entry.result.human_text &&
              !entry.result.agent_text &&
              !entry.result.waits.length
            ) {
              <p class="muted">No next action or wait predicted.</p>
            }
            <details>
              <summary>Evidence and evaluation versions</summary>
              <p>
                Evaluated {{ entry.result.observed_at | date: "medium" }} ·
                valid until {{ entry.result.valid_until | date: "medium" }} ·
                {{ entry.disposition }} diagnostic revision {{ entry.revision }}
              </p>
              <p>
                {{ entry.evaluation_id }} · {{ entry.result.context_digest }}
              </p>
              @for (dep of entry.result.dependencies; track dep.key) {
                <p>
                  Scope: {{ dep.key }} · complete: {{ dep.complete }} ·
                  @if (dep.head) {
                    head <span class="version">{{ dep.head }}</span> ·
                  }
                  <span class="version">{{ dep.version }}</span>
                </p>
              }
              @for (
                evidence of entry.result.evidence;
                track evidence.kind + ":" + evidence.id + evidence.version
              ) {
                <p>
                  {{ evidence.kind }} #{{ evidence.id }} ·
                  @if (evidence.head) {
                    head <span class="version">{{ evidence.head }}</span> ·
                  }
                  <span class="version">{{ evidence.version }}</span>
                </p>
              }
            </details>
          </article>
        }
        @if (!view.results.length) {
          <p>Unknown: pending evidence collection.</p>
        }
        <details>
          <summary>Configuration, provenance and comparison history</summary>
          <p class="version">Source revision: {{ view.source_revision }}</p>
          @for (binding of view.bindings; track binding.id) {
            <p>{{ binding.type }} v{{ binding.version }}</p>
            @if (binding.subject?.repository) {
              <p>
                Repository {{ binding.subject?.repository }} · ref
                {{ binding.subject?.ref }}
              </p>
            }
            <pre
              >{{ binding.config | json }}
{{ binding.origin | json }}</pre>
          }
          @if (diagnostics(); as diag) {
            @if (diag.evaluations) {
              <p>
                First retained visible observation collected
                {{ diag.started_at | date: "medium" }} ·
                {{ diag.evaluations }} recorded evaluations
              </p>
            } @else {
              <p>
                History counts cover only the visible returned samples;
                aggregate metadata is restricted.
              </p>
            }
            <p class="muted">
              Counts cover retained evidence changes and hourly samples, not all
              evaluations. Up to 72 hours / 1024 samples per task; newest 20
              below. No cutover is scheduled.
            </p>
            @if (diag.counts["unknown_invalid_history"]) {
              <p>
                Some retained samples could not be safely read as
                observation-only history and were withheld. Current observations
                remain separate.
              </p>
            }
            @for (count of diag.counts | keyvalue; track count.key) {
              <p>{{ count.key }}: {{ count.value }}</p>
            }
            <ol>
              @for (sample of diag.history; track sample.generation) {
                <li>
                  {{ sample.at | date: "medium" }} ·
                  <span class="version">{{ sample.comparison }}</span> ·
                  revision {{ sample.generation }}
                  @for (
                    entry of sample.view.results;
                    track entry.evaluation_id
                  ) {
                    <span>
                      · {{ entry.result.type }}: {{ entry.result.status }}</span
                    >
                  }
                </li>
              }
            </ol>
          } @else {
            <p>Diagnostic history unavailable.</p>
          }
        </details>
      </section>
    }
  `,
})
export class TaskObservationsComponent {
  private readonly now = signal(Date.now());
  constructor() {
    // Display expiry must not keep Angular stability checks or unrelated
    // task flows pending on a permanent Zone macro-task.
    const timer = inject(NgZone).runOutsideAngular(() =>
      setInterval(() => this.now.set(Date.now()), 1000),
    );
    inject(DestroyRef).onDestroy(() => clearInterval(timer));
  }
  readonly observations = input<GateObservations>();
  readonly diagnostics = input<GateObservationDiagnostics>();
  readonly pane = input<boolean>(false);
  profile(config: unknown): string {
    if (!config || typeof config !== "object") return "unknown selection";
    const fields = config as Record<string, unknown>;
    for (const key of ["product", "mode", "predicate", "kind", "category"]) {
      if (typeof fields[key] === "string") return fields[key];
    }
    if (Array.isArray(fields["requirement_ids"])) return "named approval";
    return "unknown selection";
  }

  checksLabel(id?: string): string {
    const binding = this.observations()?.bindings.find((b) =>
      id?.endsWith("/" + b.id),
    );
    const profile = binding?.origin?.imported
      ? "Legacy CI import"
      : "Current CI policy";
    const subject = binding?.subject;
    return subject
      ? profile +
          " · " +
          subject.repository +
          " · " +
          subject.change_kind +
          " " +
          subject.id
      : profile;
  }
  profileLabel(index: number): string {
    const binding = this.observations()?.bindings[index];
    const config = binding?.config;
    if (
      binding?.type === "review" &&
      config &&
      typeof config === "object" &&
      "semantics" in config &&
      config.semantics === "legacy_completion_v1"
    ) {
      return "Imported legacy comparison · Shadow only";
    }
    return "Current-scope prediction · Shadow only";
  }
  stale(until: string): boolean {
    return Date.parse(until) <= this.now();
  }
}
