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
  inject,
  input,
  output,
  signal,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import type { GoalAssumption, GoalDeliverable, Task } from "../api/models";
import { CreateTaskService } from "../create/create-task.service";
import { isTaskMerged } from "../inbox/inbox-classifier";
import { InboxStore } from "../inbox/inbox.store";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import { errorText } from "../sessions/session-helpers";
import { StatusIconComponent } from "../shared/status-icon.component";
import { TaskStore } from "./task.store";

@Component({
  selector: "g-task-goal-card",
  imports: [StatusIconComponent],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./task-goal-card.component.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (visible()) {
      <section
        class="goal-card"
        data-testid="task-goal-card"
        aria-label="Autonomous goal scorecard"
      >
        <div class="goal-card-head">
          <div class="goal-card-meta">
            <span
              [class]="'status-pill ' + statusTone()"
              data-testid="goal-status-pill"
            >
              <g-status-icon [tone]="statusTone()" />{{ statusLabel() }}
            </span>
            @if (budgetSummary(); as budget) {
              <span class="goal-sub">{{ budget }}</span>
            }
          </div>
          <div class="goal-card-actions">
            @if (showSwitchSupervised()) {
              <button
                type="button"
                class="btn-tonal"
                data-testid="act-switch-supervised"
                [disabled]="busy()"
                (click)="switchToSupervised()"
              >
                Switch to supervised
              </button>
            }
            @if (canRevise()) {
              <button
                type="button"
                class="btn-outlined"
                data-testid="act-revise-goal"
                [disabled]="busy()"
                (click)="revise($event)"
              >
                Revise…
              </button>
            }
          </div>
        </div>

        @if (summaryText(); as summary) {
          <p class="goal-card-summary" data-testid="goal-summary">
            {{ summary }}
          </p>
        }

        <div class="goal-card-grid">
          <div class="goal-section" data-testid="goal-deliverables">
            <h3 class="goal-section-title">
              Deliverables
              @if (deliverables().length) {
                ({{ verifiedCount() }}/{{ deliverables().length }} verified)
              }
            </h3>
            @if (deliverables().length) {
              <ul class="goal-list">
                @for (d of deliverables(); track d.requirement + ":" + $index) {
                  <li class="goal-item" data-testid="goal-deliverable">
                    <div class="goal-item-row">
                      <span
                        [class]="
                          'goal-check ' + (d.verified ? 'ok' : 'pending')
                        "
                        aria-hidden="true"
                        >{{ d.verified ? "✓" : "!" }}</span
                      >
                      <span>{{ d.requirement }}</span>
                    </div>
                    @if (d.evidence) {
                      <span class="goal-sub">{{ d.evidence }}</span>
                    }
                  </li>
                }
              </ul>
            } @else {
              <span class="goal-sub">No deliverable checks recorded yet.</span>
            }
          </div>

          <div class="goal-section" data-testid="goal-assumptions">
            <h3 class="goal-section-title">
              Assumptions ({{ assumptions().length }})
            </h3>
            @if (assumptions().length) {
              <ul class="goal-list">
                @for (
                  a of assumptions();
                  track a.id || a.topic + ":" + $index
                ) {
                  <li class="goal-item" data-testid="goal-assumption">
                    <div class="goal-item-row">
                      <strong>{{ a.topic }}:</strong>
                      <span>{{ a.decision }}</span>
                    </div>
                    @if (a.rationale) {
                      <span class="goal-sub">{{ a.rationale }}</span>
                    }
                    @if (a.alternatives) {
                      <span class="goal-sub"
                        >Alternatives: {{ a.alternatives }}</span
                      >
                    }
                  </li>
                }
              </ul>
            } @else {
              <span class="goal-sub">No assumptions recorded yet.</span>
            }
          </div>
        </div>
      </section>
    }
  `,
})
export class TaskGoalCardComponent {
  readonly task = input.required<Task>();
  readonly attemptsCount = input<number>(0);
  readonly acted = output<void>();

  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly createSvc = inject(CreateTaskService);
  private readonly snackbar = inject(SnackbarService);
  private readonly taskStore = inject(TaskStore);
  private readonly inboxStore = inject(InboxStore);

  protected readonly busy = signal(false);

  protected readonly visible = computed(() => {
    const t = this.task();
    return (
      !!t.goalMode?.enabled ||
      !!t.goalScorecard ||
      !!(t.goalAssumptions && t.goalAssumptions.length > 0)
    );
  });

  protected readonly deliverables = computed<GoalDeliverable[]>(
    () => this.task().goalScorecard?.deliverables ?? [],
  );

  protected readonly verifiedCount = computed(
    () => this.deliverables().filter((d) => d.verified).length,
  );

  protected readonly assumptions = computed<GoalAssumption[]>(
    () => this.task().goalAssumptions ?? [],
  );

  protected readonly statusTone = computed<
    "healthy" | "warn" | "setup" | "neutral"
  >(() => {
    const sc = this.task().goalScorecard;
    if (sc?.verified) return "healthy";
    if (sc?.stoppedAtCap) return "warn";
    if (this.task().goalMode?.enabled) return "setup";
    return "neutral";
  });

  protected readonly statusLabel = computed(() => {
    const t = this.task();
    const sc = t.goalScorecard;
    if (sc?.verified) {
      const n = sc.passesUsed || 1;
      return `Goal verified · ${n} ${n === 1 ? "pass" : "passes"}`;
    }
    if (sc?.stoppedAtCap) {
      const used = sc.passesUsed ?? 0;
      const max = sc.maxPasses || t.goalMode?.maxPasses || 8;
      return `Stopped at cap · ${used} / ${max} passes`;
    }
    if (t.goalMode?.enabled) {
      const cur = sc?.passesUsed || Math.max(1, this.attemptsCount() || 1);
      const max = sc?.maxPasses || t.goalMode.maxPasses || 8;
      return `Autonomous goal · Pass ${cur} / ${max}`;
    }
    const used = sc?.passesUsed ?? 0;
    return `Goal scorecard · ${used} ${used === 1 ? "pass" : "passes"}`;
  });

  protected readonly budgetSummary = computed(() => {
    const gm = this.task().goalMode;
    if (!gm?.enabled) return "";
    const parts: string[] = [];
    if (gm.wallClockMinutes) parts.push(`${gm.wallClockMinutes}m budget`);
    if (gm.mockMissingSecrets) parts.push("Mock missing secrets");
    return parts.join(" · ");
  });

  protected readonly summaryText = computed(() => {
    const sc = this.task().goalScorecard;
    if (!sc) return "";
    if (sc.summary && sc.stopReason) {
      return `${sc.summary} (${sc.stopReason})`;
    }
    return sc.summary || sc.stopReason || "";
  });

  protected readonly showSwitchSupervised = computed(() => {
    const t = this.task();
    if (!t.goalMode?.enabled || t.state === "closed" || isTaskMerged(t)) {
      return false;
    }
    return (
      this.access.canAccessTask("PATCH /api/tasks/{id}", t.repo || null) &&
      this.access.canScope("tasks:admin", t.repo || null)
    );
  });

  protected readonly canRevise = computed(() => {
    const t = this.task();
    return (
      this.access.canAccessTask("POST /api/tasks", t.repo || null) &&
      this.access.canScope("tasks:admin", t.repo || null)
    );
  });

  protected async switchToSupervised(): Promise<void> {
    if (this.busy()) return;
    const t = this.task();
    this.busy.set(true);
    try {
      await this.api.patch(
        "PATCH /api/tasks/{id}",
        { id: t.id },
        { goalMode: { enabled: false } },
      );
      void this.taskStore.refresh();
      void this.inboxStore.reloadSelectedDetail();
      this.acted.emit();
      this.snackbar.show(`Switched #${t.id} to supervised mode`);
    } catch (err) {
      this.snackbar.show(
        `Could not switch #${t.id} to supervised mode: ${errorText(err)}`,
      );
    } finally {
      this.busy.set(false);
    }
  }

  protected revise(ev: Event): void {
    const t = this.task();
    const gm = t.goalMode;
    const title = t.title.startsWith("Revise: ")
      ? t.title
      : `Revise: ${t.title}`;
    const lines: string[] = [`Follow-up revision on #${t.id} (${t.title}).`];
    if (t.goalScorecard?.summary) {
      lines.push(`Prior scorecard: ${t.goalScorecard.summary}`);
    }
    if (t.goalAssumptions?.length) {
      lines.push(
        "Prior assumptions:\n" +
          t.goalAssumptions
            .map((a) => `- ${a.topic}: ${a.decision}`)
            .join("\n"),
      );
    }
    this.createSvc.open(
      {
        title,
        description: lines.join("\n\n"),
        repo: t.repo || "",
        base: t.branch || t.base || "",
        generatedFrom: t.id,
        autoApproveProposals: this.access.canScope(
          "tasks:admin",
          t.repo || null,
        ),
        goalMode: {
          enabled: true,
          maxPasses: gm?.maxPasses ?? t.goalScorecard?.maxPasses ?? 8,
          wallClockMinutes: gm?.wallClockMinutes ?? 120,
          mockMissingSecrets: gm?.mockMissingSecrets ?? true,
        },
      },
      ev.currentTarget as HTMLElement | null,
    );
  }
}
