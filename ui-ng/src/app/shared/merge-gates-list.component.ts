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
  ViewEncapsulation,
  computed,
  input,
  output,
} from "@angular/core";
import { RouterLink } from "@angular/router";
import { StatusIconComponent } from "./status-icon.component";
import type { MergeGate } from "../api/models";
import { gateActions, type AnyTask, type TaskAction } from "./task-actions";

// The merge gates list (the classic ui/src/components/MergeGatesList.jsx):
// every gate the server reports for a task, with its state, detail, a link
// to its review task or CI run, whether it may be overridden, and -- on a
// gate a person has to settle -- the actions that settle it, drawn from
// the same availableActions the action area shows.

const STATE_WORDS: Record<string, string> = {
  passed: "passed",
  overridden: "overridden",
  failing: "failing",
  findings: "findings",
  running: "running",
  needed: "needed",
};

@Component({
  selector: "g-merge-gates-list",
  imports: [RouterLink, StatusIconComponent],
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  styles: `
    .merge-gates-list {
      list-style: none;
      margin: 0;
      padding: 0;
      display: flex;
      flex-direction: column;
      gap: 4px;
    }
    .merge-gate-row {
      display: grid;
      grid-template-columns: minmax(0, max-content) 1fr auto;
      align-items: center;
      gap: 4px 12px;
      padding: 4px 0;
      border-top: 1px solid var(--md-sys-color-outline-variant);
      font-size: 13px;
    }
    .merge-gate-row:first-child {
      border-top: 0;
    }
    /* A gate held on findings or failing checks says what in its chip
       (chipText) on one line, cut with an ellipsis; the whole of it is in
       the tooltip. */
    .merge-gate-row .gate-chip {
      box-sizing: border-box;
      max-width: 100%;
      min-width: 0;
      flex-shrink: 1;
      justify-self: start;
    }
    .merge-gate-row .gate-chip g-status-icon,
    .merge-gate-row .gate-chip-label {
      flex-shrink: 0;
    }
    .merge-gate-row .gate-chip-detail {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .merge-gate-detail {
      min-width: 0;
      overflow-wrap: anywhere;
    }
    .merge-gate-actions {
      display: flex;
      gap: 6px;
      flex-wrap: wrap;
      justify-content: flex-end;
    }
    @media (max-width: 600px) {
      .merge-gate-row {
        grid-template-columns: minmax(0, 1fr) auto;
      }
      .merge-gate-detail,
      .merge-gate-actions {
        grid-column: 1 / -1;
        justify-content: flex-start;
      }
    }
  `,
  template: `
    <ul
      class="merge-gates-list"
      aria-label="Merge gates"
      data-testid="merge-gates-list"
    >
      @for (g of gates(); track g.id) {
        <li
          class="merge-gate-row"
          [attr.data-testid]="'merge-gate-' + g.id"
          [attr.data-state]="g.state"
        >
          <span
            [class]="'status-pill gate-chip ' + tone(g)"
            [attr.data-gate]="g.id"
            [attr.title]="g.detail || g.state"
          >
            <g-status-icon [tone]="tone(g)" [kind]="g.state" />
            <span class="gate-chip-label">{{ g.label || g.id }}: </span>
            <span class="gate-chip-detail">{{ chipText(g) }}</span>
          </span>
          <span class="merge-gate-detail muted">
            @if (restOfDetail(g)) {
              {{ restOfDetail(g) }}
            }
            @if (g.approvedBy) {
              · by {{ g.approvedBy }}
            }
            @if (g.reviewTask) {
              ·
              <a
                [routerLink]="['/task', g.reviewTask]"
                [attr.data-testid]="'merge-gate-link-' + g.id"
                >review task #{{ g.reviewTask }}</a
              >
            } @else if (g.id === "ci" && ciLink()) {
              ·
              <a
                [routerLink]="['/task', task().id]"
                [queryParams]="{ tab: 'changes' }"
                [attr.data-testid]="'merge-gate-link-' + g.id"
                >view the run</a
              >
            }
            @if (g.overridable && !satisfied(g)) {
              · can be overridden
            }
          </span>
          <span class="merge-gate-actions">
            @for (a of rowActions(g); track a.id) {
              <button
                type="button"
                [class]="cls(a)"
                [attr.data-testid]="'gate-' + g.id + '-' + a.id"
                [attr.data-action]="a.id"
                [disabled]="!!a.disabled || busy()"
                [attr.title]="a.disabled || a.tip || null"
                (click)="run.emit(a)"
              >
                {{ a.label }}
              </button>
            }
          </span>
        </li>
      }
    </ul>
  `,
})
export class MergeGatesListComponent {
  readonly task = input.required<AnyTask>();
  readonly actions = input<TaskAction[]>([]);
  readonly busy = input(false);
  readonly run = output<TaskAction>();

  protected readonly gates = computed(() => this.task().mergeGates ?? []);
  protected readonly ciLink = computed(
    () => !!(this.task().ci?.runId || this.task().pullRequest),
  );

  protected rowActions(g: MergeGate): TaskAction[] {
    if (this.satisfied(g)) return [];
    return gateActions(this.actions(), g);
  }

  protected satisfied(g: MergeGate): boolean {
    return g.state === "passed" || g.state === "overridden";
  }

  protected word(g: MergeGate): string {
    return STATE_WORDS[g.state] ?? g.state;
  }

  // chipText is what a gate's chip says after its label: for a gate held
  // on findings or failing, its detail -- the count and the first finding,
  // or the checks that failed -- so the row says what is wrong without a
  // hover; otherwise its state.
  protected chipText(g: MergeGate): string {
    const detail = (g.detail ?? "").trim();
    if ((g.state === "findings" || g.state === "failing") && detail) {
      return detail;
    }
    return this.word(g);
  }

  // restOfDetail is the gate's detail when the chip does not already say
  // it (and it says more than the state does).
  protected restOfDetail(g: MergeGate): string {
    const detail = (g.detail ?? "").trim();
    if (!detail || detail === this.chipText(g) || detail === g.state) {
      return "";
    }
    return detail;
  }

  protected tone(g: MergeGate): string {
    if (g.state === "passed") return "healthy";
    if (g.state === "overridden") return "proposal";
    if (g.state === "failing" || g.state === "findings") return "error";
    if (g.state === "running") return "setup";
    return "reply";
  }

  protected cls(a: TaskAction): string {
    const base =
      a.variant === "primary"
        ? "btn-primary"
        : a.variant === "tonal"
          ? "btn-tonal"
          : "btn-text";
    return a.danger ? `${base} danger` : base;
  }
}
