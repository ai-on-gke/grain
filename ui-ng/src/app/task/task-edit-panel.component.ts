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
  effect,
  inject,
  input,
  output,
  signal,
  untracked,
} from "@angular/core";
import { GrainApi } from "../api/grain-api.service";
import type {
  GoalDecl,
  GrainConfig,
  TaskContextView,
  TaskDetail,
} from "../api/models";
import { targetableRepos } from "../create/new-task-form";
import { AccessService } from "../services/access.service";
import { SnackbarService } from "../services/snackbar.service";
import {
  type PillOption,
  PillSelectComponent,
} from "../shared/pill-select.component";
import { PriorityChipComponent } from "../shared/priority-chip.component";
import { priorityOf } from "../tasks/task-view";
import { TaskStore } from "./task.store";

const NETWORK_OPTIONS: readonly PillOption[] = [
  { value: "", label: "Deployment default" },
  { value: "open", label: "Open internet" },
  { value: "none", label: "No internet (none)" },
];

const NOTHING_OPTIONS: readonly PillOption[] = [
  { value: "close", label: "Close the task" },
  { value: "ack", label: "Ask me to acknowledge" },
];

const FRAMEWORK_OPTIONS: readonly PillOption[] = [
  { value: "", label: "Deployment default" },
  { value: "antigravity", label: "Antigravity" },
  { value: "claude", label: "Claude" },
  { value: "codex", label: "Codex" },
];

const GOAL_KINDS: readonly PillOption[] = [
  { value: "person-closes", label: "Until a person closes it" },
  { value: "base-green", label: "Base branch green" },
  { value: "pr-merged", label: "Pull request merged" },
  { value: "tasks-closed", label: "Named tasks closed" },
  { value: "pipeline-healthy", label: "Pipeline healthy" },
  { value: "cl-submitted", label: "Changelist submitted" },
  { value: "nightly-green", label: "Nightly workflow green" },
];

const GOAL_ON_MET: readonly PillOption[] = [
  { value: "close", label: "Close when met" },
  { value: "rest", label: "Rest when met" },
];

const GOAL_ON_LIMIT: readonly PillOption[] = [
  { value: "hold", label: "Hold on limit" },
  { value: "notify", label: "Notify & back off" },
];

@Component({
  selector: "g-task-edit-panel",
  imports: [PillSelectComponent, PriorityChipComponent],
  encapsulation: ViewEncapsulation.None,
  styleUrl: "./task-edit-panel.css",
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <section
      class="task-edit-drawer"
      data-testid="task-edit-panel"
      aria-labelledby="task-edit-h"
    >
      <div class="edit-drawer-head">
        <h2 id="task-edit-h" class="section-title">
          Edit task #{{ task().id }}
        </h2>
        <button
          type="button"
          class="btn-text"
          data-testid="edit-close-btn"
          (click)="closed.emit()"
        >
          Close
        </button>
      </div>

      @if (can("PATCH /api/tasks/{id}")) {
        <form (submit)="saveTask($event)">
          <div style="display: flex; flex-direction: column; gap: 12px">
            <label class="edit-field">
              <span>Title</span>
              <input
                type="text"
                data-testid="edit-title"
                [value]="title()"
                (input)="title.set($any($event.target).value)"
              />
            </label>

            <label class="edit-field">
              <span>Description</span>
              <textarea
                rows="4"
                data-testid="edit-description"
                [value]="description()"
                (input)="description.set($any($event.target).value)"
              ></textarea>
            </label>

            <div class="edit-grid">
              <div class="edit-field">
                <span>Repository</span>
                <g-pill-select
                  [options]="repoOptions()"
                  [value]="repo()"
                  ariaLabel="Repository"
                  align="left"
                  testid="edit-repo"
                  (valueChange)="repo.set($event)"
                />
              </div>

              <label class="edit-field">
                <span>Base branch</span>
                <input
                  type="text"
                  data-testid="edit-base"
                  placeholder="Default branch"
                  [value]="base()"
                  (input)="base.set($any($event.target).value)"
                />
              </label>

              <div class="edit-field">
                <span>Priority</span>
                <g-priority-chip
                  [priority]="priority()"
                  [editable]="true"
                  testid="edit-priority"
                  menuTestid="edit-priority-menu"
                  (priorityChange)="priority.set($event)"
                />
              </div>

              <div class="edit-field">
                <span>Model</span>
                <g-pill-select
                  [options]="modelOptions()"
                  [value]="model()"
                  ariaLabel="Model"
                  align="left"
                  testid="edit-model"
                  (valueChange)="model.set($event)"
                />
              </div>

              <div class="edit-field">
                <span>Agent framework</span>
                <g-pill-select
                  [options]="frameworkOptions"
                  [value]="agentFramework()"
                  ariaLabel="Agent framework"
                  align="left"
                  testid="edit-framework"
                  (valueChange)="agentFramework.set($event)"
                />
              </div>

              <div class="edit-field">
                <span>Connection (backend)</span>
                <g-pill-select
                  [options]="backendOptions()"
                  [value]="agentBackendId()"
                  ariaLabel="Connection backend"
                  align="left"
                  testid="edit-backend"
                  (valueChange)="agentBackendId.set($event)"
                />
              </div>

              <div class="edit-field">
                <span>Network access</span>
                <g-pill-select
                  [options]="networkOptions"
                  [value]="networkAccess()"
                  ariaLabel="Network access"
                  align="left"
                  testid="edit-network"
                  (valueChange)="networkAccess.set($event)"
                />
              </div>

              <div class="edit-field">
                <span>If nothing needed</span>
                <g-pill-select
                  [options]="nothingOptions"
                  [value]="nothingNeededAction()"
                  ariaLabel="Nothing needed action"
                  align="left"
                  testid="edit-nothing"
                  (valueChange)="nothingNeededAction.set($event)"
                />
              </div>
            </div>

            <div class="edit-toggles">
              <label class="check-row">
                <input
                  type="checkbox"
                  data-testid="edit-auto-merge"
                  [checked]="autoMerge()"
                  (change)="autoMerge.set($any($event.target).checked)"
                />
                <span>Auto-merge once CI &amp; review pass</span>
              </label>
              <label class="check-row">
                <input
                  type="checkbox"
                  data-testid="edit-auto-approve"
                  [checked]="autoApproveProposals()"
                  (change)="
                    autoApproveProposals.set($any($event.target).checked)
                  "
                />
                <span>Auto-approve child proposals</span>
              </label>
            </div>

            <div class="edit-actions">
              <button
                type="submit"
                class="btn-primary"
                data-testid="edit-save-btn"
                [disabled]="!title().trim() || store.isBusy('edit', task().id)"
              >
                Save changes
              </button>
            </div>
          </div>
        </form>
      }

      <!-- Prompt Brief Override (POST /api/tasks/{id}/brief) -->
      @if (can("POST /api/tasks/{id}/brief")) {
        <div class="edit-subcard" data-testid="edit-brief-section">
          <h3 class="section-subtitle" style="margin: 0">
            Standing prompt brief
          </h3>
          <p class="muted" style="margin: 0; font-size: 12px">
            Replaces the original description in future agent prompts while
            preserving history. Leave brief empty to revert.
          </p>
          <label class="edit-field">
            <span>Current brief</span>
            <textarea
              rows="3"
              data-testid="edit-brief-text"
              placeholder="Updated prompt brief for subsequent runs"
              [value]="briefText()"
              (input)="briefText.set($any($event.target).value)"
            ></textarea>
          </label>
          <label class="edit-field">
            <span>Why the brief changed (required)</span>
            <input
              type="text"
              data-testid="edit-brief-reason"
              placeholder="e.g. Narrowed scope to backend handlers"
              [value]="briefReason()"
              (input)="briefReason.set($any($event.target).value)"
            />
          </label>
          <div class="edit-actions">
            <button
              type="button"
              class="btn-tonal"
              data-testid="edit-brief-save"
              [disabled]="
                !briefReason().trim() || store.isBusy('brief', task().id)
              "
              (click)="saveBrief()"
            >
              Update brief
            </button>
          </div>
        </div>
      }

      <!-- Durable Task Goal Editor (PUT / DELETE /api/tasks/{id}/goal) -->
      @if (can("PUT /api/tasks/{id}/goal") && task().state !== "closed") {
        <div class="edit-subcard" data-testid="edit-goal-section">
          <h3 class="section-subtitle" style="margin: 0">Task goal</h3>
          <div class="edit-grid">
            <div class="edit-field">
              <span>Goal kind</span>
              <g-pill-select
                [options]="goalKinds"
                [value]="goalKind()"
                ariaLabel="Goal kind"
                align="left"
                testid="edit-goal-kind"
                (valueChange)="goalKind.set($event)"
              />
            </div>

            @if (
              goalKind() === "base-green" ||
              goalKind() === "nightly-green" ||
              goalKind() === "pr-merged"
            ) {
              <label class="edit-field">
                <span>Goal repository</span>
                <input
                  type="text"
                  data-testid="edit-goal-repo"
                  placeholder="owner/name"
                  [value]="goalRepo()"
                  (input)="goalRepo.set($any($event.target).value)"
                />
              </label>
            }

            @if (goalKind() === "base-green") {
              <label class="edit-field">
                <span>Branch</span>
                <input
                  type="text"
                  data-testid="edit-goal-branch"
                  placeholder="main"
                  [value]="goalBranch()"
                  (input)="goalBranch.set($any($event.target).value)"
                />
              </label>
            }

            @if (goalKind() === "nightly-green") {
              <label class="edit-field">
                <span>Workflow</span>
                <input
                  type="text"
                  data-testid="edit-goal-workflow"
                  placeholder="nightly.yml"
                  [value]="goalWorkflow()"
                  (input)="goalWorkflow.set($any($event.target).value)"
                />
              </label>
            }

            @if (
              goalKind() === "pr-merged" ||
              goalKind() === "tasks-closed" ||
              goalKind() === "cl-submitted"
            ) {
              <label class="edit-field">
                <span>Target task ID(s)</span>
                <input
                  type="text"
                  data-testid="edit-goal-tasks"
                  placeholder="e.g. 42 or 42, 43"
                  [value]="goalTasks()"
                  (input)="goalTasks.set($any($event.target).value)"
                />
              </label>
            }

            <div class="edit-field">
              <span>On met</span>
              <g-pill-select
                [options]="goalOnMetOpts"
                [value]="goalOnMet()"
                ariaLabel="On goal met"
                align="left"
                testid="edit-goal-on-met"
                (valueChange)="goalOnMet.set($event)"
              />
            </div>

            <div class="edit-field">
              <span>At limit</span>
              <g-pill-select
                [options]="goalOnLimitOpts"
                [value]="goalOnLimit()"
                ariaLabel="On goal limit"
                align="left"
                testid="edit-goal-on-limit"
                (valueChange)="goalOnLimit.set($event)"
              />
            </div>
          </div>

          <div class="edit-actions">
            @if (task().goal && can("DELETE /api/tasks/{id}/goal")) {
              <button
                type="button"
                class="btn-text danger"
                data-testid="edit-goal-clear"
                [disabled]="store.isBusy('goal-clear', task().id)"
                (click)="clearGoal()"
              >
                Clear goal
              </button>
            }
            <button
              type="button"
              class="btn-tonal"
              data-testid="edit-goal-save"
              [disabled]="store.isBusy('goal-save', task().id)"
              (click)="saveGoal()"
            >
              Set goal
            </button>
          </div>
        </div>
      }
    </section>
  `,
})
export class TaskEditPanelComponent {
  protected readonly store = inject(TaskStore);
  private readonly api = inject(GrainApi);
  private readonly access = inject(AccessService);
  private readonly snackbar = inject(SnackbarService);

  readonly task = input.required<TaskDetail>();
  readonly acted = output<void>();
  readonly closed = output<void>();

  protected readonly networkOptions = NETWORK_OPTIONS;
  protected readonly nothingOptions = NOTHING_OPTIONS;
  protected readonly frameworkOptions = FRAMEWORK_OPTIONS;
  protected readonly goalKinds = GOAL_KINDS;
  protected readonly goalOnMetOpts = GOAL_ON_MET;
  protected readonly goalOnLimitOpts = GOAL_ON_LIMIT;

  protected readonly cfg = signal<GrainConfig | null>(null);
  protected readonly title = signal("");
  protected readonly description = signal("");
  protected readonly repo = signal("");
  protected readonly base = signal("");
  protected readonly priority = signal(5);
  protected readonly model = signal("");
  protected readonly agentFramework = signal("");
  protected readonly agentBackendId = signal("");
  protected readonly networkAccess = signal("");
  protected readonly nothingNeededAction = signal("close");
  protected readonly autoMerge = signal(false);
  protected readonly autoApproveProposals = signal(false);

  protected readonly briefText = signal("");
  protected readonly briefReason = signal("");

  protected readonly goalKind = signal("person-closes");
  protected readonly goalRepo = signal("");
  protected readonly goalBranch = signal("main");
  protected readonly goalWorkflow = signal("nightly");
  protected readonly goalTasks = signal("");
  protected readonly goalOnMet = signal("close");
  protected readonly goalOnLimit = signal("hold");

  protected readonly repoOptions = computed<PillOption[]>(() => {
    const list = targetableRepos(this.cfg(), [this.task()]);
    const cur = this.task().repo;
    const all = cur && !list.includes(cur) ? [cur, ...list] : list;
    return [
      ...(all.length ? [] : [{ value: "", label: "No repository" }]),
      ...all.map((r) => ({ value: r, label: r })),
    ];
  });

  protected readonly modelOptions = computed<PillOption[]>(() => {
    const models = this.cfg()?.models ?? [];
    const cur = this.model();
    const list = cur && !models.includes(cur) ? [cur, ...models] : models;
    const def = this.cfg()?.defaultModel;
    return [
      {
        value: "",
        label: def ? `Deployment default (${def})` : "Deployment default",
      },
      ...list.map((m) => ({ value: m, label: m })),
    ];
  });

  protected readonly backendOptions = computed<PillOption[]>(() => {
    const backends = this.cfg()?.agentBackends ?? [];
    return [
      { value: "", label: "Automatic rotation" },
      ...backends.map((b) => ({
        value: b.id,
        label: b.name ? `${b.name} (${b.id})` : b.id,
      })),
    ];
  });

  constructor() {
    effect(() => {
      const t = this.task();
      untracked(() => {
        this.seedFromTask(t);
        void this.loadSupporting(t.id);
      });
    });
  }

  private seedFromTask(t: TaskDetail): void {
    this.title.set(t.title || "");
    this.description.set(t.description ?? t.body ?? "");
    this.repo.set(t.repo || "");
    this.base.set(t.base || "");
    this.priority.set(priorityOf(t));
    this.model.set(t.model || "");
    this.agentFramework.set(t.agentFramework || "");
    this.agentBackendId.set(t.agentBackendId || "");
    this.networkAccess.set(t.networkAccess || "");
    this.nothingNeededAction.set(t.nothingNeededAction || "close");
    this.autoMerge.set(!!t.autoMerge);
    this.autoApproveProposals.set(!!t.autoApproveProposals);

    if (t.goal) {
      this.goalKind.set(t.goal.kind || "person-closes");
      this.goalRepo.set(t.goal.spec?.repo || t.repo || "");
      this.goalBranch.set(t.goal.spec?.branch || t.base || "main");
      this.goalWorkflow.set(t.goal.spec?.workflow || "nightly");
      this.goalTasks.set(
        t.goal.spec?.tasks?.join(", ") || t.goal.spec?.task || "",
      );
      this.goalOnMet.set(t.goal.on_met || "close");
      this.goalOnLimit.set(t.goal.on_limit || "hold");
    } else {
      this.goalRepo.set(t.repo || "");
      this.goalBranch.set(t.base || "main");
    }
  }

  private async loadSupporting(id: string): Promise<void> {
    try {
      const c = await this.api.config();
      this.cfg.set(c);
    } catch {
      // Keep existing options.
    }
    if (this.can("GET /api/tasks/{id}/context")) {
      try {
        const ctx = await this.api.get<TaskContextView>(
          "GET /api/tasks/{id}/context",
          { id },
        );
        if (this.task().id === id && ctx?.brief !== undefined) {
          this.briefText.set(ctx.brief);
        }
      } catch {
        // Optional context read.
      }
    }
  }

  protected can(route: Parameters<AccessService["can"]>[0]): boolean {
    return this.access.can(route, this.task().repo || undefined);
  }

  protected async saveTask(ev: Event): Promise<void> {
    ev.preventDefault();
    const t = this.task();
    const body: Record<string, unknown> = {
      title: this.title().trim(),
      description: this.description(),
      ...(this.repo() ? { repo: this.repo() } : {}),
      base: this.base().trim(),
      priority: this.priority(),
      model: this.model(),
      agentFramework: this.agentFramework(),
      agentBackendId: this.agentBackendId(),
      networkAccess: this.networkAccess(),
      nothingNeededAction:
        this.nothingNeededAction() === "close"
          ? ""
          : this.nothingNeededAction(),
      autoMerge: this.autoMerge(),
      autoApproveProposals: this.autoApproveProposals(),
    };
    const ok = await this.store.act(
      "edit",
      (id) => this.api.patch("PATCH /api/tasks/{id}", { id }, body),
      t.id,
    );
    if (ok) {
      this.snackbar.show(`Saved #${t.id}`);
      this.acted.emit();
      this.closed.emit();
    }
  }

  protected async saveBrief(): Promise<void> {
    const t = this.task();
    const reason = this.briefReason().trim();
    if (!reason) return;
    const ok = await this.store.act(
      "brief",
      (id) =>
        this.api.post(
          "POST /api/tasks/{id}/brief",
          { id },
          { brief: this.briefText(), reason },
        ),
      t.id,
    );
    if (ok) {
      this.briefReason.set("");
      this.snackbar.show(`Updated brief for #${t.id}`);
      this.acted.emit();
    }
  }

  protected async saveGoal(): Promise<void> {
    const t = this.task();
    const kind = this.goalKind();
    const ids = this.goalTasks()
      .split(/[\s,]+/)
      .map((s) => s.replace(/^#/, "").trim())
      .filter(Boolean);
    const decl: GoalDecl = {
      ...t.goal,
      kind,
      spec: {
        ...(t.goal?.spec || {}),
        ...(kind === "base-green"
          ? {
              repo: this.goalRepo().trim() || t.repo,
              branch: this.goalBranch().trim() || "main",
            }
          : {}),
        ...(kind === "nightly-green"
          ? {
              repo: this.goalRepo().trim() || t.repo,
              workflow: this.goalWorkflow().trim() || "nightly",
            }
          : {}),
        ...(kind === "pr-merged"
          ? {
              ...(ids[0] ? { task: ids[0] } : {}),
              ...(this.goalRepo().trim()
                ? { repo: this.goalRepo().trim() }
                : {}),
              ...(this.goalRepo().trim() && !isNaN(Number(ids[0]))
                ? { number: Number(ids[0]) }
                : {}),
            }
          : {}),
        ...(kind === "tasks-closed" ? { tasks: ids, until: "closed" } : {}),
        ...(kind === "cl-submitted" && ids[0]
          ? { task: ids[0], cl: Number(ids[0]) || undefined }
          : {}),
        ...(kind === "pipeline-healthy" ? { repos: ids } : {}),
      },
      on_met: this.goalOnMet(),
      on_limit: this.goalOnLimit(),
    };
    const ok = await this.store.act(
      "goal-save",
      (id) => this.api.put("PUT /api/tasks/{id}/goal", { id }, decl),
      t.id,
    );
    if (ok) {
      this.snackbar.show(`Set goal on #${t.id}`);
      this.acted.emit();
    }
  }

  protected async clearGoal(): Promise<void> {
    const t = this.task();
    const ok = await this.store.act(
      "goal-clear",
      (id) => this.api.delete("DELETE /api/tasks/{id}/goal", { id }),
      t.id,
    );
    if (ok) {
      this.snackbar.show(`Cleared goal on #${t.id}`);
      this.acted.emit();
    }
  }
}
